package server

// 会话快照（Runtime 的 N-19；WEB-SNAP，验收测试在 snapshot_test.go）：页面打开会话时先取快照画首屏，再对没结束的任务从快照的游标续传。
//
//   - GET <会话>/snapshot：带用户的令牌转调 Task API 的 GET /v1/sessions/{Runtime 的 session_id}/snapshot，原样带回 JSON；
//     查询参数只转 tasks 与 events（Runtime 自己再截到上限）。会话还没交给 Runtime 时回空的快照，不去问。
//   - 上游 404（Runtime 还没有这个端点，或会话没了）原样 404，页面据此退回逐个任务回放；其余失败是 502，不带上游的正文。
//   - 游标过期（事件流上游回 409 resnapshot_required）在 tasks.go 的 handleEvents 里换成一条 resnapshot_required 事件。

import (
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/url"
	"time"
)

func (s *Server) handleSnapshot(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	if sess.RuntimeSessionID == "" {
		writeJSON(c, http.StatusOK, map[string]any{"session_id": "", "tasks": []any{}, "errors": []any{}})
		return
	}
	query := url.Values{}
	for _, k := range []string{"tasks", "events"} {
		if v := c.Query(k); v != "" && allDigits(v) {
			query.Set(k, v)
		}
	}
	ctx, cancel := ctxTimeout(c.Request, 30*time.Second)
	defer cancel()
	res, err := s.api.Snapshot(ctx, id.token, sess.RuntimeSessionID, query)
	if err != nil {
		writeMsg(c, http.StatusBadGateway, "连不上 task-service")
		return
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		c.Header("Content-Type", "application/json")
		c.Status(http.StatusOK)
		_, _ = io.Copy(c.Writer, io.LimitReader(res.Body, 32<<20))
	case http.StatusNotFound:
		writeMsg(c, http.StatusNotFound, "会话不存在")
	default:
		writeMsg(c, http.StatusBadGateway, "暂时取不到会话快照")
	}
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0 && len(s) <= 4
}
