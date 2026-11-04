package server

// G-04（docs/plan/github-app.md 第 4 节 G-04）：Webhook 事件到动作。每个处理函数都执行两次，结果与执行一次相同（G-02 第 6 条）。
//
//   - installation 的 deleted、suspend：停用（之后的操作提示清楚、不签发），取消相关的运行中的 Task；unsuspend、created：恢复。
//   - installation_repositories：移除的仓库操作被拒、工作区保留；加回来的恢复。
//   - repository 的 renamed、transferred：按仓库 ID 改名；deleted：标记“远端已不存在”。
//   - push：只有基线分支标记“远端有更新”，不触发 Task。
//   - pull_request、workflow_run：按 API 取当前状态再更新，迟到的旧事件不覆盖新状态。
//   - pull_request_review：记录、展示，不触发 Agent。
//   - github_app_authorization 的 revoked：删这个用户的令牌，别的用户不受影响。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentruntime/coding-agent/internal/github"
)

// cancelRecorder 是 Task API 的替身：记下 CancelTask 与 SubmitTask。
type cancelRecorder struct {
	mu      sync.Mutex
	cancels []string
	submits int
}

func (c *cancelRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case strings.HasSuffix(r.URL.Path, "/CancelTask"):
		id, _ := body["taskId"].(string)
		c.cancels = append(c.cancels, id)
		_, _ = w.Write([]byte("{}"))
	case strings.HasSuffix(r.URL.Path, "/SubmitTask"):
		c.submits++
		_, _ = w.Write([]byte("{}"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type eventFixture struct {
	*appFixture
	tasks *cancelRecorder
}

func newEventFixture(t *testing.T) *eventFixture {
	t.Helper()
	f := &eventFixture{appFixture: newAppFixture(t), tasks: &cancelRecorder{}}
	rec := httptest.NewServer(f.tasks)
	t.Cleanup(rec.Close)
	f.srv.api = NewTaskAPI(rec.URL)
	p := Project{ID: "p1", Name: "demo", Provider: "github", Repo: "octo-cat/demo", RepoID: 555, InstallationID: 100, BaseBranch: "main", CreatedBy: "octo-cat"}
	if err := f.srv.store.UpdateProjects(f.id.TenantID, func(ps []Project) ([]Project, error) { return append(ps, p), nil }); err != nil {
		t.Fatal(err)
	}
	sess := Session{ID: "s1", ProjectID: "p1", WorkspaceID: "p1--s1", CreatedBy: "octo-cat", Branch: "agent/fix", PRNumber: 7}
	if err := f.srv.store.UpdateSessions(f.id.TenantID, func(all []Session) ([]Session, error) { return append(all, sess), nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.store.UpdateTasks(f.id.TenantID, "p1--s1", func([]TaskRecord) []TaskRecord {
		return []TaskRecord{{ID: "tsk_running", CreatedBy: "octo-cat", Status: "TASK_STATUS_RUNNING"}, {ID: "tsk_done", CreatedBy: "octo-cat", Status: "TASK_STATUS_DONE"}}
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// deliver 把一个事件交给它的处理函数两次。
func (f *eventFixture) deliver(event string, payload map[string]any) {
	f.t.Helper()
	h := f.srv.GitHubHandlers()[event]
	if h == nil {
		f.t.Fatalf("%s 没有处理函数", event)
	}
	body, _ := json.Marshal(payload)
	action, _ := payload["action"].(string)
	for range 2 {
		if err := h(context.Background(), github.Delivery{ID: "d-" + event, Event: event, Action: action, Payload: body}); err != nil {
			f.t.Fatalf("%s：%v", event, err)
		}
	}
}

func (f *eventFixture) projectNow() Project {
	f.t.Helper()
	p, ok, err := f.srv.store.Project(f.id.TenantID, "p1")
	if err != nil || !ok {
		f.t.Fatalf("项目：%v %v", ok, err)
	}
	return p
}

func (f *eventFixture) sessionNow() Session {
	f.t.Helper()
	all, err := f.srv.store.Sessions(f.id.TenantID)
	if err != nil || len(all) != 1 {
		f.t.Fatalf("会话：%v %v", all, err)
	}
	return all[0]
}

func TestInstallationSuspendAndDeleteStopWorkAndCancelTasks(t *testing.T) {
	f := newEventFixture(t)
	f.deliver("installation", map[string]any{"action": "suspend", "installation": map[string]any{"id": 100}})
	p := f.projectNow()
	if p.RemoteState != "app_suspended" {
		t.Fatalf("停用：%+v", p)
	}
	for _, id := range f.tasks.cancels {
		if id != "tsk_running" {
			t.Fatalf("只取消运行中的 Task：%v", f.tasks.cancels)
		}
	}
	if len(f.tasks.cancels) == 0 {
		t.Fatal("运行中的 Task 被取消")
	}
	if _, err := f.authorize(&p, repoRead); err == nil || !strings.Contains(err.Error(), "暂停") {
		t.Fatalf("停用之后不签发，提示清楚：%v", err)
	}
	f.deliver("installation", map[string]any{"action": "unsuspend", "installation": map[string]any{"id": 100}})
	p = f.projectNow()
	if p.RemoteState != "" {
		t.Fatalf("恢复：%+v", p)
	}
	g, err := f.authorize(&p, repoRead)
	if err != nil {
		t.Fatalf("恢复后能再签发：%v", err)
	}
	g.Done()
	f.deliver("installation", map[string]any{"action": "deleted", "installation": map[string]any{"id": 100}})
	if p = f.projectNow(); p.RemoteState != "app_removed" {
		t.Fatalf("卸载：%+v", p)
	}
	if _, err := f.authorize(&p, repoRead); err == nil {
		t.Fatal("卸载之后不签发")
	}
}

func TestRemovedRepositoriesAreRefusedButTheWorkspaceStays(t *testing.T) {
	f := newEventFixture(t)
	f.deliver("installation_repositories", map[string]any{"action": "removed", "installation": map[string]any{"id": 100},
		"repositories_removed": []any{map[string]any{"id": 555, "full_name": "octo-cat/demo"}}})
	p := f.projectNow()
	if p.RemoteState != "repo_removed" {
		t.Fatalf("移除的仓库：%+v", p)
	}
	if _, err := f.authorize(&p, repoWrite); err == nil {
		t.Fatal("移除的仓库操作被拒")
	}
	f.deliver("installation_repositories", map[string]any{"action": "added", "installation": map[string]any{"id": 100},
		"repositories_added": []any{map[string]any{"id": 555, "full_name": "octo-cat/demo"}}})
	if p = f.projectNow(); p.RemoteState != "" {
		t.Fatalf("加回来之后恢复：%+v", p)
	}
}

func TestRepositoryRenamedAndDeleted(t *testing.T) {
	f := newEventFixture(t)
	f.deliver("repository", map[string]any{"action": "renamed", "repository": map[string]any{"id": 555, "full_name": "octo-cat/renamed"}})
	if p := f.projectNow(); p.Repo != "octo-cat/renamed" {
		t.Fatalf("按仓库 ID 改名：%+v", p)
	}
	f.deliver("repository", map[string]any{"action": "transferred", "repository": map[string]any{"id": 555, "full_name": "new-owner/renamed"}})
	if p := f.projectNow(); p.Repo != "new-owner/renamed" {
		t.Fatalf("转移：%+v", p)
	}
	f.deliver("repository", map[string]any{"action": "deleted", "repository": map[string]any{"id": 555, "full_name": "new-owner/renamed"}})
	if p := f.projectNow(); p.RemoteState != "remote_deleted" {
		t.Fatalf("删除后标记远端已不存在：%+v", p)
	}
}

func TestPushMarksOnlyTheBaseBranchAndStartsNoTask(t *testing.T) {
	f := newEventFixture(t)
	f.deliver("push", map[string]any{"ref": "refs/heads/feature", "repository": map[string]any{"id": 555}})
	if p := f.projectNow(); p.RemoteUpdated {
		t.Fatal("别的分支不标记")
	}
	f.deliver("push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"id": 555}})
	if p := f.projectNow(); !p.RemoteUpdated {
		t.Fatal("基线分支标记远端有更新")
	}
	if f.tasks.submits != 0 {
		t.Fatal("push 不触发 Task")
	}
}

func TestPullRequestStateComesFromTheAPI(t *testing.T) {
	f := newEventFixture(t)
	f.gh.pr = map[string]any{"number": 7, "state": "closed", "merged_at": "2026-11-03T10:00:00Z", "updated_at": "2026-11-03T10:00:00Z"}
	// 迟到的旧事件说的是“打开了”：按 API 取到的当前状态是已合并。
	f.deliver("pull_request", map[string]any{"action": "opened", "number": 7, "pull_request": map[string]any{"number": 7, "state": "open"},
		"repository": map[string]any{"id": 555, "full_name": "octo-cat/demo"}, "installation": map[string]any{"id": 100}})
	if s := f.sessionNow(); s.PRState != "merged" {
		t.Fatalf("按 API 的当前状态：%+v", s)
	}
}

func TestWorkflowRunLateEventsDoNotOverwrite(t *testing.T) {
	f := newEventFixture(t)
	newer := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	run := func(status, conclusion string, at time.Time) map[string]any {
		return map[string]any{"action": "completed", "workflow_run": map[string]any{"id": 9, "head_branch": "agent/fix", "status": status, "conclusion": conclusion, "updated_at": at.Format(time.RFC3339)},
			"repository": map[string]any{"id": 555, "full_name": "octo-cat/demo"}, "installation": map[string]any{"id": 100}}
	}
	f.deliver("workflow_run", run("completed", "success", newer)) // App 没有 Actions 权限（替身回 403）：用事件里的
	if s := f.sessionNow(); s.CI == nil || s.CI.Conclusion != "success" {
		t.Fatalf("记下 CI 状态：%+v", s.CI)
	}
	f.deliver("workflow_run", run("in_progress", "", newer.Add(-time.Hour)))
	if s := f.sessionNow(); s.CI.Conclusion != "success" || s.CI.Status != "completed" {
		t.Fatalf("迟到的旧事件不覆盖：%+v", s.CI)
	}
	f.gh.run = map[string]any{"id": 9, "head_branch": "agent/fix", "status": "completed", "conclusion": "failure", "updated_at": newer.Add(time.Hour).Format(time.RFC3339)}
	f.deliver("workflow_run", run("completed", "success", newer))
	if s := f.sessionNow(); s.CI.Conclusion != "failure" {
		t.Fatalf("能读 API 时按 API 的当前状态：%+v", s.CI)
	}
}

func TestReviewsAreRecordedOnceAndStartNoTask(t *testing.T) {
	f := newEventFixture(t)
	f.deliver("pull_request_review", map[string]any{"action": "submitted", "review": map[string]any{"id": 31, "state": "changes_requested", "user": map[string]any{"login": "reviewer"}, "submitted_at": "2026-11-03T11:00:00Z"},
		"pull_request": map[string]any{"number": 7}, "repository": map[string]any{"id": 555}})
	s := f.sessionNow()
	if len(s.Reviews) != 1 || s.Reviews[0].State != "changes_requested" || s.Reviews[0].User != "reviewer" {
		t.Fatalf("记录一次（处理两次也只有一条）：%+v", s.Reviews)
	}
	if f.tasks.submits != 0 {
		t.Fatal("评审不触发 Agent")
	}
}

func TestAppAuthorizationRevokedDeletesOnlyThatUsersToken(t *testing.T) {
	f := newEventFixture(t)
	other := identity{}
	other.TenantID, other.UserID = "gh-someone", "someone"
	conn := Connection{Provider: "github", RemoteID: "43", Login: "someone", App: true}
	if _, err := f.srv.updateAccount(other.TenantID, other.UserID, func(a *Account) error {
		if err := f.srv.sealConnection(other.TenantID, other.UserID, &conn, tokenSet{Access: "ghu_other"}); err != nil {
			return err
		}
		a.Connections["github"] = conn
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]identityRef{"github:42": {Tenant: f.id.TenantID, User: f.id.UserID}, "github:43": {Tenant: other.TenantID, User: other.UserID}}
	if err := writeFileAtomic(f.srv.identitiesFile(), ids); err != nil {
		t.Fatal(err)
	}
	f.deliver("github_app_authorization", map[string]any{"action": "revoked", "sender": map[string]any{"id": 42, "login": "octo-cat"}})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, _, err := f.srv.connToken(req, f.id, f.srv.providers["github"]); err != errNotConnected {
		t.Fatalf("这个用户要重新授权：%v", err)
	}
	if _, tok, err := f.srv.connToken(req, other, f.srv.providers["github"]); err != nil || tok != "ghu_other" {
		t.Fatalf("别的用户不受影响：%q %v", tok, err)
	}
}
