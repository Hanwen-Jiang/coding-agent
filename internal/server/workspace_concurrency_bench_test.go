package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type concurrencyScenarioResult struct {
	Name            string  `json:"name"`
	Replicas        int     `json:"replicas"`
	Concurrency     int     `json:"concurrency"`
	TotalRequests   int     `json:"total_requests"`
	Succeeded       int     `json:"succeeded"`
	Failed          int     `json:"failed"`
	ElapsedMs       float64 `json:"elapsed_ms"`
	QPS             float64 `json:"qps"`
	P50Ms           float64 `json:"p50_ms"`
	P95Ms           float64 `json:"p95_ms"`
	P99Ms           float64 `json:"p99_ms"`
	DownstreamStart int64   `json:"downstream_starts"`
	DownstreamGet   int64   `json:"downstream_gets"`
}

type concurrencyBenchReport struct {
	Arm             string                      `json:"arm"`
	Replicas        int                         `json:"replicas"`
	Concurrency     int                         `json:"concurrency"`
	TotalRequests   int                         `json:"total_requests"`
	TotalSucceeded  int                         `json:"total_succeeded"`
	OverallQPS      float64                     `json:"overall_qps"`
	OverallP95Ms    float64                     `json:"overall_p95_ms"`
	OverallP50Ms    float64                     `json:"overall_p50_ms"`
	DownstreamCalls int64                       `json:"downstream_calls"`
	Scenarios       []concurrencyScenarioResult `json:"scenarios"`
}

func percentileMs(latencies []float64, q float64) float64 {
	if len(latencies) == 0 {
		return 0
	}
	cp := append([]float64(nil), latencies...)
	sort.Float64s(cp)
	idx := int(math.Ceil(float64(len(cp))*q)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

// TestWorkspaceHighConcurrencyBenchmark runs a real 4-replica, 200-concurrency,
// 14,000-request stress benchmark across hot-key duplicate submission, active
// status polling storms, and completed record reads.
// Run with: AR_RUN_CONCURRENCY_BENCH=1 AR_BENCH_OUT=/path/to/out.json AR_BENCH_ARM=baseline go test ./internal/server -run TestWorkspaceHighConcurrencyBenchmark -v
func TestWorkspaceHighConcurrencyBenchmark(t *testing.T) {
	if os.Getenv("AR_RUN_CONCURRENCY_BENCH") != "1" {
		t.Skip("set AR_RUN_CONCURRENCY_BENCH=1 to execute high-concurrency benchmark")
	}
	gin.SetMode(gin.ReleaseMode)
	const (
		numReplicas = 4
		concurrency = 200
	)
	dir := t.TempDir()
	var startCalls atomic.Int64
	var getCalls atomic.Int64
	var readyFlag atomic.Bool

	remote := &workspaceRemote{started: map[string]map[string]any{}}
	countingRT := workspaceRoundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/StartWorkspaceOperation") {
			startCalls.Add(1)
		} else if strings.HasSuffix(r.URL.Path, "/GetWorkspaceOperation") {
			getCalls.Add(1)
			// Simulate realistic 15ms downstream RPC latency while running.
			if !readyFlag.Load() {
				time.Sleep(15 * time.Millisecond)
			}
		}
		remote.mu.Lock()
		remote.ready = readyFlag.Load()
		remote.mu.Unlock()
		return remote.roundTrip(r)
	})

	replicas := make([]*Server, numReplicas)
	for i := range replicas {
		api := NewTaskAPI("http://runtime.invalid")
		client := &http.Client{Transport: countingRT}
		api.client, api.slow = client, client
		s := &Server{cfg: Config{Workspaces: "runtime"}, store: NewStore(dir), api: api, providers: map[string]*Provider{}}
		s.ws = runtimeWorkspaces{api: api}
		replicas[i] = s
	}
	id := workspaceOwner()

	runLoad := func(name string, total int, fn func(workerIdx, reqIdx int) bool) concurrencyScenarioResult {
		startBefore := startCalls.Load()
		getBefore := getCalls.Load()
		latencies := make([]float64, total)
		var okCount atomic.Int64
		var failCount atomic.Int64
		var nextIdx atomic.Int64

		t0 := time.Now()
		var wg sync.WaitGroup
		wg.Add(concurrency)
		for w := 0; w < concurrency; w++ {
			worker := w
			go func() {
				defer wg.Done()
				for {
					idx := int(nextIdx.Add(1) - 1)
					if idx >= total {
						return
					}
					reqStart := time.Now()
					ok := fn(worker, idx)
					latencies[idx] = float64(time.Since(reqStart).Microseconds()) / 1000.0
					if ok {
						okCount.Add(1)
					} else {
						failCount.Add(1)
					}
				}
			}()
		}
		wg.Wait()
		elapsed := time.Since(t0)
		elapsedMs := float64(elapsed.Microseconds()) / 1000.0
		return concurrencyScenarioResult{
			Name:            name,
			Replicas:        numReplicas,
			Concurrency:     concurrency,
			TotalRequests:   total,
			Succeeded:       int(okCount.Load()),
			Failed:          int(failCount.Load()),
			ElapsedMs:       elapsedMs,
			QPS:             float64(total) / elapsed.Seconds(),
			P50Ms:           percentileMs(latencies, 0.50),
			P95Ms:           percentileMs(latencies, 0.95),
			P99Ms:           percentileMs(latencies, 0.99),
			DownstreamStart: startCalls.Load() - startBefore,
			DownstreamGet:   getCalls.Load() - getBefore,
		}
	}

	// Scenario 1: 200 concurrent clients blasting 2,000 duplicate POST requests for the same Idempotency-Key across 4 replicas.
	hotKey := "bench-hot-key-duplicate-submit-0001"
	hotBody := `{"name":"bench-app"}`
	hotOpID := "who-" + workspaceHash(id.TenantID, id.UserID, hotKey)
	s1 := runLoad("hot_duplicate_submit_200", 2000, func(_ int, i int) bool {
		srv := replicas[i%numReplicas]
		req := httptest.NewRequest("POST", "/api/projects", strings.NewReader(hotBody)).WithContext(context.Background())
		req.Header.Set("X-Coding-Async", "1")
		req.Header.Set("Idempotency-Key", hotKey)
		out := httptest.NewRecorder()
		srv.workspaceHTTP("create_project")(testContext(out, req), id)
		return out.Code == http.StatusAccepted
	})

	// Scenario 2: 200 concurrent clients hammering 6,000 GET status polls across 4 replicas while operation is RUNNING.
	s2 := runLoad("running_status_poll_storm_200", 6000, func(_ int, i int) bool {
		srv := replicas[i%numReplicas]
		req := httptest.NewRequest("GET", "/api/workspace-operations/"+hotOpID, nil)
		out := httptest.NewRecorder()
		srv.handleWorkspaceHTTPStatus(testContext(out, req, "operation", hotOpID), id)
		return out.Code == http.StatusOK
	})

	// Complete the running operation cleanly and wait for background runners to drain.
	readyFlag.Store(true)
	waitWorkspaceStatus(t, replicas[0], id, hotOpID, "COMPLETED")
	for _, r := range replicas {
		r.background.Wait()
	}

	// Scenario 3: 200 concurrent clients hammering 6,000 GET status polls across 4 replicas for COMPLETED operation.
	s3 := runLoad("completed_status_poll_storm_200", 6000, func(_ int, i int) bool {
		srv := replicas[i%numReplicas]
		req := httptest.NewRequest("GET", "/api/workspace-operations/"+hotOpID, nil)
		out := httptest.NewRecorder()
		srv.handleWorkspaceHTTPStatus(testContext(out, req, "operation", hotOpID), id)
		return out.Code == http.StatusOK
	})

	scenarios := []concurrencyScenarioResult{s1, s2, s3}
	totalReq := s1.TotalRequests + s2.TotalRequests + s3.TotalRequests
	totalOK := s1.Succeeded + s2.Succeeded + s3.Succeeded
	totalElapsedS := (s1.ElapsedMs + s2.ElapsedMs + s3.ElapsedMs) / 1000.0
	weightedP95 := (s1.P95Ms*float64(s1.TotalRequests) + s2.P95Ms*float64(s2.TotalRequests) + s3.P95Ms*float64(s3.TotalRequests)) / float64(totalReq)
	weightedP50 := (s1.P50Ms*float64(s1.TotalRequests) + s2.P50Ms*float64(s2.TotalRequests) + s3.P50Ms*float64(s3.TotalRequests)) / float64(totalReq)

	report := concurrencyBenchReport{
		Arm:             os.Getenv("AR_BENCH_ARM"),
		Replicas:        numReplicas,
		Concurrency:     concurrency,
		TotalRequests:   totalReq,
		TotalSucceeded:  totalOK,
		OverallQPS:      float64(totalReq) / totalElapsedS,
		OverallP95Ms:    weightedP95,
		OverallP50Ms:    weightedP50,
		DownstreamCalls: startCalls.Load() + getCalls.Load(),
		Scenarios:       scenarios,
	}
	if totalOK != totalReq {
		t.Fatalf("expected all %d requests to succeed, got %d", totalReq, totalOK)
	}
	if startCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 downstream StartWorkspaceOperation across 2000 duplicate submits, got %d", startCalls.Load())
	}

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("BENCHMARK REPORT (%s):\n%s", report.Arm, string(raw))
	if outPath := os.Getenv("AR_BENCH_OUT"); outPath != "" {
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outPath, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
