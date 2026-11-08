package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Workspace 界面（任务 23）与右栏“环境”在真正的页面里、两种语言下各走一遍：
// 环境配置与 Runtime 的环境记录（就绪只看 ready、读不到时 blocked_external）、拉取基线分支（sync）、文件树标出改动并跳到 diff、
// 提交记录标出本会话的提交与会话起点、复制会话（copy）。界面文字取自 i18n.js 的 t()，所以中英文都核对到了字典。
func TestWorkspaceUIInBrowser(t *testing.T) {
	browser := environmentTestBrowser()
	if browser == "" {
		t.Skip("Chrome/Edge not found; set CODING_TEST_BROWSER")
	}
	for _, lang := range []string{"zh", "en"} {
		t.Run(lang, func(t *testing.T) { runWorkspaceUIProbe(t, browser, lang) })
	}
}

func runWorkspaceUIProbe(t *testing.T, browser, lang string) {
	ts, fake, cfg, app := newServerWith(t, nil)
	app.git.AllowFileProtocol = true // 远端是本机的裸仓库
	c := newClient(t, ts.URL)
	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	api, ws, bare := publishable(t, c, cfg, app, "acme", "github", "o/demo")
	parts := strings.Split(api, "/")
	pid, sid := parts[3], parts[5]
	if _, err := app.updateProject("acme", pid, func(p *Project) { p.Source = "https://example.invalid/o/demo.git" }); err != nil {
		t.Fatal(err)
	}
	_, env, _ := c.do("POST", "/api/environments", map[string]any{"name": "Py env"})
	sess, err := app.updateSession("acme", Session{ProjectID: pid, ID: sid}, func(x *Session) {
		x.EnvironmentID, x.EnvironmentHandoff, x.RuntimeSessionID = env["id"].(string), handoffPending, "ses_ui"
	})
	if err != nil {
		t.Fatal(err)
	}
	// 会话开出时记下的环境配置快照（这里的会话是先开、后补上环境的）。
	stored, _, _ := app.store.Environment("acme", env["id"].(string))
	if err := app.store.PutSnapshot("acme", sess.WorkspaceID, snapshotOf(stored, time.Now())); err != nil {
		t.Fatal(err)
	}
	fake.envReply = map[string]any{"environments": []any{
		map[string]any{"envRef": "sbe-now", "state": "ENVIRONMENT_STATE_READY", "ready": true, "reused": true, "reusedAt": "2026-11-03T01:10:00Z",
			"healthConfirmedAt": "2026-11-03T01:00:05Z", "expiresAt": "2026-11-03T01:41:00Z", "maxUntil": "2026-11-03T05:00:00Z", "createdAt": "2026-11-03T01:00:00Z"},
		map[string]any{"envRef": "sbe-old", "state": "ENVIRONMENT_STATE_READY", "createdAt": "2026-11-03T00:00:00Z"},
	}}
	// 远端的 main 多了一个提交：页面上“拉取 main”之后会话的基线提交应是它。
	upstream := filepath.Join(t.TempDir(), "up")
	plain := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=u", "-c", "user.email=u@example.com"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v：%v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	plain(filepath.Dir(upstream), "clone", "-q", bare, upstream)
	plain(upstream, "commit", "-q", "--allow-empty", "-m", "upstream change")
	plain(upstream, "push", "-q", "origin", "main")
	head := plain(upstream, "rev-parse", "HEAD")

	consts, _ := json.Marshal(map[string]string{"pid": pid, "sid": sid, "head": head, "env": "Py env"})
	probe := "const C = " + string(consts) + ";\n" + workspaceUIProbe
	result := make(chan string, 1)
	handler := app.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__probe.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = io.WriteString(w, probe)
		case "/__write": // 在工作副本里改一个文件（Agent 改的那样）
			_ = os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("changed by the agent\n"), 0o644)
			w.WriteHeader(http.StatusNoContent)
		case "/__env_blocked": // Runtime 说环境状态暂时读不到
			fake.envStatus = http.StatusServiceUnavailable
			fake.envBody = map[string]any{"code": "unavailable", "message": "x",
				"details": []any{map[string]any{"type": "google.rpc.ErrorInfo", "debug": map[string]any{"reason": "environment_status_unavailable", "domain": errorInfoDomain}}}}
			w.WriteHeader(http.StatusNoContent)
		case "/__result":
			data, _ := io.ReadAll(r.Body)
			select {
			case result <- string(data):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		case "/":
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			for k, values := range rec.Header() {
				w.Header()[k] = values
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: app.ingress.IssueToken("acme", "alice", time.Hour), Path: "/", HttpOnly: true})
			http.SetCookie(w, &http.Cookie{Name: langCookie, Value: lang, Path: "/"})
			_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", `<script type="module" src="/__probe.js"></script></body>`, 1))
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	profile, err := os.MkdirTemp("", "coding-web-browser")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for range 50 {
			if os.RemoveAll(profile) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser, "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run", "--no-default-browser-check",
		"--window-size=1440,1000", "--user-data-dir="+profile, server.URL+"/#/w/"+pid+"/s/"+sid)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	select {
	case got := <-result:
		if got != "ok" {
			t.Fatal(got)
		}
	case <-ctx.Done():
		t.Fatal("workspace UI timed out")
	}
	sessions, err := app.store.Sessions("acme")
	if err != nil || len(sessions) != 2 {
		t.Fatalf("复制之后应有两个会话：%d %v", len(sessions), err)
	}
	for _, s := range sessions {
		if s.ID != sid && (s.EnvironmentID != env["id"] || s.Branch != "") {
			t.Fatalf("副本沿用环境配置、不带分支：%#v", s)
		}
	}
}

const workspaceUIProbe = `
const { t } = await import('/assets/i18n.js');
const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];
const wait = async (test, label) => {
  const end = Date.now() + 15000;
  while (Date.now() < end) { const v = test(); if (v) return v; await new Promise((r) => setTimeout(r, 25)); }
  throw new Error('Timed out: ' + label + '\n' + document.body.innerText.slice(0, 3000));
};
const check = (ok, text) => { if (!ok) throw new Error(text); };
const session = async () => (await (await fetch('/api/sessions')).json()).find((s) => s.id === C.sid);
try {
  // 右栏“环境”：选定的配置与“未交给 Runtime”；沙箱环境就绪只看 ready。
  const rec = await wait(() => $('.env-record[data-env-ref="sbe-now"]'), 'runtime environment record');
  check(rec.innerText.includes(t('env.state.ready')), 'ready chip: ' + rec.innerText);
  const panel = rec.closest('.sp-body');
  check(panel.innerText.includes(C.env) && panel.innerText.includes(t('env.handoffPending')), 'environment config row: ' + panel.innerText);
  panel.querySelector('details.env-older summary').click();
  const older = await wait(() => $('.env-older-row'), 'older record');
  check(older.querySelector('.chip').innerText === t('env.state.unconfirmed'), 'unconfirmed READY: ' + older.innerText);
  $('.task-settings-trigger').click();await wait(()=>$('[data-environment-select]'),'task settings');
  check($('[data-environment-select]').disabled && $('[data-environment-select]').innerText.includes(C.env), 'composer shows the bound environment');
  $('[data-task-settings-apply]').click();

  // 拉取 main（sync）：会话的基线提交变成远端的最新提交。
  const sync = await wait(() => $('[data-action="sync"]'), 'sync button');
  check(sync.innerText.includes(t('sync.btn', { base: 'main' })), 'sync label: ' + sync.innerText);
  sync.click();
  await wait(() => document.body.innerText.includes(t('sync.done', { base: 'main', head: C.head.slice(0, 7) })), 'sync toast');
  check((await session()).base_commit === C.head, 'base commit after sync');

  // 文件树标出改动，点开文件可以跳到它的 diff。
  await fetch('/__write');
  $('.topbar button[title="' + t('drawer.title') + '"]').click();
  await wait(() => $('.diff-file[data-path="notes.txt"]'), 'diff of the change');
  [...$$('.dtab')].find((b) => b.innerText.includes(t('drawer.files'))).click();
  const item = await wait(() => $('.tree-item.changed[data-path="notes.txt"]'), 'changed marker in the tree');
  check(item.querySelector('.tree-mark').innerText === t('files.changed'), 'marker text');
  item.click();
  const viewDiff = await wait(() => $('[data-action="file-diff"]'), 'view diff button');
  check(viewDiff.innerText.includes(t('files.viewDiff')), 'view diff label');
  viewDiff.click();
  await wait(() => $('.dtab.active') && $('.dtab.active').innerText.includes(t('sp.changes')) && $('.diff-file[data-path="notes.txt"]'), 'back on the diff');

  // 提交之后，提交记录里是“本会话”的提交，下面是“会话起点”。
  const committed = await fetch('/api/projects/' + C.pid + '/sessions/' + C.sid + '/commit', { method: 'POST', headers: { 'X-Coding-Web': '1', 'Content-Type': 'application/json' }, body: JSON.stringify({ message: 'notes' }) });
  check(committed.ok, 'commit');
  [...$$('.dtab')].find((b) => b.innerText.includes(t('drawer.history'))).click();
  const own = await wait(() => $('.commit-row [data-own]'), 'own commit marker');
  check(own.innerText === t('history.own') && own.closest('.commit-row').innerText.includes('notes'), 'own commit');
  const start = await wait(() => $('.commit-row [data-start]'), 'session start marker');
  check(start.innerText === t('history.start') && start.closest('.commit-row').dataset.hash === C.head, 'session start is the synced head');
  check(!$('[data-action="history-more"]'), 'no more button for a short history');

  // Runtime 说读不到：blocked_external，不沿用刚才的记录。
  document.querySelector('.drawer-head button[title="' + t('drawer.close') + '"]').click();
  await fetch('/__env_blocked');
  [...$$('.sp-actions button')].find((b) => b.title === t('common.reload')).click();
  const blocked = await wait(() => $('[data-env-status="blocked_external"]'), 'blocked_external');
  check(blocked.innerText.includes(t('env.statusBlocked')) && !$('.env-record'), 'blocked text: ' + blocked.innerText);

  // 复制会话：新会话从这个会话的工作副本复制，标题按语言加“副本”。
  $('.topbar button[title="' + t('session.menuTitle') + '"]').click();
  const dup = await wait(() => [...$$('.pop-item')].find((b) => b.innerText.includes(t('session.duplicate'))), 'duplicate item');
  const title = (await session()).title;
  dup.click();
  await wait(() => { const m = location.hash.match(/\/s\/([^/?]+)/); return m && m[1] !== C.sid; }, 'opened the copy');
  await wait(() => $('.topbar .title') && $('.topbar .title').innerText === t('session.copyTitle', { title }), 'copy title');
  await fetch('/__result', { method: 'POST', body: 'ok' });
} catch (err) {
  await fetch('/__result', { method: 'POST', body: String(err.stack || err) });
}
`
