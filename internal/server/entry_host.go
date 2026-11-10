package server

// 接入点的宿主一侧（EP-01，docs/plan/entry-points.md）：internal/entry 的框架要 coding-web 做的事——给触发建会话与
// 工作区、提交 Task、看 Task 的状态与花费。各类型都一样；类型自己的那部分（GitHub 评论见 entry_github.go）经 entry.Kind。

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
	"github.com/agentruntime/coding-agent/internal/entry"
)

// sessionPreparer 是要在新会话的工作区上先做点事的类型（如拉基线分支的最新提交）：返回会话的基线提交，空表示照用
// 复制来的。
type sessionPreparer interface {
	prepareSession(ctx context.Context, id identity, t entry.Trigger, workspaceID string, p Project) string
}

type entryHost struct{ s *Server }

// newEntryEngine 建接入点的 Engine：存储是 cfg.Entry（Postgres），没给时是数据目录里的 entry.json（单进程）。
func (s *Server) newEntryEngine() error {
	store := s.cfg.Entry
	if store == nil {
		store = entry.NewFile(filepath.Join(s.cfg.DataDir, "entry.json"))
	}
	eng, err := entry.New(entry.Options{Store: store, Host: entryHost{s}, Now: s.now, Metrics: s.metrics()})
	if err != nil {
		return err
	}
	s.entry, s.entryKinds = eng, map[string]entry.Kind{}
	for _, k := range []entry.Kind{githubCommentKind{s}} {
		s.entryKinds[k.Name()] = k
		eng.Register(k)
	}
	eng.RegisterSink(githubCommentSink{s})
	return s.migrateGitHubTriggers(context.Background())
}

// AdvanceEntries 把各接入点起的运行往前推一步，并发出到时间的投递（coding-web 每 10 秒跑一次）。
func (s *Server) AdvanceEntries(ctx context.Context) error { return s.entry.Advance(ctx) }

// entryIdentity 是代触发的账号调 Runtime 的身份（现签 5 分钟的令牌）。
func (s *Server) entryIdentity(t entry.Trigger) identity {
	return identity{Identity: ingress.Identity{TenantID: t.Tenant, UserID: t.User}, token: s.ingress.IssueToken(t.Tenant, t.User, defaultRuntimeTokenTTL)}
}

// OpenSession 给触发建一个新会话：工作区从项目复制，类型要的话再准备一下（拉基线分支）。
func (h entryHost) OpenSession(ctx context.Context, t entry.Trigger, in entry.TaskInput) (string, string, error) {
	s := h.s
	id := s.entryIdentity(t)
	p, ok, err := s.store.Project(t.Tenant, t.ProjectID)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", &entry.Stop{Outcome: entry.Outcome{Code: entry.ProjectGone}}
	}
	if _, err := s.ensureSessions(t.Tenant); err != nil {
		return "", "", err
	}
	now := s.now().UTC()
	sess := Session{ID: newSessionID(), ProjectID: p.ID, Title: sessionTitle(in.Title), CreatedAt: now, CreatedBy: t.User, UpdatedAt: now}
	sess.WorkspaceID = p.ID + "--" + sess.ID
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	info, err := s.ws.Create(cctx, id, sess.WorkspaceID, CreateSpec{From: p.ID})
	if err != nil {
		return "", "", err
	}
	sess.BaseCommit = info.Head
	if prep, ok := s.entryKinds[t.Kind].(sessionPreparer); ok {
		if head := prep.prepareSession(cctx, id, t, sess.WorkspaceID, p); head != "" {
			sess.BaseCommit = head
		}
	}
	if err := s.store.UpdateSessions(t.Tenant, func(all []Session) ([]Session, error) { return append(all, sess), nil }); err != nil {
		_ = s.ws.Delete(cctx, id, sess.WorkspaceID)
		return "", "", err
	}
	return sess.ID, sess.WorkspaceID, nil
}

// Submit 提交 Task 并记进会话；clientRequestId 由触发定，重试时 Runtime 去重。
func (h entryHost) Submit(ctx context.Context, t entry.Trigger, in entry.TaskInput) (string, error) {
	s := h.s
	id := s.entryIdentity(t)
	submit := map[string]any{
		"workspaceId":     t.WorkspaceID,
		"clientRequestId": in.ClientRequestID,
		"message":         in.Message,
		"sessionAccess":   "SESSION_ACCESS_READ_WRITE",
		"inputs":          map[string]any{"constraints": map[string]any{"model_preference": defaultModel}},
		"effectCeiling":   effectEnum(defaultSandbox),
	}
	var resp submitResponse
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.api.Call(cctx, id.token, "SubmitTask", submit, &resp); err != nil {
		return "", err
	}
	now := s.now().UTC()
	_, _ = s.updateSession(t.Tenant, Session{ID: t.SessionID, ProjectID: t.ProjectID}, func(x *Session) {
		if x.RuntimeSessionID == "" {
			x.RuntimeSessionID = resp.SessionID
		}
		x.UpdatedAt = now
	})
	record := TaskRecord{ID: resp.TaskID, Message: in.Message, CreatedAt: now, CreatedBy: t.User, Status: "TASK_STATUS_QUEUED", Sandbox: defaultSandbox, Model: defaultModel}
	if err := s.store.UpdateTasks(t.Tenant, t.WorkspaceID, func(ts []TaskRecord) []TaskRecord {
		if slices.ContainsFunc(ts, func(x TaskRecord) bool { return x.ID == record.ID }) {
			return ts
		}
		return append(ts, record)
	}); err != nil {
		return "", err
	}
	return resp.TaskID, nil
}

// Check 问 Runtime 这个 Task 现在的状态，并记下状态与花费（每小时的花费上限按它算）。
func (h entryHost) Check(ctx context.Context, t entry.Trigger) (entry.TaskStatus, error) {
	s := h.s
	var q queryResponse
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.api.Call(cctx, s.entryIdentity(t).token, "QueryTask", map[string]string{"taskId": t.TaskID}, &q); err != nil || q.Status == "" {
		return entry.TaskStatus{}, err
	}
	_ = s.store.UpdateTasks(t.Tenant, t.WorkspaceID, func(ts []TaskRecord) []TaskRecord {
		for i := range ts {
			if ts[i].ID == t.TaskID {
				ts[i] = withQuery(ts[i], q)
			}
		}
		return ts
	})
	return entry.TaskStatus{Status: q.Status, Terminal: terminal(q.Status), Done: q.Status == "TASK_STATUS_DONE"}, nil
}

func (h entryHost) Spent(_ context.Context, t entry.Trigger) float64 {
	tasks, _ := h.s.store.Tasks(t.Tenant, t.WorkspaceID)
	for _, task := range tasks {
		if task.ID == t.TaskID {
			return task.CostUSD
		}
	}
	return 0
}

// legacyGitHubTrigger 是 EP-01 之前 github-triggers.json 里的一条记录（G-06）。
type legacyGitHubTrigger struct {
	Comment      int64     `json:"comment"`
	Repo         string    `json:"repo"`
	RepoID       int64     `json:"repo_id"`
	Installation int64     `json:"installation"`
	Issue        int       `json:"issue"`
	Author       string    `json:"author"`
	AuthorID     int64     `json:"author_id"`
	Tenant       string    `json:"tenant"`
	User         string    `json:"user"`
	ProjectID    string    `json:"project_id"`
	SessionID    string    `json:"session_id"`
	WorkspaceID  string    `json:"workspace_id"`
	TaskID       string    `json:"task_id"`
	Instruction  string    `json:"instruction"`
	IssueTitle   string    `json:"issue_title"`
	IssueBody    string    `json:"issue_body"`
	State        string    `json:"state"`
	Reason       string    `json:"reason"`
	Notified     []string  `json:"notified"`
	Attempts     int       `json:"attempts"`
	CreatedAt    time.Time `json:"created_at"`
	AskedAt      time.Time `json:"asked_at"`
}

// migrateGitHubTriggers 把旧的 github-triggers.json 搬进接入点的存储，搬完把文件改名（不再读）。可以重复执行：已有的
// 触发与投递不动，中途断了下次接着搬。说过的话记成已发出，免得搬完再说一遍；没说出去的那句放进发件箱。
func (s *Server) migrateGitHubTriggers(ctx context.Context) error {
	path := filepath.Join(s.cfg.DataDir, "github-triggers.json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	old, err := readJSONFile[[]legacyGitHubTrigger](path)
	if err != nil {
		return err
	}
	store, sink := s.entry.Store(), githubCommentSink{}.Name()
	for _, o := range old {
		origin, _ := json.Marshal(ghOrigin{Comment: o.Comment, Repo: o.Repo, RepoID: o.RepoID, Installation: o.Installation, Issue: o.Issue, Author: o.Author, AuthorID: o.AuthorID})
		t := entry.Trigger{
			ID: ghTriggerID(o.Comment), Kind: githubCommentKind{}.Name(), Scope: ghScope(o.RepoID), Tenant: o.Tenant, User: o.User, ProjectID: o.ProjectID,
			SessionID: o.SessionID, WorkspaceID: o.WorkspaceID, TaskID: o.TaskID, State: entry.State(o.State), Attempts: o.Attempts, Origin: origin,
			CreatedAt: o.CreatedAt, AskedAt: o.AskedAt,
		}
		if o.Instruction != "" {
			t.Input, _ = json.Marshal(ghInput{Instruction: o.Instruction, IssueTitle: o.IssueTitle, IssueBody: o.IssueBody})
		}
		if _, _, err := store.AddTrigger(ctx, t); err != nil {
			return err
		}
		now := s.now().UTC()
		for _, state := range o.Notified {
			phase := legacyPhase(state)
			if _, err := store.AddDelivery(ctx, entry.Delivery{Key: entry.DeliveryKey(t.ID, phase, sink), Trigger: t.ID, Sink: sink, Phase: phase,
				State: entry.DeliverySent, NextAt: now, CreatedAt: now, SentAt: now}); err != nil {
				return err
			}
		}
		if o.Reason != "" && !slices.Contains(o.Notified, o.State) {
			phase := legacyPhase(o.State)
			if _, err := store.AddDelivery(ctx, entry.Delivery{Key: entry.DeliveryKey(t.ID, phase, sink), Trigger: t.ID, Sink: sink, Phase: phase,
				Body: o.Reason, State: entry.DeliveryPending, NextAt: now, CreatedAt: now}); err != nil {
				return err
			}
		}
	}
	if err := os.Rename(path, path+".migrated"); err != nil {
		return err
	}
	log.Printf("接入点：旧的 GitHub 触发记录 %d 条已搬进新的存储", len(old))
	return nil
}

// legacyPhase 是旧记录里在某个状态说的话对应的阶段。
func legacyPhase(state string) entry.Phase {
	switch entry.State(state) {
	case entry.Running:
		return entry.PhaseAccepted
	case entry.AwaitingConfirmation:
		return entry.PhaseQuestion
	case entry.Done:
		return entry.PhaseResult
	}
	return entry.PhaseFailed
}
