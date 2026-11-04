package server

// HIST-01 的网页一侧（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md 第 8 节 HIST-01-WEB）：批量取会话里几个任务的事件。
//
//   - POST <会话>/events {cursors: [{task_id, after_event_id}], page_size}：带用户的令牌转调 Task API 的 ListSessionEvents
//     （Runtime 的 session_id），回来的 JSON 换成 proto 字段名（与快照、事件流的 data 一样：event_id、payload.value），页面直接拿去画。
//   - 会话还没交给 Runtime 时回空的 tasks，不去问。别的租户 404，不去问。
//   - 上游 404 或 Unimplemented（Runtime 还没有这个方法）回 404，页面退回逐个回放；别的失败是 502，不带上游的正文。
//   - 请求：最多 50 个任务；不是 JSON 或任务太多 400，不去问。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// eventsTaskAPI 在 fakeTaskAPI 外面加上 ListSessionEvents。
type eventsTaskAPI struct {
	fake     *fakeTaskAPI
	mu       sync.Mutex
	requests []map[string]any // 收到的 ListSessionEvents 请求体
	auth     []string
	status   int    // 0 是 200
	code     string // status 不为 0 时的 connect 错误码
}

const eventsReply = `{"tasks":[{"taskId":"tsk_1","found":true,"events":[
 {"eventId":"1","taskId":"tsk_1","type":"TASK_EVENT_TYPE_TASK_ACCEPTED","emittedAt":"2026-11-04T08:00:00Z"},
 {"eventId":"2","taskId":"tsk_1","type":"TASK_EVENT_TYPE_TASK_FINISHED","payload":{"value":{"status":"TASK_STATUS_DONE"}}}],
 "nextAfterEventId":"2","finished":true}]}`

func (a *eventsTaskAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/agentruntime.taskapi.v1.TaskService/ListSessionEvents" {
		a.fake.ServeHTTP(w, r)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	a.requests = append(a.requests, body)
	a.auth = append(a.auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	if a.status != 0 {
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, `{"code":"`+a.code+`","message":"upstream detail"}`)
		return
	}
	_, _ = io.WriteString(w, eventsReply)
}

func newEventsSession(t *testing.T, submit bool) (*httptest.Server, *client, string, *eventsTaskAPI) {
	t.Helper()
	up := &eventsTaskAPI{fake: &fakeTaskAPI{}}
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
	return ts, c, api, up
}

var oneCursor = map[string]any{"cursors": []any{map[string]any{"task_id": "tsk_1", "after_event_id": 0}}, "page_size": 100}

func TestSessionEventsAreForwardedWithProtoNames(t *testing.T) {
	_, c, api, up := newEventsSession(t, true)
	code, out, raw := c.do("POST", api+"/events", oneCursor)
	if code != http.StatusOK {
		t.Fatalf("批量取事件失败：%d %s", code, raw)
	}
	if len(up.requests) != 1 || !strings.HasPrefix(up.auth[0], "Bearer ") {
		t.Fatalf("应带用户的令牌转调一次：%v %v", up.requests, up.auth)
	}
	req := up.requests[0]
	cursors, _ := req["cursors"].([]any)
	first, _ := cursors[0].(map[string]any)
	if req["sessionId"] != "ses_1" || len(cursors) != 1 || first["taskId"] != "tsk_1" || req["pageSize"] != float64(100) {
		t.Fatalf("用 Runtime 的 session_id 与请求的游标：%v", req)
	}
	tasks, _ := out["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("结果原样带回：%s", raw)
	}
	task, _ := tasks[0].(map[string]any)
	events, _ := task["events"].([]any)
	if task["task_id"] != "tsk_1" || task["finished"] != true || task["next_after_event_id"] != "2" || len(events) != 2 {
		t.Fatalf("换成 proto 字段名：%s", raw)
	}
	last, _ := events[1].(map[string]any)
	payload, _ := last["payload"].(map[string]any)
	value, _ := payload["value"].(map[string]any)
	if last["event_id"] != "2" || last["type"] != "TASK_EVENT_TYPE_TASK_FINISHED" || value["status"] != "TASK_STATUS_DONE" {
		t.Fatalf("事件与快照里的一个样子：%s", raw)
	}
}

func TestSessionEventsWithoutRuntimeSessionAreEmpty(t *testing.T) {
	_, c, api, up := newEventsSession(t, false)
	code, out, raw := c.do("POST", api+"/events", oneCursor)
	if tasks, ok := out["tasks"].([]any); code != http.StatusOK || !ok || len(tasks) != 0 {
		t.Fatalf("没提交过任务的会话回空的 tasks：%d %s", code, raw)
	}
	if len(up.requests) != 0 {
		t.Fatal("还没有 Runtime 的会话，不该去问")
	}
}

func TestSessionEventsErrorsKeepTheirMeaning(t *testing.T) {
	ts, c, api, up := newEventsSession(t, true)
	other := newClient(t, ts.URL)
	other.do("POST", "/api/login", map[string]string{"tenant": "globex", "user": "bob"})
	if code, _, _ := other.do("POST", api+"/events", oneCursor); code != http.StatusNotFound {
		t.Fatalf("别的租户应 404，得到 %d", code)
	}
	many := make([]any, 51)
	for i := range many {
		many[i] = map[string]any{"task_id": "tsk_1", "after_event_id": i}
	}
	if code, _, _ := c.do("POST", api+"/events", map[string]any{"cursors": many}); code != http.StatusBadRequest {
		t.Fatalf("超过 50 个任务应 400，得到 %d", code)
	}
	if code, _, _ := c.do("POST", api+"/events", "not an object"); code != http.StatusBadRequest {
		t.Fatalf("不是 JSON 对象应 400，得到 %d", code)
	}
	if len(up.requests) != 0 {
		t.Fatalf("越权与不合规的请求不该去问 Task API：%v", up.requests)
	}
	for _, tc := range []struct {
		status int
		code   string
		want   int
	}{
		{http.StatusNotFound, "not_found", http.StatusNotFound},
		{http.StatusNotImplemented, "unimplemented", http.StatusNotFound},
		{http.StatusInternalServerError, "internal", http.StatusBadGateway},
	} {
		up.mu.Lock()
		up.status, up.code = tc.status, tc.code
		up.mu.Unlock()
		code, _, raw := c.do("POST", api+"/events", oneCursor)
		if code != tc.want || strings.Contains(raw, "upstream detail") {
			t.Fatalf("上游 %d %s：应返回 %d 且不带上游的正文，得到 %d %s", tc.status, tc.code, tc.want, code, raw)
		}
	}
}
