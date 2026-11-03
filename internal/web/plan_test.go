package web

// WEB-PLAN（Runtime 的 docs/plan/all-plans-dag-2026-11-03.md；N-04 的网页一侧，n04-task-plan-implementation-2026-11-03.md“没做的”）：
// 计划区。Runtime 在规划后、每轮结束与收尾时推 plan_updated（3～8 步，每步 id、title、status、round；current_step 是正在做的一步）。
//
//   - app.js 监听 plan_updated；每个任务只看最新的一份（丢旧重建，不按步骤合并）。
//   - 计划区 .plan 在这个任务的那一轮里，标题是 plan.title；每一步一个 .plan-step，data-status 是 pending、active、done、blocked，
//     current_step 那一步另加 current；文字是 title。没有 plan_updated 的任务（fast）不画计划区。
//   - 新的文字两种语言都有。
//
// 静态检查总是跑；真正执行 JS 的检查用浏览器（找不到时跳过）。实现之前（app.js 还没监听 plan_updated）这些测试跳过。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func requirePlanArea(t *testing.T) string {
	t.Helper()
	_, app := getFrom(t, Handler(), "/assets/app.js")
	if !strings.Contains(string(app), `"plan_updated"`) {
		t.Skip("计划区还没写：先审验收测试（WEB-PLAN）")
	}
	return string(app)
}

func TestAppListensForPlanUpdates(t *testing.T) {
	app := requirePlanArea(t)
	start := strings.Index(app, "const EVENT_TYPES = [")
	end := strings.Index(app[start:], "];")
	if start < 0 || end < 0 || !strings.Contains(app[start:start+end], `"plan_updated"`) {
		t.Fatal("EVENT_TYPES 里要有 plan_updated（EventSource 只收登记了的事件）")
	}
}

func TestThePlanTextIsInBothLanguages(t *testing.T) {
	requirePlanArea(t)
	zh, en := dictionaries(t)
	for _, key := range []string{"plan.title", "plan.status.pending", "plan.status.active", "plan.status.done", "plan.status.blocked"} {
		if _, ok := zh[key]; !ok {
			t.Errorf("中文字典缺 %s", key)
		}
		if _, ok := en[key]; !ok {
			t.Errorf("英文字典缺 %s", key)
		}
	}
}

// planSite 是真正的页面、注入的探针、假的 API，以及可以随时往 t1 的事件流里推事件的通道。
type planSite struct {
	t       *testing.T
	pushes  chan string
	seq     int
	results chan planResult
}

type planResult struct {
	Errors []string       `json:"errors"`
	Steps  map[string]any `json:"steps"`
}

func (s *planSite) frame(name string, value map[string]any) string {
	s.seq++
	data, _ := json.Marshal(params{"event_id": s.seq, "emitted_at": "2026-11-03T08:00:00Z", "payload": params{"value": value}})
	return fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", s.seq, name, data)
}

func planUpdated(round int, current string, steps ...params) params {
	out := params{"event": "plan_updated", "round_seq": round, "steps": steps}
	if current != "" {
		out["current_step"] = current
	}
	return out
}

func (s *planSite) handler() http.Handler {
	const base = "/api/projects/p1/sessions/s1"
	running := params{"id": "t1", "status": "TASK_STATUS_RUNNING", "message": "修一下测试", "created_at": "2026-11-03T08:00:00Z"}
	fast := params{"id": "t0", "status": "TASK_STATUS_DONE", "message": "你好", "created_at": "2026-11-03T07:00:00Z"}
	api := map[string]any{
		"/api/config":                params{},
		"/api/me":                    params{"tenant": "t1", "user": "u1", "connections": []any{}},
		"/api/projects":              []params{{"id": "p1", "name": "demo"}},
		"/api/samples":               []any{},
		"/api/sessions":              []params{{"id": "s1", "project_id": "p1", "title": "demo"}},
		"/api/tasks/active":          []any{},
		base + "/tasks":              []params{running, fast},
		base + "/tasks/t0":           params{"task": fast, "runtime": params{"result": params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "你好！"}}}}},
		base + "/tasks/t1":           params{"task": running, "runtime": params{}},
		base + "/tasks/t0/artifacts": params{"artifacts": []any{}},
		base + "/env":                params{},
		base + "/changes":            params{"files": []any{}},
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
		_, _ = w.Write([]byte(planProbe))
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
		var res planResult
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
	mux.HandleFunc("GET "+base+"/tasks/{task}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		switch r.PathValue("task") {
		case "t1":
			first := planUpdated(1, "locate",
				params{"id": "locate", "title": "找到出错的测试", "status": "active", "round": 1},
				params{"id": "fix", "title": "修改代码", "status": "pending", "round": 1},
				params{"id": "verify", "title": "跑测试确认", "status": "pending", "round": 1})
			for _, f := range []struct {
				name  string
				value map[string]any
			}{
				{"task_accepted", params{}},
				{"round_started", params{"round_seq": 1, "rerun": false}},
				{"plan_updated", first},
				{"stage_started", params{"round_seq": 1, "goal": "定位", "exit_when": "找到原因", "nodes": []any{}}},
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
		case "t0":
			_, _ = fmt.Fprint(w, s.frame("task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "你好！"}}}))
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
	})
	return mux
}

// planProbe 读计划区：每一步的文字、data-status、是否 current；推一份新的 plan_updated 再读一次。
const planProbe = `
const out = { errors: [], steps: {} };
addEventListener("error", (e) => out.errors.push(String(e.message)));
addEventListener("unhandledrejection", (e) => out.errors.push(String(e.reason && e.reason.stack || e.reason)));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms = 4000) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
const $$ = (sel, root = document) => (root ? [...root.querySelectorAll(sel)] : []);
const push = (name, value) => fetch("/__push", { method: "POST", body: JSON.stringify({ name, value }) });
const read = () => $$(".plan").map((p) => ({
  title: (p.querySelector(".plan-title") || {}).textContent || null,
  steps: $$(".plan-step", p).map((s) => ({ text: s.textContent, status: s.dataset.status, current: s.classList.contains("current") })),
}));
try {
  await wait(() => $$(".plan-step").length >= 3, 8000);
  out.steps.first = read();
  await push("plan_updated", { event: "plan_updated", round_seq: 2, current_step: "verify", steps: [
    { id: "locate", title: "找到出错的测试", status: "done", round: 1 },
    { id: "fix", title: "修改代码", status: "done", round: 2 },
    { id: "verify", title: "跑测试确认", status: "active", round: 2 },
    { id: "docs", title: "补说明", status: "blocked", round: 2 },
  ] });
  await wait(() => $$(".plan-step").length >= 4, 4000);
  out.steps.second = read();
} catch (ex) {
  out.errors.push(String(ex && ex.stack || ex));
}
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

type planStep struct {
	Text    string `json:"text"`
	Status  string `json:"status"`
	Current bool   `json:"current"`
}

func planOf(t *testing.T, raw any) (string, []planStep) {
	t.Helper()
	data, _ := json.Marshal(raw)
	var plans []struct {
		Title string     `json:"title"`
		Steps []planStep `json:"steps"`
	}
	if err := json.Unmarshal(data, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("应只有一个计划区（只给有计划的任务画，fast 的不画）：%s", data)
	}
	return plans[0].Title, plans[0].Steps
}

func TestThePlanAreaInBrowser(t *testing.T) {
	requirePlanArea(t)
	browser := findBrowser()
	if browser == "" {
		t.Skip("找不到浏览器（CODING_TEST_BROWSER 可以指定）")
	}
	site := &planSite{t: t, pushes: make(chan string, 16), results: make(chan planResult, 1)}
	srv := httptest.NewServer(site.handler())
	t.Cleanup(srv.Close)
	dir, err := os.MkdirTemp("", "coding-web-plan")
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
	ctx, stop := openPage(t, browser, dir, srv.URL+"/#/w/p1/s/s1")
	defer stop()
	var res planResult
	select {
	case res = <-site.results:
	case <-ctx.Done():
		t.Fatal("等不到页面的结果")
	}
	if len(res.Errors) != 0 {
		t.Fatalf("页面报错：%v", res.Errors)
	}

	title, steps := planOf(t, res.Steps["first"])
	if title != "计划" {
		t.Errorf("计划区的标题（plan.title）：%q", title)
	}
	want := []planStep{{"找到出错的测试", "active", true}, {"修改代码", "pending", false}, {"跑测试确认", "pending", false}}
	if len(steps) != len(want) {
		t.Fatalf("第一份计划：%v", steps)
	}
	for i, w := range want {
		if !strings.Contains(steps[i].Text, w.Text) || steps[i].Status != w.Status || steps[i].Current != w.Current {
			t.Errorf("第一份计划第 %d 步：%+v，应是 %+v", i, steps[i], w)
		}
	}

	_, steps = planOf(t, res.Steps["second"])
	want = []planStep{{"找到出错的测试", "done", false}, {"修改代码", "done", false}, {"跑测试确认", "active", true}, {"补说明", "blocked", false}}
	if len(steps) != len(want) {
		t.Fatalf("新的一份要整个替换旧的（丢旧重建）：%v", steps)
	}
	for i, w := range want {
		if !strings.Contains(steps[i].Text, w.Text) || steps[i].Status != w.Status || steps[i].Current != w.Current {
			t.Errorf("第二份计划第 %d 步：%+v，应是 %+v", i, steps[i], w)
		}
	}
}
