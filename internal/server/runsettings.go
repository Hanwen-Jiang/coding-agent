package server

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
)

// 沙箱等级就是 Task 的 effect 上限（docs/plan/coding-agent-mvp.md 第 7.3 节）：
// read 只读代码、回答问题（shell_run 是 write_reversible，跑不了）；write_reversible 能改工作区、在沙箱里跑命令。
// 提交时经 SubmitTaskRequest.effect_ceiling 传给 Runtime（Runtime 的 D-08），只能比租户策略低；租户策略本身由 Runtime 的
// 运维定（开发时第一次登录给新租户 write_reversible，见 ensureTenantPolicy）。完全访问（联网、不可逆的外部写）不开放。
const defaultSandbox = "write_reversible"

var sandboxLevels = []string{"read", "write_reversible"}

// effectEnum 把沙箱等级换成 Task API 的 Effect 枚举名（proto 的 agentruntime.common.v1.Effect）。
func effectEnum(level string) string {
	return "EFFECT_" + strings.ToUpper(level)
}

// 模型倾向放进 inputs.constraints.model_preference，进 Task State 的约束，Planner 选 Tier 时参考。
const defaultModel = "balanced"

var modelPreferences = []string{"economy", "balanced", "best"}

func pickOption(value string, allowed []string, fallback string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, true
	}
	return value, slices.Contains(allowed, value)
}

// withQuery 用 QueryTask 的结果更新任务记录：状态、各 Round 花费之和、轮数与 usage；结束了就标为 Settled。
func withQuery(t TaskRecord, q queryResponse) TaskRecord {
	t.Status = q.Status
	var st struct {
		Value struct {
			Rounds []struct {
				CostUSD float64 `json:"cost_usd"`
			} `json:"rounds"`
		} `json:"value"`
	}
	if len(q.TaskState) > 0 && json.Unmarshal(q.TaskState, &st) == nil && len(st.Value.Rounds) > 0 {
		cost := 0.0
		for _, r := range st.Value.Rounds {
			cost += r.CostUSD
		}
		t.CostUSD = math.Round(cost*1e6) / 1e6
		t.Rounds = len(st.Value.Rounds)
	}
	if usage := strings.TrimSpace(string(q.Usage)); usage != "" && usage != "null" && usage != "{}" {
		t.Usage = q.Usage
	}
	t.Settled = terminal(q.Status)
	return t
}

func sameRecord(a, b TaskRecord) bool {
	return a.Status == b.Status && a.CostUSD == b.CostUSD && a.Rounds == b.Rounds && a.Settled == b.Settled && string(a.Usage) == string(b.Usage)
}

// handleEnv 是右栏“环境”：Runtime 是否连得上、沙箱、会话工作副本的分支与 HEAD、工作区来源与 Runtime 的 Session。
func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request, id identity) {
	p, sess, ok := s.session(w, r, id)
	if !ok {
		return
	}
	ctx, cancel := ctxTimeout(r, time.Minute)
	defer cancel()
	// 读不到（Runtime 那边一时不可用）时照样回其余的部分：分支、HEAD 为空。
	changes, _ := s.ws.Changes(ctx, id, sess.WorkspaceID)
	subject := ""
	if commits, err := s.ws.Log(ctx, id, sess.WorkspaceID, 1, ""); err == nil && len(commits) > 0 {
		subject = commits[0].Subject
	}
	sandbox := map[string]any{"mode": s.cfg.Sandbox}
	if s.cfg.Sandbox == "docker" {
		sandbox["image"] = s.cfg.SandboxImage
		sandbox["network"] = "none"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runtime": map[string]any{"ok": s.api.Healthy(ctx)},
		"sandbox": sandbox,
		"git": map[string]any{
			"branch": changes.Branch, "head": changes.Head, "subject": strings.TrimSpace(subject),
			"dirty": len(changes.Files), "base_branch": p.BaseBranch,
		},
		"project": map[string]any{"source": p.Source, "git_provider": p.Provider, "repo": p.Repo, "created_at": p.CreatedAt},
		"session": map[string]any{"workspace_id": sess.WorkspaceID, "runtime_session_id": sess.RuntimeSessionID, "base_commit": sess.BaseCommit, "own_copy": sess.WorkspaceID != p.ID},
		// 租户策略里现在的上限；每个任务实际的上限在它的 Task State 里。
		"effect_ceiling": s.tenantCeiling(id.TenantID),
	})
}

type activeTask struct {
	ProjectID    string     `json:"project_id"`
	ProjectName  string     `json:"project_name"`
	SessionID    string     `json:"session_id"`
	SessionTitle string     `json:"session_title"`
	Task         TaskRecord `json:"task"`
}

// handleActiveTasks 是“进行中”：这个租户所有工作区、所有会话里还没结束的任务，按提交时间排。
func (s *Server) handleActiveTasks(w http.ResponseWriter, r *http.Request, id identity) {
	projects, err := s.store.Projects(id.TenantID)
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	sessions, err := s.ensureSessions(id.TenantID)
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	names := map[string]string{}
	for _, p := range projects {
		names[p.ID] = p.Name
	}
	out := []activeTask{}
	for _, sess := range sessions {
		for _, t := range s.refresh(r, id, sess.WorkspaceID) {
			if !terminal(t.Status) {
				out = append(out, activeTask{ProjectID: sess.ProjectID, ProjectName: names[sess.ProjectID], SessionID: sess.ID, SessionTitle: sess.Title, Task: t})
			}
		}
	}
	slices.SortFunc(out, func(a, b activeTask) int { return a.Task.CreatedAt.Compare(b.Task.CreatedAt) })
	writeJSON(w, http.StatusOK, out)
}

// notFound 判断 Task API 的错误是不是“没有这个任务”（例如 Runtime 的数据重建过）：这样的任务不再去查。
func notFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
