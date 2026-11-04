package github

import (
	"context"
	"slices"

	"github.com/agentruntime/agent-runtime/libs/go-core/providers/telemetry"
)

// TelemetryMetrics 把 Metrics 接到 libs/go-core 的 Telemetry（G-05 第 1 条）。t 是平台作用域的客户端；导出失败不影响处理。
func TelemetryMetrics(t telemetry.Telemetry) Metrics { return telemetryMetrics{t} }

type telemetryMetrics struct{ t telemetry.Telemetry }

func (m telemetryMetrics) Count(name string, n int64, labels map[string]string) {
	_ = m.t.Count(context.Background(), name, n, attributes(labels)...)
}

func (m telemetryMetrics) Observe(name string, v float64, labels map[string]string) {
	_ = m.t.Observe(context.Background(), name, v, attributes(labels)...)
}

func attributes(labels map[string]string) []telemetry.Attribute {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]telemetry.Attribute, 0, len(keys))
	for _, k := range keys {
		out = append(out, telemetry.Attribute{Key: k, Value: labels[k]})
	}
	return out
}
