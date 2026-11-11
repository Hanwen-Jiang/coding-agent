package server

import (
	"log"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
	"github.com/agentruntime/coding-agent/internal/web"
)

// 不打印 Gin 的调试输出（路由表、每次请求一行）。
func init() { gin.SetMode(gin.ReleaseMode) }

// ctxIdentityKey 是 requireSession 把身份放进 gin.Context 用的键。
const ctxIdentityKey = "coding.identity"

// authedHandler 是登录后的处理函数：身份由 requireSession 核对，withIdentity 取出来交给它。
type authedHandler func(c *gin.Context, id identity)

// Handler 返回挂好全部路由的 http.Handler（docs/plan/gin-migration-2026-11-11.md）。
func (s *Server) Handler() http.Handler {
	r := gin.New() // 不用 gin.Default()：它的日志与恢复中间件会把请求细节（查询串、Cookie）写进日志
	// 与原来的 http.ServeMux 保持一致：按转义后的路径分段（%2F 不拆段）、参数值照常解码、不做斜杠重定向、方法不对回 405。
	r.UseRawPath = true
	r.UnescapePathValues = true
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.HandleMethodNotAllowed = true
	r.Use(recovery(), securityHeaders())

	get(r, "/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	get(r, "/.well-known/jwks.json", s.handleJWKS)

	// 公开的：登录之前就要用，或者由对方的签名、密钥认证。
	pub := r.Group("/api")
	// GitHub App 的 Webhook（G-02）：不要登录与 X-Coding-Web 头，签名由收件箱验。
	switch {
	case s.cfg.Webhook != nil:
		pub.POST("/webhooks/github", gin.WrapH(s.cfg.Webhook))
	case s.cfg.GitHubApp != nil:
		pub.POST("/webhooks/github", func(c *gin.Context) { c.Status(http.StatusServiceUnavailable) })
	}
	get(pub, "/config", s.handleConfig)
	pub.POST("/login", s.handleLogin)
	pub.POST("/logout", s.handleLogout)
	// 第三方登录与账号关联（accounts.go）。
	get(pub, "/auth/:provider/login", s.handleOAuthLogin)
	get(pub, "/auth/:provider/callback", s.handleOAuthCallback)

	// 登录后的：requireSession 核对会话与 X-Coding-Web 头。
	in := r.Group("/api", s.requireSession())
	a := withIdentity
	s.entryRoutes(pub, in)
	in.POST("/sessions/revoke-all", a(s.handleRevokeAll))
	get(in, "/me", a(s.handleMe))
	get(in, "/workspace-operations", a(s.handleWorkspaceHTTPList))
	get(in, "/workspace-operations/:operation", a(s.handleWorkspaceHTTPStatus))
	get(in, "/network-policy", a(s.handleNetworkPolicy))
	get(in, "/account", a(s.handleAccount))
	in.POST("/account/connections/:provider/disconnect", a(s.handleDisconnect))
	get(in, "/git/:provider/repos", a(s.handleRepos))
	get(in, "/github/repos", a(s.handleRepos))
	// GitHub App 的 Setup URL（G-03）：安装或调整之后跳回这里，核对过才关联。
	get(in, "/github/setup", a(s.handleGitHubSetup))
	get(in, "/github/status", a(s.handleGitHubStatus))
	// 网络密钥（WEB-SEC）：转调 Runtime 的 UserSecretService，值只进不出；没打开时 404。
	get(in, "/secrets", a(s.handleListSecrets))
	in.PUT("/secrets/:name", a(s.handlePutSecret))
	in.DELETE("/secrets/:name", a(s.handleDeleteSecret))
	// 我的 Skill（WEB-SKILLS，Runtime 的 N-14）：转调 Runtime 的 SkillService；Runtime 没有它时列表回 available false。
	get(in, "/discovery/skills", a(s.handleExploreSkills))
	get(in, "/discovery/skills/detail", a(s.handleExploreSkillDetail))
	get(in, "/discovery/mcp", a(s.handleExploreMCP))
	get(in, "/skills", a(s.handleListSkills))
	get(in, "/personal-config", a(s.handleGetPersonalConfig))
	in.PUT("/personal-config", a(s.handlePutPersonalConfig))
	in.DELETE("/personal-config", a(s.handleRetirePersonalConfig))
	get(in, "/skills/:name", a(s.handleGetSkill))
	in.PUT("/skills/:name", a(s.handlePutSkill))
	in.DELETE("/skills/:name", a(s.handleRetireSkill))
	// 我的 MCP（Runtime 的 N-15）：转调 Runtime 的 McpConnectionService；Runtime 没有它时列表回 available false。
	get(in, "/mcp", a(s.handleListMcp))
	in.POST("/mcp", a(s.handleCreateMcp))
	get(in, "/mcp/:id", a(s.handleGetMcp))
	in.POST("/mcp/:id/refresh", a(s.handleRefreshMcp))
	in.PUT("/mcp/:id/tools", a(s.handleConfigureMcp))
	in.DELETE("/mcp/:id", a(s.handleDeleteMcp))
	get(in, "/samples", a(s.handleSamples))
	// 环境配置与项目解耦；这里仅保存普通配置，不报告 Runtime 生命周期状态。
	get(in, "/environments", a(s.handleListEnvironments))
	in.POST("/environments", a(s.handleCreateEnvironment))
	get(in, "/environments/:environment", a(s.handleGetEnvironment))
	in.PUT("/environments/:environment", a(s.handleUpdateEnvironment))
	in.PATCH("/environments/:environment", a(s.handleUpdateEnvironment))
	in.DELETE("/environments/:environment", a(s.handleDeleteEnvironment))
	get(in, "/sessions", a(s.handleListSessions))
	get(in, "/tasks/active", a(s.handleActiveTasks))
	// 网页报上来的计量（PERF-00，client_metrics.go）。
	in.POST("/metrics/history-load", a(s.handleHistoryLoad))

	// 工作区（项目）：基线仓库。
	get(in, "/projects", a(s.handleListProjects))
	in.POST("/projects", a(s.workspaceHTTP("create_project")))
	proj := in.Group("/projects/:project")
	proj.DELETE("", a(s.workspaceHTTP("delete_project")))
	get(proj, "/sources/capabilities", a(s.handleSourceCapabilities))
	get(proj, "/repos", a(s.handleWorkspaceRepos))
	proj.POST("/repos", a(s.workspaceHTTP("attach")))
	proj.POST("/repos/:repo/detach", a(s.workspaceHTTP("detach")))
	proj.POST("/sessions", a(s.workspaceHTTP("create_session")))

	// 会话：一段对话加一份工作副本；文件、改动、提交、推送与任务都按会话。
	sess := proj.Group("/sessions/:session")
	sess.DELETE("", a(s.workspaceHTTP("delete_session")))
	get(sess, "/repos", a(s.handleWorkspaceRepos))
	sess.POST("/repos", a(s.workspaceHTTP("attach")))
	sess.POST("/repos/:repo/detach", a(s.workspaceHTTP("detach")))
	sess.POST("/rename", a(s.handleRenameSession))
	sess.POST("/duplicate", a(s.workspaceHTTP("duplicate_session")))
	sess.POST("/sources", a(s.handleUploadSource))
	get(sess, "/sources", a(s.handleListSources))
	get(sess, "/sources/capabilities", a(s.handleSourceCapabilities))
	get(sess, "/sources/:source/content", a(s.handleSourceContent))
	get(sess, "/sources/:source/preview", a(s.handleSourceContent))
	// 文件工作台：原样转给 Runtime 的文件与检索接口（file_workbench.go）。
	get(sess, "/files/*file_tail", a(s.handleFileWorkbench))
	sess.POST("/files/*file_tail", a(s.handleFileWorkbench))
	sess.POST("/file-jobs", a(s.handleFileWorkbench))
	get(sess, "/file-jobs/*file_tail", a(s.handleFileWorkbench))
	sess.POST("/file-jobs/*file_tail", a(s.handleFileWorkbench))
	get(sess, "/knowledge/:knowledge_action", a(s.handleFileWorkbench))
	sess.POST("/knowledge/:knowledge_action", a(s.handleFileWorkbench))
	get(sess, "/settings", a(s.handleSessionTaskSettings))
	sess.PUT("/settings", a(s.handleSessionTaskSettings))
	get(sess, "/env", a(s.handleEnv))
	get(sess, "/tree", a(s.handleTree))
	get(sess, "/file", a(s.handleFile))
	get(sess, "/log", a(s.handleLog))
	get(sess, "/changes", a(s.handleChanges))
	get(sess, "/patch", a(s.handlePatch))
	sess.POST("/commit", a(s.workspaceHTTP("commit")))
	sess.POST("/discard", a(s.workspaceHTTP("discard")))
	sess.POST("/publish", a(s.workspaceHTTP("publish")))
	sess.POST("/reset-branch", a(s.workspaceHTTP("reset")))
	sess.POST("/sync", a(s.workspaceHTTP("sync")))
	get(sess, "/snapshot", a(s.handleSnapshot))
	sess.POST("/events", a(s.handleSessionEvents))
	get(sess, "/tasks", a(s.handleListTasks))
	sess.POST("/tasks", a(s.handleSubmitTask))

	task := sess.Group("/tasks/:task")
	get(task, "", a(s.handleQueryTask))
	get(task, "/events", a(s.handleEvents))
	task.POST("/reply", a(s.handleReply))
	task.POST("/guidance", a(s.handleGuidance))
	task.POST("/guidance/:guidance/edit", a(s.handleGuidanceEdit))
	task.POST("/guidance/:guidance/retract", a(s.handleGuidanceRetract))
	task.POST("/message", a(s.handleEditQueued))
	task.POST("/message/result", a(s.handleQueuedResult))
	task.POST("/cancel", a(s.handleCancel))
	get(task, "/artifacts", a(s.handleListArtifacts))
	get(task, "/artifacts/:artifact", a(s.handleArtifactDownload))

	// 其余的 GET 都交给内嵌的网页（单页应用自己管路由）；别的方法回 405。与原来 ServeMux 的 "GET /" 相同。
	static := web.Handler()
	fallback := func(c *gin.Context) {
		// 路径登记过别的方法时，Gin 已经把那些方法写进 Allow。
		others := c.Writer.Header().Get("Allow")
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			allow := "GET, HEAD" // 任何路径的 GET 都有兜底
			if others != "" {
				allow += ", " + others
			}
			c.Header("Allow", allow)
			http.Error(c.Writer, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		c.Writer.Header().Del("Allow")
		// Gin 进到这里之前把状态码预设成 404 或 405；静态页自己不写状态码时要的是 200。
		c.Status(http.StatusOK)
		static.ServeHTTP(c.Writer, c.Request)
	}
	r.NoRoute(fallback)
	r.NoMethod(fallback)
	return r
}

// get 同时登记 GET 与 HEAD：ServeMux 的 GET 路由也接 HEAD，响应体由 net/http 丢弃。
func get(g gin.IRoutes, path string, handlers ...gin.HandlerFunc) {
	g.GET(path, handlers...)
	g.HEAD(path, handlers...)
}

// recovery 捕获处理函数的 panic，回 500。不用 gin.Recovery()：它把请求头（含 Cookie）写进日志。
// 这里只记方法、路由模板与堆栈，不记查询串（OAuth 回调的授权码在里面）。
func recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { // net/http 约定的“中断这次响应”，照旧交给它
				panic(v)
			}
			log.Printf("coding-web: panic 于 %s %s：%v\n%s", c.Request.Method, c.FullPath(), v, debug.Stack())
			if c.Writer.Written() {
				c.Abort()
				return
			}
			c.AbortWithStatus(http.StatusInternalServerError)
		}()
		c.Next()
	}
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "same-origin")
		// 头像来自各家平台（GitHub、Gitee、GitLab、Gravatar、Google…），图片放开 https；脚本、样式、连接仍只许同源。
		c.Header("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob: https:; style-src 'self'; script-src 'self'; connect-src 'self'")
		c.Next()
	}
}

// requireSession 是登录后那组路由的中间件：核对 Cookie 里的会话，改写请求还要带 X-Coding-Web 头（配合 SameSite Cookie
// 防跨站请求）。通过后把身份放进 Context，带着这个会话现签的 Runtime 短期令牌。
func (s *Server) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Request.Cookie(cookieName)
		if err != nil || cookie.Value == "" {
			abortMsg(c, http.StatusUnauthorized, "请先登录")
			return
		}
		csrfOK := c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.GetHeader(csrfHeader) != ""
		sid := cookie.Value
		if strings.Contains(sid, ".") {
			// 过渡（AUTH-02 之前登录的）：Cookie 里还是令牌。有效的换成会话，之后浏览器只拿会话 id。
			old, err := s.ingress.Authenticate(c.Request.Context(), ingress.Credentials{BearerToken: sid})
			if err != nil {
				s.clearCookie(c)
				abortMsg(c, http.StatusUnauthorized, "登录已过期，请重新登录")
				return
			}
			if !csrfOK {
				abortMsg(c, http.StatusForbidden, "缺少 X-Coding-Web 头")
				return
			}
			if sid, err = s.setSession(c, old.TenantID, old.UserID); err != nil {
				abortMsg(c, http.StatusInternalServerError, "建会话失败：%v", err)
				return
			}
		}
		sess, ok, err := s.sessions.lookup(sid)
		if err != nil {
			abortMsg(c, http.StatusInternalServerError, "读会话失败：%v", err)
			return
		}
		if !ok {
			s.clearCookie(c)
			abortMsg(c, http.StatusUnauthorized, "登录已过期，请重新登录")
			return
		}
		if !csrfOK {
			abortMsg(c, http.StatusForbidden, "缺少 X-Coding-Web 头")
			return
		}
		c.Set(ctxIdentityKey, s.sessionIdentity(sid, sess))
		c.Next()
	}
}

// withIdentity 把 requireSession 放进 Context 的身份交给处理函数。路由漏挂中间件时回 401，不会拿着空身份往下走。
func withIdentity(h authedHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, _ := c.Get(ctxIdentityKey)
		id, ok := v.(identity)
		if !ok {
			abortMsg(c, http.StatusUnauthorized, "请先登录")
			return
		}
		h(c, id)
	}
}
