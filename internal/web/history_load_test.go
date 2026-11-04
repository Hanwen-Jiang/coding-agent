package web

// PERF-00 的 coding-agent 一侧（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md 第 8 节的 PERF-00-CA）：打开会话时网页量
// 从开始加载历史到画完的耗时与请求数（取快照、开事件流），画完之后报一次 POST /api/metrics/history-load
// {mode, duration_ms, requests}。只是计量，不改加载的方式。
//
//   - 有快照：mode 是 snapshot，请求是快照一次加上没结束的任务续传的事件流一条（结束的任务不再开事件流）。
//   - 没有快照（404）：mode 是 replay，请求是取快照的那一次加上每个任务一条事件流。
//   - 一次打开只报一次；报不上（端点不存在）不影响页面，没有未处理的错误。
//
// 实现之前（app.js 里还没有这个端点）这些测试跳过。

import (
	"strings"
	"testing"
)

func requireHistoryLoad(t *testing.T) {
	t.Helper()
	_, app := getFrom(t, Handler(), "/assets/app.js")
	if !strings.Contains(string(app), "/api/metrics/history-load") {
		t.Skip("历史加载的计量还没写：先审验收测试（PERF-00-CA）")
	}
}

// historyLoadProbe 在 app.js 之前包住 fetch，记下报给 /api/metrics/history-load 的内容；等回答画出来，再等报告。
const historyLoadProbe = `
const out = { errors: [], steps: {} };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const reports = [];
const realFetch = window.fetch.bind(window);
window.fetch = (input, init) => {
  const url = typeof input === "string" ? input : input.url;
  if (url.endsWith("/api/metrics/history-load")) {
    try { reports.push(JSON.parse(init.body)); } catch (ex) { out.errors.push("报告不是 JSON：" + ex); }
  }
  return realFetch(input, init);
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms = 4000) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
try {
  out.steps.answer = !!(await wait(() => [...document.querySelectorAll(".answer")].some((a) => a.textContent.includes("__WANT__")), 8000));
  await wait(() => reports.length > 0, 4000);
  await sleep(600);
  out.steps.reports = reports;
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await realFetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

func historyReport(t *testing.T, res planResult) map[string]any {
	t.Helper()
	if len(res.Errors) != 0 {
		t.Fatalf("页面报错（报告报不上也不该有未处理的错误）：%v", res.Errors)
	}
	if res.Steps["answer"] != true {
		t.Fatalf("历史要照常画出来：%v", res.Steps)
	}
	reports, _ := res.Steps["reports"].([]any)
	if len(reports) != 1 {
		t.Fatalf("一次打开只报一次：%v", res.Steps["reports"])
	}
	report, _ := reports[0].(map[string]any)
	if d, ok := report["duration_ms"].(float64); !ok || d < 0 || d > 60000 || d != float64(int64(d)) {
		t.Errorf("duration_ms 是毫秒的整数：%v", report)
	}
	if len(report) != 3 {
		t.Errorf("报告只有 mode、duration_ms、requests 三项：%v", report)
	}
	return report
}

func TestTheHistoryLoadFromTheSnapshotIsReportedInBrowser(t *testing.T) {
	requireHistoryLoad(t)
	snapshot := params{"session_id": "ses_1", "generated_at": "2026-11-03T08:00:00Z", "errors": []any{}, "tasks": []params{
		{"task_id": "t0", "status": "done", "last_event_id": 3, "source": "events", "events": []params{
			snapshotEvent(1, "t0", "task_accepted", params{}),
			snapshotEvent(3, "t0", "task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "快照里的回答"}}}),
		}},
		{"task_id": "t1", "status": "running", "last_event_id": 5, "source": "events", "events": []params{
			snapshotEvent(5, "t1", "task_accepted", params{}),
		}},
	}}
	probe := strings.Replace(historyLoadProbe, "__WANT__", "快照里的回答", 1)
	_, res := runStreamProbe(t, probe, nil, func(s *streamSite) { s.snapshot = snapshot })
	report := historyReport(t, res)
	if report["mode"] != "snapshot" || report["requests"] != float64(2) {
		t.Errorf("有快照：mode 是 snapshot，请求是快照一次加 t1 的事件流一条：%v", report)
	}
}

func TestTheReplayedHistoryLoadIsReportedInBrowser(t *testing.T) {
	requireHistoryLoad(t)
	probe := strings.Replace(historyLoadProbe, "__WANT__", "你好！", 1)
	_, res := runStreamProbe(t, probe, nil)
	report := historyReport(t, res)
	// 批量取事件（HIST-01-WEB）接上之后，退回逐个回放之前还试过一次批量取（这里 404）。
	want := float64(3)
	if _, app := getFrom(t, Handler(), "/assets/app.js"); strings.Contains(string(app), `base + "/events"`) {
		want = 4
	}
	if report["mode"] != "replay" || report["requests"] != want {
		t.Errorf("没有快照：mode 是 replay，请求是取快照（与批量取）各一次加 t0、t1 的事件流各一条：%v", report)
	}
}
