package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/telemetry"
)

type recordedCount struct {
	name   string
	value  int64
	labels map[string]string
}

type recordedSpan struct {
	name     string
	attrs    []telemetry.Attribute
	recorded []error
	ended    bool
}

type fakeTelemetrySpan struct {
	rec *recordedSpan
}

func (f *fakeTelemetrySpan) TraceID() string                          { return "00000000000000000000000000000001" }
func (f *fakeTelemetrySpan) SpanID() string                           { return "0000000000000001" }
func (f *fakeTelemetrySpan) SetAttributes(attrs ...telemetry.Attribute) { f.rec.attrs = append(f.rec.attrs, attrs...) }
func (f *fakeTelemetrySpan) AddEvent(string, ...telemetry.Attribute)  {}
func (f *fakeTelemetrySpan) RecordError(err error)                    { f.rec.recorded = append(f.rec.recorded, err) }
func (f *fakeTelemetrySpan) End()                                     { f.rec.ended = true }

type fakeTelemetryRecorder struct {
	mu     sync.Mutex
	spans  []*recordedSpan
	counts []recordedCount
}

func (r *fakeTelemetryRecorder) StartSpan(ctx context.Context, name string, attrs ...telemetry.Attribute) (context.Context, telemetry.Span, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := &recordedSpan{name: name, attrs: append([]telemetry.Attribute(nil), attrs...)}
	r.spans = append(r.spans, rec)
	return ctx, &fakeTelemetrySpan{rec: rec}, nil
}

func (r *fakeTelemetryRecorder) Count(_ context.Context, name string, value int64, _ ...telemetry.Attribute) error {
	return nil
}
func (r *fakeTelemetryRecorder) Observe(context.Context, string, float64, ...telemetry.Attribute) error {
	return nil
}
func (r *fakeTelemetryRecorder) Log(context.Context, telemetry.Level, string, ...telemetry.Attribute) error {
	return nil
}
func (r *fakeTelemetryRecorder) Flush(context.Context) error { return nil }

func (r *fakeTelemetryRecorder) CountMetric(name string, v int64, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts = append(r.counts, recordedCount{name: name, value: v, labels: labels})
}

type countMetricRecorder struct {
	mu     sync.Mutex
	counts []recordedCount
}

func (r *countMetricRecorder) Count(name string, v int64, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts = append(r.counts, recordedCount{name: name, value: v, labels: labels})
}
func (r *countMetricRecorder) Observe(string, float64, map[string]string) {}

func seedFeedbackTask(t *testing.T, s *Server) string {
	t.Helper()
	if err := s.store.UpdateProjects("acme", func(ps []Project) ([]Project, error) {
		return append(ps, Project{ID: "p1", Name: "P1", Source: "empty"}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.UpdateSessions("acme", func(ss []Session) ([]Session, error) {
		return append(ss, Session{ID: "s1", ProjectID: "p1", Title: "S1", WorkspaceID: "w1", RuntimeSessionID: "rs1"}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.UpdateTasks("acme", "w1", func(ts []TaskRecord) []TaskRecord {
		return append(ts, TaskRecord{ID: "t1", Message: "fix bug", Status: "TASK_STATUS_DONE"})
	}); err != nil {
		t.Fatal(err)
	}
	return "/api/projects/p1/sessions/s1/tasks/t1/feedback"
}

func TestTaskFeedbackRequiresAuthAndValidPayload(t *testing.T) {
	tel := &fakeTelemetryRecorder{}
	metrics := &countMetricRecorder{}
	ts, _, _, srv := newServerWith(t, func(c *Config) {
		c.Telemetry = tel
		c.Metrics = metrics
	})
	path := seedFeedbackTask(t, srv)
	c := newClient(t, ts.URL)

	if code, _, _ := c.do("POST", path, map[string]any{"vote": "up"}); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST feedback = %d, want 401", code)
	}

	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})
	for name, body := range map[string]any{
		"invalid vote":  map[string]any{"vote": "meh"},
		"unknown field": map[string]any{"vote": "up", "extra": 1},
		"note too long": map[string]any{"vote": "down", "note": strings.Repeat("x", 501)},
	} {
		if code, _, raw := c.do("POST", path, body); code != http.StatusBadRequest {
			t.Errorf("%s: got %d (%s), want 400", name, code, raw)
		}
	}
	if len(tel.spans) != 0 {
		t.Fatalf("expected 0 spans on rejected requests, got %d", len(tel.spans))
	}
}

func TestTaskFeedbackPersistsAndEmitsOTelBadCaseSpan(t *testing.T) {
	tel := &fakeTelemetryRecorder{}
	metrics := &countMetricRecorder{}
	ts, _, _, srv := newServerWith(t, func(c *Config) {
		c.Telemetry = tel
		c.Metrics = metrics
	})
	path := seedFeedbackTask(t, srv)
	c := newClient(t, ts.URL)
	c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"})

	// 1. Thumbs up persists on TaskRecord and records span without error.
	if code, _, raw := c.do("POST", path, map[string]any{"vote": "up"}); code != http.StatusOK {
		t.Fatalf("up feedback = %d %s", code, raw)
	}
	// 2. Thumbs down persists and marks failure.reason=user_thumbs_down + RecordError.
	if code, _, raw := c.do("POST", path, map[string]any{"vote": "down", "note": "wrong file edited"}); code != http.StatusOK {
		t.Fatalf("down feedback = %d %s", code, raw)
	}
	code, _, tasksRaw := c.do("GET", "/api/projects/p1/sessions/s1/tasks", nil)
	if code != http.StatusOK {
		t.Fatalf("GET tasks = %d %s", code, tasksRaw)
	}
	var tasks []TaskRecord
	if err := json.Unmarshal([]byte(tasksRaw), &tasks); err != nil || len(tasks) != 1 {
		t.Fatalf("unmarshal tasks: %v %s", err, tasksRaw)
	}
	if tasks[0].Feedback != "down" {
		t.Fatalf("persisted TaskRecord.Feedback = %q, want down", tasks[0].Feedback)
	}

	// 3. Clearing vote clears persisted feedback without emitting a vote span.
	if code, _, raw := c.do("POST", path, map[string]any{"vote": ""}); code != http.StatusOK {
		t.Fatalf("clear feedback = %d %s", code, raw)
	}
	_, _, tasksRaw = c.do("GET", "/api/projects/p1/sessions/s1/tasks", nil)
	tasks = nil
	if err := json.Unmarshal([]byte(tasksRaw), &tasks); err != nil || len(tasks) != 1 {
		t.Fatalf("unmarshal cleared tasks: %v %s", err, tasksRaw)
	}
	if tasks[0].Feedback != "" {
		t.Fatalf("cleared TaskRecord.Feedback = %q, want empty", tasks[0].Feedback)
	}

	wantMetrics := []recordedCount{
		{"coding.answer_feedback.total", 1, map[string]string{"vote": "up"}},
		{"coding.answer_feedback.total", 1, map[string]string{"vote": "down"}},
		{"coding.answer_feedback.total", 1, map[string]string{"vote": "clear"}},
	}
	if !reflect.DeepEqual(metrics.counts, wantMetrics) {
		t.Fatalf("metrics = %#v, want %#v", metrics.counts, wantMetrics)
	}

	if len(tel.spans) != 2 {
		t.Fatalf("spans len = %d, want 2", len(tel.spans))
	}
	downSpan := tel.spans[1]
	if downSpan.name != "coding_agent.answer_feedback" || !downSpan.ended || len(downSpan.recorded) != 1 {
		t.Fatalf("down span unexpected: %#v", downSpan)
	}
	attrMap := map[string]any{}
	for _, a := range downSpan.attrs {
		attrMap[a.Key] = a.Value
	}
	if attrMap["skill.name"] != "coding_agent.chat" || attrMap["failure.reason"] != "user_thumbs_down" || attrMap["feedback.vote"] != "down" || attrMap["feedback.note"] != "wrong file edited" {
		t.Fatalf("down span attrs = %#v", attrMap)
	}
}
