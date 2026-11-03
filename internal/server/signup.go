package server

// 开放注册的防滥用（Runtime 的 docs/plan/open-signup.md 第 2 节第 4 条，F4）。验收测试在 signup_test.go；
// 实现等测试人审过再写，在那之前 SignupRulesFromEnv 报 errSignupPending，测试跳过，Config.Signup 不起作用。

import (
	"errors"
	"time"
)

// SignupRules 是新注册（平台账号还没关联到本地账号）要满足的条件。零值表示不查。
type SignupRules struct {
	// MinAccountAge 是平台账号至少注册了多久（CODING_SIGNUP_MIN_ACCOUNT_AGE_DAYS，默认 30 天，0 不查）。
	MinAccountAge time.Duration
	// RequireVerifiedEmail 要求平台上有验证过的邮箱（CODING_SIGNUP_REQUIRE_VERIFIED_EMAIL，默认 true）。
	RequireVerifiedEmail bool
	// Blocked 是封禁的平台账号（CODING_BLOCKED_LOGINS，"<平台>:<登录名>"，不分大小写）：登录与连接都拒绝。
	Blocked []string
}

var errSignupPending = errors.New("F4 的注册防滥用还没实现")

// SignupRulesFromEnv 按环境变量给出注册规则。
func SignupRulesFromEnv(func(string) string) (SignupRules, error) {
	return SignupRules{}, errSignupPending
}
