package server

// G-05（docs/plan/github-app.md 第 4 节 G-05）的服务端：
//
//  1. 指标（不带负载）：续期失败、签发失败计数（收件箱的投递数、结果、积压与死信在 internal/github 里测）。
//  2. 对账：按 GET /app/installations 与各安装的仓库列表纠正项目的远端状况（覆盖超过 3 天的停机：漏掉的事件对账补上）。
//  3. 给界面用的状态接口：连接状态、是不是旧的 OAuth 连接、已关联的安装、安装入口、各项目的远端状况。
//  4. 卸载：关联到账号的这个安装立即去掉。
//  5. 卸载后重新安装（新的安装 ID）、移除后又加回仓库：按 API 的当前状态恢复，不被记下的旧状态卡住（事件只当提示）。

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type metricsRecorder struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (m *metricsRecorder) Count(name string, n int64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts == nil {
		m.counts = map[string]int64{}
	}
	m.counts[name] += n
}

func (m *metricsRecorder) Observe(string, float64, map[string]string) {}

func (m *metricsRecorder) get(name string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[name]
}

func TestRefreshAndIssueFailuresAreCounted(t *testing.T) {
	f := newAppFixture(t)
	rec := &metricsRecorder{}
	f.srv.cfg.Metrics = rec
	f.gh.failIssue = true
	if _, err := f.authorize(f.project("octo-cat/demo"), repoRead); err == nil {
		t.Fatal("签发失败时报错")
	}
	if rec.get("coding.github.issue_failures") != 1 {
		t.Fatalf("签发失败计数：%v", rec.counts)
	}
	f.gh.failIssue = false
	f.connect(true, f.clock.now().Add(time.Minute))
	f.gh.failRefresh = true
	if _, err := f.authorize(f.project("octo-cat/demo"), repoRead); err == nil {
		t.Fatal("续不上时报错")
	}
	if rec.get("coding.github.token_refresh_failures") != 1 {
		t.Fatalf("续期失败计数：%v", rec.counts)
	}
}

func TestReconcileInstallationsCorrectsRemoteState(t *testing.T) {
	f := newEventFixture(t)
	more := []Project{
		{ID: "p2", Name: "gone", Provider: "github", Repo: "octo-cat/gone", RepoID: 600, InstallationID: 200},
		{ID: "p3", Name: "dropped", Provider: "github", Repo: "octo-cat/readonly", RepoID: 556, InstallationID: 100},
		{ID: "p4", Name: "back", Provider: "github", Repo: "octo-cat/suspended", RepoID: 557, InstallationID: 101, RemoteState: "app_suspended"},
	}
	if err := f.srv.store.UpdateProjects(f.id.TenantID, func(ps []Project) ([]Project, error) { return append(ps, more...), nil }); err != nil {
		t.Fatal(err)
	}
	f.gh.installations = []map[string]any{
		{"id": 100, "suspended_at": nil},
		{"id": 101, "suspended_at": nil},
	}
	f.gh.instRepos = map[int][]map[string]any{
		100: {{"id": 555, "full_name": "octo-cat/demo"}},
		101: {{"id": 557, "full_name": "octo-cat/suspended"}},
	}
	f.gh.instOf = map[string]int{"octo-cat/gone": 0, "octo-cat/readonly": 0} // 不在任何安装里
	for range 2 {
		if err := f.srv.ReconcileGitHubInstallations(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	ps, _ := f.srv.store.Projects(f.id.TenantID)
	state := map[string]string{}
	for _, p := range ps {
		state[p.ID] = p.RemoteState
	}
	want := map[string]string{"p1": "", "p2": "app_removed", "p3": "repo_removed", "p4": ""}
	for id, w := range want {
		if state[id] != w {
			t.Fatalf("%s：应为 %q，实际 %q（%v）", id, w, state[id], state)
		}
	}
	f.gh.installations[0]["suspended_at"] = "2026-11-03T00:00:00Z"
	if err := f.srv.ReconcileGitHubInstallations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := f.srv.store.Project(f.id.TenantID, "p1"); p.RemoteState != "app_suspended" {
		t.Fatalf("暂停的安装：%+v", p)
	}
	for _, tok := range f.gh.revoked {
		if tok == "" {
			t.Fatal("对账用的安装令牌也吊销")
		}
	}
	if len(f.gh.revoked) == 0 {
		t.Fatal("对账用的安装令牌用完吊销")
	}
}

func TestGitHubStatusForTheAccountPage(t *testing.T) {
	f := newEventFixture(t)
	if _, err := f.srv.updateAccount(f.id.TenantID, f.id.UserID, func(a *Account) error {
		a.GitHubInstallations = []int64{100}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sid, err := f.srv.sessions.create(f.id.TenantID, f.id.UserID)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, f.ts.URL+"/api/github/status", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: sid})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Slug          string  `json:"slug"`
		InstallURL    string  `json:"install_url"`
		Connected     bool    `json:"connected"`
		Legacy        bool    `json:"legacy"`
		Installations []int64 `json:"installations"`
		Projects      []struct {
			ID          string `json:"id"`
			Repo        string `json:"repo"`
			RemoteState string `json:"remote_state"`
		} `json:"projects"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&out) != nil {
		t.Fatalf("状态接口：%d", res.StatusCode)
	}
	if out.Slug != "coding-agent-link" || out.InstallURL != "https://github.com/apps/coding-agent-link/installations/new" || !out.Connected || out.Legacy ||
		!slices.Equal(out.Installations, []int64{100}) || len(out.Projects) != 1 || out.Projects[0].Repo != "octo-cat/demo" {
		t.Fatalf("状态：%+v", out)
	}
}

func TestReinstallingTheAppRestoresAccess(t *testing.T) {
	f := newEventFixture(t)
	f.gh.instOf = map[string]int{"octo-cat/demo": 0}
	f.deliver("installation", map[string]any{"action": "deleted", "installation": map[string]any{"id": 100}})
	p := f.projectNow()
	if _, err := f.authorize(&p, repoRead); err == nil || !strings.Contains(err.Error(), "卸载") {
		t.Fatalf("卸载之后不签发：%v", err)
	}
	// 重新安装：GitHub 给的是新的安装 ID。
	f.gh.instOf["octo-cat/demo"] = 300
	p = f.projectNow()
	g, err := f.authorize(&p, repoWrite)
	if err != nil {
		t.Fatalf("重新安装之后能再签发：%v", err)
	}
	g.Done()
	if p = f.projectNow(); p.InstallationID != 300 || p.RemoteState != "" {
		t.Fatalf("改记新的安装、清掉卸载的标记：%+v", p)
	}
	if last := f.gh.issuedTo[len(f.gh.issuedTo)-1]; last != "/app/installations/300/access_tokens" {
		t.Fatalf("按新的安装签发：%s", last)
	}
}

func TestRepositoryAddedBackRestoresAccessEvenIfTheEventWasMissed(t *testing.T) {
	f := newEventFixture(t)
	f.deliver("installation_repositories", map[string]any{"action": "removed", "installation": map[string]any{"id": 100},
		"repositories_removed": []any{map[string]any{"id": 555, "full_name": "octo-cat/demo"}}})
	// 加回仓库的事件丢了：GitHub 现在又能看到这个仓库的安装。
	p := f.projectNow()
	g, err := f.authorize(&p, repoWrite)
	if err != nil {
		t.Fatalf("按 API 的当前状态签发：%v", err)
	}
	g.Done()
	if p = f.projectNow(); p.RemoteState != "" {
		t.Fatalf("清掉移除的标记：%+v", p)
	}
}

func TestReconcileAdoptsTheNewInstallationAfterMissedEvents(t *testing.T) {
	f := newEventFixture(t)
	// 停机超过 3 天：卸载与重新安装的事件都错过了，项目还记着旧的安装 100。
	f.gh.instOf = map[string]int{"octo-cat/demo": 300}
	f.gh.installations = []map[string]any{{"id": 300, "suspended_at": nil}}
	f.gh.instRepos = map[int][]map[string]any{300: {{"id": 555, "full_name": "octo-cat/demo"}}}
	for range 2 {
		if err := f.srv.ReconcileGitHubInstallations(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if p := f.projectNow(); p.InstallationID != 300 || p.RemoteState != "" {
		t.Fatalf("对账改记现在的安装：%+v", p)
	}
	// 真卸载了（GitHub 上查不到这个仓库的安装）：标记卸载，安装 ID 不动。
	f.gh.instOf["octo-cat/demo"] = 0
	f.gh.installations = []map[string]any{}
	if err := f.srv.ReconcileGitHubInstallations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := f.projectNow(); p.InstallationID != 300 || p.RemoteState != "app_removed" {
		t.Fatalf("卸载：%+v", p)
	}
}

func TestUninstallUnlinksTheInstallationRightAway(t *testing.T) {
	f := newEventFixture(t)
	if _, err := f.srv.updateAccount(f.id.TenantID, f.id.UserID, func(a *Account) error {
		a.GitHubInstallations = []int64{100, 300}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.deliver("installation", map[string]any{"action": "deleted", "installation": map[string]any{"id": 100}})
	a, _ := f.srv.readAccount(f.id.TenantID, f.id.UserID)
	if !slices.Equal(a.GitHubInstallations, []int64{300}) {
		t.Fatalf("卸载的安装立即去掉：%v", a.GitHubInstallations)
	}
}
