// Package alert accepts the default Alertmanager and Grafana webhook envelope.
package alert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agentruntime/coding-agent/internal/entry"
	"strings"
	"time"
)

const MaxBody = 2 << 20
const MaxAlerts = 100

var ErrPayload = errors.New("invalid alert webhook payload")

type Event struct {
	Status       string            `json:"status"`
	Fingerprint  string            `json:"fingerprint"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	GeneratorURL string            `json:"generatorURL"`
}
type Envelope struct {
	Status string  `json:"status"`
	Alerts []Event `json:"alerts"`
}
type Kind struct{}

func (Kind) Name() string { return "alert" }
func (Kind) Schema() entry.Schema {
	lo, hi := 1.0, 1440.0
	return entry.Schema{Name: "alert", Label: "服务器告警", LabelEN: "Server alerts", Fields: []entry.Field{
		{Name: "instruction", Label: "分析指令", LabelEN: "Analysis instructions", Type: "text", Required: true},
		{Name: "severity", Label: "告警级别（空为全部）", LabelEN: "Severity (empty for all)", Type: "string"},
		{Name: "match_labels", Label: "标签匹配（key=value，逗号分隔）", LabelEN: "Label matches (key=value, comma separated)", Type: "string"},
		{Name: "dedup_minutes", Label: "去重窗口（分钟）", LabelEN: "Deduplication window (minutes)", Type: "integer", Default: 60, Min: &lo, Max: &hi},
		{Name: "autonomy", Label: "自主程度", LabelEN: "Autonomy", Type: "select", Default: "analyze", Options: []string{"analyze", "draft_pr", "confirm"}},
		{Name: "language", Label: "语言", LabelEN: "Language", Type: "select", Default: "zh", Options: []string{"zh", "en"}},
	}}
}
func (Kind) Ingest(ctx context.Context, in entry.Inbound) ([]entry.Trigger, error) {
	if !in.Trusted {
		return nil, errors.New("unauthenticated alert webhook")
	}
	if len(in.Body) > MaxBody {
		return nil, ErrPayload
	}
	var body Envelope
	if json.Unmarshal(in.Body, &body) != nil || len(body.Alerts) > MaxAlerts {
		return nil, ErrPayload
	}
	c := in.Connection
	out := []entry.Trigger{}
	seen := map[string]bool{}
	for _, a := range body.Alerts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if a.Status == "resolved" {
			continue
		}
		if a.Status != "firing" || a.Fingerprint == "" || len(a.Fingerprint) > 256 || a.StartsAt.IsZero() {
			return nil, ErrPayload
		}
		digest := sha256.Sum256([]byte(c.ID + "\x00" + a.Fingerprint + "\x00" + a.StartsAt.UTC().Format(time.RFC3339Nano)))
		id := "alert:" + hex.EncodeToString(digest[:])
		if seen[id] {
			continue
		}
		seen[id] = true
		payload, _ := json.Marshal(a)
		out = append(out, entry.Trigger{ID: id, ConnectionID: c.ID, Tenant: c.Tenant, User: c.User, ProjectID: c.ProjectID, Kind: "alert", DedupKey: a.Fingerprint, Scope: c.ProjectID, State: "accepted", Payload: payload, Materials: string(payload), CreatedAt: in.Now, UpdatedAt: in.Now, NextAt: in.Now})
	}
	return out, nil
}
func Matches(config map[string]any, a Event) bool {
	severity, _ := config["severity"].(string)
	if severity != "" && a.Labels["severity"] != severity {
		return false
	}
	matches, _ := config["match_labels"].(string)
	for _, match := range strings.Split(matches, ",") {
		if strings.TrimSpace(match) == "" {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(match), "=")
		if !ok || key == "" || a.Labels[strings.TrimSpace(key)] != strings.TrimSpace(value) {
			return false
		}
	}
	return true
}
func (Kind) Admit(_ context.Context, c entry.Connection, t entry.Trigger) entry.Admission {
	if !c.Enabled {
		return entry.Admission{Reason: "connection disabled"}
	}
	var a Event
	if json.Unmarshal(t.Payload, &a) != nil || !Matches(c.Config, a) {
		return entry.Admission{Reason: "alert does not match connection filters"}
	}
	instruction, _ := c.Config["instruction"].(string)
	if strings.TrimSpace(instruction) == "" {
		return entry.Admission{Reason: "analysis instructions required"}
	}
	return entry.Admission{Allowed: true}
}
func (Kind) Compose(c entry.Connection, t entry.Trigger) entry.TaskInput {
	instruction, _ := c.Config["instruction"].(string)
	autonomy, _ := c.Config["autonomy"].(string)
	ceiling := "EFFECT_READ"
	if autonomy == "draft_pr" || autonomy == "confirm" {
		ceiling = "EFFECT_WRITE_REVERSIBLE"
	}
	// encoding/json escapes angle brackets, so the payload cannot close a surrounding data block.
	var v any
	materials := t.Materials
	if json.Unmarshal(t.Payload, &v) == nil {
		b, _ := json.Marshal(v)
		materials = string(b)
	}
	return entry.TaskInput{Instruction: instruction, Materials: materials, EffectCeiling: ceiling, BudgetUSD: c.Limits.TaskUSD}
}
func (Kind) DefaultSink() (string, entry.Address) { return "", entry.Address{} }
func Validate(config map[string]any) error {
	instruction, _ := config["instruction"].(string)
	if strings.TrimSpace(instruction) == "" {
		return fmt.Errorf("%w: instruction", entry.ErrInvalid)
	}
	matches, _ := config["match_labels"].(string)
	for _, m := range strings.Split(matches, ",") {
		if strings.TrimSpace(m) == "" {
			continue
		}
		k, _, ok := strings.Cut(m, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return fmt.Errorf("%w: match_labels", entry.ErrInvalid)
		}
	}
	return nil
}
