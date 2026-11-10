package server

// External delivery reads only public outcome events. In particular, token,
// thinking, planner and tool events can never become a comment or email body.
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	commonv1 "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/common/v1"
	taskapiv1 "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/taskapi/v1"
	workflowv1 "github.com/agentruntime/agent-runtime/proto/gen/go/agentruntime/workflow/v1"
	"github.com/agentruntime/coding-agent/internal/entry"
	"google.golang.org/protobuf/encoding/protojson"
)

// entryTaskOutcome reads a bounded tail at the QueryTask snapshot. The terminal
// event is last in that task stream; we never replay accumulated token deltas.
// There is deliberately no shared cursor: a failed outbox write must be able to
// reread the same complete answer, and an unanswered question stays observable.
func (s *Server) entryTaskOutcome(ctx context.Context, id identity, t entry.Trigger, q queryResponse) (summary string, question string, err error) {
	if t.Tenant != id.TenantID || t.User != id.UserID || t.TaskID == "" || q.TaskID != "" && q.TaskID != t.TaskID {
		return "", "", errors.New("entry outcome identity mismatch")
	}
	sessions, err := s.store.Sessions(t.Tenant)
	if err != nil {
		return "", "", err
	}
	runtimeID := ""
	for _, sess := range sessions {
		if sess.ID == t.SessionID && sess.ProjectID == t.ProjectID && sess.WorkspaceID == t.WorkspaceID {
			runtimeID = sess.RuntimeSessionID
			break
		}
	}
	if runtimeID == "" {
		return "", "", errors.New("entry runtime session unavailable")
	}
	if q.SessionID != "" && q.SessionID != runtimeID {
		return "", "", errors.New("entry outcome session mismatch")
	}
	last := int64(0)
	if q.LastEventID != "" {
		last, err = strconv.ParseInt(q.LastEventID, 10, 64)
		if err != nil || last < 0 {
			return "", "", errors.New("invalid entry event cursor")
		}
	}
	after := max(int64(0), last-1000)
	nodeAnswer := ""
	finished := false
	readLast := int64(0)
	for page := 0; page < 4; page++ {
		req := map[string]any{"sessionId": runtimeID, "cursors": []any{map[string]any{"taskId": t.TaskID, "afterEventId": strconv.FormatInt(after, 10)}}, "pageSize": 1000}
		var raw json.RawMessage
		if err = s.api.Call(ctx, id.token, "ListSessionEvents", req, &raw); err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Code == "unimplemented") {
				return entryTaskSummary(q), "", nil
			}
			return "", "", errors.New("entry outcome events unavailable")
		}
		resp := &taskapiv1.ListSessionEventsResponse{}
		if (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, resp) != nil {
			return "", "", errors.New("invalid entry outcome events")
		}
		if len(resp.GetTasks()) != 1 || resp.Tasks[0].GetTaskId() != t.TaskID || !resp.Tasks[0].GetFound() {
			return "", "", errors.New("entry outcome task unavailable")
		}
		batch := resp.Tasks[0]
		if batch.GetEventsUnavailable() {
			return "", "", errors.New("entry outcome event history unavailable")
		}
		events := batch.GetEvents()
		sort.SliceStable(events, func(i, j int) bool { return events[i].GetEventId() < events[j].GetEventId() })
		for _, event := range events {
			if event.GetTaskId() != t.TaskID || event.GetSessionId() != "" && event.GetSessionId() != runtimeID {
				return "", "", errors.New("entry outcome event identity mismatch")
			}
			eid := event.GetEventId()
			if eid <= after || eid <= readLast {
				return "", "", errors.New("invalid entry event ordering")
			}
			if last > 0 && eid > last {
				break
			}
			readLast = eid
			value := event.GetPayload().GetValue().GetStructValue().AsMap()
			switch event.GetType() {
			case taskapiv1.TaskEventType_TASK_EVENT_TYPE_TASK_FINISHED:
				summary, err = entryFinishedText(value, t.TaskID, q.Status)
				if err != nil {
					return "", "", err
				}
				question = ""
				finished = true
			case taskapiv1.TaskEventType_TASK_EVENT_TYPE_NODE_FINISHED:
				// Current Runtime emits only state here. A legacy explicit deliver result
				// remains usable; outputs from ordinary nodes are never public answers.
				if value["role"] == "deliver" && value["status"] == "succeeded" {
					if v, ok := value["result"]; ok {
						nodeAnswer = entryPublicAnswer(v)
					}
				}
				question = ""
			case taskapiv1.TaskEventType_TASK_EVENT_TYPE_HUMAN_INPUT_REQUIRED:
				if !finished {
					question = entryQuestionText(value)
				}
			case taskapiv1.TaskEventType_TASK_EVENT_TYPE_GUIDANCE_UPDATED:
				if value["state"] == "applied" && value["mode"] == "interrupt" {
					question = ""
				}
			case taskapiv1.TaskEventType_TASK_EVENT_TYPE_HEARTBEAT, taskapiv1.TaskEventType_TASK_EVENT_TYPE_TOKEN_DELTA, taskapiv1.TaskEventType_TASK_EVENT_TYPE_THINKING_DELTA, taskapiv1.TaskEventType_TASK_EVENT_TYPE_TASK_MESSAGE_EDITED, taskapiv1.TaskEventType_TASK_EVENT_TYPE_CONTEXT_USAGE, taskapiv1.TaskEventType_TASK_EVENT_TYPE_PLAN_UPDATED:
				// Same quiet-event semantics as the web conversation; no payload copied.
			default:
				question = ""
			}
		}
		if last > 0 && readLast >= last || !batch.GetMore() {
			if terminal(q.Status) {
				question = ""
				if summary == "" {
					summary = nodeAnswer
				}
				if !finished && summary == "" {
					return "", "", errors.New("entry terminal outcome has not arrived")
				}
			}
			return summary, question, nil
		}
		next := batch.GetNextAfterEventId()
		if next <= after || next < readLast {
			return "", "", errors.New("entry event cursor did not advance")
		}
		after = next
	}
	return "", "", errors.New("entry outcome pagination limit reached")
}

func entryFinishedText(value map[string]any, taskID, status string) (string, error) {
	if nested, ok := value["result"].(map[string]any); ok {
		value = nested
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	outcome := &workflowv1.TaskWorkflowResult{}
	if (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, outcome) != nil {
		return "", errors.New("invalid task-finished outcome")
	}
	if outcome.GetTaskId() != "" && outcome.GetTaskId() != taskID {
		return "", errors.New("task-finished identity mismatch")
	}
	state := outcome.GetStatus()
	if state == commonv1.TaskStatus_TASK_STATUS_UNSPECIFIED {
		state = commonv1.TaskStatus(commonv1.TaskStatus_value[status])
	}
	if terminal(status) && outcome.GetStatus() != commonv1.TaskStatus_TASK_STATUS_UNSPECIFIED && state.String() != status {
		return "", errors.New("task-finished status mismatch")
	}
	answer := entryPublicAnswer(outcome.GetDeliverable().GetValue().AsInterface())
	if state == commonv1.TaskStatus_TASK_STATUS_DONE {
		if answer == "" {
			return "The task is complete. Open the conversation to read the result.", nil
		}
		return answer, nil
	}
	label := "Task ended: " + strings.TrimPrefix(state.String(), "TASK_STATUS_")
	if state == commonv1.TaskStatus_TASK_STATUS_UNSPECIFIED {
		label = "Task ended"
	}
	if answer != "" {
		label += "\n\n" + answer
	}
	if e := outcome.GetError(); e != nil {
		if message := strings.TrimSpace(e.GetMessage()); message != "" {
			label += "\n\n" + message
		} else if e.GetCode() != "" {
			label += "\n\n" + e.GetCode()
		}
	}
	return label, nil
}

func entryQuestionText(v map[string]any) string {
	text, _ := v["text"].(string)
	if text == "" {
		text, _ = v["question"].(string)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if options, ok := v["options"].([]any); ok {
		choices := []string{}
		for _, option := range options {
			if s, ok := option.(string); ok && s != "" {
				choices = append(choices, s)
			}
		}
		if len(choices) > 0 {
			text += "\n\nOptions: " + strings.Join(choices, ", ")
		}
	}
	return text
}

// entryPublicAnswer handles both plain answers and structured abort deliverables.
// Only explicit deliverable objects reach here; internal metadata is removed even
// when an older producer accidentally includes it beside its public result.
func entryPublicAnswer(value any) string {
	if obj, ok := value.(map[string]any); ok {
		if wrapped, exists := obj["value"]; exists && len(obj) <= 2 {
			value = wrapped
		}
	}
	value = entryPublicValue(value, 0)
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	if obj, ok := value.(map[string]any); ok {
		if len(obj) == 0 {
			return ""
		}
		_, aborted := obj["not_completed"]
		if !aborted {
			for _, key := range []string{"text", "answer", "reply", "content", "final_answer"} {
				if text, ok := obj[key].(string); ok && strings.TrimSpace(text) != "" {
					return text
				}
			}
		}
	}
	if value == nil {
		return ""
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}
func entryPublicValue(value any, depth int) any {
	if depth > 64 {
		return nil
	}
	switch x := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, v := range x {
			switch strings.ToLower(key) {
			case "thinking", "reasoning", "rationale", "chain_of_thought", "analysis", "tool_calls", "raw_response", "codes":
				continue
			}
			out[key] = entryPublicValue(v, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = entryPublicValue(v, depth+1)
		}
		return out
	default:
		return value
	}
}
