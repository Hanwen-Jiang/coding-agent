// Package entry 是接入点的框架（EP-01，docs/plan/entry-points.md）：网页之外能起任务的地方（GitHub 的 @ 评论、定时任务、
// 告警、邮件……）共用的那部分。
//
// 一种接入点是一个 Kind（怎么认人、怎么组指令、结果怎么说）；一种出口是一个 Sink（怎么把一句话发出去）。外部的一条事件
// 翻译成一条 Trigger，带稳定的 ID：同一个 ID 只起一个任务。Engine 管各类型都一样的事：去重、限流与预算、起会话与 Task、
// 盯 Task 的状态、发件箱。状态：
//
//	accepted → running → [awaiting_confirmation → confirmed] → publishing → done
//	核对没过的是 refused；没完成或发布失败是 failed；等确认超时是 expired
//
// 要对外说的话不直接发，先记成一条 Delivery（发件箱）再发：幂等键是触发、阶段与出口，同一阶段只说一遍；发不出去按退避
// 重试，MaxDeliveryAttempts 次后进死信；发到一半进程没了的不重发，标成 unknown（宁可少说一遍，不说两遍）。投递的成败
// 不改变任务与触发的状态。多副本时一条触发同一时间只由一个副本推进（Claim 带租约）。
//
// 本包不依赖 coding-web：会话、Task 与工作区经 Host 做。日志只有触发的 ID、类型与状态，不带指令与外部内容。
package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"
)

// State 是一条触发走到哪一步。
type State string

const (
	Accepted             State = "accepted"
	Running              State = "running"
	AwaitingConfirmation State = "awaiting_confirmation"
	Confirmed            State = "confirmed"
	Publishing           State = "publishing"
	Done                 State = "done"
	Refused              State = "refused"
	Failed               State = "failed"
	Expired              State = "expired"
)

// Ended 说这个状态之后不再推进。
func (s State) Ended() bool { return s == Done || s == Refused || s == Failed || s == Expired }

// Phase 是对外说话的阶段；一条触发在一个出口上每个阶段只说一遍。
type Phase string

const (
	PhaseAccepted Phase = "accepted" // 已受理
	PhaseQuestion Phase = "question" // 要人确认
	PhaseResult   Phase = "result"   // 有结果
	PhaseFailed   Phase = "failed"   // 没做成（被拒、失败、超时）
)

// Trigger 是一条触发的记录。Origin 是类型自己的字段（哪个仓库、哪条评论、谁发的），框架不解读；Input 是指令与外部内容，
// Task 提交出去就清掉。
type Trigger struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Connection  string          `json:"connection,omitempty"`
	Scope       string          `json:"scope,omitempty"` // 限流与预算按它算
	Tenant      string          `json:"tenant,omitempty"`
	User        string          `json:"user,omitempty"`
	ProjectID   string          `json:"project_id,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	WorkspaceID string          `json:"workspace_id,omitempty"`
	TaskID      string          `json:"task_id,omitempty"`
	State       State           `json:"state"`
	Attempts    int             `json:"attempts,omitempty"`
	Origin      json.RawMessage `json:"origin,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	AskedAt     time.Time       `json:"asked_at,omitzero"`
}

// 投递的状态。
const (
	DeliveryPending = "pending"
	DeliverySending = "sending"
	DeliverySent    = "sent"
	DeliveryDead    = "dead"
	DeliveryUnknown = "unknown" // 发到一半进程没了：不知道对方收到没有，不重发
)

// Delivery 是发件箱里的一条。Interrupted 只在领取时有：上次发到一半没有结果。
type Delivery struct {
	Key         string    `json:"key"`
	Trigger     string    `json:"trigger"`
	Sink        string    `json:"sink"`
	Phase       Phase     `json:"phase"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	Attempts    int       `json:"attempts,omitempty"`
	NextAt      time.Time `json:"next_at"`
	CreatedAt   time.Time `json:"created_at"`
	SentAt      time.Time `json:"sent_at,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	Interrupted bool      `json:"-"`
}

// DeliveryKey 是一条投递的幂等键。
func DeliveryKey(trigger string, phase Phase, sink string) string {
	return trigger + "|" + string(phase) + "|" + sink
}

// Admission 是准入的结果：Silent 时不起任务、不留记录、也不回话（陌生人不给刷屏的机会）；Refusal 非空时不起任务，
// 把这句话说回去。
type Admission struct {
	Silent  bool
	Refusal string
}

// TaskInput 是交给 Task 的输入。ClientRequestID 由触发定，重试时 Runtime 按它去重。
type TaskInput struct {
	Message         string
	Title           string
	ClientRequestID string
}

// Limits 是一种接入点的平台上限。RunsPerHour、USDPerHour 按 Trigger.Scope 算，0 表示不限。
type Limits struct {
	RunsPerHour   int
	USDPerHour    float64
	ConfirmWithin time.Duration
	StartAttempts int
}

// 框架要类型来说的几种结果（Outcome.Code）。
const (
	Started        = "started"         // Task 提交出去了
	LimitRuns      = "limit_runs"      // 超了每小时的次数
	LimitSpend     = "limit_spend"     // 超了每小时的花费
	StartFailed    = "start_failed"    // 试了 StartAttempts 次起不来
	ProjectGone    = "project_gone"    // 触发对应的项目已经删了
	TaskEnded      = "task_ended"      // Task 结束了但不是 done（Status）
	Finished       = "finished"        // Task 做完了，这个类型没有后续要做的
	NoConfirmation = "no_confirmation" // 等确认超时
	Interrupted    = "interrupted"     // 发布到一半进程没了
	PublishFailed  = "publish_failed"  // 发布失败
)

// Outcome 是框架的一个结果，交给 Kind.Words 说成这个类型的话。
type Outcome struct {
	Code   string
	Runs   int
	Spent  float64
	Status string
}

// Kind 是一种接入点。
type Kind interface {
	Name() string
	// Sink 是默认的出口。
	Sink() string
	Limits() Limits
	// Admit 核对一次触发：认人、找项目与这个类型自己的条件；t 的 Tenant、User、ProjectID 就地补上。限流与预算由框架做。
	Admit(ctx context.Context, t *Trigger) (Admission, error)
	// Compose 把触发组成 Task 的输入：指令与外部内容分开，外部内容只当资料。
	Compose(t Trigger) (TaskInput, error)
	// Words 把框架的一个结果说成一句话；返回空就不说。
	Words(t Trigger, o Outcome) string
}

// 任务正常结束之后怎么办。
const (
	Nothing = "nothing" // 没有可发布的：结束，说一句
	Publish = "publish" // 直接发布
	Ask     = "ask"     // 先问人，等确认
)

// Decision 是 Finisher.Review 的结论；Text 是 Nothing 时的说明、Ask 时的问题。
type Decision struct {
	Action string
	Text   string
}

// Finisher 是任务正常结束后还有事要做的类型（推送、开 PR）。没实现它的类型直接说 Finished。
type Finisher interface {
	Review(ctx context.Context, t Trigger) (Decision, error)
	// Publish 只会被调用一次：调用之前触发已经记成 publishing，失败与中断都不重试。返回要说的结果。
	Publish(ctx context.Context, t Trigger) (string, error)
}

// Message 是交给出口的一句话。
type Message struct {
	Key   string
	Phase Phase
	Text  string
}

// Sink 是一种出口。
type Sink interface {
	Name() string
	Deliver(ctx context.Context, t Trigger, m Message) error
}

// TaskStatus 是 Host.Check 看到的 Task 的状态。Status 为空表示还不知道。
type TaskStatus struct {
	Status   string
	Terminal bool
	Done     bool
}

// Host 是框架要宿主（coding-web）做的事。都可能被重复调用。
type Host interface {
	// OpenSession 给触发建一个会话与它的工作区。
	OpenSession(ctx context.Context, t Trigger, in TaskInput) (sessionID, workspaceID string, err error)
	// Submit 把 Task 提交出去，返回 Task 的 ID。
	Submit(ctx context.Context, t Trigger, in TaskInput) (taskID string, err error)
	Check(ctx context.Context, t Trigger) (TaskStatus, error)
	// Spent 是这条触发的 Task 记下的花费（美元）。
	Spent(ctx context.Context, t Trigger) float64
}

// Stop 是 Host 返回的不必再试的错误：触发直接结束成 failed，按 Outcome 说明。
type Stop struct{ Outcome Outcome }

func (e *Stop) Error() string { return "接入点：不再重试" }

// Store 存触发与投递。实现：File（单进程，开发与测试）与 Postgres（多副本）。
type Store interface {
	// AddTrigger 登记一条触发；这个 ID 已经有记录时不动，返回已有的那条与 false。
	AddTrigger(ctx context.Context, t Trigger) (Trigger, bool, error)
	Trigger(ctx context.Context, id string) (Trigger, bool, error)
	// UpdateTrigger 原子地改一条记录；fn 返回 false 时不改。返回改后的值与改了没有。
	UpdateTrigger(ctx context.Context, id string, fn func(*Trigger) bool) (Trigger, bool, error)
	// OpenTriggers 是没结束的触发，按登记的先后。
	OpenTriggers(ctx context.Context) ([]Trigger, error)
	// InScope 是这个范围里 since 之后登记的触发。
	InScope(ctx context.Context, scope string, since time.Time) ([]Trigger, error)
	// Claim 领取一条触发到 until；别人领着（租约没到期）时返回 false。Release 交还。
	Claim(ctx context.Context, id string, now, until time.Time) (bool, error)
	Release(ctx context.Context, id string) error
	// AddDelivery 登记一条投递；这个键已经有记录时不动，返回 false。
	AddDelivery(ctx context.Context, d Delivery) (bool, error)
	// ClaimDeliveries 领取到时间的投递并记成 sending（租约到 until）：pending 且到了 NextAt 的，以及 sending 而租约
	// 过期的（Interrupted）。trigger 非空时只领这条触发的。
	ClaimDeliveries(ctx context.Context, trigger string, now, until time.Time, limit int) ([]Delivery, error)
	SaveDelivery(ctx context.Context, d Delivery) error
	Deliveries(ctx context.Context, trigger string) ([]Delivery, error)
}

// Metrics 是计数；标签只有类型、出口与结果。
type Metrics interface {
	Count(name string, n int64, labels map[string]string)
}

type noMetrics struct{}

func (noMetrics) Count(string, int64, map[string]string) {}

// Options 是 Engine 的构造参数；零值的字段用默认值。
type Options struct {
	Store Store
	Host  Host
	Now   func() time.Time
	// Lease 是推进一条触发的一步最多多久（默认 15 分钟：建会话与推送各自最多 10 分钟）。
	Lease time.Duration
	// DeliveryLease 是发一条投递最多多久（默认 2 分钟）；MaxDeliveryAttempts 默认 5。
	DeliveryLease       time.Duration
	MaxDeliveryAttempts int
	// Backoff 是第 n 次失败之后等多久（默认 30 秒 × 2^(n-1)，最多 30 分钟）。
	Backoff func(attempt int) time.Duration
	Metrics Metrics
}

// Engine 推进触发并发出投递。
type Engine struct {
	opts  Options
	kinds map[string]Kind
	sinks map[string]Sink
}

// New 建一个 Engine。
func New(opts Options) (*Engine, error) {
	if opts.Store == nil || opts.Host == nil {
		return nil, errors.New("接入点要 Store 与 Host")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Lease <= 0 {
		opts.Lease = 15 * time.Minute
	}
	if opts.DeliveryLease <= 0 {
		opts.DeliveryLease = 2 * time.Minute
	}
	if opts.MaxDeliveryAttempts <= 0 {
		opts.MaxDeliveryAttempts = 5
	}
	if opts.Backoff == nil {
		opts.Backoff = func(n int) time.Duration {
			d := 30 * time.Second << (n - 1)
			if d <= 0 || d > 30*time.Minute {
				return 30 * time.Minute
			}
			return d
		}
	}
	if opts.Metrics == nil {
		opts.Metrics = noMetrics{}
	}
	return &Engine{opts: opts, kinds: map[string]Kind{}, sinks: map[string]Sink{}}, nil
}

// Register 登记一种接入点；RegisterSink 登记一种出口。都要在收第一条触发之前调用。
func (e *Engine) Register(k Kind)     { e.kinds[k.Name()] = k }
func (e *Engine) RegisterSink(s Sink) { e.sinks[s.Name()] = s }

// Store 返回底下的存储（排查与测试用）。
func (e *Engine) Store() Store { return e.opts.Store }

func (e *Engine) now() time.Time { return e.opts.Now() }

func (e *Engine) count(name, kind, result string) {
	e.opts.Metrics.Count(name, 1, map[string]string{"kind": kind, "result": result})
}

// Receive 收一条触发：见过的不再登记（只把它没发出去的话再发一次）；准入没过的不起任务；过了的登记成 accepted，由
// Advance 往下推。t 要带 ID、Kind、Scope、Origin 与 Input。
func (e *Engine) Receive(ctx context.Context, t Trigger) error {
	k := e.kinds[t.Kind]
	if k == nil || t.ID == "" {
		return fmt.Errorf("接入点：没有登记的类型 %q 或触发没有 ID", t.Kind)
	}
	if _, seen, err := e.opts.Store.Trigger(ctx, t.ID); err != nil {
		return err
	} else if seen {
		e.count("coding.entry.triggers", t.Kind, "duplicate")
		return e.deliver(ctx, t.ID)
	}
	t.State, t.CreatedAt = Accepted, e.now().UTC()
	adm, err := k.Admit(ctx, &t)
	if err != nil {
		return err
	}
	if adm.Silent {
		e.count("coding.entry.triggers", t.Kind, "ignored")
		return nil
	}
	refusal := adm.Refusal
	if refusal == "" {
		if refusal, err = e.overLimit(ctx, k, t); err != nil {
			return err
		}
	}
	if refusal != "" {
		t.State, t.Input = Refused, nil
	}
	if _, added, err := e.opts.Store.AddTrigger(ctx, t); err != nil || !added {
		return err
	}
	if refusal == "" {
		e.count("coding.entry.triggers", t.Kind, "accepted")
		return nil
	}
	e.count("coding.entry.triggers", t.Kind, "refused")
	return e.say(ctx, t, PhaseFailed, refusal)
}

// overLimit 按 Scope 核对最近一小时的次数与花费；超了返回要说的话。
func (e *Engine) overLimit(ctx context.Context, k Kind, t Trigger) (string, error) {
	lim := k.Limits()
	if t.Scope == "" || lim.RunsPerHour <= 0 && lim.USDPerHour <= 0 {
		return "", nil
	}
	recent, err := e.opts.Store.InScope(ctx, t.Scope, t.CreatedAt.Add(-time.Hour))
	if err != nil {
		return "", err
	}
	runs, spent := 0, 0.0
	for _, x := range recent {
		if x.State == Refused || x.ID == t.ID {
			continue
		}
		runs++
		if x.TaskID != "" {
			spent += e.opts.Host.Spent(ctx, x)
		}
	}
	switch {
	case lim.RunsPerHour > 0 && runs >= lim.RunsPerHour:
		return k.Words(t, Outcome{Code: LimitRuns, Runs: runs, Spent: spent}), nil
	case lim.USDPerHour > 0 && spent >= lim.USDPerHour:
		return k.Words(t, Outcome{Code: LimitSpend, Runs: runs, Spent: spent}), nil
	}
	return "", nil
}

// Confirm 放行这个范围里等确认、match 认下的触发（调用方已经核对过确认的人）；过了时限的不放行。
func (e *Engine) Confirm(ctx context.Context, match func(Trigger) bool) error {
	open, err := e.opts.Store.OpenTriggers(ctx)
	if err != nil {
		return err
	}
	for _, t := range open {
		k := e.kinds[t.Kind]
		if k == nil || t.State != AwaitingConfirmation || !match(t) {
			continue
		}
		within := k.Limits().ConfirmWithin
		if _, _, err := e.opts.Store.UpdateTrigger(ctx, t.ID, func(x *Trigger) bool {
			if x.State != AwaitingConfirmation || within > 0 && e.now().Sub(x.AskedAt) > within {
				return false
			}
			x.State = Confirmed
			return true
		}); err != nil {
			return err
		}
	}
	return nil
}

// Advance 把每条没结束的触发往前推一步，再发到时间的投递。一条出错只记日志，下一轮再来。
func (e *Engine) Advance(ctx context.Context) error {
	open, err := e.opts.Store.OpenTriggers(ctx)
	if err != nil {
		return err
	}
	for _, t := range open {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := e.now()
		got, err := e.opts.Store.Claim(ctx, t.ID, now, now.Add(e.opts.Lease))
		if err != nil || !got {
			continue
		}
		// 领到之后重读：别的副本可能刚推进过它。
		cur, ok, err := e.opts.Store.Trigger(ctx, t.ID)
		if err == nil && ok && !cur.State.Ended() {
			err = e.step(ctx, cur)
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("接入点：触发 %s（%s）推进失败：%v", t.ID, cur.State, err)
		}
		_ = e.opts.Store.Release(context.WithoutCancel(ctx), t.ID)
	}
	return e.deliver(ctx, "")
}

func (e *Engine) step(ctx context.Context, t Trigger) error {
	k := e.kinds[t.Kind]
	if k == nil {
		return fmt.Errorf("没有登记的类型 %q", t.Kind)
	}
	switch t.State {
	case Accepted:
		return e.start(ctx, k, t)
	case Running:
		return e.check(ctx, k, t)
	case Confirmed:
		return e.publish(ctx, k, t)
	case AwaitingConfirmation:
		if within := k.Limits().ConfirmWithin; within > 0 && e.now().Sub(t.AskedAt) > within {
			return e.end(ctx, t, Expired, k.Words(t, Outcome{Code: NoConfirmation}))
		}
	case Publishing:
		// 上次发布到一半进程没了：不重做（一条触发只授权一次发布）。
		return e.end(ctx, t, Failed, k.Words(t, Outcome{Code: Interrupted}))
	}
	return nil
}

// start 建会话、提交 Task。会话建好就记下，提交失败时下一轮不重建。
func (e *Engine) start(ctx context.Context, k Kind, t Trigger) error {
	fail := func(cause error) error {
		var stop *Stop
		if errors.As(cause, &stop) {
			return e.end(ctx, t, Failed, k.Words(t, stop.Outcome))
		}
		x, _, err := e.opts.Store.UpdateTrigger(ctx, t.ID, func(x *Trigger) bool { x.Attempts++; return true })
		if max := k.Limits().StartAttempts; err == nil && max > 0 && x.Attempts >= max {
			log.Printf("接入点：触发 %s 开始不了，放弃：%v", t.ID, cause)
			return e.end(ctx, t, Failed, k.Words(t, Outcome{Code: StartFailed}))
		}
		return cause
	}
	in, err := k.Compose(t)
	if err != nil {
		return fail(err)
	}
	if t.SessionID == "" {
		sid, wid, err := e.opts.Host.OpenSession(ctx, t, in)
		if err != nil {
			return fail(err)
		}
		if t, _, err = e.opts.Store.UpdateTrigger(ctx, t.ID, func(x *Trigger) bool { x.SessionID, x.WorkspaceID = sid, wid; return true }); err != nil {
			return err
		}
	}
	taskID, err := e.opts.Host.Submit(ctx, t, in)
	if err != nil {
		return fail(err)
	}
	t, moved, err := e.opts.Store.UpdateTrigger(ctx, t.ID, func(x *Trigger) bool {
		if x.State != Accepted {
			return false
		}
		x.TaskID, x.State, x.Input = taskID, Running, nil
		return true
	})
	if err != nil || !moved {
		return err
	}
	return e.say(ctx, t, PhaseAccepted, k.Words(t, Outcome{Code: Started}))
}

// check 看 Task 结束了没有：没完成的说明状态；做完的按类型的 Review 结束、发布或请求确认。
func (e *Engine) check(ctx context.Context, k Kind, t Trigger) error {
	st, err := e.opts.Host.Check(ctx, t)
	if err != nil || !st.Terminal {
		return err
	}
	if !st.Done {
		return e.end(ctx, t, Failed, k.Words(t, Outcome{Code: TaskEnded, Status: st.Status}))
	}
	fin, ok := k.(Finisher)
	if !ok {
		return e.end(ctx, t, Done, k.Words(t, Outcome{Code: Finished}))
	}
	d, err := fin.Review(ctx, t)
	if err != nil {
		return err
	}
	switch d.Action {
	case Publish:
		return e.publish(ctx, k, t)
	case Ask:
		t, moved, err := e.opts.Store.UpdateTrigger(ctx, t.ID, func(x *Trigger) bool {
			if x.State != Running {
				return false
			}
			x.State, x.AskedAt = AwaitingConfirmation, e.now().UTC()
			return true
		})
		if err != nil || !moved {
			return err
		}
		return e.say(ctx, t, PhaseQuestion, d.Text)
	}
	return e.end(ctx, t, Done, d.Text)
}

// publish 先把记录设成 publishing 再动手：一条触发只发布一次，失败也不重试。
func (e *Engine) publish(ctx context.Context, k Kind, t Trigger) error {
	fin, ok := k.(Finisher)
	if !ok {
		return e.end(ctx, t, Done, k.Words(t, Outcome{Code: Finished}))
	}
	t, claimed, err := e.opts.Store.UpdateTrigger(ctx, t.ID, func(x *Trigger) bool {
		if x.State != Running && x.State != Confirmed {
			return false
		}
		x.State = Publishing
		return true
	})
	if err != nil || !claimed {
		return err
	}
	text, err := fin.Publish(ctx, t)
	if err != nil {
		log.Printf("接入点：触发 %s 发布失败，不重试：%v", t.ID, err)
		return e.end(ctx, t, Failed, k.Words(t, Outcome{Code: PublishFailed}))
	}
	return e.end(ctx, t, Done, text)
}

// end 把触发设成结束状态并说明。
func (e *Engine) end(ctx context.Context, t Trigger, state State, text string) error {
	t, _, err := e.opts.Store.UpdateTrigger(ctx, t.ID, func(x *Trigger) bool { x.State, x.Input = state, nil; return true })
	if err != nil {
		return err
	}
	e.count("coding.entry.ended", t.Kind, string(state))
	phase := PhaseFailed
	if state == Done {
		phase = PhaseResult
	}
	return e.say(ctx, t, phase, text)
}

// say 把一句话记进发件箱并马上试着发一次。发不出去不算错：发件箱会重试。
func (e *Engine) say(ctx context.Context, t Trigger, phase Phase, text string) error {
	k := e.kinds[t.Kind]
	if text == "" || k == nil || k.Sink() == "" {
		return nil
	}
	now := e.now().UTC()
	d := Delivery{Key: DeliveryKey(t.ID, phase, k.Sink()), Trigger: t.ID, Sink: k.Sink(), Phase: phase, Body: text, State: DeliveryPending, NextAt: now, CreatedAt: now}
	if _, err := e.opts.Store.AddDelivery(ctx, d); err != nil {
		return err
	}
	return e.deliver(ctx, t.ID)
}

// deliver 发到时间的投递；trigger 非空时只发这条触发的。
func (e *Engine) deliver(ctx context.Context, trigger string) error {
	now := e.now()
	due, err := e.opts.Store.ClaimDeliveries(ctx, trigger, now, now.Add(e.opts.DeliveryLease), 50)
	if err != nil {
		return err
	}
	for _, d := range due {
		e.send(ctx, d)
	}
	return nil
}

func (e *Engine) send(ctx context.Context, d Delivery) {
	result := func(state string) {
		d.State = state
		if err := e.opts.Store.SaveDelivery(context.WithoutCancel(ctx), d); err != nil {
			log.Printf("接入点：投递 %s 的结果没记下：%v", d.Key, err)
		}
		e.opts.Metrics.Count("coding.entry.deliveries", 1, map[string]string{"sink": d.Sink, "result": state})
	}
	if d.Interrupted {
		log.Printf("接入点：投递 %s 上次发到一半没有结果，不重发", d.Key)
		result(DeliveryUnknown)
		return
	}
	sink := e.sinks[d.Sink]
	t, ok, err := e.opts.Store.Trigger(ctx, d.Trigger)
	if err == nil && (sink == nil || !ok) {
		err = fmt.Errorf("没有出口 %q 或触发已经不在", d.Sink)
	}
	if err == nil {
		err = sink.Deliver(ctx, t, Message{Key: d.Key, Phase: d.Phase, Text: d.Body})
	}
	if err == nil {
		d.SentAt, d.LastError = e.now().UTC(), ""
		result(DeliverySent)
		return
	}
	d.Attempts++
	d.LastError = clip(err.Error(), 200)
	if d.Attempts >= e.opts.MaxDeliveryAttempts {
		log.Printf("接入点：投递 %s 试了 %d 次发不出去，进死信：%v", d.Key, d.Attempts, err)
		result(DeliveryDead)
		return
	}
	d.NextAt = e.now().Add(e.opts.Backoff(d.Attempts)).UTC()
	result(DeliveryPending)
}

// Redeliver 把一条死信或结果未知的投递放回发件箱（人看过之后手动重发）。
func (e *Engine) Redeliver(ctx context.Context, trigger, key string) error {
	ds, err := e.opts.Store.Deliveries(ctx, trigger)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if d.Key != key || d.State != DeliveryDead && d.State != DeliveryUnknown {
			continue
		}
		d.State, d.Attempts, d.NextAt, d.LastError = DeliveryPending, 0, e.now().UTC(), ""
		if err := e.opts.Store.SaveDelivery(ctx, d); err != nil {
			return err
		}
		return e.deliver(ctx, trigger)
	}
	return errors.New("没有这条可以重发的投递")
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
