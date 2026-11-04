package server

// 网页报上来的计量（PERF-00 的 coding-agent 一侧，Runtime 的 docs/plan/workspace-read-latency-and-disk-2026-11-03.md 第 2 节）：
// 打开会话时加载历史的耗时与请求数，记成 coding.history_load.duration_ms 与 coding.history_load.requests（标签只有 mode），
// 线上取一周的 p50、p95 作基线。只是计量：登录了才收；取值有上限，不合规的 400、什么都不记；没接 Telemetry 时照样 204。

import (
	"encoding/json"
	"math"
	"net/http"
)

const (
	maxHistoryLoadMS       = 10 * 60 * 1000
	maxHistoryLoadRequests = 10000
)

func (s *Server) handleHistoryLoad(w http.ResponseWriter, r *http.Request, _ identity) {
	var report struct {
		Mode       string   `json:"mode"`
		DurationMS *float64 `json:"duration_ms"`
		Requests   *float64 `json:"requests"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		writeMsg(w, r, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	d, n := report.DurationMS, report.Requests
	if (report.Mode != "snapshot" && report.Mode != "replay") || d == nil || n == nil ||
		*d < 0 || *d > maxHistoryLoadMS || *n < 0 || *n > maxHistoryLoadRequests || *n != math.Trunc(*n) {
		writeMsg(w, r, http.StatusBadRequest, "计量的取值不对")
		return
	}
	labels := map[string]string{"mode": report.Mode}
	s.metrics().Observe("coding.history_load.duration_ms", *d, labels)
	s.metrics().Observe("coding.history_load.requests", *n, labels)
	w.WriteHeader(http.StatusNoContent)
}
