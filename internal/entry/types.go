// Package entry provides the shared, durable admission and delivery machinery
// for external task entry points. It never calls Runtime or sends a message.
package entry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

var (
	ErrNotFound        = errors.New("entry not found")
	ErrConflict        = errors.New("entry conflict")
	ErrLeaseLost       = errors.New("entry lease lost")
	ErrInvalid         = errors.New("invalid entry configuration")
	ErrUnknownDelivery = errors.New("delivery outcome unknown")
)

const MaxRunsPerHour = 3
const MaxUSDPerHour = 5.0
const MaxAttempts = 5

type Limits struct {
	RunsPerHour int     `json:"runs_per_hour"`
	USDPerHour  float64 `json:"usd_per_hour"`
	TaskUSD     float64 `json:"task_usd"`
}
type Destination struct {
	Sink    string  `json:"sink"`
	Address Address `json:"address"`
	Phases  []Phase `json:"phases,omitempty"`
}
type Address struct {
	Repo         string `json:"repo,omitempty"`
	RepoID       int64  `json:"repo_id,omitempty"`
	Installation int64  `json:"installation,omitempty"`
	Issue        int    `json:"issue,omitempty"`
	Email        string `json:"email,omitempty"`
	ThreadID     string `json:"thread_id,omitempty"`
	References   string `json:"references,omitempty"`
}
type Connection struct {
	ID              string         `json:"id"`
	Tenant          string         `json:"tenant"`
	User            string         `json:"user"`
	ProjectID       string         `json:"project_id"`
	Kind            string         `json:"kind"`
	Name            string         `json:"name"`
	Enabled         bool           `json:"enabled"`
	Config          map[string]any `json:"config"`
	Sinks           []Destination  `json:"sinks"`
	Limits          Limits         `json:"limits"`
	SecretHash      []byte         `json:"-"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	LastScheduledAt time.Time      `json:"last_scheduled_at,omitzero"`
}
type Trigger struct {
	NotifyPending      bool            `json:"-"`
	SourceAddress      Address         `json:"source_address,omitempty"`
	SourceURL          string          `json:"source_url,omitempty"`
	ConnectionSnapshot *Connection     `json:"-"`
	DedupKey           string          `json:"dedup_key,omitempty"`
	ID                 string          `json:"id"`
	ConnectionID       string          `json:"connection_id"`
	Tenant             string          `json:"tenant"`
	User               string          `json:"user"`
	ProjectID          string          `json:"project_id"`
	Kind               string          `json:"kind"`
	Scope              string          `json:"scope"`
	State              string          `json:"state"`
	Reason             string          `json:"reason,omitempty"`
	SessionID          string          `json:"session_id,omitempty"`
	WorkspaceID        string          `json:"workspace_id,omitempty"`
	TaskID             string          `json:"task_id,omitempty"`
	Payload            json.RawMessage `json:"-"`
	Instruction        string          `json:"-"`
	Materials          string          `json:"-"`
	ReservedUSD        float64         `json:"reserved_usd"`
	CostUSD            float64         `json:"cost_usd"`
	Attempts           int             `json:"attempts"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	NextAt             time.Time       `json:"next_at"`
	LeaseUntil         time.Time       `json:"-"`
	LeaseToken         int64           `json:"-"`
}
type Phase string

const (
	Accepted Phase = "accepted"
	Question Phase = "question"
	Result   Phase = "result"
	Failed   Phase = "failed"
)

type Message struct {
	Subject string            `json:"subject,omitempty"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers,omitempty"`
}
type Delivery struct {
	ID           string    `json:"id"`
	ConnectionID string    `json:"connection_id"`
	TriggerID    string    `json:"trigger_id"`
	Tenant       string    `json:"tenant"`
	User         string    `json:"user"`
	Sink         string    `json:"sink"`
	Address      Address   `json:"address"`
	Phase        Phase     `json:"phase"`
	Message      Message   `json:"-"`
	State        string    `json:"state"`
	Attempts     int       `json:"attempts"`
	Receipt      string    `json:"receipt,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	NextAt       time.Time `json:"next_at"`
	LeaseUntil   time.Time `json:"-"`
	LeaseToken   int64     `json:"-"`
	// Recovery is true when a previous attempt may have sent a message. The sink
	// must reconcile by ID before sending again, or return ErrUnknownDelivery.
	Recovery bool `json:"-"`
}
type Field struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	LabelEN  string   `json:"label_en"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Default  any      `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
}
type Schema struct {
	Name    string  `json:"name"`
	Label   string  `json:"label"`
	LabelEN string  `json:"label_en"`
	Fields  []Field `json:"fields"`
}
type Inbound struct {
	Trusted    bool
	Body       []byte
	Header     http.Header
	Now        time.Time
	Connection Connection
}
type Admission struct {
	Allowed bool
	Reason  string
}
type TaskInput struct {
	Instruction   string
	Materials     string
	EffectCeiling string
	BudgetUSD     float64
}
type Outcome struct {
	Trigger Trigger
	URL     string
	Summary string
}
type Kind interface {
	Name() string
	Schema() Schema
	Ingest(context.Context, Inbound) ([]Trigger, error)
	Admit(context.Context, Connection, Trigger) Admission
	Compose(Connection, Trigger) TaskInput
	DefaultSink() (string, Address)
}
type Sink interface {
	Name() string
	Phases() []Phase
	Render(Outcome, Phase, string) Message
	Deliver(context.Context, Connection, Address, Message, string) error
}

type EnqueueResult struct {
	Trigger  Trigger
	Added    bool
	Admitted bool
}
type Stats struct{ Pending, Running, Dead, Unknown int64 }

// Store is internal infrastructure: handlers must use the scoped methods.
// Claim/Save and lookup by global ID are for the background worker only.
type Store interface {
	Connections(context.Context, string, string) ([]Connection, error)
	Connection(context.Context, string, string, string) (Connection, error)
	LookupConnection(context.Context, string) (Connection, error)
	PutConnection(context.Context, Connection) error
	RotateSecret(context.Context, string, string, string, []byte, time.Time) (Connection, error)
	DeleteConnection(context.Context, string, string, string) error
	AllConnections(context.Context) ([]Connection, error)
	Enqueue(context.Context, Trigger) (EnqueueResult, error)
	ImportTrigger(context.Context, Trigger) error
	Trigger(context.Context, string) (Trigger, error)
	Triggers(context.Context, string, string, string, int) ([]Trigger, error)
	ActiveTriggers(context.Context, string, string) ([]Trigger, error)
	SessionTriggers(context.Context, string, string, string) ([]Trigger, error)
	ClaimTrigger(context.Context, time.Time, time.Duration) (Trigger, error)
	SaveTrigger(context.Context, Trigger) error
	MutateTrigger(context.Context, string, int64, func(*Trigger) bool) (Trigger, bool, error)
	RenewTrigger(context.Context, string, int64, time.Time) error
	ReleaseTrigger(context.Context, string, int64, time.Time) error
	QueueDelivery(context.Context, Delivery) error
	Deliveries(context.Context, string, string, string, int) ([]Delivery, error)
	ClaimDelivery(context.Context, time.Time, time.Duration) (Delivery, error)
	SaveDelivery(context.Context, Delivery) error
	RenewDelivery(context.Context, string, int64, time.Time) error
	ReplayDelivery(context.Context, string, string, string, time.Time) error
	Stats(context.Context) (Stats, error)
	Close()
}

func Terminal(state string) bool {
	switch state {
	case "done", "refused", "failed", "expired", "skipped", "cancelled":
		return true
	}
	return false
}
