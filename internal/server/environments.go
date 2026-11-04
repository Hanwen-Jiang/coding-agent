package server

import (
	"errors"
	"fmt"
	"net"
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
	// .env 的上限与 Runtime 的 SetWorkspaceEnvironment 一致（task-service 的 workspaceapi.checkDotEnv），否则开会话时才报 env_too_large。
	maxEnvironmentDotEnvBytes = 16 << 10
	maxEnvironmentDotEnvVars  = 64
	maxEnvironmentHosts       = 100
	maxEnvironmentHostBytes   = 253
	maxEnvironmentSecrets     = 32
	maxEnvironmentSecretBytes = 4 << 10
)

const onlyMePrivacy = "only_me"

// 联网模式（人工验收 Q29）：禁止、受限列表、完全开放。默认禁止（同现在断网的沙箱）。
const (
	networkDeny      = "deny"
	networkAllowlist = "allowlist"
	networkOpen      = "open"
)

var environmentVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// hostLabel 是域名里的一段（只收 ASCII；国际化域名写成 punycode）。
var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// environmentInput is deliberately plain text. Secret references and runtime
// lifecycle fields are not accepted by this API; they belong to Runtime.
type environmentInput struct {
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	Privacy      string             `json:"privacy"`
	SetupScript  string             `json:"setup_script"`
	StartCommand string             `json:"start_command"`
	Env          string             `json:"env"`
	Network      EnvironmentNetwork `json:"network"`
	// Secrets 是想要的全部密钥（只写）：给了值就设成它，没给值就保留原来的值，不在列表里的删掉。
	// 不带这个字段（nil）时密钥不动。
	Secrets *[]secretInput `json:"secrets"`
}

type secretInput struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// secretView 是密钥给浏览器看的样子：只有名字。
type secretView struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

// environmentJSON 是环境配置给浏览器的样子：Secrets 换成只有名字的 secretView（外层的字段盖住嵌入的 Environment.Secrets），
// 加密后的值也不出服务端。
type environmentJSON struct {
	Environment
	Secrets []secretView `json:"secrets"`
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
	network, err := normalizeNetwork(in.Network)
	if err != nil {
		return environmentInput{}, err
	}
	in.Network = network
	if in.Secrets != nil {
		secrets, err := normalizeSecrets(*in.Secrets, in.Env)
		if err != nil {
			return environmentInput{}, err
		}
		in.Secrets = &secrets
	}
	return in, nil
}

// normalizeSecrets 校验密钥的名字与值（值不进任何说明）：名字同环境变量的规则、不重复、不与 .env 里的变量同名，值有上限、不含 NUL。
// 没给值的名字表示保留原来的值，由 mergeSecrets 核对原来有没有。
func normalizeSecrets(in []secretInput, dotenv string) ([]secretInput, error) {
	if len(in) > maxEnvironmentSecrets {
		return nil, msg("密钥最多 %d 个", maxEnvironmentSecrets)
	}
	plain := map[string]bool{}
	for _, raw := range strings.Split(dotenv, "\n") {
		if key, _, ok := strings.Cut(strings.TrimSpace(raw), "="); ok {
			plain[key] = true
		}
	}
	out := make([]secretInput, 0, len(in))
	seen := map[string]bool{}
	for _, sec := range in {
		name := strings.TrimSpace(sec.Name)
		switch {
		case !environmentVarName.MatchString(name) || len(name) > 128:
			return nil, msg("密钥名 %s 不合法", name)
		case seen[name]:
			return nil, msg("密钥 %s 重复", name)
		case plain[name]:
			return nil, msg("密钥 %s 与 .env 里的变量同名", name)
		case len(sec.Value) > maxEnvironmentSecretBytes:
			return nil, msg("密钥 %s 的值不超过 %d 个字节", name, maxEnvironmentSecretBytes)
		case strings.ContainsRune(sec.Value, '\x00'):
			return nil, msg("密钥 %s 的值包含非法字符", name)
		}
		seen[name] = true
		out = append(out, secretInput{Name: name, Value: sec.Value})
	}
	return out, nil
}

// mergeSecrets 按请求算出新的密钥：给了值的加密存放，没给值的沿用原来的（原来没有就报错），不在列表里的删掉。
func (s *Server) mergeSecrets(tenant, user, envID string, existing []EnvironmentSecret, in []secretInput) ([]EnvironmentSecret, error) {
	var out []EnvironmentSecret
	for _, sec := range in {
		if sec.Value == "" {
			i := slices.IndexFunc(existing, func(e EnvironmentSecret) bool { return e.Name == sec.Name })
			if i < 0 {
				return nil, msg("密钥 %s 没有值", sec.Name)
			}
			out = append(out, existing[i])
			continue
		}
		sealed, err := s.sealSecret(tenant, user, envID, sec.Name, sec.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, EnvironmentSecret{Name: sec.Name, Sealed: sealed})
	}
	return out, nil
}

// normalizeNetwork 校验联网需求：模式只能是三档之一；受限列表要有域名，别的模式不能带域名（不静默丢掉用户填的东西）。
// 这里只挡写法不对的；能不能用（open 要租户策略打开、域名不能落在内网）由 Runtime 在发授权时按租户策略核对。
func normalizeNetwork(n EnvironmentNetwork) (EnvironmentNetwork, error) {
	n.Mode = strings.ToLower(strings.TrimSpace(n.Mode))
	if n.Mode == "" {
		n.Mode = networkDeny
	}
	if !slices.Contains([]string{networkDeny, networkAllowlist, networkOpen}, n.Mode) {
		return EnvironmentNetwork{}, msg("联网模式只能是 deny、allowlist 或 open")
	}
	var hosts []string
	for _, raw := range n.AllowedHosts {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		host, err := normalizeHost(raw)
		if err != nil {
			return EnvironmentNetwork{}, err
		}
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	switch {
	case n.Mode != networkAllowlist && len(hosts) > 0:
		return EnvironmentNetwork{}, msg("只有受限列表模式才填允许的域名")
	case n.Mode == networkAllowlist && len(hosts) == 0:
		return EnvironmentNetwork{}, msg("受限列表至少要有一个域名")
	case len(hosts) > maxEnvironmentHosts:
		return EnvironmentNetwork{}, msg("允许的域名最多 %d 个", maxEnvironmentHosts)
	}
	n.AllowedHosts = hosts
	return n, nil
}

// normalizeHost 把一个允许的域名整理成小写、去掉末尾的点；*.example.com 匹配子域名（Q30）。只收主机名：不带协议、端口、路径，
// 不收 IP 地址、裸的 * 与单段的名字（localhost 这类）。
func normalizeHost(raw string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	name := strings.TrimPrefix(host, "*.")
	if strings.ContainsAny(name, "/:@?# \t*") {
		return "", msg("允许的域名 %s 只写主机名，不带协议、端口、路径或通配符（子域名写 *.example.com）", raw)
	}
	if net.ParseIP(name) != nil {
		return "", msg("允许的域名不能是 IP 地址：%s", raw)
	}
	labels := strings.Split(name, ".")
	if len(host) > maxEnvironmentHostBytes || len(labels) < 2 {
		return "", msg("允许的域名 %s 不是公网域名", raw)
	}
	for _, label := range labels {
		if !hostLabel.MatchString(label) {
			return "", msg("允许的域名 %s 不是公网域名", raw)
		}
	}
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", msg("允许的域名 %s 不是公网域名", raw)
	}
	return host, nil
}

// validateDotEnv checks the ordinary .env form without interpreting shell
// syntax. Values may contain '='; secrets are rejected by name so users do not
// accidentally persist credentials in a config visible to the environment's
// users. Secret material must be provisioned through Runtime instead.
func validateDotEnv(dotenv string) error {
	if len(dotenv) > maxEnvironmentDotEnvBytes {
		return msg("环境变量不超过 %d 个字节", maxEnvironmentDotEnvBytes)
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
			return msg(".env 第 %d 行的 %s 可能是机密，请放到“密钥”里", lineNo+1, key)
		}
		if strings.ContainsRune(value, '\x00') {
			return msg(".env 第 %d 行包含非法字符", lineNo+1)
		}
	}
	if len(seen) > maxEnvironmentDotEnvVars {
		return msg("环境变量最多 %d 个", maxEnvironmentDotEnvVars)
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

func environmentView(env Environment) environmentJSON {
	// Keep the response as configuration only; DeletedAt is omitted for active
	// records by the handlers and no runtime status is inferred here.
	// Records saved before network intent existed read as the default (deny).
	if env.Network.Mode == "" {
		env.Network.Mode = networkDeny
	}
	out := environmentJSON{Environment: env, Secrets: []secretView{}}
	for _, sec := range env.Secrets {
		out.Secrets = append(out.Secrets, secretView{Name: sec.Name, Set: sec.Sealed != ""})
	}
	return out
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
	out := []environmentJSON{}
	for _, env := range envs {
		out = append(out, environmentView(env))
	}
	writeJSON(w, http.StatusOK, out)
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
		SetupScript: in.SetupScript, StartCommand: in.StartCommand, Env: in.Env, Network: in.Network,
		Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: id.UserID,
	}
	if in.Secrets != nil {
		if env.Secrets, err = s.mergeSecrets(id.TenantID, id.UserID, env.ID, nil, *in.Secrets); err != nil {
			writeMsg(w, r, http.StatusBadRequest, "环境配置不合法：%v", err)
			return
		}
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
			if in.Secrets != nil {
				secrets, err := s.mergeSecrets(id.TenantID, id.UserID, all[i].ID, all[i].Secrets, *in.Secrets)
				if err != nil {
					return nil, err
				}
				all[i].Secrets = secrets
			} else {
				// 不带 secrets：密钥不动，但仍不能与新的 .env 同名。
				keep := make([]secretInput, 0, len(all[i].Secrets))
				for _, sec := range all[i].Secrets {
					keep = append(keep, secretInput{Name: sec.Name})
				}
				if _, err := normalizeSecrets(keep, in.Env); err != nil {
					return nil, err
				}
			}
			all[i].Name, all[i].Description, all[i].Privacy = in.Name, in.Description, in.Privacy
			all[i].SetupScript, all[i].StartCommand, all[i].Env, all[i].Network = in.SetupScript, in.StartCommand, in.Env, in.Network
			all[i].Version++
			all[i].UpdatedAt = time.Now().UTC()
			return all, nil
		}
		return nil, fmt.Errorf("环境不存在")
	})
	// 密钥的校验要看原来的值，在锁里做：它的错误是 *Msg（同名、不存在是普通的 error）。
	if bad := (*Msg)(nil); errors.As(err, &bad) {
		writeMsg(w, r, http.StatusBadRequest, "环境配置不合法：%v", bad)
		return
	}
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
