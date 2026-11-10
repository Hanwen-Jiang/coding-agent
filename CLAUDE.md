# CLAUDE.md

本仓库是 Coding Agent：建在 Agent Runtime（仓库 `agent-runtime`）上的在线多租户编码智能体。coding-web（Go）加原生 JS 的网页；任务都经 Runtime 的 Task API 跑，Planner、tool_loop、Reviewer、沙箱都在 Runtime 那边。

## 先读什么

| 文件 | 作用 |
| --- | --- |
| `docs/plan/coding-agent-mvp.md` | 计划与取舍：界面、工作区与会话、多平台登录、多语言、与 Runtime 的关系（第 10 节） |
| `README.md` | 怎么跑、怎么配第三方登录 |
| Runtime 仓库的 `CLAUDE.md` 与 `docs/design/agent-runtime-design.md` | Runtime 的术语、接口与硬性规则 |

## 与 Runtime 的边界

- 本仓库只是应用：只经 Task API、Workspace API 与只读的 EnvironmentService（都在 task-service 上同一个地址；EnvironmentService 只给沙箱环境的记录，右栏“环境”一栏读它，看一眼不借沙箱）与 Runtime 打交道，代码上只依赖 Runtime 的 `libs/go-core`（开发登录用）。不在这里绕过它们调 Runtime 的内部服务。工作区的文件一律经 Workspace API（`internal/server/workspaces.go`，`CODING_WORKSPACES=runtime`，默认）；`local`（自己在共用的工作区根里跑 git）只留给单元测试与没有 Workspace API 的 Runtime。
- Runtime 要改的（契约、Task API、Worker 的工具与沙箱）在 Runtime 仓库里改，按它的规则走（`/proto`、`/schemas` 走 S-01）；不要把应用的代码放回 Runtime 仓库。
- 两个仓库并排检出：`../agent-runtime`。`go.mod` 用 replace 指过去（Runtime 的模块路径还是占位）。
- 术语按 Runtime 设计书附录 A（Task、Session、effect 上限、Verdict……）；界面上给用户看的叫法以计划第 7 节为准（“项目”在界面上叫工作区）。

## 硬性规则

- 凭据与密钥（OAuth 令牌、签名密钥、Client Secret）不进日志、错误说明、浏览器与 `.git/config`；令牌只在服务端加密存放。
- 租户只来自登录得到的令牌；工作区路径一律由服务端按（租户、工作区、会话）拼出并校验。
- 推送只推会话自己的 `agent/*` 分支，不强推。网页推送由用户触发；已启用接入点的单次触发按连接的自主程度授权，GitHub 评论还要现查写权限。改 `.github/workflows/` 或 Agent 自行提交的改动必须先确认，24 小时后授权过期（`docs/plan/github-app.md` G-06、`docs/plan/entry-points.md`）。
- 给用户看的文字两种语言都要有：网页的在 `internal/web/static/i18n.js`（两份字典同样的键）；服务端的用 `msg`、`writeMsg` 写中文，英文加进 `internal/server/messages_en.go`（`TestEveryMessageHasEnglish` 会查）。
- 不引入新的 Go 依赖（现在只有标准库与 `libs/go-core`）；网页不用构建工具与外部 CDN（CSP 只允许同源）。

## 常用命令

| 做什么 | 命令 |
| --- | --- |
| 测试 | `go build ./...`、`go vet ./...`、`go test ./...`（要 PATH 上有 git；网页的 JS 在 headless 的 Chrome 或 Edge 里跑，找不到时那条测试跳过，可用 `CODING_TEST_BROWSER` 指定） |
| 本机拉起整套 | `python scripts/dev.py up --sandbox docker`（离线加 `--provider mock --decisions stub`；Runtime 不在旁边时加 `--runtime <目录>`） |
| 改了代码后重启 coding-web | `python scripts/dev.py kill`，再 `python scripts/dev.py start`（会重新编译） |
| 状态、停掉 | `python scripts/dev.py status`、`python scripts/dev.py down` |

本机环境（Windows、仓库在网络共享盘上、工具装在 `%USERPROFILE%\.local`、`UV_PROJECT_ENVIRONMENT` 要指到本地盘）同 Runtime 的 CLAUDE.md。`scripts/dev.py` 调 Runtime 的 `dev_stack.py` 时用 `uv run --no-sync`，所以 `UV_PROJECT_ENVIRONMENT` 要先设好。

代码托管在 GitHub（私有仓库，`gh` 已登录）；每完成一个小阶段就提交并推送。
