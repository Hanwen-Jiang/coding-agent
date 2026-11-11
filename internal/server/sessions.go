package server

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// 会话（docs/plan/coding-agent-mvp.md 第 8 节）：一个工作区可以开很多会话，每个会话一段对话（Runtime 的一个 Session）
// 加一份自己的工作副本。工作副本从工作区的基线仓库复制（Runtime 的 Workspace API 的 from_workspace_id），Runtime 的
// workspace id 是 <项目>--<会话>：Worker 的工具与沙箱看到的是一个完整的仓库，会话之间可以同时跑、互不干扰。

// legacySession 是会话出现之前的项目迁移成的会话：直接用项目自己的目录。
const legacySession = "main"

const maxTitleRunes = 40

func newSessionID() string {
	return "s" + randomHex(3)
}

// sessionTitle 从任务描述里取会话标题：第一行，最多 40 个字。
func sessionTitle(message string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > maxTitleRunes {
		line = string([]rune(line)[:maxTitleRunes]) + "…"
	}
	return line
}

// ensureSessions 返回租户的全部会话；还没有会话、但有旧数据（对话、任务或推送记录）的项目先迁移成 main 会话。
func (s *Server) ensureSessions(tenant string) ([]Session, error) {
	projects, err := s.store.Projects(tenant)
	if err != nil {
		return nil, err
	}
	sessions, err := s.store.Sessions(tenant)
	if err != nil {
		return nil, err
	}
	var add []Session
	for _, p := range projects {
		if slices.ContainsFunc(sessions, func(x Session) bool { return x.ProjectID == p.ID }) {
			continue
		}
		if p.SessionID == "" && p.Branch == "" && !s.store.HasTasks(tenant, p.ID) {
			continue
		}
		main := Session{
			ID: legacySession, ProjectID: p.ID, Title: p.Name, CreatedAt: p.CreatedAt, CreatedBy: p.CreatedBy, UpdatedAt: p.CreatedAt,
			WorkspaceID: p.ID, RuntimeSessionID: p.SessionID, Branch: p.Branch, PRURL: p.PRURL, PRNumber: p.PRNumber, PushedHead: p.PushedHead,
		}
		if tasks, _ := s.store.Tasks(tenant, p.ID); len(tasks) > 0 {
			main.Title = sessionTitle(tasks[0].Message)
			main.UpdatedAt = tasks[len(tasks)-1].CreatedAt
		}
		add = append(add, main)
	}
	if len(add) == 0 {
		return sessions, nil
	}
	err = s.store.UpdateSessions(tenant, func(cur []Session) ([]Session, error) {
		for _, m := range add {
			if !slices.ContainsFunc(cur, func(x Session) bool { return x.ProjectID == m.ProjectID }) {
				cur = append(cur, m)
			}
		}
		sessions = cur
		return cur, nil
	})
	return sessions, err
}

// session 取出请求路径里的工作区与会话，确认它们属于这个租户。
func (s *Server) session(c *gin.Context, id identity) (Project, Session, bool) {
	p, ok := s.project(c, id)
	if !ok {
		return Project{}, Session{}, false
	}
	sid := c.Param("session")
	var saved Session
	if workspaceFact(c.Request.Context(), "session:"+p.ID+":"+sid, &saved) {
		projected, err := s.personalSession(id, saved)
		if err != nil {
			writeErr(c, 500, err)
			return Project{}, Session{}, false
		}
		return p, projected, true
	}
	sessions, err := s.ensureSessions(id.TenantID)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return Project{}, Session{}, false
	}
	i := slices.IndexFunc(sessions, func(x Session) bool { return x.ProjectID == p.ID && x.ID == sid })
	if !slug.MatchString(sid) || i < 0 {
		writeMsg(c, http.StatusNotFound, "会话不存在")
		return Project{}, Session{}, false
	}
	saveWorkspaceFact(c.Request.Context(), "session:"+p.ID+":"+sid, sessions[i])
	projected, err := s.personalSession(id, sessions[i])
	if err != nil {
		writeErr(c, 500, err)
		return Project{}, Session{}, false
	}
	return p, projected, true
}

// updateSession 在锁里改一个会话的记录，返回改后的值。
func (s *Server) updateSession(tenant string, sess Session, fn func(*Session)) (Session, error) {
	var out Session
	err := s.store.UpdateSessions(tenant, func(all []Session) ([]Session, error) {
		for i := range all {
			if all[i].ProjectID == sess.ProjectID && all[i].ID == sess.ID {
				if sess.repo != "" {
					projected := projectRepoSession(all[i], sess.repo)
					fn(&projected)
					if all[i].RepoStates == nil {
						all[i].RepoStates = make(map[string]RepoSessionState)
					}
					all[i].RepoStates[sess.repo] = repoSessionState(projected, all[i].RepoStates[sess.repo].BaseBranch)
				} else {
					fn(&all[i])
				}
				out = all[i]
				return all, nil
			}
		}
		return nil, msg("会话不存在")
	})
	out.PersonalConfig = clonePersonalSelection(sess.PersonalConfig)
	out.TaskSettings = cloneTaskSettings(sess.TaskSettings)
	return out, err
}

func (s *Server) handleListSessions(c *gin.Context, id identity) {
	sessions, err := s.ensureSessions(id.TenantID)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	if sessions == nil {
		sessions = []Session{}
	}
	for i := range sessions {
		sessions[i], err = s.personalSession(id, sessions[i])
		if err != nil {
			writeErr(c, 500, err)
			return
		}
	}
	slices.SortFunc(sessions, func(a, b Session) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	writeJSON(c, http.StatusOK, sessions)
}

func (s *Server) handleCreateSession(c *gin.Context, id identity) {
	p, ok := s.project(c, id)
	if !ok {
		return
	}
	var req struct {
		Title          string               `json:"title"`
		EnvironmentID  string               `json:"environment_id"`
		PersonalConfig *personalSelection   `json:"personal_config"`
		TaskSettings   *sessionTaskSettings `json:"task_settings"`
	}
	if !readJSON(c, &req) {
		return
	}
	if err := req.PersonalConfig.validate(); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if err := req.TaskSettings.validate(); err != nil {
		writeErr(c, 400, err)
		return
	}
	if _, err := s.ensureSessions(id.TenantID); err != nil { // 旧项目先迁移出 main 会话，免得被新会话挤掉
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	var env *Environment
	if req.EnvironmentID != "" {
		found, ok, err := s.environmentForUser(id.TenantID, id.UserID, req.EnvironmentID)
		if err != nil {
			writeErr(c, http.StatusInternalServerError, err)
			return
		} else if !ok {
			writeMsg(c, http.StatusBadRequest, "环境不存在或不属于当前用户")
			return
		}
		env = &found
	}
	sess, err := s.createSession(c.Request, id, p, sessionTitle(req.Title), env, nil)
	if err != nil {
		writeMsg(c, http.StatusInternalServerError, "开会话失败：%v", err)
		return
	}
	if req.PersonalConfig != nil {
		err = s.store.PutPersonalSession(id.TenantID, id.UserID, sess.WorkspaceID, req.PersonalConfig)
		if err != nil {
			writeErr(c, http.StatusInternalServerError, err)
			return
		}
		sess.PersonalConfig = clonePersonalSelection(req.PersonalConfig)
	}
	if err := s.store.PutTaskSettings(id.TenantID, id.UserID, sess.WorkspaceID, req.TaskSettings); err != nil {
		writeErr(c, 500, err)
		return
	}
	sess.TaskSettings = cloneTaskSettings(req.TaskSettings)
	writeJSON(c, http.StatusCreated, sess)
}

// createSession 从工作区的基线仓库复制出会话的工作副本（Workspaces.Create 的 From），Git 平台的工作区再尽量切到基线分支
// 在远端的最新提交（拉不到时照用复制来的）。给了 from 时改从那个会话的工作副本复制（复制会话：连同未提交的改动，不拉取），
// 基线提交沿用它的。选了环境配置时记在会话上，再经接缝交给 Runtime（envhandoff.go）。
func (s *Server) createSession(r *http.Request, id identity, p Project, title string, env *Environment, from *Session) (Session, error) {
	now := workspaceCreated(r.Context())
	sess := Session{ID: workspaceResourceID(r.Context(), "session", newSessionID()), ProjectID: p.ID, Title: title, CreatedAt: now, CreatedBy: id.UserID, UpdatedAt: now}
	// Existing project IDs can already occupy most of Runtime's 63-byte slug.
	// Keep an async workspace identity independent of their combined lengths.
	sess.WorkspaceID = workspaceResourceID(r.Context(), "workspace", p.ID+"--"+sess.ID)
	ctx, cancel := ctxTimeout(r, 10*time.Minute)
	defer cancel()
	source := p.ID
	if from != nil {
		source = from.WorkspaceID
		sess.PersonalConfig = from.PersonalConfig
		sess.TaskSettings = cloneTaskSettings(from.TaskSettings)
	}
	info, err := s.ws.Create(ctx, id, sess.WorkspaceID, CreateSpec{From: source, Layout: p.Layout})
	if err != nil {
		return Session{}, err
	}
	sess.BaseCommit = info.Head
	if p.Layout == layoutMulti {
		sess.RepoStates = make(map[string]RepoSessionState)
		for _, repo := range info.Repos {
			base, baseBranch := repo.Head, repo.Branch
			if from != nil {
				if old, ok := from.RepoStates[repo.Name]; ok {
					base, baseBranch = old.BaseCommit, old.BaseBranch
				}
			}
			sess.RepoStates[repo.Name] = RepoSessionState{BaseCommit: base, BaseBranch: baseBranch}
		}
	}
	if from != nil && from.BaseCommit != "" {
		sess.BaseCommit = from.BaseCommit
	}
	// 环境配置的快照：新会话取选定配置的这一刻；复制的会话沿用原会话的快照（不是活的配置，删掉了也照用）。
	var snap *EnvironmentSnapshot
	if env != nil {
		taken := snapshotOf(*env, now)
		snap = &taken
	} else if from != nil {
		if old, ok, err := s.store.Snapshot(id.TenantID, from.WorkspaceID); err == nil && ok {
			snap = &old
		}
	}
	if from == nil && p.Repo != "" && strings.HasPrefix(p.Source, "https://") && s.providers[p.Provider] != nil && p.BaseBranch != "" {
		// 尽力而为：拿不到授权或拉不下来时照用复制来的基线。
		if grant, err := s.authorizeRepo(r, id, &p, repoRead); err == nil {
			if head, err := s.ws.Sync(ctx, id, sess.WorkspaceID, p.BaseBranch, grant.Auth); err == nil {
				sess.BaseCommit = head
			}
			grant.Done()
		}
	}
	if snap != nil {
		sess.EnvironmentID, sess.EnvironmentHandoff = snap.EnvironmentID, handoffPending
		if err := s.store.PutSnapshot(id.TenantID, sess.WorkspaceID, *snap); err != nil {
			_ = s.ws.Delete(ctx, id, sess.WorkspaceID)
			return Session{}, err
		}
		if in, ok, err := s.environmentHandoffInput(id.TenantID, sess); err == nil && ok {
			if state, err := s.handoff.Bind(ctx, id, sess, in); err == nil && state != "" {
				sess.EnvironmentHandoff = state
			}
		}
	}
	if err := s.store.UpdateSessions(id.TenantID, func(all []Session) ([]Session, error) {
		for _, existing := range all {
			if existing.ProjectID == sess.ProjectID && existing.ID == sess.ID {
				return all, nil
			}
		}
		return append(all, sessionWithoutPersonal(sess)), nil
	}); err != nil {
		_ = s.ws.Delete(ctx, id, sess.WorkspaceID)
		_ = s.store.DeleteSession(id.TenantID, sess)
		return Session{}, err
	}
	if from != nil && from.PersonalConfig != nil {
		if err := s.store.PutPersonalSession(id.TenantID, id.UserID, sess.WorkspaceID, from.PersonalConfig); err != nil {
			return Session{}, err
		}
	}
	if from != nil {
		if err := s.store.PutTaskSettings(id.TenantID, id.UserID, sess.WorkspaceID, from.TaskSettings); err != nil {
			return Session{}, err
		}
	}
	return sess, nil
}

// handleDuplicateSession 复制会话（Workspace API 的 copy）：新会话的工作副本从这个会话的工作副本复制，环境配置的快照沿用（配置删掉了也照用，没有密钥），
// 对话、分支与 PR 不带过去（新会话推送时有自己的 agent/* 分支）。在跑任务时不复制：拿到的会是半截的改动。
func (s *Server) handleDuplicateSession(c *gin.Context, id identity) {
	p, src, ok := s.session(c, id)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if !readJSON(c, &req) {
		return
	}
	if s.busy(c.Request, id, src.WorkspaceID) {
		writeMsg(c, http.StatusConflict, "还有任务在跑，等它结束再复制")
		return
	}
	title := sessionTitle(req.Title)
	if title == "" {
		title = src.Title
	}
	sess, err := s.createSession(c.Request, id, p, title, nil, &src)
	if err != nil {
		writeMsg(c, statusOf(err, http.StatusInternalServerError), "复制会话失败：%v", err)
		return
	}
	writeJSON(c, http.StatusCreated, sess)
}

func (s *Server) handleRenameSession(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if !readJSON(c, &req) {
		return
	}
	title := sessionTitle(req.Title)
	if title == "" {
		writeMsg(c, http.StatusBadRequest, "标题不能为空")
		return
	}
	updated, err := s.updateSession(id.TenantID, sess, func(x *Session) { x.Title = title })
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	writeJSON(c, http.StatusOK, updated)
}

// handleDeleteSession 删掉会话与它的工作副本；迁移来的 main 会话用的是工作区自己的目录，只删记录，不删目录。
func (s *Server) handleDeleteSession(c *gin.Context, id identity) {
	p, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	if s.busy(c.Request, id, sess.WorkspaceID) {
		writeMsg(c, http.StatusConflict, "还有任务在跑，等它结束或先取消")
		return
	}
	if sess.WorkspaceID != p.ID {
		ctx, cancel := ctxTimeout(c.Request, 5*time.Minute)
		defer cancel()
		if err := s.deleteWorkspace(ctx, id, sess.WorkspaceID); err != nil {
			writeWorkspaceError(c, err, http.StatusInternalServerError)
			return
		}
	}
	if err := s.store.DeleteSession(id.TenantID, sess); err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]bool{"ok": true})
}

// projectSessions 是工作区下的全部会话。
func (s *Server) projectSessions(tenant, project string) ([]Session, error) {
	sessions, err := s.ensureSessions(tenant)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(sessions, func(x Session) bool { return x.ProjectID != project }), nil
}

// sessionWorkspaces 是工作区下所有会话自己的工作副本（不含基线仓库）。
func sessionWorkspaces(p Project, sessions []Session) []string {
	var out []string
	for _, x := range sessions {
		if x.WorkspaceID != p.ID {
			out = append(out, x.WorkspaceID)
		}
	}
	return out
}
