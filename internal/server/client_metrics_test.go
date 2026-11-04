package server

// PERF-00 的 coding-agent 一侧（Runtime 的 docs/plan/workspace-read-latency-and-disk-2026-11-03.md 第 2 节；
// docs/plan/all-plans-dag-2026-11-03.md 第 8 节的 PERF-00-CA）：网页报上来打开会话时加载历史的耗时与请求数，
// coding-web 记成 coding.history_load.duration_ms 与 coding.history_load.requests（Observe，标签只有 mode）。
//
//  1. 登录了才收；记下的值与报上来的一致，标签只有 mode（snapshot 或 replay）。
//  2. 不合规的（mode 不认识、负数、超过上限、不是 JSON、多出来的字段）400，什么都不记。
//  3. 没接 Telemetry（Metrics 为 nil）时照样 204。

import (
	"net/http"
	"reflect"
	"sync"
	"testing"
)

type observation struct {
	name   string
	value  float64
	labels map[string]string
}

type observeRecorder struct {
	mu  sync.Mutex
	got []observation
}

func (r *observeRecorder) Count(string, int64, map[string]string) {}

func (r *observeRecorder) Observe(name string, v float64, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, observation{name, v, labels})
}

func (r *observeRecorder) all() []observation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observation(nil), r.got...)
}

const historyLoadPath = "/api/metrics/history-load"

func TestHistoryLoadIsRecorded(t *testing.T) {
	rec := &observeRecorder{}
	ts, _, _, _ := newServerWith(t, func(c *Config) { c.Metrics = rec })
	c := newClient(t, ts.URL)
	report := map[string]any{"mode": "snapshot", "duration_ms": 1234.5, "requests": 3}
	if code, _, _ := c.do("POST", historyLoadPath, report); code != http.StatusUnauthorized {
		t.Fatalf("没登录应 401，实际 %d", code)
	}
	if len(rec.all()) != 0 {
		t.Fatalf("没登录的报告不记：%v", rec.all())
	}
	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	if code, _, raw := c.do("POST", historyLoadPath, report); code != http.StatusNoContent {
		t.Fatalf("应 204，实际 %d %s", code, raw)
	}
	if code, _, raw := c.do("POST", historyLoadPath, map[string]any{"mode": "replay", "duration_ms": 0, "requests": 0}); code != http.StatusNoContent {
		t.Fatalf("零也是合法的取值：%d %s", code, raw)
	}
	// HIST-01-WEB：取不到快照时先批量取事件，mode 是 events。
	if code, _, raw := c.do("POST", historyLoadPath, map[string]any{"mode": "events", "duration_ms": 80, "requests": 3}); code != http.StatusNoContent {
		t.Fatalf("mode events：%d %s", code, raw)
	}
	mode := func(m string) map[string]string { return map[string]string{"mode": m} }
	want := []observation{
		{"coding.history_load.duration_ms", 1234.5, mode("snapshot")},
		{"coding.history_load.requests", 3, mode("snapshot")},
		{"coding.history_load.duration_ms", 0, mode("replay")},
		{"coding.history_load.requests", 0, mode("replay")},
		{"coding.history_load.duration_ms", 80, mode("events")},
		{"coding.history_load.requests", 3, mode("events")},
	}
	if got := rec.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("记下的：%v\n应为：%v", got, want)
	}
}

func TestHistoryLoadRejectsBadReports(t *testing.T) {
	rec := &observeRecorder{}
	ts, _, _, _ := newServerWith(t, func(c *Config) { c.Metrics = rec })
	c := newClient(t, ts.URL)
	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	for name, body := range map[string]any{
		"mode 不认识":   map[string]any{"mode": "other", "duration_ms": 10, "requests": 1},
		"缺 mode":     map[string]any{"duration_ms": 10, "requests": 1},
		"耗时为负":       map[string]any{"mode": "snapshot", "duration_ms": -1, "requests": 1},
		"耗时超过十分钟":    map[string]any{"mode": "snapshot", "duration_ms": 600001, "requests": 1},
		"请求数为负":      map[string]any{"mode": "snapshot", "duration_ms": 10, "requests": -1},
		"请求数超过一万":    map[string]any{"mode": "snapshot", "duration_ms": 10, "requests": 10001},
		"请求数不是整数":    map[string]any{"mode": "snapshot", "duration_ms": 10, "requests": 1.5},
		"多出来的字段":     map[string]any{"mode": "snapshot", "duration_ms": 10, "requests": 1, "path": "/secret"},
		"不是 JSON 对象": "snapshot",
	} {
		if code, _, raw := c.do("POST", historyLoadPath, body); code != http.StatusBadRequest {
			t.Errorf("%s：应 400，实际 %d %s", name, code, raw)
		}
	}
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("不合规的报告什么都不记：%v", got)
	}
}

func TestHistoryLoadWithoutTelemetry(t *testing.T) {
	ts, _, _ := newServer(t)
	c := newClient(t, ts.URL)
	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	if code, _, raw := c.do("POST", historyLoadPath, map[string]any{"mode": "replay", "duration_ms": 50, "requests": 3}); code != http.StatusNoContent {
		t.Fatalf("没接 Telemetry 时照样 204：%d %s", code, raw)
	}
}
