package github

// G-02（docs/plan/github-app.md 第 3 节“收件箱”、第 4 节 G-02）：Webhook 收件与对账。Postgres 用 Runtime 的 pgtest 拉起临时实例。
//
//  1. 签名错或缺：401，不落库、不解析 JSON。只认 X-Hub-Signature-256。
//  2. 目标 App ID 不符：拒绝。
//  3. 超过上限：413，不落库。
//  4. 处理函数卡住时，接收仍在 1 秒内回 2xx。
//  5. 同一个 delivery：done、processing 的回 2xx 不重复处理；dead 的重新入队。重启后去重仍生效；两个副本并发领取，同一条只处理一次。
//  6. 失败按退避重试，5 次后进死信；Replay 能重放；processing 的租约到期后重新领取。
//  7. 对账：GitHub 那边 3 天内失败的投递按 id 重投，超过 3 天的、成功的、已经收到的不重投；每轮有上限；同一条不重投第二次。
//  8. 负载 7 天后清掉，行留着（去重照常）；库连不上时回 503 并计数。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/adapters/postgres/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

const testSecret = "webhook-test-secret"

type fakeApp struct{}

func (fakeApp) VerifyWebhook(body []byte, header string) bool {
	return header == signBody(body)
}

func (fakeApp) AppJWT(time.Time) (string, error) { return "app-jwt", nil }

func signBody(body []byte) string {
	m := hmac.New(sha256.New, []byte(testSecret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	t     *testing.T
	dsn   string
	clock *clock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	c := pgtest.Current(t)
	return &fixture{t: t, dsn: c.DSN(pgtest.AppRole, c.NewDatabase(t)), clock: &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}}
}

func (f *fixture) service(edit func(*Options)) *Service {
	f.t.Helper()
	opts := Options{DSN: f.dsn, App: fakeApp{}, AppID: "5542758", Now: f.clock.now, Backoff: func(int) time.Duration { return time.Minute }}
	if edit != nil {
		edit(&opts)
	}
	s, err := New(context.Background(), opts)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(s.Close)
	return s
}

// post 投一个 Webhook；sig 为空时按正确的 secret 签。
func post(t *testing.T, h http.Handler, delivery, event string, body []byte, sig string, headers map[string]string) int {
	t.Helper()
	if sig == "" {
		sig = signBody(body)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
	req.Header.Set("X-GitHub-Hook-Installation-Target-ID", "5542758")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func payload(action string) []byte {
	return []byte(fmt.Sprintf(`{"action":%q,"installation":{"id":42}}`, action))
}

func count(t *testing.T, s *Service) int {
	t.Helper()
	n, err := s.count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBadOrMissingSignatureIs401AndNothingIsStored(t *testing.T) {
	s := newFixture(t).service(nil)
	body := []byte(`not json at all`)
	for _, sig := range []string{"missing", "sha256=00", "sha1=" + strings.TrimPrefix(signBody(body), "sha256=")} {
		header := sig
		if sig == "missing" {
			header = "-"
		}
		if code := post(t, s, "d-bad", "push", body, header, nil); code != http.StatusUnauthorized {
			t.Fatalf("签名 %q：应 401（不先解析 JSON），实际 %d", sig, code)
		}
	}
	if n := count(t, s); n != 0 {
		t.Fatalf("签名不对的不落库：%d", n)
	}
	if st := s.Stats(); st.SignatureFailures != 3 {
		t.Fatalf("签名失败计数：%+v", st)
	}
}

func TestWrongTargetAppIsRejected(t *testing.T) {
	s := newFixture(t).service(nil)
	code := post(t, s, "d-other-app", "push", payload("x"), "", map[string]string{"X-GitHub-Hook-Installation-Target-ID": "999"})
	if code != http.StatusForbidden || count(t, s) != 0 {
		t.Fatalf("目标 App ID 不符：%d", code)
	}
}

func TestOversizedBodyIs413(t *testing.T) {
	s := newFixture(t).service(func(o *Options) { o.MaxBody = 1024 })
	big := []byte(`{"action":"` + strings.Repeat("x", 2048) + `"}`)
	if code := post(t, s, "d-big", "push", big, "", nil); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超过上限：%d", code)
	}
	if count(t, s) != 0 {
		t.Fatal("超过上限的不落库")
	}
	if code := post(t, s, "d-small", "push", payload("ok"), "", nil); code != http.StatusAccepted {
		t.Fatalf("上限之内照收：%d", code)
	}
}

func TestReceiveAnswersFastEvenWhenTheHandlerHangs(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f := newFixture(t)
	s := f.service(func(o *Options) {
		o.Now = nil // 后台循环按真实时间
		o.Handlers = map[string]Handler{"push": func(ctx context.Context, d Delivery) error {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	defer close(release)
	begin := time.Now()
	if code := post(t, s, "d-slow-1", "push", payload("a"), "", nil); code != http.StatusAccepted {
		t.Fatalf("入箱：%d", code)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("后台没有开始处理")
	}
	if code := post(t, s, "d-slow-2", "push", payload("b"), "", nil); code != http.StatusAccepted {
		t.Fatalf("处理函数卡着时照常入箱：%d", code)
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("接收不等处理：%s", elapsed)
	}
	start := time.Now()
	post(t, s, "d-slow-3", "push", payload("c"), "", nil)
	if time.Since(start) > time.Second {
		t.Fatalf("接收在 1 秒内回 2xx：%s", time.Since(start))
	}
}

func TestDuplicatesAreNotProcessedTwiceAcrossRestartsAndReplicas(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	calls := map[string]int{}
	handler := func(ctx context.Context, d Delivery) error {
		mu.Lock()
		calls[d.ID]++
		mu.Unlock()
		return nil
	}
	a := f.service(func(o *Options) { o.Handlers = map[string]Handler{"push": handler} })
	if code := post(t, a, "d-1", "push", payload("x"), "", nil); code != http.StatusAccepted {
		t.Fatal(code)
	}
	if _, err := a.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 同一个 delivery 再来（GitHub 重投时 delivery 不变）：2xx，不再处理；换一个进程（重启）也一样。
	b := f.service(func(o *Options) { o.Handlers = map[string]Handler{"push": handler} })
	for _, s := range []*Service{a, b} {
		if code := post(t, s, "d-1", "push", payload("x"), "", nil); code != http.StatusAccepted {
			t.Fatal(code)
		}
		if _, err := s.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls["d-1"] != 1 {
		t.Fatalf("done 的不重复处理：%d", calls["d-1"])
	}

	// 两个副本并发领取：每条只处理一次。
	for i := range 20 {
		post(t, a, fmt.Sprintf("d-many-%d", i), "push", payload("x"), "", nil)
	}
	var wg sync.WaitGroup
	for _, s := range []*Service{a, b} {
		wg.Add(1)
		go func(s *Service) {
			defer wg.Done()
			for range 10 {
				if _, err := s.ProcessOnce(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}(s)
	}
	wg.Wait()
	for i := range 20 {
		if n := calls[fmt.Sprintf("d-many-%d", i)]; n != 1 {
			t.Fatalf("d-many-%d 处理了 %d 次", i, n)
		}
	}
}

func TestFailuresRetryWithBackoffThenDeadLetterAndReplay(t *testing.T) {
	f := newFixture(t)
	fail := true
	var attempts int
	s := f.service(func(o *Options) {
		o.Handlers = map[string]Handler{"push": func(ctx context.Context, d Delivery) error {
			attempts++
			if fail {
				return fmt.Errorf("boom")
			}
			return nil
		}}
	})
	post(t, s, "d-fail", "push", payload("x"), "", nil)
	for i := range 5 {
		if _, err := s.ProcessOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		st, _ := s.Get(context.Background(), "d-fail")
		want := "retry"
		if i == 4 {
			want = "dead"
		}
		if st.State != want || st.Attempts != i+1 {
			t.Fatalf("第 %d 次失败后：%+v", i+1, st)
		}
		// 退避之前不再领取。
		if n, _ := s.ProcessOnce(context.Background()); n != 0 && i < 4 {
			t.Fatalf("退避期间不领取：%d", n)
		}
		f.clock.advance(time.Minute)
	}
	if attempts != 5 {
		t.Fatalf("5 次后进死信：%d", attempts)
	}
	// dead 的再投一次：重新入队。
	post(t, s, "d-fail", "push", payload("x"), "", nil)
	if st, _ := s.Get(context.Background(), "d-fail"); st.State != "received" || st.Attempts != 0 {
		t.Fatalf("dead 的重新入队：%+v", st)
	}
	fail = false
	if _, err := s.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Get(context.Background(), "d-fail"); st.State != "done" {
		t.Fatalf("修好之后处理完：%+v", st)
	}
	// github-replay：任何状态都能重放。
	if err := s.Replay(context.Background(), "d-fail"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Get(context.Background(), "d-fail"); st.State != "received" {
		t.Fatalf("重放后重新入队：%+v", st)
	}
	if err := s.Replay(context.Background(), "nope"); err == nil {
		t.Fatal("没有这条时报错")
	}
}

func TestExpiredLeasesAreReclaimed(t *testing.T) {
	f := newFixture(t)
	var handled []string
	s := f.service(func(o *Options) {
		o.Lease = 2 * time.Minute
		o.Handlers = map[string]Handler{"push": func(ctx context.Context, d Delivery) error {
			handled = append(handled, d.ID)
			return nil
		}}
	})
	post(t, s, "d-crash", "push", payload("x"), "", nil)
	// 一个进程领走之后崩溃：只领不处理。
	claimed, err := s.claim(context.Background(), 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("领取：%v %v", claimed, err)
	}
	if n, _ := s.ProcessOnce(context.Background()); n != 0 {
		t.Fatal("租约没到期时别人领不走")
	}
	f.clock.advance(3 * time.Minute)
	if n, _ := s.ProcessOnce(context.Background()); n != 1 || len(handled) != 1 {
		t.Fatalf("租约到期后重新领取：%d %v", n, handled)
	}
}

type fakeHookAPI struct {
	mu          sync.Mutex
	deliveries  []map[string]any
	redelivered []string
	auth        []string
}

func (f *fakeHookAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/app/hook/deliveries":
		_ = json.NewEncoder(w).Encode(f.deliveries)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/app/hook/deliveries/") && strings.HasSuffix(r.URL.Path, "/attempts"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/app/hook/deliveries/"), "/attempts")
		f.redelivered = append(f.redelivered, id)
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestReconcileRedeliversFailedDeliveriesWithinTheWindow(t *testing.T) {
	f := newFixture(t)
	now := f.clock.now()
	at := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	api := &fakeHookAPI{deliveries: []map[string]any{
		{"id": 1, "guid": "g-fail-1", "delivered_at": at(time.Hour), "status_code": 502, "status": "Bad Gateway", "event": "push"},
		{"id": 2, "guid": "g-fail-2", "delivered_at": at(30 * time.Hour), "status_code": 0, "status": "Timeout", "event": "push"},
		{"id": 3, "guid": "g-fail-3", "delivered_at": at(70 * time.Hour), "status_code": 503, "status": "Service Unavailable", "event": "pull_request"},
		{"id": 4, "guid": "g-old", "delivered_at": at(80 * time.Hour), "status_code": 503, "status": "Service Unavailable", "event": "push"},
		{"id": 5, "guid": "g-ok", "delivered_at": at(time.Hour), "status_code": 202, "status": "OK", "event": "push"},
		{"id": 6, "guid": "g-have", "delivered_at": at(time.Hour), "status_code": 500, "status": "Internal Server Error", "event": "push"},
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	s := f.service(func(o *Options) { o.APIURL, o.ReconcileLimit = srv.URL, 2 })
	post(t, s, "g-have", "push", payload("x"), "", nil) // 后来自己收到了的
	n, err := s.Reconcile(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("每轮有上限（2）：%d %v", n, err)
	}
	n, err = s.Reconcile(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("第二轮重投剩下的那条：%d %v", n, err)
	}
	if n, _ := s.Reconcile(context.Background()); n != 0 {
		t.Fatalf("同一条不重投第二次：%d", n)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	got := strings.Join(api.redelivered, ",")
	if got != "1,2,3" {
		t.Fatalf("只重投 3 天内失败、还没收到的：%s", got)
	}
	for _, a := range api.auth {
		if a != "Bearer app-jwt" {
			t.Fatalf("调 App 的接口用 App 的 JWT：%q", a)
		}
	}
}

func TestPayloadsArePrunedAfterSevenDaysButDedupeStays(t *testing.T) {
	f := newFixture(t)
	calls := 0
	s := f.service(func(o *Options) {
		o.Handlers = map[string]Handler{"push": func(context.Context, Delivery) error { calls++; return nil }}
	})
	post(t, s, "d-old", "push", payload("x"), "", nil)
	_, _ = s.ProcessOnce(context.Background())
	f.clock.advance(8 * 24 * time.Hour)
	if n, err := s.Prune(context.Background()); err != nil || n != 1 {
		t.Fatalf("清掉 7 天前的负载：%d %v", n, err)
	}
	if st, _ := s.Get(context.Background(), "d-old"); st.HasPayload || st.State != "done" {
		t.Fatalf("负载清空、行留着：%+v", st)
	}
	post(t, s, "d-old", "push", payload("x"), "", nil)
	_, _ = s.ProcessOnce(context.Background())
	if calls != 1 {
		t.Fatalf("清掉负载之后去重照常：%d", calls)
	}
}

func TestDatabaseUnavailableReturns503(t *testing.T) {
	s := newFixture(t).service(nil)
	s.db.Close()
	if code := post(t, s, "d-down", "push", payload("x"), "", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("库连不上时回 503：%d", code)
	}
	if st := s.Stats(); st.Unavailable != 1 {
		t.Fatalf("计数：%+v", st)
	}
}
