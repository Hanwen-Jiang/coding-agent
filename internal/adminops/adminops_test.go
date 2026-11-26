package adminops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("test-operator-pass-123!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "scrypt$32768$8$1$") {
		t.Fatalf("unexpected hash prefix: %s", hash)
	}
	if !VerifyPassword("test-operator-pass-123!", hash) {
		t.Fatal("VerifyPassword rejected matching password")
	}
	if VerifyPassword("wrong-password", hash) {
		t.Fatal("VerifyPassword accepted wrong password")
	}
	if VerifyPassword("test-operator-pass-123!", "scrypt$10$8$1$bad$bad") {
		t.Fatal("VerifyPassword accepted invalid scrypt parameters")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("expected error for <8 char password")
	}
}

func TestAuthManagerRateLimitAndSessionLifecycle(t *testing.T) {
	now := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	hash, err := HashPassword("operator-secret-99")
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewAuthManager(AuthConfig{
		Email:        "ops@example.com",
		PasswordHash: hash,
		AllowedUsers: map[string]bool{"alice@example.com": true},
		Now:          func() time.Time { return now },
	})
	if !mgr.Enabled() || !mgr.PasswordEnabled() {
		t.Fatal("expected auth manager enabled")
	}
	if !mgr.IsAllowedUser("nobody", "ALICE@EXAMPLE.COM") || mgr.IsAllowedUser("bob", "bob@example.com") {
		t.Fatal("unexpected RBAC allowlist behavior")
	}

	for i := 0; i < maxFailedPerIP; i++ {
		if _, err := mgr.Login("10.0.0.5", "ops@example.com", "bad-pass"); err == nil {
			t.Fatalf("expected failure on attempt %d", i)
		}
	}
	if _, err := mgr.Login("10.0.0.5", "ops@example.com", "operator-secret-99"); err != ErrTooManyAttempts {
		t.Fatalf("expected ErrTooManyAttempts, got %v", err)
	}

	// Different IP succeeds and resets once failures clear.
	sess, err := mgr.Login("10.0.0.6", "OPS@EXAMPLE.COM", "operator-secret-99")
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if _, ok := mgr.LookupSession(sess.ID); !ok {
		t.Fatal("expected active session")
	}

	now = now.Add(adminIdleTTL + time.Minute)
	if _, ok := mgr.LookupSession(sess.ID); ok {
		t.Fatal("expected session expired after idle TTL")
	}

	sess2, err := mgr.Login("10.0.0.6", "ops@example.com", "operator-secret-99")
	if err != nil {
		t.Fatal(err)
	}
	mgr.RevokeSession(sess2.ID)
	if _, ok := mgr.LookupSession(sess2.ID); ok {
		t.Fatal("expected revoked session removed")
	}
}

func TestTelemetrySummaryQueriesVMGrafanaTaskAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte("ok"))
		case "/api/v1/query":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "vector",
					"result": []any{
						map[string]any{"metric": map[string]any{}, "value": []any{1764000000, "2"}},
					},
				},
			})
		case "/api/v1/query_range":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "matrix",
					"result": []any{
						map[string]any{
							"values": [][]any{
								{1764000000, "2.5"},
								{1764000060, "3.0"},
							},
						},
					},
				},
			})
		case "/api/health":
			_, _ = w.Write([]byte(`{"database":"ok","version":"11.0.0"}`))
		case "/api/search":
			u, p, ok := r.BasicAuth()
			if !ok || u != "admin" || p != "gf-secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"uid": "ar-maturity-ops", "title": "Agent Runtime Maturity, Evolution & Crawler", "url": "/d/ar-maturity-ops"},
			})
		case "/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":              true,
				"upstream_search": "ddg_lite",
				"cache_entries":   3,
				"metrics":         map[string]any{"cache_hits": 2, "blocked_requests": 1},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewTelemetryClient(TelemetryConfig{
		VictoriaMetricsURL: srv.URL,
		GrafanaURL:         srv.URL,
		GrafanaExternalURL: "https://grafana.example.com",
		GrafanaUser:        "admin",
		GrafanaPassword:    "gf-secret",
		TaskAPIURL:         srv.URL,
		CrawlerURL:         srv.URL,
	})
	client.Companion.HintChecks.Add(4)
	client.Companion.HintsRevealed.Add(2)

	summary := client.Summary(context.Background())
	if ok, _ := summary.TaskAPI["healthy"].(bool); !ok {
		t.Fatalf("expected task API healthy: %#v", summary.TaskAPI)
	}
	if ok, _ := summary.VictoriaMetrics["available"].(bool); !ok {
		t.Fatalf("expected VM available: %#v", summary.VictoriaMetrics)
	}
	if up, _ := summary.VictoriaMetrics["targets_up"].(float64); up != 2 {
		t.Fatalf("expected 2 up targets, got %#v", summary.VictoriaMetrics)
	}
	if len(summary.Dashboards) != 1 || summary.Dashboards[0].UID != "ar-maturity-ops" {
		t.Fatalf("unexpected dashboards: %#v", summary.Dashboards)
	}
	if avail, _ := summary.Crawler["available"].(bool); !avail || summary.Crawler["upstream_search"] != "ddg_lite" {
		t.Fatalf("unexpected crawler summary: %#v", summary.Crawler)
	}
	if summary.CompanionMetrics["hint_checks"] != 4 || summary.CompanionMetrics["hints_revealed"] != 2 {
		t.Fatalf("unexpected companion metrics: %#v", summary.CompanionMetrics)
	}
}

func TestEvolutionManagerPromoteAndRollback(t *testing.T) {
	var published []map[string]any
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
				"tenantPolicy": map[string]any{
					"version":  "1",
					"document": map[string]any{"value": map[string]any{"isolated": true}},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/Publish"):
			if r.Header.Get("Authorization") != "Bearer reg-secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			published = append(published, body)
			ver := "2"
			if len(published) == 2 {
				ver = "3"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":    body["name"],
				"version": ver,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer regSrv.Close()

	repo, err := OpenFileRepo(filepath.Join(t.TempDir(), "evolution.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistryAdminClient(regSrv.URL, "reg-secret")
	mgr := NewEvolutionManager(repo, reg)

	ctx := context.Background()
	cand, err := mgr.Import(ctx, CandidateRecord{
		CandidateID:       "cand_explore_v2",
		TargetKind:        "skill",
		TargetName:        "explore-repo",
		BaselineVersion:   "1",
		BaselineDocument:  map[string]any{"name": "explore-repo", "body": "v1"},
		CandidateDocument: map[string]any{"name": "explore-repo", "body": "v2"},
		UnifiedDiff:       "--- a/SKILL.md\n+++ b/SKILL.md\n+better repo hints\n",
		MatrixReport:      map[string]any{"gate": map[string]any{"passed": true}},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if cand.Status != "pending_approval" {
		t.Fatalf("expected pending_approval, got %s", cand.Status)
	}

	promoted, err := mgr.Promote(ctx, cand.CandidateID, "ops@example.com")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if promoted.Status != "promoted" || promoted.PromotedVersion != "2" || promoted.ApprovedBy != "ops@example.com" {
		t.Fatalf("unexpected promoted record: %+v", promoted)
	}

	rolledBack, err := mgr.Rollback(ctx, cand.CandidateID, "ops@example.com", "regression in canary")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolledBack.Status != "rolled_back" || rolledBack.RollbackVersion != "3" {
		t.Fatalf("unexpected rolled back record: %+v", rolledBack)
	}
	if len(published) != 2 {
		t.Fatalf("expected 2 Registry publishes (promote + rollback), got %d", len(published))
	}

	ov := mgr.RegistryOverview(ctx)
	if !ov.Available || len(ov.PlatformSkills) != 1 || ov.SysEvolutionPolicy["version"] != "1" {
		t.Fatalf("unexpected registry overview: %+v", ov)
	}

	audits, err := mgr.ListAudit(ctx, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(audits) != 3 {
		t.Fatalf("expected 3 audit entries (import, promote, rollback), got %d: %+v", len(audits), audits)
	}
	if audits[0].Action != "rollback" || audits[0].BeforeVersion != "2" || audits[0].AfterVersion != "3" {
		t.Fatalf("unexpected latest audit entry: %+v", audits[0])
	}
}

func TestTelemetryAlertsAndCodingDanceInfra(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/alerts":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"labels":      map[string]string{"alertname": "AgentRuntimeCrawlerErrors", "severity": "warning", "service": "crawler-gateway"},
					"annotations": map[string]string{"summary": "Crawler upstream 5xx elevated"},
					"startsAt":    "2026-11-26T08:00:00Z",
					"status":      map[string]string{"state": "active"},
				},
			})
		case "/api/v1/alerts":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"alerts": []map[string]any{
						{
							"labels":      map[string]string{"alertname": "AgentRuntimeBudgetHigh", "severity": "critical", "service": "model-gateway"},
							"annotations": map[string]string{"summary": "Budget burn high"},
							"state":       "firing",
							"activeAt":    "2026-11-26T08:05:00Z",
						},
					},
				},
			})
		case "/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "mode": "firecracker"})
		case "/healthz":
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewTelemetryClient(TelemetryConfig{
		VictoriaMetricsURL: srv.URL,
		AlertmanagerURL:    srv.URL,
		CDRunnerURL:        srv.URL,
		CDAPIURL:           srv.URL,
	})
	sum := client.Summary(context.Background())
	if len(sum.Alerts) != 2 {
		t.Fatalf("expected 2 deduplicated alerts, got %+v", sum.Alerts)
	}
	if sum.CodingDanceInfra == nil || sum.CodingDanceInfra["cd_runner"].(map[string]any)["available"] != true || sum.CodingDanceInfra["cd_api"].(map[string]any)["healthy"] != true {
		t.Fatalf("unexpected coding-dance infra status: %+v", sum.CodingDanceInfra)
	}
}

func TestEvolutionRunnerClientTriggerAndBudgetCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runner-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"runs": []map[string]any{{"run_id": "evol-1", "status": "completed", "mode": "fixture"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/evol-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"run_id": "evol-1", "status": "completed", "logs": []string{"line1", "line2"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"run_id": "evol-2", "status": "running", "mode": "gateway",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewEvolutionRunnerClient(srv.URL, "runner-tok", 0.5)
	ctx := context.Background()
	if _, err := c.TriggerRun(ctx, "gateway", 0.9, 2); err == nil {
		t.Fatal("expected over-budget run trigger to fail locally")
	}
	started, err := c.TriggerRun(ctx, "gateway", 0.25, 2)
	if err != nil || started["run_id"] != "evol-2" {
		t.Fatalf("TriggerRun failed: %v (%+v)", err, started)
	}
	runs, err := c.ListRuns(ctx)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns failed: %v (%+v)", err, runs)
	}
	detail, err := c.GetRun(ctx, "evol-1")
	if err != nil || len(detail["logs"].([]any)) != 2 {
		t.Fatalf("GetRun failed: %v (%+v)", err, detail)
	}
}
