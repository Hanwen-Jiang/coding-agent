package server

// AUTH-01（Runtime 仓库的 docs/plan/auth-tokens-2026-11-03.md 第 2 节）的 coding-agent 一侧：
//
//  1. 配了 Ed25519 私钥（JWTKey、JWTKeyID）时，转给 Task API 的是 JWT：Runtime 只凭 coding-agent 的公钥
//     （jwtingress，iss = JWTIssuer、aud = agent-runtime）就验得过；开发令牌（HMAC）的 Cookie 不再认。
//     （AUTH-02 之后 Cookie 里是会话 id，见 browser_sessions_test.go。）
//  2. 第三方平台令牌的加密密钥独立（TokenKey），不再从签名密钥派生：换签名密钥不影响，签名密钥泄露也解不开。
//  3. 迁移：旧数据（用签名密钥派生的密钥加密的）读到时用旧密钥解开、用新密钥重新加密写回；之后去掉签名密钥照样能读。
//  4. 配置：签名密钥与 JWT 私钥至少给一个；用 JWT 而没有签名密钥时必须给 TokenKey；JWT 私钥要配 kid；TokenKey 是 32 字节。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/adapters/jwtingress"
	"github.com/agentruntime/agent-runtime/libs/go-core/dev/localingress"
	"github.com/agentruntime/agent-runtime/libs/go-core/providers/ingress"
)

func newEd25519(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func tokenKey32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

// runtimeSide 是 Runtime 的 task-service 那边：只有 coding-agent 的公钥。
func runtimeSide(t *testing.T, key ed25519.PrivateKey, kid string) *jwtingress.Provider {
	t.Helper()
	p, err := jwtingress.New(jwtingress.Options{Config: jwtingress.Config{Issuers: []jwtingress.Issuer{{
		Issuer: "coding-agent", Keys: map[string]string{kid: jwtingress.EncodePublicKey(key.Public().(ed25519.PublicKey))},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestJWTModeSignsCookiesAndRuntimeCallsWithEd25519(t *testing.T) {
	key := newEd25519(t)
	ts, fake, _, _ := newServerWith(t, func(c *Config) {
		c.SigningKey, c.JWTKey, c.JWTKeyID, c.TokenKey = nil, key, "k1", tokenKey32(7)
	})
	c := newClient(t, ts.URL)
	if code, _, raw := c.do("POST", "/api/login", map[string]string{"tenant": "acme", "user": "alice"}); code != http.StatusOK {
		t.Fatalf("登录失败：%d %s", code, raw)
	}
	// AUTH-02 之后 Cookie 里是会话 id（browser_sessions_test.go），这里只看转给 Runtime 的令牌。
	runtime := runtimeSide(t, key, "k1")

	pid := sampleProject(t, c)
	code, sess, raw := c.do("POST", "/api/projects/"+pid+"/sessions", map[string]string{"title": "jwt"})
	if code != http.StatusCreated {
		t.Fatalf("开会话失败：%d %s", code, raw)
	}
	api := "/api/projects/" + pid + "/sessions/" + sess["id"].(string)
	if code, _, raw := c.do("POST", api+"/tasks", map[string]string{"message": "hello"}); code != http.StatusCreated {
		t.Fatalf("提交任务失败：%d %s", code, raw)
	}
	if len(fake.tokens) == 0 {
		t.Fatal("要把令牌转给 Task API")
	}
	forwarded := strings.TrimPrefix(fake.tokens[0], "Bearer ")
	id, err := runtime.Authenticate(context.Background(), ingress.Credentials{BearerToken: forwarded})
	if err != nil || id.TenantID != "acme" || id.UserID != "alice" || strings.Count(forwarded, ".") != 2 {
		t.Fatalf("转给 Task API 的是 Runtime 只凭公钥就验得过的 JWT：%+v %v", id, err)
	}
}

func TestJWTModeRejectsDevTokenCookies(t *testing.T) {
	ts, _, _, _ := newServerWith(t, func(c *Config) {
		c.SigningKey, c.JWTKey, c.JWTKeyID, c.TokenKey = nil, newEd25519(t), "k1", tokenKey32(7)
	})
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(ts.URL)
	dev := localingress.New(localingress.Options{SigningKey: []byte("test-key")}).IssueToken("acme", "alice", time.Hour)
	jar.SetCookies(u, []*http.Cookie{{Name: cookieName, Value: dev, Path: "/"}})
	res, err := (&http.Client{Jar: jar}).Get(ts.URL + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("改发 JWT 之后，开发令牌的 Cookie 要重新登录：%d", res.StatusCode)
	}
}

func TestThirdPartyTokensUseAnIndependentKey(t *testing.T) {
	_, _, _, srv := newServerWith(t, func(c *Config) { c.TokenKey = tokenKey32(9) })
	sealed, err := srv.seal("gho_secret", tokenAAD("acme", "alice", "github"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := srv.open(sealed, tokenAAD("acme", "alice", "github")); err != nil || plain != "gho_secret" {
		t.Fatalf("用独立密钥加密、解开：%q %v", plain, err)
	}
	if _, err := openWith(legacyTokenKey([]byte("test-key")), sealed, tokenAAD("acme", "alice", "github")); err == nil {
		t.Fatal("有了独立密钥之后，签名密钥派生的旧密钥解不开新数据")
	}
	_, _, _, other := newServerWith(t, func(c *Config) { c.TokenKey, c.SigningKey = tokenKey32(9), []byte("another-signing-key") })
	if plain, err := other.open(sealed, tokenAAD("acme", "alice", "github")); err != nil || plain != "gho_secret" {
		t.Fatalf("换签名密钥不影响第三方令牌：%q %v", plain, err)
	}
}

func TestLegacyTokensAreReencryptedWhenRead(t *testing.T) {
	dir := t.TempDir()
	conn := Connection{Provider: "github", RemoteID: "1", Login: "octo"}
	_, _, _, old := newServerWith(t, func(c *Config) { c.DataDir = dir })
	if _, err := old.updateAccount("acme", "alice", func(a *Account) error {
		if err := old.sealConnection("acme", "alice", &conn, tokenSet{Access: "gho_legacy", Refresh: "ghr_legacy"}); err != nil {
			return err
		}
		a.Connections["github"] = conn
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 迁移期间：既有签名密钥（旧密钥），又有独立密钥。读到旧数据时解开并用新密钥写回。
	_, _, _, migrating := newServerWith(t, func(c *Config) { c.DataDir, c.TokenKey = dir, tokenKey32(5) })
	a, err := migrating.readAccount("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := migrating.open(a.Connections["github"].Token, tokenAAD("acme", "alice", "github")); err != nil || plain != "gho_legacy" {
		t.Fatalf("读到旧数据：%q %v", plain, err)
	}

	// 迁移之后：只用 JWT、不再有签名密钥，照样解得开（文件里已经是新密钥加密的）。
	_, _, _, after := newServerWith(t, func(c *Config) {
		c.DataDir, c.SigningKey, c.JWTKey, c.JWTKeyID, c.TokenKey = dir, nil, newEd25519(t), "k1", tokenKey32(5)
	})
	a, err = after.readAccount("acme", "alice")
	if err != nil {
		t.Fatal(err)
	}
	for field, sealed := range map[string]string{"token": a.Connections["github"].Token, "refresh": a.Connections["github"].Refresh} {
		aad := tokenAAD("acme", "alice", "github")
		want := "gho_legacy"
		if field == "refresh" {
			aad, want = aad+"/refresh", "ghr_legacy"
		}
		if plain, err := after.open(sealed, aad); err != nil || plain != want {
			t.Fatalf("%s 读到时已经重新加密：%q %v", field, plain, err)
		}
	}
}

func TestConfigNeedsAKeyForEveryToken(t *testing.T) {
	base := func() Config {
		return Config{TaskAPI: "http://127.0.0.1:1", DataDir: t.TempDir()}
	}
	cases := map[string]struct {
		edit func(*Config)
		want string
	}{
		"两种都没有":            {func(c *Config) {}, "AR_DEV_SIGNING_KEY"},
		"JWT 没有 kid":       {func(c *Config) { c.JWTKey, c.TokenKey = newEd25519(t), tokenKey32(1) }, "CODING_JWT_KID"},
		"JWT 没有第三方令牌的密钥":   {func(c *Config) { c.JWTKey, c.JWTKeyID = newEd25519(t), "k1" }, "CODING_TOKEN_KEY"},
		"第三方令牌的密钥不是 32 字节": {func(c *Config) { c.SigningKey, c.TokenKey = []byte("k"), []byte("short") }, "CODING_TOKEN_KEY"},
	}
	for name, tc := range cases {
		cfg := base()
		tc.edit(&cfg)
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：应当报错并指出 %s，实际 %v", name, tc.want, err)
		}
	}
	cfg := base()
	cfg.JWTKey, cfg.JWTKeyID, cfg.TokenKey = newEd25519(t), "k1", tokenKey32(1)
	if _, err := New(cfg); err != nil {
		t.Fatalf("只用 JWT 加独立密钥能起来：%v", err)
	}
}
