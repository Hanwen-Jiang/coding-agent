package server

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	maxEnvironmentNameBytes        = 80
	maxEnvironmentDescriptionBytes = 4 << 10
	maxEnvironmentScriptBytes      = 64 << 10
)

const onlyMePrivacy = "only_me"

var environmentVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// environmentInput is deliberately plain text. Secret references and runtime
// lifecycle fields are not accepted by this API; they belong to Runtime.
type environmentInput struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Privacy      string `json:"privacy"`
	SetupScript  string `json:"setup_script"`
	StartCommand string `json:"start_command"`
	Env          string `json:"env"`
}

func normalizeEnvironmentInput(in environmentInput) (environmentInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Privacy = strings.TrimSpace(in.Privacy)
	if in.Privacy == "" {
		in.Privacy = onlyMePrivacy
	}
	if in.Name == "" || len(in.Name) > maxEnvironmentNameBytes {
		return environmentInput{}, msg("环境名不能为空且不超过 %d 个字节", maxEnvironmentNameBytes)
	}
	if len(in.Description) > maxEnvironmentDescriptionBytes {
		return environmentInput{}, msg("环境描述不超过 %d 个字节", maxEnvironmentDescriptionBytes)
	}
	if in.Privacy != onlyMePrivacy {
		return environmentInput{}, msg("隐私目前只支持 %q", onlyMePrivacy)
	}
	if len(in.SetupScript) > maxEnvironmentScriptBytes {
		return environmentInput{}, msg("安装脚本不超过 %d 个字节", maxEnvironmentScriptBytes)
	}
	if len(in.StartCommand) > maxEnvironmentScriptBytes {
		return environmentInput{}, msg("启动命令不超过 %d 个字节", maxEnvironmentScriptBytes)
	}
	if err := validateDotEnv(in.Env); err != nil {
		return environmentInput{}, err
	}
	return in, nil
}

// validateDotEnv checks the ordinary .env form without interpreting shell
// syntax. Values may contain '='; secrets are rejected by name so users do not
// accidentally persist credentials in a config visible to the environment's
// users. Secret material must be provisioned through Runtime instead.
func validateDotEnv(dotenv string) error {
	if len(dotenv) > maxEnvironmentScriptBytes {
		return msg("环境变量不超过 %d 个字节", maxEnvironmentScriptBytes)
	}
	seen := map[string]bool{}
	for lineNo, raw := range strings.Split(dotenv, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !environmentVarName.MatchString(key) {
			return msg(".env 第 %d 行应为 KEY=value", lineNo+1)
		}
		if seen[key] {
			return msg(".env 第 %d 行重复定义 %s", lineNo+1, key)
		}
		seen[key] = true
		if looksLikeSecretName(key) {
			return msg(".env 第 %d 行的 %s 可能是机密，请使用 Runtime 网络密钥", lineNo+1, key)
		}
		if strings.ContainsRune(value, '\x00') {
			return msg(".env 第 %d 行包含非法字符", lineNo+1)
		}
	}
	return nil
}

func looksLikeSecretName(key string) bool {
	for _, part := range strings.Split(strings.ToUpper(key), "_") {
		switch part {
		case "SECRET", "TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL", "CREDENTIALS", "APIKEY", "PRIVATEKEY":
			return true
		}
	}
	return false
}

func environmentView(env Environment) Environment {
	// Keep the response as configuration only; DeletedAt is omitted for active
	// records by the handlers and no runtime status is inferred here.
	return env
}

func (s *Server) environmentForUser(tenant, user, id string) (Environment, bool, error) {
	env, ok, err := s.store.Environment(tenant, id)
	if err != nil || !ok || env.DeletedAt != nil || env.CreatedBy != user {
		return Environment{}, false, err
	}
	return env, true, nil
}

func (s *Server) handleListEnvironments(w http.ResponseWriter, r *http.Request, id identity) {
	envs, err := s.store.Environments(id.TenantID)
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	envs = slices.DeleteFunc(envs, func(env Environment) bool {
		return env.DeletedAt != nil || env.CreatedBy != id.UserID
	})
	slices.SortFunc(envs, func(a, b Environment) int { return b.CreatedAt.Compare(a.CreatedAt) })
	for i := range envs {
		envs[i] = environmentView(envs[i])
	}
	if envs == nil {
		envs = []Environment{}
	}
	writeJSON(w, http.StatusOK, envs)
}

func (s *Server) handleCreateEnvironment(w http.ResponseWriter, r *http.Request, id identity) {
	var req environmentInput
	if !readJSON(w, r, &req) {
		return
	}
	in, err := normalizeEnvironmentInput(req)
	if err != nil {
		writeMsg(w, r, http.StatusBadRequest, "环境配置不合法：%v", err)
		return
	}
	now := time.Now().UTC()
	env := Environment{
		ID: newEnvironmentID(in.Name), Name: in.Name, Description: in.Description, Privacy: in.Privacy,
		SetupScript: in.SetupScript, StartCommand: in.StartCommand, Env: in.Env,
		CreatedAt: now, CreatedBy: id.UserID,
	}
	err = s.store.UpdateEnvironments(id.TenantID, func(all []Environment) ([]Environment, error) {
		for _, existing := range all {
			if existing.DeletedAt == nil && existing.CreatedBy == id.UserID && strings.EqualFold(existing.Name, env.Name) {
				return nil, fmt.Errorf("你已有同名环境")
			}
		}
		return append(all, env), nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "同名") {
			writeMsg(w, r, http.StatusConflict, "你已有同名环境")
		} else {
			writeErr(w, r, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, environmentView(env))
}

func (s *Server) handleGetEnvironment(w http.ResponseWriter, r *http.Request, id identity) {
	env, ok, err := s.environmentForUser(id.TenantID, id.UserID, r.PathValue("environment"))
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeMsg(w, r, http.StatusNotFound, "环境不存在")
		return
	}
	writeJSON(w, http.StatusOK, environmentView(env))
}

func (s *Server) handleUpdateEnvironment(w http.ResponseWriter, r *http.Request, id identity) {
	env, ok, err := s.environmentForUser(id.TenantID, id.UserID, r.PathValue("environment"))
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeMsg(w, r, http.StatusNotFound, "环境不存在")
		return
	}
	var req environmentInput
	if !readJSON(w, r, &req) {
		return
	}
	in, err := normalizeEnvironmentInput(req)
	if err != nil {
		writeMsg(w, r, http.StatusBadRequest, "环境配置不合法：%v", err)
		return
	}
	err = s.store.UpdateEnvironments(id.TenantID, func(all []Environment) ([]Environment, error) {
		for i := range all {
			if all[i].ID != env.ID {
				continue
			}
			for j, existing := range all {
				if j != i && existing.DeletedAt == nil && existing.CreatedBy == id.UserID && strings.EqualFold(existing.Name, in.Name) {
					return nil, fmt.Errorf("你已有同名环境")
				}
			}
			all[i].Name, all[i].Description, all[i].Privacy = in.Name, in.Description, in.Privacy
			all[i].SetupScript, all[i].StartCommand, all[i].Env = in.SetupScript, in.StartCommand, in.Env
			return all, nil
		}
		return nil, fmt.Errorf("环境不存在")
	})
	if err != nil {
		if strings.Contains(err.Error(), "同名") {
			writeMsg(w, r, http.StatusConflict, "你已有同名环境")
		} else if strings.Contains(err.Error(), "不存在") {
			writeMsg(w, r, http.StatusNotFound, "环境不存在")
		} else {
			writeErr(w, r, http.StatusInternalServerError, err)
		}
		return
	}
	env, _, _ = s.store.Environment(id.TenantID, env.ID)
	writeJSON(w, http.StatusOK, environmentView(env))
}

func (s *Server) handleDeleteEnvironment(w http.ResponseWriter, r *http.Request, id identity) {
	env, ok, err := s.environmentForUser(id.TenantID, id.UserID, r.PathValue("environment"))
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeMsg(w, r, http.StatusNotFound, "环境不存在")
		return
	}
	if err := s.store.DeleteEnvironment(id.TenantID, env.ID, time.Now().UTC()); err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
