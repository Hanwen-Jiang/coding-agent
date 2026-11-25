package codingdance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agentruntime/coding-agent/internal/entry"
)

func TestKindIngestAdmitComposeConsentFirst(t *testing.T) {
	k := Kind{}
	if k.Name() != "coding_dance" {
		t.Fatalf("unexpected kind name: %s", k.Name())
	}
	conn := entry.Connection{
		ID:        "conn_cd_1",
		Tenant:    "t1",
		User:      "u1",
		ProjectID: "proj1",
		Kind:      "coding_dance",
		Enabled:   true,
		Config: map[string]any{
			"consent_mode":       "proactive_consent",
			"default_hint_level": "L2",
			"event_types":        "submission_failed,stuck_signal,history_review",
			"autonomy":           "analyze",
			"language":           "en",
		},
		Limits: entry.Limits{TaskUSD: 0.75},
	}

	now := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	if _, err := k.Ingest(context.Background(), entry.Inbound{
		Trusted:    false,
		Body:       []byte(`{"event_type":"stuck_signal","problem_id":"two-sum"}`),
		Now:        now,
		Connection: conn,
	}); err == nil {
		t.Fatal("expected error for untrusted webhook")
	}

	triggers, err := k.Ingest(context.Background(), entry.Inbound{
		Trusted:    true,
		Body:       []byte(`{"events":[{"event_id":"ev1","event_type":"stuck_signal","problem_id":"two-sum","consent_granted":false},{"event_id":"ev1","event_type":"stuck_signal","problem_id":"two-sum"},{"event_id":"ev2","event_type":"stuck_signal","problem_id":"two-sum","consent_granted":true,"hint_level":"L3"},{"event_id":"ev3","event_type":"history_review","problem_id":"all","consent_mode":"off","consent_granted":true}]}`),
		Now:        now,
		Connection: conn,
	})
	if err != nil {
		t.Fatalf("Ingest failed: %v", err)
	}
	if len(triggers) != 3 {
		t.Fatalf("expected 3 deduplicated triggers, got %d", len(triggers))
	}
	for _, tr := range triggers {
		if !strings.HasPrefix(tr.ID, "cd:") {
			t.Fatalf("expected cd: trigger id, got %s", tr.ID)
		}
	}

	// ev1 without consent_granted must be blocked
	if adm := k.Admit(context.Background(), conn, triggers[0]); adm.Allowed {
		t.Fatalf("expected unconsented stuck_signal blocked, got allowed")
	}
	// ev2 with consent_granted allowed & composed with L3
	if adm := k.Admit(context.Background(), conn, triggers[1]); !adm.Allowed {
		t.Fatalf("expected consented trigger allowed, got %s", adm.Reason)
	}
	taskIn := k.Compose(conn, triggers[1])
	if !strings.Contains(taskIn.Instruction, "Hint Level L3") || taskIn.EffectCeiling != "EFFECT_READ" || taskIn.BudgetUSD != 0.75 {
		t.Fatalf("unexpected composed TaskInput: %+v", taskIn)
	}
	// ev3 with consent_mode=off blocked even if consent_granted=true
	if adm := k.Admit(context.Background(), conn, triggers[2]); adm.Allowed {
		t.Fatalf("expected off mode blocked")
	}

	// on_demand must never loosen to proactive_consent
	if got := EffectiveConsentMode(map[string]any{"consent_mode": "on_demand"}, Event{ConsentMode: "proactive_consent"}); got != "on_demand" {
		t.Fatalf("expected on_demand clamp, got %s", got)
	}
	if err := Validate(map[string]any{"consent_mode": "bad"}); err == nil {
		t.Fatal("expected Validate error on invalid consent_mode")
	}
}
