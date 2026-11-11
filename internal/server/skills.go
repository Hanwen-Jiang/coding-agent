package server

// “我的 Skill”页的服务端（WEB-SKILLS；Runtime 的 N-14，docs/plan/n14-skill-contract.md）：转调 Runtime 的 SkillService。
// Skill 存在 Runtime 的 Registry 里：平台层、团队（租户）层由运维发布，这里只读；用户层是本人的，可以发布与收回。
// Runtime 按 Task 固定版本：发布新版本不影响已经在跑的任务。Runtime 没接 Registry（Unimplemented、skills_unavailable）时
// 列表回 available false，网页写明这个部署还没有 Skill。

import (
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const skillService = "/agentruntime.skill.v1.SkillService/"

// 与 N-14 契约一致：小写字母开头，小写字母、数字、_ 与 -，最多 64 个字符。
var skillName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type skillFile struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type,omitempty"`
	Content   string `json:"content"`
}

// skillView 是给网页看的 Skill。列表不带 instructions 与 files。
type skillView struct {
	Name         string      `json:"name"`
	Version      string      `json:"version"`
	Scope        string      `json:"scope"`
	Description  string      `json:"description"`
	Owner        string      `json:"owner,omitempty"`
	Shadowed     bool        `json:"shadowed,omitempty"`
	PublishedAt  string      `json:"published_at,omitempty"`
	Instructions string      `json:"instructions,omitempty"`
	Files        []skillFile `json:"files,omitempty"`
}

// wireSkill 是 Runtime 回的 registry.v1.Skill（connect 的 JSON：驼峰、枚举是名字）。
type wireSkill struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	Shadowed    bool   `json:"shadowed"`
	PublishedAt string `json:"publishedAt"`
	Document    struct {
		Value struct {
			Description  string      `json:"description"`
			Instructions string      `json:"instructions"`
			Files        []skillFile `json:"files"`
		} `json:"value"`
	} `json:"document"`
}

func (w wireSkill) view(withBody bool) skillView {
	scope := strings.ToLower(strings.TrimPrefix(w.Scope, "SKILL_SCOPE_"))
	v := skillView{Name: w.Name, Version: w.Version, Scope: scope, Description: w.Description, Owner: w.Owner, Shadowed: w.Shadowed, PublishedAt: w.PublishedAt}
	if withBody {
		v.Instructions, v.Files = w.Document.Value.Instructions, w.Document.Value.Files
		if v.Description == "" {
			v.Description = w.Document.Value.Description
		}
	}
	return v
}

// callSkills 带这个用户现签的令牌调 SkillService。
func (s *Server) callSkills(r *http.Request, id identity, method string, req, resp any) error {
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	return s.api.call(ctx, s.api.client, skillService+method, id.token, req, resp)
}

// skillsUnavailable：Runtime 没有 SkillService（没接 Registry 的部署，或还没有这个服务的旧版本）。
func skillsUnavailable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && (apiErr.Reason == "skills_unavailable" || apiErr.Code == "unimplemented" || apiErr.Status == http.StatusNotImplemented)
}

func (s *Server) handleListSkills(c *gin.Context, id identity) {
	var resp struct {
		Skills []wireSkill `json:"skills"`
	}
	err := s.callSkills(c.Request, id, "ListSkills", map[string]any{"tenantId": id.TenantID, "pageSize": 500}, &resp)
	if skillsUnavailable(err) {
		writeJSON(c, http.StatusOK, map[string]any{"skills": []skillView{}, "available": false})
		return
	}
	if err != nil {
		writeAPIError(c, err)
		return
	}
	out := make([]skillView, 0, len(resp.Skills))
	for _, x := range resp.Skills {
		out = append(out, x.view(false))
	}
	writeJSON(c, http.StatusOK, map[string]any{"skills": out, "available": true})
}

// checkSkillName 不合规时写 400 并返回 false。
func checkSkillName(c *gin.Context, name string) bool {
	if !skillName.MatchString(name) {
		writeMsg(c, http.StatusBadRequest, "Skill 的名字要小写字母开头，只用小写字母、数字、_ 与 -，最多 64 个字符")
		return false
	}
	return true
}

func (s *Server) handleGetSkill(c *gin.Context, id identity) {
	name := c.Param("name")
	if !checkSkillName(c, name) {
		return
	}
	var resp struct {
		Skill wireSkill `json:"skill"`
	}
	req := map[string]any{"tenantId": id.TenantID, "name": name, "version": c.Query("version")}
	if err := s.callSkills(c.Request, id, "GetSkill", req, &resp); err != nil {
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"skill": resp.Skill.view(true)})
}

func (s *Server) handlePutSkill(c *gin.Context, id identity) {
	name := c.Param("name")
	if !checkSkillName(c, name) {
		return
	}
	var req struct {
		Description  string      `json:"description"`
		Instructions string      `json:"instructions"`
		Files        []skillFile `json:"files"`
	}
	if !readJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.Description) == "" || strings.TrimSpace(req.Instructions) == "" {
		writeMsg(c, http.StatusBadRequest, "Skill 要写说明与做法")
		return
	}
	doc := map[string]any{"description": req.Description, "instructions": req.Instructions}
	if len(req.Files) > 0 {
		doc["files"] = req.Files
	}
	var resp struct {
		Skill      wireSkill `json:"skill"`
		Created    bool      `json:"created"`
		NewVersion bool      `json:"newVersion"`
	}
	body := map[string]any{"tenantId": id.TenantID, "name": name, "document": map[string]any{"value": doc}}
	if err := s.callSkills(c.Request, id, "PutSkill", body, &resp); err != nil {
		if skillsUnavailable(err) {
			writeMsg(c, http.StatusNotImplemented, "这个部署还没有 Skill")
			return
		}
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"skill": resp.Skill.view(false), "created": resp.Created, "new_version": resp.NewVersion})
}

func (s *Server) handleRetireSkill(c *gin.Context, id identity) {
	name := c.Param("name")
	if !checkSkillName(c, name) {
		return
	}
	var resp struct {
		Retired bool   `json:"retired"`
		Version string `json:"version"`
	}
	if err := s.callSkills(c.Request, id, "RetireSkill", map[string]any{"tenantId": id.TenantID, "name": name}, &resp); err != nil {
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"retired": resp.Retired, "version": resp.Version})
}
