package web

// WEB-DELTA（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md 7.1 DELTA 的网页一侧）：回答边生成边显示。
//
//   - Runtime 的 SSE 推 token_delta、thinking_delta（payload：node_id、offset、text），没有 id 行：浏览器的 lastEventId 停在
//     上一条持久事件上，不能拿它去重，否则增量全被当成重复丢掉。
//   - 同一个节点按 offset 拼成草稿 .answer.draft：接着末尾就追加；offset 落回已有范围（activity 重试从 0 重来）就从那里重写；
//     只有思考、还没有正文时显示 .draft-thinking。
//   - task_finished 之后草稿让位给最终的回答。
//
// 真正执行 JS 的检查用浏览器（找不到时跳过）。

import (
	"strings"
	"testing"
)

const deltaProbe = `
const out = { errors: [], steps: {} };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms = 4000) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
const text = (sel) => { const el = document.querySelector(sel); return el ? el.textContent : null; };
const push = (name, value, delta = false) => fetch("/__push", { method: "POST", body: JSON.stringify({ name, value, delta }) });
const delta = (name, offset, piece) => push(name, { node_id: "answer", offset, text: piece }, true);
try {
  await wait(() => document.querySelector("#turn-t1"), 8000);
  await delta("thinking_delta", 0, "先看看测试");
  out.steps.thinking = await wait(() => text(".draft-thinking"));
  await delta("token_delta", 0, "原因是");
  await delta("token_delta", 3, "断言写反了😀");
  await delta("token_delta", 9, "，已修好。");
  out.steps.draft = await wait(() => (text(".answer.draft") || "").includes("已修好") && text(".answer.draft"));
  out.steps.thinkingAfterAnswer = text(".draft-thinking");
  await delta("token_delta", 0, "重来一遍");
  out.steps.restart = await wait(() => text(".answer.draft") === "重来一遍" && text(".answer.draft"));
  await push("task_finished", { status: "TASK_STATUS_DONE", deliverable: { value: { answer: "最终的回答" } } });
  out.steps.final = await wait(() => (text("#turn-t1 .answer") || "").includes("最终的回答") && text("#turn-t1 .answer"));
  out.steps.draftAfter = text(".answer.draft");
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

func TestDeltasDrawADraftAnswerInBrowser(t *testing.T) {
	_, res := runStreamProbe(t, deltaProbe, []streamEvent{
		{"task_accepted", params{}},
		{"round_started", params{"round_seq": 1, "rerun": false}},
		{"node_started", params{"round_seq": 1, "node_id": "answer", "role": "deliver"}},
	})
	if len(res.Errors) != 0 {
		t.Fatalf("页面报错：%v", res.Errors)
	}
	str := func(k string) string { s, _ := res.Steps[k].(string); return s }
	if !strings.Contains(str("thinking"), "先看看测试") {
		t.Errorf("只有思考时显示思考：%v", res.Steps["thinking"])
	}
	if got := str("draft"); got != "原因是断言写反了😀，已修好。" {
		t.Errorf("按 offset 拼成草稿（offset 按字符数，表情也算一个）：%q", got)
	}
	if res.Steps["thinkingAfterAnswer"] != nil {
		t.Errorf("有了正文就不再显示思考：%v", res.Steps["thinkingAfterAnswer"])
	}
	if str("restart") != "重来一遍" {
		t.Errorf("offset 落回 0 就从头重写：%v", res.Steps["restart"])
	}
	if !strings.Contains(str("final"), "最终的回答") || res.Steps["draftAfter"] != nil {
		t.Errorf("结束后草稿让位给最终的回答：%v %v", res.Steps["final"], res.Steps["draftAfter"])
	}
}
