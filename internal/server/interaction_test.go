package server

// Runtime 的 docs/plan/running-task-interaction.md 第 2 节验收（coding-web 的服务端）：网页发来的纠偏、撤回、修改转给 Task API。
//
// 权限与生效的先后都由 Runtime 定（谁的信号先到谁算，结果靠事件 guidance_updated）；这里只转发，并把 Runtime 拒绝的原因翻成人话。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// containsText 看响应里有没有这段文字（JSON 可能把中文转义）。
func containsText(raw, want string) bool {
	var v any
	if json.Unmarshal([]byte(raw), &v) == nil {
		data, _ := json.Marshal(v)
		raw = string(data)
	}
	return strings.Contains(raw, want)
}

func signed(t *testing.T) (*client, *fakeTaskAPI, string, string) {
	t.Helper()
	ts, fake, cfg := newServer(t)
	c := newClient(t, ts.URL)
	if code, _, raw := c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"}); code != http.StatusOK {
		t.Fatalf("登录失败：%d %s", code, raw)
	}
	pid := sampleProject(t, c)
	api, _ := newSession(t, c, cfg, "acme", pid)
	code, task, raw := c.do("POST", api+"/tasks", map[string]string{"message": "修一下测试"})
	if code != http.StatusCreated {
		t.Fatalf("提交任务失败：%d %s", code, raw)
	}
	return c, fake, api, task["id"].(string)
}

func lastSignal(t *testing.T, fake *fakeTaskAPI) map[string]any {
	t.Helper()
	if len(fake.signals) == 0 {
		t.Fatal("没有发 SignalTask")
	}
	return fake.signals[len(fake.signals)-1]
}

func TestGuidanceIsForwardedWithItsMode(t *testing.T) {
	for mode, wire := range map[string]string{"next_step": "GUIDANCE_MODE_NEXT_STEP", "interrupt": "GUIDANCE_MODE_INTERRUPT"} {
		t.Run(mode, func(t *testing.T) {
			c, fake, api, id := signed(t)
			code, _, raw := c.do("POST", api+"/tasks/"+id+"/guidance", map[string]string{"guidance_id": "g1", "text": "不要改公共接口", "mode": mode})
			if code != http.StatusOK {
				t.Fatalf("发纠偏失败：%d %s", code, raw)
			}
			sig := lastSignal(t, fake)
			if sig["taskId"] != id {
				t.Errorf("要发给这个 Task：%v", sig)
			}
			body, _ := sig["submitGuidance"].(map[string]any)
			if body["guidanceId"] != "g1" || body["text"] != "不要改公共接口" || body["mode"] != wire {
				t.Errorf("载荷不对：%v", sig)
			}
		})
	}
}

func TestGuidanceMustSayWhenItTakesEffect(t *testing.T) {
	c, fake, api, id := signed(t)
	for _, body := range []map[string]string{
		{"guidance_id": "g1", "text": "x"},
		{"guidance_id": "g1", "text": "x", "mode": "queue"},
		{"guidance_id": "g1", "text": "", "mode": "next_step"},
		{"guidance_id": "", "text": "x", "mode": "next_step"},
	} {
		if code, _, _ := c.do("POST", api+"/tasks/"+id+"/guidance", body); code != http.StatusBadRequest {
			t.Errorf("%v 应 400，实际 %d", body, code)
		}
	}
	if len(fake.signals) != 0 {
		t.Errorf("不合法的不转发：%v", fake.signals)
	}
}

func TestEditAndRetractAreForwarded(t *testing.T) {
	c, fake, api, id := signed(t)
	if code, _, raw := c.do("POST", api+"/tasks/"+id+"/guidance/g1/edit", map[string]string{"text": "改过的"}); code != http.StatusOK {
		t.Fatalf("修改失败：%d %s", code, raw)
	}
	body, _ := lastSignal(t, fake)["editGuidance"].(map[string]any)
	if body["guidanceId"] != "g1" || body["text"] != "改过的" {
		t.Errorf("修改的载荷不对：%v", fake.signals)
	}
	if code, _, raw := c.do("POST", api+"/tasks/"+id+"/guidance/g1/retract", nil); code != http.StatusOK {
		t.Fatalf("撤回失败：%d %s", code, raw)
	}
	body, _ = lastSignal(t, fake)["retractGuidance"].(map[string]any)
	if body["guidanceId"] != "g1" {
		t.Errorf("撤回的载荷不对：%v", fake.signals)
	}
}

func TestEditingAQueuedMessageIsForwarded(t *testing.T) {
	c, fake, api, id := signed(t)
	if code, _, raw := c.do("POST", api+"/tasks/"+id+"/message", map[string]string{"message": "改过的消息"}); code != http.StatusOK {
		t.Fatalf("修改排队的消息失败：%d %s", code, raw)
	}
	body, _ := lastSignal(t, fake)["editQueuedMessage"].(map[string]any)
	if body["message"] != "改过的消息" {
		t.Errorf("载荷不对：%v", fake.signals)
	}
	if code, _, _ := c.do("POST", api+"/tasks/"+id+"/message", map[string]string{"message": " "}); code != http.StatusBadRequest {
		t.Errorf("空消息应 400，实际 %d", code)
	}
}

func TestAnotherSessionsTaskIsNotFound(t *testing.T) {
	c, fake, api, id := signed(t)
	other := api[:len(api)-1] + "x"
	if code, _, _ := c.do("POST", other+"/tasks/"+id+"/guidance", map[string]string{"guidance_id": "g1", "text": "x", "mode": "next_step"}); code != http.StatusNotFound {
		t.Errorf("别的会话应 404，实际 %d", code)
	}
	if len(fake.signals) != 0 {
		t.Errorf("不转发：%v", fake.signals)
	}
}

func TestRuntimeRefusalsAreTranslated(t *testing.T) {
	for reason, want := range map[string]string{
		"task_finished":    "任务已经结束，可以作为新任务发送",
		"task_not_started": "任务还没开始，可以直接修改这条消息",
	} {
		t.Run(reason, func(t *testing.T) {
			c, fake, api, id := signed(t)
			fake.signalError = reason
			code, _, raw := c.do("POST", api+"/tasks/"+id+"/guidance", map[string]string{"guidance_id": "g1", "text": "x", "mode": "next_step"})
			if code != http.StatusConflict {
				t.Errorf("应 409，实际 %d：%s", code, raw)
			}
			if !containsText(raw, want) {
				t.Errorf("要说人话（%s）：%s", want, raw)
			}
		})
	}
}

func TestAQueuedEditOnlySticksAfterRuntimeSaysSo(t *testing.T) {
	c, _, api, id := signed(t)
	message := func() string {
		_, task, raw := c.do("GET", api+"/tasks/"+id, nil)
		rec, ok := task["task"].(map[string]any)
		if !ok {
			t.Fatalf("查不到任务：%s", raw)
		}
		return rec["message"].(string)
	}
	if code, _, raw := c.do("POST", api+"/tasks/"+id+"/message", map[string]string{"message": "第一次改"}); code != http.StatusOK {
		t.Fatalf("修改失败：%d %s", code, raw)
	}
	if got := message(); got != "修一下测试" {
		t.Errorf("Runtime 确认之前不改：%q", got)
	}
	if code, _, raw := c.do("POST", api+"/tasks/"+id+"/message/result", map[string]bool{"applied": false}); code != http.StatusOK {
		t.Fatalf("记结果失败：%d %s", code, raw)
	}
	if got := message(); got != "修一下测试" {
		t.Errorf("Runtime 拒绝了就不改：%q", got)
	}
	if code, _, raw := c.do("POST", api+"/tasks/"+id+"/message", map[string]string{"message": "第二次改"}); code != http.StatusOK {
		t.Fatalf("修改失败：%d %s", code, raw)
	}
	if code, _, raw := c.do("POST", api+"/tasks/"+id+"/message/result", map[string]bool{"applied": true}); code != http.StatusOK {
		t.Fatalf("记结果失败：%d %s", code, raw)
	}
	if got := message(); got != "第二次改" {
		t.Errorf("Runtime 确认了就改：%q", got)
	}
}
