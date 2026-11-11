package server

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const layoutMulti = "WORKSPACE_LAYOUT_MULTI"
const repoWriteAccess = "REPO_ACCESS_WRITE"

// Repo metadata is authoritative in Runtime; coding-web stores only per-repository UI state.
type WorkspaceRepo struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Ref        string `json:"ref,omitempty"`
	Path       string `json:"path"`
	Access     string `json:"access"`
	Primary    bool   `json:"primary"`
	Head       string `json:"head,omitempty"`
	Branch     string `json:"branch,omitempty"`
	AttachedAt string `json:"attachedAt,omitempty"`
}

type RepoSessionState struct {
	BaseBranch string `json:"base_branch,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	Branch     string `json:"branch,omitempty"`
	PRURL      string `json:"pr_url,omitempty"`
	PRNumber   int    `json:"pr_number,omitempty"`
	PushedHead string `json:"pushed_head,omitempty"`
}

func repoSessionState(s Session, base string) RepoSessionState {
	return RepoSessionState{BaseBranch: base, BaseCommit: s.BaseCommit, Branch: s.Branch, PRURL: s.PRURL, PRNumber: s.PRNumber, PushedHead: s.PushedHead}
}
func projectRepoSession(s Session, name string) Session {
	r := s.RepoStates[name]
	s.repo = name
	s.BaseCommit, s.Branch, s.PRURL, s.PRNumber, s.PushedHead = r.BaseCommit, r.Branch, r.PRURL, r.PRNumber, r.PushedHead
	s.PRState, s.CI, s.Reviews = "", nil, nil
	return s
}

type multiWorkspaces interface {
	Repos(context.Context, identity, string) ([]WorkspaceRepo, error)
	Attach(context.Context, identity, string, WorkspaceRepo, GitAuth) (WorkspaceRepo, error)
	Detach(context.Context, identity, string, string, bool) (map[string]any, error)
}

func (w runtimeWorkspaces) Repos(ctx context.Context, id identity, ws string) ([]WorkspaceRepo, error) {
	var resp struct {
		Repos []WorkspaceRepo `json:"repos"`
	}
	err := w.api.CallWorkspace(ctx, id.token, "ListRepos", map[string]any{"workspaceId": ws}, &resp)
	if resp.Repos == nil {
		resp.Repos = []WorkspaceRepo{}
	}
	return resp.Repos, err
}
func (w runtimeWorkspaces) Attach(ctx context.Context, id identity, ws string, repo WorkspaceRepo, auth GitAuth) (WorkspaceRepo, error) {
	req := map[string]any{"workspaceId": ws, "repo": repo}
	if c := credential(auth); c != nil {
		req["credential"] = c
	}
	var resp struct {
		Repo WorkspaceRepo `json:"repo"`
	}
	err := w.api.workspaceOperation(ctx, id.token, "AttachRepo", req, &resp)
	return resp.Repo, err
}
func (w runtimeWorkspaces) Detach(ctx context.Context, id identity, ws, name string, discard bool) (map[string]any, error) {
	var out map[string]any
	err := w.api.workspaceOperation(ctx, id.token, "DetachRepo", map[string]any{"workspaceId": ws, "repo": name, "discardChanges": discard}, &out)
	return out, err
}

// Resolve a selected repository before any Git action or credential issuance. A synthetic
// Project intentionally has no ID: GitHub's per-project cache must never overwrite the
// enclosing workspace's remote reference with a child repository's credentials or IDs.
func (s *Server) repoProject(p Project, repo WorkspaceRepo, base string) Project {
	out := Project{Name: p.Name, Source: repo.URL, BaseBranch: base, Layout: layoutMulti}
	if prov, name := s.providerOfURL(repo.URL); prov != nil {
		out.Provider, out.Repo = prov.id, name
	}
	return out
}
func (s *Server) gitSession(c *gin.Context, id identity, write bool) (identity, Project, Session, bool) {
	p, sess, ok := s.session(c, id)
	if !ok {
		return id, p, sess, false
	}
	name := c.Query("repo")
	if p.Layout != layoutMulti {
		if name != "" {
			writeMsg(c, http.StatusBadRequest, "根仓工作区不能选择子仓库")
			return id, p, sess, false
		}
		return id, p, sess, true
	}
	mw, ok := s.ws.(multiWorkspaces)
	if !ok {
		writeMsg(c, http.StatusBadRequest, "多仓工作区需要 Runtime Workspace API")
		return id, p, sess, false
	}
	repos, err := mw.Repos(c.Request.Context(), id, sess.WorkspaceID)
	if err != nil {
		writeWorkspaceError(c, err, http.StatusBadGateway)
		return id, p, sess, false
	}
	at := slices.IndexFunc(repos, func(x WorkspaceRepo) bool { return x.Name == name })
	if name == "" || at < 0 {
		writeMsg(c, http.StatusBadRequest, "请选择当前工作区中的仓库")
		return id, p, sess, false
	}
	repo := repos[at]
	if write && repo.Access != repoWriteAccess {
		writeMsg(c, http.StatusForbidden, "这个仓库是只读的，不能提交或推送")
		return id, p, sess, false
	}
	base := sess.RepoStates[name].BaseBranch
	if base == "" {
		base = repo.Ref
	}
	if base == "" {
		base = repo.Branch
	}
	p = s.repoProject(p, repo, base)
	sess = projectRepoSession(sess, name)
	id.repo = name
	return id, p, sess, true
}

func (s *Server) repoWorkspace(c *gin.Context, id identity) (Project, Session, string, multiWorkspaces, bool) {
	var p Project
	var sess Session
	var ok bool
	if c.Param("session") != "" {
		p, sess, ok = s.session(c, id)
	} else {
		p, ok = s.project(c, id)
	}
	if !ok {
		return p, sess, "", nil, false
	}
	mw, good := s.ws.(multiWorkspaces)
	if p.Layout != layoutMulti || !good {
		writeMsg(c, http.StatusBadRequest, "多仓工作区需要 Runtime Workspace API")
		return p, sess, "", nil, false
	}
	ws := p.ID
	if sess.WorkspaceID != "" {
		ws = sess.WorkspaceID
	}
	return p, sess, ws, mw, true
}

func (s *Server) handleWorkspaceRepos(c *gin.Context, id identity) {
	p, sess, ws, mw, ok := s.repoWorkspace(c, id)
	if !ok {
		return
	}
	repos, err := mw.Repos(c.Request.Context(), id, ws)
	if err != nil {
		writeWorkspaceError(c, err, http.StatusBadGateway)
		return
	}
	type view struct {
		WorkspaceRepo
		Provider   string           `json:"git_provider,omitempty"`
		RemoteRepo string           `json:"remote_repo,omitempty"`
		State      RepoSessionState `json:"state"`
	}
	out := make([]view, 0, len(repos))
	for _, repo := range repos {
		st := sess.RepoStates[repo.Name]
		if st.BaseBranch == "" {
			st.BaseBranch = repo.Branch
		}
		if st.BaseCommit == "" {
			st.BaseCommit = repo.Head
		}
		rp := s.repoProject(p, repo, st.BaseBranch)
		out = append(out, view{repo, rp.Provider, rp.Repo, st})
	}
	writeJSON(c, http.StatusOK, map[string]any{"layout": p.Layout, "repos": out})
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var repoPath = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)

func (s *Server) handleAttachRepo(c *gin.Context, id identity) {
	p, sess, ws, mw, ok := s.repoWorkspace(c, id)
	if !ok {
		return
	}
	var req struct {
		Repo WorkspaceRepo `json:"repo"`
	}
	if !readJSON(c, &req) {
		return
	}
	repo := req.Repo
	u, err := url.Parse(repo.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		writeMsg(c, http.StatusBadRequest, "只支持 https 的 Git 地址，且地址里不能带用户名或密码")
		return
	}
	if !repoName.MatchString(repo.Name) || !repoPath.MatchString(repo.Path) {
		writeMsg(c, http.StatusBadRequest, "仓库名称或一级目录名不合法")
		return
	}
	if repo.Access == "" {
		repo.Access = "REPO_ACCESS_READ"
	}
	if repo.Access != "REPO_ACCESS_READ" && repo.Access != repoWriteAccess {
		writeMsg(c, http.StatusBadRequest, "仓库权限只能是只读或可写")
		return
	}
	if strings.HasPrefix(repo.Ref, "-") || strings.ContainsAny(repo.Ref, " \t\n~^:?*[\\") {
		writeMsg(c, http.StatusBadRequest, "分支名不合法")
		return
	}
	if s.busy(c.Request, id, ws) {
		writeMsg(c, http.StatusConflict, "还有任务在跑，等它结束或先取消")
		return
	}
	// Response-only metadata is ignored. Grant is restricted to this URL and call.
	repo.Head, repo.Branch, repo.AttachedAt = "", "", ""
	rp := s.repoProject(p, repo, repo.Ref)
	grant, err := s.authorizeRepo(c.Request, id, &rp, repoRead)
	if err != nil {
		writeGitError(c, err)
		return
	}
	defer grant.Done()
	ctx, cancel := ctxTimeout(c.Request, 10*time.Minute)
	defer cancel()
	attached, err := mw.Attach(ctx, id, ws, repo, grant.Auth)
	if err != nil {
		writeWorkspaceError(c, err, http.StatusBadGateway)
		return
	}
	if sess.ID != "" {
		_, err = s.updateSession(id.TenantID, sess, func(x *Session) {
			if x.RepoStates == nil {
				x.RepoStates = make(map[string]RepoSessionState)
			}
			x.RepoStates[attached.Name] = RepoSessionState{BaseCommit: attached.Head, BaseBranch: attached.Branch}
		})
		if err != nil {
			writeErr(c, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(c, http.StatusCreated, map[string]any{"repo": attached})
}
func (s *Server) handleDetachRepo(c *gin.Context, id identity) {
	_, sess, ws, mw, ok := s.repoWorkspace(c, id)
	if !ok {
		return
	}
	var req struct {
		DiscardChanges bool `json:"discard_changes"`
	}
	if !readJSON(c, &req) {
		return
	}
	if s.busy(c.Request, id, ws) {
		writeMsg(c, http.StatusConflict, "还有任务在跑，等它结束或先取消")
		return
	}
	name := c.Param("repo")
	ctx, cancel := ctxTimeout(c.Request, 10*time.Minute)
	defer cancel()
	out, err := mw.Detach(ctx, id, ws, name, req.DiscardChanges)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Reason == "repo_dirty" {
			writeJSON(c, http.StatusConflict, map[string]any{"code": "repo_dirty", "error": msg("仓库有未提交或未推送的改动，需要明确确认丢弃后才能卸下").In(requestLang(c.Request))})
		} else {
			writeWorkspaceError(c, err, http.StatusBadGateway)
		}
		return
	}
	if sess.ID != "" {
		if _, err := s.updateSession(id.TenantID, sess, func(x *Session) { delete(x.RepoStates, name) }); err != nil {
			writeErr(c, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(c, http.StatusOK, out)
}
