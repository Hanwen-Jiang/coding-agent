package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agentruntime/coding-agent/internal/entry"
	"github.com/agentruntime/coding-agent/internal/entry/githubcomment"
	"github.com/agentruntime/coding-agent/internal/github"
)

func githubEntryID(comment int64) string { return "github-comment-" + strconv.FormatInt(comment, 10) }
func decodeGitHubEntry(t entry.Trigger) (githubTrigger, error) {
	var g githubTrigger
	err := json.Unmarshal(t.Payload, &g)
	if len(t.Payload) == 0 {
		g.Comment, _ = strconv.ParseInt(strings.TrimPrefix(t.ID, "github-comment-"), 10, 64)
		err = nil
	}
	g.ConnectionID = t.ConnectionID
	g.Tenant = t.Tenant
	g.User = t.User
	g.ProjectID = t.ProjectID
	g.State = t.State
	g.Reason = t.Reason
	g.SessionID = t.SessionID
	g.WorkspaceID = t.WorkspaceID
	g.TaskID = t.TaskID
	g.CreatedAt = t.CreatedAt
	return g, err
}
func encodeGitHubEntry(g githubTrigger, t *entry.Trigger) {
	t.Payload, _ = json.Marshal(g)
	t.SessionID = g.SessionID
	t.WorkspaceID = g.WorkspaceID
	t.TaskID = g.TaskID
	t.State = g.State
	t.Reason = g.Reason
	t.UpdatedAt = time.Now().UTC()
}
func (s *Server) loadTriggers() ([]githubTrigger, error) {
	out := []githubTrigger{}
	if s.cfg.EntryStore == nil {
		return out, nil
	}
	cs, err := s.cfg.EntryStore.AllConnections(context.Background())
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		if c.Kind != "github_comment" {
			continue
		}
		ts, e := s.cfg.EntryStore.Triggers(context.Background(), c.Tenant, c.User, c.ID, 500)
		if e != nil {
			return nil, e
		}
		for _, t := range ts {
			g, e := decodeGitHubEntry(t)
			if e != nil {
				return nil, e
			}
			out = append(out, g)
		}
	}
	return out, nil
}
func (s *Server) findTrigger(comment int64) (githubTrigger, bool, error) {
	if s.cfg.EntryStore == nil {
		return githubTrigger{}, false, nil
	}
	t, err := s.cfg.EntryStore.Trigger(context.Background(), githubEntryID(comment))
	if errors.Is(err, entry.ErrNotFound) {
		return githubTrigger{}, false, nil
	}
	if err != nil {
		return githubTrigger{}, false, err
	}
	g, err := decodeGitHubEntry(t)
	return g, true, err
}
func (s *Server) addTrigger(g githubTrigger) (githubTrigger, bool, error) {
	if s.cfg.EntryStore == nil {
		return g, false, entry.ErrInvalid
	}
	c, err := s.cfg.EntryStore.LookupConnection(context.Background(), g.ConnectionID)
	if err != nil {
		return g, false, err
	}
	p, ok, err := s.store.Project(c.Tenant, c.ProjectID)
	if err != nil || !ok {
		return g, false, entry.ErrNotFound
	}
	t := entry.Trigger{ID: githubEntryID(g.Comment), CreatedAt: g.CreatedAt}
	s.fillEntryTrigger(&t, c, p)
	encodeGitHubEntry(g, &t)
	t.SourceAddress = entry.Address{Repo: g.Repo, RepoID: g.RepoID, Installation: g.Installation, Issue: g.Issue}
	if prov := s.providers["github"]; prov != nil {
		t.SourceURL = prov.Web + fmt.Sprintf("/%s/issues/%d#issuecomment-%d", g.Repo, g.Issue, g.Comment)
	}
	result, err := s.cfg.EntryStore.Enqueue(context.Background(), t)
	if err != nil {
		return g, false, err
	}
	out, err := decodeGitHubEntry(result.Trigger)
	return out, result.Added, err
}
func (s *Server) updateTrigger(comment int64, fn func(*githubTrigger) bool) (githubTrigger, bool, error) {
	if s.cfg.EntryStore == nil {
		return githubTrigger{}, false, entry.ErrNotFound
	}
	token := int64(0)
	if v, ok := s.entryLeases.Load(comment); ok {
		token = v.(int64)
	}
	t, changed, err := s.cfg.EntryStore.MutateTrigger(context.Background(), githubEntryID(comment), token, func(t *entry.Trigger) bool {
		g, e := decodeGitHubEntry(*t)
		if e != nil || !fn(&g) {
			return false
		}
		encodeGitHubEntry(g, t)
		return true
	})
	if err != nil {
		return githubTrigger{}, false, err
	}
	g, err := decodeGitHubEntry(t)
	return g, changed, err
}

// MigrateEntryTriggers imports historical runs once, preserving existing work.
// Migrated connections stay disabled until their owner explicitly enables them.
func (s *Server) MigrateEntryTriggers(ctx context.Context) error {
	if s.cfg.EntryStore == nil {
		return nil
	}
	marker := s.triggersFile() + ".migrated"
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	all, err := readJSONFile[[]githubTrigger](s.triggersFile())
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return nil
	}
	for _, g := range all {
		if g.Tenant == "" || g.User == "" || g.ProjectID == "" {
			continue
		}
		cid := "migrated-" + workspaceHash(g.Tenant, g.User, g.ProjectID)[:24]
		c, err := s.cfg.EntryStore.LookupConnection(ctx, cid)
		if errors.Is(err, entry.ErrNotFound) {
			mention := "coding-agent"
			if s.cfg.GitHubApp != nil {
				mention = s.cfg.GitHubApp.Slug
			}
			// G-06 had only the hourly budget; keep that behaviour for migrated runs
			// rather than silently capping each task at the new-connection default.
			limits := entry.DefaultLimits()
			limits.TaskUSD = limits.USDPerHour
			c = entry.Connection{ID: cid, Tenant: g.Tenant, User: g.User, ProjectID: g.ProjectID, Kind: "github_comment", Name: "GitHub comments (migrated)", Config: map[string]any{"mention": mention, "autonomy": "draft_pr", "language": "en"}, Sinks: []entry.Destination{{Sink: "github_comment"}}, Limits: limits, CreatedAt: s.now().UTC(), UpdatedAt: s.now().UTC()}
			err = s.cfg.EntryStore.PutConnection(ctx, c)
		}
		if err != nil {
			return err
		}
		g.ConnectionID = cid
		g.Autonomy = "draft_pr"
		g.Mention = entryConfig(c, "mention")
		t := entry.Trigger{ID: githubEntryID(g.Comment), ConnectionID: cid, Tenant: g.Tenant, User: g.User, ProjectID: g.ProjectID, Kind: "github_comment", Scope: fmt.Sprintf("github/%d", g.RepoID), CreatedAt: g.CreatedAt, NextAt: s.now().UTC(), ReservedUSD: entry.MaxUSDPerHour}
		encodeGitHubEntry(g, &t)
		if err = s.cfg.EntryStore.ImportTrigger(ctx, t); err != nil {
			return err
		}
	}
	if err := os.Rename(s.triggersFile(), marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, e := os.Stat(marker); e == nil {
				return nil
			}
		}
		return err
	}
	return nil
}
func (s *Server) onIssueComment(ctx context.Context, d github.Delivery) error {
	if s.cfg.EntryStore == nil || s.cfg.GitHubApp == nil {
		return nil
	}
	e, err := parseEvent(d)
	if err != nil {
		return err
	}
	if e.Action != "created" || e.Comment.ID == 0 || e.Repository.ID == 0 || e.Installation.ID == 0 || e.Issue.Number == 0 || e.Sender.Type == "Bot" || strings.EqualFold(e.Sender.Login, s.cfg.GitHubApp.Slug+"[bot]") {
		return nil
	}
	if _, seen, err := s.findTrigger(e.Comment.ID); err != nil || seen {
		return err
	}
	if handled, err := s.confirmGitHubComment(ctx, e); handled || err != nil {
		return err
	}
	ref, linked := s.lookupIdentity(identityKey("github", strconv.FormatInt(e.Sender.ID, 10)))
	if !linked {
		return nil
	}
	cs, err := s.cfg.EntryStore.Connections(ctx, ref.Tenant, ref.User)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if c.Kind != "github_comment" {
			continue
		}
		p, ok, err := s.store.Project(c.Tenant, c.ProjectID)
		if err != nil {
			return err
		}
		// Match on the immutable repository ID. A name only identifies a project that
		// predates RepoID; after a rename or transfer another repository can reuse it.
		if !ok || p.Provider != "github" || p.RepoID != 0 && p.RepoID != e.Repository.ID || p.RepoID == 0 && !strings.EqualFold(p.Repo, e.Repository.FullName) {
			continue
		}
		instruction, confirm, match := githubcomment.Command(e.Comment.Body, githubcomment.Mention(c))
		if !match {
			continue
		}
		writer, err := s.canWrite(ctx, e.Installation.ID, e.Repository, e.Sender.Login)
		if err != nil || !writer {
			return err
		}
		if confirm {
			return s.confirmEntryGitHub(ctx, e, c)
		}
		g := githubTrigger{ConnectionID: c.ID, Comment: e.Comment.ID, Repo: e.Repository.FullName, RepoID: e.Repository.ID, Installation: e.Installation.ID, Issue: e.Issue.Number, Author: e.Sender.Login, AuthorID: e.Sender.ID, Tenant: c.Tenant, User: c.User, ProjectID: c.ProjectID, Instruction: clip(instruction), IssueTitle: clip(e.Issue.Title), IssueBody: clip(e.Issue.Body), State: "accepted", CreatedAt: s.now().UTC(), Autonomy: entryConfig(c, "autonomy"), Mention: githubcomment.Mention(c), BudgetUSD: c.Limits.TaskUSD}
		if kw := entryConfig(c, "keywords"); kw != "" && !strings.Contains(instruction, kw) {
			g.State = "refused"
			g.Reason = "comment_filter"
			g.Instruction = ""
			g.IssueTitle = ""
			g.IssueBody = ""
		}
		if e.Issue.PullRequest != nil {
			var pr struct {
				Head struct {
					Repo *ghRepo `json:"repo"`
				} `json:"head"`
			}
			if err = s.installationGet(ctx, g.Installation, g.RepoID, map[string]string{"pull_requests": "read"}, fmt.Sprintf("/repos/%s/pulls/%d", g.Repo, g.Issue), &pr); err != nil {
				return err
			}
			if pr.Head.Repo == nil || pr.Head.Repo.ID != g.RepoID {
				g.State = "refused"
				g.Reason = "fork_pull_request"
				g.Instruction = ""
				g.IssueTitle = ""
				g.IssueBody = ""
			}
		}
		_, _, err = s.addTrigger(g)
		return err
	}
	return nil
}
func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (s *Server) confirmEntryGitHub(ctx context.Context, e ghEvent, c entry.Connection) error {
	rows, err := s.cfg.EntryStore.Triggers(ctx, c.Tenant, c.User, c.ID, 500)
	if err != nil {
		return err
	}
	for _, t := range rows {
		g, err := decodeGitHubEntry(t)
		if err != nil {
			return err
		}
		if g.RepoID != e.Repository.ID || g.Issue != e.Issue.Number || t.State != "awaiting_confirmation" || s.now().Sub(g.AskedAt) > triggerConfirmWithin {
			continue
		}
		_, _, err = s.cfg.EntryStore.MutateTrigger(ctx, t.ID, 0, func(x *entry.Trigger) bool {
			if x.State != "awaiting_confirmation" {
				return false
			}
			x.State = "confirmed"
			x.Reason = ""
			x.NextAt = time.Now().UTC()
			return true
		})
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *Server) notifyTrigger(ctx context.Context, comment int64) error {
	t, err := s.cfg.EntryStore.Trigger(ctx, githubEntryID(comment))
	if err != nil {
		return err
	}
	g, err := decodeGitHubEntry(t)
	if err != nil || g.Reason == "" || slices.Contains(g.Notified, g.State) {
		return err
	}
	phase := entry.Failed
	switch g.State {
	case "running":
		phase = entry.Accepted
	case "awaiting_confirmation":
		phase = entry.Question
	case "done":
		phase = entry.Result
	}
	return s.queueEntryOutcome(ctx, t, phase, g.Reason)
}
func (s *Server) AdvanceGitHubTriggers(ctx context.Context) error { return s.AdvanceEntries(ctx) }

func (s *Server) confirmGitHubComment(ctx context.Context, e ghEvent) (bool, error) {
	cs, err := s.cfg.EntryStore.AllConnections(ctx)
	if err != nil {
		return false, err
	}
	seen := map[string]bool{}
	handled := false
	authorized := false
	for _, c := range cs {
		owner := c.Tenant + "/" + c.User
		if seen[owner] {
			continue
		}
		seen[owner] = true
		ts, err := s.cfg.EntryStore.ActiveTriggers(ctx, c.Tenant, c.User)
		if err != nil {
			return false, err
		}
		for _, t := range ts {
			if t.Kind != "github_comment" || t.State != "awaiting_confirmation" {
				continue
			}
			g, err := decodeGitHubEntry(t)
			if err != nil {
				return false, err
			}
			if g.RepoID != e.Repository.ID || g.Issue != e.Issue.Number {
				continue
			}
			_, confirm, ok := githubcomment.Command(e.Comment.Body, g.Mention)
			if !confirm || !ok {
				continue
			}
			handled = true
			if !authorized {
				writer, err := s.canWrite(ctx, e.Installation.ID, e.Repository, e.Sender.Login)
				if err != nil || !writer {
					return true, err
				}
				authorized = true
			}
			_, _, err = s.cfg.EntryStore.MutateTrigger(ctx, t.ID, 0, func(x *entry.Trigger) bool {
				g, err := decodeGitHubEntry(*x)
				if err != nil || x.State != "awaiting_confirmation" || g.AskedAt.IsZero() || s.now().Sub(g.AskedAt) > triggerConfirmWithin {
					return false
				}
				x.State = "confirmed"
				x.Reason = ""
				x.NextAt = time.Now().UTC()
				return true
			})
			if err != nil {
				return true, err
			}
		}
	}
	return handled, nil
}
