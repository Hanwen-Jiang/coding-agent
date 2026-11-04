package web

// WEB-SUB（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md；N-16 的网页一侧，n16-sub-agents-implementation-2026-11-03.md“没做的”）：
// 并行子 Agent 的进度。Runtime 对 mode 为 sub_agent 的 Goal Node 推 node_started（带 mode），之后 node_id 是它的 tool_status、
// context_usage 是这个子 Agent 的进度（几个子 Agent 的事件交错到达），node_finished 带 status。
//
//   - 有子 Agent 的任务在这一轮里画 .subagents，标题 subagent.title；每个子 Agent 一行 .subagent（按开始的先后），data-node 是 node_id，
//     data-state 是 running（还没 node_finished）、succeeded 或 failed（其余结局都算 failed）；
//     .subagent-steps 是它的工具调用次数（按 step 数，不按 running/succeeded 两条事件数），.subagent-last 是最近一次调用的工具与 target。
//   - 不是子 Agent 的节点（例如普通的 tool_loop）的调用不进这一区；没有子 Agent 的任务不画。
//   - 新的文字两种语言都有。
//
// 实现之前（app.js 里还没有 subagent 的文字）这些测试跳过。

import (
	"encoding/json"
	"strings"
	"testing"
)

func requireSubAgents(t *testing.T) {
	t.Helper()
	_, app := getFrom(t, Handler(), "/assets/app.js")
	if !strings.Contains(string(app), "subagent.title") {
		t.Skip("子 Agent 的进度还没写：先审验收测试（WEB-SUB）")
	}
}

func TestTheSubAgentTextIsInBothLanguages(t *testing.T) {
	requireSubAgents(t)
	zh, en := dictionaries(t)
	for _, key := range []string{"subagent.title", "subagent.steps", "subagent.state.running", "subagent.state.succeeded", "subagent.state.failed"} {
		if _, ok := zh[key]; !ok {
			t.Errorf("中文字典缺 %s", key)
		}
		if _, ok := en[key]; !ok {
			t.Errorf("英文字典缺 %s", key)
		}
	}
}

// subAgentProbe 读子 Agent 区，推两条 node_finished 之后再读一次。
const subAgentProbe = `
const out = { errors: [], steps: {} };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms = 4000) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
const $$ = (sel, root = document) => (root ? [...root.querySelectorAll(sel)] : []);
const push = (name, value) => fetch("/__push", { method: "POST", body: JSON.stringify({ name, value }) });
const read = () => $$(".subagents").map((box) => ({
  title: (box.querySelector(".subagents-title") || {}).textContent || null,
  rows: $$(".subagent", box).map((r) => ({
    node: r.dataset.node, state: r.dataset.state,
    steps: (r.querySelector(".subagent-steps") || {}).textContent || null,
    last: (r.querySelector(".subagent-last") || {}).textContent || null,
  })),
}));
try {
  await wait(() => $$(".subagent").length >= 2, 8000);
  await sleep(200);
  out.steps.first = read();
  await push("node_finished", { event: "node_finished", round_seq: 1, node_id: "scan_docs", role: "fetch", status: "succeeded" });
  await push("node_finished", { event: "node_finished", round_seq: 1, node_id: "scan_tests", role: "fetch", status: "failed", error: "超时" });
  await wait(() => $$(".subagent[data-state=running]").length === 0, 4000);
  out.steps.second = read();
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

type subAgentRow struct {
	Node  string `json:"node"`
	State string `json:"state"`
	Steps string `json:"steps"`
	Last  string `json:"last"`
}

func subAgentsOf(t *testing.T, raw any) (string, []subAgentRow) {
	t.Helper()
	data, _ := json.Marshal(raw)
	var boxes []struct {
		Title string        `json:"title"`
		Rows  []subAgentRow `json:"rows"`
	}
	if err := json.Unmarshal(data, &boxes); err != nil || len(boxes) != 1 {
		t.Fatalf("应只有一个子 Agent 区（只给有子 Agent 的任务画）：%s", data)
	}
	return boxes[0].Title, boxes[0].Rows
}

func TestParallelSubAgentsInBrowser(t *testing.T) {
	requireSubAgents(t)
	tool := func(node string, step int, name, state, target string) streamEvent {
		return streamEvent{"tool_status", params{"event": "tool_status", "round_seq": 1, "node_id": node, "step": step, "tool": name, "state": state, "target": target}}
	}
	_, res := runStreamProbe(t, subAgentProbe, []streamEvent{
		{"task_accepted", params{}},
		{"round_started", params{"round_seq": 1, "rerun": false}},
		{"stage_started", params{"round_seq": 1, "goal": "分头查", "exit_when": "都查完", "nodes": []any{}}},
		{"node_started", params{"event": "node_started", "round_seq": 1, "node_id": "scan_tests", "kind": "goal", "role": "fetch", "mode": "sub_agent"}},
		{"node_started", params{"event": "node_started", "round_seq": 1, "node_id": "scan_docs", "kind": "goal", "role": "fetch", "mode": "sub_agent"}},
		{"node_started", params{"event": "node_started", "round_seq": 1, "node_id": "fix", "kind": "goal", "role": "act", "mode": "tool_loop"}},
		tool("scan_tests", 1, "repo_list", "running", "tests/"),
		tool("scan_docs", 1, "repo_read", "running", "README.md"),
		tool("scan_tests", 1, "repo_list", "succeeded", "tests/"),
		tool("fix", 1, "repo_edit", "running", "src/calc.py"),
		tool("scan_tests", 2, "repo_grep", "running", "def test_ in tests"),
	})
	if len(res.Errors) != 0 {
		t.Fatalf("页面报错：%v", res.Errors)
	}

	title, rows := subAgentsOf(t, res.Steps["first"])
	if title != "并行的子 Agent" {
		t.Errorf("子 Agent 区的标题（subagent.title）：%q", title)
	}
	if len(rows) != 2 || rows[0].Node != "scan_tests" || rows[1].Node != "scan_docs" {
		t.Fatalf("只有 mode 为 sub_agent 的两个，按开始的先后（fix 不在里面）：%+v", rows)
	}
	if rows[0].State != "running" || !strings.Contains(rows[0].Steps, "2") || !strings.Contains(rows[0].Last, "repo_grep") || !strings.Contains(rows[0].Last, "def test_ in tests") {
		t.Errorf("scan_tests：两次调用，最近的是 repo_grep：%+v", rows[0])
	}
	if rows[1].State != "running" || !strings.Contains(rows[1].Steps, "1") || !strings.Contains(rows[1].Last, "README.md") {
		t.Errorf("scan_docs：一次调用：%+v", rows[1])
	}

	_, rows = subAgentsOf(t, res.Steps["second"])
	if len(rows) != 2 || rows[0].State != "failed" || rows[1].State != "succeeded" {
		t.Errorf("结束之后：scan_tests 失败、scan_docs 成功：%+v", rows)
	}
}
