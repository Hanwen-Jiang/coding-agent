package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Runtime 进度事件里的代码（static/codes.js）。代码表见 Runtime 的 docs/plan/eval-observability-and-runtime-fixes.md 第 11.5、11.8 节。
// 字典与 app.js 的静态检查总是跑。网页没有构建工具，也没有 JS 测试框架：真正执行 JS 的检查用本机的 Chrome 或 Edge（headless），
// 拉起真正的 app.js 对着假的 API 与事件流，读画出来的文字；找不到浏览器时跳过（CODING_TEST_BROWSER 可以指定）。

type params = map[string]any

// fallbackText 是事件自己的文字：翻译了就不该看到它。
const fallbackText = "RAW_FALLBACK"

// codeCase 是一个代码在 codes.js 里的翻译。
type codeCase struct {
	key    string   // i18n.js 的键
	fn     string   // codes.js 里翻译它的函数
	event  params   // 事件的载荷
	values []string // 文字里要出现的参数值（两种语言相同）
}

func reason(code string, p params, values ...string) codeCase {
	return codeCase{"reason." + code, "reasonText", params{"reason_code": code, "params": p, "reason": fallbackText}, values}
}

func question(code string, p params, values ...string) codeCase {
	return codeCase{"question." + code, "questionText", params{"question_code": code, "params": p, "text": fallbackText}, values}
}

func nodeError(code string, p params, values ...string) codeCase {
	return codeCase{"error." + code, "errorText", params{"error_code": code, "params": p, "error": fallbackText}, values}
}

var reasonCases = []codeCase{
	reason("rule_fallback", nil),
	reason("rule_node_failed", params{"nodes": []string{"n1", "n2"}}, "n1", "n2"),
	reason("rule_failed_twice", params{"node": "n1", "times": 2}, "n1", "2"),
	reason("rule_failed_thrice", params{"node": "n1", "times": 3}, "n1", "3"),
	reason("rule_denied", params{"nodes": []string{"n1"}}, "n1"),
	reason("rule_same_error_after_retry", params{"node": "n1", "retries": 2}, "n1", "2"),
	reason("rule_merge_conflict", params{"node": "n1"}, "n1"),
	reason("rule_replan_no_progress", params{"replans": 2}, "2"),
	reason("rule_unverified_quality", params{"nodes": []string{"n1"}}, "n1"),
	reason("rule_budget_spent", params{"spent_ratio": 1.02}, "102%"),
	reason("rule_budget_alarm", params{"spent_ratio": 0.85, "completion": 0.4}, "85%", "40%"),
	reason("rule_cap_reached", params{"step": "replan", "used": 2, "cap": 2, "next": "replan_change_shape"}, "2/2"),
	reason("rule_criteria_not_met", nil),
	reason("rule_no_deliver", nil),
	reason("rule_no_checkpoint", params{"checkpoint": "cp1"}, "cp1"),
	reason("rule_unknown_recipe", params{"recipe": "r1"}, "r1"),
	reason("rule_recipe_over_ceiling", params{"recipe": "r1", "risk": "write_irreversible", "ceiling": "read"}, "r1"),
	reason("rule_regression", params{"artifacts": []string{"a.py"}}, "a.py"),
	reason("budget_stopped", params{"budget": "wall_clock"}),
	reason("budget_no_reply", params{"budget": "tool_calls"}),
	reason("human_stopped", nil),
	reason("human_no_reply", nil),
	reason("finish_rejected", params{"count": 2}, "2"),
	reason("workspace_empty", params{"nodes": []string{"n1"}}),
}

// codeCases 是要翻译的全部代码。规则生成的问题用 Verdict 的代码（11.8），所以每个理由也要能当问题问。
func codeCases() []codeCase {
	out := slices.Clone(reasonCases)
	for _, c := range reasonCases {
		code := strings.TrimPrefix(c.key, "reason.")
		out = append(out, codeCase{c.key, "questionText", params{"question_code": code, "params": c.event["params"], "text": fallbackText}, c.values})
	}
	out = append(out,
		question("budget_exhausted", params{"budget": "wall_clock", "limit": 30, "used": 31.5}, "30", "31.5"),
		question("finish_rejected", params{"count": 2}, "2"),
		question("rule_recipe_over_ceiling", params{"recipe": "r1", "risk": "write_irreversible", "ceiling": "read"}, "r1"),

		nodeError("budget_exhausted", params{"budget": "tool_calls", "limit": 5, "used": 6}, "5", "6"),
		nodeError("max_iterations", nil),
		nodeError("repeated_error", nil),
		nodeError("upstream_failed", params{"node": "n1"}, "n1"),
		nodeError("tool_not_allowed", nil),
		nodeError("tool_rejected", nil),
		nodeError("activity_timeout", nil),
		nodeError("models_exhausted", nil),
		nodeError("file_not_found", nil),
		nodeError("dir_not_found", nil),
		nodeError("path_outside_workspace", nil),
		nodeError("path_not_allowed", nil),
		nodeError("command_not_found", nil),
		nodeError("edit_no_match", nil),
		nodeError("edit_ambiguous", nil),
		nodeError("invalid_pattern", nil),
		nodeError("invalid_url", nil),
		nodeError("fetch_failed", nil),
		nodeError("dns_failed", nil),
		nodeError("private_address", nil),
		nodeError("host_not_allowed", nil),
		nodeError("network_not_approved", nil),
		nodeError("network_needs_admin", nil),
		nodeError("sandbox_unavailable", nil),
		nodeError("mcp_session_lost", nil),

		codeCase{"goal.reply_to_message", "goalText", params{"goal_code": "reply_to_message", "goal": fallbackText}, nil},
		codeCase{"goal.run_recipe", "goalText", params{"goal_code": "run_recipe", "params": params{"recipe": "ci_fix@v2"}, "goal": fallbackText}, []string{"ci_fix@v2"}},
		codeCase{"goal.explore_task", "goalText", params{"goal_code": "explore_task", "goal": fallbackText}, nil},
		codeCase{"exitWhen.reply_given", "exitWhenText", params{"exit_when_code": "reply_given", "exit_when": fallbackText}, nil},
		codeCase{"exitWhen.recipe_delivered", "exitWhenText", params{"exit_when_code": "recipe_delivered", "exit_when": fallbackText}, nil},
		codeCase{"exitWhen.node_result", "exitWhenText", params{"exit_when_code": "node_result", "params": params{"node": "explore"}, "exit_when": fallbackText}, []string{"explore"}},
	)
	return out
}

// otherKeys 是代码的翻译用到的其余键与它们能用的占位。
var otherKeys = map[string][]string{
	"question.fromReason":         {"why"},
	"budget.wall_clock":           nil,
	"budget.tool_calls":           nil,
	"budget.usd":                  nil,
	"budget.spent.wall_clock":     {"limit", "used"},
	"budget.spent.tool_calls":     {"limit", "used"},
	"budget.spent.usd":            {"limit", "used"},
	"ladder.repair_retry":         nil,
	"ladder.repair_change_method": nil,
	"ladder.replan":               nil,
	"ladder.replan_change_shape":  nil,
	"ladder.rollback":             nil,
	"ladder.escalate":             nil,
	"ladder.escalate_tier":        nil,
	"ladder.escalate_human":       nil,
	"ladder.abort":                nil,
	"effect.read":                 nil,
	"effect.write_reversible":     nil,
	"effect.write_irreversible":   nil,
	"retry.step.plan_round":       nil,
	"retry.step.review_round":     nil,
	"retry.step.tool_loop_step":   {"node"},
	"retry.call":                  nil,
	"retry.timeout":               {"what", "attempt", "max"},
	"retry.unavailable":           {"what", "attempt", "max"},
	"retry.failed":                {"what", "attempt", "max"},
	"step.nodeSkipped":            {"id"},
}

// exactCases 按两种语言核对整句：decision_retry、参数的换算，以及翻不了时回落到事件自己的文字。
var exactCases = []struct {
	fn     string
	event  params
	zh, en string
}{
	{"retryTitle", params{"step": "plan_round", "attempt": 2, "max_attempts": 5, "error_code": "activity_timeout"},
		"规划调用超时，正在重试（2/5）", "Planning call timed out, retrying (2/5)"},
	{"retryTitle", params{"step": "review_round", "attempt": 3, "max_attempts": 5, "error_code": "grpc_deadline_exceeded"},
		"评审调用超时，正在重试（3/5）", "Review call timed out, retrying (3/5)"},
	{"retryTitle", params{"step": "tool_loop_step", "attempt": 2, "max_attempts": 5, "node_id": "n1", "error_code": "models_exhausted"},
		"n1 的模型调用失败：模型供应商暂时不可用，正在重试（2/5）", "Model call for n1 failed: the model provider is temporarily unavailable. Retrying (2/5)"},
	{"retryTitle", params{"step": "plan_round", "attempt": 2, "max_attempts": 5},
		"规划调用没有成功，正在重试（2/5）", "Planning call failed, retrying (2/5)"},
	{"retryTitle", params{"step": "plan_round", "attempt": 2, "max_attempts": 5, "error_code": "constructor"},
		"规划调用没有成功，正在重试（2/5）", "Planning call failed, retrying (2/5)"},
	{"retryTitle", params{"step": "brand_new_step", "attempt": 2, "max_attempts": 5},
		"模型调用没有成功，正在重试（2/5）", "Model call failed, retrying (2/5)"},
	{"retryDetail", params{"step": "plan_round", "error_code": "activity_timeout", "error": fallbackText}, "", ""},
	{"retryDetail", params{"step": "plan_round", "error_code": "brand_new", "error": fallbackText}, fallbackText, fallbackText},
	{"retryDetail", params{"step": "plan_round", "error_code": "brand_new"}, "brand_new", "brand_new"},

	{"reasonText", params{"reason_code": "rule_cap_reached", "params": params{"step": "repair_retry", "used": 2, "cap": 2, "next": "escalate_human"}},
		"原地重试已用满上限（2/2），改为问你", "Limit reached for retrying in place (2/2); moving on to asking you"},
	{"reasonText", params{"reason_code": "rule_regression", "params": params{"artifacts": []string{"a.py", "b.py"}}},
		"这一轮改坏了已有的产物（a.py、b.py），退回最近的检查点", "This round broke existing work (a.py, b.py); rolling back to the latest checkpoint"},
	{"reasonText", params{"reason_code": "rule_budget_spent", "params": params{"spent_ratio": 0.95}},
		"预算已用 95%，只能问你，或用现有结果收尾", "95% of the budget is spent; the only options are asking you or wrapping up with the results so far"},
	{"reasonText", params{"reason_code": "budget_stopped", "params": params{"budget": "wall_clock"}},
		"时间预算用完，你选择停止，用现有结果收尾", "The time budget ran out and you chose to stop; wrapping up with the results so far"},
	{"reasonText", params{"reason_code": "rule_recipe_over_ceiling", "params": params{"recipe": "r1", "risk": "write_irreversible", "ceiling": "read"}},
		"r1 的风险（不可撤销的写操作）超出这个任务的上限（只读），要先问你", "r1 needs irreversible writes, above this task's limit of read-only; asking you first"},
	{"questionText", params{"question_code": "budget_exhausted", "params": params{"budget": "tool_calls", "limit": 50, "used": 50}},
		"工具调用预算 50 次已经用完（已用 50 次）。原样继续（再加一份同样的预算）、按补充说明重新规划，还是停下并用现有结果收尾？",
		"The tool-call budget (50) is used up (50 so far). Continue as is (adds the same budget again), replan with your note, or stop and wrap up with the results so far?"},
	{"errorText", params{"error_code": "budget_exhausted", "params": params{"budget": "usd", "limit": 2, "used": 2.0312}},
		"费用预算 $2.00 已经用完（已花 $2.03）", "The cost budget ($2.00) is used up ($2.03 so far)"},
	{"errorText", params{"error_code": "budget_exhausted", "params": params{"budget": "wall_clock", "limit": 30, "used": 31.52}},
		"时间预算 30 分钟已经用完（已用 31.52 分钟）", "The time budget (30 min) is used up (31.52 min so far)"},

	// 不认识的代码、没有代码、少了参数、不认识的预算：一律用事件自己的文字。
	{"reasonText", params{"reason_code": "brand_new", "reason": fallbackText}, fallbackText, fallbackText},
	{"reasonText", params{"reason": fallbackText}, fallbackText, fallbackText},
	{"reasonText", params{"reason_code": "rule_cap_reached", "reason": fallbackText}, fallbackText, fallbackText},
	{"reasonText", params{"reason_code": "budget_stopped", "params": params{"used": 1}, "reason": fallbackText}, fallbackText, fallbackText},
	{"questionText", params{"question_code": "brand_new", "text": fallbackText}, fallbackText, fallbackText},
	{"questionText", params{"text": fallbackText}, fallbackText, fallbackText},
	{"questionText", params{"question_code": "budget_exhausted", "params": params{"budget": "gpu_hours", "limit": 1, "used": 1}, "text": fallbackText}, fallbackText, fallbackText},
	{"questionText", params{"question_code": "budget_exhausted", "text": fallbackText}, fallbackText, fallbackText},
	{"errorText", params{"error_code": "brand_new", "error": fallbackText}, fallbackText, fallbackText},
	{"errorText", params{"error": fallbackText}, fallbackText, fallbackText},
	{"errorText", params{"error_code": "upstream_failed", "error": fallbackText}, fallbackText, fallbackText},
	{"errorText", params{"error_code": "brand_new"}, "", ""},
	{"goalText", params{"goal_code": "brand_new", "goal": fallbackText}, fallbackText, fallbackText},
	{"goalText", params{"goal": fallbackText}, fallbackText, fallbackText},
	{"goalText", params{"goal_code": "run_recipe", "goal": fallbackText}, fallbackText, fallbackText},
	{"exitWhenText", params{"exit_when_code": "brand_new", "exit_when": fallbackText}, fallbackText, fallbackText},
	{"exitWhenText", params{"exit_when": fallbackText}, fallbackText, fallbackText},
	{"exitWhenText", params{"exit_when_code": "node_result", "exit_when": fallbackText}, fallbackText, fallbackText},
	// goal_code 不再顶替 exit_when_code：Runtime 没有给退出条件的代码时用事件的文字。
	{"exitWhenText", params{"goal_code": "reply_to_message", "exit_when": fallbackText}, fallbackText, fallbackText},

	// 代码写的固定规划。
	{"goalText", params{"goal_code": "reply_to_message", "goal": fallbackText}, "回复这条消息", "Reply to this message"},
	{"goalText", params{"goal_code": "run_recipe", "params": params{"recipe": "ci_fix@v2"}, "goal": fallbackText},
		"按 ci_fix@v2 完成这个任务", "Complete this task with ci_fix@v2"},
	{"goalText", params{"goal_code": "explore_task", "goal": fallbackText}, "探索并推进这个任务", "Explore and move this task forward"},
	{"exitWhenText", params{"exit_when_code": "reply_given", "exit_when": fallbackText}, "给出回复", "a reply is given"},
	{"exitWhenText", params{"exit_when_code": "recipe_delivered", "exit_when": fallbackText}, "Recipe 跑完，结果已交付", "the recipe finishes and the result is delivered"},
	{"exitWhenText", params{"exit_when_code": "node_result", "params": params{"node": "explore"}, "exit_when": fallbackText}, "explore 给出结果", "explore returns a result"},
}

// --- 字典（总是跑） ---

var (
	dictEntry   = regexp.MustCompile(`(?m)^  "([^"]+)": (.+),$`)
	placeholder = regexp.MustCompile(`\{(\w+)\}`)
	funcArgs    = regexp.MustCompile(`^\(\{([^}]*)\}\) =>`)
	chinese     = regexp.MustCompile(`[\p{Han}（），。：；？、]`)
)

// dictValue 是字典的一项：字符串（占位取自文字）或函数（占位取自解构的参数）。
type dictValue struct {
	isFunc bool
	names  []string
}

func dictionaries(t *testing.T) (zh, en map[string]dictValue) {
	t.Helper()
	data, err := static.ReadFile("static/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	zhAt, enAt := strings.Index(src, "const ZH = {"), strings.Index(src, "const EN = {")
	end := strings.Index(src[enAt:], "\n};")
	if zhAt < 0 || enAt < zhAt || end < 0 {
		t.Fatal("i18n.js 里找不到 ZH、EN 两份字典")
	}
	parse := func(block string) map[string]dictValue {
		out := map[string]dictValue{}
		for _, m := range dictEntry.FindAllStringSubmatch(block, -1) {
			v := dictValue{}
			if s, err := strconv.Unquote(m[2]); err == nil {
				for _, p := range placeholder.FindAllStringSubmatch(s, -1) {
					v.names = append(v.names, p[1])
				}
			} else {
				v.isFunc = true
				if a := funcArgs.FindStringSubmatch(m[2]); a != nil {
					for _, name := range strings.Split(a[1], ",") {
						v.names = append(v.names, strings.TrimSpace(name))
					}
				}
			}
			slices.Sort(v.names)
			v.names = slices.Compact(v.names)
			out[m[1]] = v
		}
		return out
	}
	return parse(src[zhAt:enAt]), parse(src[enAt : enAt+end])
}

// 两份字典同样的键、同样的占位（CLAUDE.md 的硬性规则）。
func TestDictionariesMatch(t *testing.T) {
	zh, en := dictionaries(t)
	if len(zh) < 100 {
		t.Fatalf("只解析出 %d 个键，解析不对", len(zh))
	}
	for key, z := range zh {
		e, ok := en[key]
		if !ok {
			t.Errorf("英文字典少了 %s", key)
		} else if !slices.Equal(z.names, e.names) {
			t.Errorf("%s 的占位不同：中文 %v，英文 %v", key, z.names, e.names)
		}
	}
	for key := range en {
		if _, ok := zh[key]; !ok {
			t.Errorf("中文字典少了 %s", key)
		}
	}
}

// 代码表里的每个代码两种语言都有翻译。只能是字符串：少了参数时才能看出来、回落到事件自己的文字，函数看不出来。
func TestRuntimeCodesAreTranslated(t *testing.T) {
	zh, en := dictionaries(t)
	check := func(key string, allowed []string) {
		for lang, dict := range map[string]map[string]dictValue{"zh": zh, "en": en} {
			v, ok := dict[key]
			switch {
			case !ok:
				t.Errorf("%s 少了 %s", lang, key)
			case v.isFunc:
				t.Errorf("%s 的 %s 要写成字符串，不能是函数", lang, key)
			}
			for _, name := range v.names {
				if !slices.Contains(allowed, name) {
					t.Errorf("%s 的 %s 用了事件没有的参数 {%s}（有 %v）", lang, key, name, allowed)
				}
			}
		}
	}
	for _, c := range codeCases() {
		var allowed []string
		if p, ok := c.event["params"].(params); ok {
			for name := range p {
				allowed = append(allowed, name)
			}
			if p["budget"] != nil {
				allowed = append(allowed, "spent") // codes.js 按预算的种类算出来的
			}
		}
		check(c.key, allowed)
	}
	for key, allowed := range otherKeys {
		check(key, allowed)
	}
}

// EventSource 的命名事件只送给 addEventListener 过的名字，onmessage 收不到：漏了就一声不响地看不见。
func TestAppListensForDecisionRetry(t *testing.T) {
	data, err := static.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)const EVENT_TYPES = \[(.*?)\];`).FindSubmatch(data)
	if m == nil {
		t.Fatal("app.js 里找不到 EVENT_TYPES")
	}
	if !strings.Contains(string(m[1]), `"decision_retry"`) {
		t.Errorf("EVENT_TYPES 没有 decision_retry：%s", m[1])
	}
}

// --- 在浏览器里跑 ---

func findBrowser() string {
	if p := os.Getenv("CODING_TEST_BROWSER"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome", "msedge", "microsoft-edge"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		for _, rel := range []string{`Google\Chrome\Application\chrome.exe`, `Microsoft\Edge\Application\msedge.exe`} {
			if root := os.Getenv(env); root != "" {
				if p := filepath.Join(root, rel); fileExists(p) {
					return p
				}
			}
		}
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// pageEvent 是假事件流里的一个事件；step 是它画成的一步在两种语言里的样子（问题不画成步骤）。
type pageEvent struct {
	name  string
	value params
	zh    *pageStep
	en    *pageStep
}

type pageStep struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func stepOf(title, detail string) *pageStep { return &pageStep{title, detail} }

var replies = []string{"continue_as_is", "replan_with_guidance", "stop_and_wrap_up"}

// pageEvents 覆盖 app.js 里每种接线：有代码的翻译，代码不认识或没有代码的用事件的文字。
var pageEvents = []pageEvent{
	{"task_accepted", params{}, stepOf("任务已受理", ""), stepOf("Task accepted", "")},
	{"round_started", params{"round_seq": 1, "rerun": false}, stepOf("开始第 1 轮", ""), stepOf("Round 1 started", "")},
	{"stage_started", params{"round_seq": 1, "goal": "回复用户这条消息", "goal_code": "reply_to_message", "exit_when": "answer 给出回复", "exit_when_code": "reply_given", "nodes": []any{}},
		stepOf("计划：回复这条消息", "完成标准：给出回复"), stepOf("Plan: Reply to this message", "Done when: a reply is given")},
	{"stage_started", params{"round_seq": 2, "goal": "RAW_GOAL", "exit_when": "RAW_EXIT", "nodes": []any{}},
		stepOf("计划：RAW_GOAL", "完成标准：RAW_EXIT"), stepOf("Plan: RAW_GOAL", "Done when: RAW_EXIT")},
	{"stage_started", params{"round_seq": 3, "goal": "按 ci_fix@v2 完成：修一下 CI", "goal_code": "run_recipe", "exit_when": "Recipe 的子图完成，结果已交付", "exit_when_code": "recipe_delivered", "params": params{"recipe": "ci_fix@v2"}, "nodes": []any{}},
		stepOf("计划：按 ci_fix@v2 完成这个任务", "完成标准：Recipe 跑完，结果已交付"), stepOf("Plan: Complete this task with ci_fix@v2", "Done when: the recipe finishes and the result is delivered")},
	{"stage_started", params{"round_seq": 4, "goal": "探索并推进这个任务", "goal_code": "explore_task", "exit_when": "explore 给出结果", "exit_when_code": "node_result", "params": params{"node": "explore"}, "nodes": []any{}},
		stepOf("计划：探索并推进这个任务", "完成标准：explore 给出结果"), stepOf("Plan: Explore and move this task forward", "Done when: explore returns a result")},
	{"tool_status", params{"node_id": "n1", "step": 1, "tool": "repo_read", "state": "failed", "target": "a.py", "error": "file_not_found：工作区里没有文件 a.py", "error_code": "file_not_found"},
		stepOf("没成功：正在读 a.py", "工作区里没有这个文件"), stepOf("Didn't work: Reading a.py", "That file isn't in the workspace")},
	{"tool_status", params{"node_id": "n1", "step": 2, "tool": "repo_grep", "state": "failed", "target": "x", "error": "RAW_TOOL_ERROR", "error_code": "brand_new"},
		stepOf("没成功：正在搜索 x", "RAW_TOOL_ERROR"), stepOf("Didn't work: Searching x", "RAW_TOOL_ERROR")},
	{"node_finished", params{"node_id": "n1", "role": "deliver", "status": "failed", "error": "工具调用预算 5 次已经用完", "error_code": "budget_exhausted", "params": params{"budget": "tool_calls", "limit": 5, "used": 5}},
		stepOf("失败：n1", "deliver · failed\n工具调用预算 5 次已经用完（已用 5 次）"), stepOf("Failed: n1", "deliver · failed\nThe tool-call budget (5) is used up (5 so far)")},
	{"node_finished", params{"node_id": "n2", "role": "deliver", "status": "skipped", "error": "上游 n1 没有成功", "error_code": "upstream_failed", "params": params{"node": "n1"}},
		stepOf("跳过：n2", "deliver · skipped\n上游 n1 没有成功"), stepOf("Skipped: n2", "deliver · skipped\nUpstream n1 didn't succeed")},
	{"node_finished", params{"node_id": "n3", "role": "deliver", "status": "failed", "error": "RAW_NODE_ERROR", "error_code": "brand_new"},
		stepOf("失败：n3", "deliver · failed\nRAW_NODE_ERROR"), stepOf("Failed: n3", "deliver · failed\nRAW_NODE_ERROR")},
	{"verdict", params{"verdict": "repair", "reason": "中文的理由", "reason_code": "rule_cap_reached", "params": params{"step": "repair_retry", "used": 2, "cap": 2, "next": "repair_change_method"}},
		stepOf("Reviewer 判定：修复（repair）", "原地重试已用满上限（2/2），改为换方法"), stepOf("Reviewer verdict: Repair (repair)", "Limit reached for retrying in place (2/2); moving on to changing method")},
	{"verdict", params{"verdict": "continue", "reason": "RAW_REASON"},
		stepOf("Reviewer 判定：继续（continue）", "RAW_REASON"), stepOf("Reviewer verdict: Continue (continue)", "RAW_REASON")},
	{"verdict", params{"verdict": "abort", "reason": "中文的理由", "reason_code": "human_no_reply"},
		stepOf("Reviewer 判定：终止（abort）", "没有等到回复，按停止处理，用现有结果收尾"), stepOf("Reviewer verdict: Abort (abort)", "No reply came; stopping and wrapping up with the results so far")},
	{"decision_retry", params{"step": "tool_loop_step", "attempt": 3, "max_attempts": 5, "node_id": "n1", "error_code": "models_exhausted"},
		stepOf("n1 的模型调用失败：模型供应商暂时不可用，正在重试（3/5）", ""), stepOf("Model call for n1 failed: the model provider is temporarily unavailable. Retrying (3/5)", "")},
	{"decision_retry", params{"step": "review_round", "attempt": 2, "max_attempts": 5, "error_code": "brand_new", "error": "RAW_RETRY_ERROR"},
		stepOf("评审调用没有成功，正在重试（2/5）", "RAW_RETRY_ERROR"), stepOf("Review call failed, retrying (2/5)", "RAW_RETRY_ERROR")},
	{"decision_retry", params{"step": "plan_round", "attempt": 2, "max_attempts": 5, "error_code": "activity_timeout", "error": "中文的错误"},
		stepOf("规划调用超时，正在重试（2/5）", ""), stepOf("Planning call timed out, retrying (2/5)", "")},
	{"human_input_required", params{"question_id": "_review", "kind": "escalation", "text": "中文的问题", "question_code": "rule_failed_twice", "params": params{"node": "n1", "times": 2}, "options": replies}, nil, nil},
	{"human_input_required", params{"question_id": "q2", "kind": "human", "text": "RAW_QUESTION", "question_code": "brand_new", "options": replies}, nil, nil},
	{"human_input_required", params{"question_id": "_budget", "kind": "escalation", "text": "中文的问题", "question_code": "budget_exhausted", "params": params{"budget": "wall_clock", "limit": 30, "used": 31.5}, "options": replies}, nil, nil},
}

// taskRounds 是 Task State 里的 Round（右栏“上下文”的 Rounds）：stage_goal 是 Runtime 写的文字，每一轮的代码在同一轮的 stage_started 事件里。
var taskRounds = []params{
	{"seq": 1, "stage_goal": "回复用户这条消息", "status": "completed", "verdict": "continue"},
	{"seq": 2, "stage_goal": "RAW_GOAL", "status": "completed", "verdict": "continue"},
	{"seq": 3, "stage_goal": "按 ci_fix@v2 完成：修一下 CI", "status": "completed", "verdict": "continue"},
	{"seq": 4, "stage_goal": "探索并推进这个任务", "status": "completed", "verdict": "continue"},
	{"seq": 5, "stage_goal": "RAW_NO_EVENT", "status": "running"},
}

// probeJS 记下页面的错误，跑 codes.js 的全部用例，等真正的界面把事件画完，再把结果发回测试。
const probeJS = `
const out = { errors: [] };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const text = (el) => (el ? el.textContent : "");
try {
  const plan = await (await fetch("/__plan")).json();
  const codes = await import("/assets/codes.js");
  out.results = plan.cases.map((c) => codes[c.fn](c.event));
  const steps = () => [...document.querySelectorAll(".worklog-steps > .step:not(.now)")];
  const rounds = () => [...document.querySelectorAll(".round-row .g")].map(text);
  // 第一轮换掉了 Task State 的文字，才算 Rounds 与事件都到了。
  const roundsReady = () => rounds().length > 0 && rounds()[0] !== "回复用户这条消息";
  for (let i = 0; i < 600 && (steps().length < plan.steps || !document.querySelector(".c-ask .q") || !roundsReady()); i++) {
    await new Promise((r) => setTimeout(r, 50));
  }
  out.rounds = rounds();
  out.steps = steps().map((el) => ({ title: text(el.querySelector(".step-title")), detail: text(el.querySelector(".step-detail")) }));
  out.now = text(document.querySelector(".worklog-steps > .step.now .step-title"));
  out.ask = text(document.querySelector(".c-ask .q"));
  out.inline = [...document.querySelectorAll(".ask-inline")].map(text);
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

type probeResult struct {
	Errors  []string   `json:"errors"`
	Results []string   `json:"results"`
	Rounds  []string   `json:"rounds"`
	Steps   []pageStep `json:"steps"`
	Now     string     `json:"now"`
	Ask     string     `json:"ask"`
	Inline  []string   `json:"inline"`
}

// fakeSite 是真正的页面加上探针、假的 API 与事件流；事件流发完后一直开着（任务还在跑），直到 done。
func fakeSite(t *testing.T, lang string, cases []probeCase, done <-chan struct{}, results chan<- probeResult) http.Handler {
	t.Helper()
	const base = "/api/projects/p1/sessions/s1"
	task := params{"id": "t1", "status": "TASK_STATUS_RUNNING", "message": "hello", "created_at": "2026-10-28T08:00:00Z"}
	api := map[string]any{
		"/api/config":       params{},
		"/api/me":           params{"tenant": "t1", "user": "u1", "connections": []any{}},
		"/api/projects":     []params{{"id": "p1", "name": "demo"}},
		"/api/samples":      []any{},
		"/api/sessions":     []params{{"id": "s1", "project_id": "p1", "title": "demo"}},
		"/api/tasks/active": []any{},
		base + "/tasks":     []params{task},
		base + "/tasks/t1":  params{"task": task, "runtime": params{"taskState": params{"value": params{"goal": "hello", "done_when": "x", "rounds": taskRounds}}}},
		base + "/env":       params{},
		base + "/changes":   params{"files": []any{}},
	}
	steps := 0
	for _, e := range pageEvents {
		if e.en != nil {
			steps++
		}
	}
	app := Handler()

	mux := http.NewServeMux()
	mux.Handle("/assets/", app)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		_, page := getFrom(t, app, "/")
		// 探针排在 app.js 前面：先接上错误事件，app.js 启动时的错误也记得到。
		html := strings.Replace(string(page), `<script type="module"`, `<script type="module" src="/__probe.js"></script><script type="module"`, 1)
		w.Header().Set("Set-Cookie", "cw_lang="+lang+"; path=/")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("GET /__probe.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(probeJS))
	})
	mux.HandleFunc("GET /__plan", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(params{"cases": cases, "steps": steps})
	})
	mux.HandleFunc("POST /__result", func(w http.ResponseWriter, r *http.Request) {
		var res probeResult
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			res.Errors = append(res.Errors, err.Error())
		}
		select {
		case results <- res:
		default:
		}
	})
	for path, body := range api {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		})
	}
	mux.HandleFunc("GET "+base+"/tasks/t1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i, e := range pageEvents {
			data, _ := json.Marshal(params{"event_id": i + 1, "emitted_at": "2026-10-28T08:00:00Z", "payload": params{"value": e.value}})
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", i+1, e.name, data)
		}
		w.(http.Flusher).Flush()
		select {
		case <-done:
		case <-r.Context().Done():
		}
	})
	return mux
}

// runProbe 用这种语言拉起一次浏览器，等探针发回结果。
func runProbe(t *testing.T, browser, lang string, cases []probeCase) probeResult {
	t.Helper()
	done := make(chan struct{})
	results := make(chan probeResult, 1)
	srv := httptest.NewServer(fakeSite(t, lang, cases, done, results))
	defer srv.Close()

	// 不用 t.TempDir：浏览器的子进程在主进程结束后才陆续退出，Windows 上目录一时删不掉不该让测试失败。
	dir, err := os.MkdirTemp("", "coding-web-browser")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for range 50 {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	defer close(done)

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	// 窗口够宽，右栏才默认打开（Rounds 在那里）。
	cmd := exec.CommandContext(ctx, browser, "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run", "--no-default-browser-check", "--window-size=1400,900",
		"--user-data-dir="+dir, srv.URL+"/#/w/p1/s/s1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉不起浏览器 %s：%v", browser, err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()
	select {
	case res := <-results:
		return res
	case <-ctx.Done():
		t.Fatalf("等不到页面的结果（%s）", browser)
		return probeResult{}
	}
}

type probeCase struct {
	Fn    string `json:"fn"`
	Event params `json:"event"`
}

func TestRuntimeCodesInBrowser(t *testing.T) {
	browser := findBrowser()
	if browser == "" {
		t.Skip("没有找到 Chrome 或 Edge（可以用 CODING_TEST_BROWSER 指定）")
	}
	all := codeCases()
	cases := make([]probeCase, 0, len(all)+len(exactCases))
	for _, c := range all {
		cases = append(cases, probeCase{c.fn, c.event})
	}
	for _, c := range exactCases {
		cases = append(cases, probeCase{c.fn, c.event})
	}
	for _, lang := range []string{"zh", "en"} {
		t.Run(lang, func(t *testing.T) {
			r := runProbe(t, browser, lang, cases)
			if len(r.Errors) > 0 {
				t.Fatalf("页面出错：%q", r.Errors)
			}
			if len(r.Results) != len(cases) {
				t.Fatalf("结果 %d 个，用例 %d 个", len(r.Results), len(cases))
			}

			// 每个代码都译成了这种语言，参数都填上了。
			for i, c := range all {
				got, name := r.Results[i], c.fn+" "+c.key
				switch {
				case got == fallbackText:
					t.Errorf("%s 没翻译，回落到了事件的文字", name)
				case got == "" || placeholder.MatchString(got):
					t.Errorf("%s 翻出来不对：%q", name, got)
				case lang == "en" && chinese.MatchString(got):
					t.Errorf("%s 的英文里有中文：%q", name, got)
				case lang == "zh" && !chinese.MatchString(got):
					t.Errorf("%s 的中文里没有中文：%q", name, got)
				}
				for _, v := range c.values {
					if !strings.Contains(got, v) {
						t.Errorf("%s 少了参数值 %s：%q", name, v, got)
					}
				}
			}
			for i, c := range exactCases {
				want := c.en
				if lang == "zh" {
					want = c.zh
				}
				if got := r.Results[len(all)+i]; got != want {
					t.Errorf("%s(%v)\n得到 %q\n应是 %q", c.fn, c.event, got, want)
				}
			}

			// 真正的界面：事件画成的步骤。
			var want []pageStep
			for _, e := range pageEvents {
				if s := e.en; s != nil {
					if lang == "zh" {
						s = e.zh
					}
					want = append(want, *s)
				}
			}
			if len(r.Steps) != len(want) {
				t.Errorf("画出 %d 步，应是 %d 步：%+v", len(r.Steps), len(want), r.Steps)
			}
			for i := range min(len(r.Steps), len(want)) {
				if r.Steps[i] != want[i] {
					t.Errorf("第 %d 步\n得到 %+v\n应是 %+v", i+1, r.Steps[i], want[i])
				}
			}
			now, ask, inline := map[string]string{
				"zh": "规划调用超时，正在重试（2/5）",
				"en": "Planning call timed out, retrying (2/5)",
			}[lang], map[string]string{
				"zh": "时间预算 30 分钟已经用完（已用 31.5 分钟）。原样继续（再加一份同样的预算）、按补充说明重新规划，还是停下并用现有结果收尾？",
				"en": "The time budget (30 min) is used up (31.5 min so far). Continue as is (adds the same budget again), replan with your note, or stop and wrap up with the results so far?",
			}[lang], map[string][]string{
				"zh": {
					"已回复：n1 已经失败 2 次，不再原地重试，换个方法。原样继续、按补充说明重新规划，还是停下并用现有结果收尾？",
					"已回复：RAW_QUESTION",
				},
				"en": {
					"Replied: n1 has failed 2 times; changing method instead of retrying in place. Continue as is, replan with your note, or stop and wrap up with the results so far?",
					"Replied: RAW_QUESTION",
				},
			}[lang]
			if r.Now != now {
				t.Errorf("此刻那一行\n得到 %q\n应是 %q", r.Now, now)
			}
			// 右栏的 Rounds：有代码的按同一轮的事件翻译；没有代码、没有事件的用 Task State 的文字。
			rounds := map[string][]string{
				"zh": {"回复这条消息", "RAW_GOAL", "按 ci_fix@v2 完成这个任务", "探索并推进这个任务", "RAW_NO_EVENT"},
				"en": {"Reply to this message", "RAW_GOAL", "Complete this task with ci_fix@v2", "Explore and move this task forward", "RAW_NO_EVENT"},
			}[lang]
			if !slices.Equal(r.Rounds, rounds) {
				t.Errorf("右栏的 Rounds\n得到 %q\n应是 %q", r.Rounds, rounds)
			}
			if r.Ask != ask {
				t.Errorf("输入框上方的问题\n得到 %q\n应是 %q", r.Ask, ask)
			}
			if len(r.Inline) != 3 || !slices.Equal(r.Inline[:2], inline) || !strings.HasSuffix(r.Inline[2], ask) {
				t.Errorf("对话里的问题\n得到 %q\n应是 %q 加上 %q", r.Inline, inline, ask)
			}
		})
	}
}
