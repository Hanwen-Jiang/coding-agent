package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agentruntime/coding-agent/internal/samples"
)

const (
	maxDiffBytes = 2 << 20
	maxFileBytes = 512 << 10
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newProjectID 由名字派生项目 id（也是 Runtime 的 workspace id），后面加 4 位随机数避免重名。
func newProjectID(name string) string {
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "project"
	}
	return base + "-" + randomHex(2)
}

func newEnvironmentID(name string) string {
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "environment"
	}
	return "env-" + base + "-" + randomHex(2)
}

// sampleView 是示例在网页上的样子：文字按请求的语言。
type sampleView struct {
	ID string `json:"id"`
	samples.Text
}

func (s *Server) handleSamples(c *gin.Context, _ identity) {
	lang := requestLang(c.Request)
	out := make([]sampleView, 0, len(samples.All))
	for _, sm := range samples.All {
		out = append(out, sampleView{ID: sm.ID, Text: sm.In(lang)})
	}
	writeJSON(c, http.StatusOK, out)
}

func (s *Server) handleListProjects(c *gin.Context, id identity) {
	projects, err := s.store.Projects(id.TenantID)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	if projects == nil {
		projects = []Project{}
	}
	slices.SortFunc(projects, func(a, b Project) int { return b.CreatedAt.Compare(a.CreatedAt) })
	writeJSON(c, http.StatusOK, projects)
}

// author 是 coding-web 自己做的提交（初始提交、页面上点的提交）的作者。
func author(id identity) (string, string) {
	return id.UserID, id.UserID + "@" + id.TenantID + ".coding.local"
}

func (s *Server) handleCreateProject(c *gin.Context, id identity) {
	var req struct {
		Layout string `json:"layout"`
		Name   string `json:"name"`
		GitURL string `json:"git_url"`
		Branch string `json:"branch"`
		Sample string `json:"sample"`
	}
	if !readJSON(c, &req) {
		return
	}
	req.Name, req.GitURL, req.Branch = strings.TrimSpace(req.Name), strings.TrimSpace(req.GitURL), strings.TrimSpace(req.Branch)
	if req.Name == "" || len(req.Name) > 80 {
		writeMsg(c, http.StatusBadRequest, "项目名要有，且不超过 80 个字符")
		return
	}
	if req.Layout != "" && req.Layout != "WORKSPACE_LAYOUT_ROOT" && req.Layout != layoutMulti {
		writeMsg(c, http.StatusBadRequest, "工作区布局不合法")
		return
	}
	if req.Layout == layoutMulti && (req.GitURL != "" || req.Branch != "" || req.Sample != "") {
		writeMsg(c, http.StatusBadRequest, "多仓工作区先建空容器，再逐个挂仓库")
		return
	}
	if req.Layout == layoutMulti {
		if _, ok := s.ws.(multiWorkspaces); !ok {
			writeMsg(c, http.StatusBadRequest, "多仓工作区需要 Runtime Workspace API")
			return
		}
	}
	p := Project{Layout: req.Layout, ID: workspaceResourceID(c.Request.Context(), "project", newProjectID(req.Name)), Name: req.Name, Source: "empty", CreatedAt: workspaceCreated(c.Request.Context()), CreatedBy: id.UserID}
	spec := CreateSpec{}
	spec.AuthorName, spec.AuthorEmail = author(id)
	switch {
	case req.Layout == layoutMulti:
		spec.Layout = layoutMulti
	case req.GitURL != "":
		u, err := url.Parse(req.GitURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			writeMsg(c, http.StatusBadRequest, "只支持 https 的 Git 地址，且地址里不能带用户名或密码")
			return
		}
		if req.Branch != "" && (strings.HasPrefix(req.Branch, "-") || strings.ContainsAny(req.Branch, " \t\n~^:?*[\\")) {
			writeMsg(c, http.StatusBadRequest, "分支名不合法")
			return
		}
		p.Source = req.GitURL
		spec.RepoURL, spec.Ref = req.GitURL, req.Branch
		// 已配置的 Git 平台上的仓库：记下平台与仓库路径；连接了这一家就带令牌克隆（私有仓库要），令牌只用这一次。
		if prov, repo := s.providerOfURL(req.GitURL); prov != nil {
			p.Provider, p.Repo = prov.id, repo
			// 经 GitHub App 时是只读、只含这个仓库的安装令牌，克隆完吊销（G-03）。
			grant, err := s.authorizeRepo(c.Request, id, &p, repoRead)
			if err != nil {
				writeGitError(c, err)
				return
			}
			defer grant.Done()
			spec.Auth = grant.Auth
		}
	case req.Sample != "":
		files, err := samples.Files(req.Sample)
		if _, ok := samples.Find(req.Sample); !ok || err != nil {
			writeMsg(c, http.StatusBadRequest, "没有这个示例")
			return
		}
		p.Source = "sample:" + req.Sample
		for _, f := range files {
			spec.Files = append(spec.Files, InitialFile{Path: f.Path, Content: f.Content})
		}
	default:
		spec.Files = []InitialFile{{Path: "README.md", Content: "# " + req.Name + "\n"}}
	}
	ctx, cancel := ctxTimeout(c.Request, 10*time.Minute)
	defer cancel()
	info, err := s.ws.Create(ctx, id, p.ID, spec)
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		writeAPIError(c, err) // Workspace API 的说明原样转出
		return
	case err != nil:
		writeMsg(c, statusOf(err, http.StatusBadRequest), "建项目失败：%v", err)
		return
	}
	p.BaseBranch = info.Branch
	if err := s.store.UpdateProjects(id.TenantID, func(ps []Project) ([]Project, error) {
		for _, existing := range ps {
			if existing.ID == p.ID {
				return ps, nil
			}
		}
		return append(ps, p), nil
	}); err != nil {
		_ = s.ws.Delete(ctx, id, p.ID)
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	writeJSON(c, http.StatusCreated, p)
}

// handleDeleteProject 删掉工作区：基线仓库、所有会话的工作副本与记录。
func (s *Server) handleDeleteProject(c *gin.Context, id identity) {
	p, ok := s.project(c, id)
	if !ok {
		return
	}
	var sessions []Session
	var err error
	if !workspaceFact(c.Request.Context(), "delete_sessions", &sessions) {
		sessions, err = s.projectSessions(id.TenantID, p.ID)
		if err == nil {
			saveWorkspaceFact(c.Request.Context(), "delete_sessions", sessions)
		}
	}
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	for _, x := range sessions {
		if s.busy(c.Request, id, x.WorkspaceID) {
			writeMsg(c, http.StatusConflict, "还有任务在跑，等它结束或先取消")
			return
		}
	}
	ctx, cancel := ctxTimeout(c.Request, 5*time.Minute)
	defer cancel()
	for _, ws := range append(sessionWorkspaces(p, sessions), p.ID) {
		if err := s.deleteWorkspace(ctx, id, ws); err != nil {
			writeWorkspaceError(c, err, http.StatusInternalServerError)
			return
		}
	}
	if err := s.store.DeleteProject(id.TenantID, p.ID); err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]bool{"ok": true})
}

// safePath 把请求里的相对路径解析到工作区里；.git、绝对路径、..、反斜杠、经符号链接出界的一律拒绝（local 用）。
func safePath(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return root, nil
	}
	if strings.ContainsAny(rel, "\\\x00") {
		return "", msg("路径不合法")
	}
	clean := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if clean == "" {
		return root, nil
	}
	if slices.Contains(strings.Split(clean, "/"), ".git") {
		return "", msg("不能访问 .git")
	}
	target := filepath.Join(root, filepath.FromSlash(clean))
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	if within, _ := filepath.Rel(realRoot, realTarget); within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", msg("路径不在工作区里")
	}
	return target, nil
}

// cleanRel 把网页给的路径整理成 Workspace API 的写法（相对、/ 分隔、没有 . 段）；.git 与出界的路径拒绝。
func cleanRel(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return "", nil
	}
	if strings.ContainsAny(rel, "\\\x00") {
		return "", msg("路径不合法")
	}
	clean := strings.TrimPrefix(path.Clean("/"+rel), "/")
	if slices.Contains(strings.Split(clean, "/"), ".git") {
		return "", msg("不能访问 .git")
	}
	return clean, nil
}

func (s *Server) handleTree(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	rel, err := cleanRel(c.Query("path"))
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	out, err := s.ws.List(ctx, id, sess.WorkspaceID, rel)
	if err != nil {
		writeWorkspaceError(c, err, http.StatusInternalServerError)
		return
	}
	slices.SortFunc(out, func(a, b FileEntry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	writeJSON(c, http.StatusOK, out)
}

func (s *Server) handleFile(c *gin.Context, id identity) {
	_, sess, ok := s.session(c, id)
	if !ok {
		return
	}
	rel, err := cleanRel(c.Query("path"))
	if err != nil || rel == "" {
		writeMsg(c, http.StatusBadRequest, "路径不合法")
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	f, err := s.ws.Read(ctx, id, sess.WorkspaceID, rel)
	if err != nil {
		writeWorkspaceError(c, err, http.StatusInternalServerError)
		return
	}
	switch {
	case f.TooLarge:
		writeJSON(c, http.StatusOK, map[string]any{"too_large": true, "size": f.Size})
	case f.Binary:
		writeJSON(c, http.StatusOK, map[string]any{"binary": true, "size": f.Size})
	default:
		writeJSON(c, http.StatusOK, map[string]any{"content": f.Content, "size": f.Size})
	}
}

func (s *Server) handleLog(c *gin.Context, id identity) {
	id, _, sess, ok := s.gitSession(c, id, false)
	if !ok {
		return
	}
	// 默认最近 30 个，“显示更早的”时多取；Workspace API 最多给 200 个（GetLog 没有游标：窗口就是最近的 limit 个）。
	limit := 30
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 {
		limit = min(n, 200)
	}
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	commits, err := s.ws.Log(ctx, id, sess.WorkspaceID, limit, "")
	if err != nil {
		writeWorkspaceError(c, err, http.StatusInternalServerError)
		return
	}
	writeJSON(c, http.StatusOK, commits)
}

func (s *Server) handleChanges(c *gin.Context, id identity) {
	id, _, sess, ok := s.gitSession(c, id, false)
	if !ok {
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	changes, err := s.ws.Changes(ctx, id, sess.WorkspaceID)
	if err != nil {
		writeWorkspaceError(c, err, http.StatusInternalServerError)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"head": changes.Head, "branch": changes.Branch, "files": changes.Files, "diff": changes.Diff, "truncated": changes.Truncated})
}

func (s *Server) handlePatch(c *gin.Context, id identity) {
	id, _, sess, ok := s.gitSession(c, id, false)
	if !ok {
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	changes, err := s.ws.Changes(ctx, id, sess.WorkspaceID)
	if err != nil {
		writeWorkspaceError(c, err, http.StatusInternalServerError)
		return
	}
	c.Header("Content-Type", "text/x-patch; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", sess.WorkspaceID+".patch"))
	_, _ = c.Writer.Write([]byte(changes.Diff))
}

func (s *Server) handleCommit(c *gin.Context, id identity) {
	id, _, sess, ok := s.gitSession(c, id, true)
	if !ok {
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if !readJSON(c, &req) {
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "Apply agent changes"
	}
	if s.busy(c.Request, id, sess.WorkspaceID) {
		writeMsg(c, http.StatusConflict, "还有任务在跑，等它结束再提交")
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	name, email := author(id)
	head, committed, err := s.ws.Commit(ctx, id, sess.WorkspaceID, message, name, email)
	if err != nil {
		writeWorkspaceError(c, err, http.StatusInternalServerError)
		return
	}
	if !committed {
		writeMsg(c, http.StatusBadRequest, "没有可提交的改动")
		return
	}
	writeJSON(c, http.StatusOK, map[string]string{"head": head})
}

func (s *Server) handleDiscard(c *gin.Context, id identity) {
	id, _, sess, ok := s.gitSession(c, id, false)
	if !ok {
		return
	}
	if s.busy(c.Request, id, sess.WorkspaceID) {
		writeMsg(c, http.StatusConflict, "还有任务在跑，等它结束或先取消")
		return
	}
	ctx, cancel := ctxTimeout(c.Request, 2*time.Minute)
	defer cancel()
	if err := s.ws.Discard(ctx, id, sess.WorkspaceID); err != nil {
		writeWorkspaceError(c, err, http.StatusInternalServerError)
		return
	}
	writeJSON(c, http.StatusOK, map[string]bool{"ok": true})
}
