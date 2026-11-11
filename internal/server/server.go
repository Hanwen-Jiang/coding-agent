// Package server 是 coding-web 的 HTTP 服务：JSON API 与内嵌的网页。
//
// 租户与用户只来自登录得到的 Runtime 令牌（放在 HttpOnly Cookie 里），每个请求都重新校验签名与过期时间；
// 项目与任务的元数据按租户分目录存放；工作区的 id 一律由服务端按（项目、会话）拼出，文件经 Workspaces（默认是 Runtime 的
// Workspace API，租户按令牌定）读写，请求给不出别的工作区。
// 改写操作要带 X-Coding-Web 头（配合 SameSite Cookie 防跨站请求）。
package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/adapters/jwtingress"
	"github.com/agentruntime/agent-runtime/libs/go-core/dev/localingress"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/cache"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
	"github.com/agentruntime/coding-agent/internal/entry"
	"github.com/agentruntime/coding-agent/internal/github"
)

const (
	cookieName  = "cw_token"
	csrfHeader  = "X-Coding-Web"
	maxBodySize = 1 << 20
)

// slug 与 Runtime 的租户、workspace 格式一致（libs/go-core/dev/localingress、Worker 的工作区工具）。
var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Config 是 coding-web 的配置，见 cmd/coding-web 的说明。
type Config struct {
	EntryStore entry.Store
	EntryMail  entry.Sink
	// RuntimeTaskBudget 为真时提交 Task 带 budgetUsdMicros（要 Runtime 支持，agent-runtime PR 3）。
	// 默认不带：旧的 Runtime 不认这个字段会拒绝提交；单任务上限仍由轮询时的 CancelTask 兜底。
	RuntimeTaskBudget bool
	TaskAPI           string
	// Workspaces 是工作区在哪里（见 Workspaces）：runtime（默认，经 Runtime 的 Workspace API）或 local（本机的
	// WorkspaceRoot 下自己跑 git，要与 Worker 共用这块盘）。WorkspaceRoot 只有 local 要。
	Workspaces    string
	WorkspaceRoot string
	DataDir       string
	DevLogin      bool
	SigningKey    []byte
	// JWTKey、JWTKeyID、JWTIssuer（AUTH-01）：给了 Ed25519 私钥就用它签 Runtime 令牌（标准 JWT，iss 默认 coding-agent），
	// Runtime 只持对应的公钥；SigningKey 的开发令牌不再签发、也不再认。TokenKey 是第三方平台令牌的加密密钥（32 字节），
	// 与签名密钥无关；没给时沿用由 SigningKey 派生的旧密钥，两者都给时旧数据读到时用新密钥重新加密。
	JWTKey    ed25519.PrivateKey
	JWTKeyID  string
	JWTIssuer string
	TokenKey  []byte
	// TokenKeyPrevious 是轮换期间的旧 TokenKey（CODING_TOKEN_KEY_PREVIOUS，G-01）：旧数据读到时改用 TokenKey 重新加密。
	TokenKeyPrevious []byte
	// GitHubApp 是 GitHub App 的配置（G-01，githubapp.go）；nil 表示没配，功能关闭。
	GitHubApp *GitHubApp
	// Webhook 是 GitHub App 的收件箱（G-02，internal/github），挂在 POST /api/webhooks/github；配了 App 而它为 nil 时那里回 503。
	Webhook http.Handler
	// Metrics 是 GitHub App 一侧的计数（G-05，经 libs/go-core 的 Telemetry）；nil 时不记。
	Metrics github.Metrics
	// SharedCache is coding-web's own cache namespace; it contains only rebuildable
	// public discovery responses and scoped permission metadata, never credentials.
	// Nil retains the local implementation for development and tests.
	SharedCache cache.Cache
	// NetworkSecrets 打开网络密钥页（WEB-SEC，user_secrets.go）；D-23 第二阶段之前默认关着。
	NetworkSecrets bool
	// SessionIdle、SessionMaxAge 是浏览器会话的空闲与绝对有效期（AUTH-02，browser_sessions.go；默认 24 小时、7 天）。Now 只给测试换时钟。
	SessionIdle   time.Duration
	SessionMaxAge time.Duration
	Now           func() time.Time
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
	// Signup 是开放注册时新注册要满足的条件与封禁名单（signup.go，F4）。
	Signup SignupRules
	// Worker 的 shell_run 在哪里跑（docker 或 host）与 docker 时的镜像；只用来在界面上说明环境。
	Sandbox      string
	SandboxImage string
}

// Server 是 coding-web。
type Server struct {
	entryLeases  sync.Map
	entryAdvance sync.Mutex
	// entryPrunedAt is guarded by entryAdvance.
	entryPrunedAt time.Time
	discovery     *discoveryService
	cfg           Config
	ingress       tokens
	sessions      *sessions
	// github 是 GitHub App 一侧的进程内状态（G-03，githubrepo.go）。
	github    githubState
	store     *Store
	api       *TaskAPI
	git       Git
	ws        Workspaces
	wsJobs    sync.Map // in-process runners for persisted HTTP workspace operations
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
	case len(cfg.SigningKey) == 0 && cfg.JWTKey == nil:
		return nil, errors.New("缺少 AR_DEV_SIGNING_KEY 或 CODING_JWT_KEY_FILE（校验与签发 Runtime 令牌）")
	case cfg.JWTKey != nil && cfg.JWTKeyID == "":
		return nil, errors.New("给了 CODING_JWT_KEY_FILE 也要给 CODING_JWT_KID（与 Runtime 签发方登记里的 kid 一致）")
	case cfg.JWTKey != nil && len(cfg.SigningKey) == 0 && len(cfg.TokenKey) == 0:
		return nil, errors.New("只用 JWT（没有 AR_DEV_SIGNING_KEY）时要给 CODING_TOKEN_KEY（第三方平台令牌的加密密钥）")
	case len(cfg.TokenKey) != 0 && len(cfg.TokenKey) != 32:
		return nil, errors.New("CODING_TOKEN_KEY 要是 32 字节")
	case len(cfg.TokenKeyPrevious) != 0 && (len(cfg.TokenKeyPrevious) != 32 || len(cfg.TokenKey) == 0):
		return nil, errors.New("CODING_TOKEN_KEY_PREVIOUS 要是 32 字节，并且要同时给新的 CODING_TOKEN_KEY")
	}
	tok, err := newTokens(cfg)
	if err != nil {
		return nil, err
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
	if app := cfg.GitHubApp; app != nil && app.clientSecret != "" {
		// G-03：配了 App 的 Client secret 之后，GitHub 的登录与连接都走 App 的 OAuth（用户令牌会过期、可续期）；旧 OAuth App
		// 的连接在切换期内照用（2026-11-03 人定，第 5 节第 3 条）。网页与 API 地址沿用 GitHub 一项的配置。
		providers := map[string]ProviderConfig{}
		for k, v := range cfg.Providers {
			providers[k] = v
		}
		gh := providers["github"]
		if gh.ClientID == "" && cfg.GitHubClientID != "" {
			gh = ProviderConfig{URL: cfg.GitHubURL, APIURL: cfg.GitHubAPI, Scopes: cfg.GitHubScopes}
		}
		gh.ClientID, gh.ClientSecret = app.ClientID, app.clientSecret
		providers["github"] = gh
		cfg.Providers = providers
	}
	providers := newProviders(cfg)
	if len(providers) > 0 && cfg.PublicURL == "" {
		return nil, errors.New("打开第三方登录要给 CODING_PUBLIC_URL（拼 OAuth 回调地址）")
	}
	allowed := map[string]bool{}
	for _, entry := range cfg.AllowedLogins {
		key, ok := loginKey(entry)
		if !ok {
			return nil, fmt.Errorf("CODING_ALLOWED_LOGINS 的 %q 不是 <平台>:<登录名>", entry)
		}
		allowed[key] = true
	}
	for _, entry := range cfg.Signup.Blocked {
		if _, ok := loginKey(entry); !ok {
			return nil, fmt.Errorf("CODING_BLOCKED_LOGINS 的 %q 不是 <平台>:<登录名>", entry)
		}
	}
	api := NewTaskAPI(strings.TrimRight(cfg.TaskAPI, "/"))
	s := &Server{
		discovery: newDiscoveryService(),
		allowed:   allowed,
		cfg:       cfg,
		ingress:   tok,
		sessions:  newSessions(cfg),
		store:     NewStore(cfg.DataDir),
		api:       api,
		git:       Git{Bin: cfg.Git, HooksDir: hooks},
		ws:        runtimeWorkspaces{api: api},
		providers: providers,
		handoff:   pendingHandoff{},
	}
	s.discovery.shared = cfg.SharedCache
	if cfg.Workspaces == "local" {
		if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
			return nil, err
		}
		s.ws = localWorkspaces{root: cfg.WorkspaceRoot, git: &s.git}
	}
	return s, nil
}

// --- 认证 ---

// tokens 签发与校验放进 Cookie、转给 Runtime 的令牌。
type tokens interface {
	ingress.Ingress
	IssueToken(tenant, user string, ttl time.Duration) string
}

// newTokens：给了 JWT 私钥时签 Ed25519 的 JWT、只认自己签的（AUTH-01）；否则是 Runtime 的开发实现（HMAC，与 task-service
// 共用 AR_DEV_SIGNING_KEY）。
func newTokens(cfg Config) (tokens, error) {
	if cfg.JWTKey == nil {
		return localingress.New(localingress.Options{SigningKey: cfg.SigningKey, Limit: 1 << 30, Window: time.Minute}), nil
	}
	issuer := cfg.JWTIssuer
	if issuer == "" {
		issuer = "coding-agent"
	}
	public := jwtingress.EncodePublicKey(cfg.JWTKey.Public().(ed25519.PublicKey))
	verify, err := jwtingress.New(jwtingress.Options{
		Config: jwtingress.Config{Issuers: []jwtingress.Issuer{{Issuer: issuer, Keys: map[string]string{cfg.JWTKeyID: public}}}},
		Limit:  1 << 30, Window: time.Minute,
	})
	if err != nil {
		return nil, err
	}
	return jwtTokens{Provider: verify, signer: jwtingress.NewSigner(issuer, cfg.JWTKeyID, cfg.JWTKey, nil)}, nil
}

type jwtTokens struct {
	*jwtingress.Provider
	signer *jwtingress.Signer
}

// IssueToken 签一个 JWT，每个都带随机的 jti（AUTH-02）；租户与用户已经按 slug 校验过，签不出来时返回空串
// （Runtime 拒绝，用户看到要重新登录）。
func (j jwtTokens) IssueToken(tenant, user string, ttl time.Duration) string {
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return ""
	}
	token, err := j.signer.Issue(jwtingress.Claims{Tenant: tenant, User: user, TTL: ttl, ID: hex.EncodeToString(jti)})
	if err != nil {
		return ""
	}
	return token
}

// identity 是认证过的请求：token 是这个会话现签的 Runtime 短期令牌，session 是会话 id（退出登录用）。
type identity struct {
	ingress.Identity
	token   string
	session string
	repo    string // selected only after tenant-owned Workspace API metadata validation
}

// handleJWKS 发布签 Runtime 令牌用的公钥（AUTH-03）：RFC 8037 的 OKP Ed25519 JWK，只有公钥。Runtime 或网关可以按它登记
// 这个签发方（task-service 的登记文件现在是手写的，keygen 打印同一个公钥）。没有 JWT 私钥时 404。
func (s *Server) handleJWKS(c *gin.Context) {
	if s.cfg.JWTKey == nil {
		writeMsg(c, http.StatusNotFound, "没有配置 JWT 私钥")
		return
	}
	c.Header("Cache-Control", "public, max-age=300")
	writeJSON(c, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "OKP", "crv": "Ed25519", "x": jwtingress.EncodePublicKey(s.cfg.JWTKey.Public().(ed25519.PublicKey)),
		"kid": s.cfg.JWTKeyID, "alg": "EdDSA", "use": "sig",
	}}})
}

// sessionIdentity 是会话对应的身份，带这个会话现签的 Runtime 短期令牌。
func (s *Server) sessionIdentity(sid string, sess browserSession) identity {
	id := ingress.Identity{TenantID: sess.Tenant, UserID: sess.User, ExpiresAt: s.sessions.expiresAt(sess)}
	return identity{Identity: id, token: s.sessions.runtimeToken(sid, sess, s.ingress.IssueToken), session: sid}
}

func (s *Server) handleConfig(c *gin.Context) {
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
	out := map[string]any{
		"dev_login": s.cfg.DevLogin, "github_login": s.providers["github"] != nil, "providers": providers,
		// 有开发租户策略文件才能按任务设沙箱等级（见 runsettings.go）。
		"sandbox_control": true, "sandbox": s.cfg.Sandbox,
	}
	if app := s.cfg.GitHubApp; app != nil {
		// 不是密钥：网页用 slug 拼安装链接（https://github.com/apps/<slug>/installations/new，G-05）。
		out["github_app"] = map[string]string{"slug": app.Slug}
	}
	if s.cfg.NetworkSecrets {
		out["network_secrets"] = true
	}
	writeJSON(c, http.StatusOK, out)
}

// setSession 建一个浏览器会话（browser_sessions.go），会话 id 放进 HttpOnly Cookie；返回会话 id。
func (s *Server) setSession(c *gin.Context, tenant, user string) (string, error) {
	id, err := s.sessions.create(tenant, user)
	if err != nil {
		return "", err
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: cookieName, Value: id, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode, MaxAge: int(s.sessions.maxAge.Seconds()),
	})
	return id, nil
}

func (s *Server) handleLogin(c *gin.Context) {
	if !s.cfg.DevLogin {
		writeMsg(c, http.StatusForbidden, "没有打开开发登录（CODING_DEV_LOGIN=1）")
		return
	}
	if c.GetHeader(csrfHeader) == "" {
		writeMsg(c, http.StatusForbidden, "缺少 X-Coding-Web 头")
		return
	}
	var req struct {
		Tenant string `json:"tenant"`
		User   string `json:"user"`
	}
	if !readJSON(c, &req) {
		return
	}
	req.Tenant, req.User = strings.ToLower(strings.TrimSpace(req.Tenant)), strings.ToLower(strings.TrimSpace(req.User))
	if !slug.MatchString(req.Tenant) || !slug.MatchString(req.User) {
		writeMsg(c, http.StatusBadRequest, "租户与用户名只能用小写字母、数字、- 与 _，以字母或数字开头")
		return
	}
	if reservedTenant(req.Tenant) {
		writeMsg(c, http.StatusBadRequest, "gh-、gitee-、gl-、gitea-、bb-、google- 开头的租户留给第三方登录")
		return
	}
	if err := s.ensureTenantPolicy(req.Tenant); err != nil {
		writeMsg(c, http.StatusInternalServerError, "写租户策略失败：%v", err)
		return
	}
	if _, err := s.setSession(c, req.Tenant, req.User); err != nil {
		writeMsg(c, http.StatusInternalServerError, "建会话失败：%v", err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]string{"tenant": req.Tenant, "user": req.User})
}

// handleLogout 删掉这个会话的记录（立即生效，AUTH-02）并清掉 Cookie。
func (s *Server) handleLogout(c *gin.Context) {
	if cookie, err := c.Request.Cookie(cookieName); err == nil && cookie.Value != "" && !strings.Contains(cookie.Value, ".") {
		if err := s.sessions.revoke(cookie.Value); err != nil {
			writeMsg(c, http.StatusInternalServerError, "删会话失败：%v", err)
			return
		}
	}
	s.clearCookie(c)
	writeJSON(c, http.StatusOK, map[string]bool{"ok": true})
}

// handleRevokeAll 退出所有设备：删掉这个用户的全部会话（AUTH-02）。
func (s *Server) handleRevokeAll(c *gin.Context, id identity) {
	n, err := s.sessions.revokeUser(id.TenantID, id.UserID)
	if err != nil {
		writeMsg(c, http.StatusInternalServerError, "删会话失败：%v", err)
		return
	}
	s.clearCookie(c)
	writeJSON(c, http.StatusOK, map[string]int{"revoked": n})
}

func (s *Server) clearCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (s *Server) handleMe(c *gin.Context, id identity) {
	me := map[string]any{"tenant": id.TenantID, "user": id.UserID, "expires_at": id.ExpiresAt}
	a, conns := s.connections(id)
	me["connections"] = conns
	for _, conn := range conns {
		if conn.Provider == a.Primary || me["avatar_url"] == nil {
			me["name"], me["avatar_url"], me["login"] = conn.DisplayName, conn.AvatarURL, conn.Login
		}
		if conn.Provider == "github" { // 旧的网页与测试读这个
			me["github"] = map[string]any{"login": conn.Login, "name": conn.DisplayName, "avatar_url": conn.AvatarURL}
		}
	}
	writeJSON(c, http.StatusOK, me)
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

// writeJSON 写出 JSON 响应；接口的数据都不该被缓存。
func writeJSON(c *gin.Context, status int, v any) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, v)
}

// readJSON 把请求体（有大小上限）绑定到 v；不是合法的 JSON 时写出 400 并返回 false。
// 没有用 binding 标签做校验：给用户看的报错要有中英两种，校验都在各处理函数里手写。
func readJSON(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodySize)
	if err := c.ShouldBindJSON(v); err != nil {
		writeMsg(c, http.StatusBadRequest, "请求体不是合法的 JSON")
		return false
	}
	return true
}

// project 取出请求路径里的项目，确认它属于这个租户。
func (s *Server) project(c *gin.Context, id identity) (Project, bool) {
	pid := c.Param("project")
	var saved Project
	if workspaceFact(c.Request.Context(), "project:"+pid, &saved) {
		return saved, true
	}
	if !slug.MatchString(pid) {
		writeMsg(c, http.StatusNotFound, "项目不存在")
		return Project{}, false
	}
	p, ok, err := s.store.Project(id.TenantID, pid)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return Project{}, false
	}
	if !ok {
		writeMsg(c, http.StatusNotFound, "项目不存在")
		return Project{}, false
	}
	saveWorkspaceFact(c.Request.Context(), "project:"+pid, p)
	return p, true
}

func ctxTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	if workspaceJournal(r.Context()) != nil && d >= 2*time.Minute {
		return context.WithCancel(r.Context())
	}
	return context.WithTimeout(r.Context(), d)
}
