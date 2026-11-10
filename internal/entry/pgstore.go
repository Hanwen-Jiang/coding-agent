package entry

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres 是多副本用的 Store：触发与投递都按行领取，带租约。与 GitHub 的收件箱（internal/github）同一个库。
type Postgres struct{ db *pgxpool.Pool }

// NewPostgres 连上库并建表。
func NewPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		// 不带原始错误：连接串里可能有口令。
		return nil, errors.New("接入点的库不是合格的 Postgres 连接串")
	}
	p := &Postgres{db: pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

// Close 关掉连接池。
func (p *Postgres) Close() { p.db.Close() }

func (p *Postgres) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, p.db, func(tx pgx.Tx) error {
		// 几个副本同时启动时只有一个在建表。
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('coding_web_entry_migrate'))`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS entry_triggers (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL,
  connection   TEXT NOT NULL DEFAULT '',
  scope        TEXT NOT NULL DEFAULT '',
  tenant       TEXT NOT NULL DEFAULT '',
  user_id      TEXT NOT NULL DEFAULT '',
  project_id   TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL DEFAULT '',
  workspace_id TEXT NOT NULL DEFAULT '',
  task_id      TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL,
  attempts     INT NOT NULL DEFAULT 0,
  origin       BYTEA,
  input        BYTEA,
  created_at   TIMESTAMPTZ NOT NULL,
  asked_at     TIMESTAMPTZ,
  lease_until  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS entry_triggers_open ON entry_triggers (created_at) WHERE state NOT IN ('done', 'refused', 'failed', 'expired');
CREATE INDEX IF NOT EXISTS entry_triggers_scope ON entry_triggers (scope, created_at);
CREATE TABLE IF NOT EXISTS entry_deliveries (
  key         TEXT PRIMARY KEY,
  trigger_id  TEXT NOT NULL,
  sink        TEXT NOT NULL,
  phase       TEXT NOT NULL,
  body        TEXT NOT NULL,
  state       TEXT NOT NULL,
  attempts    INT NOT NULL DEFAULT 0,
  next_at     TIMESTAMPTZ NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL,
  sent_at     TIMESTAMPTZ,
  lease_until TIMESTAMPTZ,
  last_error  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS entry_deliveries_due ON entry_deliveries (state, next_at);
CREATE INDEX IF NOT EXISTS entry_deliveries_trigger ON entry_deliveries (trigger_id);`)
		return err
	})
}

const triggerColumns = `id, kind, connection, scope, tenant, user_id, project_id, session_id, workspace_id, task_id, state, attempts, origin, input, created_at, asked_at`

func scanTrigger(row pgx.Row) (Trigger, error) {
	var t Trigger
	var origin, input []byte
	var asked *time.Time
	var state string
	err := row.Scan(&t.ID, &t.Kind, &t.Connection, &t.Scope, &t.Tenant, &t.User, &t.ProjectID, &t.SessionID, &t.WorkspaceID, &t.TaskID,
		&state, &t.Attempts, &origin, &input, &t.CreatedAt, &asked)
	t.State, t.Origin, t.Input = State(state), origin, input
	if asked != nil {
		t.AskedAt = asked.UTC()
	}
	t.CreatedAt = t.CreatedAt.UTC()
	return t, err
}

func triggerArgs(t Trigger) []any {
	var asked *time.Time
	if !t.AskedAt.IsZero() {
		asked = &t.AskedAt
	}
	return []any{t.ID, t.Kind, t.Connection, t.Scope, t.Tenant, t.User, t.ProjectID, t.SessionID, t.WorkspaceID, t.TaskID,
		string(t.State), t.Attempts, []byte(t.Origin), []byte(t.Input), t.CreatedAt, asked}
}

func collectTriggers(rows pgx.Rows, err error) ([]Trigger, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Trigger
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *Postgres) AddTrigger(ctx context.Context, t Trigger) (Trigger, bool, error) {
	tag, err := p.db.Exec(ctx, `INSERT INTO entry_triggers (`+triggerColumns+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) ON CONFLICT (id) DO NOTHING`, triggerArgs(t)...)
	if err != nil {
		return Trigger{}, false, err
	}
	if tag.RowsAffected() == 1 {
		return t, true, nil
	}
	existing, _, err := p.Trigger(ctx, t.ID)
	return existing, false, err
}

func (p *Postgres) Trigger(ctx context.Context, id string) (Trigger, bool, error) {
	t, err := scanTrigger(p.db.QueryRow(ctx, `SELECT `+triggerColumns+` FROM entry_triggers WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Trigger{}, false, nil
	}
	return t, err == nil, err
}

func (p *Postgres) UpdateTrigger(ctx context.Context, id string, fn func(*Trigger) bool) (Trigger, bool, error) {
	var out Trigger
	changed := false
	err := pgx.BeginFunc(ctx, p.db, func(tx pgx.Tx) error {
		t, err := scanTrigger(tx.QueryRow(ctx, `SELECT `+triggerColumns+` FROM entry_triggers WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out = t
		if !fn(&t) {
			return nil
		}
		out, changed = t, true
		_, err = tx.Exec(ctx, `UPDATE entry_triggers SET kind=$2, connection=$3, scope=$4, tenant=$5, user_id=$6, project_id=$7, session_id=$8,
workspace_id=$9, task_id=$10, state=$11, attempts=$12, origin=$13, input=$14, created_at=$15, asked_at=$16 WHERE id=$1`, triggerArgs(t)...)
		return err
	})
	if err != nil {
		return Trigger{}, false, err
	}
	return out, changed, nil
}

func (p *Postgres) OpenTriggers(ctx context.Context) ([]Trigger, error) {
	return collectTriggers(p.db.Query(ctx, `SELECT `+triggerColumns+` FROM entry_triggers
WHERE state NOT IN ('done', 'refused', 'failed', 'expired') ORDER BY created_at, id`))
}

func (p *Postgres) InScope(ctx context.Context, scope string, since time.Time) ([]Trigger, error) {
	return collectTriggers(p.db.Query(ctx, `SELECT `+triggerColumns+` FROM entry_triggers WHERE scope = $1 AND created_at > $2 ORDER BY created_at, id`, scope, since))
}

func (p *Postgres) Claim(ctx context.Context, id string, now, until time.Time) (bool, error) {
	tag, err := p.db.Exec(ctx, `UPDATE entry_triggers SET lease_until = $3 WHERE id = $1 AND (lease_until IS NULL OR lease_until <= $2)`, id, now, until)
	return err == nil && tag.RowsAffected() == 1, err
}

func (p *Postgres) Release(ctx context.Context, id string) error {
	_, err := p.db.Exec(ctx, `UPDATE entry_triggers SET lease_until = NULL WHERE id = $1`, id)
	return err
}

func (p *Postgres) AddDelivery(ctx context.Context, d Delivery) (bool, error) {
	var sent *time.Time
	if !d.SentAt.IsZero() {
		sent = &d.SentAt
	}
	tag, err := p.db.Exec(ctx, `INSERT INTO entry_deliveries (key, trigger_id, sink, phase, body, state, attempts, next_at, created_at, sent_at, last_error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (key) DO NOTHING`,
		d.Key, d.Trigger, d.Sink, string(d.Phase), d.Body, d.State, d.Attempts, d.NextAt, d.CreatedAt, sent, d.LastError)
	return err == nil && tag.RowsAffected() == 1, err
}

const deliveryColumns = `key, trigger_id, sink, phase, body, state, attempts, next_at, created_at, sent_at, last_error`

func scanDelivery(row pgx.Row, extra ...any) (Delivery, error) {
	var d Delivery
	var sent *time.Time
	var phase string
	err := row.Scan(append([]any{&d.Key, &d.Trigger, &d.Sink, &phase, &d.Body, &d.State, &d.Attempts, &d.NextAt, &d.CreatedAt, &sent, &d.LastError}, extra...)...)
	d.Phase = Phase(phase)
	if sent != nil {
		d.SentAt = sent.UTC()
	}
	d.NextAt, d.CreatedAt = d.NextAt.UTC(), d.CreatedAt.UTC()
	return d, err
}

func (p *Postgres) ClaimDeliveries(ctx context.Context, trigger string, now, until time.Time, limit int) ([]Delivery, error) {
	// was 是领取之前的状态：sending 而租约过期的是上次发到一半没有结果的。
	rows, err := p.db.Query(ctx, `
WITH due AS (
  SELECT key, state AS was FROM entry_deliveries
  WHERE ($1 = '' OR trigger_id = $1)
    AND ((state = 'pending' AND next_at <= $2) OR (state = 'sending' AND lease_until <= $2))
  ORDER BY next_at, key LIMIT $4 FOR UPDATE SKIP LOCKED
)
UPDATE entry_deliveries d SET state = 'sending', lease_until = $3 FROM due WHERE d.key = due.key
RETURNING d.key, d.trigger_id, d.sink, d.phase, d.body, d.state, d.attempts, d.next_at, d.created_at, d.sent_at, d.last_error, due.was`, trigger, now, until, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var was string
		d, err := scanDelivery(rows, &was)
		if err != nil {
			return nil, err
		}
		d.Interrupted = was == DeliverySending
		out = append(out, d)
	}
	return out, rows.Err()
}

func (p *Postgres) SaveDelivery(ctx context.Context, d Delivery) error {
	var sent *time.Time
	if !d.SentAt.IsZero() {
		sent = &d.SentAt
	}
	tag, err := p.db.Exec(ctx, `UPDATE entry_deliveries SET state=$2, attempts=$3, next_at=$4, sent_at=$5, last_error=$6, lease_until=NULL WHERE key=$1`,
		d.Key, d.State, d.Attempts, d.NextAt, sent, d.LastError)
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("没有这条投递")
	}
	return err
}

func (p *Postgres) Deliveries(ctx context.Context, trigger string) ([]Delivery, error) {
	rows, err := p.db.Query(ctx, `SELECT `+deliveryColumns+` FROM entry_deliveries WHERE trigger_id = $1 ORDER BY created_at, key`, trigger)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
