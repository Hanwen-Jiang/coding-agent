package server

// G-03（docs/plan/github-app.md 第 3 节“一次操作的授权”、第 4 节 G-03）：身份、安装与令牌。替身 GitHub 在下面。
//
//  1. 伪造 Setup URL 的 installation_id（用户看不到这个安装）：不关联；看得到的才关联。
//  2. 安装覆盖了仓库、但用户本人没权限：拒绝，不调签发接口。
//  3. 只有读权限的用户：能克隆（读），推送与开 PR 被拒，提示写清原因与去哪儿调整。
//  4. 安装被暂停或没装：不签发（没装时给出安装入口）。
//  5. 签发请求体只有这个仓库的 ID 与最小权限；令牌不落盘、不进日志；结束时吊销，吊销失败不影响结果。
//  6. App 的 JWT 复用到快过期，过期前换新的。
//  7. 10 个并发请求同时遇到用户令牌到期：只续一次，全部成功。续不上时报“要重新授权”，不发空凭据。
//  10. 旧工作区第一次使用时补上仓库 ID 与安装 ID；仓库改名后照常工作（按 ID 取当前名字）。
//  11. 旧 OAuth 的连接在切换期内照用（不经 App）；经 App 重新连接后旧令牌被替换掉。
//  （第 8 条 Workspace 操作的环境与 Agent 的命令不共用、第 9 条 .git/config 拿不到令牌在 Runtime 仓库验。）

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAppGitHub 是 GitHub App 一侧的替身：用户令牌的续期（续期令牌一次性）、用户能看到的安装、仓库与权限、安装令牌的签发与吊销。
type fakeAppGitHub struct {
	mu          sync.Mutex
	server      *httptest.Server
	refreshes   int
	failRefresh bool
	refreshOK   map[string]bool
	issued      []map[string]any
	issuedTo    []string
	revoked     []string
	failRevoke  bool
	jwts        map[string]bool
	names       map[int]string // 仓库 ID → 当前全名（改名用）
	// pr 是 GET /repos/{repo}/pulls/{n} 的当前状态（G-04）；run 是 GET /repos/{repo}/actions/runs/{id}，nil 时回 403（App 没有 Actions 权限）。
	pr  map[string]any
	run map[string]any
	// G-05：GET /app/installations 的安装列表、GET /installation/repositories 的仓库（按安装 ID），failIssue 时签发回 500。
	installations []map[string]any
	instRepos     map[int][]map[string]any
	failIssue     bool
	// instOf 覆盖 GET /repos/{repo}/installation：值是这个仓库现在的安装 ID，0 时回 404（卸载了，或仓库不在任何安装里）。
	instOf map[string]int
	// G-06：各登录名在仓库上的权限（collaborators/{login}/permission，没有时为 read）、用安装令牌发的评论与开的 PR。
	perms    map[string]string
	comments []fakeComment
	pulls    []map[string]any
}

type fakeComment struct{ Path, Body, Token string }

func newFakeAppGitHub(t *testing.T) *fakeAppGitHub {
	f := &fakeAppGitHub{refreshOK: map[string]bool{"ghr_1": true}, jwts: map[string]bool{}, names: map[int]string{555: "octo-cat/demo", 556: "octo-cat/readonly", 557: "octo-cat/suspended", 558: "octo-cat/noinstall"}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAppGitHub) repoByName(name string) (int, bool) {
	for id, n := range f.names {
		if n == name {
			return id, true
		}
	}
	return 0, false
}

func (f *fakeAppGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	isJWT := strings.Count(auth, ".") == 2
	if isJWT {
		f.jwts[auth] = true
	}
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/login/oauth/access_token":
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ghu_app_new", "refresh_token": "ghr_new", "expires_in": 28800, "token_type": "bearer"})
			return
		}
		f.refreshes++
		old := r.Form.Get("refresh_token")
		if f.failRefresh || !f.refreshOK[old] || r.Form.Get("client_id") != "Iv1.client" || r.Form.Get("client_secret") != "app-client-secret" {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_refresh_token"})
			return
		}
		delete(f.refreshOK, old) // 一次性
		next := fmt.Sprintf("ghr_%d", f.refreshes+1)
		f.refreshOK[next] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("ghu_user_%d", f.refreshes+1), "refresh_token": next, "expires_in": 28800, "token_type": "bearer"})
	case path == "/user":
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "octo-cat", "id": 42, "name": "Octo"})
	case path == "/user/installations":
		if !strings.HasPrefix(auth, "ghu_") {
			notFound()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "installations": []map[string]any{{"id": 100, "account": map[string]any{"login": "octo-cat"}}}})
	case strings.HasPrefix(path, "/repositories/"):
		var id int
		_, _ = fmt.Sscanf(strings.TrimPrefix(path, "/repositories/"), "%d", &id)
		if name, ok := f.names[id]; ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "full_name": name})
			return
		}
		notFound()
	case strings.HasPrefix(path, "/repos/") && strings.HasSuffix(path, "/installation"):
		name := strings.TrimSuffix(strings.TrimPrefix(path, "/repos/"), "/installation")
		if !isJWT {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if id, ok := f.instOf[name]; ok {
			if id == 0 {
				notFound()
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "suspended_at": nil})
			return
		}
		switch {
		case name == "octo-cat/suspended":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 101, "suspended_at": "2026-11-01T00:00:00Z"})
		case strings.HasPrefix(name, "octo-cat/") && name != "octo-cat/noinstall":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 100, "suspended_at": nil})
		default:
			notFound()
		}
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/") && strings.Contains(path, "/collaborators/") && strings.HasSuffix(path, "/permission"):
		if !strings.HasPrefix(auth, "ghs_") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		login := strings.TrimSuffix(path[strings.Index(path, "/collaborators/")+len("/collaborators/"):], "/permission")
		perm := f.perms[login]
		if perm == "" {
			perm = "read"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"permission": perm, "user": map[string]any{"login": login}})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/repos/") && strings.Contains(path, "/issues/") && strings.HasSuffix(path, "/comments"):
		if !strings.HasPrefix(auth, "ghs_") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.comments = append(f.comments, fakeComment{Path: path, Body: body.Body, Token: auth})
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1000 + len(f.comments)})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/repos/") && strings.HasSuffix(path, "/pulls"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["token"] = auth
		f.pulls = append(f.pulls, body)
		n := 90 + len(f.pulls)
		repo := strings.TrimSuffix(strings.TrimPrefix(path, "/repos/"), "/pulls")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"number": n, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d", repo, n)})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/") && strings.Contains(path, "/pulls/"):
		_ = json.NewEncoder(w).Encode(f.pr)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/") && strings.Contains(path, "/actions/runs/"):
		if f.run == nil {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Resource not accessible by integration"})
			return
		}
		_ = json.NewEncoder(w).Encode(f.run)
	case strings.HasPrefix(path, "/repos/") && r.Method == http.MethodGet:
		name := strings.TrimPrefix(path, "/repos/")
		id, ok := f.repoByName(name)
		if !ok || name == "octo-cat/private-other" || !strings.HasPrefix(auth, "ghu_") {
			notFound()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "full_name": name, "private": true, "permissions": map[string]bool{"pull": true, "push": name != "octo-cat/readonly"}})
	case r.Method == http.MethodGet && path == "/app/installations":
		if !isJWT {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(f.installations)
	case r.Method == http.MethodGet && path == "/installation/repositories":
		// 令牌是哪个安装签的：按签发的顺序找回安装 ID。
		inst := 0
		for i, to := range f.issuedTo {
			if fmt.Sprintf("ghs_inst_secret_%d", i+1) == auth {
				_, _ = fmt.Sscanf(strings.TrimPrefix(to, "/app/installations/"), "%d", &inst)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(f.instRepos[inst]), "repositories": f.instRepos[inst]})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/app/installations/") && strings.HasSuffix(path, "/access_tokens"):
		if !isJWT {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.failIssue {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "boom"})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.issued = append(f.issued, body)
		f.issuedTo = append(f.issuedTo, path)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": fmt.Sprintf("ghs_inst_secret_%d", len(f.issued)), "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
	case r.Method == http.MethodDelete && path == "/installation/token":
		f.revoked = append(f.revoked, auth)
		if f.failRevoke {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		notFound()
	}
}

type appFixture struct {
	t     *testing.T
	gh    *fakeAppGitHub
	srv   *Server
	ts    *httptest.Server
	cfg   Config
	clock *fakeNow
	id    identity
}

func newAppFixture(t *testing.T) *appFixture {
	t.Helper()
	gh := newFakeAppGitHub(t)
	keyFile, _ := writeAppKey(t)
	env := appEnv(keyFile)
	env["CODING_GITHUB_APP_CLIENT_SECRET"] = "app-client-secret"
	env["CODING_GITHUB_APP_CLIENT_ID"] = "Iv1.client"
	app, err := GitHubAppFromEnv(envFunc(env))
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeNow{t: time.Now()}
	ts, _, cfg, srv := newServerWith(t, func(c *Config) {
		c.GitHubApp, c.Now = app, clock.now
		c.Providers = map[string]ProviderConfig{"github": {ClientID: "oauth-client", ClientSecret: "secret"}}
		c.PublicURL = "http://placeholder"
	})
	srv.cfg.PublicURL = ts.URL
	pointProvider(srv, "github", gh.server.URL)
	f := &appFixture{t: t, gh: gh, srv: srv, ts: ts, cfg: cfg, clock: clock, id: identity{}}
	f.id.TenantID, f.id.UserID = "gh-octo-cat", "octo-cat"
	f.connect(true, clock.now().Add(8*time.Hour))
	return f
}

// connect 给账号放一个 GitHub 连接：viaApp 时是经 App 授权的用户令牌（会过期、带续期令牌），否则是旧 OAuth App 的令牌。
func (f *appFixture) connect(viaApp bool, expires time.Time) {
	f.t.Helper()
	conn := Connection{Provider: "github", RemoteID: "42", Login: "octo-cat", App: viaApp}
	t := tokenSet{Access: "gho_legacy_oauth"}
	if viaApp {
		t = tokenSet{Access: "ghu_user_1", Refresh: "ghr_1", ExpiresAt: expires}
	}
	if _, err := f.srv.updateAccount(f.id.TenantID, f.id.UserID, func(a *Account) error {
		if err := f.srv.sealConnection(f.id.TenantID, f.id.UserID, &conn, t); err != nil {
			return err
		}
		a.Connections["github"] = conn
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *appFixture) project(repo string) *Project {
	return &Project{ID: "p1", Provider: "github", Repo: repo, BaseBranch: "main"}
}

func (f *appFixture) authorize(p *Project, need repoAccess) (repoGrant, error) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	return f.srv.authorizeRepo(req, f.id, p, need)
}

func TestSetupURLOnlyLinksInstallationsTheUserCanSee(t *testing.T) {
	f := newAppFixture(t)
	c := newClient(t, f.ts.URL)
	sid, err := f.srv.sessions.create(f.id.TenantID, f.id.UserID)
	if err != nil {
		t.Fatal(err)
	}
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(installation string) string {
		req, _ := http.NewRequest(http.MethodGet, f.ts.URL+"/api/github/setup?setup_action=install&installation_id="+installation, nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: sid})
		res, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.Header.Get("Location")
	}
	if loc := get("999"); !strings.Contains(loc, "error=") {
		t.Fatalf("伪造的 installation_id 不关联：%s", loc)
	}
	a, _ := f.srv.readAccount(f.id.TenantID, f.id.UserID)
	if len(a.GitHubInstallations) != 0 {
		t.Fatalf("没有关联：%v", a.GitHubInstallations)
	}
	if loc := get("100"); strings.Contains(loc, "error=") {
		t.Fatalf("用户看得到的安装关联上：%s", loc)
	}
	a, _ = f.srv.readAccount(f.id.TenantID, f.id.UserID)
	if len(a.GitHubInstallations) != 1 || a.GitHubInstallations[0] != 100 {
		t.Fatalf("关联了 100：%v", a.GitHubInstallations)
	}
}

func TestNoTokenIsIssuedWithoutTheUsersOwnPermission(t *testing.T) {
	f := newAppFixture(t)
	if _, err := f.authorize(f.project("octo-cat/private-other"), repoRead); err == nil {
		t.Fatal("用户自己看不到的仓库要拒绝")
	}
	_, err := f.authorize(f.project("octo-cat/readonly"), repoWrite)
	var ge *GitError
	if !errors.As(err, &ge) || ge.Status != http.StatusForbidden || !strings.Contains(ge.Error(), "只有读权限") {
		t.Fatalf("只有读权限的不能推送，提示写清原因：%v", err)
	}
	if len(f.gh.issued) != 0 {
		t.Fatalf("没权限时不调签发接口：%v", f.gh.issued)
	}
	g, err := f.authorize(f.project("octo-cat/readonly"), repoRead)
	if err != nil || g.Auth.User != "x-access-token" {
		t.Fatalf("只有读权限的能克隆：%+v %v", g, err)
	}
	g.Done()
	if perms := f.gh.issued[0]["permissions"].(map[string]any); perms["contents"] != "read" || len(perms) != 1 {
		t.Fatalf("读只要 contents:read：%v", perms)
	}
}

func TestSuspendedOrMissingInstallationsGetNoToken(t *testing.T) {
	f := newAppFixture(t)
	if _, err := f.authorize(f.project("octo-cat/suspended"), repoRead); err == nil || !strings.Contains(err.Error(), "暂停") {
		t.Fatalf("安装被暂停时不签发：%v", err)
	}
	_, err := f.authorize(f.project("octo-cat/noinstall"), repoRead)
	if err == nil || !strings.Contains(err.Error(), "github.com/apps/coding-agent-link/installations/new") {
		t.Fatalf("没装 App 时给出安装入口：%v", err)
	}
	if len(f.gh.issued) != 0 {
		t.Fatalf("不签发：%v", f.gh.issued)
	}
}

func TestInstallationTokensAreMinimalNeverStoredAndRevoked(t *testing.T) {
	f := newAppFixture(t)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	g, err := f.authorize(f.project("octo-cat/demo"), repoWrite)
	if err != nil {
		t.Fatal(err)
	}
	if g.Auth.User != "x-access-token" || !strings.HasPrefix(g.Auth.Pass, "ghs_inst_secret_") || g.UserToken != "ghu_user_1" {
		t.Fatalf("推送用安装令牌、开 PR 用用户令牌：%+v", g)
	}
	body := f.gh.issued[0]
	ids, _ := body["repository_ids"].([]any)
	perms, _ := body["permissions"].(map[string]any)
	if len(body) != 2 || len(ids) != 1 || ids[0] != float64(555) || len(perms) != 1 || perms["contents"] != "write" {
		t.Fatalf("签发请求只有这个仓库的 ID 与最小权限：%v", body)
	}
	if f.gh.issuedTo[0] != "/app/installations/100/access_tokens" {
		t.Fatalf("签给这个仓库的安装：%s", f.gh.issuedTo[0])
	}
	f.gh.failRevoke = true
	g.Done()
	if len(f.gh.revoked) != 1 || f.gh.revoked[0] != g.Auth.Pass {
		t.Fatalf("结束时吊销：%v", f.gh.revoked)
	}
	_ = filepath.WalkDir(f.cfg.DataDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(path); bytes.Contains(data, []byte("ghs_inst_secret")) || bytes.Contains(data, []byte("ghu_user_1")) {
				t.Errorf("令牌落盘了：%s", path)
			}
		}
		return nil
	})
	if strings.Contains(logs.String(), "ghs_inst_secret") || strings.Contains(logs.String(), "ghu_user") {
		t.Fatal("令牌进了日志")
	}
}

func TestAppJWTIsReusedUntilNearExpiry(t *testing.T) {
	f := newAppFixture(t)
	for range 3 {
		g, err := f.authorize(f.project("octo-cat/demo"), repoRead)
		if err != nil {
			t.Fatal(err)
		}
		g.Done()
	}
	if n := len(f.gh.jwts); n != 1 {
		t.Fatalf("快过期之前复用同一个 JWT：%d 个", n)
	}
	f.clock.advance(9 * time.Minute)
	g, err := f.authorize(f.project("octo-cat/demo"), repoRead)
	if err != nil {
		t.Fatal(err)
	}
	g.Done()
	if n := len(f.gh.jwts); n != 2 {
		t.Fatalf("快过期时换新的：%d 个", n)
	}
}

func TestConcurrentRequestsRefreshTheUserTokenOnce(t *testing.T) {
	f := newAppFixture(t)
	f.connect(true, f.clock.now().Add(time.Minute)) // 两分钟内到期：要续
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := f.authorize(f.project("octo-cat/demo"), repoRead)
			if err == nil {
				g.Done()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("全部成功：%v", err)
		}
	}
	if f.gh.refreshes != 1 {
		t.Fatalf("只续一次（续期令牌一次性）：%d", f.gh.refreshes)
	}

	f.connect(true, f.clock.now().Add(time.Minute))
	f.gh.failRefresh = true
	issued := len(f.gh.issued)
	_, err := f.authorize(f.project("octo-cat/demo"), repoRead)
	if err == nil || !strings.Contains(err.Error(), "重新授权") {
		t.Fatalf("续不上时报要重新授权：%v", err)
	}
	if len(f.gh.issued) != issued {
		t.Fatal("续不上时不签发，也不发空凭据")
	}
}

func TestRepoIDIsBackfilledAndRenamesKeepWorking(t *testing.T) {
	f := newAppFixture(t)
	p := f.project("octo-cat/demo")
	if err := f.srv.store.UpdateProjects(f.id.TenantID, func(ps []Project) ([]Project, error) { return append(ps, *p), nil }); err != nil {
		t.Fatal(err)
	}
	g, err := f.authorize(p, repoRead)
	if err != nil {
		t.Fatal(err)
	}
	g.Done()
	saved, _ := f.srv.store.Projects(f.id.TenantID)
	if p.RepoID != 555 || p.InstallationID != 100 || len(saved) != 1 || saved[0].RepoID != 555 {
		t.Fatalf("第一次使用时补上仓库 ID 与安装 ID：%+v %+v", p, saved)
	}
	f.gh.mu.Lock()
	f.gh.names[555] = "octo-cat/renamed"
	f.gh.mu.Unlock()
	g, err = f.authorize(p, repoWrite)
	if err != nil {
		t.Fatalf("改名后照常工作：%v", err)
	}
	g.Done()
	saved, _ = f.srv.store.Projects(f.id.TenantID)
	if p.Repo != "octo-cat/renamed" || saved[0].Repo != "octo-cat/renamed" {
		t.Fatalf("按仓库 ID 取到当前的名字：%+v", saved)
	}
}

func TestLegacyOAuthConnectionsKeepWorkingUntilReconnected(t *testing.T) {
	f := newAppFixture(t)
	f.connect(false, time.Time{})
	g, err := f.authorize(f.project("octo-cat/demo"), repoWrite)
	if err != nil || g.Auth.Pass != "x-oauth-basic" || g.Auth.User != "gho_legacy_oauth" || g.UserToken != "gho_legacy_oauth" {
		t.Fatalf("切换期内旧 OAuth 的连接照用：%+v %v", g, err)
	}
	g.Done()
	if len(f.gh.issued) != 0 {
		t.Fatal("旧连接不经 App 签发")
	}
	if !f.srv.legacyGitHub(f.id) {
		t.Fatal("旧连接要提示重新连接")
	}
	f.connect(true, f.clock.now().Add(8*time.Hour))
	a, _ := f.srv.readAccount(f.id.TenantID, f.id.UserID)
	if tok, _ := f.srv.open(a.Connections["github"].Token, tokenAAD(f.id.TenantID, f.id.UserID, "github")); tok != "ghu_user_1" || !a.Connections["github"].App {
		t.Fatalf("重新连接后旧令牌被替换：%q", tok)
	}
	if f.srv.legacyGitHub(f.id) {
		t.Fatal("重新连接之后不再提示")
	}
}
