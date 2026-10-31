package server

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

// 运行中的纠偏、立刻打断、撤回与修改，以及修改排队中的消息（Runtime 的 docs/plan/running-task-interaction.md 第 2 节）。
// 权限与生效的先后都由 Runtime 定（谁的信号先到谁算，结果靠事件 guidance_updated、task_message_edited）；这里只转发，并把 Runtime
// 拒绝的原因翻成人话。文字不进日志。

var guidanceModes = map[string]string{"next_step": "GUIDANCE_MODE_NEXT_STEP", "interrupt": "GUIDANCE_MODE_INTERRUPT"}

// signalTask 发一个 SignalTask。Task 已经结束或还没开始是 409：界面据此提示可以作为新任务发送，或者直接修改排队的消息。
func (s *Server) signalTask(w http.ResponseWriter, r *http.Request, id identity, signal map[string]any) bool {
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	err := s.api.Call(ctx, id.token, "SignalTask", signal, nil)
	if err == nil {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Reason == "task_finished" || apiErr.Reason == "task_not_started") {
		writeErr(w, r, http.StatusConflict, runtimeMsg(apiErr.Reason))
		return false
	}
	writeAPIError(w, r, err)
	return false
}

func (s *Server) handleGuidance(w http.ResponseWriter, r *http.Request, id identity) {
	_, t, ok := s.task(w, r, id)
	if !ok {
		return
	}
	var req struct {
		GuidanceID string `json:"guidance_id"`
		Text       string `json:"text"`
		Mode       string `json:"mode"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	mode, known := guidanceModes[req.Mode]
	if !known || req.GuidanceID == "" || strings.TrimSpace(req.Text) == "" {
		writeMsg(w, r, http.StatusBadRequest, "要给出 guidance_id、text，mode 是 next_step 或 interrupt")
		return
	}
	body := map[string]string{"guidanceId": req.GuidanceID, "text": req.Text, "mode": mode}
	if s.signalTask(w, r, id, map[string]any{"taskId": t.ID, "submitGuidance": body}) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) handleGuidanceEdit(w http.ResponseWriter, r *http.Request, id identity) {
	_, t, ok := s.task(w, r, id)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeMsg(w, r, http.StatusBadRequest, "要给出 text")
		return
	}
	body := map[string]string{"guidanceId": r.PathValue("guidance"), "text": req.Text}
	if s.signalTask(w, r, id, map[string]any{"taskId": t.ID, "editGuidance": body}) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) handleGuidanceRetract(w http.ResponseWriter, r *http.Request, id identity) {
	_, t, ok := s.task(w, r, id)
	if !ok {
		return
	}
	body := map[string]string{"guidanceId": r.PathValue("guidance")}
	if s.signalTask(w, r, id, map[string]any{"taskId": t.ID, "retractGuidance": body}) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleEditQueued 修改排队中的消息。改不改由 Runtime 的 Session 工作流定（Task 可能刚好开始了），所以这里只记下 PendingMessage，
// 等界面收到 task_message_edited 再经 handleQueuedResult 定下来。
func (s *Server) handleEditQueued(w http.ResponseWriter, r *http.Request, id identity) {
	sess, t, ok := s.task(w, r, id)
	if !ok {
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		writeMsg(w, r, http.StatusBadRequest, "消息不能是空的")
		return
	}
	if !s.signalTask(w, r, id, map[string]any{"taskId": t.ID, "editQueuedMessage": map[string]string{"message": message}}) {
		return
	}
	if err := s.updateTask(id, sess.WorkspaceID, t.ID, func(rec *TaskRecord) { rec.PendingMessage = message }); err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleQueuedResult 界面收到 task_message_edited 之后告诉这里结果：改好了就把这里记的消息换掉（对话里显示它）。
func (s *Server) handleQueuedResult(w http.ResponseWriter, r *http.Request, id identity) {
	sess, t, ok := s.task(w, r, id)
	if !ok {
		return
	}
	var req struct {
		Applied bool `json:"applied"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	err := s.updateTask(id, sess.WorkspaceID, t.ID, func(rec *TaskRecord) {
		if req.Applied && rec.PendingMessage != "" {
			rec.Message = rec.PendingMessage
		}
		rec.PendingMessage = ""
	})
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) updateTask(id identity, workspaceID, taskID string, change func(*TaskRecord)) error {
	return s.store.UpdateTasks(id.TenantID, workspaceID, func(tasks []TaskRecord) []TaskRecord {
		if i := slices.IndexFunc(tasks, func(t TaskRecord) bool { return t.ID == taskID }); i >= 0 {
			change(&tasks[i])
		}
		return tasks
	})
}
