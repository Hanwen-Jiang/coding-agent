package entry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is a deterministic substitute for local development and checks;
// deployments must use Postgres so separate replicas share leases and budgets.
type MemoryStore struct {
	mu          sync.Mutex
	now         func() time.Time
	connections map[string]Connection
	triggers    map[string]Trigger
	deliveries  map[string]Delivery
}

func NewMemory() *MemoryStore {
	return &MemoryStore{connections: map[string]Connection{}, triggers: map[string]Trigger{}, deliveries: map[string]Delivery{}}
}
func (s *MemoryStore) Close()                              {}
func NewMemoryWithClock(now func() time.Time) *MemoryStore { s := NewMemory(); s.now = now; return s }
func (s *MemoryStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
func copyConnection(c Connection) Connection {
	c.SecretHash = append([]byte(nil), c.SecretHash...)
	b, _ := json.Marshal(c.Config)
	c.Config = nil
	_ = json.Unmarshal(b, &c.Config)
	c.Sinks = append([]Destination(nil), c.Sinks...)
	for i := range c.Sinks {
		c.Sinks[i].Phases = append([]Phase(nil), c.Sinks[i].Phases...)
	}
	return c
}
func copyTrigger(t Trigger) Trigger {
	t.Payload = append(json.RawMessage(nil), t.Payload...)
	if t.ConnectionSnapshot != nil {
		c := copyConnection(*t.ConnectionSnapshot)
		t.ConnectionSnapshot = &c
	}
	return t
}
func copyDelivery(d Delivery) Delivery {
	if d.Message.Headers != nil {
		m := map[string]string{}
		for k, v := range d.Message.Headers {
			m[k] = v
		}
		d.Message.Headers = m
	}
	return d
}
func (s *MemoryStore) Connections(ctx context.Context, tenant, user string) ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Connection{}
	for _, c := range s.connections {
		if c.Tenant == tenant && c.User == user && !deletedConnection(c) {
			out = append(out, copyConnection(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, ctx.Err()
}
func (s *MemoryStore) AllConnections(ctx context.Context) ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Connection{}
	for _, c := range s.connections {
		out = append(out, copyConnection(c))
	}
	return out, ctx.Err()
}
func (s *MemoryStore) LookupConnection(ctx context.Context, id string) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.connections[id]
	if !ok {
		return c, ErrNotFound
	}
	return copyConnection(c), ctx.Err()
}
func (s *MemoryStore) Connection(ctx context.Context, tenant, user, id string) (Connection, error) {
	c, e := s.LookupConnection(ctx, id)
	if e == nil && (c.Tenant != tenant || c.User != user || deletedConnection(c)) {
		return Connection{}, ErrNotFound
	}
	return c, e
}
func (s *MemoryStore) PutConnection(ctx context.Context, c Connection) error {
	if e := ValidateConnection(c); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.connections[c.ID]; ok {
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
	if c.Kind == "github_comment" {
		for _, other := range s.connections {
			if other.ID != c.ID && other.Tenant == c.Tenant && other.User == c.User && other.ProjectID == c.ProjectID && other.Kind == c.Kind && !deletedConnection(other) && strings.EqualFold(fmt.Sprint(other.Config["mention"]), fmt.Sprint(c.Config["mention"])) {
				return ErrConflict
			}
		}
	}
	s.connections[c.ID] = copyConnection(c)
	return ctx.Err()
}
func (s *MemoryStore) DeleteConnection(ctx context.Context, tenant, user, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.connections[id]
	if !ok || c.Tenant != tenant || c.User != user {
		return ErrNotFound
	}
	c.Enabled = false
	if c.Config == nil {
		c.Config = map[string]any{}
	}
	c.Config["deleted"] = true
	s.connections[id] = c
	return ctx.Err()
}
func normalizeTrigger(t *Trigger, c Connection) {
	if t.State == "refused" {
		t.NotifyPending = true
	}
	snapshot := copyConnection(c)
	snapshot.SecretHash = nil
	t.ConnectionSnapshot = &snapshot
	t.ConnectionID = c.ID
	t.Tenant = c.Tenant
	t.User = c.User
	t.ProjectID = c.ProjectID
	t.Kind = c.Kind
	if t.Scope == "" {
		t.Scope = c.Tenant + ":" + c.ProjectID
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	t.UpdatedAt = t.CreatedAt
	if t.NextAt.IsZero() {
		t.NextAt = t.CreatedAt
	}
	if t.State == "" {
		t.State = "accepted"
	}
	if t.ReservedUSD <= 0 {
		t.ReservedUSD = c.Limits.TaskUSD
	}
}
func charge(t Trigger) float64 {
	if Terminal(t.State) {
		return t.CostUSD
	}
	if t.CostUSD > t.ReservedUSD {
		return t.CostUSD
	}
	return t.ReservedUSD
}
func admittedState(t Trigger) bool { return t.State != "refused" && t.State != "skipped" }
func admission(t Trigger, c Connection, all []Trigger) string {
	if t.State == "refused" {
		if t.Reason != "" {
			return t.Reason
		}
		return "admission_refused"
	}
	if !c.Enabled {
		return "connection_disabled"
	}
	if t.ReservedUSD > c.Limits.TaskUSD {
		return "task_budget_limit"
	}
	scopeRuns, connRuns := 0, 0
	scopeUSD, connUSD := 0.0, 0.0
	dedupMinutes, _ := number(c.Config["dedup_minutes"])
	if dedupMinutes <= 0 {
		dedupMinutes = 60
	}
	if dedupMinutes > 1440 {
		dedupMinutes = 1440
	}
	for _, x := range all {
		if c.Kind == "alert" && t.DedupKey != "" && x.ConnectionID == c.ID && x.DedupKey == t.DedupKey && admittedState(x) && !x.CreatedAt.Before(t.CreatedAt.Add(-time.Duration(dedupMinutes)*time.Minute)) {
			return "alert_deduplicated"
		}
		if x.ConnectionID == c.ID && c.Kind == "schedule" && !Terminal(x.State) {
			return "previous_run_active"
		}
		if !admittedState(x) {
			continue
		}
		recent := !x.CreatedAt.Before(t.CreatedAt.Add(-time.Hour))
		if !recent && Terminal(x.State) {
			continue
		}
		if x.Scope == t.Scope {
			if recent {
				scopeRuns++
			}
			scopeUSD += charge(x)
		}
		if x.ConnectionID == c.ID {
			if recent {
				connRuns++
			}
			connUSD += charge(x)
		}
	}
	if scopeRuns >= MaxRunsPerHour || connRuns >= c.Limits.RunsPerHour {
		return "hourly_run_limit"
	}
	if scopeUSD+t.ReservedUSD > MaxUSDPerHour+1e-9 || connUSD+t.ReservedUSD > c.Limits.USDPerHour+1e-9 {
		return "hourly_budget_limit"
	}
	return ""
}
func refuse(t *Trigger, reason string) {
	t.NotifyPending = true
	t.State = "refused"
	if reason == "previous_run_active" || reason == "alert_deduplicated" {
		t.State = "skipped"
	}
	t.Reason = reason
	t.Payload = nil
	t.Instruction = ""
	t.Materials = ""
	t.ReservedUSD = 0
}
func (s *MemoryStore) Enqueue(ctx context.Context, t Trigger) (EnqueueResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return EnqueueResult{}, err
	}
	if t.ID == "" {
		return EnqueueResult{}, ErrInvalid
	}
	if old, ok := s.triggers[t.ID]; ok {
		if old.ConnectionID != t.ConnectionID {
			return EnqueueResult{}, ErrConflict
		}
		return EnqueueResult{copyTrigger(old), false, admittedState(old)}, nil
	}
	c, ok := s.connections[t.ConnectionID]
	if !ok {
		return EnqueueResult{}, ErrNotFound
	}
	normalizeTrigger(&t, c)
	all := make([]Trigger, 0, len(s.triggers))
	for _, x := range s.triggers {
		all = append(all, x)
	}
	reason := admission(t, c, all)
	if reason != "" {
		refuse(&t, reason)
	}
	s.triggers[t.ID] = copyTrigger(t)
	return EnqueueResult{copyTrigger(t), true, reason == ""}, nil
}
func (s *MemoryStore) ImportTrigger(ctx context.Context, t Trigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == "" {
		return ErrInvalid
	}
	if _, ok := s.triggers[t.ID]; !ok {
		s.triggers[t.ID] = copyTrigger(t)
	}
	return ctx.Err()
}
func (s *MemoryStore) Trigger(ctx context.Context, id string) (Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[id]
	if !ok {
		return t, ErrNotFound
	}
	return copyTrigger(t), ctx.Err()
}
func limit(n int) int {
	if n <= 0 || n > 500 {
		return 100
	}
	return n
}
func (s *MemoryStore) Triggers(ctx context.Context, tenant, user, connection string, n int) ([]Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Trigger{}
	for _, t := range s.triggers {
		if t.Tenant == tenant && t.User == user && (connection == "" || connection == t.ConnectionID) {
			out = append(out, copyTrigger(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit(n) {
		out = out[:limit(n)]
	}
	return out, ctx.Err()
}
func (s *MemoryStore) ClaimTrigger(ctx context.Context, now time.Time, lease time.Duration) (Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *Trigger
	for _, t := range s.triggers {
		if (!Terminal(t.State) || t.NotifyPending) && !t.NextAt.After(now) && !t.LeaseUntil.After(now) && (found == nil || t.NextAt.Before(found.NextAt)) {
			v := t
			found = &v
		}
	}
	if found == nil {
		return Trigger{}, ErrNotFound
	}
	found.LeaseToken++
	found.LeaseUntil = now.Add(lease)
	found.Attempts++
	s.triggers[found.ID] = *found
	return copyTrigger(*found), ctx.Err()
}
func (s *MemoryStore) SaveTrigger(ctx context.Context, t Trigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.triggers[t.ID]
	if !ok || old.LeaseToken != t.LeaseToken || !old.LeaseUntil.After(s.clock()) {
		return ErrLeaseLost
	}
	if old.Tenant != t.Tenant || old.ConnectionID != t.ConnectionID {
		return ErrConflict
	}
	s.triggers[t.ID] = copyTrigger(t)
	return ctx.Err()
}
func (s *MemoryStore) MutateTrigger(ctx context.Context, id string, token int64, fn func(*Trigger) bool) (Trigger, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[id]
	if !ok {
		return t, false, ErrNotFound
	}
	if token > 0 && (t.LeaseToken != token || !t.LeaseUntil.After(s.clock())) {
		return Trigger{}, false, ErrLeaseLost
	}
	v := copyTrigger(t)
	changed := fn(&v)
	if changed {
		if v.ID != t.ID || v.Tenant != t.Tenant || v.User != t.User || v.ConnectionID != t.ConnectionID {
			return Trigger{}, false, ErrConflict
		}
		v.LeaseToken = t.LeaseToken
		v.LeaseUntil = t.LeaseUntil
		s.triggers[id] = copyTrigger(v)
	}
	return v, changed, ctx.Err()
}
func (s *MemoryStore) RenewTrigger(ctx context.Context, id string, token int64, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[id]
	if !ok || t.LeaseToken != token || !t.LeaseUntil.After(s.clock()) {
		return ErrLeaseLost
	}
	t.LeaseUntil = until
	s.triggers[id] = t
	return ctx.Err()
}
func (s *MemoryStore) QueueDelivery(ctx context.Context, d Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalizeDelivery(&d)
	if d.ID == "" || d.TriggerID == "" {
		return ErrInvalid
	}
	if _, ok := s.deliveries[d.ID]; !ok {
		s.deliveries[d.ID] = copyDelivery(d)
	}
	return ctx.Err()
}
func normalizeDelivery(d *Delivery) {
	if d.ID == "" {
		d.ID = DeliveryKey(d.ConnectionID, d.TriggerID, d.Phase, d.Sink)
	}
	if d.State == "" {
		d.State = "pending"
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	if d.NextAt.IsZero() {
		d.NextAt = d.CreatedAt
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = d.CreatedAt
	}
}
func (s *MemoryStore) Deliveries(ctx context.Context, tenant, user, trigger string, n int) ([]Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Delivery{}
	for _, d := range s.deliveries {
		if d.Tenant == tenant && d.User == user && (trigger == "" || trigger == d.TriggerID) {
			out = append(out, copyDelivery(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit(n) {
		out = out[:limit(n)]
	}
	return out, ctx.Err()
}
func (s *MemoryStore) ClaimDelivery(ctx context.Context, now time.Time, lease time.Duration) (Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *Delivery
	for _, d := range s.deliveries {
		if (d.State == "pending" || d.State == "sending") && !d.NextAt.After(now) && !d.LeaseUntil.After(now) && (found == nil || d.NextAt.Before(found.NextAt)) {
			v := d
			found = &v
		}
	}
	if found == nil {
		return Delivery{}, ErrNotFound
	}
	found.Recovery = found.Recovery || found.State == "sending"
	found.State = "sending"
	found.Attempts++
	found.LeaseToken++
	found.LeaseUntil = now.Add(lease)
	s.deliveries[found.ID] = *found
	return copyDelivery(*found), ctx.Err()
}
func (s *MemoryStore) SaveDelivery(ctx context.Context, d Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.deliveries[d.ID]
	if !ok || old.LeaseToken != d.LeaseToken || !old.LeaseUntil.After(s.clock()) {
		return ErrLeaseLost
	}
	s.deliveries[d.ID] = copyDelivery(d)
	return ctx.Err()
}
func (s *MemoryStore) RenewDelivery(ctx context.Context, id string, token int64, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deliveries[id]
	if !ok || d.LeaseToken != token || !d.LeaseUntil.After(s.clock()) {
		return ErrLeaseLost
	}
	d.LeaseUntil = until
	s.deliveries[id] = d
	return ctx.Err()
}
func (s *MemoryStore) ReplayDelivery(ctx context.Context, tenant, user, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deliveries[id]
	if !ok || d.Tenant != tenant || d.User != user {
		return ErrNotFound
	}
	if d.State != "dead" {
		return fmt.Errorf("%w: only dead deliveries can be replayed", ErrConflict)
	}
	d.Recovery = d.Recovery || d.Sink == "github_comment"
	d.State = "pending"
	d.Attempts = 0
	d.NextAt = now
	d.LastError = ""
	s.deliveries[id] = d
	return ctx.Err()
}
func (s *MemoryStore) Stats(ctx context.Context) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var x Stats
	for _, d := range s.deliveries {
		switch d.State {
		case "pending":
			x.Pending++
		case "sending":
			x.Running++
		case "dead":
			x.Dead++
		case "unknown":
			x.Unknown++
		}
	}
	return x, ctx.Err()
}

var _ Store = (*MemoryStore)(nil)

// ReleaseTrigger updates scheduling without replacing state changed by a concurrent confirmation.
func (s *MemoryStore) ReleaseTrigger(ctx context.Context, id string, token int64, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[id]
	if !ok || t.LeaseToken != token || !t.LeaseUntil.After(s.clock()) {
		return ErrLeaseLost
	}
	t.LeaseUntil = time.Time{}
	t.NextAt = next
	s.triggers[id] = t
	return ctx.Err()
}

func (s *MemoryStore) ActiveTriggers(ctx context.Context, tenant, user string) ([]Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Trigger{}
	for _, t := range s.triggers {
		if t.Tenant == tenant && t.User == user && !Terminal(t.State) {
			out = append(out, copyTrigger(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, ctx.Err()
}

func deletedConnection(c Connection) bool { v, _ := c.Config["deleted"].(bool); return v }

func (s *MemoryStore) SessionTriggers(ctx context.Context, tenant, user, session string) ([]Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Trigger{}
	for _, t := range s.triggers {
		if t.Tenant == tenant && t.User == user && t.SessionID == session {
			out = append(out, copyTrigger(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, ctx.Err()
}

func (s *MemoryStore) RotateSecret(ctx context.Context, tenant, user, id string, hash []byte, now time.Time) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.connections[id]
	if !ok || c.Tenant != tenant || c.User != user || deletedConnection(c) {
		return Connection{}, ErrNotFound
	}
	if len(hash) != 32 || c.Kind != "alert" && c.Kind != "email" {
		return Connection{}, ErrInvalid
	}
	c.SecretHash = append([]byte(nil), hash...)
	c.UpdatedAt = now
	s.connections[id] = copyConnection(c)
	return copyConnection(c), ctx.Err()
}
