package web

// 打开一个会话：先加载最后的对话（2026-11-02 用户反馈“打开 session 首先应该加载的是最后的对话记录而不是最开头的”）。
// 已结束的 Task 从最新的往前一个一个重放；页面停在底部，最后一轮的回答看得见，更早的在上面补上时也不把视图拽走。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const historyProbe = `
const out = { errors: [], steps: {} };
addEventListener("error", (e) => out.errors.push(String(e.message)));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const wait = async (fn, ms = 8000) => { const end = Date.now() + ms; for (;;) { const v = fn(); if (v) return v; if (Date.now() > end) return null; await sleep(30); } };
// Observe insertion order rather than sampling the first DOM match: a busy
// browser can render two correctly ordered responses between 30ms polls.
let firstAnswerTurnID = null;
const firstAnswerObserver = new MutationObserver(records => {
  for (const record of records) for (const node of record.addedNodes) {
    if (!(node instanceof Element)) continue;
    const answer = node.matches(".answer") ? node : node.querySelector(".answer");
    const turn = answer?.closest(".turn");
    if (turn && firstAnswerTurnID === null) firstAnswerTurnID = turn.id;
  }
});
firstAnswerObserver.observe(document.documentElement,{subtree:true,childList:true});
try {
  // 最新的一轮先出回答。
  const first = await wait(() => firstAnswerTurnID);
  firstAnswerObserver.disconnect();
  const turns = [...document.querySelectorAll(".turn")];
  out.steps.firstAnswerTurn = first ? turns.findIndex((t) => t.id === first) : null;
  out.steps.turns = turns.length;
  await wait(() => document.querySelectorAll(".answer").length >= 6);
  await sleep(300);
  const thread = document.getElementById("thread");
  out.steps.scrollable = thread.scrollHeight > thread.clientHeight + 200;
  out.steps.atBottom = thread.scrollHeight - thread.scrollTop - thread.clientHeight < 4;
  const answers = [...document.querySelectorAll(".answer")];
  const last = answers[answers.length - 1].getBoundingClientRect(), view = thread.getBoundingClientRect();
  out.steps.lastVisible = last.bottom > view.top && last.top < view.bottom;
  out.steps.lastText = answers[answers.length - 1].textContent.slice(0, 20);
} catch (ex) { out.errors.push(String(ex && ex.stack || ex)); }
await fetch("/__result", { method: "POST", body: JSON.stringify(out) });
`

func TestASessionOpensAtItsLatestTurn(t *testing.T) {
	browser := findBrowser()
	if browser == "" {
		t.Skip("找不到浏览器（CODING_TEST_BROWSER 可以指定）")
	}
	const base = "/api/projects/p1/sessions/s1"
	var mu sync.Mutex
	var order []string
	results := make(chan interactionResult, 1)
	tasks := []params{}
	for i := 6; i >= 1; i-- { // 服务端新的在前
		tasks = append(tasks, params{"id": fmt.Sprintf("t%d", i), "status": "TASK_STATUS_DONE", "message": fmt.Sprintf("第 %d 个问题", i), "created_at": fmt.Sprintf("2026-10-30T08:0%d:00Z", i)})
	}
	api := map[string]any{
		"/api/config": params{}, "/api/me": params{"tenant": "t1", "user": "u1", "connections": []any{}}, "/api/projects": []params{{"id": "p1", "name": "demo"}},
		"/api/samples": []any{}, "/api/sessions": []params{{"id": "s1", "project_id": "p1", "title": "demo"}}, "/api/tasks/active": []any{},
		base + "/tasks": tasks, base + "/env": params{}, base + "/changes": params{"files": []any{}},
	}
	app := Handler()
	mux := http.NewServeMux()
	mux.Handle("/assets/", app)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, page := getFrom(t, app, "/")
		html := strings.Replace(string(page), `<script type="module"`, `<script type="module" src="/__probe.js"></script><script type="module"`, 1)
		w.Header().Set("Set-Cookie", "cw_lang=zh; path=/")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("GET /__probe.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(historyProbe))
	})
	mux.HandleFunc("POST /__result", func(w http.ResponseWriter, r *http.Request) {
		var res interactionResult
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
	mux.HandleFunc("GET "+base+"/tasks/{task}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(params{"task": params{"id": r.PathValue("task"), "status": "TASK_STATUS_DONE"}, "runtime": params{}})
	})
	mux.HandleFunc("GET "+base+"/tasks/{task}/events", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("task")
		mu.Lock()
		order = append(order, id)
		mu.Unlock()
		time.Sleep(40 * time.Millisecond) // 让顺序看得出来
		answer := fmt.Sprintf("%s 的回答。", id) + strings.Repeat("\n\n这一段把回答撑高，让对话能滚动。", 12)
		data, _ := json.Marshal(params{"event_id": 1, "emitted_at": "2026-10-30T08:00:00Z", "payload": params{"value": params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": answer}}}}})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "id: 1\nevent: task_finished\ndata: %s\n\n", data)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir, err := os.MkdirTemp("", "coding-web-history")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, stop := openPage(t, browser, dir, srv.URL+"/#/w/p1/s/s1")
	defer stop()
	var res interactionResult
	select {
	case res = <-results:
	case <-ctx.Done():
		t.Fatal("等不到页面的结果")
	}
	if len(res.Errors) != 0 {
		t.Errorf("页面报错：%v", res.Errors)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 6 || order[0] != "t6" || order[1] != "t5" || order[5] != "t1" {
		t.Errorf("已结束的 Task 要从最新的往前重放：%v", order)
	}
	s := res.Steps
	if s["firstAnswerTurn"] != float64(5) {
		t.Errorf("先出回答的是最后一轮（第 6 个，下标 5）：%v（共 %v 轮）", s["firstAnswerTurn"], s["turns"])
	}
	for _, k := range []string{"scrollable", "atBottom", "lastVisible"} {
		if v, _ := s[k].(bool); !v {
			t.Errorf("打开会话停在最后的对话：%s = %v", k, s[k])
		}
	}
	if txt, _ := s["lastText"].(string); !strings.HasPrefix(txt, "t6 的回答") {
		t.Errorf("最下面是最新一轮的回答：%q", s["lastText"])
	}
}
