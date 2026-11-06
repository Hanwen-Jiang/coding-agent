package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

type taskNetworkRequest struct {
	Scope   string   `json:"scope"`
	Hosts   []string `json:"hosts,omitempty"`
	Ports   []int    `json:"ports,omitempty"`
	Minutes int      `json:"minutes"`
	Reason  string   `json:"reason"`
}

func (n *taskNetworkRequest) validate() error {
	if n.Scope == "" {
		n.Scope = "hosts"
	}
	if n.Scope != "hosts" && n.Scope != "public" {
		return errors.New("scope must be hosts or public")
	}
	if n.Scope == "public" && len(n.Hosts) > 0 {
		return errors.New("public scope requires empty hosts")
	}
	if n.Scope == "hosts" && len(n.Hosts) == 0 {
		return errors.New("hosts scope requires hosts")
	}
	if n.Minutes != 15 && n.Minutes != 30 && n.Minutes != 60 {
		return errors.New("network duration must be 15, 30 or 60 minutes")
	}
	return nil
}
func (n taskNetworkRequest) wire() map[string]any {
	scope := "NETWORK_SCOPE_HOSTS"
	if n.Scope == "public" {
		scope = "NETWORK_SCOPE_PUBLIC"
	}
	return map[string]any{"scope": scope, "hosts": n.Hosts, "ports": n.Ports, "minutes": n.Minutes, "reason": n.Reason}
}

// The Runtime authenticates the forwarded token and selects the tenant.
// No tenant, mode or permission is inferred from the workspace environment.
func (s *Server) handleNetworkPolicy(w http.ResponseWriter, r *http.Request, id identity) {
	ctx, cancel := ctxTimeout(r, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api.base+"/v1/network-policy", nil)
	if err != nil {
		writeErr(w, r, http.StatusBadGateway, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+id.token)
	resp, err := s.api.client.Do(req)
	if err != nil {
		writeErr(w, r, http.StatusBadGateway, errors.New("network policy unavailable"))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeMsg(w, r, resp.StatusCode, "network policy unavailable")
		return
	}
	var policy struct {
		Allowed    bool     `json:"allowed"`
		Modes      []string `json:"modes"`
		Hosts      []string `json:"hosts"`
		MaxMinutes int      `json:"max_grant_minutes"`
		Approver   string   `json:"approver"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&policy) != nil {
		writeMsg(w, r, http.StatusBadGateway, "network policy unavailable")
		return
	}
	writeJSON(w, http.StatusOK, policy)
}
