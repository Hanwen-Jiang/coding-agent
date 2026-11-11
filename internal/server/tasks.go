package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxMessageChars = 20000

// maxBackfill 是一次刷新最多补查几个“已结束、还没取过计量”的任务（之前版本留下的记录）。
const maxBackfill = 8

// refresh 向 Runtime 查询一个会话（按 WorkspaceID）里还没结束的任务（以及少量还没取过计量的已结束任务），
// 更新缓存的状态与计量；查不到的保持原样。
func (s *Server) refresh(r *http.Request, id identity, workspaceID string) []TaskRecord {
	tasks, err := s.store.Tasks(id.TenantID, workspaceID)
	if err != nil {
		return nil
	}
	changed := map[string]TaskRecord{}
	backfill := 0
	for _, t := range tasks {
		if t.Settled {
			continue
		}
		if terminal(t.Status) {
			if backfill >= maxBackfill {
				continue
			}
			backfill++
		}
		var q queryResponse
		ctx, cancel := ctxTimeout(r, 10*time.Second)
		err := s.api.Call(ctx, id.token, "QueryTask", map[string]string{"taskId": t.ID}, &q)
		cancel()
		switch {
		case err == nil && q.Status != "":
			if next := withQuery(t, q); !sameRecord(next, t) {
				changed[t.ID] = next
			}
		case notFound(err) && terminal(t.Status):
			t.Settled = true
			changed[t.ID] = t
		}
	}
	if len(changed) > 0 {
		_ = s.store.UpdateTasks(id.TenantID, workspaceID, func(ts []TaskRecord) []TaskRecord {
			for i := range ts {
				if next, ok := changed[ts[i].ID]; ok {
					ts[i] = next
				}
			}
			return ts
		})
		for i := range tasks {
			if next, ok := changed[tasks[i].ID]; ok {
				tasks[i] = next
			}
		}
	}
	return tasks
}

// busy 判断一个会话里是否还有没结束的任务（提交、丢弃、删除之前查）。
func (s *Server) busy(r *http.Request, id identity, workspaceID string) bool {
	return slices.ContainsFunc(s.refresh(r, id, workspaceID), func(t TaskRecord) bool { return !terminal(t.Status) })
}

func (s *Server) handleListTasks(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	tasks := s.refresh(c.Request, id, sess.WorkspaceID)
	if tasks == nil {
		tasks = []TaskRecord{}
	}
	slices.SortFunc(tasks, func(a, b TaskRecord) int { return b.CreatedAt.Compare(a.CreatedAt) })
	writeJSON(c, http.StatusOK, tasks)
}

func (s *Server) handleSubmitTask(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	var req struct {
		SourceIDs         []string           `json:"source_ids"`
		SourceVersions    map[string]string  `json:"source_versions"`
		FileCollectionIDs []string           `json:"file_collection_ids"`
		Message           string             `json:"message"`
		Sandbox           string             `json:"sandbox"`
		Model             string             `json:"model"`
		Network           json.RawMessage    `json:"network_request"`
		PersonalConfig    *personalSelection `json:"personal_config"`
	}
	if !readJSON(c, &req) {
		return
	}
	settings := cloneTaskSettings(sess.TaskSettings)
	if req.Sandbox == "" {
		req.Sandbox = settings.Sandbox
	}
	network := settings.Network
	if len(req.Network) > 0 {
		network = nil // An explicit request replaces the saved preference, including null.
		if err := json.Unmarshal(req.Network, &network); err != nil {
			writeMsg(c, 400, "联网设置不合法")
			return
		}
	}
	selection := req.PersonalConfig
	if selection == nil {
		selection = sess.PersonalConfig
	}
	if err := selection.validate(); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" || utf8.RuneCountInString(msg) > maxMessageChars {
		writeMsg(c, http.StatusBadRequest, "任务描述不能为空，也不能超过 20000 个字")
		return
	}
	sandbox, ok := pickOption(req.Sandbox, sandboxLevels, defaultSandbox)
	if !ok {
		writeMsg(c, http.StatusBadRequest, "沙箱等级只能是 read 或 write_reversible")
		return
	}
	model, ok := pickOption(req.Model, modelPreferences, defaultModel)
	if !ok {
		writeMsg(c, http.StatusBadRequest, "模型倾向只能是 economy、balanced 或 best")
		return
	}
	if len(req.FileCollectionIDs) > 1 {
		writeMsg(c, 400, "每次任务最多选择一个知识集合")
		return
	}
	for _, collection := range req.FileCollectionIDs {
		if len(collection) != 36 || !strings.HasPrefix(collection, "col_") {
			writeMsg(c, 400, "知识集合标识无效")
			return
		}
	}
	sources, err := s.resolveSources(c.Request.Context(), id, sess.WorkspaceID, req.SourceIDs, req.SourceVersions)
	if err != nil {
		writeErr(c, 400, err)
		return
	}
	submit := map[string]any{
		"workspaceId":     sess.WorkspaceID,
		"clientRequestId": randomHex(16),
		"message":         msg,
		"sessionAccess":   "SESSION_ACCESS_READ_WRITE",
		"inputs":          map[string]any{"constraints": map[string]any{"model_preference": model}},
		// 沙箱等级是这个 Task 的 effect 上限（Runtime 的 SubmitTaskRequest.effect_ceiling，D-08）：只能比租户策略低，
		// 高了 Task API 直接拒绝，不悄悄截断。不再改写租户策略文件。
		"effectCeiling": effectEnum(sandbox),
	}
	if len(req.FileCollectionIDs) > 0 {
		submit["inputs"].(map[string]any)["file_collection_ids"] = req.FileCollectionIDs
	}
	if len(sources) > 0 {
		refs := make([]string, 0, len(sources))
		for _, source := range sources {
			refs = append(refs, source.ObjectURI)
		}
		submit["attachments"] = refs
	}
	if selection != nil {
		submit["inputs"].(map[string]any)["personal_config"] = selection
	}
	if network != nil {
		if err := network.validate(); err != nil {
			writeErr(c, http.StatusBadRequest, err)
			return
		}
		submit["networkRequest"] = network.wire()
	}
	if sess.RuntimeSessionID != "" {
		submit["sessionId"] = sess.RuntimeSessionID
	}
	if req.PersonalConfig != nil {
		if err := s.store.PutPersonalSession(id.TenantID, id.UserID, sess.WorkspaceID, selection); err != nil {
			writeErr(c, 500, err)
			return
		}
	}
	var resp submitResponse
	ctx, cancel := ctxTimeout(c.Request, 30*time.Second)
	defer cancel()
	if err := s.api.Call(ctx, id.token, "SubmitTask", submit, &resp); err != nil {
		writeAPIError(c, err)
		return
	}
	now := time.Now().UTC()
	_, _ = s.updateSession(id.TenantID, sess, func(x *Session) {
		if x.RuntimeSessionID == "" {
			x.RuntimeSessionID = resp.SessionID
		}
		if x.Title == "" {
			x.Title = sessionTitle(msg)
		}
		x.UpdatedAt = now
	})
	resolvedVersions := map[string]string{}
	for _, source := range sources {
		resolvedVersions[source.ID] = source.VersionID
	}
	record := TaskRecord{SourceVersions: resolvedVersions, FileCollectionIDs: append([]string(nil), req.FileCollectionIDs...), SourceIDs: append([]string(nil), req.SourceIDs...), ID: resp.TaskID, Message: msg, CreatedAt: now, CreatedBy: id.UserID, Status: "TASK_STATUS_QUEUED", Sandbox: sandbox, Model: model}
	if err := s.store.UpdateTasks(id.TenantID, sess.WorkspaceID, func(ts []TaskRecord) []TaskRecord {
		if slices.ContainsFunc(ts, func(t TaskRecord) bool { return t.ID == record.ID }) {
			return ts
		}
		return append(ts, record)
	}); err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	// 任务开始时的提交记在后台补上：经 Workspace API 读 HEAD 要借一个沙箱环境（冷启动几秒到十几秒），不让提交等它。
	// Task 自己不提交（提交由用户点按钮），所以晚一点读到的 HEAD 与提交那一刻相同。
	s.background.Go(func() { s.recordBaseCommit(id, sess.WorkspaceID, record.ID) })
	writeJSON(c, http.StatusCreated, record)
}

// recordBaseCommit 读工作区当前的提交，补进任务记录（读不到时留空，与以前相同）。
func (s *Server) recordBaseCommit(id identity, workspaceID, taskID string) {
	lock := s.workspaceLock(workspaceID)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	commits, err := s.ws.Log(ctx, id, workspaceID, 1, "")
	if err != nil || len(commits) == 0 {
		return
	}
	_ = s.store.UpdateTasks(id.TenantID, workspaceID, func(ts []TaskRecord) []TaskRecord {
		for i := range ts {
			if ts[i].ID == taskID && ts[i].BaseCommit == "" {
				ts[i].BaseCommit = commits[0].Hash
			}
		}
		return ts
	})
}

// workspaceLock 让后台读提交与删工作区互斥：删的时候不撞上还在工作区里跑的 git（Windows 上删不掉正被打开的目录）。
func (s *Server) workspaceLock(workspaceID string) *sync.Mutex {
	m, _ := s.wsLocks.LoadOrStore(workspaceID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func (s *Server) deleteWorkspace(ctx context.Context, id identity, workspaceID string) error {
	lock := s.workspaceLock(workspaceID)
	lock.Lock()
	defer lock.Unlock()
	return s.ws.Delete(ctx, id, workspaceID)
}

// task 取出请求路径里的任务，确认它属于这个租户的这个会话。
func (s *Server) task(c *gin.Context, id identity) (Session, TaskRecord, bool) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return Session{}, TaskRecord{}, false
	}
	tasks, err := s.store.Tasks(id.TenantID, sess.WorkspaceID)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return Session{}, TaskRecord{}, false
	}
	i := slices.IndexFunc(tasks, func(t TaskRecord) bool { return t.ID == c.Param("task") })
	if i < 0 {
		writeMsg(c, http.StatusNotFound, "任务不存在")
		return Session{}, TaskRecord{}, false
	}
	return sess, tasks[i], true
}

func (s *Server) handleQueryTask(c *gin.Context, id identity) {
	sess, t, ok := s.task(c, id)
	if !ok {
		return
	}
	var raw json.RawMessage
	ctx, cancel := ctxTimeout(c.Request, 15*time.Second)
	defer cancel()
	if err := s.api.Call(ctx, id.token, "QueryTask", map[string]string{"taskId": t.ID}, &raw); err != nil {
		writeAPIError(c, err)
		return
	}
	var q queryResponse
	if err := json.Unmarshal(raw, &q); err == nil && q.Status != "" {
		if next := withQuery(t, q); !sameRecord(next, t) {
			_ = s.store.UpdateTasks(id.TenantID, sess.WorkspaceID, func(ts []TaskRecord) []TaskRecord {
				for i := range ts {
					if ts[i].ID == t.ID {
						ts[i] = next
					}
				}
				return ts
			})
			t = next
		}
	}
	writeJSON(c, http.StatusOK, map[string]any{"task": t, "runtime": raw})
}

// handleEvents 代理 Task 的 SSE 流：浏览器的 EventSource 带不了 Authorization 头，由这里补上令牌。
// 断线重连时浏览器会带 Last-Event-ID，原样转给 task-service 续传。
func (s *Server) handleEvents(c *gin.Context, id identity) {
	_, t, ok := s.task(c, id)
	if !ok {
		return
	}
	last := c.GetHeader("Last-Event-ID")
	if last == "" {
		last = c.Query("last_event_id")
	}
	res, err := s.api.Events(c.Request.Context(), id.token, t.ID, last)
	if err != nil {
		writeMsg(c, http.StatusBadGateway, "连不上 task-service")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		var reason struct {
			Code string `json:"code"`
		}
		if res.StatusCode == http.StatusConflict && json.Unmarshal(body, &reason) == nil && reason.Code == "resnapshot_required" {
			// 游标过期（N-19）：EventSource 读不到状态码，换成一条 resnapshot_required 事件，页面据此重新取快照。
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Status(http.StatusOK)
			_, _ = io.WriteString(c.Writer, "event: resnapshot_required\ndata: {}\n\n")
			return
		}
		writeErr(c, res.StatusCode, errors.New(strings.TrimSpace(string(body)))) // task-service 的说明原样转出
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	// 上游的事件原样转发，读到一块就写一块并刷新。不用 c.SSEvent：那会把事件重新编码。
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
		if n > 0 {
			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				return
			}
			c.Writer.Flush()
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) handleReply(c *gin.Context, id identity) {
	_, t, ok := s.task(c, id)
	if !ok {
		return
	}
	var req struct {
		QuestionID string `json:"question_id"`
		Option     string `json:"option"`
		Guidance   string `json:"guidance"`
	}
	if !readJSON(c, &req) {
		return
	}
	if req.QuestionID == "" || req.Option == "" {
		writeMsg(c, http.StatusBadRequest, "要给出 question_id 与 option")
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 15*time.Second)
	defer cancel()
	signal := map[string]any{"taskId": t.ID, "humanReply": map[string]string{"questionId": req.QuestionID, "option": req.Option, "guidance": req.Guidance}}
	if err := s.api.Call(ctx, id.token, "SignalTask", signal, nil); err != nil {
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCancel(c *gin.Context, id identity) {
	_, t, ok := s.task(c, id)
	if !ok {
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 15*time.Second)
	defer cancel()
	var resp map[string]any
	if err := s.api.Call(ctx, id.token, "CancelTask", map[string]string{"taskId": t.ID, "reason": msg("用户在网页上取消").In(requestLang(c.Request))}, &resp); err != nil {
		writeAPIError(c, err)
		return
	}
	writeJSON(c, http.StatusOK, resp)
}

func writeAPIError(c *gin.Context, err error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		status := apiErr.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		switch m := runtimeMsg(apiErr.Reason); {
		case apiErr.Note != nil:
			writeErr(c, status, apiErr.Note)
		case m != nil:
			writeErr(c, status, m)
		case requestLang(c.Request) == langEN:
			// Runtime 的说明是中文：没有翻译的错误，英文界面只给错误码。
			code := apiErr.Reason
			if code == "" {
				code = apiErr.Code
			}
			writeMsg(c, status, "Runtime 返回了错误（%s）", code)
		default:
			writeErr(c, status, errors.New(apiErr.Message)) // task-service 的说明原样转出
		}
		return
	}
	writeErr(c, http.StatusBadGateway, err)
}
