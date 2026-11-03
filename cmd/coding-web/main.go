// Command coding-web 是在线多租户 coding agent 的 MVP 服务（docs/plan/coding-agent-mvp.md）：网页、项目（Git 工作区）、
// 经 Task API 下任务、代理 SSE 进度、看改动与提交。Runtime 一侧用 task-service 与 Worker；本机用 scripts/dev.py up 一起拉起。
//
// 环境变量：
//
//	CODING_ADDR                 监听地址，默认 127.0.0.1:8095
//	CODING_TASK_API             task-service 的地址，例如 http://127.0.0.1:8080（必填）；Workspace API 在同一个地址
//	CODING_WORKSPACES           工作区在哪里：runtime（默认，经 Runtime 的 Workspace API，coding-web 不碰工作区的文件）
//	                            或 local（自己在 CODING_WORKSPACE_ROOT 里跑 git，要与 Worker 共用这块盘）
//	CODING_WORKSPACE_ROOT       local 时的工作区根，与 Worker 的 AR_TOOL_WORKSPACE_ROOT 相同
//	CODING_DATA_DIR             项目与任务的元数据目录（必填）
//	CODING_DEV_LOGIN=1          打开开发登录（租户 + 用户名换令牌，仅供测试）；要同时给 AR_DEV_SIGNING_KEY
//	AR_DEV_SIGNING_KEY          与 task-service 相同的开发签名密钥
//	CODING_JWT_KEY_FILE         Ed25519 私钥文件（Runtime 的 task-service keygen 生成，AUTH-01）：给了就签标准 JWT，Runtime 只持公钥；
//	                            开发令牌不再签发、也不再认（登录过的用户重新登录一次）
//	CODING_JWT_KID              私钥的 kid（与 Runtime 签发方登记里的一致；给了私钥就必填）
//	CODING_JWT_ISSUER           JWT 的 iss，默认 coding-agent
//	CODING_TOKEN_KEY            第三方平台令牌的加密密钥（32 字节的 base64）；只用 JWT 时必填。没给时沿用由 AR_DEV_SIGNING_KEY 派生的；
//	                            两者都给时，旧数据读到时用它重新加密，之后可以去掉 AR_DEV_SIGNING_KEY
//	CODING_TENANT_POLICIES_FILE task-service 的开发租户策略文件；给了就在租户首次登录时把 effect 上限设为 write_reversible
//	                            （只给本机开发；每个任务的沙箱等级经 effect_ceiling 传，集群里租户策略由 Runtime 的 Registry 定）
//	CODING_GIT                  git 可执行文件，默认 PATH 上的 git
//	CODING_SECURE_COOKIE=1      Cookie 只走 HTTPS
//	CODING_PUBLIC_URL           对外的地址（拼 OAuth 回调 <地址>/api/auth/<平台>/callback），例如 http://127.0.0.1:8095
//	CODING_ALLOWED_LOGINS       逗号分隔的 <平台>:<登录名>（例如 github:octo-cat）：给了就只放行这些账号（与关联到它们的别家账号）；
//	                            不给时谁都能用第三方登录
//	CODING_SIGNUP_MIN_ACCOUNT_AGE_DAYS    开放注册时新注册的平台账号至少注册了多少天（默认 30，0 不查；GitHub、GitLab、Gitee、Gitea）
//	CODING_SIGNUP_REQUIRE_VERIFIED_EMAIL  新注册要有验证过的邮箱（默认 true；GitHub 读 /user/emails，Google 看 email_verified）
//	CODING_BLOCKED_LOGINS       逗号分隔的 <平台>:<登录名>：封禁，登录与连接都拒绝（已有的账号也一样）
//
// 第三方登录（<平台> 是 GITHUB、GITEE、GITLAB、GITEA、BITBUCKET、GOOGLE；Client ID 与 Secret 都给了才打开这一家）：
//
//	CODING_<平台>_CLIENT_ID      OAuth 应用的 Client ID（Bitbucket 叫 Key）
//	CODING_<平台>_CLIENT_SECRET  OAuth 应用的 Client Secret（不写进日志）
//	CODING_<平台>_URL            网页地址：自建的 GitLab、Gitea 填它的地址；GitHub Enterprise 也填（默认各家公共站，Gitea 默认 Codeberg）
//	CODING_<平台>_API_URL        API 地址，不填按网页地址推出
//	CODING_<平台>_SCOPES         OAuth 的 scope，不填用各家默认（GitHub "repo read:user user:email"，GitLab "api read_user"…）
//	CODING_<平台>_NAME           显示名，例如自建 Gitea 叫 "公司 Git"
//	CODING_SANDBOX              Worker 的 shell_run 在哪里跑（docker 或 host），只用来在界面上说明环境
//	CODING_SANDBOX_IMAGE        docker 时 shell_run 用的镜像（与 Worker 的 AR_TOOL_SHELL_IMAGE 相同）
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/agentruntime/agent-runtime/libs/go-core/adapters/jwtingress"
	"github.com/agentruntime/coding-agent/internal/server"
)

func main() {
	cfg := server.Config{
		TaskAPI:       os.Getenv("CODING_TASK_API"),
		Workspaces:    os.Getenv("CODING_WORKSPACES"),
		WorkspaceRoot: os.Getenv("CODING_WORKSPACE_ROOT"),
		DataDir:       os.Getenv("CODING_DATA_DIR"),
		DevLogin:      os.Getenv("CODING_DEV_LOGIN") == "1",
		SigningKey:    []byte(os.Getenv("AR_DEV_SIGNING_KEY")),
		PoliciesFile:  os.Getenv("CODING_TENANT_POLICIES_FILE"),
		Git:           os.Getenv("CODING_GIT"),
		SecureCookie:  os.Getenv("CODING_SECURE_COOKIE") == "1",

		PublicURL:     os.Getenv("CODING_PUBLIC_URL"),
		Providers:     map[string]server.ProviderConfig{},
		AllowedLogins: strings.FieldsFunc(os.Getenv("CODING_ALLOWED_LOGINS"), func(r rune) bool { return r == ',' }),

		Sandbox:      os.Getenv("CODING_SANDBOX"),
		SandboxImage: os.Getenv("CODING_SANDBOX_IMAGE"),
	}
	signup, err := server.SignupRulesFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	cfg.Signup = signup
	if err := readKeys(&cfg, os.Getenv); err != nil {
		log.Fatalf("coding-web: %v", err)
	}
	for _, id := range []string{"github", "gitee", "gitlab", "gitea", "bitbucket", "google"} {
		env := func(name string) string { return os.Getenv("CODING_" + strings.ToUpper(id) + "_" + name) }
		if c := (server.ProviderConfig{
			ClientID: env("CLIENT_ID"), ClientSecret: env("CLIENT_SECRET"), URL: env("URL"), APIURL: env("API_URL"), Scopes: env("SCOPES"), Name: env("NAME"),
		}); c.ClientID != "" {
			cfg.Providers[id] = c
		}
	}
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("coding-web: %v", err)
	}
	addr := os.Getenv("CODING_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8095"
	}
	httpServer := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	log.Printf("coding-web 在 http://%s 上运行", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("coding-web: %v", err)
	}
}

// readKeys 读 JWT 私钥与第三方令牌的加密密钥（AUTH-01）；错误信息只提变量名，不带密钥。
func readKeys(cfg *server.Config, getenv func(string) string) error {
	if path := getenv("CODING_JWT_KEY_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读不了 CODING_JWT_KEY_FILE：%w", err)
		}
		key, err := jwtingress.ParsePrivateKey(raw)
		if err != nil {
			return fmt.Errorf("CODING_JWT_KEY_FILE 不是 Ed25519 私钥：%w", err)
		}
		cfg.JWTKey, cfg.JWTKeyID, cfg.JWTIssuer = key, getenv("CODING_JWT_KID"), getenv("CODING_JWT_ISSUER")
	}
	if raw := strings.TrimSpace(getenv("CODING_TOKEN_KEY")); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			if key, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "=")); err != nil {
				return errors.New("CODING_TOKEN_KEY 要是 base64")
			}
		}
		cfg.TokenKey = key
	}
	return nil
}
