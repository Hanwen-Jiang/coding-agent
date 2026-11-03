package server

// 开放注册的防滥用（Runtime 的 docs/plan/open-signup.md 第 2 节第 4 条，F4；验收测试在 signup_test.go）。
//
//   - 只管新注册（这个平台账号还没关联到本地账号）；已有的账号照常登录；名单制（CODING_ALLOWED_LOGINS）里的账号不受限。
//   - GitHub：账号满 MinAccountAge（created_at），并且有验证过的邮箱（GET /user/emails，默认的 scope 带 user:email）；读不到邮箱按没验证过。
//   - GitLab、Gitee、Gitea：查账号的创建时间；Google：邮箱要验证过（user() 里本来就查）；Bitbucket 两样都查不了，新注册不收，只能连接。
//   - 封禁名单：登录与连接都拒绝，已有的账号也一样。规则每次从 Config.Signup 读，运维改配置重启即生效。

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SignupRules 是新注册要满足的条件与封禁名单。零值表示不查。
type SignupRules struct {
	// MinAccountAge 是平台账号至少注册了多久（CODING_SIGNUP_MIN_ACCOUNT_AGE_DAYS，默认 30 天，0 不查）。
	MinAccountAge time.Duration
	// RequireVerifiedEmail 要求平台上有验证过的邮箱（CODING_SIGNUP_REQUIRE_VERIFIED_EMAIL，默认 true）。
	RequireVerifiedEmail bool
	// Blocked 是封禁的平台账号（CODING_BLOCKED_LOGINS，"<平台>:<登录名>"，不分大小写）：登录与连接都拒绝。
	Blocked []string
}

// SignupRulesFromEnv 按环境变量给出注册规则（没设时满 30 天、要验证过的邮箱、没有封禁）。
func SignupRulesFromEnv(getenv func(string) string) (SignupRules, error) {
	r := SignupRules{MinAccountAge: 30 * 24 * time.Hour, RequireVerifiedEmail: true}
	if v := strings.TrimSpace(getenv("CODING_SIGNUP_MIN_ACCOUNT_AGE_DAYS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return SignupRules{}, fmt.Errorf("CODING_SIGNUP_MIN_ACCOUNT_AGE_DAYS 要是不小于 0 的整数，得到 %q", v)
		}
		r.MinAccountAge = time.Duration(n) * 24 * time.Hour
	}
	if v := strings.TrimSpace(getenv("CODING_SIGNUP_REQUIRE_VERIFIED_EMAIL")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return SignupRules{}, fmt.Errorf("CODING_SIGNUP_REQUIRE_VERIFIED_EMAIL 要是 true 或 false，得到 %q", v)
		}
		r.RequireVerifiedEmail = b
	}
	for _, entry := range strings.Split(getenv("CODING_BLOCKED_LOGINS"), ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			r.Blocked = append(r.Blocked, entry)
		}
	}
	return r, nil
}

// loginKey 把 "<平台>:<登录名>" 规范成小写；平台不认识或登录名为空时 ok 为 false。
func loginKey(entry string) (string, bool) {
	provider, login, ok := strings.Cut(strings.ToLower(strings.TrimSpace(entry)), ":")
	if _, known := specOf(provider); !ok || login == "" || !known {
		return "", false
	}
	return provider + ":" + login, true
}

func (s *Server) blockedLogin(provider, login string) bool {
	want := provider + ":" + strings.ToLower(login)
	for _, entry := range s.cfg.Signup.Blocked {
		if key, ok := loginKey(entry); ok && key == want {
			return true
		}
	}
	return false
}

// signupCheck 看一个新注册的平台账号满不满足规则；不满足时给出说明。
func (s *Server) signupCheck(ctx context.Context, p *Provider, token string, u remoteUser) *Msg {
	rules := s.cfg.Signup
	if rules.MinAccountAge == 0 && !rules.RequireVerifiedEmail {
		return nil
	}
	if p.id == "bitbucket" {
		return msg("%s 查不了账号的注册时间与邮箱，不能用它注册新账号：先用别的方式登录，再在账号页连接 %s", p.Name, p.Name)
	}
	if rules.MinAccountAge > 0 && p.id != "google" {
		if u.CreatedAt.IsZero() {
			return msg("读不到这个 %s 账号的注册时间，暂时不能用它注册", p.Name)
		}
		if time.Since(u.CreatedAt) < rules.MinAccountAge {
			return msg("这个 %s 账号注册还不满 %d 天，暂时不能用它注册", p.Name, int(rules.MinAccountAge.Hours()/24))
		}
	}
	if rules.RequireVerifiedEmail && p.id == "github" {
		var emails []struct {
			Verified bool `json:"verified"`
		}
		err := p.api(ctx, token, http.MethodGet, "/user/emails", nil, &emails)
		verified := false
		for _, e := range emails {
			verified = verified || e.Verified
		}
		if err != nil || !verified {
			return msg("这个 %s 账号没有验证过的邮箱（或没有授权读取邮箱）：在 %s 上验证邮箱后再来", p.Name, p.Name)
		}
	}
	return nil
}
