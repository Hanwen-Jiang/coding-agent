package server

// 一次 Git 操作的授权（G-03，docs/plan/github-app.md 第 3 节“一次操作的授权”）。经 GitHub App 授权的连接：
// 取用户令牌（按用户串行续期，续期令牌是一次性的）→ 用用户令牌确认他对这个仓库有所需的权限（读：克隆、拉取；写：推送、开 PR；
// 结果缓存不超过 60 秒）→ 用 App 的 JWT 找到这个仓库的安装、核对没被暂停 → 签一个只含这个仓库、只给这次要的权限的安装令牌
// → 经 Workspace API 传入（用户名 x-access-token）→ 操作结束吊销，吊销失败不影响结果。令牌不落盘、不进日志。
// 开 PR 用用户令牌（记在这个人名下，2026-11-03 人定）。旧 OAuth App 的连接在切换期内照旧用它的令牌（第 5 节第 3 条）。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"
)

type repoAccess int

const (
	repoRead repoAccess = iota
	repoWrite
)

// repoGrant 是一次操作拿到的授权：Auth 给 Workspace API，UserToken 给要记在用户名下的调用（开 PR），Conn 是这一家的连接
// （没连接时为零值），Done 在操作结束时调用。
type repoGrant struct {
	Auth      GitAuth
	UserToken string
	Conn      Connection
	Done      func()
}

func noop() {}

// permEntry 是用户对一个仓库的权限（缓存不超过 permTTL）。
type permEntry struct {
	id         int64
	name       string
	pull, push bool
	private    bool
	at         time.Time
}

const permTTL = 60 * time.Second

// githubState 是 App 一侧的进程内状态：App 的 JWT（复用到快过期）、权限缓存、按用户的续期锁。
type githubState struct {
	mu       sync.Mutex
	jwt      string
	jwtExp   time.Time
	perms    map[string]permEntry
	refreshL sync.Map // 租户/用户/平台 → *sync.Mutex
	// triggerMu 护着自动触发的记录文件（G-06，githubtrigger.go）。
	triggerMu sync.Mutex
}

func (s *Server) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// appJWT 返回 App 的 JWT：还有一分钟以上才过期就复用，否则签新的（AppJWT 签 9 分钟）。
func (s *Server) appJWT() (string, error) {
	g := &s.github
	g.mu.Lock()
	defer g.mu.Unlock()
	now := s.now()
	if g.jwt != "" && g.jwtExp.Sub(now) > time.Minute {
		return g.jwt, nil
	}
	token, err := s.cfg.GitHubApp.AppJWT(now)
	if err != nil {
		return "", err
	}
	g.jwt, g.jwtExp = token, now.Add(9*time.Minute)
	return token, nil
}

func (s *Server) refreshLock(key string) *sync.Mutex {
	l, _ := s.github.refreshL.LoadOrStore(key, &sync.Mutex{})
	return l.(*sync.Mutex)
}

// viaApp 是这个连接经 GitHub App 授权（新连接），而不是旧 OAuth App 的。
func (s *Server) viaApp(c Connection) bool {
	return c.Provider == "github" && c.App && s.cfg.GitHubApp != nil
}

// legacyGitHub 是这个账号还连着旧 OAuth App 的 GitHub（切换期内照用，界面提示重新连接）。
func (s *Server) legacyGitHub(id identity) bool {
	if s.cfg.GitHubApp == nil {
		return false
	}
	a, err := s.readAccount(id.TenantID, id.UserID)
	if err != nil {
		return false
	}
	c, ok := a.Connections["github"]
	return ok && !c.App
}

// authorizeRepo 为这个工作区的仓库要一次 need 级别的授权；p 的 RepoID、InstallationID 与改名后的 Repo 会就地补上并存回。
// 没连接时：读按公开仓库处理（不带凭据），写返回 errNotConnected。
func (s *Server) authorizeRepo(r *http.Request, id identity, p *Project, need repoAccess) (repoGrant, error) {
	prov := s.providers[p.Provider]
	if prov == nil || p.Repo == "" {
		return repoGrant{Done: noop}, nil
	}
	// Webhook 记下的远端状况（G-04）：不正常时不签发，提示写清原因。卸载、移除仓库之后不直接拒绝：重新安装（安装 ID 会变）
	// 或加回仓库的事件可能丢了，下面按 API 再查一次这个仓库现在的安装，查不到才拒（事件只当提示）。
	removed := map[string]*Msg{
		"app_removed":  msg("GitHub App 已从 %s 卸载：重新安装后才能操作这个仓库", p.Repo),
		"repo_removed": msg("%s 已从 GitHub App 的安装里移除：在 GitHub 上把它加回安装后才能操作，工作区还在", p.Repo),
	}[p.RemoteState]
	switch p.RemoteState {
	case "app_suspended":
		return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusForbidden, Note: msg("GitHub App 在 %s 上的安装已暂停，暂时不能操作这个仓库", p.Repo)}
	case "remote_deleted":
		return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusGone, Note: msg("%s 在 GitHub 上已经不存在了，工作区还在", p.Repo)}
	}
	conn, token, err := s.connToken(r, id, prov)
	if errors.Is(err, errNotConnected) {
		if need == repoRead {
			return repoGrant{Done: noop}, nil
		}
		return repoGrant{}, errNotConnected
	}
	var ge *GitError
	if errors.As(err, &ge) && ge.Status == http.StatusUnauthorized {
		// 续不上：报要重新授权，不退回成不带凭据（G-03 第 7 条）。
		return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusUnauthorized, Note: msg("%s 的授权过期且续不上：要在“账号与平台”里重新授权", prov.Name)}
	}
	if err != nil {
		return repoGrant{}, err
	}
	if !s.viaApp(conn) {
		if removed != nil {
			return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusForbidden, Note: removed}
		}
		user, pass := prov.gitAuth(conn, token)
		return repoGrant{Auth: GitAuth{Host: prov.Web + "/", User: user, Pass: pass}, UserToken: token, Conn: conn, Done: noop}, nil
	}
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	perm, err := s.repoPermission(ctx, prov, id, token, p)
	if err != nil {
		return repoGrant{}, err
	}
	if !perm.pull {
		return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusForbidden, Note: msg("你在 GitHub 上没有 %s 的读权限", p.Repo)}
	}
	if need == repoWrite && !perm.push {
		return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusForbidden,
			Note: msg("你对 %s 只有读权限：推送与开 PR 要写权限，请仓库的管理员在 GitHub 的 Settings → Collaborators 里调整", p.Repo)}
	}
	jwt, err := s.appJWT()
	if err != nil {
		return repoGrant{}, err
	}
	var inst struct {
		ID          int64   `json:"id"`
		SuspendedAt *string `json:"suspended_at"`
	}
	switch err := prov.api(ctx, jwt, http.MethodGet, "/repos/"+p.Repo+"/installation", nil, &inst); {
	case errors.As(err, &ge) && ge.Status == http.StatusNotFound:
		if removed != nil {
			return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusForbidden, Note: removed}
		}
		if need == repoRead && !perm.private {
			return repoGrant{Conn: conn, UserToken: token, Done: noop}, nil // 公开仓库：不带凭据也能克隆、拉取
		}
		return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusForbidden,
			Note: msg("还没有在 %s 上安装 GitHub App：到 https://github.com/apps/%s/installations/new 安装并选上这个仓库", p.Repo, s.cfg.GitHubApp.Slug)}
	case err != nil:
		return repoGrant{}, err
	}
	if inst.SuspendedAt != nil && *inst.SuspendedAt != "" {
		return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusForbidden, Note: msg("GitHub App 在 %s 上的安装已暂停，暂时不能操作这个仓库", p.Repo)}
	}
	if p.RepoID != perm.id || p.InstallationID != inst.ID {
		p.RepoID, p.InstallationID = perm.id, inst.ID
		s.saveRepoRef(id.TenantID, *p)
	}
	contents := "read"
	if need == repoWrite {
		contents = "write"
	}
	var issued struct {
		Token string `json:"token"`
	}
	body := map[string]any{"repository_ids": []int64{perm.id}, "permissions": map[string]string{"contents": contents}}
	if err := prov.api(ctx, jwt, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID), body, &issued); err != nil {
		s.metrics().Count("coding.github.issue_failures", 1, map[string]string{"use": "operation"})
		return repoGrant{}, err
	}
	if issued.Token == "" {
		return repoGrant{}, &GitError{Provider: prov.Name, Status: http.StatusBadGateway, Note: msg("GitHub 没有签出安装令牌")}
	}
	if removed != nil && p.ID != "" {
		// 签得出来：重新装好了或仓库加回来了。只清这两种标记，不覆盖期间事件记下的别的状况。
		p.RemoteState = ""
		_, _ = s.updateProject(id.TenantID, p.ID, func(x *Project) {
			if x.RemoteState == "app_removed" || x.RemoteState == "repo_removed" {
				x.RemoteState = ""
			}
		})
	}
	done := func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rcancel()
		if err := prov.api(rctx, issued.Token, http.MethodDelete, "/installation/token", nil, nil); err != nil {
			log.Printf("吊销 %s 的安装令牌失败（不影响这次操作，令牌一小时内自己过期）：%v", p.Repo, err)
		}
	}
	return repoGrant{Auth: GitAuth{Host: prov.Web + "/", User: "x-access-token", Pass: issued.Token}, UserToken: token, Conn: conn, Done: done}, nil
}

// repoPermission 用用户令牌查他对这个仓库的权限；有仓库 ID 时先按 ID 取当前的名字（改名、转移之后照常工作）。
func (s *Server) repoPermission(ctx context.Context, prov *Provider, id identity, token string, p *Project) (permEntry, error) {
	if p.RepoID != 0 {
		var cur struct {
			FullName string `json:"full_name"`
		}
		if err := prov.api(ctx, token, http.MethodGet, "/repositories/"+strconv.FormatInt(p.RepoID, 10), nil, &cur); err == nil && cur.FullName != "" && cur.FullName != p.Repo {
			p.Repo = cur.FullName
			s.saveRepoRef(id.TenantID, *p)
		}
	}
	key := id.TenantID + "/" + id.UserID + "/" + p.Repo
	g := &s.github
	g.mu.Lock()
	if e, ok := g.perms[key]; ok && s.now().Sub(e.at) < permTTL {
		g.mu.Unlock()
		return e, nil
	}
	g.mu.Unlock()
	var repo struct {
		ID          int64  `json:"id"`
		FullName    string `json:"full_name"`
		Private     bool   `json:"private"`
		Permissions struct {
			Pull bool `json:"pull"`
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	var ge *GitError
	if err := prov.api(ctx, token, http.MethodGet, "/repos/"+p.Repo, nil, &repo); err != nil {
		if errors.As(err, &ge) && ge.Status == http.StatusNotFound {
			return permEntry{}, &GitError{Provider: prov.Name, Status: http.StatusForbidden, Note: msg("你在 GitHub 上看不到 %s（没有权限，或仓库不存在）", p.Repo)}
		}
		return permEntry{}, err
	}
	e := permEntry{id: repo.ID, name: repo.FullName, pull: repo.Permissions.Pull, push: repo.Permissions.Push, private: repo.Private, at: s.now()}
	g.mu.Lock()
	if g.perms == nil {
		g.perms = map[string]permEntry{}
	}
	g.perms[key] = e
	g.mu.Unlock()
	return e, nil
}

// saveRepoRef 把补上的仓库 ID、安装 ID 与当前名字存回项目；项目还没存（正在建）时什么也不做。
func (s *Server) saveRepoRef(tenant string, p Project) {
	if p.ID == "" {
		return
	}
	_, _ = s.updateProject(tenant, p.ID, func(x *Project) { x.Repo, x.RepoID, x.InstallationID = p.Repo, p.RepoID, p.InstallationID })
}

// handleGitHubSetup 是 App 的 Setup URL（安装或调整之后 GitHub 跳回这里）。installation_id 可能被伪造：用用户令牌列出他能看到的
// 安装，在里面的才关联到这个账号。
func (s *Server) handleGitHubSetup(w http.ResponseWriter, r *http.Request, id identity) {
	fail := func(why *Msg) {
		log.Printf("GitHub App 的 Setup 没有关联（%s/%s）：%s", id.TenantID, id.UserID, why)
		http.Redirect(w, r, "/#/?error="+url.QueryEscape(why.In(requestLang(r))), http.StatusFound)
	}
	prov := s.providers["github"]
	if s.cfg.GitHubApp == nil || prov == nil {
		fail(msg("没有配置 GitHub App"))
		return
	}
	inst, err := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
	if err != nil || inst <= 0 {
		fail(msg("安装编号不对"))
		return
	}
	_, token, err := s.connToken(r, id, prov)
	if err != nil {
		fail(msg("先在“账号与平台”里连接 GitHub，再安装 App"))
		return
	}
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	found := false
	for page := 1; page <= 10 && !found; page++ {
		var out struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		if err := prov.api(ctx, token, http.MethodGet, fmt.Sprintf("/user/installations?per_page=100&page=%d", page), nil, &out); err != nil {
			fail(msg("读不到你的 GitHub App 安装：%v", err))
			return
		}
		for _, x := range out.Installations {
			found = found || x.ID == inst
		}
		if len(out.Installations) < 100 {
			break
		}
	}
	if !found {
		fail(msg("这个安装不在你的 GitHub 账号能看到的范围里，没有关联"))
		return
	}
	if _, err := s.updateAccount(id.TenantID, id.UserID, func(a *Account) error {
		if !slices.Contains(a.GitHubInstallations, inst) {
			a.GitHubInstallations = append(a.GitHubInstallations, inst)
		}
		return nil
	}); err != nil {
		fail(msg("保存安装失败：%v", err))
		return
	}
	http.Redirect(w, r, "/#/?github=installed", http.StatusFound)
}
