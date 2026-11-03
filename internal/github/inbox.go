// Package github 是 GitHub App 的 Webhook 收件箱与对账（G-02，docs/plan/github-app.md 第 3 节“收件箱”、第 4 节 G-02）。
//
// 接收：边读边限长（上限 MaxBody，GitHub 不投 25 MiB 以上的）→ 验 X-Hub-Signature-256（签名不对不解析 JSON）→ 核对目标 App ID
// → 按 delivery 入箱（INSERT … ON CONFLICT：已有的不动，dead 的改回 received）→ 202。处理在后台：FOR UPDATE SKIP LOCKED 领取，
// processing 带租约（进程崩溃时到期后别人重新领取），失败按退避重试，MaxAttempts 次后进死信；多副本也不会重复处理。
// GitHub 不自动重投、只保留 3 天，所以对账（Reconcile）在启动时与每 10 分钟拉一次 GET /app/hook/deliveries，把 3 天内失败、
// 自己又没收到的按 id 重投，每轮有上限，同一条只重投一次。负载留 Retention（7 天）后清空，行留着做去重与统计。
// 库连不上时回 503 并计数（GitHub 不自动重投，靠对账补）。日志只有 delivery、event、action 与 installation id。
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// App 是验签与签 App 的 JWT 的能力（server.GitHubApp 实现它）。
type App interface {
	VerifyWebhook(body []byte, header string) bool
	AppJWT(now time.Time) (string, error)
}

// Delivery 是收件箱里的一条投递。
type Delivery struct {
	ID             string
	Event          string
	Action         string
	InstallationID int64
	Payload        []byte
	Attempts       int
}

// Handler 处理一类事件；要能重复执行（同一条可能因租约到期被处理两次）。返回错误时按退避重试。
type Handler func(ctx context.Context, d Delivery) error

// Options 是构造参数；零值的字段用默认值。
type Options struct {
	DSN      string
	App      App
	AppID    string
	APIURL   string // 默认 https://api.github.com
	Handlers map[string]Handler
	Now      func() time.Time
	// MaxBody 默认 25 MiB；MaxAttempts 默认 5；Lease 默认 2 分钟；Retention 默认 7 天；HandlerTimeout 默认 1 分钟。
	MaxBody        int64
	MaxAttempts    int
	Lease          time.Duration
	Retention      time.Duration
	HandlerTimeout time.Duration
	// Backoff 是第 n 次失败之后等多久（默认 30 秒 × 2^(n-1)，最多 30 分钟）。
	Backoff func(attempt int) time.Duration
	// ReconcileWindow 默认 72 小时（GitHub 保留投递记录的时长）；ReconcileLimit 是每轮最多重投几条，默认 50。
	ReconcileWindow time.Duration
	ReconcileLimit  int
	HTTP            *http.Client
}

// Stats 是计数（G-05 接进 Telemetry）。
type Stats struct {
	Received, SignatureFailures, Unavailable, Redelivered int64
}

// Status 是一条投递现在的状态（排查与测试用）。
type Status struct {
	State      string
	Attempts   int
	HasPayload bool
}

// Service 是收件箱：http.Handler 是接收端，Run 是后台的处理、对账与清理。
type Service struct {
	db   *pgxpool.Pool
	opts Options
	wake chan struct{}

	received, sigFailures, unavailable, redelivered atomic.Int64
}

// New 连上库并建表。
func New(ctx context.Context, opts Options) (*Service, error) {
	if opts.App == nil || opts.AppID == "" || opts.DSN == "" {
		return nil, errors.New("收件箱要 App、App ID 与 CODING_GITHUB_DSN")
	}
	if opts.APIURL == "" {
		opts.APIURL = "https://api.github.com"
	}
	opts.APIURL = strings.TrimRight(opts.APIURL, "/")
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxBody <= 0 {
		opts.MaxBody = 25 << 20
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 5
	}
	if opts.Lease <= 0 {
		opts.Lease = 2 * time.Minute
	}
	if opts.Retention <= 0 {
		opts.Retention = 7 * 24 * time.Hour
	}
	if opts.HandlerTimeout <= 0 {
		opts.HandlerTimeout = time.Minute
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
	if opts.ReconcileWindow <= 0 {
		opts.ReconcileWindow = 72 * time.Hour
	}
	if opts.ReconcileLimit <= 0 {
		opts.ReconcileLimit = 50
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	pool, err := pgxpool.New(ctx, opts.DSN)
	if err != nil {
		// 不带原始错误：连接串里可能有口令。
		return nil, errors.New("CODING_GITHUB_DSN 不是合格的 Postgres 连接串")
	}
	s := &Service{db: pool, opts: opts, wake: make(chan struct{}, 1)}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Service) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// 几个副本同时启动时只有一个在建表。
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('coding_web_github_migrate'))`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS github_deliveries (
  delivery        TEXT PRIMARY KEY,
  event           TEXT NOT NULL,
  action          TEXT NOT NULL DEFAULT '',
  installation_id BIGINT NOT NULL DEFAULT 0,
  payload         BYTEA,
  state           TEXT NOT NULL,
  attempts        INT NOT NULL DEFAULT 0,
  next_at         TIMESTAMPTZ NOT NULL,
  lease_until     TIMESTAMPTZ,
  received_at     TIMESTAMPTZ NOT NULL,
  last_error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS github_deliveries_due ON github_deliveries (state, next_at);
CREATE TABLE IF NOT EXISTS github_redeliveries (
  guid             TEXT PRIMARY KEY,
  hook_delivery_id BIGINT NOT NULL,
  redelivered_at   TIMESTAMPTZ NOT NULL
);`)
		return err
	})
}

// Close 关掉连接池。
func (s *Service) Close() { s.db.Close() }

// Stats 返回计数。
func (s *Service) Stats() Stats {
	return Stats{Received: s.received.Load(), SignatureFailures: s.sigFailures.Load(), Unavailable: s.unavailable.Load(), Redelivered: s.redelivered.Load()}
}

// --- 接收 ---

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.ContentLength > s.opts.MaxBody {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.opts.MaxBody+1))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if int64(len(body)) > s.opts.MaxBody {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if !s.opts.App.VerifyWebhook(body, r.Header.Get("X-Hub-Signature-256")) {
		s.sigFailures.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-GitHub-Hook-Installation-Target-ID") != s.opts.AppID {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	d := Delivery{ID: r.Header.Get("X-GitHub-Delivery"), Event: r.Header.Get("X-GitHub-Event"), Payload: body}
	var meta struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if d.ID == "" || d.Event == "" || len(d.ID) > 200 || json.Unmarshal(body, &meta) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	d.Action, d.InstallationID = meta.Action, meta.Installation.ID
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.insert(ctx, d); err != nil {
		s.unavailable.Add(1)
		log.Printf("GitHub 投递 %s（%s）入箱失败，回 503：%v", d.ID, d.Event, err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	s.received.Add(1)
	log.Printf("GitHub 投递 %s：%s %s，安装 %d", d.ID, d.Event, d.Action, d.InstallationID)
	w.WriteHeader(http.StatusAccepted)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) insert(ctx context.Context, d Delivery) error {
	_, err := s.db.Exec(ctx, `
INSERT INTO github_deliveries (delivery, event, action, installation_id, payload, state, attempts, next_at, received_at)
VALUES ($1, $2, $3, $4, $5, 'received', 0, $6, $6)
ON CONFLICT (delivery) DO UPDATE
  SET state = 'received', attempts = 0, next_at = EXCLUDED.next_at, payload = EXCLUDED.payload, lease_until = NULL, last_error = ''
  WHERE github_deliveries.state = 'dead'`,
		d.ID, d.Event, d.Action, d.InstallationID, d.Payload, s.opts.Now())
	return err
}

// --- 处理 ---

// claim 领走到期的投递（含租约到期的 processing），标成 processing 并带上新租约。
func (s *Service) claim(ctx context.Context, limit int) ([]Delivery, error) {
	now := s.opts.Now()
	rows, err := s.db.Query(ctx, `
UPDATE github_deliveries SET state = 'processing', attempts = attempts + 1, lease_until = $2
WHERE delivery IN (
  SELECT delivery FROM github_deliveries
  WHERE (state IN ('received', 'retry') AND next_at <= $1) OR (state = 'processing' AND lease_until < $1)
  ORDER BY next_at LIMIT $3
  FOR UPDATE SKIP LOCKED)
RETURNING delivery, event, action, installation_id, COALESCE(payload, ''::bytea), attempts`,
		now, now.Add(s.opts.Lease), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Delivery, error) {
		var d Delivery
		err := row.Scan(&d.ID, &d.Event, &d.Action, &d.InstallationID, &d.Payload, &d.Attempts)
		return d, err
	})
}

// ProcessOnce 领一批到期的投递并处理，返回处理了几条。
func (s *Service) ProcessOnce(ctx context.Context) (int, error) {
	batch, err := s.claim(ctx, 10)
	if err != nil {
		return 0, err
	}
	for _, d := range batch {
		var herr error
		if h := s.opts.Handlers[d.Event]; h != nil {
			hctx, cancel := context.WithTimeout(ctx, s.opts.HandlerTimeout)
			herr = h(hctx, d)
			cancel()
		}
		if herr == nil {
			_, err = s.db.Exec(ctx, `UPDATE github_deliveries SET state = 'done', lease_until = NULL, last_error = '' WHERE delivery = $1 AND state = 'processing'`, d.ID)
		} else {
			state, next := "retry", s.opts.Now().Add(s.opts.Backoff(d.Attempts))
			if d.Attempts >= s.opts.MaxAttempts {
				state = "dead"
				log.Printf("GitHub 投递 %s（%s）失败 %d 次，进死信", d.ID, d.Event, d.Attempts)
			}
			reason := herr.Error()
			if len(reason) > 300 {
				reason = reason[:300]
			}
			_, err = s.db.Exec(ctx, `UPDATE github_deliveries SET state = $2, next_at = $3, lease_until = NULL, last_error = $4 WHERE delivery = $1 AND state = 'processing'`, d.ID, state, next, reason)
		}
		if err != nil {
			return 0, err
		}
	}
	return len(batch), nil
}

// Replay 把一条投递重新入队（coding-web github-replay）；没有这条时报错。
func (s *Service) Replay(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE github_deliveries SET state = 'received', attempts = 0, next_at = $2, lease_until = NULL, last_error = '' WHERE delivery = $1`, id, s.opts.Now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("收件箱里没有投递 %s", id)
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// Get 返回一条投递的状态。
func (s *Service) Get(ctx context.Context, id string) (Status, error) {
	var st Status
	err := s.db.QueryRow(ctx, `SELECT state, attempts, payload IS NOT NULL FROM github_deliveries WHERE delivery = $1`, id).Scan(&st.State, &st.Attempts, &st.HasPayload)
	return st, err
}

func (s *Service) count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM github_deliveries`).Scan(&n)
	return n, err
}

// Prune 清空 Retention 之前收到的负载（行留着做去重与统计），返回清了几条。
func (s *Service) Prune(ctx context.Context) (int, error) {
	tag, err := s.db.Exec(ctx, `UPDATE github_deliveries SET payload = NULL WHERE payload IS NOT NULL AND received_at < $1`, s.opts.Now().Add(-s.opts.Retention))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// --- 对账 ---

type hookDelivery struct {
	ID          int64     `json:"id"`
	GUID        string    `json:"guid"`
	DeliveredAt time.Time `json:"delivered_at"`
	StatusCode  int       `json:"status_code"`
}

func (s *Service) appRequest(ctx context.Context, method, url string) (*http.Response, error) {
	token, err := s.opts.App.AppJWT(s.opts.Now())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return s.opts.HTTP.Do(req)
}

// Reconcile 把 GitHub 那边 ReconcileWindow 之内投递失败、自己又没收到的按 id 重投（每轮最多 ReconcileLimit 条，
// 同一个 guid 只重投一次），返回这一轮重投了几条。
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	since := s.opts.Now().Add(-s.opts.ReconcileWindow)
	var all []hookDelivery
	next := s.opts.APIURL + "/app/hook/deliveries?per_page=100"
	for page := 0; next != "" && page < 20; page++ {
		res, err := s.appRequest(ctx, http.MethodGet, next)
		if err != nil {
			return 0, err
		}
		var items []hookDelivery
		err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&items)
		res.Body.Close()
		if res.StatusCode != http.StatusOK || err != nil {
			return 0, fmt.Errorf("GET /app/hook/deliveries：HTTP %d", res.StatusCode)
		}
		all = append(all, items...)
		next = nextLink(res.Header.Get("Link"))
		if len(items) == 0 || items[len(items)-1].DeliveredAt.Before(since) {
			break
		}
	}
	ok := map[string]bool{}
	for _, d := range all {
		if d.StatusCode >= 200 && d.StatusCode < 300 {
			ok[d.GUID] = true
		}
	}
	done, seen := 0, map[string]bool{}
	for _, d := range all {
		if done >= s.opts.ReconcileLimit {
			break
		}
		if d.GUID == "" || seen[d.GUID] || ok[d.GUID] || d.DeliveredAt.Before(since) {
			continue
		}
		seen[d.GUID] = true
		var known bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM github_deliveries WHERE delivery = $1) OR EXISTS (SELECT 1 FROM github_redeliveries WHERE guid = $1)`, d.GUID).Scan(&known); err != nil {
			return done, err
		}
		if known {
			continue
		}
		res, err := s.appRequest(ctx, http.MethodPost, fmt.Sprintf("%s/app/hook/deliveries/%d/attempts", s.opts.APIURL, d.ID))
		if err != nil {
			return done, err
		}
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			log.Printf("重投 GitHub 投递 %s 失败：HTTP %d", d.GUID, res.StatusCode)
			continue
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO github_redeliveries (guid, hook_delivery_id, redelivered_at) VALUES ($1, $2, $3) ON CONFLICT (guid) DO NOTHING`, d.GUID, d.ID, s.opts.Now()); err != nil {
			return done, err
		}
		s.redelivered.Add(1)
		done++
	}
	return done, nil
}

// nextLink 取 Link 头里 rel="next" 的地址（GitHub 的游标分页）。
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		url, rel, ok := strings.Cut(part, ";")
		if ok && strings.Contains(rel, `rel="next"`) {
			return strings.Trim(strings.TrimSpace(url), "<>")
		}
	}
	return ""
}

// --- 后台 ---

// Run 是后台循环：有新投递或每 5 秒处理一次；启动时与每 10 分钟对账；每小时清一次过期的负载。ctx 结束时返回。
func (s *Service) Run(ctx context.Context) {
	reconcile := func() {
		if n, err := s.Reconcile(ctx); err != nil {
			log.Printf("GitHub 投递对账失败：%v", err)
		} else if n > 0 {
			log.Printf("GitHub 投递对账：重投 %d 条", n)
		}
	}
	reconcile()
	poll, recon, prune := time.NewTicker(5*time.Second), time.NewTicker(10*time.Minute), time.NewTicker(time.Hour)
	defer poll.Stop()
	defer recon.Stop()
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-recon.C:
			reconcile()
			continue
		case <-prune.C:
			if _, err := s.Prune(ctx); err != nil {
				log.Printf("清 GitHub 投递的负载失败：%v", err)
			}
			continue
		case <-s.wake:
		case <-poll.C:
		}
		for ctx.Err() == nil {
			n, err := s.ProcessOnce(ctx)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("处理 GitHub 投递失败：%v", err)
				}
				break
			}
			if n == 0 {
				break
			}
		}
	}
}
