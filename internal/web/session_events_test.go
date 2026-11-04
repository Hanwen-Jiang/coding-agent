package web

// HIST-01 的网页一侧（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md 第 8 节 HIST-01-WEB）：取不到快照时先批量取事件。
//
//   - 快照 404 时 POST <会话>/events，带最近几个任务的游标（从头）；有 more 的任务拿 next_after_event_id 接着取。
//   - 取到的事件直接画出来；结束的任务不再开事件流，没结束的从 next_after_event_id 续传。
//   - Runtime 给不了的任务（found 为假、events_unavailable）退回逐个回放；端点 404 时整个退回逐个回放。
//   - 历史加载的计量：mode 是 events。
//
// 实现之前（app.js 里还没有这个端点）这些测试跳过。

import (
	"fmt"
	"strings"
	"testing"
)

func requireSessionEvents(t *testing.T) {
	t.Helper()
	_, app := getFrom(t, Handler(), "/assets/app.js")
	if !strings.Contains(string(app), `base + "/events"`) {
		t.Skip("批量取事件还没接上：先审验收测试（HIST-01-WEB）")
	}
}

func batchEvent(id int, task, typ string, value params) params {
	return params{"event_id": fmt.Sprint(id), "task_id": task, "type": "TASK_EVENT_TYPE_" + strings.ToUpper(typ), "emitted_at": "2026-11-04T08:00:00Z", "payload": params{"value": value}}
}

func finishedWith(id int, task, answer string) params {
	return batchEvent(id, task, "task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": answer}}})
}

func TestHistoryComesFromTheBatchWhenThereIsNoSnapshotInBrowser(t *testing.T) {
	requireSessionEvents(t)
	reply := params{"tasks": []params{
		{"task_id": "t1", "found": true, "events": []params{batchEvent(5, "t1", "task_accepted", params{})}, "next_after_event_id": "5"},
		{"task_id": "t0", "found": true, "finished": true, "next_after_event_id": "3", "events": []params{
			batchEvent(1, "t0", "task_accepted", params{}), finishedWith(3, "t0", "批量取的回答"),
		}},
	}}
	probe := strings.Replace(historyLoadProbe, "__WANT__", "批量取的回答", 1)
	site, res := runStreamProbe(t, probe, nil, func(s *streamSite) { s.sessionEvents = reply })
	report := historyReport(t, res)
	if report["mode"] != "events" || report["requests"] != float64(3) {
		t.Errorf("mode 是 events，请求是快照、批量取各一次加 t1 的事件流：%v", report)
	}
	if got := site.seen("/tasks/t0/events"); len(got) != 0 {
		t.Errorf("批量取到、已经结束的任务不再开事件流：%v", got)
	}
	if streams := site.seen("/tasks/t1/events"); len(streams) == 0 || !strings.Contains(streams[0], "last_event_id=5") {
		t.Errorf("没结束的任务从 next_after_event_id 续传：%v", streams)
	}
}

func TestTheBatchIsPagedInBrowser(t *testing.T) {
	requireSessionEvents(t)
	pages := func(body map[string]any) any {
		after := "0"
		for _, c := range body["cursors"].([]any) {
			if m := c.(map[string]any); m["task_id"] == "t0" {
				after = fmt.Sprint(m["after_event_id"])
			}
		}
		t0 := params{"task_id": "t0", "found": true, "more": true, "next_after_event_id": "1", "events": []params{batchEvent(1, "t0", "task_accepted", params{})}}
		if after != "0" {
			t0 = params{"task_id": "t0", "found": true, "finished": true, "next_after_event_id": "2", "events": []params{finishedWith(2, "t0", "翻页之后的回答")}}
		}
		return params{"tasks": []params{t0, {"task_id": "t1", "found": true, "next_after_event_id": "0", "events": []params{}}}}
	}
	probe := strings.Replace(historyLoadProbe, "__WANT__", "翻页之后的回答", 1)
	site, res := runStreamProbe(t, probe, nil, func(s *streamSite) { s.sessionEvents = pages })
	historyReport(t, res)
	if got := site.seen("/sessions/s1/events"); len(got) != 2 {
		t.Errorf("有 more 时接着取第二页：%v", got)
	}
	if got := site.seen("/tasks/t0/events"); len(got) != 0 {
		t.Errorf("翻页取完、已经结束的任务不再开事件流：%v", got)
	}
}

func TestTasksTheBatchCannotServeAreReplayedInBrowser(t *testing.T) {
	requireSessionEvents(t)
	reply := params{"tasks": []params{
		{"task_id": "t0", "found": true, "events_unavailable": true, "next_after_event_id": "0"},
		{"task_id": "t1", "found": false, "next_after_event_id": "0"},
	}}
	probe := strings.Replace(historyLoadProbe, "__WANT__", "你好！", 1)
	site, res := runStreamProbe(t, probe, nil, func(s *streamSite) { s.sessionEvents = reply })
	historyReport(t, res)
	if got := site.seen("/tasks/t0/events"); len(got) == 0 {
		t.Errorf("events_unavailable 的任务退回逐个回放：%v", site.seen(""))
	}
}
