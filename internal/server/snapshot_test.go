package server

// WEB-SNAP（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md；N-19 的网页一侧，n19-n20-implementation-2026-11-03.md“没做的”）：
// 会话快照的转发与游标过期。
//
//   - GET <会话>/snapshot：带用户的令牌转调 Task API 的 GET /v1/sessions/{Runtime 的 session_id}/snapshot，原样带回 JSON；
//     查询参数只转 tasks 与 events（Runtime 自己再截到上限）。会话还没交给 Runtime（没提交过任务）时直接回空的快照，不去问。
//   - 别的租户：404，不去问 Task API。上游 404（Runtime 还没有这个端点，或会话没了）原样 404，页面据此退回逐个任务回放；5xx 换成 502。
//   - 事件转发：上游 409 resnapshot_required 时回 200 的事件流，只有一条 resnapshot_required 事件，然后结束（EventSource 读不到状态码，
//     只能这样告诉页面重新取快照）；别的失败照旧。
//
// 实现在 snapshot.go 与 tasks.go 的 handleEvents。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// snapshotTaskAPI 在 fakeTaskAPI 外面加上快照端点与可以按任务定状态的事件端点。
type snapshotTaskAPI struct {
	fake     *fakeTaskAPI
	mu       sync.Mutex
	snapshot []string // 收到的快照请求：路径?查询 与 Authorization
	status   int      // 快照端点的状态；0 是 200
	events   []string // 收到的事件请求：路径 与 Last-Event-ID
	expired  bool     // 事件端点回 409 resnapshot_required
}

func (a *snapshotTaskAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/snapshot"):
		a.snapshot = append(a.snapshot, r.URL.Path+"?"+r.URL.RawQuery+" "+r.Header.Get("Authorization"))
		if a.status != 0 {
			http.Error(w, `{"code":"not_found","message":"upstream detail"}`, a.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"session_id":"ses_1","generated_at":"2026-11-03T10:00:00Z","errors":[],"tasks":[{"task_id":"tsk_1","status":"TASK_STATUS_RUNNING","last_event_id":7,"events":[],"source":"stream"}]}`)
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/events"):
		a.events = append(a.events, r.URL.Path+" "+r.Header.Get("Last-Event-ID"))
		if a.expired {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"resnapshot_required","message":"cursor expired"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 8\nevent: task_finished\ndata: {}\n\n")
	default:
		a.fake.ServeHTTP(w, r)
	}
}

// newSnapshotSession 起 coding-web 与上游，登录 acme/alice，开会话。submit 为真时先提交一个任务（会话就有了 Runtime 的 session_id）。
func newSnapshotSession(t *testing.T, submit bool) (*httptest.Server, *client, string, *snapshotTaskAPI) {
	t.Helper()
	up := &snapshotTaskAPI{fake: &fakeTaskAPI{}}
	upstream := httptest.NewServer(up)
	t.Cleanup(upstream.Close)
	ts, _, cfg, _ := newServerWith(t, func(c *Config) { c.TaskAPI = upstream.URL })
	up.fake.policies = cfg.PoliciesFile
	c := newClient(t, ts.URL)
	pid := sampleProject(t, c)
	api, _ := newSession(t, c, cfg, "acme", pid)
	if submit {
		if code, _, raw := c.do("POST", api+"/tasks", map[string]string{"message": "修一下测试"}); code != http.StatusCreated {
			t.Fatalf("提交任务失败：%d %s", code, raw)
		}
	}
	up.mu.Lock()
	up.snapshot = nil
	up.mu.Unlock()
	return ts, c, api, up
}

func TestTheSnapshotIsForwardedForTheRuntimeSession(t *testing.T) {
	_, c, api, up := newSnapshotSession(t, true)
	code, out, raw := c.do("GET", api+"/snapshot?tasks=10&events=80&other=x", nil)
	if code != http.StatusOK {
		t.Fatalf("取快照失败：%d %s", code, raw)
	}
	if tasks, _ := out["tasks"].([]any); len(tasks) != 1 {
		t.Fatalf("快照原样带回：%s", raw)
	}
	if len(up.snapshot) != 1 {
		t.Fatalf("应转调一次快照端点：%v", up.snapshot)
	}
	path, auth, _ := strings.Cut(up.snapshot[0], " ")
	u, _ := url.Parse(path)
	if u.Path != "/v1/sessions/ses_1/snapshot" || !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("要用 Runtime 的 session_id 与用户的令牌：%s", up.snapshot[0])
	}
	if q := u.Query(); q.Get("tasks") != "10" || q.Get("events") != "80" || q.Has("other") {
		t.Fatalf("只转 tasks 与 events：%s", u.RawQuery)
	}
}

func TestASessionWithoutRuntimeSessionHasAnEmptySnapshot(t *testing.T) {
	_, c, api, up := newSnapshotSession(t, false)
	code, out, raw := c.do("GET", api+"/snapshot", nil)
	if code != http.StatusOK {
		t.Fatalf("没提交过任务的会话：%d %s", code, raw)
	}
	if tasks, ok := out["tasks"].([]any); !ok || len(tasks) != 0 {
		t.Fatalf("应是空的快照：%s", raw)
	}
	if len(up.snapshot) != 0 {
		t.Fatalf("还没有 Runtime 的会话，不该去问：%v", up.snapshot)
	}
}

func TestSnapshotErrorsKeepTheirMeaning(t *testing.T) {
	ts, c, api, up := newSnapshotSession(t, true)
	other := newClient(t, ts.URL)
	other.do("POST", "/api/login", map[string]string{"tenant": "globex", "user": "bob"})
	if code, _, _ := other.do("GET", api+"/snapshot", nil); code != http.StatusNotFound {
		t.Fatalf("别的租户应 404，得到 %d", code)
	}
	if len(up.snapshot) != 0 {
		t.Fatalf("越权的请求不该去问 Task API：%v", up.snapshot)
	}
	for _, tc := range []struct{ upstream, want int }{{http.StatusNotFound, http.StatusNotFound}, {http.StatusInternalServerError, http.StatusBadGateway}} {
		up.mu.Lock()
		up.status = tc.upstream
		up.mu.Unlock()
		code, _, raw := c.do("GET", api+"/snapshot", nil)
		if code != tc.want || strings.Contains(raw, "upstream detail") {
			t.Fatalf("上游 %d：应返回 %d 且不带上游的正文，得到 %d %s", tc.upstream, tc.want, code, raw)
		}
	}
}

func TestAnExpiredCursorBecomesAResnapshotEvent(t *testing.T) {
	_, c, api, up := newSnapshotSession(t, true)
	up.mu.Lock()
	up.expired = true
	up.mu.Unlock()
	res, body := c.get(api + "/tasks/tsk_1/events?last_event_id=3")
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("游标过期应回 200 的事件流：%d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "event: resnapshot_required") {
		t.Fatalf("应有一条 resnapshot_required 事件：%q", body)
	}
	if len(up.events) != 1 || up.events[0] != "/v1/tasks/tsk_1/events 3" {
		t.Fatalf("last_event_id 要作 Last-Event-ID 转给 task-service：%v", up.events)
	}

	up.mu.Lock()
	up.expired = false
	up.mu.Unlock()
	res, body = c.get(api + "/tasks/tsk_1/events?last_event_id=7")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "event: task_finished") {
		t.Fatalf("正常的续传照旧：%d %q", res.StatusCode, body)
	}
}
