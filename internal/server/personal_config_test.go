package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
)

type personalTransport func(*http.Request) (*http.Response, error)

func (f personalTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPersonalConfigurationUsesAuthenticatedOwnerAndValidatesBeforeForwarding(t *testing.T) {
	api := NewTaskAPI("http://runtime.invalid")
	calls := 0
	api.client.Transport = personalTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["tenantId"] != "t1" || r.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatalf("identity: %v %v", body, r.Header)
		}
		if _, ok := body["userId"]; ok {
			t.Fatal("caller supplied an owner")
		}
		if r.URL.Path != personalConfigService+"PutPersonalConfig" {
			t.Fatal(r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"config":{"version":"user/v2","digest":"digest"},"newVersion":true}`)), Header: make(http.Header)}, nil
	})
	s := &Server{api: api}
	id := identity{Identity: ingress.Identity{TenantID: "t1", UserID: "alice"}, token: "user-token"}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"tenantId":"foreign","userId":"bob","document":{"instructions":"Use Chinese","files":[{"path":".config/ruff.toml","content":"line-length=100"}]},"expected_version":"user/v1"}`, 200},
		{`{"document":{"files":[{"path":"../escape","content":"oops"}]}}`, 400},
		{`{"document":{"files":[{"path":".npmrc","content":"_authToken=plain-secret"}]}}`, 400},
		{`{"document":{"network":"open"}}`, 400},
	} {
		w := httptest.NewRecorder()
		s.handlePutPersonalConfig(testContext(w, httptest.NewRequest("PUT", "/api/personal-config", strings.NewReader(tc.body))), id)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid packages reached Runtime: %d", calls)
	}
}

func TestPersonalSelectionPreservesExplicitEmptyAndRejectsInvalidReferences(t *testing.T) {
	for _, raw := range []string{`{}`, `{"skill_names":[],"mcp_connection_ids":["mc1"]}`} {
		var selection personalSelection
		if err := json.Unmarshal([]byte(raw), &selection); err != nil {
			t.Fatal(err)
		}
		if err := selection.validate(); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(selection)
		if string(out) != raw {
			t.Fatalf("selection changed: %s -> %s", raw, out)
		}
	}
	for _, refs := range [][]string{{"one", "one"}, {"bad name"}, {"../escape"}} {
		if err := (&personalSelection{Skills: &refs}).validate(); err == nil {
			t.Fatalf("accepted %v", refs)
		}
	}
}
