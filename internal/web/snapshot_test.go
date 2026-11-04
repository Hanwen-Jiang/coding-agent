package web

// WEB-SNAP（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md；N-19 的网页一侧）：用会话快照画首屏、处理游标过期。
//
//   - 打开会话先取 <会话>/snapshot：快照里的事件直接画出来（与事件流的 data 同一种格式），已经结束的任务不再单独回放事件流；
//     没结束的任务从快照的 last_event_id 续传（事件流地址带 last_event_id）。快照里的 plan 照样画进计划区。
//   - 事件流来了 resnapshot_required（coding-web 把 Runtime 的 409 换成这条事件）：重新取快照。
//   - 取不到快照（404：Runtime 还没有这个端点）时照旧逐个任务回放，页面一样能用。
//
// 实现之前（app.js 里还没有 /snapshot）这些测试跳过。

import (
	"strings"
	"testing"
)

func requireSnapshot(t *testing.T) string {
	t.Helper()
	_, app := getFrom(t, Handler(), "/assets/app.js")
	if !strings.Contains(string(app), `/snapshot"`) {
		t.Skip("用快照画首屏还没写：先审验收测试（WEB-SNAP）")
	}
	return string(app)
}

func TestAppListensForResnapshot(t *testing.T) {
	app := requireSnapshot(t)
	start := strings.Index(app, "const EVENT_TYPES = [")
	end := strings.Index(app[start:], "];")
	if start < 0 || end < 0 || !strings.Contains(app[start:start+end], `"resnapshot_required"`) {
		t.Fatal("EVENT_TYPES 里要有 resnapshot_required（EventSource 只收登记了的事件）")
	}
}

func snapshotEvent(id int, task, typ string, value params) params {
	return params{"event_id": id, "task_id": task, "type": "TASK_EVENT_TYPE_" + strings.ToUpper(typ), "emitted_at": "2026-11-03T08:00:00Z", "payload": params{"value": value}}
}

// snapshotProbe 等快照里的回答与计划画出来，推一条 resnapshot_required，再等一会儿让页面重新取快照。
const snapshotProbe = `
const out = { errors: [], steps: {} };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms = 4000) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
const $$ = (sel, root = document) => (root ? [...root.querySelectorAll(sel)] : []);
const push = (name, value) => fetch("/__push", { method: "POST", body: JSON.stringify({ name, value }) });
const answerWith = (s) => $$(".answer").find((a) => a.textContent.includes(s));
try {
  const want = new URLSearchParams(location.search).get("want") || "快照里的回答";
  out.steps.answer = !!(await wait(() => answerWith(want), 8000));
  out.steps.planSteps = $$(".plan-step").length;
  if (want === "快照里的回答") {
    await sleep(300);
    await push("resnapshot_required", {});
    await sleep(1500);
  }
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

func TestTheFirstPaintComesFromTheSnapshotInBrowser(t *testing.T) {
	requireSnapshot(t)
	plan := snapshotEvent(6, "t1", "plan_updated", planUpdated(1, "fix",
		params{"id": "locate", "title": "找到出错的测试", "status": "done", "round": 1},
		params{"id": "fix", "title": "修改代码", "status": "active", "round": 1},
		params{"id": "verify", "title": "跑测试确认", "status": "pending", "round": 1}))
	snapshot := params{"session_id": "ses_1", "generated_at": "2026-11-03T08:00:00Z", "errors": []any{}, "tasks": []params{
		{"task_id": "t0", "status": "done", "last_event_id": 3, "source": "events", "events": []params{
			snapshotEvent(1, "t0", "task_accepted", params{}),
			snapshotEvent(3, "t0", "task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "快照里的回答"}}}),
		}},
		{"task_id": "t1", "status": "running", "last_event_id": 7, "source": "events", "plan": plan, "events": []params{
			snapshotEvent(5, "t1", "task_accepted", params{}),
			plan,
			snapshotEvent(7, "t1", "stage_started", params{"round_seq": 1, "goal": "修复", "exit_when": "测试通过", "nodes": []any{}}),
		}},
	}}
	site, res := runStreamProbe(t, snapshotProbe, nil, func(s *streamSite) { s.snapshot = snapshot })
	if len(res.Errors) != 0 {
		t.Fatalf("页面报错：%v", res.Errors)
	}
	if res.Steps["answer"] != true {
		t.Errorf("结束的任务的回答要从快照画出来：%v", res.Steps)
	}
	if n, _ := res.Steps["planSteps"].(float64); n != 3 {
		t.Errorf("快照里的计划要画进计划区：%v", res.Steps)
	}
	if got := site.seen("/tasks/t0/events"); len(got) != 0 {
		t.Errorf("快照里已经结束的任务不该再回放事件流：%v", got)
	}
	streams := site.seen("/tasks/t1/events")
	if len(streams) == 0 || !strings.Contains(streams[0], "last_event_id=7") {
		t.Errorf("没结束的任务要从快照的游标续传：%v", streams)
	}
	if got := site.seen("/snapshot"); len(got) != 2 {
		t.Errorf("收到 resnapshot_required 要重新取快照（一共两次）：%v", got)
	}
}

func TestWithoutASnapshotTheTasksAreReplayedInBrowser(t *testing.T) {
	requireSnapshot(t)
	site, res := runStreamProbe(t, strings.Replace(snapshotProbe, `|| "快照里的回答"`, `|| "你好！"`, 1), nil)
	if len(res.Errors) != 0 {
		t.Fatalf("页面报错：%v", res.Errors)
	}
	if res.Steps["answer"] != true {
		t.Errorf("没有快照时照旧回放，结束的任务的回答也要画出来：%v", res.Steps)
	}
	if got := site.seen("/tasks/t0/events"); len(got) == 0 {
		t.Errorf("取不到快照时要逐个任务回放：%v", site.seen(""))
	}
}
