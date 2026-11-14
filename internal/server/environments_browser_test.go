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

// Runs the embedded app against the actual coding-web handlers and file store.
// Runtime execution uses the same fakeTaskAPI as the other server tests.
func TestEnvironmentConfigurationInBrowser(t *testing.T) {
	browser := environmentTestBrowser()
	if browser == "" {
		t.Skip("Chrome/Edge not found; set CODING_TEST_BROWSER")
	}
	ts, _, _, app := newServerWith(t, nil)
	c := newClient(t, ts.URL)
	first := sampleProject(t, c)
	code, second, raw := c.do("POST", "/api/projects", map[string]string{"name": "Second", "source": "empty"})
	if code != http.StatusCreated {
		t.Fatalf("second project: %d %s", code, raw)
	}
	projects, _ := json.Marshal([]string{first, second["id"].(string)})
	result := make(chan string, 1)
	handler := app.Handler()
	probe := "const projects = " + string(projects) + ";\n" + environmentBrowserProbe
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__environment_probe.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = io.WriteString(w, probe)
		case "/__environment_result":
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
			http.SetCookie(w, &http.Cookie{Name: langCookie, Value: "en", Path: "/"})
			_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", `<script type="module" src="/__environment_probe.js"></script></body>`, 1))
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	// 同 internal/web 的浏览器用例：不用 t.TempDir 放浏览器的资料目录，浏览器的子进程在主进程结束后才陆续退出，目录一时删不掉不该让测试失败。
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
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser, "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run", "--no-default-browser-check", "--window-size=1440,1000", "--user-data-dir="+profile, server.URL+"/#/environments")
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
		t.Fatal("environment UI timed out")
	}
	sessions, err := app.store.Sessions("acme")
	if err != nil || len(sessions) != 3 {
		t.Fatalf("sessions = %#v, err = %v", sessions, err)
	}
	bound := map[string]string{}
	for _, s := range sessions {
		bound[s.Title] = s.EnvironmentID
	}
	if bound["configured one"] == "" || bound["configured one"] != bound["configured two"] || bound["deployment default"] != "" {
		t.Fatalf("repo-independent/default bindings = %#v", bound)
	}
	// 删除时密钥立即删掉；会话保留创建时的快照。
	tomb, found, err := app.store.Environment("acme", bound["configured one"])
	if err != nil || !found || tomb.DeletedAt == nil || len(tomb.Secrets) != 0 {
		t.Fatalf("tombstone keeps no secrets: %#v %v", tomb, err)
	}
	for _, s := range sessions {
		if snap, ok, _ := app.store.Snapshot("acme", s.WorkspaceID); s.EnvironmentID != "" && (!ok || snap.EnvironmentID != s.EnvironmentID || snap.StartCommand != "python -m app") {
			t.Fatalf("session %q keeps its snapshot: %#v %v", s.Title, snap, ok)
		}
	}
}

func environmentTestBrowser() string {
	if p := os.Getenv("CODING_TEST_BROWSER"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome", "msedge", "microsoft-edge"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if root == "" {
			continue
		}
		for _, rel := range []string{`Google\Chrome\Application\chrome.exe`, `Microsoft\Edge\Application\msedge.exe`} {
			if p := filepath.Join(root, rel); p != "" {
				if _, err := os.Stat(p); err == nil {
					return p
				}
			}
		}
	}
	return ""
}

const environmentBrowserProbe = `
const $ = s => document.querySelector(s);
const wait = async (test, label) => {
  const end = Date.now() + 8000;
  while (Date.now() < end) { const value = test(); if (value) return value; await new Promise(r => setTimeout(r, 25)); }
  throw new Error('Timed out: ' + label + '\n' + document.body.innerText);
};
const check = (ok, text) => { if (!ok) throw new Error(text); };
const field = (name, value) => {
  const el = $('.environment-editor [name="' + name + '"]');
  check(el, 'Missing field ' + name);
  if (value !== undefined) { el.value = value; el.dispatchEvent(new Event('input', {bubbles:true})); }
  return el;
};
const save = () => $('.environment-editor form').requestSubmit();
try {
  (await wait(() => $('[data-environment-create]'), 'configuration list')).click();
  await wait(() => $('.environment-editor'), 'editor');
  field('name', '跨仓库 Python');
  field('description', 'Shared tooling');
  field('setup_script', '中'.repeat(21846));
  save();
  await wait(() => $('.environment-editor [role="alert"]').textContent.includes('65536'), 'UTF-8 size error');
  check((await (await fetch('/api/environments')).json()).length === 0, 'oversize script saved');
  field('setup_script', 'python --version');
  field('env', 'A=' + 'x'.repeat(16383));
  save();
  await wait(() => $('.environment-editor [role="alert"]').textContent.includes('16384'), '.env size error (Runtime limit)');
  check((await (await fetch('/api/environments')).json()).length === 0, 'oversize .env saved');
  field('env', '');
  field('start_command', 'python -m app');
  field('env', 'BAD-NAME=value');
  save();
  await wait(() => $('.environment-editor [role="alert"]').textContent.includes('line 1 must be KEY=value'), 'localized server validation');
  check(field('setup_script').value === 'python --version', 'failed save discarded edits');
  field('env', 'MODE=dev\nPORT=8080');
  save();
  const card = await wait(() => $('[data-environment-id]'), 'saved card');
  const id = card.dataset.environmentId;
  check(!$('.environment-editor'), 'editor did not close');
  card.querySelector('[data-environment-edit]').click();
  check(field('env').value === 'MODE=dev\nPORT=8080', 'edit did not restore ordinary .env');
  field('description', 'Updated shared tooling');
  // .env 的密钥：只写，存在服务端；之后只看得到名字。
  $('.environment-editor [data-secret-add]').click();
  const secretRow = await wait(() => $('.environment-editor [data-secret-row]:last-child'), 'secret row');
  const secretName = secretRow.querySelector('[data-secret-name]');
  const secretValue = secretRow.querySelector('[data-secret-value]');
  check(secretValue.type === 'password', 'secret value is a password field');
  secretName.value = 'OPENAI_API_KEY'; secretName.dispatchEvent(new Event('input', {bubbles:true}));
  secretValue.value = 'sk-leaky-value'; secretValue.dispatchEvent(new Event('input', {bubbles:true}));
  save();
  await wait(() => !$('.environment-editor') && document.body.innerText.includes('Updated shared tooling'), 'edit save');
  const stored = await (await fetch('/api/environments/' + id)).json();
  check(stored.privacy === 'only_me' && stored.start_command === 'python -m app', 'configuration lost fields');
  check(JSON.stringify(stored.secrets) === JSON.stringify([{name: 'OPENAI_API_KEY', set: true}]), 'secret names only: ' + JSON.stringify(stored.secrets));
  check(Object.values(localStorage).every(v => !String(v).includes('MODE=dev') && !String(v).includes('sk-leaky')), '.env leaked into localStorage');
  check(!document.body.innerHTML.includes('sk-leaky'), 'secret value left in the page');
  document.querySelector('[data-environment-id="' + id + '"] [data-environment-edit]').click();
  const kept = await wait(() => $('.environment-editor [data-secret-row] [data-secret-name]'), 'kept secret');
  check(kept.value === 'OPENAI_API_KEY' && $('.environment-editor [data-secret-value]').value === '', 'secret shown by name only');
  $('.environment-editor [data-editor-cancel]').click();
  await wait(() => !$('.environment-editor'), 'editor cancelled');
  for (let i=0; i<3; i++) {
    location.hash = '#/w/' + projects[i % 2] + '/new';
    await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
    (await wait(()=>$('.task-settings-trigger'),'task settings')).click();
    const select = await wait(() => { const e = $('[data-environment-select]'); return e && !e.disabled && e; }, 'draft selector');
    select.value = i===2 ? '' : id;
    select.dispatchEvent(new Event('change', {bubbles:true}));
    $('[data-task-settings-apply]').click();
    const input = $('.composer textarea');
    check(input, 'composer input');
    input.value = ['configured one','configured two','deployment default'][i];
    input.dispatchEvent(new Event('input', {bubbles:true}));
    // Let React commit the input before the synthetic click, as between real browser events.
    await wait(() => $('.send') && !$('.send').disabled, 'send enabled after input');
    $('.send').click();
    await wait(() => /\/s\//.test(location.hash), 'session binding');
    $('.task-settings-trigger').click();await wait(()=>$('[data-environment-select]')?.disabled,'bound environment');$('[data-task-settings-apply]').click();
    // 发出后输入框清空，发送按钮按原来的做法变灰；任务提交完成的标志是对话里出现这一轮（不再是“提交中”）。
    await wait(() => document.querySelector('section.turn[id^="turn-"]') && !document.querySelector('section.turn.pending'), 'session submission');
  }
  location.hash = '#/environments';
  await wait(() => $('[data-environment-id="' + id + '"]'), 'list after sessions');
  // 删除时说清还有几个会话在用、密钥立即删除（2026-11-03 人定）。
  // The confirmation is the HeroUI AlertDialog once chat-ui.js has loaded, the browser's confirm before that.
  let asked = '';
  window.confirm = (text) => { asked = text; return true; };
  $('[data-environment-delete]').click();
  const confirmation = await wait(() => asked || $('[data-pro-component="AlertDialog"]'), 'delete confirmation');
  if (typeof confirmation !== 'string') { asked = confirmation.innerText; confirmation.querySelector('[data-dialog-confirm]').click(); }
  await wait(() => !$('[data-environment-id]'), 'delete');
  check(asked.includes('2 sessions') && asked.includes('immediately'), 'delete confirmation: ' + asked);
  check((await fetch('/api/environments/' + id)).status === 404, 'deleted environment still available');
  // 用它的会话：右栏说明这个环境已删除、用的是创建时的设置、密钥已删除。
  const used = (await (await fetch('/api/sessions')).json()).find((x) => x.title === 'configured one');
  location.hash = '#/w/' + used.project_id + '/s/' + used.id;
  const gone = await wait(() => $('[data-env-config-deleted]'), 'deleted environment note');
  check(gone.innerText.includes('creation-time settings') && gone.innerText.includes('secrets'), 'deleted note: ' + gone.innerText);
  $('.task-settings-trigger').click();await wait(()=>$('[data-environment-select]'),'task settings');
  check($('[data-environment-select]').disabled, 'still bound');
  await fetch('/__environment_result', {method:'POST',body:'ok'});
} catch (err) {
  await fetch('/__environment_result', {method:'POST',body:String(err.stack || err)});
}
`
