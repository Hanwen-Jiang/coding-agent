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
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"uid": "ar-overview", "title": "Agent Runtime Overview", "url": "/d/ar-overview"},
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
		TaskAPIURL:         srv.URL,
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
	if len(summary.Dashboards) != 1 || summary.Dashboards[0].UID != "ar-overview" {
		t.Fatalf("unexpected dashboards: %#v", summary.Dashboards)
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
}
