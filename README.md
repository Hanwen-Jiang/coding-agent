# Coding Agent

建在 [Agent Runtime](https://github.com/Hanwen-Jiang/agent-runtime) 上的在线多租户编码智能体。在网页上登录、建工作区、用一句话下任务；Agent 读代码、改代码、在沙箱里跑测试，Reviewer 判定是否完成。你看进度、回答需要确认的问题、审阅 diff，再提交、推送并开草稿 PR，或者下载补丁、丢弃。

本仓库只是一个应用：任务都经 Runtime 的 Task API 跑，Planner、tool_loop、Reviewer 与沙箱都在 Runtime 那边。这里是 coding-web（Go 服务）加它内嵌的网页。

## 能做什么

- **工作区与会话**：一个工作区放一个或多个代码仓库，下面开很多会话。每个会话有自己的对话与工作副本，互不干扰，可以同时跑；一个会话一个分支、一个 PR。
- **看着它干活**：对话里是逐字输出的进度与成组的工具调用，右栏是环境、计划、并行的子 Agent、交付的文件、改动与用量。要确认的步骤会弹出来问你。
- **审阅与交付**：看 diff、文件与提交历史；推送只推这个会话的 `agent/…` 分支并开草稿 PR（GitLab 是 Merge Request），不强推。
- **多平台登录**：GitHub、Gitee、GitLab、Gitea / Forgejo、Bitbucket、Google。一个账号可以连接多家，工作区里可以混放各家的仓库。
- **GitHub App**：按仓库授权、用短期令牌；Webhook 进持久的收件箱，去重、重试、对账。
- **接入点**：网页之外，任务还能由 GitHub 的 @ 评论、定时任务、服务器告警、邮件触发，结果回到触发的地方。见[下面一节](#接入点)。
- **每个任务的设置**：沙箱等级、模型倾向、是否联网；可复用的环境配置；Skills 与 MCP 连接；网络密钥。
- **附件、来源与文件工作台**：输入框可以选文件、拖放、粘贴图片；文件工作台里预览、检索、编辑并发布新版本。
- **两种语言**：界面有简体中文与英文。

## 跑起来

前置：Go、[uv](https://docs.astral.sh/uv/)、Temporal CLI、Docker（沙箱用）、git，以及 Runtime 仓库检出在本仓库旁边：

```text
<某个目录>/
  agent-runtime/   Runtime（按它的 README 装好依赖：uv sync --all-packages --locked）
  coding-agent/    本仓库
```

在本仓库根目录：

```bash
python scripts/dev.py up --sandbox docker
```

它先用 Runtime 的 `scripts/dev_stack.py` 拉起 Temporal、task-service、Model Gateway 与 Worker，再编译并拉起 coding-web。输出里的 `Coding` 就是网页地址（默认 http://127.0.0.1:8095）。用任意租户名与用户名登录（开发登录，仅供测试），新建工作区时选“示例”可以直接试“修好 CI”的任务。

- **离线试用**：加 `--provider mock --decisions stub`，不调真模型，Agent 只会回一句桩回复。
- **真模型**：地址与密钥放在 `~/.agentruntime/model-provider.env`（见 Runtime 的 README）。
- **Runtime 不在旁边**：用 `--runtime <目录>` 或环境变量 `AR_RUNTIME_DIR` 指定。
- 其余选项原样交给 `dev_stack.py up`。

| 做什么 | 命令 |
| --- | --- |
| 看状态 | `python scripts/dev.py status` |
| 只重启 coding-web（改了代码或配置） | `python scripts/dev.py kill`，再 `python scripts/dev.py start`（会重新编译） |
| 看 coding-web 的环境变量（密钥打码） | `python scripts/dev.py env` |
| 全部停掉 | `python scripts/dev.py down` |
| 构建与测试 | `go build ./...`、`go vet ./...`、`go test ./...` |

运行目录默认 `~/.agentruntime/coding-dev`（`--root` 可改），Runtime 与 coding-web 共用；coding-web 的数据在 `<运行目录>/coding`。网页的 JS 测试在 headless 的 Chrome 或 Edge 里跑，找不到浏览器时那几条跳过（`CODING_TEST_BROWSER` 可指定）。

## 第三方登录

在各家建一个 OAuth 应用，回调地址都是 `http://127.0.0.1:8095/api/auth/<平台>/callback`，把 Client ID 与 Secret 写进 `~/.agentruntime/oauth.env`（在仓库外，不提交），然后重启 coding-web。配了哪几家，登录页就出现哪几家。

| 平台 | 在哪里建应用 | 回调里的 `<平台>` | 权限 |
| --- | --- | --- | --- |
| GitHub | Settings → Developer settings → OAuth Apps | `github` | 默认 scope `repo read:user user:email` |
| Gitee | 设置 → 第三方应用 → 创建应用 | `gitee` | 勾选 user_info、projects、pull_requests |
| GitLab（含自建） | User Settings → Applications | `gitlab` | 勾选 api、read_user；自建的填 `CODING_GITLAB_URL` |
| Gitea / Forgejo / Codeberg | Settings → Applications → OAuth2 Applications | `gitea` | 默认指向 Codeberg；自建的填 `CODING_GITEA_URL`（可加 `CODING_GITEA_NAME`） |
| Bitbucket Cloud | Workspace settings → OAuth consumers | `bitbucket` | Account: Read、Repositories: Write、Pull requests: Write |
| Google（只用来登录） | Google Cloud Console → Credentials → OAuth client ID（Web） | `google` | openid、email、profile |

```text
CODING_GITHUB_CLIENT_ID = <Client ID>
CODING_GITHUB_CLIENT_SECRET = <Client Secret>
CODING_GITLAB_CLIENT_ID = <Application ID>
CODING_GITLAB_CLIENT_SECRET = <Secret>
CODING_GITLAB_URL = https://git.example.com
```

第一次用哪一家登录就自动注册，租户是 `<前缀>-<登录名>`（前缀：gh、gitee、gl、gitea、bb、google）。登录后在左下角菜单的“账号与平台”里连接别家，之后用其中任何一家登录都是同一个账号。

令牌只存在服务端，按账号与平台加密，不进浏览器、日志与 `.git/config`；git 用环境变量传认证头；会过期的令牌用前自动续期。

## 接入点

接入点让任务从网页之外的地方起。不管从哪儿起，都是一个普通的会话加一个 Runtime Task：网页上照常看进度、追加指令、回答确认、审阅改动；到了要对外说话的时候（已受理、要确认、有结果、失败），再按出口投递一次。

| 类型 | 怎么触发 | 结果默认回到哪儿 |
| --- | --- | --- |
| GitHub @ 评论 | 在 Issue 或 PR 里用 @ 开头的评论；评论的人要有写权限并已关联账号 | 原 Issue 或 PR 下回帖 |
| 定时任务 | 按 cron 表达式，间隔不小于 1 小时；上一次没跑完就跳过 | 建连接时选 |
| 服务器告警 | 每个连接一个带密钥的地址；同一个告警在一段时间内只起一个任务，默认只分析不改代码 | 建连接时选 |
| 邮件 | 账号里验证过的邮箱发来的信 | 同一个邮件线程里回信 |

“类型”是代码，“连接”是用户的配置：在 设置 → 接入点 里建连接，绑一个工作区，定自主程度、回复语言、每小时的次数与预算。连接建好后默认停用，启用了才接收。出口现在有网页、GitHub 回帖与邮件三种。

接入点要一个 Postgres（`CODING_ENTRY_DSN`，没设时沿用 `CODING_GITHUB_DSN`；没有库时这个功能关闭）。触发、运行与投递都落库，带租约领取，重启不丢、不重复投递。设计与各类型的规格见 [docs/plan/entry-points.md](docs/plan/entry-points.md)，各类型的配置项与邮件服务的配法见 [docs/plan/entry-points-providers.md](docs/plan/entry-points-providers.md)。

## 配置

coding-web 全部经环境变量配置，完整的说明在 [cmd/coding-web/main.go](cmd/coding-web/main.go) 开头的注释里。最常用的：

| 变量 | 作用 |
| --- | --- |
| `CODING_TASK_API` | Runtime task-service 的地址（必填）；Workspace API 在同一个地址 |
| `CODING_DATA_DIR` | 项目与会话的元数据目录（必填） |
| `CODING_PUBLIC_URL` | 对外的地址，用来拼 OAuth 回调 |
| `CODING_DEV_LOGIN=1` | 打开开发登录，仅供测试；要同时给 `AR_DEV_SIGNING_KEY` |
| `CODING_JWT_KEY_FILE`、`CODING_JWT_KID` | 用 Ed25519 私钥签标准 JWT，Runtime 只持公钥 |
| `CODING_TOKEN_KEY` | 第三方平台令牌的加密密钥；只用 JWT 时必填 |
| `CODING_ALLOWED_LOGINS` | 只放行这些 `<平台>:<登录名>`；不给时谁都能用第三方登录 |
| `CODING_GITHUB_APP_*`、`CODING_GITHUB_WEBHOOK_SECRET`、`CODING_GITHUB_DSN` | GitHub App 与它的收件箱；全不配时关闭，只配一部分时启动失败 |
| `CODING_ENTRY_DSN`、`CODING_SMTP_*` | 接入点的库与邮件出口 |
| `CODING_RUNTIME_TASK_BUDGET` | 提交任务时带上单个任务的预算上限；Runtime 支持这个字段之后再打开，默认关 |
| `CODING_VALKEY_URL` | 应用缓存用 Valkey；`CODING_PROFILE=production` 时必填，连不上就拒绝启动 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 给了就经 OpenTelemetry 发指标 |

应用缓存只放公开的 Skills / MCP 目录、目录上游的限流状态与 GitHub 的仓库权限；浏览器会话、项目与触发记录不进缓存。见 [docs/plan/coding-cache-2026-11-10.md](docs/plan/coding-cache-2026-11-10.md)。

## 组成

| 部分 | 位置 | 说明 |
| --- | --- | --- |
| coding-web | `cmd/coding-web`、`internal/server` | Go 服务：登录与账号、工作区与会话、经 Task API 下任务、代理 SSE、diff、提交与推送 |
| 网页 | `ui/chat`、`internal/web/static` | 主对话、输入框与文件工作台用 React 19 + HeroUI；其余是原生 JS。构建产物同源，由 Go 内嵌 |
| GitHub App | `internal/github` | Webhook 收件箱（Postgres）：验签、去重、退避重试、死信、重放、对账 |
| 接入点 | `internal/entry` | 连接、触发、投递的存储与各类型（`githubcomment`、`schedule`、`alert`、`email`） |
| 示例仓库 | `internal/samples` | 内嵌的“CI 修复”示例 |
| 本机拉起 | `scripts/dev.py` | 调 Runtime 的 `dev_stack.py`，再拉起 coding-web |
| 部署 | `deploy/` | Dockerfile 与 K8s 清单 |

与 Runtime 的边界：只经 Task API、Workspace API 与只读的 EnvironmentService 打交道，代码上只依赖 Runtime 的 `libs/go-core` 与生成的 proto；工作区的文件一律经 Workspace API，coding-web 自己不碰。`go.mod` 用 replace 指向并排检出的 `../agent-runtime`。

## 部署

`go.mod` 指向并排检出的 Runtime，所以镜像要在两个仓库的上一级目录里构建：

```bash
docker build -f coding-agent/deploy/Dockerfile -t <镜像名>:<版本> .
```

K8s 清单在 [deploy/k8s/coding-web.yaml](deploy/k8s/coding-web.yaml)，装在 Runtime 的命名空间里，开头的注释写了要改的地方。打 `v*` 标签时 `.github/workflows/image.yml` 构建镜像并推到 GHCR。coding-web 现在只支持单副本。

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/plan/coding-agent-mvp.md](docs/plan/coding-agent-mvp.md) | 计划与取舍：界面、工作区与会话、多平台登录、多语言、与 Runtime 的关系（第 10 节） |
| [docs/plan/github-app.md](docs/plan/github-app.md) | GitHub App 的接入（G-01～G-06） |
| [docs/plan/entry-points.md](docs/plan/entry-points.md) | 接入点的设计、规格与交付 |
| [docs/plan/cloud-environments-config.md](docs/plan/cloud-environments-config.md) | 环境配置 |
| [docs/plan/multi-repository-web.md](docs/plan/multi-repository-web.md) | 一个工作区里放多个仓库 |
| [docs/file-workbench.md](docs/file-workbench.md) | 文件工作台 |
| [docs/ui](docs/ui) | 界面：对话的设计、HeroUI 的集成、上传与来源、接入点管理页 |
| [DESIGN.md](DESIGN.md) | 界面的设计规范 |
| [CLAUDE.md](CLAUDE.md) | 给在本仓库工作的人与 AI 编码助手：边界、硬性规则、常用命令 |

## 已知限制

- **单副本**：项目与会话的元数据存 JSON 文件；只有 GitHub 收件箱与接入点在 Postgres 里。
- **文件工作台与上传**要 Runtime 一侧的文件接口、Worker 与 ObjectStore 一起更新；Runtime 没有这些接口时面板是空的，上传会失败。
- **Git 平台**：不支持 fork 后提 PR，要有仓库的写权限；Gitee、Bitbucket 的一些细节只对着替身测过；每家只能配一个实例。
- **进度**：对话有逐字增量与工具之间的公开进度，完整的工具 stdout 还不在事件契约里。
- **接入点**：还没有 IM（飞书、Slack）与出站 Webhook。
