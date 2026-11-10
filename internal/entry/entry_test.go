package entry_test

// EP-01（docs/plan/entry-points.md 第 11 节）：接入点的框架。每条用例在两种存储上各跑一遍：文件（单进程）与 Postgres
// （用 Runtime 的 pgtest 拉起临时实例）。
//
//  3. 同一个触发收到两次只起一个任务；两个副本并发推进，同一条只处理一次。
//  4. 投递单独记录：发不出去按退避重试，5 次进死信；重试不重跑任务；同一阶段不说两遍；发到一半进程没了的不重发。
//  5. 任务取消、失败时各有一条说明。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/adapters/postgres/pgtest"
	"github.com/agentruntime/coding-agent/internal/entry"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

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

// fakeHost 记下建了几个会话、提交了几个 Task；status 是 Task 现在的状态。
type fakeHost struct {
	mu       sync.Mutex
	sessions int
	submits  int
	status   string
	slow     time.Duration
}

func (h *fakeHost) OpenSession(context.Context, entry.Trigger, entry.TaskInput) (string, string, error) {
	time.Sleep(h.slow)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions++
	return "s1", "w1", nil
}

func (h *fakeHost) Submit(context.Context, entry.Trigger, entry.TaskInput) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.submits++
	return "tsk_1", nil
}

func (h *fakeHost) Check(context.Context, entry.Trigger) (entry.TaskStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return entry.TaskStatus{Status: h.status, Terminal: h.status != "" && h.status != "RUNNING", Done: h.status == "DONE"}, nil
}

func (h *fakeHost) Spent(context.Context, entry.Trigger) float64 { return 0 }

func (h *fakeHost) set(status string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status = status
}

func (h *fakeHost) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions, h.submits
}

// fakeKind 没有后续要做的事：任务做完就说 finished。
type fakeKind struct{}

func (fakeKind) Name() string         { return "fake" }
func (fakeKind) Sink() string         { return "memo" }
func (fakeKind) Limits() entry.Limits { return entry.Limits{RunsPerHour: 2, StartAttempts: 3} }
func (fakeKind) Admit(_ context.Context, t *entry.Trigger) (entry.Admission, error) {
	t.Tenant, t.User, t.ProjectID = "t1", "u1", "p1"
	return entry.Admission{}, nil
}
func (fakeKind) Compose(entry.Trigger) (entry.TaskInput, error) {
	return entry.TaskInput{Message: "do it", Title: "fake", ClientRequestID: "fake-1"}, nil
}
func (fakeKind) Words(_ entry.Trigger, o entry.Outcome) string {
	return strings.TrimSpace(o.Code + " " + o.Status)
}

// memoSink 记下发出去的话；failing 时发不出去。
type memoSink struct {
	mu      sync.Mutex
	sent    []entry.Message
	tries   int
	failing bool
}

func (*memoSink) Name() string { return "memo" }

func (m *memoSink) Deliver(_ context.Context, _ entry.Trigger, msg entry.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tries++
	if m.failing {
		return errors.New("对方不可用")
	}
	m.sent = append(m.sent, msg)
	return nil
}

func (m *memoSink) fail(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failing = on
}

func (m *memoSink) texts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, x := range m.sent {
		out = append(out, string(x.Phase)+":"+x.Text)
	}
	return out
}

type fixture struct {
	t     *testing.T
	store entry.Store
	clock *clock
	host  *fakeHost
	sink  *memoSink
	eng   *entry.Engine
}

func (f *fixture) engine() *entry.Engine {
	f.t.Helper()
	eng, err := entry.New(entry.Options{Store: f.store, Host: f.host, Now: f.clock.now})
	if err != nil {
		f.t.Fatal(err)
	}
	eng.Register(fakeKind{})
	eng.RegisterSink(f.sink)
	return eng
}

func (f *fixture) receive(id string) {
	f.t.Helper()
	if err := f.eng.Receive(context.Background(), entry.Trigger{ID: id, Kind: "fake", Scope: "scope-1", Input: []byte(`{}`)}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) advance() {
	f.t.Helper()
	if err := f.eng.Advance(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) state(id string) entry.State {
	f.t.Helper()
	t, _, err := f.store.Trigger(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return t.State
}

func (f *fixture) delivery(id string, phase entry.Phase) entry.Delivery {
	f.t.Helper()
	ds, err := f.store.Deliveries(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, d := range ds {
		if d.Phase == phase {
			return d
		}
	}
	f.t.Fatalf("没有 %s 阶段的投递：%+v", phase, ds)
	return entry.Delivery{}
}

// onStores 在文件与 Postgres 两种存储上各跑一遍 fn。
func onStores(t *testing.T, fn func(f *fixture)) {
	stores := map[string]func(t *testing.T) entry.Store{
		"file": func(t *testing.T) entry.Store { return entry.NewFile(filepath.Join(t.TempDir(), "entry.json")) },
		"postgres": func(t *testing.T) entry.Store {
			c := pgtest.Current(t)
			p, err := entry.NewPostgres(context.Background(), c.DSN(pgtest.AppRole, c.NewDatabase(t)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.Close)
			return p
		},
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			f := &fixture{t: t, store: open(t), clock: &clock{t: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}, host: &fakeHost{}, sink: &memoSink{}}
			f.eng = f.engine()
			fn(f)
		})
	}
}

func TestOneTriggerStartsOneTaskAndSaysEachPhaseOnce(t *testing.T) {
	onStores(t, func(f *fixture) {
		f.receive("fake:1")
		f.receive("fake:1")
		f.advance()
		f.advance()
		if sessions, submits := f.host.counts(); sessions != 1 || submits != 1 || f.state("fake:1") != entry.Running {
			t.Fatalf("收到两次只起一个任务：会话 %d、提交 %d、状态 %s", sessions, submits, f.state("fake:1"))
		}
		f.host.set("DONE")
		f.advance()
		f.advance()
		f.receive("fake:1")
		if got := f.sink.texts(); len(got) != 2 || got[0] != "accepted:started" || got[1] != "result:finished" {
			t.Fatalf("每个阶段只说一遍：%v", got)
		}
		if f.state("fake:1") != entry.Done {
			t.Fatalf("状态：%s", f.state("fake:1"))
		}
		if tr, _, _ := f.store.Trigger(context.Background(), "fake:1"); len(tr.Input) != 0 {
			t.Fatalf("指令与外部内容提交出去就清掉：%q", tr.Input)
		}
	})
}

func TestTwoReplicasAdvanceATriggerOnce(t *testing.T) {
	onStores(t, func(f *fixture) {
		f.host.slow = 200 * time.Millisecond
		f.receive("fake:1")
		a, b := f.engine(), f.engine()
		var wg sync.WaitGroup
		for _, eng := range []*entry.Engine{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := eng.Advance(context.Background()); err != nil {
					f.t.Error(err)
				}
			}()
		}
		wg.Wait()
		if sessions, submits := f.host.counts(); sessions != 1 || submits != 1 {
			t.Fatalf("两个副本并发推进只处理一次：会话 %d、提交 %d", sessions, submits)
		}
		if got := f.sink.texts(); len(got) != 1 {
			t.Fatalf("只说一遍：%v", got)
		}
	})
}

func TestRunsPerHourAreLimitedByScope(t *testing.T) {
	onStores(t, func(f *fixture) {
		f.receive("fake:1")
		f.receive("fake:2")
		f.receive("fake:3")
		if f.state("fake:3") != entry.Refused || f.state("fake:2") != entry.Accepted {
			t.Fatalf("超了次数的被拒：%s %s", f.state("fake:2"), f.state("fake:3"))
		}
		if got := f.sink.texts(); len(got) != 1 || got[0] != "failed:limit_runs" {
			t.Fatalf("说明一次：%v", got)
		}
		f.clock.advance(61 * time.Minute)
		f.receive("fake:4")
		if f.state("fake:4") != entry.Accepted {
			t.Fatalf("过了一小时又可以：%s", f.state("fake:4"))
		}
	})
}

func TestFailedDeliveriesRetryThenDeadLetterWithoutRerunningTheTask(t *testing.T) {
	onStores(t, func(f *fixture) {
		f.sink.fail(true)
		f.receive("fake:1")
		f.advance() // 起任务；“已受理”第一次发不出去
		if d := f.delivery("fake:1", entry.PhaseAccepted); d.State != entry.DeliveryPending || d.Attempts != 1 || !d.NextAt.After(f.clock.now()) {
			t.Fatalf("发不出去：记下次数，退避之后再试：%+v", d)
		}
		f.advance() // 没到时间不重试
		if f.sink.tries != 1 {
			t.Fatalf("没到时间不重试：%d", f.sink.tries)
		}
		for range 6 {
			f.clock.advance(31 * time.Minute)
			f.advance()
		}
		if d := f.delivery("fake:1", entry.PhaseAccepted); d.State != entry.DeliveryDead || d.Attempts != 5 || f.sink.tries != 5 {
			t.Fatalf("5 次之后进死信，不再试：%+v（试了 %d 次）", d, f.sink.tries)
		}
		if sessions, submits := f.host.counts(); sessions != 1 || submits != 1 || f.state("fake:1") != entry.Running {
			t.Fatalf("投递失败不重跑任务、不改状态：会话 %d、提交 %d、%s", sessions, submits, f.state("fake:1"))
		}
		// 对方恢复之后：结果照常发；死信要人手动重发。
		f.sink.fail(false)
		f.host.set("DONE")
		f.advance()
		if got := f.sink.texts(); len(got) != 1 || got[0] != "result:finished" {
			t.Fatalf("后面的阶段照常发：%v", got)
		}
		d := f.delivery("fake:1", entry.PhaseAccepted)
		if err := f.eng.Redeliver(context.Background(), "fake:1", d.Key); err != nil {
			t.Fatal(err)
		}
		if got := f.sink.texts(); len(got) != 2 || got[1] != "accepted:started" || f.delivery("fake:1", entry.PhaseAccepted).State != entry.DeliverySent {
			t.Fatalf("死信手动重发：%v", got)
		}
	})
}

func TestAnInterruptedSendIsNotResent(t *testing.T) {
	onStores(t, func(f *fixture) {
		f.receive("fake:1")
		ctx, now := context.Background(), f.clock.now()
		d := entry.Delivery{Key: entry.DeliveryKey("fake:1", entry.PhaseQuestion, "memo"), Trigger: "fake:1", Sink: "memo", Phase: entry.PhaseQuestion,
			Body: "confirm?", State: entry.DeliveryPending, NextAt: now, CreatedAt: now}
		if added, err := f.store.AddDelivery(ctx, d); err != nil || !added {
			t.Fatalf("登记：%v %v", added, err)
		}
		if again, err := f.store.AddDelivery(ctx, d); err != nil || again {
			t.Fatalf("同一个键只登记一次：%v %v", again, err)
		}
		// 领走之后进程没了：没有结果。
		if got, err := f.store.ClaimDeliveries(ctx, "", now, now.Add(2*time.Minute), 10); err != nil || len(got) != 1 || got[0].Interrupted {
			t.Fatalf("领取：%+v %v", got, err)
		}
		if got, _ := f.store.ClaimDeliveries(ctx, "", now, now.Add(2*time.Minute), 10); len(got) != 0 {
			t.Fatalf("租约没到期别人领不到：%+v", got)
		}
		f.clock.advance(3 * time.Minute)
		f.advance()
		if got := f.delivery("fake:1", entry.PhaseQuestion); got.State != entry.DeliveryUnknown {
			t.Fatalf("发到一半没有结果的标成 unknown：%+v", got)
		}
		for _, text := range f.sink.texts() {
			if strings.Contains(text, "confirm?") {
				t.Fatalf("不重发：%v", f.sink.texts())
			}
		}
	})
}

func TestCancelledAndFailedTasksSayWhy(t *testing.T) {
	onStores(t, func(f *fixture) {
		for i, status := range []string{"CANCELLED", "FAILED"} {
			id := []string{"fake:1", "fake:2"}[i]
			f.host.set("RUNNING")
			f.receive(id)
			f.advance()
			f.host.set(status)
			f.advance()
			f.advance()
			if d := f.delivery(id, entry.PhaseFailed); f.state(id) != entry.Failed || d.State != entry.DeliverySent || d.Body != "task_ended "+status {
				t.Fatalf("%s：状态 %s，说明 %+v", status, f.state(id), d)
			}
		}
	})
}
