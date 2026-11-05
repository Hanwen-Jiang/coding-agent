package server

// “我的 MCP”页的服务端（Runtime 的 N-15，agent-runtime docs/plan/n15-mcp-contract.md）：转调 Runtime 的 McpConnectionService。
// 连接属于本人：建连接（https 地址、可选的网络密钥引用与请求头名）、看工具、逐个打开并定 effect（read 或 write_irreversible）、
// 刷新、删除。网络密钥的值不经这里：连接只引用“网络密钥”页里存的名字。Runtime 没有这个服务（Unimplemented）时列表回
// available false；租户策略不允许时建连接报 mcp_not_allowed，网页照样能列出、删除已有的。

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const mcpService = "/agentruntime.mcp.v1.McpConnectionService/"

var mcpConnName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
var mcpConnID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type mcpToolView struct {
	Tool              string          `json:"tool"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	InputSchema       json.RawMessage `json:"input_schema,omitempty"`
	SchemaDigest      string          `json:"schema_digest"`
	ReadOnlyHint      *bool           `json:"read_only_hint,omitempty"`
	DestructiveHint   *bool           `json:"destructive_hint,omitempty"`
	Enabled           bool            `json:"enabled"`
	Effect            string          `json:"effect,omitempty"`
	TimeoutS          int32           `json:"timeout_s,omitempty"`
	UnavailableReason string          `json:"unavailable_reason,omitempty"`
	ConfirmedAt       string          `json:"confirmed_at,omitempty"`
}

type mcpConnView struct {
	ID            string        `json:"connection_id"`
	Name          string        `json:"name"`
	URL           string        `json:"url"`
	SecretName    string        `json:"secret_name,omitempty"`
	Header        string        `json:"header,omitempty"`
	State         string        `json:"state"`
	ErrorCode     string        `json:"error_code,omitempty"`
	ServerName    string        `json:"server_name,omitempty"`
	ServerVersion string        `json:"server_version,omitempty"`
	CreatedAt     string        `json:"created_at,omitempty"`
	RefreshedAt   string        `json:"refreshed_at,omitempty"`
	Tools         []mcpToolView `json:"tools"`
}

// wireMcpConn 是 Runtime 回的 mcp.v1.McpConnection（connect 的 JSON：驼峰、枚举是名字）。
type wireMcpConn struct {
	ConnectionID string `json:"connectionId"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	Auth         *struct {
		SecretName string `json:"secretName"`
		Header     string `json:"header"`
	} `json:"auth"`
	State         string `json:"state"`
	ErrorCode     string `json:"errorCode"`
	ServerName    string `json:"serverName"`
	ServerVersion string `json:"serverVersion"`
	CreatedAt     string `json:"createdAt"`
	RefreshedAt   string `json:"refreshedAt"`
	Tools         []struct {
		Tool        string `json:"tool"`
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema *struct {
			Value json.RawMessage `json:"value"`
		} `json:"inputSchema"`
		SchemaDigest string `json:"schemaDigest"`
		Annotations  *struct {
			ReadOnlyHint    *bool `json:"readOnlyHint"`
			DestructiveHint *bool `json:"destructiveHint"`
		} `json:"annotations"`
		Enabled           bool   `json:"enabled"`
		Effect            string `json:"effect"`
		TimeoutS          int32  `json:"timeoutS"`
		UnavailableReason string `json:"unavailableReason"`
		ConfirmedAt       string `json:"confirmedAt"`
	} `json:"tools"`
}

func mcpEffect(wire string) string {
	switch wire {
	case "EFFECT_READ":
		return "read"
	case "EFFECT_WRITE_IRREVERSIBLE":
		return "write_irreversible"
	}
	return ""
}

func (w wireMcpConn) view() mcpConnView {
	v := mcpConnView{ID: w.ConnectionID, Name: w.Name, URL: w.URL, ErrorCode: w.ErrorCode, ServerName: w.ServerName, ServerVersion: w.ServerVersion,
		CreatedAt: w.CreatedAt, RefreshedAt: w.RefreshedAt, Tools: []mcpToolView{}}
	// 不认识的状态不当作可用（契约：旧客户端看到不认识的值不能当作 READY）。
	v.State = "error"
	if w.State == "MCP_CONNECTION_STATE_READY" {
		v.State = "ready"
	}
	if w.Auth != nil {
		v.SecretName, v.Header = w.Auth.SecretName, w.Auth.Header
	}
	for _, t := range w.Tools {
		tv := mcpToolView{Tool: t.Tool, Name: t.Name, Description: t.Description, SchemaDigest: t.SchemaDigest, Enabled: t.Enabled,
			Effect: mcpEffect(t.Effect), TimeoutS: t.TimeoutS, UnavailableReason: t.UnavailableReason, ConfirmedAt: t.ConfirmedAt}
		if t.InputSchema != nil {
			tv.InputSchema = t.InputSchema.Value
		}
		if t.Annotations != nil {
			tv.ReadOnlyHint, tv.DestructiveHint = t.Annotations.ReadOnlyHint, t.Annotations.DestructiveHint
		}
		v.Tools = append(v.Tools, tv)
	}
	return v
}

func (s *Server) callMcp(r *http.Request, id identity, method string, req, resp any) error {
	// 建连接与刷新时 Runtime 要连一次远端（至多 15 秒）。
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	return s.api.call(ctx, s.api.client, mcpService+method, id.token, req, resp)
}

func mcpUnavailable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && (apiErr.Code == "unimplemented" || apiErr.Status == http.StatusNotImplemented)
}

func (s *Server) handleListMcp(w http.ResponseWriter, r *http.Request, id identity) {
	var resp struct {
		Connections []wireMcpConn `json:"connections"`
	}
	err := s.callMcp(r, id, "ListMcpConnections", map[string]any{"tenantId": id.TenantID}, &resp)
	if mcpUnavailable(err) {
		writeJSON(w, http.StatusOK, map[string]any{"connections": []mcpConnView{}, "available": false})
		return
	}
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	out := make([]mcpConnView, 0, len(resp.Connections))
	for _, c := range resp.Connections {
		out = append(out, c.view())
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out, "available": true})
}

func (s *Server) handleCreateMcp(w http.ResponseWriter, r *http.Request, id identity) {
	var req struct {
		Name       string `json:"name"`
		URL        string `json:"url"`
		SecretName string `json:"secret_name"`
		Header     string `json:"header"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name, req.URL, req.SecretName, req.Header = strings.TrimSpace(req.Name), strings.TrimSpace(req.URL), strings.TrimSpace(req.SecretName), strings.TrimSpace(req.Header)
	if !mcpConnName.MatchString(req.Name) {
		writeMsg(w, r, http.StatusBadRequest, "连接的名字要小写字母开头，只用小写字母、数字与 _，最多 32 个字符")
		return
	}
	if !strings.HasPrefix(req.URL, "https://") {
		writeMsg(w, r, http.StatusBadRequest, "MCP Server 的地址要以 https:// 开头")
		return
	}
	body := map[string]any{"tenantId": id.TenantID, "name": req.Name, "url": req.URL}
	if req.SecretName != "" {
		body["auth"] = map[string]any{"secretName": req.SecretName, "header": req.Header}
	}
	var resp struct {
		Connection wireMcpConn `json:"connection"`
		Created    bool        `json:"created"`
	}
	if err := s.callMcp(r, id, "CreateMcpConnection", body, &resp); err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": resp.Connection.view(), "created": resp.Created})
}

func checkMcpID(w http.ResponseWriter, r *http.Request) (string, bool) {
	cid := r.PathValue("id")
	if !mcpConnID.MatchString(cid) {
		writeMsg(w, r, http.StatusBadRequest, "MCP 连接不存在")
		return "", false
	}
	return cid, true
}

func (s *Server) handleGetMcp(w http.ResponseWriter, r *http.Request, id identity) {
	cid, ok := checkMcpID(w, r)
	if !ok {
		return
	}
	var resp struct {
		Connection wireMcpConn `json:"connection"`
	}
	if err := s.callMcp(r, id, "GetMcpConnection", map[string]any{"tenantId": id.TenantID, "connectionId": cid}, &resp); err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": resp.Connection.view()})
}

func (s *Server) handleRefreshMcp(w http.ResponseWriter, r *http.Request, id identity) {
	cid, ok := checkMcpID(w, r)
	if !ok {
		return
	}
	var resp struct {
		Connection wireMcpConn `json:"connection"`
		Added      []string    `json:"added"`
		Disabled   []string    `json:"disabled"`
		Removed    []string    `json:"removed"`
	}
	if err := s.callMcp(r, id, "RefreshMcpTools", map[string]any{"tenantId": id.TenantID, "connectionId": cid}, &resp); err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": resp.Connection.view(), "added": nonNil(resp.Added), "disabled": nonNil(resp.Disabled),
		"removed": nonNil(resp.Removed)})
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Server) handleConfigureMcp(w http.ResponseWriter, r *http.Request, id identity) {
	cid, ok := checkMcpID(w, r)
	if !ok {
		return
	}
	var req struct {
		Tools []struct {
			Tool         string `json:"tool"`
			Enabled      bool   `json:"enabled"`
			Effect       string `json:"effect"`
			TimeoutS     int32  `json:"timeout_s"`
			SchemaDigest string `json:"schema_digest"`
		} `json:"tools"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Tools) == 0 {
		writeMsg(w, r, http.StatusBadRequest, "没有要改的工具")
		return
	}
	tools := make([]map[string]any, 0, len(req.Tools))
	for _, t := range req.Tools {
		setting := map[string]any{"tool": t.Tool, "enabled": t.Enabled, "schemaDigest": t.SchemaDigest}
		if t.Enabled {
			switch t.Effect {
			case "read":
				setting["effect"] = "EFFECT_READ"
			case "write_irreversible":
				setting["effect"] = "EFFECT_WRITE_IRREVERSIBLE"
			default:
				writeMsg(w, r, http.StatusBadRequest, "打开工具时要选只读或不可逆写")
				return
			}
			if t.TimeoutS != 0 {
				setting["timeoutS"] = t.TimeoutS
			}
		}
		tools = append(tools, setting)
	}
	var resp struct {
		Connection wireMcpConn `json:"connection"`
	}
	if err := s.callMcp(r, id, "ConfigureMcpTools", map[string]any{"tenantId": id.TenantID, "connectionId": cid, "tools": tools}, &resp); err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": resp.Connection.view()})
}

func (s *Server) handleDeleteMcp(w http.ResponseWriter, r *http.Request, id identity) {
	cid, ok := checkMcpID(w, r)
	if !ok {
		return
	}
	var resp struct {
		Deleted bool `json:"deleted"`
	}
	if err := s.callMcp(r, id, "DeleteMcpConnection", map[string]any{"tenantId": id.TenantID, "connectionId": cid}, &resp); err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": resp.Deleted})
}
