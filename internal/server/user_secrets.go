package server

// 网络密钥页的服务端（WEB-SEC；Runtime 的 environment-config.md 第 4.4 节）：转调 Runtime 的 UserSecretService（SEC-API）。
// 密钥属于（租户，用户），存在 Runtime 的 SecretStore 里，出网网关只对环境里配了网络密钥的主机现读、注入请求头。coding-web 不存值，
// 响应、错误信息与日志里都没有值。
//
// D-23 第二阶段（选定首个真实服务与客户端、环境里引用密钥）之前不算已支持：默认关着（Config.NetworkSecrets，CODING_NETWORK_SECRETS=1
// 才打开），关着时三个接口都 404。

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const userSecretService = "/agentruntime.workspace.v1.UserSecretService/"

// 与 SEC-API 契约一致：名字最多 128 个字母、数字、.、_、-；值不能为空，UTF-8 最多 8 KiB，会放进请求头，所以不能有控制字符
// （水平制表符除外）。
var secretName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

const maxSecretBytes = 8 << 10

type userSecret struct {
	Name      string `json:"name"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// wireSecret 是 Runtime 回的 UserSecret（connect 的 JSON：驼峰、时间是 RFC 3339）。
type wireSecret struct {
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func (w wireSecret) view() userSecret {
	return userSecret{Name: w.Name, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
}

// callSecrets 带这个用户现签的令牌调 UserSecretService。
func (s *Server) callSecrets(r *http.Request, id identity, method string, req, resp any) error {
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	return s.api.call(ctx, s.api.client, userSecretService+method, id.token, req, resp)
}

// secretsEnabled 在关着时回 404（不去问 Runtime）。
func (s *Server) secretsEnabled(c *gin.Context) bool {
	if !s.cfg.NetworkSecrets {
		writeMsg(c, http.StatusNotFound, "网络密钥还没有打开")
		return false
	}
	return true
}

func (s *Server) handleListSecrets(c *gin.Context, id identity) {
	if !s.secretsEnabled(c) {
		return
	}
	var resp struct {
		Secrets []wireSecret `json:"secrets"`
	}
	if err := s.callSecrets(c.Request, id, "ListUserSecrets", map[string]string{"tenantId": id.TenantID}, &resp); err != nil {
		writeAPIError(c, err)
		return
	}
	out := make([]userSecret, 0, len(resp.Secrets))
	for _, x := range resp.Secrets {
		out = append(out, x.view())
	}
	writeJSON(c, http.StatusOK, map[string]any{"secrets": out})
}

func (s *Server) handlePutSecret(c *gin.Context, id identity) {
	if !s.secretsEnabled(c) {
		return
	}
	name := c.Param("name")
	if !secretName.MatchString(name) {
		writeMsg(c, http.StatusBadRequest, "密钥名只能用字母、数字与 . _ -，最多 128 个字符")
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if !readJSON(c, &req) {
		return
	}
	if problem := secretValueProblem(req.Value); problem != nil {
		writeErr(c, http.StatusBadRequest, problem)
		return
	}
	var resp struct {
		Secret  wireSecret `json:"secret"`
		Created bool       `json:"created"`
	}
	if err := s.callSecrets(c.Request, id, "PutUserSecret", map[string]string{"tenantId": id.TenantID, "name": name, "value": req.Value}, &resp); err != nil {
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"secret": resp.Secret.view(), "created": resp.Created})
}

// secretValueProblem 是值不合规的原因（不带值）；合规时为 nil。
func secretValueProblem(v string) *Msg {
	switch {
	case v == "":
		return msg("密钥的值不能为空")
	case len(v) > maxSecretBytes:
		return msg("密钥的值最多 8 KiB")
	case !utf8.ValidString(v):
		return msg("密钥的值要是 UTF-8")
	case strings.IndexFunc(v, func(r rune) bool { return r != '\t' && (r < 0x20 || r == 0x7f) }) >= 0:
		return msg("密钥的值不能有换行之类的控制字符（会放进请求头）")
	}
	return nil
}

func (s *Server) handleDeleteSecret(c *gin.Context, id identity) {
	if !s.secretsEnabled(c) {
		return
	}
	name := c.Param("name")
	if !secretName.MatchString(name) {
		writeMsg(c, http.StatusBadRequest, "密钥名只能用字母、数字与 . _ -，最多 128 个字符")
		return
	}
	var resp struct {
		Deleted bool `json:"deleted"`
	}
	if err := s.callSecrets(c.Request, id, "DeleteUserSecret", map[string]string{"tenantId": id.TenantID, "name": name}, &resp); err != nil {
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"deleted": resp.Deleted})
}
