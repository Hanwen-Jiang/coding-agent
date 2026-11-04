package server

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// agentBranch 是允许推送的分支：只推 agent/*，引用由服务端拼出，不强推。
var agentBranch = regexp.MustCompile(`^agent/[a-z0-9][a-z0-9._-]{0,60}$`)

func branchName(title string) string {
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(base) > 30 {
		base = strings.Trim(base[:30], "-")
	}
	if base == "" {
		base = "change"
	}
	return "agent/" + base + "-" + randomHex(2)
}

// updateProject 在锁里改一个项目的记录，返回改后的值。
func (s *Server) updateProject(tenant, id string, fn func(*Project)) (Project, error) {
	var out Project
	err := s.store.UpdateProjects(tenant, func(ps []Project) ([]Project, error) {
		for i := range ps {
			if ps[i].ID == id {
				fn(&ps[i])
				out = ps[i]
				return ps, nil
			}
		}
		return nil, msg("项目不存在")
	})
	return out, err
}

// handlePublish 把会话工作副本里的改动推到这个会话的 agent/* 分支并开草稿 PR（已有 PR 时只推送，PR 自动更新）。
// 一个会话一个分支、一个 PR。由人点按钮触发。
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request, id identity) {
	p, sess, ok := s.session(w, r, id)
	if !ok {
		return
	}
	prov := s.providers[p.Provider]
	if p.Repo == "" || prov == nil {
		writeMsg(w, r, http.StatusBadRequest, "只有从已配置的 Git 平台克隆的工作区能推送并开 PR")
		return
	}
	// 推送经 GitHub App 时用只含这个仓库、contents:write 的安装令牌（推完吊销）；开 PR 用用户令牌，记在这个人名下（G-03）。
	grant, err := s.authorizeRepo(r, id, &p, repoWrite)
	if errors.Is(err, errNotConnected) {
		writeMsg(w, r, http.StatusForbidden, "先在“账号与平台”里连接 %s，才能推送到 %[1]s", prov.Name)
		return
	} else if err != nil {
		writeGitError(w, r, err)
		return
	}
	defer grant.Done()
	conn, token := grant.Conn, grant.UserToken
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Agent changes"
	}
	if len(title) > 200 {
		title = title[:200]
	}
	if s.busy(r, id, sess.WorkspaceID) {
		writeMsg(w, r, http.StatusConflict, "还有任务在跑，等它结束再推送")
		return
	}
	ctx, cancel := ctxTimeout(r, 10*time.Minute)
	defer cancel()
	auth := grant.Auth
	name, email := prov.commitAuthor(conn)
	base := p.BaseBranch
	if base == "" {
		base = "main"
	}

	changes, err := s.ws.Changes(ctx, id, sess.WorkspaceID)
	if err != nil {
		writeWorkspaceError(w, r, err, http.StatusInternalServerError)
		return
	}
	dirty := len(changes.Files) > 0
	branch := sess.Branch
	if branch == "" {
		// 第一次推：工作区干净、也不比基线多提交时没有东西可推，不建分支。
		if ahead, err := s.ws.Log(ctx, id, sess.WorkspaceID, 1, "origin/"+base); !dirty && err == nil && len(ahead) == 0 {
			writeMsg(w, r, http.StatusBadRequest, "没有可推送的改动")
			return
		}
		branch = branchName(title)
	} else if !dirty && changes.Head == sess.PushedHead {
		writeMsg(w, r, http.StatusBadRequest, "没有可推送的改动")
		return
	}
	if !agentBranch.MatchString(branch) {
		writeMsg(w, r, http.StatusInternalServerError, "分支名不合法：%s", branch)
		return
	}
	if dirty {
		if _, _, err := s.ws.Commit(ctx, id, sess.WorkspaceID, title, name, email); err != nil {
			writeWorkspaceError(w, r, err, http.StatusInternalServerError)
			return
		}
	}
	// 推的是 HEAD，推到会话自己的 agent/* 分支（Runtime 那边按租户策略的 push_branches 再核一遍）；不强推。
	pushed, err := s.ws.Push(ctx, id, sess.WorkspaceID, branch, auth)
	if err != nil {
		_, _ = s.updateSession(id.TenantID, sess, func(x *Session) { x.Branch = branch })
		writeMsg(w, r, statusOf(err, http.StatusBadGateway), "推送失败（是否有这个仓库的写权限？）：%v", err)
		return
	}

	created := false
	prURL, prNumber := sess.PRURL, sess.PRNumber
	if prNumber == 0 {
		body := strings.TrimSpace(req.Body)
		if body == "" {
			body = msg("由 Coding Agent 生成的改动，请审阅后再合并。").In(requestLang(r))
		}
		prCtx, prCancel := ctxTimeout(r, 30*time.Second)
		pr, err := prov.openPR(prCtx, token, p.Repo, title, branch, base, body)
		prCancel()
		if err != nil {
			// 分支已推上去；PR 没开成时把分支记下来，下次推送再试。
			_, _ = s.updateSession(id.TenantID, sess, func(x *Session) { x.Branch, x.PushedHead = branch, pushed })
			writeGitError(w, r, err)
			return
		}
		prURL, prNumber, created = pr.HTMLURL, pr.Number, true
	}
	updated, err := s.updateSession(id.TenantID, sess, func(x *Session) {
		x.Branch, x.PRURL, x.PRNumber, x.PushedHead = branch, prURL, prNumber, pushed
	})
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": updated, "branch": branch, "pr_url": prURL, "pr_number": prNumber, "created": created, "head": pushed})
}

// handleResetBranch 让会话的工作副本回到基线分支并拉取最新：PR 合并之后开始下一轮。要求没有未提交的改动。
func (s *Server) handleResetBranch(w http.ResponseWriter, r *http.Request, id identity) {
	p, sess, ok := s.session(w, r, id)
	if !ok {
		return
	}
	if s.busy(r, id, sess.WorkspaceID) {
		writeMsg(w, r, http.StatusConflict, "还有任务在跑，等它结束再切换")
		return
	}
	ctx, cancel := ctxTimeout(r, 10*time.Minute)
	defer cancel()
	base := p.BaseBranch
	if base == "" {
		writeMsg(w, r, http.StatusBadRequest, "不知道基线分支")
		return
	}
	if changes, err := s.ws.Changes(ctx, id, sess.WorkspaceID); err != nil {
		writeWorkspaceError(w, r, err, http.StatusInternalServerError)
		return
	} else if len(changes.Files) > 0 {
		writeMsg(w, r, http.StatusConflict, "还有未提交的改动：先推送、提交或丢弃")
		return
	}
	// 取远端基线分支的最新提交并切过去（没有未提交的改动时；远端是建工作区时的仓库地址）。
	grant, err := s.authorizeRepo(r, id, &p, repoRead)
	if err != nil {
		writeGitError(w, r, err)
		return
	}
	defer grant.Done()
	head, err := s.ws.Sync(ctx, id, sess.WorkspaceID, base, grant.Auth)
	if err != nil {
		writeMsg(w, r, statusOf(err, http.StatusBadGateway), "拉取失败：%v", err)
		return
	}
	updated, err := s.updateSession(id.TenantID, sess, func(x *Session) { x.Branch, x.PRURL, x.PRNumber, x.PushedHead = "", "", 0, "" })
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": updated, "head": head})
}

// handleSync 把还没有自己分支的会话拉到基线分支在远端的最新提交（Workspace API 的 SyncWorkspace）。要求没有任务在跑、
// 没有未提交的改动，也没有还没推送的提交（会话开始以来自己的提交：拉取会切走它们）；已经推送到 agent/* 分支的会话用“回到基线分支”。
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request, id identity) {
	p, sess, ok := s.session(w, r, id)
	if !ok {
		return
	}
	switch {
	case !strings.HasPrefix(p.Source, "https://"):
		writeMsg(w, r, http.StatusBadRequest, "这个工作区没有远端仓库，没有可拉取的")
		return
	case p.BaseBranch == "":
		writeMsg(w, r, http.StatusBadRequest, "不知道基线分支")
		return
	case sess.Branch != "":
		writeMsg(w, r, http.StatusConflict, "这个会话已经推送到 %s：用“回到基线分支”", sess.Branch)
		return
	}
	if s.busy(r, id, sess.WorkspaceID) {
		writeMsg(w, r, http.StatusConflict, "还有任务在跑，等它结束再拉取")
		return
	}
	ctx, cancel := ctxTimeout(r, 10*time.Minute)
	defer cancel()
	if changes, err := s.ws.Changes(ctx, id, sess.WorkspaceID); err != nil {
		writeWorkspaceError(w, r, err, http.StatusInternalServerError)
		return
	} else if len(changes.Files) > 0 {
		writeMsg(w, r, http.StatusConflict, "还有未提交的改动：先提交或丢弃")
		return
	}
	base := sess.BaseCommit
	if base == "" {
		base = "origin/" + p.BaseBranch
	}
	if own, err := s.ws.Log(ctx, id, sess.WorkspaceID, 1, base); err != nil {
		writeWorkspaceError(w, r, err, http.StatusInternalServerError)
		return
	} else if len(own) > 0 {
		writeMsg(w, r, http.StatusConflict, "这个会话有还没推送的提交：先推送，拉取会切走它们")
		return
	}
	grant, err := s.authorizeRepo(r, id, &p, repoRead)
	if err != nil {
		writeGitError(w, r, err)
		return
	}
	defer grant.Done()
	head, err := s.ws.Sync(ctx, id, sess.WorkspaceID, p.BaseBranch, grant.Auth)
	if err != nil {
		writeMsg(w, r, statusOf(err, http.StatusBadGateway), "拉取失败：%v", err)
		return
	}
	updated, err := s.updateSession(id.TenantID, sess, func(x *Session) { x.BaseCommit = head })
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": updated, "head": head})
}
