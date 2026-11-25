package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentruntime/coding-agent/internal/adminops"
)

func TestWave3AdminConsoleFlow(t *testing.T) {
	regSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/ListSkills"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"skills": []map[string]any{
					{"name": "explore-repo", "version": "1", "scope": "SKILL_SCOPE_PLATFORM", "description": "Explore repository"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/GetTenantPolicy"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tenantPolicy": map[string]any{"version": "1", "document": map[string]any{"value": map[string]any{"isolated": true}}},
			})
		case strings.HasSuffix(r.URL.Path, "/Publish"):
			if r.Header.Get("Authorization") != "Bearer reg-pub-secret" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": body["name"], "version": "2"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer regSrv.Close()

	hash, err := adminops.HashPassword("wave3-test-secret-01")
	if err != nil {
		t.Fatal(err)
	}

	ts, _, _, _ := newServerWith(t, func(c *Config) {
		c.Admin = AdminConfig{
			Email:              "operator@example.com",
			PasswordHash:       hash,
			AllowedUsers:       []string{"rbac-op@example.com"},
			VictoriaMetricsURL: regSrv.URL,
			GrafanaURL:         regSrv.URL,
			RegistryURL:        regSrv.URL,
			RegistryToken:      "reg-pub-secret",
		}
		c.CodingDanceToken = "cd-companion-token"
	})

	anon := newClient(t, ts.URL)
	code, _, _ := anon.do(http.MethodGet, "/api/admin/v1/overview", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated overview expected 401, got %d", code)
	}

	code, _, _ = anon.do(http.MethodPost, "/api/admin/v1/login", map[string]any{
		"email":    "operator@example.com",
		"password": "wrong-password",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("bad login expected 401, got %d", code)
	}

	// CSRF guard on POST login
	reqNoCSRF, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/v1/login", strings.NewReader(`{"email":"operator@example.com","password":"wave3-test-secret-01"}`))
	reqNoCSRF.Header.Set("Content-Type", "application/json")
	resNoCSRF, err := http.DefaultClient.Do(reqNoCSRF)
	if err != nil {
		t.Fatal(err)
	}
	resNoCSRF.Body.Close()
	if resNoCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF expected 403, got %d", resNoCSRF.StatusCode)
	}

	adm := newClient(t, ts.URL)
	code, loginResp, raw := adm.do(http.MethodPost, "/api/admin/v1/login", map[string]any{
		"email":    "operator@example.com",
		"password": "wave3-test-secret-01",
	})
	if code != http.StatusOK || loginResp["authenticated"] != true {
		t.Fatalf("admin login failed (%d): %s", code, raw)
	}

	code, ov, raw := adm.do(http.MethodGet, "/api/admin/v1/overview", nil)
	if code != http.StatusOK {
		t.Fatalf("overview failed (%d): %s", code, raw)
	}
	reg, _ := ov["registry"].(map[string]any)
	skills, _ := reg["platform_skills"].([]any)
	if reg["available"] != true || len(skills) != 1 {
		t.Fatalf("expected registry available with 1 platform skill in overview: %#v", ov)
	}

	// Import, Promote, and Rollback a candidate
	code, created, raw := adm.do(http.MethodPost, "/api/admin/v1/evolution/candidates/import", map[string]any{
		"candidate_id":       "cand_wave3_explore",
		"target_kind":        "skill",
		"target_name":        "explore-repo",
		"baseline_version":   "1",
		"baseline_document":  map[string]any{"name": "explore-repo", "body": "v1"},
		"candidate_document": map[string]any{"name": "explore-repo", "body": "v2"},
		"unified_diff":       "--- a/SKILL.md\n+++ b/SKILL.md\n+improve grep order\n",
		"matrix_report":      map[string]any{"gate": map[string]any{"passed": true}},
	})
	if code != http.StatusCreated || created["status"] != "pending_approval" {
		t.Fatalf("import failed (%d): %s", code, raw)
	}

	code, promoted, raw := adm.do(http.MethodPost, "/api/admin/v1/evolution/candidates/cand_wave3_explore/promote", map[string]any{})
	if code != http.StatusOK || promoted["status"] != "promoted" || promoted["promoted_version"] != "2" {
		t.Fatalf("promote failed (%d): %s", code, raw)
	}

	code, rolled, raw := adm.do(http.MethodPost, "/api/admin/v1/evolution/candidates/cand_wave3_explore/rollback", map[string]any{"reason": "canary check"})
	if code != http.StatusOK || rolled["status"] != "rolled_back" {
		t.Fatalf("rollback failed (%d): %s", code, raw)
	}
}

func TestWave3CodingDanceCompanionAPI(t *testing.T) {
	ts, _, _, _ := newServerWith(t, func(c *Config) {
		c.CodingDanceToken = "cd-companion-token"
	})

	call := func(token string, path string, payload map[string]any) (int, map[string]any, string) {
		rawBody, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(string(rawBody)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		rawBytes, _ := json.Marshal(out)
		return res.StatusCode, out, string(rawBytes)
	}

	// Unauthenticated blocked
	code, _, _ := call("", "/api/integrations/coding-dance/v1/hint-check", map[string]any{"problem_id": "two-sum"})
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", code)
	}

	// Off mode -> silent
	code, res, raw := call("cd-companion-token", "/api/integrations/coding-dance/v1/hint-check", map[string]any{
		"consent_mode":  "off",
		"problem_id":    "two-sum",
		"stuck_seconds": 300,
	})
	if code != http.StatusOK || res["action"] != "silent" || res["should_prompt"] != false {
		t.Fatalf("off mode failed (%d): %s", code, raw)
	}

	// proactive_consent -> spoiler-free ask_consent + consent_token
	code, checkRes, raw := call("cd-companion-token", "/api/integrations/coding-dance/v1/hint-check", map[string]any{
		"consent_mode":  "proactive_consent",
		"problem_id":    "two-sum",
		"problem_title": "Two Sum",
		"stuck_seconds": 180,
		"attempt_count": 3,
	})
	if code != http.StatusOK || checkRes["action"] != "ask_consent" || checkRes["should_prompt"] != true || checkRes["spoiler_free"] != true {
		t.Fatalf("proactive hint-check failed (%d): %s", code, raw)
	}
	consentTok, _ := checkRes["consent_token"].(string)
	if consentTok == "" {
		t.Fatalf("missing consent_token in %s", raw)
	}

	// hint-reveal without user_consented -> 403
	code, _, _ = call("cd-companion-token", "/api/integrations/coding-dance/v1/hint-reveal", map[string]any{
		"consent_mode":   "proactive_consent",
		"user_consented": false,
		"consent_token":  consentTok,
		"problem_id":     "two-sum",
		"hint_level":     "L2",
	})
	if code != http.StatusForbidden {
		t.Fatalf("unconsented reveal expected 403, got %d", code)
	}

	// hint-reveal with forged consent_token -> 403
	code, _, _ = call("cd-companion-token", "/api/integrations/coding-dance/v1/hint-reveal", map[string]any{
		"consent_mode":   "proactive_consent",
		"user_consented": true,
		"consent_token":  "1.deadbeef",
		"problem_id":     "two-sum",
		"hint_level":     "L2",
	})
	if code != http.StatusForbidden {
		t.Fatalf("forged consent_token expected 403, got %d", code)
	}

	// hint-reveal with valid consent_token + user_consented=true -> 200 L2
	code, revRes, raw := call("cd-companion-token", "/api/integrations/coding-dance/v1/hint-reveal", map[string]any{
		"consent_mode":   "proactive_consent",
		"user_consented": true,
		"consent_token":  consentTok,
		"problem_id":     "two-sum",
		"problem_title":  "Two Sum",
		"hint_level":     "L2",
		"error_output":   "IndexError: list index out of range",
	})
	if code != http.StatusOK || revRes["hint_level"] != "L2" || revRes["hint"] == "" {
		t.Fatalf("hint-reveal failed (%d): %s", code, raw)
	}

	// history-analyze requires consent
	code, histRes, raw := call("cd-companion-token", "/api/integrations/coding-dance/v1/history-analyze", map[string]any{
		"consent_mode":   "on_demand",
		"user_consented": true,
		"submissions": []map[string]any{
			{"problem_id": "p1", "status": "accepted", "runtime_ms": 12, "tags": []string{"array"}},
			{"problem_id": "p2", "status": "wrong_answer", "error_type": "off_by_one", "runtime_ms": 20, "tags": []string{"dp", "binary_search"}},
		},
	})
	summary, _ := histRes["summary"].(map[string]any)
	if code != http.StatusOK || summary["total_submissions"] != float64(2) {
		t.Fatalf("history-analyze failed (%d): %s", code, raw)
	}
}
