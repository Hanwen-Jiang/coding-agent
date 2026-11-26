package adminops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type TelemetryConfig struct {
	VictoriaMetricsURL string
	AlertmanagerURL    string
	GrafanaURL         string
	GrafanaExternalURL string
	GrafanaUser        string
	GrafanaPassword    string
	TaskAPIURL         string
	CrawlerURL         string
	CDRunnerURL        string
	CDAPIURL           string
}

type AlertItem struct {
	Name     string `json:"name"`
	Severity string `json:"severity,omitempty"`
	State    string `json:"state"`
	Service  string `json:"service,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Since    string `json:"since,omitempty"`
}

type CompanionMetrics struct {
	HintChecks      atomic.Int64
	ConsentPrompts  atomic.Int64
	HintsRevealed   atomic.Int64
	ConsentBlocked  atomic.Int64
	HistoryAnalyses atomic.Int64
	WebhookTriggers atomic.Int64
}

func (m *CompanionMetrics) Snapshot() map[string]int64 {
	return map[string]int64{
		"hint_checks":      m.HintChecks.Load(),
		"consent_prompts":  m.ConsentPrompts.Load(),
		"hints_revealed":   m.HintsRevealed.Load(),
		"consent_blocked":  m.ConsentBlocked.Load(),
		"history_analyses": m.HistoryAnalyses.Load(),
		"webhook_triggers": m.WebhookTriggers.Load(),
	}
}

type MetricPoint struct {
	TS    int64   `json:"ts"`
	Value float64 `json:"value"`
}

type MetricSeries struct {
	Name   string        `json:"name"`
	Label  string        `json:"label"`
	Unit   string        `json:"unit,omitempty"`
	Latest *float64      `json:"latest,omitempty"`
	Points []MetricPoint `json:"points"`
}

type GrafanaDashboard struct {
	UID   string `json:"uid"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type TelemetrySummary struct {
	CheckedAt        string             `json:"checked_at"`
	VictoriaMetrics  map[string]any     `json:"victoria_metrics"`
	Grafana          map[string]any     `json:"grafana"`
	TaskAPI          map[string]any     `json:"task_api"`
	Crawler          map[string]any     `json:"crawler"`
	CodingDanceInfra map[string]any     `json:"coding_dance_infra,omitempty"`
	Alerts           []AlertItem        `json:"alerts"`
	Series           []MetricSeries     `json:"series"`
	Dashboards       []GrafanaDashboard `json:"dashboards"`
	CompanionMetrics map[string]int64   `json:"companion_metrics"`
}

type TelemetryClient struct {
	cfg       TelemetryConfig
	http      *http.Client
	Companion CompanionMetrics
}

func NewTelemetryClient(cfg TelemetryConfig) *TelemetryClient {
	return &TelemetryClient{
		cfg:  cfg,
		http: &http.Client{Timeout: 4 * time.Second},
	}
}

func (t *TelemetryClient) Summary(ctx context.Context) TelemetrySummary {
	out := TelemetrySummary{
		CheckedAt:        time.Now().UTC().Format(time.RFC3339),
		VictoriaMetrics:  map[string]any{"configured": strings.TrimSpace(t.cfg.VictoriaMetricsURL) != "", "available": false},
		Grafana:          map[string]any{"configured": strings.TrimSpace(t.cfg.GrafanaURL) != "", "available": false, "external_url": strings.TrimSpace(t.cfg.GrafanaExternalURL)},
		TaskAPI:          map[string]any{"configured": strings.TrimSpace(t.cfg.TaskAPIURL) != "", "healthy": false},
		Crawler:          map[string]any{"configured": strings.TrimSpace(t.cfg.CrawlerURL) != "", "available": false},
		Alerts:           []AlertItem{},
		Series:           []MetricSeries{},
		Dashboards:       []GrafanaDashboard{},
		CompanionMetrics: t.Companion.Snapshot(),
	}

	if base := strings.TrimRight(strings.TrimSpace(t.cfg.TaskAPIURL), "/"); base != "" {
		started := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if err == nil {
			resp, err := t.http.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				out.TaskAPI["healthy"] = resp.StatusCode == http.StatusOK
				out.TaskAPI["status_code"] = resp.StatusCode
				out.TaskAPI["latency_ms"] = time.Since(started).Milliseconds()
			} else {
				out.TaskAPI["error"] = "unreachable"
			}
		}
	}

	if vmBase := strings.TrimRight(strings.TrimSpace(t.cfg.VictoriaMetricsURL), "/"); vmBase != "" {
		t.populateVM(ctx, vmBase, &out)
	}
	if gfBase := strings.TrimRight(strings.TrimSpace(t.cfg.GrafanaURL), "/"); gfBase != "" {
		t.populateGrafana(ctx, gfBase, &out)
	}
	if crBase := strings.TrimRight(strings.TrimSpace(t.cfg.CrawlerURL), "/"); crBase != "" {
		t.populateCrawler(ctx, crBase, &out)
	}
	t.populateAlerts(ctx, &out)
	t.populateCodingDanceInfra(ctx, &out)
	return out
}

func (t *TelemetryClient) populateVM(ctx context.Context, base string, out *TelemetrySummary) {
	upVal, ok := t.vmInstant(ctx, base, "count(up == 1)")
	if !ok {
		// Check health endpoint before marking unreachable
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
		if err != nil {
			out.VictoriaMetrics["error"] = "invalid_url"
			return
		}
		resp, err := t.http.Do(req)
		if err != nil {
			out.VictoriaMetrics["error"] = "unreachable"
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			out.VictoriaMetrics["error"] = fmt.Sprintf("http_%d", resp.StatusCode)
			return
		}
	}
	out.VictoriaMetrics["available"] = true
	if ok {
		out.VictoriaMetrics["targets_up"] = upVal
	}
	if spanRate, hasSpan := t.vmInstant(ctx, base, "sum(rate(otelcol_receiver_accepted_spans_total[5m]))"); hasSpan {
		out.VictoriaMetrics["otel_spans_per_sec"] = spanRate
	}
	if callsTotal, hasCalls := t.vmInstant(ctx, base, "sum(runtime_model_calls_total)"); hasCalls {
		out.VictoriaMetrics["runtime_model_calls_total"] = callsTotal
	}
	if errTotal, hasErr := t.vmInstant(ctx, base, `sum(rate(runtime_model_calls_total{status!="ok"}[5m]))`); hasErr {
		out.VictoriaMetrics["runtime_model_error_rate"] = errTotal
	}

	now := time.Now().UTC()
	start := now.Add(-1 * time.Hour)
	for _, spec := range []struct {
		name  string
		label string
		query string
		unit  string
	}{
		{"otel_spans_rate", "OTel Accepted Spans / sec", "sum(rate(otelcol_receiver_accepted_spans_total[5m]))", "spans/s"},
		{"targets_up", "Healthy Scrape Targets", "count(up == 1)", "targets"},
		{"go_goroutines", "Go Runtime Goroutines", "sum(go_goroutines)", "goroutines"},
	} {
		pts := t.vmRange(ctx, base, spec.query, start, now, 120)
		var latest *float64
		if len(pts) > 0 {
			v := pts[len(pts)-1].Value
			latest = &v
		}
		out.Series = append(out.Series, MetricSeries{
			Name:   spec.name,
			Label:  spec.label,
			Unit:   spec.unit,
			Latest: latest,
			Points: pts,
		})
	}
}

func (t *TelemetryClient) vmInstant(ctx context.Context, base, promQL string) (float64, bool) {
	u := base + "/api/v1/query?query=" + url.QueryEscape(promQL)
	var wire struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if !t.getJSON(ctx, u, &wire) || wire.Status != "success" || len(wire.Data.Result) == 0 {
		return 0, false
	}
	s, ok := wire.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func (t *TelemetryClient) vmRange(ctx context.Context, base, promQL string, start, end time.Time, stepSec int) []MetricPoint {
	q := url.Values{}
	q.Set("query", promQL)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", strconv.Itoa(stepSec))
	u := base + "/api/v1/query_range?" + q.Encode()
	var wire struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Values [][2]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if !t.getJSON(ctx, u, &wire) || wire.Status != "success" || len(wire.Data.Result) == 0 {
		return []MetricPoint{}
	}
	raw := wire.Data.Result[0].Values
	pts := make([]MetricPoint, 0, len(raw))
	for _, pair := range raw {
		tsFloat, _ := pair[0].(float64)
		valStr, ok := pair[1].(string)
		if !ok {
			continue
		}
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		pts = append(pts, MetricPoint{TS: int64(tsFloat), Value: val})
	}
	return pts
}

func (t *TelemetryClient) populateGrafana(ctx context.Context, base string, out *TelemetrySummary) {
	var health struct {
		Database string `json:"database"`
		Version  string `json:"version"`
		Commit   string `json:"commit"`
	}
	if !t.getJSON(ctx, base+"/api/health", &health) {
		out.Grafana["error"] = "unreachable"
		return
	}
	out.Grafana["available"] = strings.EqualFold(health.Database, "ok")
	out.Grafana["database"] = health.Database
	out.Grafana["version"] = health.Version

	var boards []struct {
		UID   string `json:"uid"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if t.getJSONWithAuth(ctx, base+"/api/search?type=dash-db&limit=20", t.cfg.GrafanaUser, t.cfg.GrafanaPassword, &boards) {
		extBase := strings.TrimRight(strings.TrimSpace(t.cfg.GrafanaExternalURL), "/")
		for _, b := range boards {
			link := b.URL
			if extBase != "" && strings.HasPrefix(b.URL, "/") {
				link = extBase + b.URL
			}
			out.Dashboards = append(out.Dashboards, GrafanaDashboard{UID: b.UID, Title: b.Title, URL: link})
		}
	}
}

func (t *TelemetryClient) populateCrawler(ctx context.Context, base string, out *TelemetrySummary) {
	var status map[string]any
	if !t.getJSON(ctx, base+"/v1/status", &status) {
		out.Crawler["error"] = "unreachable"
		return
	}
	out.Crawler["available"] = true
	for _, k := range []string{"upstream_search", "headless_configured", "internal_allow_count", "cache_entries", "metrics"} {
		if v, ok := status[k]; ok {
			out.Crawler[k] = v
		}
	}
}

func (t *TelemetryClient) populateAlerts(ctx context.Context, out *TelemetrySummary) {
	seen := map[string]bool{}
	add := func(it AlertItem) {
		if it.Name == "" {
			return
		}
		key := it.Name + "\x00" + it.Service + "\x00" + it.State
		if seen[key] {
			return
		}
		seen[key] = true
		out.Alerts = append(out.Alerts, it)
	}

	if amBase := strings.TrimRight(strings.TrimSpace(t.cfg.AlertmanagerURL), "/"); amBase != "" {
		var amAlerts []struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			StartsAt    string            `json:"startsAt"`
			Status      struct {
				State string `json:"state"`
			} `json:"status"`
		}
		if t.getJSON(ctx, amBase+"/api/v2/alerts?active=true", &amAlerts) {
			for _, a := range amAlerts {
				state := a.Status.State
				if state == "" {
					state = "active"
				}
				add(AlertItem{
					Name:     a.Labels["alertname"],
					Severity: a.Labels["severity"],
					State:    state,
					Service:  a.Labels["service"],
					Summary:  a.Annotations["summary"],
					Since:    a.StartsAt,
				})
			}
		}
	}

	if vmBase := strings.TrimRight(strings.TrimSpace(t.cfg.VictoriaMetricsURL), "/"); vmBase != "" {
		var vmResp struct {
			Status string `json:"status"`
			Data   struct {
				Alerts []struct {
					Labels      map[string]string `json:"labels"`
					Annotations map[string]string `json:"annotations"`
					State       string            `json:"state"`
					ActiveAt    string            `json:"activeAt"`
				} `json:"alerts"`
			} `json:"data"`
		}
		if t.getJSON(ctx, vmBase+"/api/v1/alerts", &vmResp) && vmResp.Status == "success" {
			for _, a := range vmResp.Data.Alerts {
				add(AlertItem{
					Name:     a.Labels["alertname"],
					Severity: a.Labels["severity"],
					State:    a.State,
					Service:  a.Labels["service"],
					Summary:  a.Annotations["summary"],
					Since:    a.ActiveAt,
				})
			}
		}
	}
}

func (t *TelemetryClient) populateCodingDanceInfra(ctx context.Context, out *TelemetrySummary) {
	runnerURL := strings.TrimRight(strings.TrimSpace(t.cfg.CDRunnerURL), "/")
	apiURL := strings.TrimRight(strings.TrimSpace(t.cfg.CDAPIURL), "/")
	if runnerURL == "" && apiURL == "" {
		return
	}
	info := map[string]any{}
	if runnerURL != "" {
		var status map[string]any
		entry := map[string]any{"configured": true, "available": false}
		if t.getJSON(ctx, runnerURL+"/v1/status", &status) {
			entry["available"] = true
			entry["status"] = status
		}
		info["cd_runner"] = entry
	}
	if apiURL != "" {
		entry := map[string]any{"configured": true, "healthy": false}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/healthz", nil)
		if err == nil {
			resp, err := t.http.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				entry["healthy"] = resp.StatusCode == http.StatusOK
			}
		}
		info["cd_api"] = entry
	}
	out.CodingDanceInfra = info
}

func (t *TelemetryClient) getJSON(ctx context.Context, target string, out any) bool {
	return t.getJSONWithAuth(ctx, target, "", "", out)
}

func (t *TelemetryClient) getJSONWithAuth(ctx context.Context, target, user, pass string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	if strings.TrimSpace(user) != "" && pass != "" {
		req.SetBasicAuth(strings.TrimSpace(user), pass)
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return false
	}
	return json.Unmarshal(data, out) == nil
}
