package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// connectError 是 task-service 回的 connect JSON 错误：说明是中文，code 在 google.rpc.ErrorInfo 里。
func connectError(reason, message string) []byte {
	body := map[string]any{"code": "permission_denied", "message": message}
	if reason != "" {
		body["details"] = []map[string]any{{"type": "google.rpc.ErrorInfo", "value": "ignored", "debug": map[string]string{"reason": reason, "domain": "agentruntime"}}}
	}
	out, _ := json.Marshal(body)
	return out
}

func TestRuntimeErrorsFollowTheLanguage(t *testing.T) {
	var reply []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(reply)
	}))
	defer ts.Close()
	api := NewTaskAPI(ts.URL)
	write := func(lang string) (int, string) {
		err := api.CallWorkspace(t.Context(), "token", "CreateWorkspace", map[string]any{}, nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/projects", nil)
		req.Header.Set("Accept-Language", lang)
		writeWorkspaceError(testContext(rec, req), err, http.StatusInternalServerError)
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out["error"]
	}
	cases := []struct {
		reason, message, lang string
		want                  string
	}{
		{"network_not_allowed", "租户策略不允许沙箱联网（sandbox.network.allowed）", "en", "Your account's policy doesn't allow the sandbox to use the network"},
		{"network_not_allowed", "租户策略不允许沙箱联网（sandbox.network.allowed）", "zh-CN", "账号的策略不允许沙箱联网"},
		// 没有翻译的：英文只给错误码，中文原样给 Runtime 的说明。
		{"some_new_code", "新的说明", "en", "The runtime returned an error (some_new_code)"},
		{"some_new_code", "新的说明", "zh-CN", "新的说明"},
		// 旧的 task-service 不带 ErrorInfo：英文给 connect 的错误码。
		{"", "旧的说明", "en", "The runtime returned an error (permission_denied)"},
	}
	for _, c := range cases {
		reply = connectError(c.reason, c.message)
		if code, got := write(c.lang); code != http.StatusForbidden || got != c.want {
			t.Errorf("%s/%s：%d %q，应为 %q", c.reason, c.lang, code, got, c.want)
		}
	}
}

func TestEveryTranslatedRuntimeErrorHasEnglish(t *testing.T) {
	for _, reason := range []string{"network_not_allowed", "host_not_allowed", "workspace_dirty", "workspace_exists", "git_denied", "push_rejected", "sandbox_unavailable"} {
		m := runtimeMsg(reason)
		if m == nil || m.In(langEN) == m.In(langZH) {
			t.Errorf("%s 没有英文", reason)
		}
	}
	if runtimeMsg("git_failed") != nil {
		t.Error("git_failed 不翻译：中文界面要看到 Runtime 说明里 git 的输出")
	}
}

func TestOwnReposComeFirst(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.extraRepos = []map[string]any{
		{"full_name": "someone/shared-fork", "clone_url": "https://github.com/someone/shared-fork.git", "private": true, "permissions": map[string]bool{"push": true}},
		{"full_name": "other/read-only", "clone_url": "https://github.com/other/read-only.git", "permissions": map[string]bool{"push": false}},
	}
	ts, _, srv := newOAuthServer(t, "github")
	pointProvider(srv, "github", gh.server.URL)
	c, _ := githubLoginFlow(t, ts, "good-code")
	code, _, raw := c.do("GET", "/api/git/github/repos", nil)
	var repos []remoteRepo
	if err := json.Unmarshal([]byte(raw), &repos); code != http.StatusOK || err != nil {
		t.Fatalf("仓库列表：%d %s", code, raw)
	}
	var got []string
	for _, r := range repos {
		got = append(got, r.FullName)
	}
	want := []string{"octo-cat/demo", "someone/shared-fork", "other/read-only"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("自己名下的应在最前，再是能推送的：%v", got)
	}
	if !repos[0].Mine || repos[1].Mine || repos[2].Mine {
		t.Fatalf("mine 标错了：%+v", repos)
	}
}
