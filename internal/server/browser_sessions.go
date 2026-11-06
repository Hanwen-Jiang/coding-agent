package server

// 浏览器会话（AUTH-02，Runtime 仓库的 docs/plan/auth-tokens-2026-11-03.md 第 2 节）：Cookie 里只放不透明的会话 id
// （32 字节随机数的 base64url），服务端按 id 的 sha256 记（租户、用户、建立时间、最后活动）。每个请求按 id 查记录：
// 空闲超过 SessionIdle 或建立超过 SessionMaxAge 的作废。退出登录、“退出所有设备”都是删记录，立即生效。
// 调 Runtime 时按会话现签一个短期令牌（defaultRuntimeTokenTTL），在进程里缓存到快过期，浏览器拿不到 Runtime 令牌。
// 记录放在 <数据目录>/sessions.json（单实例；coding-web 改用 Postgres 时一起搬）。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"path/filepath"
	"sync"
	"time"
)

const (
	defaultSessionIdle     = 24 * time.Hour
	defaultSessionMaxAge   = 7 * 24 * time.Hour
	defaultRuntimeTokenTTL = 5 * time.Minute
	// 最后活动的时间按这个粒度写回，不是每个请求都写文件。
	lastSeenGranularity = time.Minute
	// 缓存的 Runtime 令牌剩下不到这么久就换新的：请求在路上、Runtime 的时钟稍快都不至于过期。
	runtimeTokenMargin = time.Minute
)

type browserSession struct {
	Tenant   string    `json:"tenant"`
	User     string    `json:"user"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
}

type cachedToken struct {
	token   string
	expires time.Time
}

type sessions struct {
	mu     sync.Mutex
	path   string
	idle   time.Duration
	maxAge time.Duration
	now    func() time.Time
	byHash map[string]browserSession // 惰性读入
	tokens map[string]cachedToken    // 会话哈希 → 现签的 Runtime 令牌（只在内存里）
}

func newSessions(cfg Config) *sessions {
	s := &sessions{path: filepath.Join(cfg.DataDir, "sessions.json"), idle: cfg.SessionIdle, maxAge: cfg.SessionMaxAge, now: cfg.Now, tokens: map[string]cachedToken{}}
	if s.idle <= 0 {
		s.idle = defaultSessionIdle
	}
	if s.maxAge <= 0 {
		s.maxAge = defaultSessionMaxAge
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

func sessionHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// load 读入记录；调用方持有 s.mu。
func (s *sessions) load() error {
	if s.byHash != nil {
		return nil
	}
	m, err := readJSONFile[map[string]browserSession](s.path)
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]browserSession{}
	}
	s.byHash = m
	return nil
}

func (s *sessions) expired(b browserSession, now time.Time) bool {
	return now.Sub(b.LastSeen) > s.idle || now.Sub(b.Created) > s.maxAge
}

// create 建一个会话，返回放进 Cookie 的 id；顺手清掉作废的记录。
func (s *sessions) create(tenant, user string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return "", err
	}
	now := s.now()
	for h, b := range s.byHash {
		if s.expired(b, now) {
			delete(s.byHash, h)
			delete(s.tokens, h)
		}
	}
	s.byHash[sessionHash(id)] = browserSession{Tenant: tenant, User: user, Created: now, LastSeen: now}
	return id, writeFileAtomic(s.path, s.byHash)
}

// lookup 按 id 找会话；作废的删掉并返回 false。有活动就顺延最后活动的时间。
func (s *sessions) lookup(id string) (browserSession, bool, error) {
	h := sessionHash(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return browserSession{}, false, err
	}
	b, ok := s.byHash[h]
	if !ok {
		return browserSession{}, false, nil
	}
	now := s.now()
	if s.expired(b, now) {
		delete(s.byHash, h)
		delete(s.tokens, h)
		return browserSession{}, false, writeFileAtomic(s.path, s.byHash)
	}
	if now.Sub(b.LastSeen) >= lastSeenGranularity {
		b.LastSeen = now
		s.byHash[h] = b
		if err := writeFileAtomic(s.path, s.byHash); err != nil {
			return browserSession{}, false, err
		}
	}
	return b, true, nil
}

// expiresAt 是会话最晚什么时候作废（空闲与绝对有效期取早的）。
func (s *sessions) expiresAt(b browserSession) time.Time {
	idle, abs := b.LastSeen.Add(s.idle), b.Created.Add(s.maxAge)
	if idle.Before(abs) {
		return idle
	}
	return abs
}

// revoke 删掉一个会话。
func (s *sessions) revoke(id string) error {
	h := sessionHash(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	if _, ok := s.byHash[h]; !ok {
		return nil
	}
	delete(s.byHash, h)
	delete(s.tokens, h)
	return writeFileAtomic(s.path, s.byHash)
}

// revokeUser 删掉一个用户的全部会话（退出所有设备、停用账号），返回删了几个。
func (s *sessions) revokeUser(tenant, user string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return 0, err
	}
	n := 0
	for h, b := range s.byHash {
		if b.Tenant == tenant && b.User == user {
			delete(s.byHash, h)
			delete(s.tokens, h)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, writeFileAtomic(s.path, s.byHash)
}

// runtimeToken 是这个会话调 Runtime 用的短期令牌：缓存的还够用就复用，否则用 issue 现签一个。
func (s *sessions) runtimeToken(id string, b browserSession, issue func(tenant, user string, ttl time.Duration) string) string {
	h := sessionHash(id)
	now := s.now()
	s.mu.Lock()
	cached, ok := s.tokens[h]
	s.mu.Unlock()
	if ok && cached.expires.Sub(now) > runtimeTokenMargin {
		return cached.token
	}
	token := issue(b.Tenant, b.User, defaultRuntimeTokenTTL)
	s.mu.Lock()
	if _, live := s.byHash[h]; live {
		s.tokens[h] = cachedToken{token: token, expires: now.Add(defaultRuntimeTokenTTL)}
	}
	s.mu.Unlock()
	return token
}

// Background polling is not browser activity and must not extend idle expiry.
func (s *sessions) lookupWithoutActivity(id string) (browserSession, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return browserSession{}, false, err
	}
	session, ok := s.byHash[sessionHash(id)]
	return session, ok && !s.expired(session, s.now()), nil
}
