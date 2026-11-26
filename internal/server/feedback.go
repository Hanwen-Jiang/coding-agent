package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/telemetry"
)

const maxFeedbackNoteRunes = 500

func (s *Server) handleTaskFeedback(c *gin.Context, id identity) {
	sess, t, ok := s.task(c, id)
	if !ok {
		return
	}
	var req struct {
		Vote string `json:"vote"`
		Note string `json:"note"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeMsg(c, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	vote := strings.TrimSpace(req.Vote)
	if vote != "up" && vote != "down" && vote != "" {
		writeMsg(c, http.StatusBadRequest, "vote 必须是 up、down 或空字符串")
		return
	}
	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > maxFeedbackNoteRunes {
		writeMsg(c, http.StatusBadRequest, "feedback note 过长")
		return
	}

	if err := s.updateTask(id, sess.WorkspaceID, t.ID, func(rec *TaskRecord) {
		rec.Feedback = vote
	}); err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}

	metricVote := vote
	if metricVote == "" {
		metricVote = "clear"
	}
	s.metrics().Count("coding.answer_feedback.total", 1, map[string]string{"vote": metricVote})

	if s.cfg.Telemetry != nil && vote != "" {
		attrs := []telemetry.Attribute{
			{Key: "skill.name", Value: "coding_agent.chat"},
			{Key: "task.id", Value: t.ID},
			{Key: "session.id", Value: sess.ID},
			{Key: "feedback.vote", Value: vote},
		}
		if note != "" {
			attrs = append(attrs, telemetry.Attribute{Key: "feedback.note", Value: note})
		}
		if vote == "down" {
			attrs = append(attrs, telemetry.Attribute{Key: "failure.reason", Value: "user_thumbs_down"})
		}
		_, span, err := s.cfg.Telemetry.StartSpan(c.Request.Context(), "coding_agent.answer_feedback", attrs...)
		if err == nil && span != nil {
			if vote == "down" {
				span.RecordError(errors.New("user_thumbs_down"))
			}
			span.End()
		}
	}

	writeJSON(c, http.StatusOK, map[string]any{"ok": true, "vote": vote})
}
