package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
)

// Requests use the real handlers/client wire format, with an in-process HTTP
// transport. No listening socket or mock Workspaces implementation is needed.
type workspaceRoundTrip func(*http.Request) (*http.Response, error)

func (f workspaceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func workspaceResponse(code int, v any) *http.Response {
	data, _ := json.Marshal(v)
	return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}
}

type workspaceRemote struct {
	mu        sync.Mutex
	started   map[string]map[string]any
	starts    int
	ready     bool
	fail      bool
	interrupt bool
	tokens    []string
}

func (f *workspaceRemote) roundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/StartWorkspaceOperation"):
		requestID := req["requestId"].(string)
		opID := "wso-" + workspaceHash("acme", "alice", requestID)
		if _, ok := f.started[opID]; !ok {
			f.starts++
			f.started[opID] = req
		}
		if f.interrupt {
			f.interrupt = false
			panic(workspaceHTTPAbort{context.Canceled})
		}
		return workspaceResponse(200, map[string]any{"operationId": opID}), nil
	case strings.HasSuffix(r.URL.Path, "/GetWorkspaceOperation"):
		saved := f.started[req["operationId"].(string)]
		if saved == nil {
			return workspaceResponse(404, map[string]string{"code": "not_found"}), nil
		}
		if !f.ready {
			return workspaceResponse(200, map[string]string{"status": "WORKSPACE_OPERATION_STATUS_RUNNING"}), nil
		}
		if f.fail {
			return workspaceResponse(200, map[string]any{"status": "WORKSPACE_OPERATION_STATUS_FAILED", "error": map[string]string{"category": "ERROR_CATEGORY_DENIED", "code": "repo_denied", "message": "no access"}}), nil
		}
		create, _ := saved["create"].(map[string]any)
		return workspaceResponse(200, map[string]any{"status": "WORKSPACE_OPERATION_STATUS_COMPLETED", "result": map[string]any{"workspace": map[string]any{"workspaceId": create["workspaceId"], "branch": "main", "head": "base"}}}), nil
	default:
		return workspaceResponse(200, map[string]any{}), nil
	}
}
func newWorkspaceHTTPServer(t *testing.T, dir string, remote *workspaceRemote) *Server {
	t.Helper()
	api := NewTaskAPI("http://runtime.invalid")
	client := &http.Client{Transport: workspaceRoundTrip(remote.roundTrip)}
	api.client, api.slow = client, client
	s := &Server{cfg: Config{Workspaces: "runtime"}, store: NewStore(dir), api: api, providers: map[string]*Provider{}}
	s.ws = runtimeWorkspaces{api: api}
	return s
}
func workspaceOwner() identity {
	return identity{Identity: ingress.Identity{TenantID: "acme", UserID: "alice"}, token: "token-never-on-disk"}
}
func submitWorkspaceHTTP(t *testing.T, s *Server, id identity, key, body string, ctx context.Context) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("X-Coding-Async", "1")
	req.Header.Set("Idempotency-Key", key)
	out := httptest.NewRecorder()
	s.workspaceHTTP("create_project")(out, req, id)
	var parsed map[string]any
	_ = json.Unmarshal(out.Body.Bytes(), &parsed)
	return out.Code, parsed
}
func workspaceStatus(t *testing.T, s *Server, id identity, op string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/workspace-operations/"+op, nil)
	r.SetPathValue("operation", op)
	w := httptest.NewRecorder()
	s.handleWorkspaceHTTPStatus(w, r, id)
	var parsed map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &parsed)
	return w.Code, parsed
}
func waitWorkspaceStatus(t *testing.T, s *Server, id identity, op, want string) map[string]any {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		_, v := workspaceStatus(t, s, id, op)
		if v["status"] == want {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("operation did not reach " + want)
	return nil
}

func TestWorkspaceHTTPDisconnectAndDuplicateRequests(t *testing.T) {
	remote := &workspaceRemote{started: map[string]map[string]any{}}
	s := newWorkspaceHTTPServer(t, t.TempDir(), remote)
	id := workspaceOwner()
	ctx, cancel := context.WithCancel(context.Background())
	before := time.Now()
	code, out := submitWorkspaceHTTP(t, s, id, "request-123456789", "{\"name\":\"Example\"}", ctx)
	if code != 202 || time.Since(before) > time.Second {
		t.Fatalf("not a short admission: %d %v", code, out)
	}
	cancel() // Browser disconnect must not cancel the durable operation or projection.
	op := out["operation_id"].(string)
	if code, again := submitWorkspaceHTTP(t, s, id, "request-123456789", "{\"name\":\"Example\"}", context.Background()); code != 202 || again["operation_id"] != op {
		t.Fatalf("retry: %d %v", code, again)
	}
	if code, _ := submitWorkspaceHTTP(t, s, id, "request-123456789", "{\"name\":\"Changed\"}", context.Background()); code != 409 {
		t.Fatal("changed input accepted")
	}
	other := id
	other.UserID = "bob"
	if code, _ := workspaceStatus(t, s, other, op); code != 404 {
		t.Fatal("foreign owner sees operation")
	}
	other = id
	other.TenantID = "other"
	if code, _ := workspaceStatus(t, s, other, op); code != 404 {
		t.Fatal("foreign tenant sees operation")
	}
	remote.mu.Lock()
	remote.ready = true
	remote.mu.Unlock()
	result := waitWorkspaceStatus(t, s, id, op, "COMPLETED")
	s.background.Wait()
	if result["http_status"] != float64(201) {
		t.Fatalf("result: %v", result)
	}
	projects, _ := s.store.Projects(id.TenantID)
	if len(projects) != 1 {
		t.Fatal(projects)
	}
	remote.mu.Lock()
	starts := remote.starts
	remote.mu.Unlock()
	if starts != 1 {
		t.Fatalf("started %d times", starts)
	}
	data, _ := os.ReadFile(s.store.workspaceOperationFile(id.TenantID, op))
	if bytes.Contains(data, []byte(id.token)) {
		t.Fatal("token persisted")
	}
}

func TestWorkspaceHTTPRestartResumesAcceptedRuntimeOperation(t *testing.T) {
	remote := &workspaceRemote{started: map[string]map[string]any{}, interrupt: true}
	dir := t.TempDir()
	first := newWorkspaceHTTPServer(t, dir, remote)
	id := workspaceOwner()
	_, out := submitWorkspaceHTTP(t, first, id, "restart-123456789", "{\"name\":\"Example\"}", context.Background())
	first.background.Wait() // Simulated stop after Runtime accepted, before its response.
	remote.mu.Lock()
	remote.ready = true
	remote.mu.Unlock()
	second := newWorkspaceHTTPServer(t, dir, remote)
	id.token = "fresh-authenticated-token"
	result := waitWorkspaceStatus(t, second, id, out["operation_id"].(string), "COMPLETED")
	second.background.Wait()
	if result["http_status"] != float64(201) {
		t.Fatal(result)
	}
	remote.mu.Lock()
	starts := remote.starts
	tokens := append([]string(nil), remote.tokens...)
	remote.mu.Unlock()
	if starts != 1 {
		t.Fatalf("restart repeated mutation: %d", starts)
	}
	if tokens[len(tokens)-1] != "Bearer "+id.token {
		t.Fatal("restart reused old token")
	}
	projects, _ := second.store.Projects(id.TenantID)
	if len(projects) != 1 {
		t.Fatal(projects)
	}
	// Replaying after projection persisted must not append a duplicate project.
	path := second.store.workspaceOperationFile(id.TenantID, out["operation_id"].(string))
	rec, _ := readJSONFile[*workspaceHTTPRecord](path)
	rec.Status = 0
	rec.Response = nil
	if err := writeFileAtomic(path, rec); err != nil {
		t.Fatal(err)
	}
	third := newWorkspaceHTTPServer(t, dir, remote)
	waitWorkspaceStatus(t, third, id, rec.ID, "COMPLETED")
	third.background.Wait()
	projects, _ = third.store.Projects(id.TenantID)
	if len(projects) != 1 {
		t.Fatalf("projection duplicated: %v", projects)
	}
}

func TestWorkspaceHTTPRetainsFailureAndRejectsCredentialBodies(t *testing.T) {
	remote := &workspaceRemote{started: map[string]map[string]any{}, ready: true, fail: true}
	s := newWorkspaceHTTPServer(t, t.TempDir(), remote)
	id := workspaceOwner()
	if code, _ := submitWorkspaceHTTP(t, s, id, "secret-1234567890", "{\"name\":\"Example\",\"credential\":{\"password\":\"secret\"}}", context.Background()); code != 400 {
		t.Fatal(code)
	}
	_, out := submitWorkspaceHTTP(t, s, id, "failure-123456789", "{\"name\":\"Example\"}", context.Background())
	result := waitWorkspaceStatus(t, s, id, out["operation_id"].(string), "FAILED")
	s.background.Wait()
	if result["http_status"] != float64(403) {
		t.Fatalf("failure lost: %v", result)
	}
	restarted := newWorkspaceHTTPServer(t, s.store.dir, remote)
	_, again := workspaceStatus(t, restarted, id, out["operation_id"].(string))
	if again["status"] != "FAILED" {
		t.Fatal(again)
	}
	if _, err := workspaceHTTPBody("attach", []byte(`{"repo":{"name":"app","credential":"secret"}}`)); err == nil {
		t.Fatal("nested credential accepted")
	}
}

func TestWorkspaceHTTPConcurrentAdmissionStartsOnce(t *testing.T) {
	remote := &workspaceRemote{started: map[string]map[string]any{}}
	s := newWorkspaceHTTPServer(t, t.TempDir(), remote)
	id := workspaceOwner()
	var group sync.WaitGroup
	ids := make(chan string, 12)
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			code, out := submitWorkspaceHTTP(t, s, id, "concurrent-1234567", `{"name":"Example"}`, context.Background())
			if code != 202 {
				t.Errorf("admission %d %v", code, out)
				return
			}
			ids <- out["operation_id"].(string)
		}()
	}
	group.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent retries have different identities")
		}
	}
	remote.mu.Lock()
	remote.ready = true
	remote.mu.Unlock()
	waitWorkspaceStatus(t, s, id, first, "COMPLETED")
	s.background.Wait()
	remote.mu.Lock()
	starts := remote.starts
	remote.mu.Unlock()
	if starts != 1 {
		t.Fatalf("duplicate execution: %d", starts)
	}
}
