package server

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 复制会话（Workspace API 的 copy）：新会话的工作副本从这个会话复制，环境配置沿用，对话、分支与 PR 不带过去。
func TestDuplicatingASessionCopiesItsWorkingCopy(t *testing.T) {
	ts, _, cfg, srv := newServerWith(t, nil)
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)
	_, env, _ := c.do("POST", "/api/environments", map[string]any{"name": "Python"})
	code, src, raw := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "原来的", "environment_id": env["id"].(string)})
	if code != http.StatusCreated {
		t.Fatalf("开会话：%d %s", code, raw)
	}
	api := "/api/projects/" + pid + "/sessions/" + src["id"].(string)
	dir := filepath.Join(cfg.WorkspaceRoot, "acme", src["workspace_id"].(string))
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := c.do("POST", api+"/commit", map[string]string{"message": "add notes"}); code != http.StatusOK {
		t.Fatalf("提交：%d %s", code, raw)
	}
	if _, err := srv.updateSession("acme", Session{ProjectID: pid, ID: src["id"].(string)}, func(x *Session) { x.Branch, x.PRNumber = "agent/x", 7 }); err != nil {
		t.Fatal(err)
	}

	code, dup, raw := c.do("POST", api+"/duplicate", map[string]string{"title": "原来的（副本）"})
	if code != http.StatusCreated {
		t.Fatalf("复制会话：%d %s", code, raw)
	}
	if dup["id"] == src["id"] || dup["title"] != "原来的（副本）" || dup["environment_id"] != env["id"] || dup["environment_handoff"] != handoffPending ||
		dup["branch"] != nil || dup["pr_number"] != nil || dup["runtime_session_id"] != nil || dup["base_commit"] != src["base_commit"] {
		t.Fatalf("副本：%s", raw)
	}
	copied := filepath.Join(cfg.WorkspaceRoot, "acme", dup["workspace_id"].(string), "notes.txt")
	if data, err := os.ReadFile(copied); err != nil || string(data) != "kept\n" {
		t.Fatalf("副本里应有原会话提交的文件：%q %v", data, err)
	}
	// 环境配置删掉之后，副本照样沿用原会话的快照（2026-11-03 人定：会话只用快照，只是没有密钥）。
	c.do("DELETE", "/api/environments/"+env["id"].(string), nil)
	if code, dup2, raw := c.do("POST", api+"/duplicate", map[string]string{}); code != http.StatusCreated || dup2["environment_id"] != env["id"] || dup2["title"] != "原来的" {
		t.Fatalf("没给标题时沿用原来的；删掉的环境照用快照：%d %s", code, raw)
	}
	other := newClient(t, ts.URL)
	other.do("POST", "/api/login", map[string]string{"tenant": "globex", "user": "bob"})
	if code, _, _ := other.do("POST", api+"/duplicate", map[string]string{}); code != http.StatusNotFound {
		t.Fatalf("别的租户复制不了：%d", code)
	}
}

// 在 Runtime 那边，复制会话就是 CreateWorkspace 的 from_workspace_id（连同未提交的改动）。
func TestDuplicateGoesThroughCreateWorkspaceFrom(t *testing.T) {
	ts, fake, _ := newRuntimeServer(t)
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)
	_, src, _ := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "a"})
	code, dup, raw := c.do("POST", "/api/projects/"+pid+"/sessions/"+src["id"].(string)+"/duplicate", map[string]string{"title": "b"})
	if code != http.StatusCreated {
		t.Fatalf("复制会话：%d %s", code, raw)
	}
	req := fake.last("CreateWorkspace")
	if req["fromWorkspaceId"] != src["workspace_id"] || req["workspaceId"] != dup["workspace_id"] {
		t.Fatalf("CreateWorkspace：%v", req)
	}
}

// 拉取（SyncWorkspace）：只给还没推送到自己分支、干净、没有自己提交的会话；拉到之后基线提交跟着更新。
func TestSyncPullsTheLatestBaseBranch(t *testing.T) {
	ts, _, cfg, srv := newServerWith(t, nil)
	srv.git.AllowFileProtocol = true // 远端是本机的裸仓库
	c := newClient(t, ts.URL)
	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	api, ws, bare := publishable(t, c, cfg, srv, "acme", "github", "o/demo")
	pid := strings.Split(api, "/")[3]
	sid := strings.Split(api, "/")[5]

	if code, _, _ := c.do("POST", api+"/sync", nil); code != http.StatusBadRequest {
		t.Fatalf("没有 https 远端的工作区不能拉取：%d", code)
	}
	if _, err := srv.updateProject("acme", pid, func(p *Project) { p.Source = "https://example.invalid/o/demo.git" }); err != nil {
		t.Fatal(err)
	}
	// 远端的 main 多了一个提交。
	upstream := filepath.Join(t.TempDir(), "up")
	plainGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=u", "-c", "user.email=u@example.com"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v：%v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	plainGit(filepath.Dir(upstream), "clone", "-q", bare, upstream)
	plainGit(upstream, "commit", "-q", "--allow-empty", "-m", "upstream change")
	plainGit(upstream, "push", "-q", "origin", "main")
	want := plainGit(upstream, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(ws, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := c.do("POST", api+"/sync", nil); code != http.StatusConflict {
		t.Fatalf("有未提交的改动时不拉取：%d %s", code, raw)
	}
	if code, _, raw := c.do("POST", api+"/commit", map[string]string{"message": "mine"}); code != http.StatusOK {
		t.Fatalf("提交：%d %s", code, raw)
	}
	if code, _, raw := c.do("POST", api+"/sync", nil); code != http.StatusConflict || !strings.Contains(raw, "还没推送的提交") {
		t.Fatalf("有自己的提交时不拉取（会切走它们）：%d %s", code, raw)
	}
	gitIn(t, srv, ws, "reset", "-q", "--hard", "HEAD~1")
	code, out, raw := c.do("POST", api+"/sync", nil)
	if code != http.StatusOK || out["head"] != want || out["session"].(map[string]any)["base_commit"] != want {
		t.Fatalf("拉取：%d %s（应到 %s）", code, raw, want)
	}
	if _, err := srv.updateSession("acme", Session{ProjectID: pid, ID: sid}, func(x *Session) { x.Branch = "agent/x" }); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := c.do("POST", api+"/sync", nil); code != http.StatusConflict || !strings.Contains(raw, "agent/x") {
		t.Fatalf("推送过的会话用“回到基线分支”：%d %s", code, raw)
	}
}

func TestHistoryTakesALimit(t *testing.T) {
	ts, _, cfg, _ := newServerWith(t, nil)
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)
	api, ws := newSession(t, c, cfg, "acme", pid)
	for i := range 3 {
		_ = os.WriteFile(filepath.Join(ws, "f.txt"), []byte(strings.Repeat("x", i+1)), 0o644)
		c.do("POST", api+"/commit", map[string]string{"message": "c"})
	}
	code, list, raw := c.doList("GET", api+"/log?limit=2", nil)
	if code != http.StatusOK || len(list) != 2 {
		t.Fatalf("limit=2：%d %s", code, raw)
	}
	if _, list, _ = c.doList("GET", api+"/log?limit=999", nil); len(list) < 4 {
		t.Fatalf("limit 超过 200 时按 200：%d", len(list))
	}
}
