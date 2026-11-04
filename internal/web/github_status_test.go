package web

// G-05 第 3 条（docs/plan/github-app.md 第 4 节 G-05）：“账号与平台”里的 GitHub App 一段，数据来自 GET /api/github/status
// （internal/server/githubops.go）。
//
//   - /api/config 有 github_app 时才画（.github-app），没有时账号页与以前一样。
//   - 没有关联的安装：安装入口 a.gh-install（地址是状态接口给的 install_url）；有安装时是“去 GitHub 调整”的 a.gh-adjust，
//     地址相同，GitHub 在那里让人选账号、改仓库与权限。
//   - 还是旧 OAuth 的连接（legacy）：提示重新连接，a.gh-reconnect 指向 /api/auth/github/login?link=1。
//   - 已授权的仓库（各 GitHub 项目）：每个一行 .gh-repo，data-repo 是 owner/repo，data-state 是远端状况
//     （ok、app_suspended、app_removed、repo_removed、remote_deleted），权限不足、暂停、卸载时文字说清楚原因与做法；
//     远端有更新的另加 .gh-updated。
//   - 新的文字两种语言都有（不会把 github.* 的键名直接显示出来）。
//
// 静态检查总是跑；真正执行 JS 的检查用浏览器（找不到时跳过）。实现之前（app.js 还没读 /api/github/status）这些测试跳过。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func requireGitHubSection(t *testing.T) {
	t.Helper()
	_, app := getFrom(t, Handler(), "/assets/app.js")
	if !strings.Contains(string(app), "/api/github/status") {
		t.Skip("账号页的 GitHub App 一段还没写：先审验收测试（G-05）")
	}
}

var githubKeys = []string{
	"github.title", "github.legacy", "github.reconnect", "github.notConnected", "github.notInstalled", "github.install",
	"github.installed", "github.adjust", "github.repos", "github.noRepos", "github.remoteUpdated",
	"github.state.ok", "github.state.app_suspended", "github.state.app_removed", "github.state.repo_removed", "github.state.remote_deleted",
}

func TestTheGitHubAppTextIsInBothLanguages(t *testing.T) {
	requireGitHubSection(t)
	zh, en := dictionaries(t)
	for _, key := range githubKeys {
		if _, ok := zh[key]; !ok {
			t.Errorf("中文字典缺 %s", key)
		}
		if _, ok := en[key]; !ok {
			t.Errorf("英文字典缺 %s", key)
		}
	}
}

type githubResult struct {
	Errors  []string `json:"errors"`
	Dialog  bool     `json:"dialog"`
	Section *struct {
		Text      string `json:"text"`
		Install   string `json:"install"`
		Adjust    string `json:"adjust"`
		Reconnect string `json:"reconnect"`
		Repos     []struct {
			Repo    string `json:"repo"`
			State   string `json:"state"`
			Updated bool   `json:"updated"`
			Text    string `json:"text"`
		} `json:"repos"`
	} `json:"section"`
}

// githubProbe 等账号页打开（地址里的 ?linked=github 会打开它），读 GitHub App 一段。
const githubProbe = `
const out = { errors: [], dialog: false, section: null };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
const href = (root, sel) => { const a = root.querySelector(sel); return a ? a.getAttribute("href") : ""; };
try {
  out.dialog = !!(await wait(() => document.querySelector("dialog.account .conn-list"), 8000));
  const sec = await wait(() => document.querySelector("dialog.account .github-app"), 3000);
  if (sec) out.section = {
    text: sec.textContent,
    install: href(sec, "a.gh-install"), adjust: href(sec, "a.gh-adjust"), reconnect: href(sec, "a.gh-reconnect"),
    repos: [...sec.querySelectorAll(".gh-repo")].map((r) => ({ repo: r.dataset.repo, state: r.dataset.state, updated: r.classList.contains("gh-updated"), text: r.textContent })),
  };
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

// runGitHubProbe 起假的 API（config、status 由调用方给），用 headless 的浏览器打开账号页。找不到浏览器时跳过。
func runGitHubProbe(t *testing.T, lang string, config, status any) githubResult {
	t.Helper()
	browser := findBrowser()
	if browser == "" {
		t.Skip("找不到浏览器（CODING_TEST_BROWSER 可以指定）")
	}
	results := make(chan githubResult, 1)
	conns := []params{{"provider": "github", "login": "octo-cat", "name": "Octo", "git": true}}
	api := map[string]any{
		"/api/config":       config,
		"/api/me":           params{"tenant": "t1", "user": "u1", "connections": conns},
		"/api/account":      params{"connections": conns},
		"/api/projects":     []any{},
		"/api/samples":      []any{},
		"/api/sessions":     []any{},
		"/api/tasks/active": []any{},
	}
	if status != nil {
		api["/api/github/status"] = status
	}
	app := Handler()
	mux := http.NewServeMux()
	mux.Handle("/assets/", app)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, page := getFrom(t, app, "/")
		html := strings.Replace(string(page), `<script type="module"`, `<script type="module" src="/__probe.js"></script><script type="module"`, 1)
		w.Header().Set("Set-Cookie", "cw_lang="+lang+"; path=/")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("GET /__probe.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(githubProbe))
	})
	mux.HandleFunc("POST /__result", func(w http.ResponseWriter, r *http.Request) {
		var res githubResult
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			res.Errors = append(res.Errors, err.Error())
		}
		select {
		case results <- res:
		default:
		}
	})
	for path, body := range api {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir, err := os.MkdirTemp("", "coding-web-github")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for range 50 {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	ctx, stop := openPage(t, browser, dir, srv.URL+"/#/?linked=github")
	defer stop()
	select {
	case res := <-results:
		if len(res.Errors) != 0 {
			t.Fatalf("页面报错：%v", res.Errors)
		}
		if !res.Dialog {
			t.Fatal("账号页没有打开")
		}
		return res
	case <-ctx.Done():
		t.Fatal("等不到页面的结果")
		return githubResult{}
	}
}

const installURL = "https://github.com/apps/coding-agent-link/installations/new"

func appConfig() params {
	return params{"github_app": params{"slug": "coding-agent-link"}, "providers": []params{{"id": "github", "name": "GitHub", "git": true, "configured": true}}}
}

func TestTheAccountPageShowsInstallAndReconnectForALegacyConnection(t *testing.T) {
	requireGitHubSection(t)
	for _, lang := range []string{"zh", "en"} {
		res := runGitHubProbe(t, lang, appConfig(), params{
			"slug": "coding-agent-link", "install_url": installURL, "connected": true, "legacy": true, "installations": []any{},
			"projects": []params{
				{"id": "p1", "repo": "octo-cat/demo", "remote_state": "app_suspended"},
				{"id": "p2", "repo": "octo-cat/other", "remote_updated": true},
				{"id": "p3", "repo": "octo-cat/gone", "remote_state": "remote_deleted"},
			},
		})
		sec := res.Section
		if sec == nil {
			t.Fatalf("%s：配了 App 时有 GitHub App 一段", lang)
		}
		if sec.Install != installURL || sec.Adjust != "" {
			t.Errorf("%s：没有安装时给安装入口：%+v", lang, sec)
		}
		if sec.Reconnect != "/api/auth/github/login?link=1" {
			t.Errorf("%s：旧连接提示重新连接：%q", lang, sec.Reconnect)
		}
		want := map[string]string{"octo-cat/demo": "app_suspended", "octo-cat/other": "ok", "octo-cat/gone": "remote_deleted"}
		if len(sec.Repos) != len(want) {
			t.Fatalf("%s：每个仓库一行：%+v", lang, sec.Repos)
		}
		for _, r := range sec.Repos {
			if want[r.Repo] != r.State || strings.TrimSpace(r.Text) == "" {
				t.Errorf("%s：%s 的状况：%+v", lang, r.Repo, r)
			}
			if r.Updated != (r.Repo == "octo-cat/other") {
				t.Errorf("%s：远端有更新的另外标出：%+v", lang, r)
			}
		}
		if strings.Contains(sec.Text, "github.") {
			t.Errorf("%s：有没翻译的键名：%q", lang, sec.Text)
		}
	}
}

func TestTheAccountPageLinksToGitHubToAdjustAnInstallation(t *testing.T) {
	requireGitHubSection(t)
	res := runGitHubProbe(t, "zh", appConfig(), params{
		"slug": "coding-agent-link", "install_url": installURL, "connected": true, "legacy": false, "installations": []int{100},
		"projects": []any{},
	})
	sec := res.Section
	if sec == nil || sec.Adjust != installURL || sec.Install != "" || sec.Reconnect != "" || len(sec.Repos) != 0 {
		t.Fatalf("有安装时给“去 GitHub 调整”，不提示重新连接：%+v", sec)
	}
	if strings.Contains(sec.Text, "github.") {
		t.Errorf("有没翻译的键名：%q", sec.Text)
	}
}

func TestTheAccountPageIsUnchangedWithoutAnApp(t *testing.T) {
	requireGitHubSection(t)
	res := runGitHubProbe(t, "zh", params{"providers": []params{{"id": "github", "name": "GitHub", "git": true, "configured": true}}}, nil)
	if res.Section != nil {
		t.Fatalf("没配 App 时不画：%+v", res.Section)
	}
}
