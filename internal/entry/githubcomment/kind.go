// Package githubcomment translates authenticated GitHub comments into triggers.
package githubcomment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agentruntime/coding-agent/internal/entry"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var mentionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func ValidateMention(s string) bool { return mentionPattern.MatchString(s) }
func Mention(c entry.Connection) string {
	s, _ := c.Config["mention"].(string)
	if s == "" {
		return "coding-agent"
	}
	return strings.TrimPrefix(s, "@")
}

// Command matches only the leading alias, followed by whitespace, a colon or a comma.
func Command(body, alias string) (instruction string, confirm, ok bool) {
	body = strings.TrimSpace(body)
	mention := "@" + strings.TrimPrefix(alias, "@")
	if len(body) < len(mention) || !strings.EqualFold(body[:len(mention)], mention) {
		return "", false, false
	}
	rest := body[len(mention):]
	sep := func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(":,：，", r) }
	if r, _ := utf8.DecodeRuneInString(rest); rest == "" || !sep(r) {
		return "", false, false
	}
	instruction = strings.TrimSpace(strings.TrimLeftFunc(rest, sep))
	if instruction == "" {
		return "", false, false
	}
	return instruction, strings.EqualFold(instruction, "confirm") || instruction == "确认", true
}

type Kind struct {
	BotSlug   string
	Authorize func(context.Context, entry.Connection, entry.Trigger) entry.Admission
}

func (Kind) Name() string { return "github_comment" }
func (Kind) Schema() entry.Schema {
	return entry.Schema{Name: "github_comment", Label: "GitHub 评论", LabelEN: "GitHub comments", Fields: []entry.Field{
		{Name: "mention", Label: "触发名称（不含 @）", LabelEN: "Mention alias (without @)", Type: "string", Required: true, Default: "coding-agent"},
		{Name: "keywords", Label: "必要关键词（空为不限）", LabelEN: "Required phrase (empty for any)", Type: "string"},
		{Name: "autonomy", Label: "自主程度", LabelEN: "Autonomy", Type: "select", Default: "draft_pr", Options: []string{"analyze", "draft_pr", "confirm"}},
		{Name: "language", Label: "语言", LabelEN: "Language", Type: "select", Default: "zh", Options: []string{"zh", "en"}},
	}}
}
func (k Kind) Ingest(_ context.Context, in entry.Inbound) ([]entry.Trigger, error) {
	if !in.Trusted {
		return nil, errors.New("unauthenticated GitHub webhook")
	}
	var e struct {
		Action string `json:"action"`
		Sender struct {
			Type, Login string
			ID          int64
		} `json:"sender"`
		Comment struct {
			ID   int64
			Body string
		} `json:"comment"`
		Repository struct {
			ID       int64
			FullName string `json:"full_name"`
		} `json:"repository"`
		Issue struct {
			Number      int
			Title, Body string
		} `json:"issue"`
	}
	if len(in.Body) > 2<<20 || json.Unmarshal(in.Body, &e) != nil {
		return nil, errors.New("invalid GitHub webhook")
	}
	if e.Action != "created" || e.Sender.Type == "Bot" || strings.EqualFold(e.Sender.Login, k.BotSlug+"[bot]") || e.Comment.ID == 0 || e.Repository.ID == 0 {
		return nil, nil
	}
	c := in.Connection
	alias := Mention(c)
	if !ValidateMention(alias) {
		return nil, entry.ErrInvalid
	}
	instruction, confirm, ok := Command(e.Comment.Body, alias)
	if !ok || confirm {
		return nil, nil
	} // Confirm is handled against the original run by the authenticated server.
	if len([]rune(instruction)) > 8000 {
		instruction = string([]rune(instruction)[:8000])
	}
	materials, _ := json.Marshal(map[string]string{"title": e.Issue.Title, "body": e.Issue.Body})
	return []entry.Trigger{{ID: fmt.Sprintf("github-comment-%d", e.Comment.ID), ConnectionID: c.ID, Tenant: c.Tenant, User: c.User, ProjectID: c.ProjectID, Kind: "github_comment", Scope: fmt.Sprintf("github:%d", e.Repository.ID), Instruction: instruction, Materials: string(materials), Payload: in.Body, State: "accepted", CreatedAt: in.Now, UpdatedAt: in.Now, NextAt: in.Now}}, nil
}
func (k Kind) Admit(ctx context.Context, c entry.Connection, t entry.Trigger) entry.Admission {
	if !c.Enabled {
		return entry.Admission{Reason: "connection disabled"}
	}
	phrase, _ := c.Config["keywords"].(string)
	if phrase != "" && !strings.Contains(t.Instruction, phrase) {
		return entry.Admission{Reason: "comment does not match connection filter"}
	}
	if k.Authorize == nil {
		return entry.Admission{Reason: "GitHub permission and account verification required"}
	}
	return k.Authorize(ctx, c, t)
}
func (Kind) Compose(c entry.Connection, t entry.Trigger) entry.TaskInput {
	a, _ := c.Config["autonomy"].(string)
	ceiling := "EFFECT_WRITE_REVERSIBLE"
	if a == "analyze" {
		ceiling = "EFFECT_READ"
	}
	return entry.TaskInput{Instruction: t.Instruction, Materials: t.Materials, EffectCeiling: ceiling, BudgetUSD: c.Limits.TaskUSD}
}
func (Kind) DefaultSink() (string, entry.Address) { return "github_comment", entry.Address{} }
