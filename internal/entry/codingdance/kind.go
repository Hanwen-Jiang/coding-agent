// Package codingdance implements the asynchronous Webhook entry adapter for the <coding-dance>
// training/coding platform, enforcing Consent-First and progressive hint levels (L1-L4).
package codingdance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/agentruntime/coding-agent/internal/entry"
)

const MaxBody = 1 << 20
const MaxEvents = 50

var ErrPayload = errors.New("invalid coding-dance webhook payload")

var (
	ConsentModes = []string{"proactive_consent", "on_demand", "off"}
	HintLevels   = []string{"L1", "L2", "L3", "L4"}
)

type Event struct {
	EventID        string `json:"event_id"`
	EventType      string `json:"event_type"` // submission_failed | stuck_signal | session_completed | history_review
	UserHandle     string `json:"user_handle,omitempty"`
	ProblemID      string `json:"problem_id,omitempty"`
	ProblemTitle   string `json:"problem_title,omitempty"`
	Language       string `json:"language,omitempty"`
	Code           string `json:"code,omitempty"`
	ErrorOutput    string `json:"error_output,omitempty"`
	StuckSeconds   int    `json:"stuck_seconds,omitempty"`
	AttemptCount   int    `json:"attempt_count,omitempty"`
	ConsentMode    string `json:"consent_mode,omitempty"`
	ConsentGranted bool   `json:"consent_granted,omitempty"`
	HintLevel      string `json:"hint_level,omitempty"`
}

type Kind struct{}

func (Kind) Name() string { return "coding_dance" }

func (Kind) Schema() entry.Schema {
	return entry.Schema{
		Name:    "coding_dance",
		Label:   "Coding Dance 伴侣",
		LabelEN: "Coding Dance companion",
		Fields: []entry.Field{
			{Name: "consent_mode", Label: "主动提醒隐私模式", LabelEN: "Consent mode", Type: "select", Default: "proactive_consent", Options: slices.Clone(ConsentModes)},
			{Name: "default_hint_level", Label: "默认渐进提示等级", LabelEN: "Default hint level", Type: "select", Default: "L2", Options: slices.Clone(HintLevels)},
			{Name: "event_types", Label: "监听事件类型（逗号分隔，空为全部）", LabelEN: "Event types (comma separated, empty for all)", Type: "string", Default: "submission_failed,stuck_signal,session_completed"},
			{Name: "instruction", Label: "教练角色自定义指令", LabelEN: "Custom coaching prompt", Type: "text"},
			{Name: "autonomy", Label: "自主程度", LabelEN: "Autonomy", Type: "select", Default: "analyze", Options: []string{"analyze", "draft_pr", "confirm"}},
			{Name: "language", Label: "回复语言", LabelEN: "Language", Type: "select", Default: "zh", Options: []string{"zh", "en"}},
		},
	}
}

func (Kind) Ingest(ctx context.Context, in entry.Inbound) ([]entry.Trigger, error) {
	if !in.Trusted {
		return nil, errors.New("unauthenticated coding-dance webhook")
	}
	if len(in.Body) > MaxBody {
		return nil, ErrPayload
	}
	var batch struct {
		Events []Event `json:"events"`
	}
	var events []Event
	if json.Unmarshal(in.Body, &batch) == nil && len(batch.Events) > 0 {
		events = batch.Events
	} else {
		var single Event
		if json.Unmarshal(in.Body, &single) != nil || strings.TrimSpace(single.EventType) == "" {
			return nil, ErrPayload
		}
		events = []Event{single}
	}
	if len(events) > MaxEvents {
		return nil, ErrPayload
	}

	c := in.Connection
	out := make([]entry.Trigger, 0, len(events))
	seen := map[string]bool{}
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ev.EventType = strings.TrimSpace(ev.EventType)
		if ev.EventType == "" {
			return nil, ErrPayload
		}
		if ev.ConsentMode != "" && !slices.Contains(ConsentModes, ev.ConsentMode) {
			return nil, ErrPayload
		}
		if ev.HintLevel != "" && !slices.Contains(HintLevels, ev.HintLevel) {
			return nil, ErrPayload
		}
		keySource := ev.EventID
		if strings.TrimSpace(keySource) == "" {
			keySource = ev.EventType + "\x00" + ev.ProblemID + "\x00" + ev.UserHandle + "\x00" + in.Now.UTC().Format(time.RFC3339Nano)
		}
		digest := sha256.Sum256([]byte(c.ID + "\x00" + keySource))
		id := "cd:" + hex.EncodeToString(digest[:])
		if seen[id] {
			continue
		}
		seen[id] = true
		payload, _ := json.Marshal(ev)
		out = append(out, entry.Trigger{
			ID:           id,
			ConnectionID: c.ID,
			Tenant:       c.Tenant,
			User:         c.User,
			ProjectID:    c.ProjectID,
			Kind:         "coding_dance",
			DedupKey:     keySource,
			Scope:        c.ProjectID,
			State:        "accepted",
			Payload:      payload,
			Materials:    string(payload),
			CreatedAt:    in.Now,
			UpdatedAt:    in.Now,
			NextAt:       in.Now,
		})
	}
	return out, nil
}

func EffectiveConsentMode(cfg map[string]any, ev Event) string {
	connMode, _ := cfg["consent_mode"].(string)
	if !slices.Contains(ConsentModes, connMode) {
		connMode = "proactive_consent"
	}
	if connMode == "off" || ev.ConsentMode == "off" {
		return "off"
	}
	if ev.ConsentMode != "" && slices.Contains(ConsentModes, ev.ConsentMode) {
		// Never loosen on_demand to proactive_consent via event payload
		if connMode == "on_demand" && ev.ConsentMode == "proactive_consent" {
			return "on_demand"
		}
		return ev.ConsentMode
	}
	return connMode
}

func (Kind) Admit(_ context.Context, c entry.Connection, t entry.Trigger) entry.Admission {
	if !c.Enabled {
		return entry.Admission{Reason: "connection disabled"}
	}
	var ev Event
	if json.Unmarshal(t.Payload, &ev) != nil {
		return entry.Admission{Reason: "invalid coding-dance payload"}
	}
	mode := EffectiveConsentMode(c.Config, ev)
	if mode == "off" {
		return entry.Admission{Reason: "consent_mode is off"}
	}
	if (ev.EventType == "stuck_signal" || ev.EventType == "submission_failed") && !ev.ConsentGranted {
		return entry.Admission{Reason: "consent required before dispatching coding-dance coaching task"}
	}
	if filter, _ := c.Config["event_types"].(string); strings.TrimSpace(filter) != "" {
		matched := false
		for _, item := range strings.Split(filter, ",") {
			if strings.TrimSpace(item) == ev.EventType {
				matched = true
				break
			}
		}
		if !matched {
			return entry.Admission{Reason: "event_type does not match connection filter"}
		}
	}
	return entry.Admission{Allowed: true}
}

func LevelPromptRule(level string, lang string) string {
	switch level {
	case "L1":
		if lang == "en" {
			return "Hint Level L1 (Socratic check): Ask 1-2 short guiding questions only. Do NOT name specific data structures, algorithms, fixes, or code."
		}
		return "提示等级 L1（方向提问）：只提出 1～2 个引导思考的关键问题，严禁点破具体算法、数据结构、修法或给出任何代码。"
	case "L3":
		if lang == "en" {
			return "Hint Level L3 (Structural skeleton): Provide step-by-step pseudocode/algorithm outline and time-complexity bounds without writing complete solution code."
		}
		return "提示等级 L3（结构框架/伪代码）：给出关键步骤骨架与复杂度边界，由学习者自己补全完整代码。"
	case "L4":
		if lang == "en" {
			return "Hint Level L4 (Full walkthrough): Provide root-cause analysis, clean fix explanation, complexity analysis, and edge-case test cases."
		}
		return "提示等级 L4（完整思路与对照）：给出完整根因定位、优化实现对比、复杂度与边界测试清单。"
	default:
		if lang == "en" {
			return "Hint Level L2 (Clues & edge cases): Point out key invariants, constraints, or subtle boundary conditions without writing code."
		}
		return "提示等级 L2（切入点与边界线索）：指出核心不变量、约束条件或常见边界坑点，不给出代码。"
	}
}

func (Kind) Compose(c entry.Connection, t entry.Trigger) entry.TaskInput {
	var ev Event
	_ = json.Unmarshal(t.Payload, &ev)
	level := ev.HintLevel
	if !slices.Contains(HintLevels, level) {
		level, _ = c.Config["default_hint_level"].(string)
	}
	if !slices.Contains(HintLevels, level) {
		level = "L2"
	}
	lang, _ := c.Config["language"].(string)
	if lang != "en" {
		lang = "zh"
	}
	custom, _ := c.Config["instruction"].(string)
	rule := LevelPromptRule(level, lang)
	var b strings.Builder
	if strings.TrimSpace(custom) != "" {
		b.WriteString(strings.TrimSpace(custom))
		b.WriteString("\n\n")
	}
	b.WriteString(rule)
	if ev.EventType == "history_review" || ev.EventType == "session_completed" {
		if lang == "en" {
			b.WriteString("\nAlso summarize repeatable coding habits, potential refactoring improvements, and 2-3 targeted follow-up drills.")
		} else {
			b.WriteString("\n同时总结可沉淀的编码习惯、历史代码优化点，并给出 2～3 道针对薄弱环节的巩固练习建议。")
		}
	}

	autonomy, _ := c.Config["autonomy"].(string)
	ceiling := "EFFECT_READ"
	if autonomy == "draft_pr" || autonomy == "confirm" {
		ceiling = "EFFECT_WRITE_REVERSIBLE"
	}
	return entry.TaskInput{
		Instruction:   b.String(),
		Materials:     t.Materials,
		EffectCeiling: ceiling,
		BudgetUSD:     c.Limits.TaskUSD,
	}
}

func (Kind) DefaultSink() (string, entry.Address) { return "", entry.Address{} }

func Validate(config map[string]any) error {
	if m, _ := config["consent_mode"].(string); m != "" && !slices.Contains(ConsentModes, m) {
		return fmt.Errorf("%w: consent_mode", entry.ErrInvalid)
	}
	if l, _ := config["default_hint_level"].(string); l != "" && !slices.Contains(HintLevels, l) {
		return fmt.Errorf("%w: default_hint_level", entry.ErrInvalid)
	}
	return nil
}
