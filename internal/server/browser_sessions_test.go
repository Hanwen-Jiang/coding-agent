package server

// AUTH-02（Runtime 仓库的 docs/plan/auth-tokens-2026-11-03.md 第 2 节）：浏览器只拿可撤销的会话 id。
//
//  1. 登录后 Cookie 里是不透明的会话 id（不是令牌）；服务端只存它的哈希。
//  2. 调 Runtime 时按会话现签短期令牌（5 分钟），快过期前复用、过期就换；JWT 模式下每个令牌带不同的 jti。
//  3. 退出登录删掉会话记录，立即生效：同一个 Cookie 再来是 401。“退出所有设备”删掉这个用户的全部会话，别的用户不受影响。
//  4. 空闲超过 SessionIdle 作废（有活动就顺延）；建立超过 SessionMaxAge 作废（有活动也一样）。
//  5. 过渡：有效的旧令牌 Cookie 换成会话（重设 Cookie），无效的照样 401。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/dev/localingress"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
)

type fakeNow struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeNow) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeNow) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func cookieOf(t *testing.T, c *client, base string) string {
	t.Helper()
	u, _ := url.Parse(base)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == cookieName {
			return ck.Value
		}
	}
	return ""
}

// replay 带着给定的 Cookie 值发一个 GET，返回状态码。
func replay(t *testing.T, base, cookie, path string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+path, nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// claimOf 取 JWT 载荷里的一个字符串字段（只给测试看，不验签）。
func claimOf(t *testing.T, token, name string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("不是 JWT：%q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	v, _ := claims[name].(string)
	return v
}

func login(t *testing.T, c *client, tenant, user string) {
	t.Helper()
	if code, _, raw := c.do("POST", "/api/login", map[string]string{"tenant": tenant, "user": user}); code != http.StatusOK {
		t.Fatalf("登录失败：%d %s", code, raw)
	}
}

func TestCookieHoldsAnOpaqueSessionIDAndOnlyItsHashIsStored(t *testing.T) {
	ts, _, cfg, _ := newServerWith(t, nil)
	c := newClient(t, ts.URL)
	login(t, c, "acme", "alice")
	id := cookieOf(t, c, ts.URL)
	if len(id) < 40 || strings.Contains(id, ".") {
		t.Fatalf("Cookie 是不透明的会话 id，不是令牌：%q", id)
	}
	dev := localingress.New(localingress.Options{SigningKey: cfg.SigningKey})
	if _, err := dev.Authenticate(context.Background(), ingress.Credentials{BearerToken: id}); err == nil {
		t.Fatal("会话 id 本身不能当 Runtime 令牌用")
	}
	raw, err := os.ReadFile(filepath.Join(cfg.DataDir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), id) || !strings.Contains(string(raw), `"alice"`) {
		t.Fatalf("会话记录里只有 id 的哈希：%s", raw)
	}
	if code, me, _ := c.do("GET", "/api/me", nil); code != http.StatusOK || me["tenant"] != "acme" {
		t.Fatalf("会话认得出用户：%d %v", code, me)
	}
}

func TestRuntimeCallsUseShortLivedTokensThatAreReusedUntilNearExpiry(t *testing.T) {
	clock := &fakeNow{t: time.Now()}
	// 用 JWT：每个令牌带不同的 jti，换没换一眼看得出（开发令牌在同一秒里签出来是一样的）。
	key := newEd25519(t)
	ts, fake, cfg, _ := newServerWith(t, func(c *Config) {
		c.Now, c.SigningKey, c.JWTKey, c.JWTKeyID, c.TokenKey = clock.now, nil, key, "k1", tokenKey32(3)
	})
	c := newClient(t, ts.URL)
	login(t, c, "acme", "alice")
	pid := sampleProject(t, c)
	api, _ := newSession(t, c, cfg, "acme", pid)
	submit := func() string {
		t.Helper()
		before := len(fake.tokens)
		if code, _, raw := c.do("POST", api+"/tasks", map[string]string{"message": "hi"}); code != http.StatusCreated {
			t.Fatalf("提交任务失败：%d %s", code, raw)
		}
		return strings.TrimPrefix(fake.tokens[before], "Bearer ")
	}
	first := submit()
	id, err := runtimeSide(t, key, "k1").Authenticate(context.Background(), ingress.Credentials{BearerToken: first})
	if err != nil || id.TenantID != "acme" || id.UserID != "alice" {
		t.Fatalf("转给 Runtime 的是按会话现签的令牌：%+v %v", id, err)
	}
	if left := time.Until(id.ExpiresAt); left > defaultRuntimeTokenTTL+time.Minute {
		t.Fatalf("短期令牌（%s 内过期），实际还有 %s", defaultRuntimeTokenTTL, left)
	}
	clock.advance(time.Minute)
	if again := submit(); again != first {
		t.Fatal("快过期之前复用同一个令牌")
	}
	clock.advance(defaultRuntimeTokenTTL)
	if renewed := submit(); renewed == first {
		t.Fatal("过期之后换新的令牌")
	}
}

func TestJWTRuntimeTokensCarryDistinctIDs(t *testing.T) {
	key := newEd25519(t)
	_, _, _, srv := newServerWith(t, func(c *Config) {
		c.SigningKey, c.JWTKey, c.JWTKeyID, c.TokenKey = nil, key, "k1", tokenKey32(7)
	})
	runtime := runtimeSide(t, key, "k1")
	seen := map[string]bool{}
	for range 3 {
		token := srv.ingress.IssueToken("acme", "alice", defaultRuntimeTokenTTL)
		if _, err := runtime.Authenticate(context.Background(), ingress.Credentials{BearerToken: token}); err != nil {
			t.Fatal(err)
		}
		jti := claimOf(t, token, "jti")
		if jti == "" || seen[jti] {
			t.Fatalf("每个 JWT 有自己的 jti：%q", jti)
		}
		seen[jti] = true
	}
}

func TestLogoutRevokesTheSessionImmediately(t *testing.T) {
	ts, _, _, _ := newServerWith(t, nil)
	c := newClient(t, ts.URL)
	login(t, c, "acme", "alice")
	id := cookieOf(t, c, ts.URL)
	if code := replay(t, ts.URL, id, "/api/me"); code != http.StatusOK {
		t.Fatalf("登录后可用：%d", code)
	}
	if code, _, raw := c.do("POST", "/api/logout", nil); code != http.StatusOK {
		t.Fatalf("退出登录：%d %s", code, raw)
	}
	if code := replay(t, ts.URL, id, "/api/me"); code != http.StatusUnauthorized {
		t.Fatalf("退出之后同一个 Cookie 立即失效：%d", code)
	}
}

func TestRevokeAllSignsOutEveryDeviceOfThatUserOnly(t *testing.T) {
	ts, _, _, _ := newServerWith(t, nil)
	laptop, phone, bob := newClient(t, ts.URL), newClient(t, ts.URL), newClient(t, ts.URL)
	login(t, laptop, "acme", "alice")
	login(t, phone, "acme", "alice")
	login(t, bob, "acme", "bob")
	if code, out, raw := laptop.do("POST", "/api/sessions/revoke-all", nil); code != http.StatusOK || out["revoked"] != float64(2) {
		t.Fatalf("退出所有设备：%d %s", code, raw)
	}
	for name, c := range map[string]*client{"laptop": laptop, "phone": phone} {
		if code, _, _ := c.do("GET", "/api/me", nil); code != http.StatusUnauthorized {
			t.Errorf("%s 的会话也作废：%d", name, code)
		}
	}
	if code, _, _ := bob.do("GET", "/api/me", nil); code != http.StatusOK {
		t.Errorf("别的用户不受影响：%d", code)
	}
}

func TestSessionsExpireWhenIdleAndAtTheirMaximumAge(t *testing.T) {
	clock := &fakeNow{t: time.Now()}
	ts, _, _, _ := newServerWith(t, func(c *Config) { c.Now, c.SessionIdle, c.SessionMaxAge = clock.now, time.Hour, 3*time.Hour })
	c := newClient(t, ts.URL)
	login(t, c, "acme", "alice")
	for range 3 {
		clock.advance(50 * time.Minute)
		if code, _, _ := c.do("GET", "/api/me", nil); code != http.StatusOK {
			t.Fatalf("有活动时空闲时间顺延：%d", code)
		}
	}
	clock.advance(40 * time.Minute) // 建立 3 小时 10 分钟：超过绝对有效期
	if code, _, _ := c.do("GET", "/api/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("超过绝对有效期，有活动也作废：%d", code)
	}

	login(t, c, "acme", "alice")
	clock.advance(61 * time.Minute)
	if code, _, _ := c.do("GET", "/api/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("空闲超过一小时作废：%d", code)
	}
}

func TestLegacyTokenCookiesAreUpgradedToSessions(t *testing.T) {
	ts, _, cfg, _ := newServerWith(t, nil)
	legacy := localingress.New(localingress.Options{SigningKey: cfg.SigningKey}).IssueToken("acme", "alice", time.Hour)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/me", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: legacy})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("有效的旧令牌 Cookie 照常可用：%d", res.StatusCode)
	}
	var upgraded string
	for _, ck := range res.Cookies() {
		if ck.Name == cookieName {
			upgraded = ck.Value
		}
	}
	if upgraded == "" || strings.Contains(upgraded, ".") {
		t.Fatalf("换成会话 id 的 Cookie：%q", upgraded)
	}
	if code := replay(t, ts.URL, upgraded, "/api/me"); code != http.StatusOK {
		t.Fatalf("换出来的会话可用：%d", code)
	}
	if code := replay(t, ts.URL, legacy[:len(legacy)-2]+"xx", "/api/me"); code != http.StatusUnauthorized {
		t.Fatalf("无效的旧令牌照样 401：%d", code)
	}
}
