package adminops

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/adapters/postgres"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/relational"
)

const EvolutionPurpose = "evolution"

var (
	ErrCandidateNotFound      = errors.New("evolution candidate not found")
	ErrCandidateNotPromotable = errors.New("candidate is not pending_approval or failed gate")
	ErrCandidateNotPromoted   = errors.New("candidate is not promoted")
	ErrMissingBaseline        = errors.New("candidate is missing baseline snapshot")
	ErrRegistryUnavailable    = errors.New("registry publish endpoint unavailable or rejected")
)

var evolutionMigrations = []relational.Migration{
	{
		Version: 301,
		Name:    "evolution_candidates",
		Statements: []string{
			`CREATE TABLE evolution_candidates (
				candidate_id TEXT PRIMARY KEY,
				target_kind TEXT NOT NULL,
				target_name TEXT NOT NULL,
				status TEXT NOT NULL,
				baseline_version TEXT NOT NULL,
				baseline_document TEXT NOT NULL,
				candidate_document TEXT NOT NULL,
				unified_diff TEXT NOT NULL,
				cluster_summary TEXT NOT NULL,
				matrix_report TEXT NOT NULL,
				staged_skill_name TEXT NOT NULL,
				promoted_version TEXT NOT NULL,
				rollback_version TEXT NOT NULL,
				approved_by TEXT NOT NULL,
				created_at BIGINT NOT NULL,
				updated_at BIGINT NOT NULL
			)`,
		},
	},
	{
		Version: 302,
		Name:    "evolution_audit_log",
		Statements: []string{
			`CREATE TABLE evolution_audit_log (
				audit_id TEXT PRIMARY KEY,
				candidate_id TEXT NOT NULL,
				action TEXT NOT NULL,
				actor TEXT NOT NULL,
				target_name TEXT NOT NULL,
				before_version TEXT NOT NULL,
				after_version TEXT NOT NULL,
				reason TEXT NOT NULL,
				created_at BIGINT NOT NULL
			)`,
		},
	},
}

// AuditEntry records an operator governance action (import, promote, rollback, trigger_run).
type AuditEntry struct {
	AuditID       string `json:"audit_id"`
	CandidateID   string `json:"candidate_id,omitempty"`
	Action        string `json:"action"`
	Actor         string `json:"actor"`
	TargetName    string `json:"target_name"`
	BeforeVersion string `json:"before_version,omitempty"`
	AfterVersion  string `json:"after_version,omitempty"`
	Reason        string `json:"reason,omitempty"`
	CreatedAt     int64  `json:"created_at"`
}

// CandidateRecord mirrors agentruntime_learning.evolution.store.CandidateRecord.
type CandidateRecord struct {
	CandidateID       string         `json:"candidate_id"`
	TargetKind        string         `json:"target_kind"`
	TargetName        string         `json:"target_name"`
	Status            string         `json:"status"`
	BaselineVersion   string         `json:"baseline_version"`
	BaselineDocument  map[string]any `json:"baseline_document"`
	CandidateDocument map[string]any `json:"candidate_document"`
	UnifiedDiff       string         `json:"unified_diff"`
	ClusterSummary    map[string]any `json:"cluster_summary"`
	MatrixReport      map[string]any `json:"matrix_report"`
	StagedSkillName   string         `json:"staged_skill_name"`
	PromotedVersion   string         `json:"promoted_version"`
	RollbackVersion   string         `json:"rollback_version"`
	ApprovedBy        string         `json:"approved_by"`
	CreatedAt         int64          `json:"created_at"`
	UpdatedAt         int64          `json:"updated_at"`
}

func (c CandidateRecord) GatePassed() bool {
	if c.MatrixReport == nil {
		return false
	}
	gate, _ := c.MatrixReport["gate"].(map[string]any)
	passed, _ := gate["passed"].(bool)
	return passed
}

type EvolutionRepo interface {
	List(ctx context.Context, status string) ([]CandidateRecord, error)
	Get(ctx context.Context, id string) (CandidateRecord, error)
	Upsert(ctx context.Context, rec CandidateRecord) (CandidateRecord, error)
	RecordAudit(ctx context.Context, entry AuditEntry) error
	ListAudit(ctx context.Context, limit int) ([]AuditEntry, error)
	Close() error
}

// --- PostgresRepo (matches Wave 2 EvolutionStore bind_platform("evolution")) ---

type PostgresRepo struct {
	provider *postgres.Provider
	store    relational.Store
}

func OpenPostgresRepo(ctx context.Context, dsn string) (*PostgresRepo, error) {
	p, err := postgres.New(ctx, postgres.Options{DSN: dsn, MaxConns: 4})
	if err != nil {
		return nil, err
	}
	if err := p.Migrate(ctx, evolutionMigrations); err != nil {
		_ = p.Close()
		return nil, err
	}
	st, err := p.BindPlatform(EvolutionPurpose)
	if err != nil {
		_ = p.Close()
		return nil, err
	}
	return &PostgresRepo{provider: p, store: st}, nil
}

func (r *PostgresRepo) Close() error {
	if r == nil || r.provider == nil {
		return nil
	}
	return r.provider.Close()
}

func decodeJSONMap(raw any) map[string]any {
	s, _ := raw.(string)
	if strings.TrimSpace(s) == "" {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal([]byte(s), &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func encodeJSONMap(m map[string]any) string {
	if m == nil {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	default:
		return 0
	}
}

func rowToCandidate(row relational.Row) CandidateRecord {
	str := func(k string) string { s, _ := row[k].(string); return s }
	return CandidateRecord{
		CandidateID:       str("candidate_id"),
		TargetKind:        str("target_kind"),
		TargetName:        str("target_name"),
		Status:            str("status"),
		BaselineVersion:   str("baseline_version"),
		BaselineDocument:  decodeJSONMap(row["baseline_document"]),
		CandidateDocument: decodeJSONMap(row["candidate_document"]),
		UnifiedDiff:       str("unified_diff"),
		ClusterSummary:    decodeJSONMap(row["cluster_summary"]),
		MatrixReport:      decodeJSONMap(row["matrix_report"]),
		StagedSkillName:   str("staged_skill_name"),
		PromotedVersion:   str("promoted_version"),
		RollbackVersion:   str("rollback_version"),
		ApprovedBy:        str("approved_by"),
		CreatedAt:         asInt64(row["created_at"]),
		UpdatedAt:         asInt64(row["updated_at"]),
	}
}

func (r *PostgresRepo) List(ctx context.Context, status string) ([]CandidateRecord, error) {
	var out []CandidateRecord
	err := r.store.Transaction(ctx, relational.TxOptions{ReadOnly: true}, func(ctx context.Context, tx relational.Tx) error {
		q := `SELECT candidate_id, target_kind, target_name, status, baseline_version,
		             baseline_document, candidate_document, unified_diff, cluster_summary,
		             matrix_report, staged_skill_name, promoted_version, rollback_version,
		             approved_by, created_at, updated_at
		        FROM evolution_candidates`
		var rows []relational.Row
		var err error
		if strings.TrimSpace(status) != "" {
			rows, err = tx.Query(ctx, q+` WHERE status = $1 ORDER BY created_at DESC, candidate_id DESC`, strings.TrimSpace(status))
		} else {
			rows, err = tx.Query(ctx, q+` ORDER BY created_at DESC, candidate_id DESC`)
		}
		if err != nil {
			return err
		}
		out = make([]CandidateRecord, 0, len(rows))
		for _, row := range rows {
			out = append(out, rowToCandidate(row))
		}
		return nil
	})
	return out, err
}

func (r *PostgresRepo) Get(ctx context.Context, id string) (CandidateRecord, error) {
	var rec CandidateRecord
	err := r.store.Transaction(ctx, relational.TxOptions{ReadOnly: true}, func(ctx context.Context, tx relational.Tx) error {
		row, ok, err := tx.QueryOne(ctx, `SELECT candidate_id, target_kind, target_name, status, baseline_version,
		             baseline_document, candidate_document, unified_diff, cluster_summary,
		             matrix_report, staged_skill_name, promoted_version, rollback_version,
		             approved_by, created_at, updated_at
		        FROM evolution_candidates WHERE candidate_id = $1`, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrCandidateNotFound
		}
		rec = rowToCandidate(row)
		return nil
	})
	return rec, err
}

func (r *PostgresRepo) Upsert(ctx context.Context, rec CandidateRecord) (CandidateRecord, error) {
	if strings.TrimSpace(rec.CandidateID) == "" {
		rec.CandidateID = newCandidateID()
	}
	now := time.Now().UTC().UnixMilli()
	if rec.CreatedAt == 0 {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	err := r.store.Transaction(ctx, relational.TxOptions{}, func(ctx context.Context, tx relational.Tx) error {
		_, ok, err := tx.QueryOne(ctx, `SELECT candidate_id FROM evolution_candidates WHERE candidate_id = $1`, rec.CandidateID)
		if err != nil {
			return err
		}
		if !ok {
			_, err = tx.Execute(ctx, `INSERT INTO evolution_candidates (
				candidate_id, target_kind, target_name, status, baseline_version,
				baseline_document, candidate_document, unified_diff, cluster_summary,
				matrix_report, staged_skill_name, promoted_version, rollback_version,
				approved_by, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
				rec.CandidateID, rec.TargetKind, rec.TargetName, rec.Status, rec.BaselineVersion,
				encodeJSONMap(rec.BaselineDocument), encodeJSONMap(rec.CandidateDocument), rec.UnifiedDiff,
				encodeJSONMap(rec.ClusterSummary), encodeJSONMap(rec.MatrixReport),
				rec.StagedSkillName, rec.PromotedVersion, rec.RollbackVersion, rec.ApprovedBy,
				rec.CreatedAt, rec.UpdatedAt,
			)
			return err
		}
		_, err = tx.Execute(ctx, `UPDATE evolution_candidates
			SET status = $1, matrix_report = $2, staged_skill_name = $3,
			    promoted_version = $4, rollback_version = $5, approved_by = $6, updated_at = $7
			WHERE candidate_id = $8`,
			rec.Status, encodeJSONMap(rec.MatrixReport), rec.StagedSkillName,
			rec.PromotedVersion, rec.RollbackVersion, rec.ApprovedBy, rec.UpdatedAt, rec.CandidateID,
		)
		return err
	})
	if err != nil {
		return CandidateRecord{}, err
	}
	return r.Get(ctx, rec.CandidateID)
}

func (r *PostgresRepo) RecordAudit(ctx context.Context, entry AuditEntry) error {
	if strings.TrimSpace(entry.AuditID) == "" {
		entry.AuditID = newAuditID()
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = time.Now().UTC().UnixMilli()
	}
	return r.store.Transaction(ctx, relational.TxOptions{}, func(ctx context.Context, tx relational.Tx) error {
		_, err := tx.Execute(ctx, `INSERT INTO evolution_audit_log (
			audit_id, candidate_id, action, actor, target_name, before_version, after_version, reason, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			entry.AuditID, entry.CandidateID, entry.Action, entry.Actor, entry.TargetName,
			entry.BeforeVersion, entry.AfterVersion, entry.Reason, entry.CreatedAt,
		)
		return err
	})
}

func (r *PostgresRepo) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []AuditEntry
	err := r.store.Transaction(ctx, relational.TxOptions{ReadOnly: true}, func(ctx context.Context, tx relational.Tx) error {
		rows, err := tx.Query(ctx, `SELECT audit_id, candidate_id, action, actor, target_name, before_version, after_version, reason, created_at
			FROM evolution_audit_log ORDER BY created_at DESC, audit_id DESC LIMIT $1`, int64(limit))
		if err != nil {
			return err
		}
		str := func(row relational.Row, k string) string { s, _ := row[k].(string); return s }
		out = make([]AuditEntry, 0, len(rows))
		for _, row := range rows {
			out = append(out, AuditEntry{
				AuditID:       str(row, "audit_id"),
				CandidateID:   str(row, "candidate_id"),
				Action:        str(row, "action"),
				Actor:         str(row, "actor"),
				TargetName:    str(row, "target_name"),
				BeforeVersion: str(row, "before_version"),
				AfterVersion:  str(row, "after_version"),
				Reason:        str(row, "reason"),
				CreatedAt:     asInt64(row["created_at"]),
			})
		}
		return nil
	})
	return out, err
}

// --- FileRepo fallback for dev / unit tests ---

type FileRepo struct {
	path string
	mu   sync.Mutex
}

func OpenFileRepo(path string) (*FileRepo, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &FileRepo{path: path}, nil
}

func (f *FileRepo) Close() error { return nil }

func (f *FileRepo) readAllLocked() ([]CandidateRecord, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return []CandidateRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return []CandidateRecord{}, nil
	}
	var rows []CandidateRecord
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (f *FileRepo) writeAllLocked(rows []CandidateRecord) error {
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *FileRepo) List(_ context.Context, status string) ([]CandidateRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows, err := f.readAllLocked()
	if err != nil {
		return nil, err
	}
	want := strings.TrimSpace(status)
	var out []CandidateRecord
	for _, r := range rows {
		if want == "" || r.Status == want {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].CandidateID > out[j].CandidateID
	})
	if out == nil {
		out = []CandidateRecord{}
	}
	return out, nil
}

func (f *FileRepo) Get(_ context.Context, id string) (CandidateRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows, err := f.readAllLocked()
	if err != nil {
		return CandidateRecord{}, err
	}
	for _, r := range rows {
		if r.CandidateID == id {
			return r, nil
		}
	}
	return CandidateRecord{}, ErrCandidateNotFound
}

func (f *FileRepo) Upsert(_ context.Context, rec CandidateRecord) (CandidateRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows, err := f.readAllLocked()
	if err != nil {
		return CandidateRecord{}, err
	}
	if strings.TrimSpace(rec.CandidateID) == "" {
		rec.CandidateID = newCandidateID()
	}
	now := time.Now().UTC().UnixMilli()
	if rec.CreatedAt == 0 {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	replaced := false
	for i := range rows {
		if rows[i].CandidateID == rec.CandidateID {
			rows[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		rows = append(rows, rec)
	}
	if err := f.writeAllLocked(rows); err != nil {
		return CandidateRecord{}, err
	}
	return rec, nil
}

func (f *FileRepo) auditPath() string {
	return filepath.Join(filepath.Dir(f.path), "evolution_audit.json")
}

func (f *FileRepo) readAuditLocked() ([]AuditEntry, error) {
	data, err := os.ReadFile(f.auditPath())
	if errors.Is(err, os.ErrNotExist) {
		return []AuditEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return []AuditEntry{}, nil
	}
	var rows []AuditEntry
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (f *FileRepo) RecordAudit(_ context.Context, entry AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows, err := f.readAuditLocked()
	if err != nil {
		return err
	}
	if strings.TrimSpace(entry.AuditID) == "" {
		entry.AuditID = newAuditID()
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = time.Now().UTC().UnixMilli()
	}
	if len(rows) > 0 && entry.CreatedAt <= rows[len(rows)-1].CreatedAt {
		entry.CreatedAt = rows[len(rows)-1].CreatedAt + 1
	}
	rows = append(rows, entry)
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.auditPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.auditPath())
}

func (f *FileRepo) ListAudit(_ context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rows, err := f.readAuditLocked()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt > rows[j].CreatedAt
		}
		return rows[i].AuditID > rows[j].AuditID
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func newCandidateID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "cand_" + hex.EncodeToString(b)
}

func newAuditID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "aud_" + hex.EncodeToString(b)
}

// --- RegistryAdminClient (Connect HTTP+JSON over internal Registry port 8092) ---

type RegistrySkillSummary struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	Shadowed    bool   `json:"shadowed,omitempty"`
}

type RegistryOverview struct {
	Configured         bool                   `json:"configured"`
	Available          bool                   `json:"available"`
	PlatformSkills     []RegistrySkillSummary `json:"platform_skills"`
	SysEvolutionSkills []RegistrySkillSummary `json:"sys_evolution_skills"`
	SysEvolutionPolicy map[string]any         `json:"sys_evolution_policy,omitempty"`
	Error              string                 `json:"error,omitempty"`
}

type RegistryAdminClient struct {
	baseURL      string
	publishToken string
	http         *http.Client
}

func NewRegistryAdminClient(baseURL, publishToken string) *RegistryAdminClient {
	return &RegistryAdminClient{
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		publishToken: strings.TrimSpace(publishToken),
		http:         &http.Client{Timeout: 8 * time.Second},
	}
}

func (c *RegistryAdminClient) Configured() bool {
	return c != nil && c.baseURL != ""
}

func (c *RegistryAdminClient) PublishConfigured() bool {
	return c.Configured() && c.publishToken != ""
}

func (c *RegistryAdminClient) Overview(ctx context.Context) RegistryOverview {
	out := RegistryOverview{
		Configured:         c.Configured(),
		PlatformSkills:     []RegistrySkillSummary{},
		SysEvolutionSkills: []RegistrySkillSummary{},
	}
	if !c.Configured() {
		return out
	}
	var skillsResp struct {
		Skills []struct {
			Name        string `json:"name"`
			Version     string `json:"version"`
			Scope       string `json:"scope"`
			Description string `json:"description"`
			Shadowed    bool   `json:"shadowed"`
		} `json:"skills"`
	}
	err := c.call(ctx, "ListSkills", "", map[string]any{
		"tenant":   map[string]any{"tenantId": "sys-evolution"},
		"pageSize": 500,
	}, &skillsResp)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Available = true
	for _, s := range skillsResp.Skills {
		item := RegistrySkillSummary{
			Name:        s.Name,
			Version:     s.Version,
			Scope:       strings.ToLower(strings.TrimPrefix(s.Scope, "SKILL_SCOPE_")),
			Description: s.Description,
			Shadowed:    s.Shadowed,
		}
		if item.Scope == "platform" {
			out.PlatformSkills = append(out.PlatformSkills, item)
		} else if strings.HasPrefix(item.Name, "cand-") {
			out.SysEvolutionSkills = append(out.SysEvolutionSkills, item)
		}
	}
	var polResp struct {
		TenantPolicy struct {
			Version  string `json:"version"`
			Document struct {
				Value map[string]any `json:"value"`
			} `json:"document"`
		} `json:"tenantPolicy"`
	}
	if err := c.call(ctx, "GetTenantPolicy", "", map[string]any{
		"tenant": map[string]any{"tenantId": "sys-evolution"},
	}, &polResp); err == nil {
		out.SysEvolutionPolicy = map[string]any{
			"version":  polResp.TenantPolicy.Version,
			"document": polResp.TenantPolicy.Document.Value,
		}
	}
	return out
}

func (c *RegistryAdminClient) PublishPlatformSkill(ctx context.Context, name string, doc map[string]any, note string) (string, error) {
	if !c.PublishConfigured() {
		return "", ErrRegistryUnavailable
	}
	var resp struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	err := c.call(ctx, "Publish", c.publishToken, map[string]any{
		"kind":       "REGISTRY_KIND_SKILL",
		"name":       name,
		"document":   map[string]any{"value": doc},
		"changeNote": note,
	}, &resp)
	if err != nil {
		return "", err
	}
	return resp.Version, nil
}

func (c *RegistryAdminClient) call(ctx context.Context, method, bearer string, reqBody, respBody any) error {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/agentruntime.registry.v1.RegistryService/"+method, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRegistryUnavailable, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: http %d: %s", ErrRegistryUnavailable, res.StatusCode, strings.TrimSpace(string(data)))
	}
	if respBody == nil {
		return nil
	}
	return json.Unmarshal(data, respBody)
}

// --- EvolutionRunnerClient controls on-demand self-evolution cycles ---

var ErrRunnerUnavailable = errors.New("evolution runner service unavailable or rejected request")

type EvolutionRunnerClient struct {
	baseURL      string
	token        string
	maxBudgetUSD float64
	http         *http.Client
}

func NewEvolutionRunnerClient(baseURL, token string, maxBudgetUSD float64) *EvolutionRunnerClient {
	if maxBudgetUSD <= 0 {
		maxBudgetUSD = 1.0
	}
	return &EvolutionRunnerClient{
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:        strings.TrimSpace(token),
		maxBudgetUSD: maxBudgetUSD,
		http:         &http.Client{Timeout: 8 * time.Second},
	}
}

func (c *EvolutionRunnerClient) Configured() bool {
	return c != nil && c.baseURL != ""
}

func (c *EvolutionRunnerClient) MaxBudgetUSD() float64 {
	if c == nil || c.maxBudgetUSD <= 0 {
		return 1.0
	}
	return c.maxBudgetUSD
}

func (c *EvolutionRunnerClient) ListRuns(ctx context.Context) ([]map[string]any, error) {
	if !c.Configured() {
		return []map[string]any{}, nil
	}
	var out struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/runs", nil, &out); err != nil {
		return nil, err
	}
	if out.Runs == nil {
		return []map[string]any{}, nil
	}
	return out.Runs, nil
}

func (c *EvolutionRunnerClient) GetRun(ctx context.Context, runID string) (map[string]any, error) {
	if !c.Configured() {
		return nil, ErrRunnerUnavailable
	}
	var out map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/v1/runs/"+strings.TrimSpace(runID), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *EvolutionRunnerClient) TriggerRun(ctx context.Context, mode string, budgetUSD float64, minSupport int) (map[string]any, error) {
	if !c.Configured() {
		return nil, ErrRunnerUnavailable
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "fixture"
	}
	if mode != "fixture" && mode != "gateway" {
		return nil, fmt.Errorf("%w: mode must be fixture or gateway", ErrRunnerUnavailable)
	}
	if budgetUSD < 0 || budgetUSD > c.MaxBudgetUSD() {
		return nil, fmt.Errorf("%w: budget_usd must be between 0 and %.2f", ErrRunnerUnavailable, c.MaxBudgetUSD())
	}
	if minSupport <= 0 {
		minSupport = 2
	}
	var out map[string]any
	err := c.doJSON(ctx, http.MethodPost, "/v1/runs", map[string]any{
		"mode":        mode,
		"budget_usd":  budgetUSD,
		"min_support": minSupport,
	}, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *EvolutionRunnerClient) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRunnerUnavailable, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: http %d: %s", ErrRunnerUnavailable, res.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// --- EvolutionManager coordinates store + optional Registry promotion/rollback ---

type EvolutionManager struct {
	repo     EvolutionRepo
	registry *RegistryAdminClient
}

func NewEvolutionManager(repo EvolutionRepo, registry *RegistryAdminClient) *EvolutionManager {
	return &EvolutionManager{repo: repo, registry: registry}
}

func (m *EvolutionManager) Close() error {
	if m == nil || m.repo == nil {
		return nil
	}
	return m.repo.Close()
}

func (m *EvolutionManager) List(ctx context.Context, status string) ([]CandidateRecord, error) {
	if m == nil || m.repo == nil {
		return []CandidateRecord{}, nil
	}
	return m.repo.List(ctx, status)
}

func (m *EvolutionManager) Get(ctx context.Context, id string) (CandidateRecord, error) {
	if m == nil || m.repo == nil {
		return CandidateRecord{}, ErrCandidateNotFound
	}
	return m.repo.Get(ctx, id)
}

func (m *EvolutionManager) Import(ctx context.Context, rec CandidateRecord) (CandidateRecord, error) {
	if m == nil || m.repo == nil {
		return CandidateRecord{}, errors.New("evolution store unavailable")
	}
	if rec.Status == "" {
		if rec.GatePassed() {
			rec.Status = "pending_approval"
		} else {
			rec.Status = "rejected_by_gate"
		}
	}
	if rec.TargetKind == "" {
		rec.TargetKind = "skill"
	}
	saved, err := m.repo.Upsert(ctx, rec)
	if err == nil {
		_ = m.repo.RecordAudit(ctx, AuditEntry{
			CandidateID:   saved.CandidateID,
			Action:        "import",
			Actor:         emptyStr(saved.ApprovedBy, "evolution-pipeline"),
			TargetName:    saved.TargetName,
			BeforeVersion: saved.BaselineVersion,
			AfterVersion:  saved.Status,
			Reason:        saved.StagedSkillName,
		})
	}
	return saved, err
}

func emptyStr(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func (m *EvolutionManager) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if m == nil || m.repo == nil {
		return []AuditEntry{}, nil
	}
	return m.repo.ListAudit(ctx, limit)
}

func (m *EvolutionManager) RecordAudit(ctx context.Context, entry AuditEntry) error {
	if m == nil || m.repo == nil {
		return nil
	}
	return m.repo.RecordAudit(ctx, entry)
}

func (m *EvolutionManager) RegistryOverview(ctx context.Context) RegistryOverview {
	if m == nil || m.registry == nil {
		return RegistryOverview{PlatformSkills: []RegistrySkillSummary{}, SysEvolutionSkills: []RegistrySkillSummary{}}
	}
	return m.registry.Overview(ctx)
}

func (m *EvolutionManager) Promote(ctx context.Context, candidateID, adminActor string) (CandidateRecord, error) {
	rec, err := m.Get(ctx, candidateID)
	if err != nil {
		return CandidateRecord{}, err
	}
	if rec.Status != "pending_approval" || !rec.GatePassed() {
		return CandidateRecord{}, ErrCandidateNotPromotable
	}
	version := rec.PromotedVersion
	if rec.TargetKind == "skill" {
		if !m.registry.PublishConfigured() {
			return CandidateRecord{}, ErrRegistryUnavailable
		}
		version, err = m.registry.PublishPlatformSkill(
			ctx,
			rec.TargetName,
			rec.CandidateDocument,
			fmt.Sprintf("admin console promote %s by %s", rec.CandidateID, adminActor),
		)
		if err != nil {
			return CandidateRecord{}, err
		}
	}
	rec.Status = "promoted"
	rec.PromotedVersion = version
	rec.ApprovedBy = adminActor
	saved, err := m.repo.Upsert(ctx, rec)
	if err == nil {
		_ = m.repo.RecordAudit(ctx, AuditEntry{
			CandidateID:   saved.CandidateID,
			Action:        "promote",
			Actor:         adminActor,
			TargetName:    saved.TargetName,
			BeforeVersion: saved.BaselineVersion,
			AfterVersion:  saved.PromotedVersion,
			Reason:        "admin console promote",
		})
	}
	return saved, err
}

func (m *EvolutionManager) Rollback(ctx context.Context, candidateID, adminActor, reason string) (CandidateRecord, error) {
	rec, err := m.Get(ctx, candidateID)
	if err != nil {
		return CandidateRecord{}, err
	}
	if rec.Status != "promoted" {
		return CandidateRecord{}, ErrCandidateNotPromoted
	}
	if len(rec.BaselineDocument) == 0 {
		return CandidateRecord{}, ErrMissingBaseline
	}
	version := rec.RollbackVersion
	if rec.TargetKind == "skill" {
		if !m.registry.PublishConfigured() {
			return CandidateRecord{}, ErrRegistryUnavailable
		}
		note := fmt.Sprintf("admin console rollback %s by %s", rec.CandidateID, adminActor)
		if strings.TrimSpace(reason) != "" {
			note += ": " + strings.TrimSpace(reason)
		}
		version, err = m.registry.PublishPlatformSkill(ctx, rec.TargetName, rec.BaselineDocument, note)
		if err != nil {
			return CandidateRecord{}, err
		}
	}
	prevVer := rec.PromotedVersion
	rec.Status = "rolled_back"
	rec.RollbackVersion = version
	saved, err := m.repo.Upsert(ctx, rec)
	if err == nil {
		_ = m.repo.RecordAudit(ctx, AuditEntry{
			CandidateID:   saved.CandidateID,
			Action:        "rollback",
			Actor:         adminActor,
			TargetName:    saved.TargetName,
			BeforeVersion: prevVer,
			AfterVersion:  saved.RollbackVersion,
			Reason:        strings.TrimSpace(reason),
		})
	}
	return saved, err
}
