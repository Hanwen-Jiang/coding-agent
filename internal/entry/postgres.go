package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ db *pgxpool.Pool }

func NewPostgres(ctx context.Context, dsn string) (*PostgresStore, error) {
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, errors.New("invalid entry Postgres configuration")
	}
	s := &PostgresStore{db: db}
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, errors.New("entry Postgres initialization failed")
	}
	return s, nil
}
func (s *PostgresStore) Close() { s.db.Close() }
func (s *PostgresStore) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('coding_web_entry_migrate'))`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS entry_connections (
 id text PRIMARY KEY, tenant text NOT NULL, owner_id text NOT NULL, data jsonb NOT NULL, secret_hash bytea,
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS entry_connections_owner ON entry_connections(tenant,owner_id);
CREATE TABLE IF NOT EXISTS entry_triggers (
 id text PRIMARY KEY, connection_id text NOT NULL, tenant text NOT NULL, owner_id text NOT NULL,
 scope text NOT NULL, state text NOT NULL, data jsonb NOT NULL,
 reserved_usd double precision NOT NULL DEFAULT 0, cost_usd double precision NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL, next_at timestamptz NOT NULL,
 lease_until timestamptz, lease_token bigint NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS entry_triggers_due ON entry_triggers(state,next_at);
CREATE INDEX IF NOT EXISTS entry_triggers_open ON entry_triggers(next_at) WHERE state NOT IN ('done','refused','failed','expired','skipped','cancelled');
CREATE INDEX IF NOT EXISTS entry_triggers_pending_outcomes ON entry_triggers(next_at) WHERE data @> '{"private_notify_pending":true}'::jsonb;
CREATE INDEX IF NOT EXISTS entry_triggers_scope ON entry_triggers(scope,created_at);
CREATE INDEX IF NOT EXISTS entry_triggers_connection ON entry_triggers(connection_id,created_at);
CREATE INDEX IF NOT EXISTS entry_triggers_owner ON entry_triggers(tenant,owner_id,created_at);
CREATE INDEX IF NOT EXISTS entry_triggers_session ON entry_triggers(tenant,owner_id,(data->>'session_id'));
CREATE TABLE IF NOT EXISTS entry_deliveries (
 id text PRIMARY KEY, connection_id text NOT NULL, trigger_id text NOT NULL,
 tenant text NOT NULL, owner_id text NOT NULL, state text NOT NULL, data jsonb NOT NULL,
 created_at timestamptz NOT NULL, next_at timestamptz NOT NULL,
 lease_until timestamptz, lease_token bigint NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS entry_deliveries_due ON entry_deliveries(state,next_at);
CREATE INDEX IF NOT EXISTS entry_deliveries_owner ON entry_deliveries(tenant,owner_id,created_at);
`)
		return err
	})
}

type rowScanner interface{ Scan(...any) error }

func scanConnection(row rowScanner) (Connection, error) {
	var c Connection
	var data, secret []byte
	err := row.Scan(&data, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	c.SecretHash = secret
	return c, err
}

type storedTrigger struct {
	NotifyPending      bool        `json:"private_notify_pending,omitempty"`
	ConnectionSnapshot *Connection `json:"private_connection_snapshot,omitempty"`
	Trigger
	Payload     json.RawMessage `json:"private_payload,omitempty"`
	Instruction string          `json:"private_instruction,omitempty"`
	Materials   string          `json:"private_materials,omitempty"`
}

func triggerBytes(t Trigger) ([]byte, error) {
	return json.Marshal(storedTrigger{NotifyPending: t.NotifyPending, ConnectionSnapshot: t.ConnectionSnapshot, Trigger: t, Payload: t.Payload, Instruction: t.Instruction, Materials: t.Materials})
}
func scanTrigger(row rowScanner) (Trigger, error) {
	var data []byte
	var lease *time.Time
	var token int64
	err := row.Scan(&data, &lease, &token)
	if errors.Is(err, pgx.ErrNoRows) {
		return Trigger{}, ErrNotFound
	}
	if err != nil {
		return Trigger{}, err
	}
	var v storedTrigger
	if err = json.Unmarshal(data, &v); err != nil {
		return Trigger{}, err
	}
	v.Trigger.NotifyPending = v.NotifyPending
	v.Trigger.ConnectionSnapshot = v.ConnectionSnapshot
	v.Trigger.Payload = v.Payload
	v.Trigger.Instruction = v.Instruction
	v.Trigger.Materials = v.Materials
	v.Trigger.LeaseToken = token
	if lease != nil {
		v.Trigger.LeaseUntil = *lease
	}
	return v.Trigger, nil
}

type storedDelivery struct {
	Delivery
	Message  Message `json:"private_message"`
	Recovery bool    `json:"private_recovery"`
}

func deliveryBytes(d Delivery) ([]byte, error) {
	return json.Marshal(storedDelivery{d, d.Message, d.Recovery})
}
func scanDelivery(row rowScanner) (Delivery, error) {
	var data []byte
	var lease *time.Time
	var token int64
	err := row.Scan(&data, &lease, &token)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, err
	}
	var v storedDelivery
	if err = json.Unmarshal(data, &v); err != nil {
		return Delivery{}, err
	}
	v.Delivery.Message = v.Message
	v.Delivery.Recovery = v.Recovery
	v.Delivery.LeaseToken = token
	if lease != nil {
		v.Delivery.LeaseUntil = *lease
	}
	return v.Delivery, nil
}
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func (s *PostgresStore) Connections(ctx context.Context, tenant, user string) ([]Connection, error) {
	return s.connections(ctx, `SELECT data,secret_hash FROM entry_connections WHERE tenant=$1 AND owner_id=$2 AND COALESCE(data->'config'->>'deleted','false')!='true' ORDER BY created_at`, tenant, user)
}
func (s *PostgresStore) AllConnections(ctx context.Context) ([]Connection, error) {
	return s.connections(ctx, `SELECT data,secret_hash FROM entry_connections ORDER BY created_at`)
}
func (s *PostgresStore) connections(ctx context.Context, q string, args ...any) ([]Connection, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *PostgresStore) Connection(ctx context.Context, tenant, user, id string) (Connection, error) {
	return scanConnection(s.db.QueryRow(ctx, `SELECT data,secret_hash FROM entry_connections WHERE id=$1 AND tenant=$2 AND owner_id=$3 AND COALESCE(data->'config'->>'deleted','false')!='true'`, id, tenant, user))
}
func (s *PostgresStore) LookupConnection(ctx context.Context, id string) (Connection, error) {
	return scanConnection(s.db.QueryRow(ctx, `SELECT data,secret_hash FROM entry_connections WHERE id=$1`, id))
}
func (s *PostgresStore) PutConnection(ctx context.Context, c Connection) error {
	if err := ValidateConnection(c); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// This also serializes concurrent creation of an ID before a row exists.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, `entry-connection:`+c.ID); err != nil {
			return err
		}
		if c.Kind == "github_comment" {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "entry-alias:"+c.Tenant+":"+c.User+":"+c.ProjectID); err != nil {
				return err
			}
			var conflict bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entry_connections WHERE id<>$1 AND tenant=$2 AND owner_id=$3 AND data->>'project_id'=$4 AND data->>'kind'='github_comment' AND COALESCE(data->'config'->>'deleted','false')!='true' AND lower(data->'config'->>'mention')=lower($5))`, c.ID, c.Tenant, c.User, c.ProjectID, fmt.Sprint(c.Config["mention"])).Scan(&conflict); err != nil {
				return err
			}
			if conflict {
				return ErrConflict
			}
		}
		old, err := scanConnection(tx.QueryRow(ctx, `SELECT data,secret_hash FROM entry_connections WHERE id=$1 FOR UPDATE`, c.ID))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil {
			if deletedConnection(old) || old.Tenant != c.Tenant || old.User != c.User || old.Kind != c.Kind || old.ProjectID != c.ProjectID {
				return ErrConflict
			}
			c.SecretHash = append([]byte(nil), old.SecretHash...)
			c.CreatedAt = old.CreatedAt
			c.LastScheduledAt = old.LastScheduledAt
		}
		if c.CreatedAt.IsZero() {
			c.CreatedAt = time.Now().UTC()
		}
		if c.UpdatedAt.IsZero() {
			c.UpdatedAt = c.CreatedAt
		}
		b, err := json.Marshal(c)
		if err != nil {
			return ErrInvalid
		}
		_, err = tx.Exec(ctx, `INSERT INTO entry_connections(id,tenant,owner_id,data,secret_hash,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data,secret_hash=EXCLUDED.secret_hash,updated_at=EXCLUDED.updated_at`, c.ID, c.Tenant, c.User, b, c.SecretHash, c.CreatedAt, c.UpdatedAt)
		return err
	})
}
func (s *PostgresStore) DeleteConnection(ctx context.Context, tenant, user, id string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		c, err := scanConnection(tx.QueryRow(ctx, `SELECT data,secret_hash FROM entry_connections WHERE id=$1 AND tenant=$2 AND owner_id=$3 FOR UPDATE`, id, tenant, user))
		if err != nil {
			return err
		}
		c.Enabled = false
		if c.Config == nil {
			c.Config = map[string]any{}
		}
		c.Config["deleted"] = true
		c.UpdatedAt = time.Now().UTC()
		b, _ := json.Marshal(c)
		_, err = tx.Exec(ctx, `UPDATE entry_connections SET data=$2,updated_at=$3 WHERE id=$1`, id, b, c.UpdatedAt)
		return err
	})
}
func (s *PostgresStore) Enqueue(ctx context.Context, t Trigger) (EnqueueResult, error) {
	var out EnqueueResult
	if t.ID == "" {
		return out, ErrInvalid
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// A stable trigger lock deduplicates across connections before any quota work.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, `entry-trigger:`+t.ID); err != nil {
			return err
		}
		old, err := scanTrigger(tx.QueryRow(ctx, `SELECT data,lease_until,lease_token FROM entry_triggers WHERE id=$1`, t.ID))
		if err == nil {
			if old.ConnectionID != t.ConnectionID {
				return ErrConflict
			}
			out = EnqueueResult{old, false, admittedState(old)}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		c, err := scanConnection(tx.QueryRow(ctx, `SELECT data,secret_hash FROM entry_connections WHERE id=$1 FOR UPDATE`, t.ConnectionID))
		if err != nil {
			return err
		}
		normalizeTrigger(&t, c)
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, `entry-budget:`+t.Scope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT data,lease_until,lease_token FROM entry_triggers WHERE (scope=$1 OR connection_id=$2) AND (created_at >= $3 OR state NOT IN ('done','refused','failed','expired','skipped','cancelled'))`, t.Scope, t.ConnectionID, t.CreatedAt.Add(-24*time.Hour))
		if err != nil {
			return err
		}
		all := []Trigger{}
		for rows.Next() {
			v, e := scanTrigger(rows)
			if e != nil {
				rows.Close()
				return e
			}
			all = append(all, v)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		reason := admission(t, c, all)
		if reason != "" {
			refuse(&t, reason)
		}
		if err = insertTrigger(ctx, tx, t); err != nil {
			return err
		}
		out = EnqueueResult{t, true, reason == ""}
		return nil
	})
	return out, err
}
func insertTrigger(ctx context.Context, tx pgx.Tx, t Trigger) error {
	b, err := triggerBytes(t)
	if err != nil {
		return ErrInvalid
	}
	_, err = tx.Exec(ctx, `INSERT INTO entry_triggers(id,connection_id,tenant,owner_id,scope,state,data,reserved_usd,cost_usd,created_at,next_at,lease_until,lease_token) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(id) DO NOTHING`, t.ID, t.ConnectionID, t.Tenant, t.User, t.Scope, t.State, b, t.ReservedUSD, t.CostUSD, t.CreatedAt, t.NextAt, nullableTime(t.LeaseUntil), t.LeaseToken)
	return err
}
func (s *PostgresStore) ImportTrigger(ctx context.Context, t Trigger) error {
	if t.ID == "" {
		return ErrInvalid
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	if t.NextAt.IsZero() {
		t.NextAt = t.CreatedAt
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return insertTrigger(ctx, tx, t) })
}
func (s *PostgresStore) Trigger(ctx context.Context, id string) (Trigger, error) {
	return scanTrigger(s.db.QueryRow(ctx, `SELECT data,lease_until,lease_token FROM entry_triggers WHERE id=$1`, id))
}
func (s *PostgresStore) Triggers(ctx context.Context, tenant, user, connection string, n int) ([]Trigger, error) {
	rows, err := s.db.Query(ctx, `SELECT data,lease_until,lease_token FROM entry_triggers WHERE tenant=$1 AND owner_id=$2 AND ($3='' OR connection_id=$3) ORDER BY created_at DESC LIMIT $4`, tenant, user, connection, limit(n))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Trigger{}
	for rows.Next() {
		t, e := scanTrigger(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *PostgresStore) ClaimTrigger(ctx context.Context, now time.Time, lease time.Duration) (Trigger, error) {
	var t Trigger
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		t, err = scanTrigger(tx.QueryRow(ctx, `SELECT data,lease_until,lease_token FROM entry_triggers WHERE (state NOT IN ('done','refused','failed','expired','skipped','cancelled') OR data @> '{"private_notify_pending":true}'::jsonb) AND next_at <= $1 AND (lease_until IS NULL OR lease_until <= $1) ORDER BY next_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, now))
		if err != nil {
			return err
		}
		t.LeaseToken++
		t.LeaseUntil = now.Add(lease)
		t.Attempts++
		return updateTrigger(ctx, tx, t)
	})
	return t, err
}
func updateTrigger(ctx context.Context, tx pgx.Tx, t Trigger) error {
	b, err := triggerBytes(t)
	if err != nil {
		return ErrInvalid
	}
	_, err = tx.Exec(ctx, `UPDATE entry_triggers SET state=$2,data=$3,reserved_usd=$4,cost_usd=$5,next_at=$6,lease_until=$7,lease_token=$8 WHERE id=$1`, t.ID, t.State, b, t.ReservedUSD, t.CostUSD, t.NextAt, nullableTime(t.LeaseUntil), t.LeaseToken)
	return err
}
func (s *PostgresStore) SaveTrigger(ctx context.Context, t Trigger) error {
	b, err := triggerBytes(t)
	if err != nil {
		return ErrInvalid
	}
	tag, err := s.db.Exec(ctx, `UPDATE entry_triggers SET state=$2,data=$3,reserved_usd=$4,cost_usd=$5,next_at=$6,lease_until=$7 WHERE id=$1 AND lease_token=$8 AND lease_until>clock_timestamp() AND tenant=$9 AND connection_id=$10`, t.ID, t.State, b, t.ReservedUSD, t.CostUSD, t.NextAt, nullableTime(t.LeaseUntil), t.LeaseToken, t.Tenant, t.ConnectionID)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return err
}
func (s *PostgresStore) MutateTrigger(ctx context.Context, id string, token int64, fn func(*Trigger) bool) (Trigger, bool, error) {
	var out Trigger
	changed := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		t, err := scanTrigger(tx.QueryRow(ctx, `SELECT data,lease_until,lease_token FROM entry_triggers WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if token > 0 && (t.LeaseToken != token || !t.LeaseUntil.After(time.Now())) {
			return ErrLeaseLost
		}
		out = copyTrigger(t)
		changed = fn(&out)
		if !changed {
			return nil
		}
		out.LeaseToken = t.LeaseToken
		out.LeaseUntil = t.LeaseUntil
		if out.ID != t.ID || out.Tenant != t.Tenant || out.User != t.User || out.ConnectionID != t.ConnectionID {
			return ErrConflict
		}
		return updateTrigger(ctx, tx, out)
	})
	return out, changed, err
}
func (s *PostgresStore) RenewTrigger(ctx context.Context, id string, token int64, until time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE entry_triggers SET lease_until=$3 WHERE id=$1 AND lease_token=$2 AND lease_until>clock_timestamp()`, id, token, until)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return err
}
func (s *PostgresStore) QueueDelivery(ctx context.Context, d Delivery) error {
	normalizeDelivery(&d)
	if d.TriggerID == "" || d.ConnectionID == "" {
		return ErrInvalid
	}
	b, err := deliveryBytes(d)
	if err != nil {
		return ErrInvalid
	}
	_, err = s.db.Exec(ctx, `INSERT INTO entry_deliveries(id,connection_id,trigger_id,tenant,owner_id,state,data,created_at,next_at,lease_until,lease_token) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(id) DO NOTHING`, d.ID, d.ConnectionID, d.TriggerID, d.Tenant, d.User, d.State, b, d.CreatedAt, d.NextAt, nullableTime(d.LeaseUntil), d.LeaseToken)
	return err
}
func (s *PostgresStore) Deliveries(ctx context.Context, tenant, user, trigger string, n int) ([]Delivery, error) {
	rows, err := s.db.Query(ctx, `SELECT data,lease_until,lease_token FROM entry_deliveries WHERE tenant=$1 AND owner_id=$2 AND ($3='' OR trigger_id=$3) ORDER BY created_at DESC LIMIT $4`, tenant, user, trigger, limit(n))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, e := scanDelivery(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *PostgresStore) ClaimDelivery(ctx context.Context, now time.Time, lease time.Duration) (Delivery, error) {
	var d Delivery
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		d, err = scanDelivery(tx.QueryRow(ctx, `SELECT data,lease_until,lease_token FROM entry_deliveries WHERE state IN ('pending','sending') AND next_at <= $1 AND (lease_until IS NULL OR lease_until <= $1) ORDER BY next_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, now))
		if err != nil {
			return err
		}
		d.Recovery = d.Recovery || d.State == "sending"
		d.State = "sending"
		d.Attempts++
		d.LeaseToken++
		d.LeaseUntil = now.Add(lease)
		b, err := deliveryBytes(d)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE entry_deliveries SET state=$2,data=$3,lease_until=$4,lease_token=$5 WHERE id=$1`, d.ID, d.State, b, d.LeaseUntil, d.LeaseToken)
		return err
	})
	return d, err
}
func (s *PostgresStore) SaveDelivery(ctx context.Context, d Delivery) error {
	b, err := deliveryBytes(d)
	if err != nil {
		return ErrInvalid
	}
	tag, err := s.db.Exec(ctx, `UPDATE entry_deliveries SET state=$2,data=$3,next_at=$4,lease_until=$5 WHERE id=$1 AND lease_token=$6 AND lease_until>clock_timestamp()`, d.ID, d.State, b, d.NextAt, nullableTime(d.LeaseUntil), d.LeaseToken)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return err
}
func (s *PostgresStore) RenewDelivery(ctx context.Context, id string, token int64, until time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE entry_deliveries SET lease_until=$3 WHERE id=$1 AND lease_token=$2 AND lease_until>clock_timestamp()`, id, token, until)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return err
}
func (s *PostgresStore) ReplayDelivery(ctx context.Context, tenant, user, id string, now time.Time) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		d, err := scanDelivery(tx.QueryRow(ctx, `SELECT data,lease_until,lease_token FROM entry_deliveries WHERE id=$1 AND tenant=$2 AND owner_id=$3 FOR UPDATE`, id, tenant, user))
		if err != nil {
			return err
		}
		if d.State != "dead" {
			return fmt.Errorf("%w: only dead deliveries can be replayed", ErrConflict)
		}
		d.Recovery = d.Recovery || d.Sink == "github_comment"
		d.State = "pending"
		d.Attempts = 0
		d.NextAt = now
		d.LastError = ""
		d.UpdatedAt = now
		b, err := deliveryBytes(d)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE entry_deliveries SET state=$2,data=$3,next_at=$4 WHERE id=$1`, id, d.State, b, now)
		return err
	})
}
func (s *PostgresStore) Stats(ctx context.Context) (Stats, error) {
	var x Stats
	err := s.db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state='pending'),count(*) FILTER(WHERE state='sending'),count(*) FILTER(WHERE state='dead'),count(*) FILTER(WHERE state='unknown') FROM entry_deliveries`).Scan(&x.Pending, &x.Running, &x.Dead, &x.Unknown)
	return x, err
}

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM entry_deliveries WHERE created_at < $1 AND state IN ('done','dead','unknown')`, before)
		if err != nil {
			return err
		}
		n += tag.RowsAffected()
		tag, err = tx.Exec(ctx, `DELETE FROM entry_triggers t WHERE t.created_at < $1 AND t.state IN ('done','refused','failed','expired','skipped','cancelled') AND NOT t.data @> '{"private_notify_pending":true}'::jsonb AND NOT EXISTS (SELECT 1 FROM entry_deliveries d WHERE d.trigger_id = t.id)`, before)
		if err != nil {
			return err
		}
		n += tag.RowsAffected()
		return nil
	})
	return n, err
}
func (s *PostgresStore) ReleaseTrigger(ctx context.Context, id string, token int64, next time.Time, succeeded bool) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		t, err := scanTrigger(tx.QueryRow(ctx, `SELECT data,lease_until,lease_token FROM entry_triggers WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if t.LeaseToken != token || !t.LeaseUntil.After(time.Now()) {
			return ErrLeaseLost
		}
		t.LeaseUntil = time.Time{}
		t.NextAt = next
		if succeeded {
			t.Attempts = 0
		}
		return updateTrigger(ctx, tx, t)
	})
}

func (s *PostgresStore) ActiveTriggers(ctx context.Context, tenant, user string) ([]Trigger, error) {
	rows, err := s.db.Query(ctx, `SELECT data,lease_until,lease_token FROM entry_triggers WHERE tenant=$1 AND owner_id=$2 AND state NOT IN ('done','refused','failed','expired','skipped','cancelled') ORDER BY created_at DESC`, tenant, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Trigger{}
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *PostgresStore) SessionTriggers(ctx context.Context, tenant, user, session string) ([]Trigger, error) {
	rows, err := s.db.Query(ctx, `SELECT data,lease_until,lease_token FROM entry_triggers WHERE tenant=$1 AND owner_id=$2 AND data->>'session_id'=$3 ORDER BY created_at DESC`, tenant, user, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Trigger{}
	for rows.Next() {
		t, e := scanTrigger(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *PostgresStore) RotateSecret(ctx context.Context, tenant, user, id string, hash []byte, now time.Time) (Connection, error) {
	var result Connection
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		c, e := scanConnection(tx.QueryRow(ctx, `SELECT data,secret_hash FROM entry_connections WHERE id=$1 AND tenant=$2 AND owner_id=$3 FOR UPDATE`, id, tenant, user))
		if e != nil {
			return e
		}
		if deletedConnection(c) {
			return ErrNotFound
		}
		if len(hash) != 32 || c.Kind != "alert" && c.Kind != "email" {
			return ErrInvalid
		}
		c.SecretHash = append([]byte(nil), hash...)
		c.UpdatedAt = now
		b, e := json.Marshal(c)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE entry_connections SET data=$1,secret_hash=$2,updated_at=$3 WHERE id=$4`, b, hash, now, id)
		result = c
		return e
	})
	return result, err
}
