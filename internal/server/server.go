// Package server 是 coding-web 的 HTTP 服务：JSON API 与内嵌的网页。
//
// 租户与用户只来自登录得到的 Runtime 令牌（放在 HttpOnly Cookie 里），每个请求都重新校验签名与过期时间；
// 项目与任务的元数据按租户分目录存放；工作区的 id 一律由服务端按（项目、会话）拼出，文件经 Workspaces（默认是 Runtime 的
// Workspace API，租户按令牌定）读写，请求给不出别的工作区。
// 改写操作要带 X-Coding-Web 头（配合 SameSite Cookie 防跨站请求）。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/dev/localingress"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
	"github.com/agentruntime/coding-agent/internal/web"
)

const (
	cookieName  = "cw_token"
	csrfHeader  = "X-Coding-Web"
	tokenTTL    = 12 * time.Hour
	maxBodySize = 1 << 20
)

// slug 与 Runtime 的租户、workspace 格式一致（libs/go-core/dev/localingress、Worker 的工作区工具）。
var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Config 是 coding-web 的配置，见 cmd/coding-web 的说明。
type Config struct {
	TaskAPI string
	// Workspaces 是工作区在哪里（见 Workspaces）：runtime（默认，经 Runtime 的 Workspace API）或 local（本机的
	// WorkspaceRoot 下自己跑 git，要与 Worker 共用这块盘）。WorkspaceRoot 只有 local 要。
	Workspaces    string
	WorkspaceRoot string
	DataDir       string
	DevLogin      bool
	SigningKey    []byte
	PoliciesFile  string
	Git           string
	SecureCookie  bool
	// 登录方式（github、gitee、gitlab、gitea、bitbucket、google，见 oauth.go）：Client ID 与 Secret 都给了才打开；
	// PublicURL 用来拼回调地址 <PublicURL>/api/auth/<平台>/callback。GitHub 也可以用下面旧的 GitHub* 字段配置。
	Providers          map[string]ProviderConfig
	GitHubClientID     string
	GitHubClientSecret string
	GitHubURL          string
	GitHubAPI          string
	GitHubScopes       string
	PublicURL          string
	// AllowedLogins 是放行的平台账号（"<平台>:<登录名>"，不分大小写）：给了就只有它们（与关联到它们的别家账号）能用平台登录，
	// 线上还没对外开放时用；空表示谁都能登录。
	AllowedLogins []string
	// Worker 的 shell_run 在哪里跑（docker 或 host）与 docker 时的镜像；只用来在界面上说明环境。
	Sandbox      string
	SandboxImage string
}

// Server 是 coding-web。
type Server struct {
	cfg       Config
	ingress   *localingress.Provider
	store     *Store
	api       *TaskAPI
	git       Git
	ws        Workspaces
	wsLocks   sync.Map // 工作区 id → *sync.Mutex（workspaceLock）
	providers map[string]*Provider
	allowed   map[string]bool
	// handoff 把会话选定的环境配置交给 Runtime（envhandoff.go；ENV-02 的契约定下之前不发）。
	handoff environmentHandoff
	// background 是请求返回后还在跑的后台工作（任务开始时补记提交）：测试收尾时等它们做完再删临时目录。
	background sync.WaitGroup
}

// New 校验配置并构造 Server。
func New(cfg Config) (*Server, error) {
	if cfg.Workspaces == "" {
		cfg.Workspaces = "runtime"
	}
	switch {
	case cfg.TaskAPI == "":
		return nil, errors.New("缺少 CODING_TASK_API")
	case cfg.Workspaces != "runtime" && cfg.Workspaces != "local":
		return nil, errors.New("CODING_WORKSPACES 只能是 runtime 或 local")
	case cfg.Workspaces == "local" && cfg.WorkspaceRoot == "":
		return nil, errors.New("CODING_WORKSPACES=local 时要给 CODING_WORKSPACE_ROOT")
	case cfg.DataDir == "":
		return nil, errors.New("缺少 CODING_DATA_DIR")
	case len(cfg.SigningKey) == 0:
		return nil, errors.New("缺少 AR_DEV_SIGNING_KEY（校验与签发 Runtime 令牌）")
	}
	if cfg.Git == "" {
		cfg.Git = "git"
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	hooks := filepath.Join(cfg.DataDir, "empty-hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return nil, err
	}
	providers := newProviders(cfg)
	if len(providers) > 0 && cfg.PublicURL == "" {
		return nil, errors.New("打开第三方登录要给 CODING_PUBLIC_URL（拼 OAuth 回调地址）")
	}
	allowed := map[string]bool{}
	for _, entry := range cfg.AllowedLogins {
		provider, login, ok := strings.Cut(strings.ToLower(strings.TrimSpace(entry)), ":")
		if _, known := specOf(provider); !ok || login == "" || !known {
			return nil, fmt.Errorf("CODING_ALLOWED_LOGINS 的 %q 不是 <平台>:<登录名>", entry)
		}
		allowed[provider+":"+login] = true
	}
	api := NewTaskAPI(strings.TrimRight(cfg.TaskAPI, "/"))
	s := &Server{
		allowed:   allowed,
		cfg:       cfg,
		ingress:   localingress.New(localingress.Options{SigningKey: cfg.SigningKey, Limit: 1 << 30, Window: time.Minute}),
		store:     NewStore(cfg.DataDir),
		api:       api,
		git:       Git{Bin: cfg.Git, HooksDir: hooks},
		ws:        runtimeWorkspaces{api: api},
		providers: providers,
		handoff:   pendingHandoff{},
	}
	if cfg.Workspaces == "local" {
		if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
			return nil, err
		}
		s.ws = localWorkspaces{root: cfg.WorkspaceRoot, git: &s.git}
	}
	return s, nil
}

// Handler 返回挂好全部路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.authed(s.handleMe))
	// 第三方登录与账号关联（accounts.go）。
	mux.HandleFunc("GET /api/auth/{provider}/login", s.handleOAuthLogin)
	mux.HandleFunc("GET /api/auth/{provider}/callback", s.handleOAuthCallback)
	mux.HandleFunc("GET /api/account", s.authed(s.handleAccount))
	mux.HandleFunc("POST /api/account/connections/{provider}/disconnect", s.authed(s.handleDisconnect))
	mux.HandleFunc("GET /api/git/{provider}/repos", s.authed(s.handleRepos))
	mux.HandleFunc("GET /api/github/repos", s.authed(s.handleRepos))

	mux.HandleFunc("GET /api/samples", s.authed(s.handleSamples))
	// 环境配置与项目解耦；这里仅保存普通配置，不报告 Runtime 生命周期状态。
	mux.HandleFunc("GET /api/environments", s.authed(s.handleListEnvironments))
	mux.HandleFunc("POST /api/environments", s.authed(s.handleCreateEnvironment))
	mux.HandleFunc("GET /api/environments/{environment}", s.authed(s.handleGetEnvironment))
	mux.HandleFunc("PUT /api/environments/{environment}", s.authed(s.handleUpdateEnvironment))
	mux.HandleFunc("PATCH /api/environments/{environment}", s.authed(s.handleUpdateEnvironment))
	mux.HandleFunc("DELETE /api/environments/{environment}", s.authed(s.handleDeleteEnvironment))
	// 工作区（项目）：基线仓库。
	mux.HandleFunc("GET /api/projects", s.authed(s.handleListProjects))
	mux.HandleFunc("POST /api/projects", s.authed(s.handleCreateProject))
	mux.HandleFunc("DELETE /api/projects/{project}", s.authed(s.handleDeleteProject))
	// 会话：一段对话加一份工作副本；文件、改动、提交、推送与任务都按会话。
	mux.HandleFunc("GET /api/sessions", s.authed(s.handleListSessions))
	mux.HandleFunc("POST /api/projects/{project}/sessions", s.authed(s.handleCreateSession))
	const sess = "/api/projects/{project}/sessions/{session}"
	mux.HandleFunc("DELETE "+sess, s.authed(s.handleDeleteSession))
	mux.HandleFunc("POST "+sess+"/rename", s.authed(s.handleRenameSession))
	mux.HandleFunc("GET "+sess+"/env", s.authed(s.handleEnv))
	mux.HandleFunc("GET "+sess+"/tree", s.authed(s.handleTree))
	mux.HandleFunc("GET "+sess+"/file", s.authed(s.handleFile))
	mux.HandleFunc("GET "+sess+"/log", s.authed(s.handleLog))
	mux.HandleFunc("GET "+sess+"/changes", s.authed(s.handleChanges))
	mux.HandleFunc("GET "+sess+"/patch", s.authed(s.handlePatch))
	mux.HandleFunc("POST "+sess+"/commit", s.authed(s.handleCommit))
	mux.HandleFunc("POST "+sess+"/discard", s.authed(s.handleDiscard))
	mux.HandleFunc("POST "+sess+"/publish", s.authed(s.handlePublish))
	mux.HandleFunc("POST "+sess+"/reset-branch", s.authed(s.handleResetBranch))
	mux.HandleFunc("GET "+sess+"/tasks", s.authed(s.handleListTasks))
	mux.HandleFunc("POST "+sess+"/tasks", s.authed(s.handleSubmitTask))
	mux.HandleFunc("GET "+sess+"/tasks/{task}", s.authed(s.handleQueryTask))
	mux.HandleFunc("GET "+sess+"/tasks/{task}/events", s.authed(s.handleEvents))
	mux.HandleFunc("POST "+sess+"/tasks/{task}/reply", s.authed(s.handleReply))
	mux.HandleFunc("POST "+sess+"/tasks/{task}/guidance", s.authed(s.handleGuidance))
	mux.HandleFunc("POST "+sess+"/tasks/{task}/guidance/{guidance}/edit", s.authed(s.handleGuidanceEdit))
	mux.HandleFunc("POST "+sess+"/tasks/{task}/guidance/{guidance}/retract", s.authed(s.handleGuidanceRetract))
	mux.HandleFunc("POST "+sess+"/tasks/{task}/message", s.authed(s.handleEditQueued))
	mux.HandleFunc("POST "+sess+"/tasks/{task}/message/result", s.authed(s.handleQueuedResult))
	mux.HandleFunc("POST "+sess+"/tasks/{task}/cancel", s.authed(s.handleCancel))
	mux.HandleFunc("GET /api/tasks/active", s.authed(s.handleActiveTasks))

	mux.Handle("GET /", web.Handler())
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		// 头像来自各家平台（GitHub、Gitee、GitLab、Gravatar、Google…），图片放开 https；脚本、样式、连接仍只许同源。
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; style-src 'self'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

// --- 认证 ---

type identity struct {
	ingress.Identity
	token string
}

type authedHandler func(w http.ResponseWriter, r *http.Request, id identity)

func (s *Server) authed(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil || cookie.Value == "" {
			writeMsg(w, r, http.StatusUnauthorized, "请先登录")
			return
		}
		id, err := s.ingress.Authenticate(r.Context(), ingress.Credentials{BearerToken: cookie.Value})
		if err != nil {
			s.clearCookie(w)
			writeMsg(w, r, http.StatusUnauthorized, "登录已过期，请重新登录")
			return
		}
		if r.Method != http.MethodGet && r.Header.Get(csrfHeader) == "" {
			writeMsg(w, r, http.StatusForbidden, "缺少 X-Coding-Web 头")
			return
		}
		h(w, r, identity{Identity: id, token: cookie.Value})
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	// 全部登录方式都列出来（没配置的 configured 为 false，开发环境的登录页用它说明还能接哪些）。
	providers := []map[string]any{}
	for _, sp := range providerSpecs {
		item := map[string]any{"id": sp.id, "name": sp.name, "git": sp.git, "configured": false, "url": sp.web}
		if p := s.providers[sp.id]; p != nil {
			item["name"], item["configured"], item["url"] = p.Name, true, p.Web
			if p.git {
				item["request_name"] = p.requestName()
			}
		}
		providers = append(providers, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dev_login": s.cfg.DevLogin, "github_login": s.providers["github"] != nil, "providers": providers,
		// 有开发租户策略文件才能按任务设沙箱等级（见 runsettings.go）。
		"sandbox_control": true, "sandbox": s.cfg.Sandbox,
	})
}

// setSession 签发 Runtime 令牌，放进 HttpOnly Cookie。
func (s *Server) setSession(w http.ResponseWriter, tenant, user string) {
	token := s.ingress.IssueToken(tenant, user, tokenTTL)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode, MaxAge: int(tokenTTL.Seconds()),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevLogin {
		writeMsg(w, r, http.StatusForbidden, "没有打开开发登录（CODING_DEV_LOGIN=1）")
		return
	}
	if r.Header.Get(csrfHeader) == "" {
		writeMsg(w, r, http.StatusForbidden, "缺少 X-Coding-Web 头")
		return
	}
	var req struct {
		Tenant string `json:"tenant"`
		User   string `json:"user"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Tenant, req.User = strings.ToLower(strings.TrimSpace(req.Tenant)), strings.ToLower(strings.TrimSpace(req.User))
	if !slug.MatchString(req.Tenant) || !slug.MatchString(req.User) {
		writeMsg(w, r, http.StatusBadRequest, "租户与用户名只能用小写字母、数字、- 与 _，以字母或数字开头")
		return
	}
	if reservedTenant(req.Tenant) {
		writeMsg(w, r, http.StatusBadRequest, "gh-、gitee-、gl-、gitea-、bb-、google- 开头的租户留给第三方登录")
		return
	}
	if err := s.ensureTenantPolicy(req.Tenant); err != nil {
		writeMsg(w, r, http.StatusInternalServerError, "写租户策略失败：%v", err)
		return
	}
	s.setSession(w, req.Tenant, req.User)
	writeJSON(w, http.StatusOK, map[string]string{"tenant": req.Tenant, "user": req.User})
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	s.clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, id identity) {
	me := map[string]any{"tenant": id.TenantID, "user": id.UserID, "expires_at": id.ExpiresAt}
	a, conns := s.connections(id)
	me["connections"] = conns
	for _, c := range conns {
		if c.Provider == a.Primary || me["avatar_url"] == nil {
			me["name"], me["avatar_url"], me["login"] = c.DisplayName, c.AvatarURL, c.Login
		}
		if c.Provider == "github" { // 旧的网页与测试读这个
			me["github"] = map[string]any{"login": c.Login, "name": c.DisplayName, "avatar_url": c.AvatarURL}
		}
	}
	writeJSON(w, http.StatusOK, me)
}

// ensureTenantPolicy 在开发租户策略文件里给新租户 write_reversible 的 effect 上限（改代码、跑测试要用；每个任务再经
// effect_ceiling 往下调）。已经列出的租户不动。只用于本机开发：集群里租户策略经 Runtime 的 Registry 发布。
func (s *Server) ensureTenantPolicy(tenant string) error {
	if s.cfg.PoliciesFile == "" {
		return nil
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	doc, err := s.readPolicies()
	if err != nil {
		return err
	}
	tenants, _ := doc["tenants"].(map[string]any)
	if tenants == nil {
		tenants = map[string]any{}
	}
	if _, ok := tenants[tenant]; ok {
		return nil
	}
	tenants[tenant] = map[string]any{"effect_ceiling": defaultSandbox}
	doc["tenants"] = tenants
	return writeFileAtomic(s.cfg.PoliciesFile, doc)
}

func (s *Server) readPolicies() (map[string]any, error) {
	doc := map[string]any{}
	data, err := os.ReadFile(s.cfg.PoliciesFile)
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, msg("租户策略文件格式不对：%v", err)
	}
	return doc, nil
}

// tenantCeiling 是策略文件里这个租户现在的 effect 上限；读不到时为空。
func (s *Server) tenantCeiling(tenant string) string {
	if s.cfg.PoliciesFile == "" {
		return ""
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	doc, err := s.readPolicies()
	if err != nil {
		return ""
	}
	tenants, _ := doc["tenants"].(map[string]any)
	entry, _ := tenants[tenant].(map[string]any)
	ceiling, _ := entry["effect_ceiling"].(string)
	return ceiling
}

// --- 小工具 ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeMsg(w, r, http.StatusBadRequest, "请求体不是合法的 JSON")
		return false
	}
	return true
}

// project 取出请求路径里的项目，确认它属于这个租户。
func (s *Server) project(w http.ResponseWriter, r *http.Request, id identity) (Project, bool) {
	pid := r.PathValue("project")
	if !slug.MatchString(pid) {
		writeMsg(w, r, http.StatusNotFound, "项目不存在")
		return Project{}, false
	}
	p, ok, err := s.store.Project(id.TenantID, pid)
	if err != nil {
		writeErr(w, r, http.StatusInternalServerError, err)
		return Project{}, false
	}
	if !ok {
		writeMsg(w, r, http.StatusNotFound, "项目不存在")
		return Project{}, false
	}
	return p, true
}

func ctxTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
