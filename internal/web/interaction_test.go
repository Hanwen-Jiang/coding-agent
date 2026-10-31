package web

// Runtime 的 docs/plan/running-task-interaction.md 第 2～4 节验收（网页）。
//
//   - 第 2 节：运行中按 Enter 是纠偏，Ctrl+Enter 立刻打断，排队（下一个任务）在菜单里；还没生效的纠偏可以撤回、修改，已生效的与
//     立刻打断不行；排队中的消息可以修改；Task 结束时还没生效的提示可以作为新任务发送。
//   - 第 3 节：右栏“上下文”显示各类内容的占比（参考 Claude Code 的 /context），不显示内容；原来的内容收进底部折叠的“任务详情”。
//   - 第 4 节：右栏“输出”按 Task、Round 汇总重要文件与重要答案，不显示全文。
//
// 同 codes_test.go：字典与 app.js 的静态检查总是跑；真正执行 JS 的检查用浏览器拉起真正的 app.js，对着假的 API 与事件流，
// 找不到浏览器时跳过（CODING_TEST_BROWSER 指定位置）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppListensForTheNewEvents(t *testing.T) {
	_, app := getFrom(t, Handler(), "/assets/app.js")
	for _, name := range []string{"guidance_updated", "task_message_edited", "context_usage"} {
		if !strings.Contains(string(app), `"`+name+`"`) {
			t.Errorf("app.js 没有接 %s", name)
		}
	}
}

func TestTheNewTextIsInBothLanguages(t *testing.T) {
	zh, en := dictionaries(t)
	for _, key := range []string{
		"composer.steer", "composer.interrupt", "composer.queueNext", "hint.steer", "hint.interrupt",
		"guidance.pending", "guidance.applied", "guidance.retract", "guidance.edit", "guidance.waitIrreversible",
		"guidance.rejected.already_applied", "guidance.rejected.not_found", "guidance.rejected.too_many_pending",
		"guidance.notApplied", "guidance.sendAsTask", "queued.edit", "queued.editTooLate",
		"ctx.free", "ctx.compactionLine", "ctx.tools", "ctx.details",
		"output.round", "output.files", "output.answers", "output.inThread",
		"reason.user_guidance", "reason.user_interrupt",
	} {
		if _, ok := zh[key]; !ok {
			t.Errorf("中文字典缺 %s", key)
		}
		if _, ok := en[key]; !ok {
			t.Errorf("英文字典缺 %s", key)
		}
	}
}

// --- 真正的页面 ---

const secretGoal = "RAW_TASK_STATE_GOAL"
const fullAnswer = "RAW_FULL_ANSWER 这段是回答的全文，右栏不该显示它。"

type recorded struct {
	Path string         `json:"path"`
	Body map[string]any `json:"body"`
}

// interactionSite 是真正的页面加上探针、假的 API 与可以随时往里推事件的事件流。
type interactionSite struct {
	t       *testing.T
	mu      sync.Mutex
	posts   []recorded
	pushes  chan string
	seq     int
	results chan interactionResult
}

type interactionResult struct {
	Errors  []string          `json:"errors"`
	Steps   map[string]any    `json:"steps"`
	Context map[string]string `json:"context"`
}

func (s *interactionSite) frame(name string, value map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	data, _ := json.Marshal(params{"event_id": s.seq, "emitted_at": "2026-10-30T08:00:00Z", "payload": params{"value": value}})
	return fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", s.seq, name, data)
}

func (s *interactionSite) record(path string, body map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posts = append(s.posts, recorded{path, body})
}

func (s *interactionSite) postsTo(suffix string) []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []recorded
	for _, p := range s.posts {
		if strings.HasSuffix(p.Path, suffix) {
			out = append(out, p)
		}
	}
	return out
}

func (s *interactionSite) handler() http.Handler {
	const base = "/api/projects/p1/sessions/s1"
	running := params{"id": "t1", "status": "TASK_STATUS_RUNNING", "message": "修一下测试", "created_at": "2026-10-30T08:00:00Z"}
	queued := params{"id": "t2", "status": "TASK_STATUS_QUEUED", "message": "接下来写文档", "created_at": "2026-10-30T08:01:00Z"}
	done := params{"id": "t0", "status": "TASK_STATUS_DONE", "message": "先看看", "created_at": "2026-10-30T07:00:00Z"}
	state := params{
		"goal": secretGoal, "done_when": "测试通过", "facts": []string{"RAW_FACT"},
		"rounds": []params{
			{"seq": 1, "stage_goal": "定位原因", "status": "completed", "verdict": "continue", "highlights": params{
				"files":   []params{{"path": "src/calc.py", "why": "取整的错误在这里"}, {"path": "src/util.py"}},
				"answers": []string{"失败是因为取整用了 int()。"},
			}},
			{"seq": 2, "stage_goal": "修复", "status": "running"},
		},
	}
	doneResult := params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": fullAnswer}}}
	doneState := params{"goal": "先看看", "done_when": "x", "rounds": []params{{"seq": 1, "stage_goal": "看一遍", "status": "completed", "verdict": "done", "highlights": params{"answers": []string{"看完了。"}}}}}
	api := map[string]any{
		"/api/config":       params{},
		"/api/me":           params{"tenant": "t1", "user": "u1", "connections": []any{}},
		"/api/projects":     []params{{"id": "p1", "name": "demo"}},
		"/api/samples":      []any{},
		"/api/sessions":     []params{{"id": "s1", "project_id": "p1", "title": "demo"}},
		"/api/tasks/active": []any{},
		base + "/tasks":     []params{queued, running, done}, // 服务端新的在前
		base + "/tasks/t0":  params{"task": done, "runtime": params{"taskState": params{"value": doneState}, "result": doneResult}},
		base + "/tasks/t1":  params{"task": running, "runtime": params{"taskState": params{"value": state}}},
		base + "/tasks/t2":  params{"task": queued, "runtime": params{}},
		base + "/env":       params{},
		base + "/changes":   params{"files": []any{}},
	}
	app := Handler()
	mux := http.NewServeMux()
	mux.Handle("/assets/", app)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, page := getFrom(s.t, app, "/")
		html := strings.Replace(string(page), `<script type="module"`, `<script type="module" src="/__probe.js"></script><script type="module"`, 1)
		w.Header().Set("Set-Cookie", "cw_lang=zh; path=/")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("GET /__probe.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(interactionProbe))
	})
	mux.HandleFunc("POST /__push", func(w http.ResponseWriter, r *http.Request) {
		var ev struct {
			Name  string         `json:"name"`
			Value map[string]any `json:"value"`
		}
		_ = json.NewDecoder(r.Body).Decode(&ev)
		s.pushes <- s.frame(ev.Name, ev.Value)
	})
	mux.HandleFunc("POST /__result", func(w http.ResponseWriter, r *http.Request) {
		var res interactionResult
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			res.Errors = append(res.Errors, err.Error())
		}
		select {
		case s.results <- res:
		default:
		}
	})
	for path, body := range api {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		})
	}
	// 前端发的写请求：都记下，都回 ok。提交任务的回答要有 id。
	mux.HandleFunc("POST /api/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.record(r.URL.Path, body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/tasks") {
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(params{"id": "t3", "status": "TASK_STATUS_QUEUED", "message": body["message"], "created_at": "2026-10-30T08:02:00Z"})
			return
		}
		_ = json.NewEncoder(w).Encode(params{"ok": true})
	})
	usage := params{
		"event": "context_usage", "round_seq": 2, "node_id": "fix", "step": 3,
		"window_tokens": 200000, "effective_tokens": 32000, "total_tokens": 20000,
		"categories": []params{
			{"name": "system_prompt", "tokens": 2000}, {"name": "system_tools", "tokens": 4000}, {"name": "mcp_tools", "tokens": 1000},
			{"name": "memory", "tokens": 500}, {"name": "task_brief", "tokens": 1500}, {"name": "messages", "tokens": 11000},
		},
		"tools": []params{{"name": "repo_read", "tokens": 1500}, {"name": "repo_edit", "tokens": 2500}, {"name": "github.search", "tokens": 1000, "mcp": true}},
	}
	mux.HandleFunc("GET "+base+"/tasks/{task}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if r.PathValue("task") == "t1" {
			for _, f := range []struct {
				name  string
				value map[string]any
			}{
				{"task_accepted", params{}},
				{"round_started", params{"round_seq": 2, "rerun": false}},
				{"stage_started", params{"round_seq": 2, "goal": "修复", "exit_when": "测试通过", "nodes": []any{}}},
				{"context_usage", usage},
			} {
				_, _ = fmt.Fprint(w, s.frame(f.name, f.value))
			}
			w.(http.Flusher).Flush()
			for {
				select {
				case frame := <-s.pushes:
					_, _ = fmt.Fprint(w, frame)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	return mux
}

// interactionProbe 真的在输入框里打字、按键、点按钮，把看到的写进 steps。元素找不到时那一步是 null，Go 这边会报错。
const interactionProbe = `
const out = { errors: [], steps: {}, context: {} };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms = 4000) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
const $ = (sel, root = document) => (root ? root.querySelector(sel) : null);
const $$ = (sel, root = document) => (root ? [...root.querySelectorAll(sel)] : []);
const text = (el) => (el ? el.textContent : null);
const push = (name, value) => fetch("/__push", { method: "POST", body: JSON.stringify({ name, value }) });
const type = (s) => { const i = $("#composer"); i.value = s; i.dispatchEvent(new Event("input", { bubbles: true })); return i; };
const press = (i, init = {}) => i.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...init }));
const item = (id) => $('.guidance-item[data-guidance-id="' + id + '"]');
try {
  await wait(() => $("#composer"), 20000);

  // 第 3 节：占比。
  const usage = await wait(() => $(".ctx-usage"));
  out.steps.usageShown = !!usage;
  if (usage) {
    for (const row of $$(".ctx-row", usage)) out.context[row.dataset.cat] = text($(".ctx-label", row)) + "|" + text($(".ctx-num", row)) + "|" + text($(".ctx-pct", row));
    out.steps.segments = $$(".ctx-bar .seg", usage).map((e) => e.dataset.cat);
    out.steps.compactionLine = text($(".ctx-line", usage));
    out.steps.toolsCollapsed = !$(".ctx-tool", usage) || !$(".ctx-tools", usage).open;
    const tools = $(".ctx-tools", usage);
    if (tools) tools.open = true;
    out.steps.tools = $$(".ctx-tool", usage).map(text);
  }
  const contextSection = $$(".sp").find((e) => $(".ctx-usage", e));
  const details = contextSection && $(".task-details", contextSection);
  out.steps.detailsCollapsed = details ? !details.open : null;
  out.steps.detailsHasGoal = details ? text(details).includes("` + secretGoal + `") : null;
  if (contextSection) {
    const copy = contextSection.cloneNode(true);
    const inner = $(".task-details", copy);
    if (inner) inner.remove();
    out.steps.goalOutsideDetails = text(copy).includes("` + secretGoal + `");
  }

  // 第 4 节：各轮汇总。
  const rounds = await wait(() => $$(".out-round").length >= 2 && $$(".out-round"));
  out.steps.outputRounds = rounds ? rounds.map((r) => ({
    task: r.dataset.task, round: r.dataset.round,
    files: $$(".out-file", r).map((f) => ({ path: text($(".out-path", f)), why: text($(".out-why", f)) })),
    answers: $$(".out-answer", r).map((a) => text($(".out-text", a))),
    goto: !!$(".out-goto", r),
  })) : null;
  const outputSection = $$(".sp").find((e) => $(".out-round", e));
  out.steps.outputHasFullAnswer = outputSection ? text(outputSection).includes("RAW_FULL_ANSWER") : null;

  // 第 2 节：Enter 是纠偏。
  press(type("不要改公共接口"));
  const first = await wait(() => $(".guidance-item"));
  const firstId = first && first.dataset.guidanceId;
  out.steps.firstId = firstId;
  out.steps.inputCleared = $("#composer").value === "";
  out.steps.steeredText = text($(".guidance-text", first));
  out.steps.firstMode = first && first.dataset.mode;
  await sleep(100);
  // 收到 pending：能撤回、能修改。
  if (firstId) await push("guidance_updated", { event: "guidance_updated", guidance_id: firstId, state: "pending", mode: "next_step" });
  out.steps.pendingHasButtons = !!(await wait(() => item(firstId) && $(".guidance-retract", item(firstId)) && $(".guidance-edit", item(firstId))));

  // 修改：弹出编辑框（复用输入框），Enter 提交。
  if (out.steps.pendingHasButtons) {
    $(".guidance-edit", item(firstId)).click();
    const editor = await wait(() => $(".guidance-editor textarea", item(firstId)));
    if (editor) {
      editor.value = "改过的纠偏";
      editor.dispatchEvent(new Event("input", { bubbles: true }));
      editor.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    }
    await sleep(100);
    // 撤回。
    const retract = await wait(() => $(".guidance-retract", item(firstId)));
    if (retract) retract.click();
    await sleep(100);
    await push("guidance_updated", { event: "guidance_updated", guidance_id: firstId, state: "retracted", mode: "next_step" });
    out.steps.retractedGone = !!(await wait(() => !item(firstId)));
  }

  // 再发一条，这次生效：不能撤回、不能修改。
  press(type("第二条纠偏"));
  const secondEl = await wait(() => $(".guidance-item"));
  const secondId = secondEl && secondEl.dataset.guidanceId;
  if (secondId) {
    await push("guidance_updated", { event: "guidance_updated", guidance_id: secondId, state: "pending", mode: "next_step" });
    await push("guidance_updated", { event: "guidance_updated", guidance_id: secondId, state: "applied", mode: "next_step", round_seq: 2 });
    await wait(() => item(secondId) && item(secondId).classList.contains("applied"));
    out.steps.appliedHasNoButtons = !!item(secondId) && item(secondId).classList.contains("applied") && !$(".guidance-retract", item(secondId)) && !$(".guidance-edit", item(secondId));
    out.steps.appliedText = text(item(secondId));
  }

  // Ctrl+Enter 立刻打断：不能撤回；要等不可逆的调用时说明。
  press(type("立刻停下"), { ctrlKey: true });
  const interruptEl = await wait(() => $('.guidance-item[data-mode="interrupt"]'));
  const interruptId = interruptEl && interruptEl.dataset.guidanceId;
  out.steps.interruptHasNoButtons = !!interruptEl && !$(".guidance-retract", interruptEl) && !$(".guidance-edit", interruptEl);
  if (interruptId) {
    await push("guidance_updated", { event: "guidance_updated", guidance_id: interruptId, state: "pending", mode: "interrupt", reason_code: "irreversible_in_flight" });
    out.steps.irreversibleNote = !!(await wait(() => item(interruptId) && text(item(interruptId)).includes("这一步不可撤销，完成后立刻生效")));
  }

  // 排队（下一个任务）在菜单里。
  type("这是下一个任务");
  const menuButton = $(".send-menu-toggle");
  out.steps.hasSendMenu = !!menuButton;
  if (menuButton) {
    menuButton.click();
    const queue = await wait(() => $('.send-menu [data-action="queue"]'));
    out.steps.menuItems = $$(".send-menu [data-action]").map((e) => e.dataset.action);
    if (queue) queue.click();
    await sleep(100);
  }

  // 排队中的消息可以修改。
  const queued = await wait(() => $('.queued-item[data-task-id="t2"]'));
  out.steps.queuedShown = !!queued;
  if (queued) {
    const edit = $(".queued-edit", queued);
    out.steps.queuedHasEdit = !!edit;
    if (edit) {
      edit.click();
      const editor = await wait(() => $('.queued-item[data-task-id="t2"] .queued-editor textarea'));
      if (editor) {
        editor.value = "改过的下一个任务";
        editor.dispatchEvent(new Event("input", { bubbles: true }));
        editor.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      }
    }
  }

  // Task 结束：还没生效的提示可以作为新任务发送。
  press(type("来不及生效的纠偏"));
  const lateEl = await wait(() => $$(".guidance-item").find((e) => text(e).includes("来不及生效的纠偏")));
  const lateId = lateEl && lateEl.dataset.guidanceId;
  if (lateId) await push("guidance_updated", { event: "guidance_updated", guidance_id: lateId, state: "pending", mode: "next_step" });
  await push("task_finished", { status: "TASK_STATUS_DONE", deliverable: { value: { answer: "完成" } } });
  const asTask = await wait(() => lateId && item(lateId) && $(".guidance-send-as-task", item(lateId)));
  out.steps.lateOffersNewTask = !!asTask;
  if (asTask) { asTask.click(); await sleep(100); }
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

func runInteractionProbe(t *testing.T, browser string) (*interactionSite, interactionResult) {
	t.Helper()
	site := &interactionSite{t: t, pushes: make(chan string, 64), results: make(chan interactionResult, 1)}
	srv := httptest.NewServer(site.handler())
	t.Cleanup(srv.Close)
	dir, err := os.MkdirTemp("", "coding-web-browser")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for range 50 {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser, "--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run", "--no-default-browser-check", "--window-size=1600,1000",
		"--user-data-dir="+dir, srv.URL+"/#/w/p1/s/s1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("拉不起浏览器 %s：%v", browser, err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()
	select {
	case res := <-site.results:
		return site, res
	case <-ctx.Done():
		t.Fatalf("等不到页面的结果（%s）", browser)
		return nil, interactionResult{}
	}
}

func checkOutputPanel(t *testing.T, steps map[string]any) {
	t.Helper()
	rounds, _ := steps["outputRounds"].([]any)
	if len(rounds) < 2 {
		t.Errorf("右栏“输出”要按 Round 汇总：%v", steps["outputRounds"])
		return
	}
	first, _ := rounds[0].(map[string]any)
	if first["task"] != "t0" {
		t.Errorf("按 Task、Round 分组，先早后晚：%v", rounds)
	}
	var calc map[string]any
	for _, r := range rounds {
		m, _ := r.(map[string]any)
		if m["task"] == "t1" && fmt.Sprint(m["round"]) == "1" {
			calc = m
		}
		if m["goto"] != true {
			t.Errorf("答案旁要有“在对话里看”：%v", m)
		}
	}
	if calc == nil {
		t.Errorf("t1 的第 1 轮不见了：%v", rounds)
		return
	}
	files, _ := calc["files"].([]any)
	if len(files) != 2 {
		t.Errorf("重要文件：%v", calc)
		return
	}
	if f, _ := files[0].(map[string]any); f["path"] != "src/calc.py" || f["why"] != "取整的错误在这里" {
		t.Errorf("文件与为什么重要：%v", files[0])
	}
	if answers, _ := calc["answers"].([]any); len(answers) != 1 || answers[0] != "失败是因为取整用了 int()。" {
		t.Errorf("重要答案：%v", calc["answers"])
	}
}

func TestInteractionInBrowser(t *testing.T) {
	browser := findBrowser()
	if browser == "" {
		t.Skip("找不到浏览器（CODING_TEST_BROWSER 可以指定）")
	}
	site, res := runInteractionProbe(t, browser)
	if len(res.Errors) != 0 {
		t.Errorf("页面报错：%v", res.Errors)
	}
	steps := res.Steps
	truthy := func(name, what string) {
		t.Helper()
		if v, _ := steps[name].(bool); !v {
			t.Errorf("%s（%s = %v）", what, name, steps[name])
		}
	}
	falsy := func(name, what string) {
		t.Helper()
		if v, ok := steps[name].(bool); !ok || v {
			t.Errorf("%s（%s = %v）", what, name, steps[name])
		}
	}

	// --- 第 3 节 ---
	truthy("usageShown", "右栏“上下文”要显示占比")
	want := map[string]string{
		"system_prompt": "System prompt|2.0k|1.0%",
		"system_tools":  "System tools|4.0k|2.0%",
		"mcp_tools":     "MCP tools|1.0k|0.5%",
		"memory":        "Memory|500|0.3%",
		"task_brief":    "Task brief|1.5k|0.8%",
		"messages":      "Messages|11.0k|5.5%",
		"free":          "Free space|180.0k|90.0%",
	}
	for cat, line := range want {
		if res.Context[cat] != line {
			t.Errorf("上下文的 %s 应是 %q，实际 %q", cat, line, res.Context[cat])
		}
	}
	if segs, _ := steps["segments"].([]any); len(segs) != 7 {
		t.Errorf("分段的进度条每类一段（含 Free space）：%v", steps["segments"])
	}
	if line, _ := steps["compactionLine"].(string); !strings.Contains(line, "32.0k") {
		t.Errorf("要标出压缩线：%q", steps["compactionLine"])
	}
	truthy("toolsCollapsed", "工具默认只看合计")
	if tools, _ := steps["tools"].([]any); len(tools) != 3 {
		t.Errorf("展开能看到每个工具占多少：%v", steps["tools"])
	}
	truthy("detailsCollapsed", "原来的内容收进折叠的“任务详情”")
	truthy("detailsHasGoal", "“任务详情”里有目标、事实、Round")
	falsy("goalOutsideDetails", "上下文这一栏不再直接显示内容")

	// --- 第 4 节 ---
	checkOutputPanel(t, steps)
	falsy("outputHasFullAnswer", "不显示全文")

	// --- 第 2 节 ---
	posts := site.postsTo("/guidance")
	if len(posts) < 1 || posts[0].Body["text"] != "不要改公共接口" || posts[0].Body["mode"] != "next_step" {
		t.Errorf("Enter 是纠偏：%v", posts)
	}
	if !strings.Contains(fmt.Sprint(site.posts), "/tasks/t1/guidance") {
		t.Errorf("纠偏发给正在跑的 Task：%v", site.posts)
	}
	truthy("inputCleared", "发出去之后清空输入框")
	if steps["steeredText"] != "不要改公共接口" {
		t.Errorf("对话里看得到这条纠偏：%v", steps["steeredText"])
	}
	truthy("pendingHasButtons", "还没生效的能撤回、能修改")
	if ed := site.postsTo("/edit"); len(ed) != 1 || ed[0].Body["text"] != "改过的纠偏" {
		t.Errorf("修改：%v", ed)
	}
	if re := site.postsTo("/retract"); len(re) != 1 {
		t.Errorf("撤回：%v", re)
	}
	truthy("retractedGone", "撤回的不再显示")
	truthy("appliedHasNoButtons", "已生效的不能撤回、不能修改")
	if v, _ := steps["appliedText"].(string); !strings.Contains(v, "已生效") {
		t.Errorf("要写明已生效：%q", steps["appliedText"])
	}
	if !slices.ContainsFunc(posts, func(p recorded) bool { return p.Body["text"] == "立刻停下" && p.Body["mode"] == "interrupt" }) {
		t.Errorf("Ctrl+Enter 立刻打断：%v", posts)
	}
	truthy("interruptHasNoButtons", "立刻打断发出去就生效了，不能撤回")
	truthy("irreversibleNote", "要等不可逆的调用时要说明")
	truthy("hasSendMenu", "排队放进菜单")
	if items, _ := steps["menuItems"].([]any); !slices.Contains(items, any("queue")) {
		t.Errorf("菜单里有“下一个任务”：%v", steps["menuItems"])
	}
	if subs := site.postsTo("/tasks"); len(subs) != 1 || subs[0].Body["message"] != "这是下一个任务" {
		t.Errorf("菜单里选排队才提交新任务：%v", subs)
	}
	truthy("queuedShown", "排队中的 Task 看得到")
	truthy("queuedHasEdit", "排队中的 Task 能修改")
	if m := site.postsTo("/tasks/t2/message"); len(m) != 1 || m[0].Body["message"] != "改过的下一个任务" {
		t.Errorf("修改排队的消息：%v", m)
	}
	truthy("lateOffersNewTask", "Task 结束时还没生效的，提示可以作为新任务发送")
}
