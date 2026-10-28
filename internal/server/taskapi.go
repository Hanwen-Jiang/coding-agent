package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TaskAPI 调 task-service 的 Task API（connect 的 HTTP + JSON：POST /agentruntime.taskapi.v1.TaskService/<方法>）。
// 令牌原样转给 task-service，租户由它按令牌确定。
type TaskAPI struct {
	base   string
	client *http.Client
	stream *http.Client
	// 工作区的操作（克隆大仓库、推送）比 Task API 的调用慢得多：单独的超时。
	slow *http.Client
}

func NewTaskAPI(base string) *TaskAPI {
	return &TaskAPI{base: base, client: &http.Client{Timeout: 30 * time.Second}, stream: &http.Client{}, slow: &http.Client{Timeout: 15 * time.Minute}}
}

// CallWorkspace 调 task-service 上的 Workspace API（同一个端口：POST /agentruntime.workspace.v1.WorkspaceService/<方法>）。
func (a *TaskAPI) CallWorkspace(ctx context.Context, token, method string, req, resp any) error {
	return a.call(ctx, a.slow, "/agentruntime.workspace.v1.WorkspaceService/"+method, token, req, resp)
}

// APIError 是 Task API 返回的错误：Message 是 task-service 给的说明（原样，中文）；Reason 是 Runtime 错误模型里稳定的
// code（google.rpc.ErrorInfo，例如 network_not_allowed），界面按它翻译（runtimeMsg）。连不上这类我们这边的原因用 Note，按请求的语言写。
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
	Reason  string `json:"-"`
	Note    *Msg   `json:"-"`
}

// errorInfoDomain 是 Runtime 放进 google.rpc.ErrorInfo 的 domain（task-service 的 taskapi.ErrorDomain）。
const errorInfoDomain = "agentruntime"

// parseAPIError 读 connect 的 JSON 错误：code、message，以及 details 里 ErrorInfo 的 reason（connect 把 detail 的 JSON 放在 debug）。
func parseAPIError(status int, data []byte) *APIError {
	e := &APIError{Status: status}
	var wire struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Type  string `json:"type"`
			Debug struct {
				Reason string `json:"reason"`
				Domain string `json:"domain"`
			} `json:"debug"`
		} `json:"details"`
	}
	if json.Unmarshal(data, &wire) != nil {
		return e
	}
	e.Code, e.Message = wire.Code, wire.Message
	for _, d := range wire.Details {
		if d.Type == "google.rpc.ErrorInfo" && d.Debug.Domain == errorInfoDomain {
			e.Reason = d.Debug.Reason
		}
	}
	return e
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Task API %d %s：%s", e.Status, e.Code, e.Message)
}

func (a *TaskAPI) Call(ctx context.Context, token, method string, req, resp any) error {
	return a.call(ctx, a.client, "/agentruntime.taskapi.v1.TaskService/"+method, token, req, resp)
}

func (a *TaskAPI) call(ctx context.Context, client *http.Client, path, token string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	res, err := client.Do(r)
	if err != nil {
		note := msg("连不上 task-service")
		return &APIError{Status: http.StatusBadGateway, Code: "unavailable", Message: note.Error(), Note: note}
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return parseAPIError(res.StatusCode, data)
	}
	if resp == nil {
		return nil
	}
	return json.Unmarshal(data, resp)
}

// Events 打开一个 Task 的 SSE 流；调用方负责关闭 Body。
func (a *TaskAPI) Events(ctx context.Context, token, taskID, lastEventID string) (*http.Response, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/v1/tasks/"+taskID+"/events", nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		r.Header.Set("Last-Event-ID", lastEventID)
	}
	return a.stream.Do(r)
}

type submitResponse struct {
	TaskID       string `json:"taskId"`
	SessionID    string `json:"sessionId"`
	EventsURL    string `json:"eventsUrl"`
	Deduplicated bool   `json:"deduplicated"`
}

type queryResponse struct {
	TaskID      string          `json:"taskId"`
	SessionID   string          `json:"sessionId"`
	Status      string          `json:"status"`
	RoundSeq    int             `json:"roundSeq"`
	LastEventID string          `json:"lastEventId"`
	TaskState   json.RawMessage `json:"taskState,omitempty"`
	Usage       json.RawMessage `json:"usage,omitempty"`
}

// Healthy 看 task-service 的 /healthz 通不通（右栏“环境”用）。
func (a *TaskAPI) Healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/healthz", nil)
	if err != nil {
		return false
	}
	res, err := a.client.Do(r)
	if err != nil {
		return false
	}
	_ = res.Body.Close()
	return res.StatusCode == http.StatusOK
}
