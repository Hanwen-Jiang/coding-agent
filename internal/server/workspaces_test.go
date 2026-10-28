package server

// CODING_WORKSPACES=runtime：coding-web 经 Runtime 的 Workspace API 管工作区，不碰工作区的文件。这里的假 Workspace API 用
// localWorkspaces 在临时目录里实现（按令牌认出租户），回的是 task-service 的 JSON 写法（camelCase、int64 写成字符串、枚举名）：
// 网页的接口、Workspace API 的客户端与这套 JSON 一起被测到。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/agentruntime/agent-runtime/libs/go-core/dev/localingress"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
)

type fakeWorkspaceAPI struct {
	ws      localWorkspaces
	ingress *localingress.Provider
	mu      sync.Mutex
	calls   []map[string]any
	methods []string
}

func (f *fakeWorkspaceAPI) record(method string, body map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.methods = append(f.methods, method)
	f.calls = append(f.calls, body)
}

func (f *fakeWorkspaceAPI) last(method string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.methods) - 1; i >= 0; i-- {
		if f.methods[i] == method {
			return f.calls[i]
		}
	}
	return nil
}

func (f *fakeWorkspaceAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/agentruntime.workspace.v1.WorkspaceService/")
	who, err := f.ingress.Authenticate(r.Context(), ingress.Credentials{BearerToken: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "unauthenticated", "message": "令牌不对"})
		return
	}
	id := identity{Identity: who}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.record(method, body)
	str := func(k string) string { s, _ := body[k].(string); return s }
	auth := GitAuth{}
	if c, ok := body["credential"].(map[string]any); ok {
		auth.User, _ = c["username"].(string)
		auth.Pass, _ = c["password"].(string)
	}
	ctx := r.Context()
	var out any
	switch method {
	case "CreateWorkspace":
		spec := CreateSpec{RepoURL: str("repoUrl"), Ref: str("ref"), From: str("fromWorkspaceId"), AuthorName: str("authorName"), AuthorEmail: str("authorEmail"), Auth: auth}
		if files, ok := body["files"].([]any); ok {
			for _, x := range files {
				m, _ := x.(map[string]any)
				p, _ := m["path"].(string)
				c, _ := m["content"].(string)
				spec.Files = append(spec.Files, InitialFile{Path: p, Content: c})
			}
		}
		var info WorkspaceInfo
		if info, err = f.ws.Create(ctx, id, str("workspaceId"), spec); err == nil {
			out = map[string]any{"workspace": map[string]any{"workspaceId": str("workspaceId"), "head": info.Head, "branch": info.Branch, "state": "WORKSPACE_STATE_ACTIVE"}}
		}
	case "DeleteWorkspace":
		if _, statErr := os.Stat(f.ws.dir(id, str("workspaceId"))); statErr != nil {
			err = wsErr(http.StatusNotFound, msg("工作区不存在"))
		} else {
			err = f.ws.Delete(ctx, id, str("workspaceId"))
		}
		out = map[string]any{}
	case "ListFiles":
		var entries []FileEntry
		if entries, err = f.ws.List(ctx, id, str("workspaceId"), str("path")); err == nil {
			items := []any{}
			for _, e := range entries {
				item := map[string]any{"name": e.Name, "type": "FILE_TYPE_FILE", "size": strconv.FormatInt(e.Size, 10)}
				if e.Dir {
					item = map[string]any{"name": e.Name, "type": "FILE_TYPE_DIRECTORY"}
				}
				items = append(items, item)
			}
			out = map[string]any{"entries": items}
		}
	case "ReadFile":
		var c FileContent
		if c, err = f.ws.Read(ctx, id, str("workspaceId"), str("path")); err == nil {
			out = map[string]any{"size": strconv.FormatInt(c.Size, 10), "tooLarge": c.TooLarge, "binary": c.Binary, "content": c.Content}
		}
	case "GetLog":
		limit, _ := body["limit"].(float64)
		var commits []CommitInfo
		if commits, err = f.ws.Log(ctx, id, str("workspaceId"), int(limit), str("base")); err == nil {
			items := []any{}
			for _, c := range commits {
				items = append(items, map[string]any{"sha": c.Hash, "authorName": c.Author, "authoredAt": c.Date, "subject": c.Subject})
			}
			out = map[string]any{"commits": items}
		}
	case "GetDiff":
		if body["findRenames"] != true {
			http.Error(w, "coding-web 要检测改名", http.StatusTeapot)
			return
		}
		var c Changes
		if c, err = f.ws.Changes(ctx, id, str("workspaceId")); err == nil {
			out = map[string]any{"diff": c.Diff, "head": c.Head, "branch": c.Branch, "stats": c.Files, "truncated": c.Truncated}
		}
	case "CommitWorkspace":
		var sha string
		var committed bool
		if sha, committed, err = f.ws.Commit(ctx, id, str("workspaceId"), str("message"), str("authorName"), str("authorEmail")); err == nil {
			out = map[string]any{"sha": sha, "committed": committed}
		}
	case "DiscardChanges":
		err = f.ws.Discard(ctx, id, str("workspaceId"))
		out = map[string]any{}
	case "PushWorkspace":
		var sha string
		if sha, err = f.ws.Push(ctx, id, str("workspaceId"), str("branch"), auth); err == nil {
			out = map[string]any{"sha": sha, "branch": str("branch")}
		}
	case "SyncWorkspace":
		var head string
		if head, err = f.ws.Sync(ctx, id, str("workspaceId"), str("branch"), auth); err == nil {
			out = map[string]any{"head": head, "branch": str("branch")}
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(statusOf(err, http.StatusInternalServerError))
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "failed", "message": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

// newRuntimeServer 起 coding-web（CODING_WORKSPACES=runtime）与假的 task-service（Task API 与 Workspace API 在同一个地址）。
func newRuntimeServer(t *testing.T) (*httptest.Server, *fakeWorkspaceAPI, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("PATH 上没有 git")
	}
	dir := t.TempDir()
	key := []byte("test-key")
	hooks := filepath.Join(dir, "hooks")
	_ = os.MkdirAll(hooks, 0o755)
	root := filepath.Join(dir, "runtime-workspaces")
	fakeWS := &fakeWorkspaceAPI{ws: localWorkspaces{root: root, git: &Git{Bin: "git", HooksDir: hooks}}, ingress: localingress.New(localingress.Options{SigningKey: key})}
	tasks := &fakeTaskAPI{}
	mux := http.NewServeMux()
	mux.Handle("/agentruntime.workspace.v1.WorkspaceService/", fakeWS)
	mux.Handle("/", tasks)
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	cfg := Config{TaskAPI: api.URL, DataDir: filepath.Join(dir, "data"), DevLogin: true, SigningKey: key, PoliciesFile: filepath.Join(dir, "policies.json")}
	tasks.policies = cfg.PoliciesFile
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.ws.(runtimeWorkspaces); !ok {
		t.Fatal("默认经 Workspace API")
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, fakeWS, root
}

func TestWorkspacesGoThroughTheRuntimeWorkspaceAPI(t *testing.T) {
	ts, fake, root := newRuntimeServer(t)
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)
	create := fake.last("CreateWorkspace")
	if create["workspaceId"] != pid || create["authorName"] != "alice" || create["authorEmail"] != "alice@acme.coding.local" {
		t.Fatalf("建工作区经 Workspace API，作者是登录的用户：%v", create)
	}
	if files, _ := create["files"].([]any); len(files) < 2 || !strings.Contains(raw(files), "pricing.py") {
		t.Fatalf("示例的文件随请求写入：%v", create["files"])
	}

	code, sess, out := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "试一试"})
	if code != http.StatusCreated {
		t.Fatalf("开会话：%d %s", code, out)
	}
	ws := sess["workspace_id"].(string)
	if copy := fake.last("CreateWorkspace"); copy["workspaceId"] != ws || copy["fromWorkspaceId"] != pid {
		t.Fatalf("会话的工作副本从工作区复制：%v", copy)
	}
	api := "/api/projects/" + pid + "/sessions/" + sess["id"].(string)

	// Agent 在 Runtime 那边改了文件（这里直接写进假 Workspace API 的目录）。
	wsDir := filepath.Join(root, "acme", ws)
	if err := os.WriteFile(filepath.Join(wsDir, "new.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, changes, _ := c.do("GET", api+"/changes", nil)
	if files, _ := changes["files"].([]any); len(files) != 1 || !strings.Contains(changes["diff"].(string), "+hello") || changes["branch"] != "main" {
		t.Fatalf("改动经 GetDiff：%v", changes)
	}
	code, tree, rawTree := c.doList("GET", api+"/tree", nil)
	if code != http.StatusOK || !strings.Contains(rawTree, `"pricing.py"`) || strings.Contains(rawTree, `".git"`) || len(tree) == 0 || tree[0]["dir"] != false && tree[0]["dir"] != true {
		t.Fatalf("文件树经 ListFiles：%d %s", code, rawTree)
	}
	_, file, _ := c.do("GET", api+"/file?path=new.txt", nil)
	if file["content"] != "hello\n" || file["size"].(float64) != 6 {
		t.Fatalf("读文件经 ReadFile：%v", file)
	}
	if code, _, _ := c.do("GET", api+"/file?path=.git/config", nil); code != http.StatusBadRequest {
		t.Fatalf(".git 不给读：%d", code)
	}
	if code, _, raw := c.do("POST", api+"/commit", map[string]string{"message": "add new.txt"}); code != http.StatusOK {
		t.Fatalf("提交经 CommitWorkspace：%d %s", code, raw)
	}
	if commit := fake.last("CommitWorkspace"); commit["authorName"] != "alice" {
		t.Fatalf("提交的作者：%v", commit)
	}
	if code, _, _ := c.do("POST", api+"/commit", map[string]string{"message": "nothing"}); code != http.StatusBadRequest {
		t.Fatalf("没有改动时 400，实际 %d", code)
	}
	_, logs, rawLog := c.doList("GET", api+"/log", nil)
	if len(logs) != 2 || logs[0]["subject"] != "add new.txt" {
		t.Fatalf("提交记录经 GetLog：%s", rawLog)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := c.do("POST", api+"/discard", nil); code != http.StatusOK {
		t.Fatalf("丢弃改动经 DiscardChanges：%d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "scratch.txt")); err == nil {
		t.Fatal("丢弃之后未跟踪的文件没了")
	}
	code, _, _ = c.do("POST", api+"/tasks", map[string]string{"message": "fix it"})
	if code != http.StatusCreated {
		t.Fatalf("下任务：%d", code)
	}
	_, env, _ := c.do("GET", api+"/env", nil)
	if git, _ := env["git"].(map[string]any); git["subject"] != "add new.txt" || git["dirty"].(float64) != 0 {
		t.Fatalf("环境里的 git 信息：%v", env["git"])
	}

	if code, _, raw := c.do("DELETE", "/api/projects/"+pid, nil); code != http.StatusOK {
		t.Fatalf("删工作区：%d %s", code, raw)
	}
	for _, gone := range []string{ws, pid} {
		if _, err := os.Stat(filepath.Join(root, "acme", gone)); err == nil {
			t.Fatalf("%s 应经 DeleteWorkspace 删掉", gone)
		}
	}
}

func TestRuntimeWorkspaceErrorsArePassedThrough(t *testing.T) {
	ts, _, _ := newRuntimeServer(t)
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)
	_, sess, _ := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "x"})
	api := "/api/projects/" + pid + "/sessions/" + sess["id"].(string)
	if code, out, _ := c.do("GET", api+"/file?path=missing.txt", nil); code != http.StatusNotFound || out["error"] == "" {
		t.Fatalf("Workspace API 的 404 原样转出：%d %v", code, out)
	}
	if code, _, _ := c.do("GET", api+"/tree?path=nope", nil); code != http.StatusNotFound {
		t.Fatalf("目录不存在：%d", code)
	}
}

func TestRuntimeClientSendsCredentialsOnlyAsCredential(t *testing.T) {
	var seen map[string]any
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &seen)
		_, _ = io.WriteString(w, `{"sha":"abc","branch":"agent/x"}`)
	}))
	defer server.Close()
	w := runtimeWorkspaces{api: NewTaskAPI(server.URL)}
	sha, err := w.Push(context.Background(), identity{token: "tok-1"}, "ws1", "agent/x", GitAuth{Host: "https://github.com/", User: "ghp_x", Pass: "x-oauth-basic"})
	if err != nil || sha != "abc" {
		t.Fatalf("Push：%q %v", sha, err)
	}
	if header.Get("Authorization") != "Bearer tok-1" {
		t.Fatal("令牌原样转给 task-service")
	}
	cred, _ := seen["credential"].(map[string]any)
	if seen["workspaceId"] != "ws1" || seen["branch"] != "agent/x" || cred["username"] != "ghp_x" || cred["password"] != "x-oauth-basic" {
		t.Fatalf("推送的请求：%v", seen)
	}
	if _, ok := seen["host"]; ok {
		t.Fatal("Host 只给本机模式用，不发给 Runtime")
	}
}

func raw(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// doList 同 do，回的是数组。
func (c *client) doList(method, path string, body any) (int, []map[string]any, string) {
	c.t.Helper()
	code, _, text := c.do(method, path, body)
	var out []map[string]any
	_ = json.Unmarshal([]byte(text), &out)
	return code, out, text
}
