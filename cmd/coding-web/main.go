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
//	CODING_TOKEN_KEY_PREVIOUS   轮换 CODING_TOKEN_KEY 时的旧密钥（G-01）：旧数据读到时改用新密钥重新加密，之后去掉
//	CODING_SESSION_IDLE         浏览器会话的空闲有效期（AUTH-02，Go 的时长写法，默认 24h）：Cookie 里只有会话 id，
//	CODING_SESSION_MAX_AGE      绝对有效期（默认 168h）；退出登录、“退出所有设备”立即生效，调 Runtime 时现签 5 分钟的令牌
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
//	CODING_NETWORK_SECRETS=1    打开网络密钥页（WEB-SEC：经 Runtime 的 UserSecretService 存取用户自己的密钥，值只进不出）；
//	                            环境还不能引用它们（D-23 第二阶段），所以默认关着
//
// GitHub App（G-01，docs/plan/github-app.md 第 3 节；全不配时关闭，配了一部分时启动失败）：
//
//	CODING_GITHUB_APP_ID、CODING_GITHUB_APP_CLIENT_ID、CODING_GITHUB_APP_SLUG   不是密钥
//	CODING_GITHUB_APP_PRIVATE_KEY_FILE    私钥文件（单独的 Secret 按文件挂进来）；私钥放在环境变量里会被拒绝
//	CODING_GITHUB_WEBHOOK_SECRET、_PREVIOUS  Webhook secret；_PREVIOUS 只在轮换期间有
//	CODING_GITHUB_APP_CLIENT_SECRET       用户授权（G-03）要的 Client secret
//	CODING_GITHUB_DSN                     收件箱的 Postgres（G-02，internal/github）；配了 App 而没有它时 Webhook 回 503
//	CODING_GITHUB_API_URL                 对账调的 API，默认 https://api.github.com
//	OTEL_EXPORTER_OTLP_ENDPOINT           Collector 的 OTLP/HTTP 地址（G-05）：给了就经 Telemetry 发 coding.github.* 指标
//	                                      （投递、处理、积压、死信、续期与签发失败），不给时不记
//
//	配了 App 时每小时按 GitHub 的安装列表对账一次项目的远端状况（G-05），覆盖收件箱对账补不到的超过 3 天的停机。
//	有收件箱时，Issue 与 PR 里以 @<slug> 开头的评论触发自动运行（G-06，internal/server/githubtrigger.go），每 10 秒推进一次；
//	App 要有 Issues 读写权限并订阅 issue_comment。
//
// 接入点（docs/plan/entry-points-providers.md）：
//
//	CODING_ENTRY_DSN           接入点 Postgres，未设时沿用 CODING_GITHUB_DSN；无库时关闭
//	CODING_ENTRY_MEMORY        仅开发登录、无 JWT 时显式设 1，生产禁止
//	CODING_SMTP_ADDR           邮件出口服务器 host:port，与 FROM 同时配置
//	CODING_SMTP_FROM           发信地址
//	CODING_SMTP_USERNAME       SMTP 用户名，与 PASSWORD 成对；支持无认证的受信 TLS relay
//	CODING_SMTP_PASSWORD       SMTP 密码（不能打印或回传浏览器）
//	CODING_SMTP_IMPLICIT_TLS   true/1 用 TLS 直连；否则要求 STARTTLS，绝不降级明文
//
//	coding-web github-replay <delivery>   把收件箱里的一条投递重新入队（死信修好之后用）
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
	"github.com/agentruntime/agent-runtime/libs/go-core/adapters/oteltelemetry"
	"github.com/agentruntime/coding-agent/internal/github"
	"github.com/agentruntime/coding-agent/internal/server"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "github-replay":
			os.Exit(githubReplay(os.Args[2:]))
		default:
			log.Fatalf("不认识的子命令 %s（只有 github-replay <delivery>）", os.Args[1])
		}
	}
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

		NetworkSecrets: os.Getenv("CODING_NETWORK_SECRETS") == "1",
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if strings.TrimSpace(os.Getenv(oteltelemetry.EnvEndpoint)) != "" {
		tel, err := oteltelemetry.FromEnv("coding-web", os.Getenv, os.Stderr)
		if err != nil {
			log.Fatalf("coding-web: Telemetry 配置不合法：%v", err)
		}
		defer tel.Close()
		platform, err := tel.BindPlatform("coding-web")
		if err != nil {
			log.Fatalf("coding-web: %v", err)
		}
		cfg.Metrics = github.TelemetryMetrics(platform)
	}
	if err := configureEntries(ctx, &cfg, os.Getenv); err != nil {
		log.Fatalf("coding-web: %v", err)
	}
	if cfg.EntryStore != nil {
		defer cfg.EntryStore.Close()
	}
	var inbox *github.Service
	if cfg.GitHubApp != nil {
		if os.Getenv("CODING_GITHUB_DSN") == "" {
			log.Printf("配了 GitHub App 但没有 CODING_GITHUB_DSN：Webhook 回 503，配上库之后对账会补投 3 天内失败的")
		} else {
			if inbox, err = newInbox(ctx, cfg.GitHubApp, cfg.Metrics); err != nil {
				log.Fatalf("coding-web: %v", err)
			}
			defer inbox.Close()
			cfg.Webhook = inbox
		}
	}
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("coding-web: %v", err)
	}
	// AUTH-01 第 4 步之前：还用旧密钥（开发签名密钥派生的）加密的平台令牌与环境配置的密钥，开始服务之前一次改用 CODING_TOKEN_KEY。
	// 只记个数，不记任何值；没有旧密钥时什么也不做。
	if r, err := srv.Reseal(); err != nil {
		log.Fatalf("coding-web: 重新加密旧数据失败：%v", err)
	} else if r.Accounts > 0 || r.Secrets > 0 || r.Unreadable > 0 {
		log.Printf("coding-web: 旧数据重新加密：账号 %d 个（改写 %d 个），环境密钥 %d 个，哪把密钥都解不开的 %d 个", r.Accounts, r.AccountsResealed, r.Secrets, r.Unreadable)
	}
	if cfg.EntryStore != nil {
		if err := srv.MigrateEntryTriggers(ctx); err != nil {
			log.Fatal("coding-web: 旧 GitHub 触发记录迁移失败；未启动接入点处理")
		}
		go srv.RunEntries(ctx)
	}
	if inbox != nil {
		// 事件到动作（G-04）：处理函数在 Server 上，Server 建好之后再注册、再起后台循环。
		for event, h := range srv.GitHubHandlers() {
			inbox.Handle(event, h)
		}
		go inbox.Run(ctx)
	}
	if cfg.GitHubApp != nil {
		go reconcileInstallations(ctx, srv)
	}
	addr := os.Getenv("CODING_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8095"
	}
	httpServer := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
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

// newInbox 连上 GitHub App 的收件箱（G-02）：库是 CODING_GITHUB_DSN，对账调的 API 是 CODING_GITHUB_API_URL（默认 api.github.com）。
func newInbox(ctx context.Context, app *server.GitHubApp, metrics github.Metrics) (*github.Service, error) {
	return github.New(ctx, github.Options{DSN: os.Getenv("CODING_GITHUB_DSN"), App: app, AppID: app.AppID, APIURL: os.Getenv("CODING_GITHUB_API_URL"), Metrics: metrics})
}

// reconcileInstallations 启动后一分钟、之后每小时按 GitHub 的安装列表对账一次（G-05）；失败只记日志，下一轮再来。
func reconcileInstallations(ctx context.Context, srv *server.Server) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := srv.ReconcileGitHubInstallations(ctx); err != nil && ctx.Err() == nil {
			log.Printf("GitHub App 的安装对账失败（一小时后再试）：%v", err)
		}
		timer.Reset(time.Hour)
	}
}

// advanceTriggers 每 10 秒把 GitHub 评论触发的自动运行往前推一步（G-06）：建会话、提交 Task、等它结束、推送或请求确认。
func advanceTriggers(ctx context.Context, srv *server.Server) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := srv.AdvanceGitHubTriggers(ctx); err != nil && ctx.Err() == nil {
			log.Printf("推进 GitHub 的自动运行失败：%v", err)
		}
	}
}

// githubReplay 把收件箱里的一条投递重新入队（死信修好之后用）：coding-web github-replay <delivery>。
func githubReplay(args []string) int {
	if len(args) != 1 {
		log.Print("用法：coding-web github-replay <delivery>")
		return 2
	}
	app, err := server.GitHubAppFromEnv(os.Getenv)
	if err != nil || app == nil || os.Getenv("CODING_GITHUB_DSN") == "" {
		log.Printf("github-replay 要 GitHub App 的配置与 CODING_GITHUB_DSN：%v", err)
		return 2
	}
	inbox, err := newInbox(context.Background(), app, nil)
	if err != nil {
		log.Print(err)
		return 1
	}
	defer inbox.Close()
	if err := inbox.Replay(context.Background(), args[0]); err != nil {
		log.Print(err)
		return 1
	}
	log.Printf("投递 %s 已重新入队，coding-web 的后台会处理它", args[0])
	return 0
}

// readKeys 读 JWT 私钥、第三方令牌的加密密钥（AUTH-01）与会话的有效期（AUTH-02）；错误信息只提变量名，不带密钥。
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
	for name, target := range map[string]*time.Duration{"CODING_SESSION_IDLE": &cfg.SessionIdle, "CODING_SESSION_MAX_AGE": &cfg.SessionMaxAge} {
		if raw := getenv(name); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d <= 0 {
				return fmt.Errorf("%s 要是正的时长，例如 24h", name)
			}
			*target = d
		}
	}
	for name, target := range map[string]*[]byte{"CODING_TOKEN_KEY": &cfg.TokenKey, "CODING_TOKEN_KEY_PREVIOUS": &cfg.TokenKeyPrevious} {
		if raw := strings.TrimSpace(getenv(name)); raw != "" {
			key, err := base64.StdEncoding.DecodeString(raw)
			if err != nil {
				if key, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(raw, "=")); err != nil {
					return fmt.Errorf("%s 要是 base64", name)
				}
			}
			*target = key
		}
	}
	app, err := server.GitHubAppFromEnv(getenv)
	if err != nil {
		return err
	}
	cfg.GitHubApp = app
	return nil
}
