package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
func (s *Server) entryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/entry/kinds", s.authed(s.handleEntryKinds))
	mux.HandleFunc("GET /api/entry/connections", s.authed(s.handleEntryConnections))
	mux.HandleFunc("POST /api/entry/connections", s.authed(s.handleEntrySave))
	mux.HandleFunc("PUT /api/entry/connections/{connection}", s.authed(s.handleEntrySave))
	mux.HandleFunc("DELETE /api/entry/connections/{connection}", s.authed(s.handleEntryDelete))
	mux.HandleFunc("POST /api/entry/connections/{connection}/rotate-secret", s.authed(s.handleEntryRotate))
	mux.HandleFunc("GET /api/entry/connections/{connection}/triggers", s.authed(s.handleEntryRuns))
	mux.HandleFunc("GET /api/entry/runs", s.authed(s.handleEntryRuns))
	mux.HandleFunc("POST /api/entry/deliveries/{delivery}/retry", s.authed(s.handleEntryReplay))
	mux.HandleFunc("POST /api/entry/triggers/{trigger}/confirm", s.authed(s.handleEntryConfirm))
	mux.HandleFunc("POST /api/webhooks/entry/{connection}", s.handleEntryWebhook)
}
func (s *Server) entryAvailable(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.EntryStore == nil {
		writeMsg(w, r, 503, "接入点尚未配置数据库")
		return false
	}
	return true
}
func entryError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, entry.ErrNotFound):
		writeMsg(w, r, 404, "接入点记录不存在")
	case errors.Is(err, entry.ErrInvalid):
		writeMsg(w, r, 400, "接入点配置无效，请检查字段与限制")
	case errors.Is(err, entry.ErrConflict), errors.Is(err, entry.ErrLeaseLost):
		writeMsg(w, r, 409, "接入点记录已变化，请刷新后重试")
	default:
		writeMsg(w, r, 503, "接入点服务暂时不可用")
	}
}
func (s *Server) handleEntryKinds(w http.ResponseWriter, r *http.Request, id identity) {
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
	writeJSON(w, 200, map[string]any{"kinds": kinds, "available": s.cfg.EntryStore != nil, "email_delivery_available": s.cfg.EntryMail != nil})
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
func (s *Server) handleEntryConnections(w http.ResponseWriter, r *http.Request, id identity) {
	if !s.entryAvailable(w, r) {
		return
	}
	cs, err := s.cfg.EntryStore.Connections(r.Context(), id.TenantID, id.UserID)
	if err != nil {
		entryError(w, r, err)
		return
	}
	out := []entryConnectionDTO{}
	for _, c := range cs {
		out = append(out, s.entryDTO(c))
	}
	writeJSON(w, 200, map[string]any{"connections": out})
}

var entryMention = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func (s *Server) handleEntrySave(w http.ResponseWriter, r *http.Request, id identity) {
	if !s.entryAvailable(w, r) {
		return
	}
	var raw json.RawMessage
	if !readJSON(w, r, &raw) {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		entryError(w, r, entry.ErrInvalid)
		return
	}
	var d entryConnectionDTO
	now := s.now().UTC()
	c := entry.Connection{ID: entry.NewID(), Tenant: id.TenantID, User: id.UserID, CreatedAt: now, Limits: entry.DefaultLimits()}
	if r.Method == http.MethodPut {
		var err error
		c, err = s.cfg.EntryStore.Connection(r.Context(), id.TenantID, id.UserID, r.PathValue("connection"))
		if err != nil {
			entryError(w, r, err)
			return
		}
		d = s.entryDTO(c)
	}
	if json.Unmarshal(raw, &d) != nil {
		entryError(w, r, entry.ErrInvalid)
		return
	}
	if r.Method == http.MethodPut {
		if d.Kind != c.Kind || d.ProjectID != c.ProjectID {
			entryError(w, r, entry.ErrInvalid)
			return
		}
	}
	c.Name, c.Kind, c.ProjectID, c.Config = d.Name, d.Kind, d.ProjectID, d.Config
	c.Enabled = r.Method == http.MethodPut && d.Enabled
	c.UpdatedAt = now
	if c.Config == nil {
		c.Config = map[string]any{}
	}
	kind := s.entryKind(c.Kind)
	if kind == nil {
		entryError(w, r, entry.ErrInvalid)
		return
	}
	for _, f := range kind.Schema().Fields {
		if _, ok := c.Config[f.Name]; !ok && f.Default != nil {
			c.Config[f.Name] = f.Default
		}
	}
	if _, present := fields["language"]; present && d.Language != "" {
		c.Config["language"] = d.Language
	}
	if _, present := fields["autonomy"]; present && d.Autonomy != "" {
		if d.Autonomy == "read" {
			d.Autonomy = "analyze"
		}
		c.Config["autonomy"] = d.Autonomy
	}
	if _, present := fields["runs_per_hour"]; present {
		c.Limits.RunsPerHour = d.RunsPerHour
	}
	if _, present := fields["budget_usd_per_hour"]; present {
		c.Limits.USDPerHour = d.BudgetUSDPerHour
	}
	if _, present := fields["task_budget_usd"]; present {
		c.Limits.TaskUSD = d.TaskBudgetUSD
	}
	if _, explicit := fields["task_budget_usd"]; !explicit && c.Limits.TaskUSD > c.Limits.USDPerHour {
		c.Limits.TaskUSD = c.Limits.USDPerHour
	}
	c.Sinks = nil
	for _, out := range d.Sinks {
		sink := out.Kind
		if sink == "" {
			sink = out.Sink
		}
		c.Sinks = append(c.Sinks, entry.Destination{Sink: sink, Address: out.Address, Phases: out.Phases})
	}
	p, ok, err := s.store.Project(id.TenantID, c.ProjectID)
	if err != nil {
		entryError(w, r, err)
		return
	}
	if !ok || p.Layout == layoutMulti {
		entryError(w, r, entry.ErrInvalid)
		return
	}
	if entryConfig(c, "autonomy") != "analyze" && (p.Provider == "" || s.providers[p.Provider] == nil || p.Repo == "") {
		entryError(w, r, entry.ErrInvalid)
		return
	}
	if err = kind.Schema().Validate(c.Config); err == nil {
		err = entry.ValidateConnection(c)
	}
	if err == nil {
		switch c.Kind {
		case "schedule":
			if schedule.Validate(entryConfig(c, "cron")) != nil {
				err = entry.ErrInvalid
			}
		case "alert":
			err = alert.Validate(c.Config)
		case "email":
			err = entrymail.Validate(c.Config)
		}
	}
	if err != nil {
		entryError(w, r, err)
		return
	}
	if c.Kind == "github_comment" {
		if s.cfg.GitHubApp == nil || p.Provider != "github" || !entryMention.MatchString(entryConfig(c, "mention")) {
			entryError(w, r, entry.ErrInvalid)
			return
		}
		cs, e := s.cfg.EntryStore.Connections(r.Context(), id.TenantID, id.UserID)
		if e != nil {
			entryError(w, r, e)
			return
		}
		for _, x := range cs {
			if x.ID != c.ID && x.Kind == c.Kind && x.ProjectID == c.ProjectID && strings.EqualFold(entryConfig(x, "mention"), entryConfig(c, "mention")) {
				entryError(w, r, entry.ErrConflict)
				return
			}
		}
	}
	for i, out := range c.Sinks {
		switch out.Sink {
		case "github_comment":
			if c.Kind == "github_comment" && out.Address.Repo == "" && out.Address.Issue == 0 {
				continue
			}
			if p.Provider != "github" || !strings.EqualFold(out.Address.Repo, p.Repo) || out.Address.Issue < 1 {
				entryError(w, r, entry.ErrInvalid)
				return
			}
			grant, e := s.authorizeRepo(r, id, &p, repoWrite)
			if e != nil {
				writeErr(w, r, 403, e)
				return
			}
			grant.Done()
			c.Sinks[i].Address.RepoID = p.RepoID
			c.Sinks[i].Address.Installation = p.InstallationID
		case "email":
			if c.Enabled && s.cfg.EntryMail == nil {
				entryError(w, r, entry.ErrInvalid)
				return
			}
			if c.Kind == "email" && out.Address.Email == "" {
				continue
			} // Reply to the independently verified original sender.
			if !s.verifiedEntrySender(r.Context(), c, out.Address.Email) {
				entryError(w, r, entry.ErrInvalid)
				return
			}
		}
	}
	secret := ""
	if r.Method == http.MethodPost && (c.Kind == "alert" || c.Kind == "email") {
		secret, c.SecretHash = entry.NewSecret()
	}
	if err = s.cfg.EntryStore.PutConnection(r.Context(), c); err != nil {
		entryError(w, r, err)
		return
	}
	status := 200
	if r.Method == http.MethodPost {
		status = 201
	}
	out := map[string]any{"connection": s.entryDTO(c)}
	if secret != "" {
		out["secret"] = secret
		w.Header().Set("Cache-Control", "no-store")
	}
	writeJSON(w, status, out)
}
func (s *Server) handleEntryDelete(w http.ResponseWriter, r *http.Request, id identity) {
	if !s.entryAvailable(w, r) {
		return
	}
	if err := s.cfg.EntryStore.DeleteConnection(r.Context(), id.TenantID, id.UserID, r.PathValue("connection")); err != nil {
		entryError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleEntryRotate(w http.ResponseWriter, r *http.Request, id identity) {
	if !s.entryAvailable(w, r) {
		return
	}
	c, err := s.cfg.EntryStore.Connection(r.Context(), id.TenantID, id.UserID, r.PathValue("connection"))
	if err != nil {
		entryError(w, r, err)
		return
	}
	if c.Kind != "alert" && c.Kind != "email" {
		entryError(w, r, entry.ErrInvalid)
		return
	}
	secret, hash := entry.NewSecret()
	c, err = s.cfg.EntryStore.RotateSecret(r.Context(), id.TenantID, id.UserID, c.ID, hash, s.now().UTC())
	if err != nil {
		entryError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"connection": s.entryDTO(c), "secret": secret})
}
func (s *Server) handleEntryRuns(w http.ResponseWriter, r *http.Request, id identity) {
	if !s.entryAvailable(w, r) {
		return
	}
	connection := r.PathValue("connection")
	if connection != "" {
		if _, err := s.cfg.EntryStore.Connection(r.Context(), id.TenantID, id.UserID, connection); err != nil {
			entryError(w, r, err)
			return
		}
	}
	var ts []entry.Trigger
	var err error
	if session := r.URL.Query().Get("session"); session != "" {
		ts, err = s.cfg.EntryStore.SessionTriggers(r.Context(), id.TenantID, id.UserID, session)
	} else {
		ts, err = s.cfg.EntryStore.Triggers(r.Context(), id.TenantID, id.UserID, connection, 500)
	}
	if err != nil {
		entryError(w, r, err)
		return
	}
	active, e := s.cfg.EntryStore.ActiveTriggers(r.Context(), id.TenantID, id.UserID)
	if e != nil {
		entryError(w, r, e)
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
		if sid := r.URL.Query().Get("session"); sid != "" && t.SessionID != sid {
			continue
		}
		out = append(out, t)
		rows, e := s.cfg.EntryStore.Deliveries(r.Context(), id.TenantID, id.UserID, t.ID, 100)
		if e != nil {
			entryError(w, r, e)
			return
		}
		ds = append(ds, rows...)
	}
	writeJSON(w, 200, map[string]any{"triggers": out, "deliveries": ds})
}
func (s *Server) handleEntryReplay(w http.ResponseWriter, r *http.Request, id identity) {
	if !s.entryAvailable(w, r) {
		return
	}
	if err := s.cfg.EntryStore.ReplayDelivery(r.Context(), id.TenantID, id.UserID, r.PathValue("delivery"), s.now().UTC()); err != nil {
		entryError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"queued": true})
}
func (s *Server) handleEntryConfirm(w http.ResponseWriter, r *http.Request, id identity) {
	if !s.entryAvailable(w, r) {
		return
	}
	t, err := s.cfg.EntryStore.Trigger(r.Context(), r.PathValue("trigger"))
	if err != nil || t.Tenant != id.TenantID || t.User != id.UserID {
		entryError(w, r, entry.ErrNotFound)
		return
	}
	_, changed, err := s.cfg.EntryStore.MutateTrigger(r.Context(), t.ID, 0, func(x *entry.Trigger) bool {
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
		entryError(w, r, err)
		return
	}
	if !changed {
		entryError(w, r, entry.ErrConflict)
		return
	}
	writeJSON(w, 200, map[string]bool{"confirmed": true})
}
func (s *Server) handleEntryWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EntryStore == nil {
		w.WriteHeader(503)
		return
	}
	c, err := s.cfg.EntryStore.LookupConnection(r.Context(), r.PathValue("connection"))
	if err != nil || c.Config["deleted"] == true || c.Kind != "alert" && c.Kind != "email" {
		w.WriteHeader(401)
		return
	}
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || !entry.VerifySecret(c.SecretHash, token) {
		s.metrics().Count("coding.entry.webhooks", 1, map[string]string{"result": "unauthorized"})
		w.WriteHeader(401)
		return
	}
	limit := int64(alert.MaxBody)
	if c.Kind == "email" {
		limit = entrymail.MaxBody
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		w.WriteHeader(413)
		return
	}
	kind := s.entryKind(c.Kind)
	ts, err := kind.Ingest(r.Context(), entry.Inbound{Body: body, Header: r.Header, Now: s.now().UTC(), Connection: c, Trusted: true})
	if err != nil {
		w.WriteHeader(400)
		return
	}
	p, found, err := s.store.Project(c.Tenant, c.ProjectID)
	if err != nil || !found {
		w.WriteHeader(503)
		return
	}
	for _, t := range ts {
		a := kind.Admit(r.Context(), c, t)
		s.fillEntryTrigger(&t, c, p)
		if !a.Allowed {
			t.State, t.Reason, t.ReservedUSD = "refused", a.Reason, 0
			t.Payload, t.Instruction, t.Materials = nil, "", ""
		}
		if _, err = s.cfg.EntryStore.Enqueue(r.Context(), t); err != nil {
			w.WriteHeader(503)
			return
		}
	}
	s.metrics().Count("coding.entry.webhooks", 1, map[string]string{"kind": c.Kind, "result": "accepted"})
	w.WriteHeader(202)
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
		t.Scope = fmt.Sprintf("github/%d", p.RepoID)
		if p.RepoID == 0 {
			t.Scope = "github/" + strings.ToLower(p.Repo)
		}
	}
}
