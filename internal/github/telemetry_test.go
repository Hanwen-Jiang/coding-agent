package github

import (
	"context"
	"reflect"
	"testing"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/telemetry"
)

type recordedMetric struct {
	kind, name string
	value      float64
	attrs      []telemetry.Attribute
}

// fakeTelemetry 只记 Count 与 Observe；别的方法这里用不到。
type fakeTelemetry struct {
	telemetry.Telemetry
	got []recordedMetric
}

func (f *fakeTelemetry) Count(_ context.Context, name string, v int64, attrs ...telemetry.Attribute) error {
	f.got = append(f.got, recordedMetric{"count", name, float64(v), attrs})
	return nil
}

func (f *fakeTelemetry) Observe(_ context.Context, name string, v float64, attrs ...telemetry.Attribute) error {
	f.got = append(f.got, recordedMetric{"observe", name, v, attrs})
	return nil
}

// G-05 第 1 条：计数经 libs/go-core 的 Telemetry 发出；标签按键排好序（每次的属性顺序一样），没有标签时不带属性。
func TestTelemetryMetricsForwardsCountsAndObservations(t *testing.T) {
	f := &fakeTelemetry{}
	m := TelemetryMetrics(f)
	m.Count("coding.github.deliveries", 2, map[string]string{"result": "accepted", "event": "push"})
	m.Observe("coding.github.backlog", 7, nil)
	want := []recordedMetric{
		{"count", "coding.github.deliveries", 2, []telemetry.Attribute{{Key: "event", Value: "push"}, {Key: "result", Value: "accepted"}}},
		{"observe", "coding.github.backlog", 7, nil},
	}
	if !reflect.DeepEqual(f.got, want) {
		t.Fatalf("got %+v\nwant %+v", f.got, want)
	}
}
