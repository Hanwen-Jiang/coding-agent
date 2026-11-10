package email

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/agentruntime/coding-agent/internal/entry"
	"strings"
)

type Kind struct {
	VerifiedSender func(context.Context, entry.Connection, string) bool
}

func (Kind) Name() string { return "email" }
func (Kind) Schema() entry.Schema {
	return entry.Schema{Name: "email", Label: "邮件", LabelEN: "Email", Fields: []entry.Field{
		{Name: "recipients", Label: "接收地址（逗号分隔）", LabelEN: "Inbound addresses (comma separated)", Type: "string", Required: true},
		{Name: "instruction", Label: "补充指令", LabelEN: "Additional instructions", Type: "text"},
		{Name: "autonomy", Label: "自主程度", LabelEN: "Autonomy", Type: "select", Default: "analyze", Options: []string{"analyze", "draft_pr", "confirm"}},
		{Name: "language", Label: "语言", LabelEN: "Language", Type: "select", Default: "zh", Options: []string{"zh", "en"}},
	}}
}
func (k Kind) Ingest(ctx context.Context, in entry.Inbound) ([]entry.Trigger, error) {
	if !in.Trusted {
		return nil, errors.New("unauthenticated email provider")
	}
	e, err := DecodeEnvelope(in.Body)
	if err != nil {
		return nil, err
	}
	// Neither a failed DMARC check nor an unknown sender receives a rejection email.
	if !strings.EqualFold(strings.TrimSpace(e.DMARC), "pass") || IsAutomatic(e) {
		return nil, nil
	}
	c := in.Connection
	if k.VerifiedSender == nil || !k.VerifiedSender(ctx, c, e.From) || !matchesRecipient(c, e) {
		return nil, nil
	}
	b, _ := json.Marshal(e)
	hash := sha256.Sum256([]byte(c.ID + "\x00" + e.MessageID))
	return []entry.Trigger{{ID: "email:" + hex.EncodeToString(hash[:]), ConnectionID: c.ID, Tenant: c.Tenant, User: c.User, ProjectID: c.ProjectID, Kind: "email", Scope: c.ProjectID, State: "accepted", Payload: b, CreatedAt: in.Now, UpdatedAt: in.Now, NextAt: in.Now}}, nil
}
func matchesRecipient(c entry.Connection, e Envelope) bool {
	v, _ := c.Config["recipients"].(string)
	for _, r := range strings.Split(v, ",") {
		r, err := NormalizeAddress(r)
		if err != nil {
			continue
		}
		for _, t := range e.To {
			if strings.EqualFold(r, t) {
				return true
			}
		}
	}
	return false
}
func (k Kind) Admit(ctx context.Context, c entry.Connection, t entry.Trigger) entry.Admission {
	if !c.Enabled {
		return entry.Admission{Reason: "connection disabled"}
	}
	e, err := DecodeEnvelope(t.Payload)
	if err != nil || !strings.EqualFold(e.DMARC, "pass") || IsAutomatic(e) || k.VerifiedSender == nil || !k.VerifiedSender(ctx, c, e.From) || !matchesRecipient(c, e) {
		return entry.Admission{Reason: "email sender or authentication not accepted"}
	}
	return entry.Admission{Allowed: true}
}
func (Kind) Compose(c entry.Connection, t entry.Trigger) entry.TaskInput {
	e, err := DecodeEnvelope(t.Payload)
	if err != nil {
		return entry.TaskInput{}
	}
	instruction, _ := c.Config["instruction"].(string)
	// The authenticated sender's subject/body is the user request. Attachments are
	// untrusted materials, separate from the instruction, exactly like web uploads.
	instruction = strings.TrimSpace(instruction + "\n\n" + e.Subject + "\n\n" + e.Text)
	if e.Text == "" && e.HTML != "" {
		instruction += "\n\n" + e.HTML
	}
	materials := []map[string]any{}
	for _, a := range e.Attachments {
		h := sha256.Sum256(a.Data)
		item := map[string]any{"name": a.Name, "content_type": a.ContentType, "size": len(a.Data), "sha256": hex.EncodeToString(h[:])}
		if strings.HasPrefix(a.ContentType, "text/") || a.ContentType == "application/json" {
			item["text"] = string(a.Data)
		}
		materials = append(materials, item)
	}
	b, _ := json.Marshal(materials)
	a, _ := c.Config["autonomy"].(string)
	ceiling := "EFFECT_READ"
	if a == "draft_pr" || a == "confirm" {
		ceiling = "EFFECT_WRITE_REVERSIBLE"
	}
	return entry.TaskInput{Instruction: instruction, Materials: string(b), EffectCeiling: ceiling, BudgetUSD: c.Limits.TaskUSD}
}
func (Kind) DefaultSink() (string, entry.Address) { return "email", entry.Address{} }
func ReplyAddress(e Envelope) entry.Address {
	return entry.Address{Email: e.From, ThreadID: e.MessageID, References: Header(e.Headers, "References")}
}
func Validate(config map[string]any) error {
	v, _ := config["recipients"].(string)
	if v == "" {
		return entry.ErrInvalid
	}
	for _, r := range strings.Split(v, ",") {
		if _, err := NormalizeAddress(r); err != nil {
			return entry.ErrInvalid
		}
	}
	return nil
}
