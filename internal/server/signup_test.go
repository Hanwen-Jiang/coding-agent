package server

// Runtime 的 docs/plan/open-signup.md 第 2 节第 4 条（F4）：开放注册的防滥用。
//
//   - 规则只管新注册（这个平台账号还没关联到本地账号）；已有的账号照常登录。名单制（CODING_ALLOWED_LOGINS）里的账号不受这些规则限制。
//   - GitHub：账号满 CODING_SIGNUP_MIN_ACCOUNT_AGE_DAYS 天（默认 30，created_at），并且有验证过的邮箱（GET /user/emails，
//     要 user:email 的授权，默认的 scope 里加上它）；读不到邮箱按没有验证过处理。
//   - 其余几家：有账号创建时间的（GitLab、Gitee、Gitea）同样查天数；Google 查 email_verified（没有创建时间）；
//     Bitbucket 两样都查不了，新注册不收，只能连接到已有的账号。
//   - 封禁名单 CODING_BLOCKED_LOGINS（<平台>:<登录名>，不分大小写）：登录与连接都拒绝，已有的账号也一样。
//   - 被拒时不建账号，错误说明原因。
//
// 实现之前 SignupRulesFromEnv 报 errSignupPending，这些测试跳过。

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// signupGitHub 是 GitHub 的替身：账号的 created_at 与 /user/emails 可以改。
type signupGitHub struct {
	server    *httptest.Server
	login     string
	createdAt time.Time
	// emails 为 nil 时 /user/emails 回 404（没有 user:email 的授权）。
	emails []map[string]any
}

func newSignupGitHub(t *testing.T, age time.Duration, emails []map[string]any) *signupGitHub {
	f := &signupGitHub{login: "octo-cat", createdAt: time.Now().UTC().Add(-age), emails: emails}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login/oauth/access_token":
			writeJSON(w, http.StatusOK, map[string]string{"access_token": fakeToken, "scope": "repo,read:user,user:email", "token_type": "bearer"})
		case r.URL.Path == "/user":
			writeJSON(w, http.StatusOK, map[string]any{"login": f.login, "id": 42, "name": "Octo", "created_at": f.createdAt.Format(time.RFC3339)})
		case r.URL.Path == "/user/emails" && f.emails != nil:
			writeJSON(w, http.StatusOK, f.emails)
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

var (
	verified   = []map[string]any{{"email": "octo@example.com", "verified": true, "primary": true}}
	unverified = []map[string]any{{"email": "octo@example.com", "verified": false, "primary": true}}
	strict     = SignupRules{MinAccountAge: 30 * 24 * time.Hour, RequireVerifiedEmail: true}
)

func skipUntilSignup(t *testing.T) {
	t.Helper()
	if _, err := SignupRulesFromEnv(func(string) string { return "" }); errors.Is(err, errSignupPending) {
		t.Skip("F4 的注册防滥用还没写：先审验收测试（Runtime 的 docs/plan/open-signup.md 第 2 节第 4 条）")
	}
}

func newSignupServer(t *testing.T, rules SignupRules, edit func(*Config)) (*httptest.Server, Config, *Server) {
	t.Helper()
	skipUntilSignup(t)
	ts, _, cfg, srv := newServerWith(t, func(c *Config) {
		c.Providers = map[string]ProviderConfig{}
		for _, id := range []string{"github", "gitlab", "gitee", "gitea", "google", "bitbucket"} {
			c.Providers[id] = ProviderConfig{ClientID: "client", ClientSecret: "secret"}
		}
		c.PublicURL = "http://placeholder"
		c.Signup = rules
		if edit != nil {
			edit(c)
		}
	})
	srv.cfg.PublicURL = ts.URL
	return ts, cfg, srv
}

func loginResult(t *testing.T, ts *httptest.Server, c *client, provider, query string) string {
	t.Helper()
	res := oauthFlow(t, c, ts, provider, query, "good-code")
	loc, _ := url.QueryUnescape(res.Header.Get("Location"))
	return loc
}

func noAccount(t *testing.T, cfg Config, tenant string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(cfg.DataDir, tenant)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("被拒的注册不能建账号 %s：%v", tenant, err)
	}
}

func TestSignupRulesFromEnv(t *testing.T) {
	skipUntilSignup(t)
	env :=func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	rules, err := SignupRulesFromEnv(env(nil))
	if err != nil || rules.MinAccountAge != 30*24*time.Hour || !rules.RequireVerifiedEmail || len(rules.Blocked) != 0 {
		t.Fatalf("默认：满 30 天、要验证过的邮箱、没有封禁：%+v %v", rules, err)
	}
	rules, err = SignupRulesFromEnv(env(map[string]string{"CODING_SIGNUP_MIN_ACCOUNT_AGE_DAYS": "0", "CODING_SIGNUP_REQUIRE_VERIFIED_EMAIL": "false",
		"CODING_BLOCKED_LOGINS": "github:Spammer, gitlab:bot"}))
	if err != nil || rules.MinAccountAge != 0 || rules.RequireVerifiedEmail || len(rules.Blocked) != 2 {
		t.Fatalf("0 天与 false 关掉对应的检查，封禁名单逗号分隔：%+v %v", rules, err)
	}
	for _, bad := range []map[string]string{{"CODING_SIGNUP_MIN_ACCOUNT_AGE_DAYS": "-1"}, {"CODING_SIGNUP_MIN_ACCOUNT_AGE_DAYS": "ten"}, {"CODING_SIGNUP_REQUIRE_VERIFIED_EMAIL": "maybe"}} {
		if _, err := SignupRulesFromEnv(env(bad)); err == nil {
			t.Fatalf("%v 应报错", bad)
		}
	}
}

func TestGitHubAsksForTheEmailScope(t *testing.T) {
	_, _, srv := newSignupServer(t, strict, nil)
	if !strings.Contains(srv.providers["github"].Scopes, "user:email") {
		t.Fatalf("默认的 GitHub scope 要有 user:email（读验证过的邮箱）：%q", srv.providers["github"].Scopes)
	}
}

func TestYoungGitHubAccountsCannotSignUp(t *testing.T) {
	gh := newSignupGitHub(t, 10*24*time.Hour, verified)
	ts, cfg, srv := newSignupServer(t, strict, nil)
	pointProvider(srv, "github", gh.server.URL)
	if loc := loginResult(t, ts, newClient(t, ts.URL), "github", ""); !strings.HasPrefix(loc, "/#/login?error=") || !strings.Contains(loc, "30") {
		t.Fatalf("注册不满 30 天的账号应被拒并说明天数：%s", loc)
	}
	noAccount(t, cfg, "gh-octo-cat")

	gh.createdAt = time.Now().UTC().Add(-40 * 24 * time.Hour)
	if loc := loginResult(t, ts, newClient(t, ts.URL), "github", ""); loc != "/" {
		t.Fatalf("满 30 天、有验证过的邮箱应能注册：%s", loc)
	}
}

func TestGitHubSignupNeedsAVerifiedEmail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		emails []map[string]any
	}{{"邮箱都没验证", unverified}, {"读不到邮箱", nil}} {
		gh := newSignupGitHub(t, 400*24*time.Hour, tc.emails)
		ts, cfg, srv := newSignupServer(t, strict, nil)
		pointProvider(srv, "github", gh.server.URL)
		if loc := loginResult(t, ts, newClient(t, ts.URL), "github", ""); !strings.HasPrefix(loc, "/#/login?error=") || !strings.Contains(loc, "邮箱") {
			t.Fatalf("%s：应被拒并提到邮箱：%s", tc.name, loc)
		}
		noAccount(t, cfg, "gh-octo-cat")
	}
}

func TestExistingAccountsAreNotRechecked(t *testing.T) {
	gh := newSignupGitHub(t, 2*24*time.Hour, unverified)
	ts, _, srv := newSignupServer(t, SignupRules{}, nil)
	pointProvider(srv, "github", gh.server.URL)
	if loc := loginResult(t, ts, newClient(t, ts.URL), "github", ""); loc != "/" {
		t.Fatalf("规则关着时注册：%s", loc)
	}
	srv.cfg.Signup = strict
	if loc := loginResult(t, ts, newClient(t, ts.URL), "github", ""); loc != "/" {
		t.Fatalf("已有的账号再登录不受新注册的规则限制：%s", loc)
	}
}

func TestAllowListedAccountsSkipTheSignupRules(t *testing.T) {
	gh := newSignupGitHub(t, 24*time.Hour, nil)
	ts, _, srv := newSignupServer(t, strict, func(c *Config) { c.AllowedLogins = []string{"github:octo-cat"} })
	pointProvider(srv, "github", gh.server.URL)
	if loc := loginResult(t, ts, newClient(t, ts.URL), "github", ""); loc != "/" {
		t.Fatalf("名单制里的账号不受年龄与邮箱的限制：%s", loc)
	}
}

func TestBlockedLoginsCannotSignInOrLink(t *testing.T) {
	gh := newSignupGitHub(t, 400*24*time.Hour, verified)
	gl := newFakeForge(t, "gitlab", map[string]any{"id": 77, "username": "Bad-Bot", "name": "Bot", "created_at": time.Now().UTC().Add(-400 * 24 * time.Hour).Format(time.RFC3339)})
	ts, cfg, srv := newSignupServer(t, SignupRules{}, nil)
	pointProvider(srv, "github", gh.server.URL)
	pointProvider(srv, "gitlab", gl.server.URL)
	c := newClient(t, ts.URL)
	if loc := loginResult(t, ts, c, "github", ""); loc != "/" {
		t.Fatalf("封禁之前能登录：%s", loc)
	}
	srv.cfg.Signup = SignupRules{Blocked: []string{"GitHub:Octo-Cat", "gitlab:bad-bot"}}
	if loc := loginResult(t, ts, newClient(t, ts.URL), "github", ""); !strings.Contains(loc, "停用") {
		t.Fatalf("封禁名单里的账号（已有的也一样）不能登录：%s", loc)
	}
	srv.cfg.Signup = SignupRules{Blocked: []string{"gitlab:bad-bot"}}
	if loc := loginResult(t, ts, c, "gitlab", "?link=1"); !strings.Contains(loc, "停用") {
		t.Fatalf("封禁名单里的账号不能连接：%s", loc)
	}
	noAccount(t, cfg, "gl-bad-bot")

	cfg.Signup = SignupRules{Blocked: []string{"nowhere:x"}}
	if _, err := New(cfg); err == nil {
		t.Fatal("写错的封禁名单项让服务起不来")
	}
}

func TestOtherProvidersUseWhatTheyCanCheck(t *testing.T) {
	old, young := time.Now().UTC().Add(-400*24*time.Hour), time.Now().UTC().Add(-3*24*time.Hour)
	for _, tc := range []struct {
		name, provider string
		user           map[string]any
		ok             bool
	}{
		{"GitLab 满 30 天", "gitlab", map[string]any{"id": 1, "username": "a", "created_at": old.Format(time.RFC3339)}, true},
		{"GitLab 不满 30 天", "gitlab", map[string]any{"id": 2, "username": "b", "created_at": young.Format(time.RFC3339)}, false},
		{"Gitee 不满 30 天", "gitee", map[string]any{"id": 3, "login": "c", "created_at": young.Format(time.RFC3339)}, false},
		{"Gitea 满 30 天", "gitea", map[string]any{"id": 4, "login": "d", "created": old.Format(time.RFC3339)}, true},
		{"Google 邮箱验证过", "google", map[string]any{"sub": "5", "email": "e@example.com", "email_verified": true, "name": "E"}, true},
		{"Google 邮箱没验证", "google", map[string]any{"sub": "6", "email": "f@example.com", "email_verified": false, "name": "F"}, false},
		{"Bitbucket 新注册", "bitbucket", map[string]any{"uuid": "{7}", "username": "g", "display_name": "G"}, false},
	} {
		forge := newFakeForge(t, tc.provider, tc.user)
		ts, _, srv := newSignupServer(t, strict, nil)
		pointProvider(srv, tc.provider, forge.server.URL)
		loc := loginResult(t, ts, newClient(t, ts.URL), tc.provider, "")
		if got := loc == "/"; got != tc.ok {
			t.Fatalf("%s：应 %v，得到 %s", tc.name, tc.ok, loc)
		}
	}
}
