package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/agentruntime/coding-agent/internal/adminops"
)

type adminIdentity struct {
	Email string
	Kind  string // "password" | "user_rbac"
}

func (s *Server) setAdminCookie(c *gin.Context, sid string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(adminops.AdminCookieName, sid, 86400, "/", "", s.cfg.SecureCookie, true)
}

func (s *Server) clearAdminCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(adminops.AdminCookieName, "", -1, "/", "", s.cfg.SecureCookie, true)
}

func (s *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.adminAuth == nil || !s.adminAuth.Enabled() {
			abortMsg(c, http.StatusServiceUnavailable, "管理员认证未配置")
			return
		}
		csrfOK := c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.GetHeader(csrfHeader) != ""
		if !csrfOK {
			abortMsg(c, http.StatusForbidden, "缺少 X-Coding-Web 头")
			return
		}

		// 1. Dedicated admin credential session (cw_admin)
		if cookie, err := c.Request.Cookie(adminops.AdminCookieName); err == nil && cookie.Value != "" {
			if sess, ok := s.adminAuth.LookupSession(cookie.Value); ok {
				c.Set("coding.admin", adminIdentity{Email: sess.Email, Kind: sess.Kind})
				c.Next()
				return
			}
		}

		// 2. Normal coding-web user session matching CODING_ADMINS RBAC allowlist
		if cookie, err := c.Request.Cookie(cookieName); err == nil && cookie.Value != "" {
			if sess, ok, err := s.sessions.lookup(cookie.Value); err == nil && ok {
				email := s.adminUserEmail(sess.Tenant, sess.User)
				if s.adminAuth.IsAllowedUser(sess.User, email) {
					actor := sess.User
					if email != "" {
						actor = email
					}
					c.Set("coding.admin", adminIdentity{Email: actor, Kind: "user_rbac"})
					c.Next()
					return
				}
				abortMsg(c, http.StatusForbidden, "管理员权限不足")
				return
			}
		}

		abortMsg(c, http.StatusUnauthorized, "管理员权限不足")
	}
}

func (s *Server) adminUserEmail(tenant, user string) string {
	if acc, err := s.readAccount(tenant, user); err == nil && acc.Primary != "" {
		return acc.Connections[acc.Primary].Email
	}
	return ""
}

func getAdmin(c *gin.Context) (adminIdentity, bool) {
	v, ok := c.Get("coding.admin")
	if !ok {
		return adminIdentity{}, false
	}
	id, ok := v.(adminIdentity)
	return id, ok
}

func (s *Server) adminRoutes(pub *gin.RouterGroup, r *gin.Engine) {
	pub.POST("/admin/v1/login", s.handleAdminLogin)
	pub.POST("/admin/v1/logout", s.handleAdminLogout)
	pub.GET("/admin/v1/session", s.handleAdminSession)

	adm := r.Group("/api/admin/v1", s.requireAdmin())
	get(adm, "/overview", s.handleAdminOverview)
	get(adm, "/telemetry/summary", s.handleAdminTelemetrySummary)
	get(adm, "/evolution/candidates", s.handleAdminListCandidates)
	get(adm, "/evolution/candidates/:id", s.handleAdminGetCandidate)
	adm.POST("/evolution/candidates/import", s.handleAdminImportCandidate)
	adm.POST("/evolution/candidates/:id/promote", s.handleAdminPromoteCandidate)
	adm.POST("/evolution/candidates/:id/rollback", s.handleAdminRollbackCandidate)
}

func (s *Server) handleAdminLogin(c *gin.Context) {
	if s.adminAuth == nil || !s.adminAuth.PasswordEnabled() {
		writeMsg(c, http.StatusServiceUnavailable, "管理员认证未配置")
		return
	}
	if c.GetHeader(csrfHeader) == "" {
		writeMsg(c, http.StatusForbidden, "缺少 X-Coding-Web 头")
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !readJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.Email) == "" || req.Password == "" {
		writeMsg(c, http.StatusBadRequest, "请填写管理员邮箱和密码")
		return
	}
	sess, err := s.adminAuth.Login(c.ClientIP(), req.Email, req.Password)
	if err != nil {
		if err == adminops.ErrTooManyAttempts {
			writeMsg(c, http.StatusTooManyRequests, "登录失败次数过多，请 5 分钟后再试")
			return
		}
		writeMsg(c, http.StatusUnauthorized, "管理员账号或密码错误")
		return
	}
	s.setAdminCookie(c, sess.ID)
	writeJSON(c, http.StatusOK, map[string]any{
		"authenticated": true,
		"email":         sess.Email,
		"kind":          sess.Kind,
	})
}

func (s *Server) handleAdminLogout(c *gin.Context) {
	if c.GetHeader(csrfHeader) == "" {
		writeMsg(c, http.StatusForbidden, "缺少 X-Coding-Web 头")
		return
	}
	if cookie, err := c.Request.Cookie(adminops.AdminCookieName); err == nil && cookie.Value != "" && s.adminAuth != nil {
		s.adminAuth.RevokeSession(cookie.Value)
	}
	s.clearAdminCookie(c)
	writeJSON(c, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminSession(c *gin.Context) {
	enabled := s.adminAuth != nil && s.adminAuth.Enabled()
	pwEnabled := s.adminAuth != nil && s.adminAuth.PasswordEnabled()
	if s.adminAuth != nil {
		if cookie, err := c.Request.Cookie(adminops.AdminCookieName); err == nil && cookie.Value != "" {
			if sess, ok := s.adminAuth.LookupSession(cookie.Value); ok {
				writeJSON(c, http.StatusOK, map[string]any{
					"enabled":          enabled,
					"password_enabled": pwEnabled,
					"authenticated":    true,
					"email":            sess.Email,
					"kind":             sess.Kind,
				})
				return
			}
		}
		if cookie, err := c.Request.Cookie(cookieName); err == nil && cookie.Value != "" {
			if sess, ok, err := s.sessions.lookup(cookie.Value); err == nil && ok {
				email := s.adminUserEmail(sess.Tenant, sess.User)
				if s.adminAuth.IsAllowedUser(sess.User, email) {
					actor := sess.User
					if email != "" {
						actor = email
					}
					writeJSON(c, http.StatusOK, map[string]any{
						"enabled":          enabled,
						"password_enabled": pwEnabled,
						"authenticated":    true,
						"email":            actor,
						"kind":             "user_rbac",
					})
					return
				}
			}
		}
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"enabled":          enabled,
		"password_enabled": pwEnabled,
		"authenticated":    false,
	})
}

func (s *Server) handleAdminTelemetrySummary(c *gin.Context) {
	ctx, cancel := ctxTimeout(c.Request, 6*time.Second)
	defer cancel()
	if s.adminTelemetry == nil {
		writeJSON(c, http.StatusOK, adminops.TelemetrySummary{})
		return
	}
	writeJSON(c, http.StatusOK, s.adminTelemetry.Summary(ctx))
}

func (s *Server) handleAdminOverview(c *gin.Context) {
	ctx, cancel := ctxTimeout(c.Request, 8*time.Second)
	defer cancel()
	admin, _ := getAdmin(c)
	var summary adminops.TelemetrySummary
	if s.adminTelemetry != nil {
		summary = s.adminTelemetry.Summary(ctx)
	}
	candidates := []adminops.CandidateRecord{}
	var regOverview adminops.RegistryOverview
	if s.adminEvolution != nil {
		if rows, err := s.adminEvolution.List(ctx, ""); err == nil {
			candidates = rows
		}
		regOverview = s.adminEvolution.RegistryOverview(ctx)
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"admin":      map[string]any{"email": admin.Email, "kind": admin.Kind},
		"telemetry":  summary,
		"registry":   regOverview,
		"candidates": candidates,
	})
}

func (s *Server) handleAdminListCandidates(c *gin.Context) {
	ctx, cancel := ctxTimeout(c.Request, 6*time.Second)
	defer cancel()
	rows, err := s.adminEvolution.List(ctx, c.Query("status"))
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"candidates": rows})
}

func (s *Server) handleAdminGetCandidate(c *gin.Context) {
	ctx, cancel := ctxTimeout(c.Request, 6*time.Second)
	defer cancel()
	rec, err := s.adminEvolution.Get(ctx, c.Param("id"))
	if err != nil {
		writeMsg(c, http.StatusNotFound, "候选不存在")
		return
	}
	writeJSON(c, http.StatusOK, rec)
}

func (s *Server) handleAdminImportCandidate(c *gin.Context) {
	var rec adminops.CandidateRecord
	if !readJSON(c, &rec) {
		return
	}
	if strings.TrimSpace(rec.TargetName) == "" || strings.TrimSpace(rec.UnifiedDiff) == "" {
		writeMsg(c, http.StatusBadRequest, "候选内容不合法")
		return
	}
	if strings.TrimSpace(rec.CandidateID) == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		rec.CandidateID = "cand_" + hex.EncodeToString(b)
	}
	ctx, cancel := ctxTimeout(c.Request, 6*time.Second)
	defer cancel()
	saved, err := s.adminEvolution.Import(ctx, rec)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	writeJSON(c, http.StatusCreated, saved)
}

func (s *Server) handleAdminPromoteCandidate(c *gin.Context) {
	admin, _ := getAdmin(c)
	ctx, cancel := ctxTimeout(c.Request, 10*time.Second)
	defer cancel()
	updated, err := s.adminEvolution.Promote(ctx, c.Param("id"), admin.Email)
	if err != nil {
		switch err {
		case adminops.ErrCandidateNotFound:
			writeMsg(c, http.StatusNotFound, "候选不存在")
		case adminops.ErrCandidateNotPromotable:
			writeMsg(c, http.StatusConflict, "该候选当前不可晋升，请确认状态为待审批且评测门禁已通过")
		case adminops.ErrRegistryUnavailable:
			writeMsg(c, http.StatusBadGateway, "Runtime Registry 写服务未就绪或拒绝了发布请求")
		default:
			writeErr(c, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(c, http.StatusOK, updated)
}

func (s *Server) handleAdminRollbackCandidate(c *gin.Context) {
	admin, _ := getAdmin(c)
	var body struct {
		Reason string `json:"reason"`
	}
	_ = readJSON(c, &body)
	ctx, cancel := ctxTimeout(c.Request, 10*time.Second)
	defer cancel()
	updated, err := s.adminEvolution.Rollback(ctx, c.Param("id"), admin.Email, body.Reason)
	if err != nil {
		switch err {
		case adminops.ErrCandidateNotFound:
			writeMsg(c, http.StatusNotFound, "候选不存在")
		case adminops.ErrCandidateNotPromoted:
			writeMsg(c, http.StatusConflict, "仅已晋升候选支持回滚")
		case adminops.ErrMissingBaseline:
			writeMsg(c, http.StatusConflict, "缺少基线快照，无法回滚")
		case adminops.ErrRegistryUnavailable:
			writeMsg(c, http.StatusBadGateway, "Runtime Registry 写服务未就绪或拒绝了发布请求")
		default:
			writeErr(c, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(c, http.StatusOK, updated)
}
