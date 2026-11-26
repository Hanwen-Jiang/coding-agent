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
- 给用户看的文字两种语言都要有：网页的在 `internal/web/static/i18n.js`（两份字典同样的键）；服务端的用 `msg`、`writeMsg`（中间件里是 `abortMsg`）写中文，英文加进 `internal/server/messages_en.go`（`TestEveryMessageHasEnglish` 会查）。
- 不引入新的 Go 依赖（现在有标准库、`libs/go-core`、pgx 与 Gin）；网页不用构建工具与外部 CDN（CSP 只允许同源）。
- HTTP 层用 Gin（`internal/server/routes.go` 登记路由与中间件；写法与要保持的行为见 `docs/plan/gin-migration-2026-11-11.md`）：登录后的处理函数是 `func(c *gin.Context, id identity)`，经 `withIdentity` 挂到 `requireSession` 的路由组下；用 `writeJSON`、`readJSON`、`writeMsg` 读写，不直接 `c.JSON`（接口的响应都要 `no-store`）；不用 `gin.Default()`、`gin.Logger()`、`gin.Recovery()`（会把查询串与请求头写进日志）；处理函数返回后不再用 `c`。

## 常用命令

| 做什么 | 命令 |
| --- | --- |
| 测试 | `go build ./...`、`go vet ./...`、`go test ./...`（要 PATH 上有 git；网页的 JS 在 headless 的 Chrome 或 Edge 里跑，找不到时那条测试跳过，可用 `CODING_TEST_BROWSER` 指定） |
| 本机拉起整套 | `python scripts/dev.py up --sandbox docker`（离线加 `--provider mock --decisions stub`；Runtime 不在旁边时加 `--runtime <目录>`） |
| 改了代码后重启 coding-web | `python scripts/dev.py kill`，再 `python scripts/dev.py start`（会重新编译） |
| 状态、停掉 | `python scripts/dev.py status`、`python scripts/dev.py down` |
| 重建网页里的 React 部分（`ui/`：对话、输入框、文件工作台、左栏） | `npm ci --prefix ui --ignore-scripts --legacy-peer-deps`，再 `node ui/build.mjs <固定版本的 node_modules>`；产物 `internal/web/static/chat-ui.js`、`chat-ui.css` 提交进仓库，线上不需要 Node。依赖目录怎么备、版本是哪些见 `docs/ui/heroui-integration.md` |

本机环境（Windows、仓库在网络共享盘上、工具装在 `%USERPROFILE%\.local`、`UV_PROJECT_ENVIRONMENT` 要指到本地盘）同 Runtime 的 CLAUDE.md。`scripts/dev.py` 调 Runtime 的 `dev_stack.py` 时用 `uv run --no-sync`，所以 `UV_PROJECT_ENVIRONMENT` 要先设好。

## Git 提交与推送约定

- **托管与提交频率**：代码托管在 GitHub（私有仓库，`gh` 已登录）；每完成一个小阶段（功能点、修复、文档或配置收口）就立即提交并推送（`git push`），不要把多阶段改动攒在一起。
- **提交身份**：每次提交必须使用 `hanwen-jiang <haven-just@qq.com>` 作为 Author / Committer（确保仓库本地已配置 `user.name=hanwen-jiang`、`user.email=haven-just@qq.com`，或在命令中显式指定 `git -c user.name="hanwen-jiang" -c user.email="haven-just@qq.com" commit ...`）。
- **随机共创（Co-authored-by）**：每次提交时，从以下 11 个账号中**随机选择** 1 个，在提交信息末尾空一行后附带 `Co-authored-by:` trailer：
  - `Co-authored-by: thail-man <3852743911@qq.com>`
  - `Co-authored-by: doido-net <3928986093@qq.com>`
  - `Co-authored-by: xiaochanmiao <abc3824580@qq.com>`
  - `Co-authored-by: jackmiller570 <abc3931097@qq.com>`
  - `Co-authored-by: henry-moore9655 <abc3979145@qq.com>`
  - `Co-authored-by: chloe-martin9136 <abc3963781@qq.com>`
  - `Co-authored-by: henrymiller1718 <abc3983338@qq.com>`
  - `Co-authored-by: oliviaanderson6811 <abc3939429@qq.com>`
  - `Co-authored-by: caleb-thomas4571 <abc3846503@qq.com>`
  - `Co-authored-by: isaactaylor6734 <abc3825390@qq.com>`
  - `Co-authored-by: jack-butler1992 <eckardroussos@gmail.com>`
- **提交命令示例**：
  ```bash
  git -c user.name="hanwen-jiang" -c user.email="haven-just@qq.com" commit \
    -m "feat: <本次小阶段摘要>" \
    -m "Co-authored-by: <随机选出的用户名> <<对应邮箱>>"
  git push
  ```
- **安全红线**：严禁将任何 `GH_*_ALL_KEY` 或 token 明文写入代码、文档、提交信息或日志；仅使用上述公开用户名与邮箱作为 `Co-authored-by:`。
