package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

type multiWorkspaceFake struct {
	fakeWorkspaces
	repos    map[string][]WorkspaceRepo
	selected string
	lastPath string
}

func (f *multiWorkspaceFake) Create(_ context.Context, _ identity, ws string, spec CreateSpec) (WorkspaceInfo, error) {
	f.created = append(f.created, spec)
	if spec.From != "" {
		f.repos[ws] = slices.Clone(f.repos[spec.From])
	}
	return WorkspaceInfo{Layout: spec.Layout, Repos: slices.Clone(f.repos[ws])}, nil
}
func (f *multiWorkspaceFake) Repos(_ context.Context, _ identity, ws string) ([]WorkspaceRepo, error) {
	return slices.Clone(f.repos[ws]), nil
}
func (f *multiWorkspaceFake) Attach(_ context.Context, _ identity, ws string, repo WorkspaceRepo, _ GitAuth) (WorkspaceRepo, error) {
	if repo.Name == "broken" {
		return WorkspaceRepo{}, &APIError{Status: 502, Reason: "clone_failed", Message: "clone failed"}
	}
	repo.Head, repo.Branch = "base-"+repo.Name, "main"
	f.repos[ws] = append(f.repos[ws], repo)
	return repo, nil
}
func (f *multiWorkspaceFake) Detach(_ context.Context, _ identity, ws, name string, discard bool) (map[string]any, error) {
	if !discard {
		return nil, &APIError{Status: 412, Reason: "repo_dirty", Message: "dirty"}
	}
	f.repos[ws] = slices.DeleteFunc(f.repos[ws], func(x WorkspaceRepo) bool { return x.Name == name })
	return map[string]any{"discardedFiles": 2, "discardedCommits": 1}, nil
}
func (f *multiWorkspaceFake) Changes(_ context.Context, id identity, _ string) (Changes, error) {
	f.selected = id.repo
	return Changes{Head: "abc", Files: f.files}, nil
}
func (f *multiWorkspaceFake) Commit(_ context.Context, id identity, _, _, _, _ string) (string, bool, error) {
	f.selected = id.repo
	f.commits++
	return "abc", true, nil
}
func (f *multiWorkspaceFake) List(_ context.Context, id identity, _, path string) ([]FileEntry, error) {
	f.selected = id.repo
	f.lastPath = path
	return []FileEntry{}, nil
}

func TestMultiRepositorySessionScopeReadOnlyAndDetach(t *testing.T) {
	ts, _, _, srv := newServerWith(t, nil)
	fake := &multiWorkspaceFake{repos: make(map[string][]WorkspaceRepo)}
	srv.ws = fake
	c := newClient(t, ts.URL)
	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	status, p, raw := c.do("POST", "/api/projects", map[string]any{"name": "multi", "layout": layoutMulti})
	if status != 201 {
		t.Fatalf("create %d %s", status, raw)
	}
	base := "/api/projects/" + p["id"].(string)
	if spec := fake.created[0]; spec.Layout != layoutMulti || len(spec.Files) != 0 || spec.RepoURL != "" {
		t.Fatalf("create spec: %+v", spec)
	}
	attach := func(name, access string, want int) {
		t.Helper()
		status, _, raw := c.do("POST", base+"/repos", map[string]any{"repo": WorkspaceRepo{Name: name, Path: name, URL: "https://example.com/" + name + ".git", Access: access}})
		if status != want {
			t.Fatalf("attach %s: %d %s", name, status, raw)
		}
	}
	attach("app", repoWriteAccess, 201)
	attach("docs", "REPO_ACCESS_READ", 201)
	attach("broken", repoWriteAccess, 502)
	_, listed, _ := c.do("GET", base+"/repos", nil)
	if len(listed["repos"].([]any)) != 2 {
		t.Fatalf("failed attach changed existing repos: %v", listed)
	}
	status, sess, raw := c.do("POST", base+"/sessions", map[string]string{"title": "task"})
	if status != 201 {
		t.Fatalf("session %d %s", status, raw)
	}
	api := base + "/sessions/" + sess["id"].(string)
	if spec := fake.created[1]; spec.Layout != layoutMulti || spec.From != p["id"] {
		t.Fatalf("copy spec: %+v", spec)
	}
	for _, query := range []string{"", "?repo=missing"} {
		if status, _, _ := c.do("GET", api+"/changes"+query, nil); status != 400 {
			t.Fatalf("selector %q = %d", query, status)
		}
	}
	if status, _, raw := c.do("GET", api+"/changes?repo=app", nil); status != 200 || fake.selected != "app" {
		t.Fatalf("scoped diff %d %s repo=%s", status, raw, fake.selected)
	}
	for _, endpoint := range []string{"commit", "publish"} {
		if status, _, _ := c.do("POST", api+"/"+endpoint+"?repo=docs", map[string]string{}); status != 403 {
			t.Fatalf("read-only %s = %d", endpoint, status)
		}
	}
	if fake.commits != 0 {
		t.Fatal("read-only request committed")
	}
	if status, _, raw := c.do("POST", api+"/commit?repo=app", map[string]string{"message": "change"}); status != 200 || fake.selected != "app" {
		t.Fatalf("commit %d %s", status, raw)
	}
	c.do("GET", api+"/tree?path=docs", nil)
	if fake.selected != "" || fake.lastPath != "docs" {
		t.Fatal("file browsing must stay workspace-relative")
	}
	status, dirty, _ := c.do("POST", api+"/repos/docs/detach", map[string]bool{"discard_changes": false})
	if status != 409 || dirty["code"] != "repo_dirty" {
		t.Fatalf("detach confirmation %d %v", status, dirty)
	}
	if len(fake.repos[sess["workspace_id"].(string)]) != 2 {
		t.Fatal("dirty detach mutated repos")
	}
	status, removed, _ := c.do("POST", api+"/repos/docs/detach", map[string]bool{"discard_changes": true})
	if status != 200 || removed["discardedFiles"] != float64(2) {
		t.Fatalf("confirmed detach %d %v", status, removed)
	}
	if len(fake.repos[p["id"].(string)]) != 2 {
		t.Fatal("session detach changed baseline")
	}
	c.do("POST", "/api/login", map[string]string{"tenant": "other", "user": "bob"})
	if status, _, _ := c.do("GET", base+"/repos", nil); status != 404 {
		t.Fatalf("cross-tenant repos = %d", status)
	}
}

func TestMultiRepositoryPublishStateAndCopyAreIndependent(t *testing.T) {
	_, _, _, srv := newServerWith(t, nil)
	sess := Session{ID: "s1", ProjectID: "p1", WorkspaceID: "w1", RepoStates: map[string]RepoSessionState{"a": {BaseBranch: "main", BaseCommit: "a0"}, "b": {BaseBranch: "stable", BaseCommit: "b0"}}}
	_ = srv.store.UpdateSessions("acme", func([]Session) ([]Session, error) { return []Session{sess}, nil })
	selected := projectRepoSession(sess, "a")
	updated, err := srv.updateSession("acme", selected, func(x *Session) { x.Branch = "agent/a"; x.PRURL = "https://example.com/a/pull/1"; x.PRNumber = 1 })
	if err != nil {
		t.Fatal(err)
	}
	if updated.Branch != "" || updated.RepoStates["a"].Branch != "agent/a" || updated.RepoStates["b"].Branch != "" || updated.RepoStates["b"].BaseCommit != "b0" {
		t.Fatalf("state mixed: %+v", updated)
	}
	selected = projectRepoSession(updated, "b")
	updated, err = srv.updateSession("acme", selected, func(x *Session) { x.BaseCommit = "b1" })
	if err != nil || updated.RepoStates["a"].PRNumber != 1 || updated.RepoStates["b"].BaseCommit != "b1" {
		t.Fatalf("update mixed: %+v %v", updated, err)
	}
	rp := srv.repoProject(Project{ID: "parent", Repo: "wrong", RepoID: 123, InstallationID: 456, Provider: "github"}, WorkspaceRepo{URL: "https://example.org/right.git"}, "stable")
	if rp.ID != "" || rp.RepoID != 0 || rp.InstallationID != 0 || rp.Repo != "" || rp.Source != "https://example.org/right.git" {
		t.Fatalf("credential/cache identity leaked: %+v", rp)
	}
	fake := &multiWorkspaceFake{repos: map[string][]WorkspaceRepo{"w1": {{Name: "a", Head: "a-current", Branch: "agent/a"}, {Name: "b", Head: "b1", Branch: "stable"}}}}
	srv.ws = fake
	req := httptest.NewRequest("POST", "/", nil)
	dup, err := srv.createSession(req, identity{}, Project{ID: "p1", Layout: layoutMulti}, "copy", nil, &updated)
	if err != nil {
		t.Fatal(err)
	}
	if dup.RepoStates["a"].Branch != "" || dup.RepoStates["a"].PRURL != "" || dup.RepoStates["a"].BaseCommit != "a0" {
		t.Fatalf("copy carried publishing state: %+v", dup)
	}
	if dup.RepoStates["a"].BaseBranch != "main" {
		t.Fatalf("copy must retain original PR base, not agent branch: %+v", dup)
	}
}

func TestWorkspaceOperationRetryScopeAndFailure(t *testing.T) {
	var ids []string
	var starts []map[string]any
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer user-token" {
			t.Error("identity lost")
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.HasSuffix(r.URL.Path, "StartWorkspaceOperation") {
			ids = append(ids, req["requestId"].(string))
			starts = append(starts, req)
			if len(ids) == 1 {
				w.WriteHeader(503)
				_, _ = io.WriteString(w, `{"code":"unavailable"}`)
				return
			}
			_, _ = io.WriteString(w, `{"operationId":"op1"}`)
			return
		}
		if req["workspaceId"] != "ws" || req["operationId"] != "op1" || len(req) != 2 {
			t.Errorf("poll leaked input: %v", req)
		}
		polls++
		if polls == 1 {
			_, _ = io.WriteString(w, `{"status":"WORKSPACE_OPERATION_STATUS_RUNNING"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"WORKSPACE_OPERATION_STATUS_FAILED","error":{"category":"ERROR_CATEGORY_CONFLICT","code":"repo_dirty","message":"dirty"}}`)
	}))
	defer server.Close()
	api := NewTaskAPI(server.URL)
	err := api.workspaceOperation(t.Context(), "user-token", "DetachRepo", map[string]any{"workspaceId": "ws", "repo": "app", "discardChanges": false}, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Reason != "repo_dirty" || ae.Status != 409 {
		t.Fatalf("operation error: %v", err)
	}
	if len(ids) != 2 || ids[0] != ids[1] || len(ids[0]) != 36 {
		t.Fatalf("unstable retry IDs: %v", ids)
	}
	if starts[0]["detachRepo"].(map[string]any)["repo"] != "app" {
		t.Fatal("repo lost")
	}
}

func TestRuntimeMultiCreateAndScopedGitWire(t *testing.T) {
	var calls []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		calls = append(calls, req)
		if strings.HasSuffix(r.URL.Path, "StartWorkspaceOperation") {
			_, _ = io.WriteString(w, `{"operationId":"op1"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"WORKSPACE_OPERATION_STATUS_COMPLETED","result":{"workspace":{"layout":"WORKSPACE_LAYOUT_MULTI","repos":[{"name":"a","path":"a","access":"REPO_ACCESS_WRITE"}]},"sha":"a1","committed":true}}`)
	}))
	defer server.Close()
	runtime := runtimeWorkspaces{api: NewTaskAPI(server.URL)}
	info, err := runtime.Create(t.Context(), identity{token: "t"}, "new", CreateSpec{Layout: layoutMulti, From: "old"})
	if err != nil || len(info.Repos) != 1 {
		t.Fatalf("copy result: %+v %v", info, err)
	}
	create := calls[0]["create"].(map[string]any)
	if len(create) != 3 || create["layout"] != layoutMulti || create["fromWorkspaceId"] != "old" {
		t.Fatalf("multi create sends forbidden root fields: %v", create)
	}
	_, _, err = runtime.Commit(t.Context(), identity{token: "t", repo: "a"}, "new", "message", "name", "email")
	if err != nil || calls[2]["commit"].(map[string]any)["repo"] != "a" {
		t.Fatalf("scoped commit: %v %v", calls, err)
	}
}

func TestWorkspaceOperationCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "StartWorkspaceOperation") {
			_, _ = io.WriteString(w, `{"operationId":"op1"}`)
		} else {
			_, _ = io.WriteString(w, `{"status":"WORKSPACE_OPERATION_STATUS_RUNNING"}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	err := NewTaskAPI(server.URL).workspaceOperation(ctx, "t", "DeleteWorkspace", map[string]any{"workspaceId": "ws"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
}
