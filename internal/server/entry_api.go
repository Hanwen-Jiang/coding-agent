package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/agentruntime/coding-agent/internal/entry"
	"github.com/agentruntime/coding-agent/internal/entry/alert"
	entrymail "github.com/agentruntime/coding-agent/internal/entry/email"
	"github.com/agentruntime/coding-agent/internal/entry/githubcomment"
	"github.com/agentruntime/coding-agent/internal/entry/schedule"
)

func (s *Server) entryKinds() []entry.Kind {
	bot := ""
	if s.cfg.GitHubApp != nil {
		bot = s.cfg.GitHubApp.Slug
	}
	return []entry.Kind{githubcomment.Kind{BotSlug: bot}, schedule.Kind{}, alert.Kind{}, entrymail.Kind{VerifiedSender: s.verifiedEntrySender}}
}
func (s *Server) entryKind(name string) entry.Kind {
	for _, k := range s.entryKinds() {
		if k.Name() == name {
			return k
		}
	}
	return nil
}
func entryConfig(c entry.Connection, key string) string { v, _ := c.Config[key].(string); return v }

// entryRoutes 登记接入点的接口：管理接口要登录，入站 Webhook 由每个连接自己的密钥认证。
func (s *Server) entryRoutes(pub, in gin.IRoutes) {
	a := withIdentity
	get(in, "/entry/kinds", a(s.handleEntryKinds))
	get(in, "/entry/connections", a(s.handleEntryConnections))
	in.POST("/entry/connections", a(s.handleEntrySave))
	in.PUT("/entry/connections/:connection", a(s.handleEntrySave))
	in.DELETE("/entry/connections/:connection", a(s.handleEntryDelete))
	in.POST("/entry/connections/:connection/rotate-secret", a(s.handleEntryRotate))
	get(in, "/entry/connections/:connection/triggers", a(s.handleEntryRuns))
	get(in, "/entry/runs", a(s.handleEntryRuns))
	in.POST("/entry/deliveries/:delivery/retry", a(s.handleEntryReplay))
	in.POST("/entry/triggers/:trigger/confirm", a(s.handleEntryConfirm))
	pub.POST("/webhooks/entry/:connection", s.handleEntryWebhook)
}
func (s *Server) entryAvailable(c *gin.Context) bool {
	if s.cfg.EntryStore == nil {
		writeMsg(c, 503, "接入点尚未配置数据库")
		return false
	}
	return true
}
func entryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, entry.ErrNotFound):
		writeMsg(c, 404, "接入点记录不存在")
	case errors.Is(err, entry.ErrInvalid):
		writeMsg(c, 400, "接入点配置无效，请检查字段与限制")
	case errors.Is(err, entry.ErrConflict), errors.Is(err, entry.ErrLeaseLost):
		writeMsg(c, 409, "接入点记录已变化，请刷新后重试")
	default:
		writeMsg(c, 503, "接入点服务暂时不可用")
	}
}
func (s *Server) handleEntryKinds(c *gin.Context, id identity) {
	kinds := []any{}
	for _, k := range s.entryKinds() {
		sc := k.Schema()
		fields := []any{}
		for _, f := range sc.Fields {
			opts := []any{}
			for _, o := range f.Options {
				opts = append(opts, map[string]any{"value": o, "label": o})
			}
			fields = append(fields, map[string]any{"name": f.Name, "label": map[string]string{"zh": f.Label, "en": f.LabelEN}, "type": f.Type, "required": f.Required, "default": f.Default, "min": f.Min, "max": f.Max, "options": opts})
		}
		kinds = append(kinds, map[string]any{"name": k.Name(), "label": map[string]string{"zh": sc.Label, "en": sc.LabelEN}, "fields": fields, "available": s.cfg.EntryStore != nil && (k.Name() != "github_comment" || s.cfg.GitHubApp != nil)})
	}
	writeJSON(c, 200, map[string]any{"kinds": kinds, "available": s.cfg.EntryStore != nil, "email_delivery_available": s.cfg.EntryMail != nil})
}

type entryDestinationDTO struct {
	Kind    string        `json:"kind"`
	Sink    string        `json:"sink,omitempty"`
	Address entry.Address `json:"address"`
	Phases  []entry.Phase `json:"phases,omitempty"`
}
type entryConnectionDTO struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Kind             string                `json:"kind"`
	ProjectID        string                `json:"project_id"`
	Enabled          bool                  `json:"enabled"`
	Config           map[string]any        `json:"config"`
	Sinks            []entryDestinationDTO `json:"sinks"`
	Language         string                `json:"language"`
	Autonomy         string                `json:"autonomy"`
	RunsPerHour      int                   `json:"runs_per_hour"`
	BudgetUSDPerHour float64               `json:"budget_usd_per_hour"`
	TaskBudgetUSD    float64               `json:"task_budget_usd"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
	WebhookURL       string                `json:"webhook_url,omitempty"`
}

func (s *Server) entryDTO(c entry.Connection) entryConnectionDTO {
	d := entryConnectionDTO{ID: c.ID, Name: c.Name, Kind: c.Kind, ProjectID: c.ProjectID, Enabled: c.Enabled, Config: c.Config, Language: entryConfig(c, "language"), Autonomy: entryConfig(c, "autonomy"), RunsPerHour: c.Limits.RunsPerHour, BudgetUSDPerHour: c.Limits.USDPerHour, TaskBudgetUSD: c.Limits.TaskUSD, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Sinks: []entryDestinationDTO{}}
	for _, out := range c.Sinks {
		d.Sinks = append(d.Sinks, entryDestinationDTO{Kind: out.Sink, Address: out.Address, Phases: out.Phases})
	}
	if c.Kind == "alert" || c.Kind == "email" {
		d.WebhookURL = strings.TrimRight(s.cfg.PublicURL, "/") + "/api/webhooks/entry/" + c.ID
	}
	return d
}
func (s *Server) handleEntryConnections(c *gin.Context, id identity) {
	if !s.entryAvailable(c) {
		return
	}
	cs, err := s.cfg.EntryStore.Connections(c.Request.Context(), id.TenantID, id.UserID)
	if err != nil {
		entryError(c, err)
		return
	}
	out := []entryConnectionDTO{}
	for _, conn := range cs {
		out = append(out, s.entryDTO(conn))
	}
	writeJSON(c, 200, map[string]any{"connections": out})
}

var entryMention = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func (s *Server) handleEntrySave(c *gin.Context, id identity) {
	if !s.entryAvailable(c) {
		return
	}
	var raw json.RawMessage
	if !readJSON(c, &raw) {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		entryError(c, entry.ErrInvalid)
		return
	}
	var d entryConnectionDTO
	now := s.now().UTC()
	conn := entry.Connection{ID: entry.NewID(), Tenant: id.TenantID, User: id.UserID, CreatedAt: now, Limits: entry.DefaultLimits()}
	if c.Request.Method == http.MethodPut {
		var err error
		conn, err = s.cfg.EntryStore.Connection(c.Request.Context(), id.TenantID, id.UserID, c.Param("connection"))
		if err != nil {
			entryError(c, err)
			return
		}
		d = s.entryDTO(conn)
	}
	if json.Unmarshal(raw, &d) != nil {
		entryError(c, entry.ErrInvalid)
		return
	}
	if c.Request.Method == http.MethodPut {
		if d.Kind != conn.Kind || d.ProjectID != conn.ProjectID {
			entryError(c, entry.ErrInvalid)
			return
		}
	}
	conn.Name, conn.Kind, conn.ProjectID, conn.Config = d.Name, d.Kind, d.ProjectID, d.Config
	conn.Enabled = c.Request.Method == http.MethodPut && d.Enabled
	conn.UpdatedAt = now
	if conn.Config == nil {
		conn.Config = map[string]any{}
	}
	kind := s.entryKind(conn.Kind)
	if kind == nil {
		entryError(c, entry.ErrInvalid)
		return
	}
	for _, f := range kind.Schema().Fields {
		if _, ok := conn.Config[f.Name]; !ok && f.Default != nil {
			conn.Config[f.Name] = f.Default
		}
	}
	if _, present := fields["language"]; present && d.Language != "" {
		conn.Config["language"] = d.Language
	}
	if _, present := fields["autonomy"]; present && d.Autonomy != "" {
		if d.Autonomy == "read" {
			d.Autonomy = "analyze"
		}
		conn.Config["autonomy"] = d.Autonomy
	}
	if _, present := fields["runs_per_hour"]; present {
		conn.Limits.RunsPerHour = d.RunsPerHour
	}
	if _, present := fields["budget_usd_per_hour"]; present {
		conn.Limits.USDPerHour = d.BudgetUSDPerHour
	}
	if _, present := fields["task_budget_usd"]; present {
		conn.Limits.TaskUSD = d.TaskBudgetUSD
	}
	if _, explicit := fields["task_budget_usd"]; !explicit && conn.Limits.TaskUSD > conn.Limits.USDPerHour {
		conn.Limits.TaskUSD = conn.Limits.USDPerHour
	}
	conn.Sinks = nil
	for _, out := range d.Sinks {
		sink := out.Kind
		if sink == "" {
			sink = out.Sink
		}
		conn.Sinks = append(conn.Sinks, entry.Destination{Sink: sink, Address: out.Address, Phases: out.Phases})
	}
	p, ok, err := s.store.Project(id.TenantID, conn.ProjectID)
	if err != nil {
		entryError(c, err)
		return
	}
	if !ok || p.Layout == layoutMulti {
		entryError(c, entry.ErrInvalid)
		return
	}
	if entryConfig(conn, "autonomy") != "analyze" && (p.Provider == "" || s.providers[p.Provider] == nil || p.Repo == "") {
		entryError(c, entry.ErrInvalid)
		return
	}
	if err = kind.Schema().Validate(conn.Config); err == nil {
		err = entry.ValidateConnection(conn)
	}
	if err == nil {
		switch conn.Kind {
		case "schedule":
			if schedule.Validate(entryConfig(conn, "cron")) != nil {
				err = entry.ErrInvalid
			}
		case "alert":
			err = alert.Validate(conn.Config)
		case "email":
			err = entrymail.Validate(conn.Config)
		}
	}
	if err != nil {
		entryError(c, err)
		return
	}
	if conn.Kind == "github_comment" {
		if s.cfg.GitHubApp == nil || p.Provider != "github" || !entryMention.MatchString(entryConfig(conn, "mention")) {
			entryError(c, entry.ErrInvalid)
			return
		}
		cs, e := s.cfg.EntryStore.Connections(c.Request.Context(), id.TenantID, id.UserID)
		if e != nil {
			entryError(c, e)
			return
		}
		for _, x := range cs {
			if x.ID != conn.ID && x.Kind == conn.Kind && x.ProjectID == conn.ProjectID && strings.EqualFold(entryConfig(x, "mention"), entryConfig(conn, "mention")) {
				entryError(c, entry.ErrConflict)
				return
			}
		}
	}
	for i, out := range conn.Sinks {
		switch out.Sink {
		case "github_comment":
			if conn.Kind == "github_comment" && out.Address.Repo == "" && out.Address.Issue == 0 {
				continue
			}
			if p.Provider != "github" || !strings.EqualFold(out.Address.Repo, p.Repo) || out.Address.Issue < 1 {
				entryError(c, entry.ErrInvalid)
				return
			}
			grant, e := s.authorizeRepo(c.Request, id, &p, repoWrite)
			if e != nil {
				writeErr(c, 403, e)
				return
			}
			grant.Done()
			conn.Sinks[i].Address.RepoID = p.RepoID
			conn.Sinks[i].Address.Installation = p.InstallationID
		case "email":
			if conn.Enabled && s.cfg.EntryMail == nil {
				entryError(c, entry.ErrInvalid)
				return
			}
			if conn.Kind == "email" && out.Address.Email == "" {
				continue
			} // Reply to the independently verified original sender.
			if !s.verifiedEntrySender(c.Request.Context(), conn, out.Address.Email) {
				entryError(c, entry.ErrInvalid)
				return
			}
		}
	}
	secret := ""
	if c.Request.Method == http.MethodPost && (conn.Kind == "alert" || conn.Kind == "email") {
		secret, conn.SecretHash = entry.NewSecret()
	}
	if err = s.cfg.EntryStore.PutConnection(c.Request.Context(), conn); err != nil {
		entryError(c, err)
		return
	}
	status := 200
	if c.Request.Method == http.MethodPost {
		status = 201
	}
	out := map[string]any{"connection": s.entryDTO(conn)}
	if secret != "" {
		out["secret"] = secret
		c.Header("Cache-Control", "no-store")
	}
	writeJSON(c, status, out)
}
func (s *Server) handleEntryDelete(c *gin.Context, id identity) {
	if !s.entryAvailable(c) {
		return
	}
	if err := s.cfg.EntryStore.DeleteConnection(c.Request.Context(), id.TenantID, id.UserID, c.Param("connection")); err != nil {
		entryError(c, err)
		return
	}
	c.Status(204)
}
func (s *Server) handleEntryRotate(c *gin.Context, id identity) {
	if !s.entryAvailable(c) {
		return
	}
	conn, err := s.cfg.EntryStore.Connection(c.Request.Context(), id.TenantID, id.UserID, c.Param("connection"))
	if err != nil {
		entryError(c, err)
		return
	}
	if conn.Kind != "alert" && conn.Kind != "email" {
		entryError(c, entry.ErrInvalid)
		return
	}
	secret, hash := entry.NewSecret()
	conn, err = s.cfg.EntryStore.RotateSecret(c.Request.Context(), id.TenantID, id.UserID, conn.ID, hash, s.now().UTC())
	if err != nil {
		entryError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeJSON(c, 200, map[string]any{"connection": s.entryDTO(conn), "secret": secret})
}
func (s *Server) handleEntryRuns(c *gin.Context, id identity) {
	if !s.entryAvailable(c) {
		return
	}
	connection := c.Param("connection")
	if connection != "" {
		if _, err := s.cfg.EntryStore.Connection(c.Request.Context(), id.TenantID, id.UserID, connection); err != nil {
			entryError(c, err)
			return
		}
	}
	var ts []entry.Trigger
	var err error
	if session := c.Query("session"); session != "" {
		ts, err = s.cfg.EntryStore.SessionTriggers(c.Request.Context(), id.TenantID, id.UserID, session)
	} else {
		ts, err = s.cfg.EntryStore.Triggers(c.Request.Context(), id.TenantID, id.UserID, connection, 500)
	}
	if err != nil {
		entryError(c, err)
		return
	}
	active, e := s.cfg.EntryStore.ActiveTriggers(c.Request.Context(), id.TenantID, id.UserID)
	if e != nil {
		entryError(c, e)
		return
	}
	for _, t := range active {
		if (connection == "" || t.ConnectionID == connection) && !slices.ContainsFunc(ts, func(x entry.Trigger) bool { return x.ID == t.ID }) {
			ts = append(ts, t)
		}
	}
	ds := []entry.Delivery{}
	out := []entry.Trigger{}
	for _, t := range ts {
		if sid := c.Query("session"); sid != "" && t.SessionID != sid {
			continue
		}
		out = append(out, t)
		rows, e := s.cfg.EntryStore.Deliveries(c.Request.Context(), id.TenantID, id.UserID, t.ID, 100)
		if e != nil {
			entryError(c, e)
			return
		}
		ds = append(ds, rows...)
	}
	writeJSON(c, 200, map[string]any{"triggers": out, "deliveries": ds})
}
func (s *Server) handleEntryReplay(c *gin.Context, id identity) {
	if !s.entryAvailable(c) {
		return
	}
	if err := s.cfg.EntryStore.ReplayDelivery(c.Request.Context(), id.TenantID, id.UserID, c.Param("delivery"), s.now().UTC()); err != nil {
		entryError(c, err)
		return
	}
	writeJSON(c, 200, map[string]bool{"queued": true})
}
func (s *Server) handleEntryConfirm(c *gin.Context, id identity) {
	if !s.entryAvailable(c) {
		return
	}
	t, err := s.cfg.EntryStore.Trigger(c.Request.Context(), c.Param("trigger"))
	if err != nil || t.Tenant != id.TenantID || t.User != id.UserID {
		entryError(c, entry.ErrNotFound)
		return
	}
	_, changed, err := s.cfg.EntryStore.MutateTrigger(c.Request.Context(), t.ID, 0, func(x *entry.Trigger) bool {
		asked := x.UpdatedAt
		if x.Kind == "github_comment" {
			g, e := decodeGitHubEntry(*x)
			if e != nil {
				return false
			}
			asked = g.AskedAt
		}
		if x.State != "awaiting_confirmation" || asked.IsZero() || s.now().Sub(asked) > triggerConfirmWithin {
			return false
		}
		x.State = "confirmed"
		x.NextAt = time.Now().UTC()
		return true
	})
	if err != nil {
		entryError(c, err)
		return
	}
	if !changed {
		entryError(c, entry.ErrConflict)
		return
	}
	writeJSON(c, 200, map[string]bool{"confirmed": true})
}
func (s *Server) handleEntryWebhook(c *gin.Context) {
	if s.cfg.EntryStore == nil {
		c.Status(503)
		return
	}
	conn, err := s.cfg.EntryStore.LookupConnection(c.Request.Context(), c.Param("connection"))
	if err != nil || conn.Config["deleted"] == true || conn.Kind != "alert" && conn.Kind != "email" {
		c.Status(401)
		return
	}
	header := c.GetHeader("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || !entry.VerifySecret(conn.SecretHash, token) {
		s.metrics().Count("coding.entry.webhooks", 1, map[string]string{"result": "unauthorized"})
		c.Status(401)
		return
	}
	limit := int64(alert.MaxBody)
	if conn.Kind == "email" {
		limit = entrymail.MaxBody
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, limit))
	if err != nil {
		c.Status(413)
		return
	}
	kind := s.entryKind(conn.Kind)
	ts, err := kind.Ingest(c.Request.Context(), entry.Inbound{Body: body, Header: c.Request.Header, Now: s.now().UTC(), Connection: conn, Trusted: true})
	if err != nil {
		c.Status(400)
		return
	}
	p, found, err := s.store.Project(conn.Tenant, conn.ProjectID)
	if err != nil || !found {
		c.Status(503)
		return
	}
	for _, t := range ts {
		a := kind.Admit(c.Request.Context(), conn, t)
		s.fillEntryTrigger(&t, conn, p)
		if !a.Allowed {
			t.State, t.Reason, t.ReservedUSD = "refused", a.Reason, 0
			t.Payload, t.Instruction, t.Materials = nil, "", ""
		}
		if _, err = s.cfg.EntryStore.Enqueue(c.Request.Context(), t); err != nil {
			c.Status(503)
			return
		}
	}
	s.metrics().Count("coding.entry.webhooks", 1, map[string]string{"kind": conn.Kind, "result": "accepted"})
	c.Status(202)
}

// Only provider-verified mailboxes can authorize an email task. Public profile
// email addresses are insufficient; GitHub is checked against /user/emails.
func (s *Server) verifiedEntrySender(ctx context.Context, c entry.Connection, email string) bool {
	normalized, err := entrymail.NormalizeAddress(email)
	if err != nil {
		return false
	}
	email = normalized
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	id := s.triggerIdentity(githubTrigger{Tenant: c.Tenant, User: c.User})
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/", nil)
	for _, name := range []string{"github", "google"} {
		p := s.providers[name]
		if p == nil {
			continue
		}
		_, token, err := s.connToken(r, id, p)
		if err != nil {
			continue
		}
		if name == "google" {
			u, e := p.user(ctx, token)
			if e == nil && strings.EqualFold(u.Email, email) {
				return true
			}
			continue
		}
		var rows []struct {
			Email    string `json:"email"`
			Verified bool   `json:"verified"`
		}
		if p.api(ctx, token, http.MethodGet, "/user/emails", nil, &rows) == nil && slices.ContainsFunc(rows, func(x struct {
			Email    string `json:"email"`
			Verified bool   `json:"verified"`
		}) bool {
			return x.Verified && strings.EqualFold(x.Email, email)
		}) {
			return true
		}
	}
	return false
}
func (s *Server) fillEntryTrigger(t *entry.Trigger, c entry.Connection, p Project) {
	t.ConnectionID = c.ID
	t.Tenant = c.Tenant
	t.User = c.User
	t.ProjectID = c.ProjectID
	t.Kind = c.Kind
	t.ReservedUSD = c.Limits.TaskUSD
	if t.State == "" {
		t.State = "accepted"
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = s.now().UTC()
	}
	t.UpdatedAt = t.CreatedAt
	t.NextAt = time.Now().UTC()
	if c.Kind == "email" {
		if e, err := entrymail.DecodeEnvelope(t.Payload); err == nil {
			t.SourceAddress = entrymail.ReplyAddress(e)
		}
	}
	t.Scope = c.Tenant + "/" + p.ID
	if p.Provider == "github" {
		repo := fmt.Sprintf("github/%d", p.RepoID)
		if p.RepoID == 0 {
			repo = "github/" + strings.ToLower(p.Repo)
		}
		// Only repository writers can start comment runs, so they share one
		// repository-wide budget. Schedules, alerts and email need no write access;
		// keep them per tenant so another tenant cannot exhaust the maintainers' runs.
		t.Scope = repo
		if c.Kind != "github_comment" {
			t.Scope = c.Tenant + "/" + repo
		}
	}
}
