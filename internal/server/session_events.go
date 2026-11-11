package server

// 批量取会话里几个任务的事件（HIST-01 的网页一侧，验收测试在 session_events_test.go）：取不到快照时页面先用它画历史，
// 只对没结束的任务开事件流续传，不再逐个任务从头回放。
//
//   - POST <会话>/events {cursors: [{task_id, after_event_id}], page_size}：带用户的令牌转调 Task API 的 ListSessionEvents
//     （Runtime 的 session_id）；回来的 JSON 换成 proto 字段名（event_id、payload.value），与快照和事件流的 data 一个样子。
//   - 会话还没交给 Runtime 时回空的 tasks，不去问。上游 404 或 Unimplemented（Runtime 还没有这个方法）回 404，页面退回
//     逐个回放；别的失败是 502，不带上游的正文。

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	taskapiv1 "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/taskapi/v1"
)

// maxEventCursors 同 Runtime 的 ListSessionEvents：一次最多取这么多个任务。
const maxEventCursors = 50

func (s *Server) handleSessionEvents(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	var body struct {
		Cursors []struct {
			TaskID string `json:"task_id"`
			After  int64  `json:"after_event_id"`
		} `json:"cursors"`
		PageSize int32 `json:"page_size"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)).Decode(&body); err != nil {
		writeMsg(c, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	if len(body.Cursors) > maxEventCursors {
		writeMsg(c, http.StatusBadRequest, "一次最多取 %d 个任务的事件", maxEventCursors)
		return
	}
	if sess.RuntimeSessionID == "" {
		writeJSON(c, http.StatusOK, map[string]any{"tasks": []any{}})
		return
	}
	cursors := make([]map[string]any, 0, len(body.Cursors))
	for _, cursor := range body.Cursors {
		cursors = append(cursors, map[string]any{"taskId": cursor.TaskID, "afterEventId": cursor.After})
	}
	req := map[string]any{"sessionId": sess.RuntimeSessionID, "cursors": cursors, "pageSize": body.PageSize}
	ctx, cancel := ctxTimeout(c.Request, 30*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := s.api.Call(ctx, id.token, "ListSessionEvents", req, &raw); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Code == "not_found" || apiErr.Code == "unimplemented") {
			writeMsg(c, http.StatusNotFound, "会话不存在")
			return
		}
		writeMsg(c, http.StatusBadGateway, "暂时取不到会话的事件")
		return
	}
	resp := &taskapiv1.ListSessionEventsResponse{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, resp); err != nil {
		writeMsg(c, http.StatusBadGateway, "暂时取不到会话的事件")
		return
	}
	out, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(resp)
	if err != nil {
		writeMsg(c, http.StatusBadGateway, "暂时取不到会话的事件")
		return
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(out)
}
