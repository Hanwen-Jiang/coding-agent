package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/agentruntime/coding-agent/internal/entry"
	"strings"
)

type Kind struct{}

func (Kind) Name() string { return "schedule" }
func (Kind) Schema() entry.Schema {
	return entry.Schema{Name: "schedule", Label: "定时任务", LabelEN: "Scheduled tasks", Fields: []entry.Field{
		{Name: "cron", Label: "计划（UTC）", LabelEN: "Schedule (UTC)", Type: "cron", Required: true, Default: "0 * * * *"},
		{Name: "instruction", Label: "任务指令", LabelEN: "Task instructions", Type: "text", Required: true},
		{Name: "autonomy", Label: "自主程度", LabelEN: "Autonomy", Type: "select", Default: "analyze", Options: []string{"analyze", "draft_pr", "confirm"}},
		{Name: "language", Label: "语言", LabelEN: "Language", Type: "select", Default: "zh", Options: []string{"zh", "en"}},
	}}
}
func (Kind) Ingest(ctx context.Context, in entry.Inbound) ([]entry.Trigger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := in.Connection
	expr, _ := c.Config["cron"].(string)
	t, err := Latest(expr, in.Now)
	if err != nil {
		return nil, err
	}
	if t.Before(c.CreatedAt) || !t.After(c.LastScheduledAt) {
		return nil, nil
	}
	p, _ := json.Marshal(map[string]any{"scheduled_at": t})
	return []entry.Trigger{{ID: fmt.Sprintf("schedule:%s:%d", c.ID, t.Unix()), ConnectionID: c.ID, Tenant: c.Tenant, User: c.User, ProjectID: c.ProjectID, Kind: "schedule", Scope: c.ProjectID, State: "accepted", Payload: p, CreatedAt: in.Now, UpdatedAt: in.Now, NextAt: in.Now}}, nil
}
func (Kind) Admit(_ context.Context, c entry.Connection, _ entry.Trigger) entry.Admission {
	if !c.Enabled {
		return entry.Admission{Reason: "connection disabled"}
	}
	expr, _ := c.Config["cron"].(string)
	if Validate(expr) != nil {
		return entry.Admission{Reason: "invalid UTC schedule"}
	}
	instruction, _ := c.Config["instruction"].(string)
	if strings.TrimSpace(instruction) == "" {
		return entry.Admission{Reason: "task instructions required"}
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
	return entry.TaskInput{Instruction: instruction, Materials: t.Materials, EffectCeiling: ceiling, BudgetUSD: c.Limits.TaskUSD}
}
func (Kind) DefaultSink() (string, entry.Address) { return "", entry.Address{} }
