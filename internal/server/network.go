package server

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"time"
)

type taskNetworkRequest struct {
	Scope     string   `json:"scope"`
	Hosts     []string `json:"hosts,omitempty"`
	Ports     []int    `json:"ports,omitempty"`
	Minutes   int      `json:"minutes"`
	Unlimited bool     `json:"unlimited,omitempty"`
	Reason    string   `json:"reason"`
}

func (n *taskNetworkRequest) validate() error {
	if n.Scope == "" {
		n.Scope = "hosts"
	}
	if n.Scope != "hosts" && n.Scope != "public" {
		return msg("联网范围只能是 hosts 或 public")
	}
	if n.Scope == "public" && len(n.Hosts) > 0 {
		return msg("所有公网授权不能同时指定主机")
	}
	if n.Scope == "hosts" && len(n.Hosts) == 0 {
		return msg("按主机联网需要填写主机名单")
	}
	if n.Minutes == 0 && !n.Unlimited {
		return msg("不限时授权需要显式 unlimited=true")
	}
	if n.Minutes != 0 && n.Unlimited {
		return msg("unlimited=true 只能与 minutes=0 一起使用")
	}
	if n.Minutes != 0 && n.Minutes != 15 && n.Minutes != 30 && n.Minutes != 60 {
		return msg("联网授权时长只能是 0（本任务不限时）、15、30、60 分钟")
	}
	return nil
}
func (n taskNetworkRequest) wire() map[string]any {
	scope := "NETWORK_SCOPE_HOSTS"
	if n.Scope == "public" {
		scope = "NETWORK_SCOPE_PUBLIC"
	}
	out := map[string]any{"scope": scope, "hosts": n.Hosts, "ports": n.Ports, "minutes": n.Minutes, "reason": n.Reason}
	if n.Unlimited {
		out["unlimited"] = true
	}
	return out
}

// The Runtime authenticates the forwarded token and selects the tenant.
// No tenant, mode or permission is inferred from the workspace environment.
func (s *Server) handleNetworkPolicy(c *gin.Context, id identity) {
	ctx, cancel := ctxTimeout(c.Request, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api.base+"/v1/network-policy", nil)
	if err != nil {
		writeErr(c, http.StatusBadGateway, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+id.token)
	resp, err := s.api.client.Do(req)
	if err != nil {
		writeErr(c, http.StatusBadGateway, msg("暂时无法读取联网策略"))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeMsg(c, resp.StatusCode, "暂时无法读取联网策略")
		return
	}
	var policy struct {
		Allowed           bool     `json:"allowed"`
		Modes             []string `json:"modes"`
		Hosts             []string `json:"hosts"`
		MaxMinutes        int      `json:"max_grant_minutes"`
		Approver          string   `json:"approver"`
		SupportsUnlimited bool     `json:"supports_unlimited"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&policy) != nil {
		writeMsg(c, http.StatusBadGateway, "暂时无法读取联网策略")
		return
	}
	writeJSON(c, http.StatusOK, policy)
}
