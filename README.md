# Coding Agent

建在 [Agent Runtime](https://github.com/Hanwen-Jiang/agent-runtime) 上的在线多租户编码智能体：网页上登录、建工作区、用一句话下任务；Agent 读代码、改代码、在沙箱里跑测试，Reviewer 判定是否完成；你在网页上看进度、回答需要确认的问题、审阅 diff，再提交、推送并开草稿 PR，或者下载补丁、丢弃。计划与取舍见 [docs/plan/coding-agent-mvp.md](docs/plan/coding-agent-mvp.md)。

本仓库只是一个应用：任务都经 Runtime 的 Task API 跑，Planner、tool_loop、Reviewer、沙箱都在 Runtime 那边。与 Runtime 的关系见计划第 10 节。

## 跑起来

前置：Go、uv、Temporal CLI、Docker（沙箱用），以及 Runtime 仓库检出在本仓库旁边：

```text
<某个目录>/
  agent-runtime/   Runtime（按它的 CLAUDE.md 装好依赖：uv sync --all-packages --locked）
  coding-agent/    本仓库
```

在本仓库根目录：

```bash
python scripts/dev.py up --sandbox docker
```

它先用 Runtime 的 `scripts/dev_stack.py` 拉起 Temporal、task-service、Model Gateway 与 Worker，再编译并拉起 coding-web。Runtime 不在旁边时用 `--runtime <目录>` 或环境变量 `AR_RUNTIME_DIR` 指定。离线试用（不调真模型，Agent 只会回一句桩回复）加 `--provider mock --decisions stub`；真模型的地址与密钥放在 `~/.agentruntime/model-provider.env`（见 Runtime 的 CLAUDE.md）。其余选项原样交给 `dev_stack.py up`。

输出里的 `Coding` 就是网页地址（默认 http://127.0.0.1:8095）。用任意租户名与用户名登录（开发登录，仅供测试），新建工作区时选“示例”可以直接试“修好 CI”的任务。

| 做什么 | 命令 |
| --- | --- |
| 看状态 | `python scripts/dev.py status` |
| 只重启 coding-web（改了代码或 OAuth 配置） | `python scripts/dev.py kill`，再 `python scripts/dev.py start`（会重新编译） |
| 看 coding-web 的环境变量（密钥打码） | `python scripts/dev.py env` |
| 全部停掉 | `python scripts/dev.py down` |
| 测试 | `go test ./...`（要 PATH 上有 git） |

运行目录默认 `~/.agentruntime/coding-dev`（`--root` 可改），Runtime 与 coding-web 共用；coding-web 的数据在 `<运行目录>/coding`。

### 打开第三方登录（GitHub、Gitee、GitLab、Gitea、Bitbucket、Google）

在各家建一个 OAuth 应用，回调地址都是 `http://127.0.0.1:8095/api/auth/<平台>/callback`，把 Client ID 与 Secret 写进 `~/.agentruntime/oauth.env`（仓库外，不提交；只有 GitHub 时的旧文件 `github-oauth.env` 也照样读），然后重启 coding-web。配了哪几家，登录页就出现哪几家：

| 平台 | 在哪里建应用 | 回调里的 `<平台>` | 权限 |
| --- | --- | --- | --- |
| GitHub | Settings → Developer settings → OAuth Apps | `github` | 默认 scope `repo read:user` |
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

**一个账号，多个平台**：第一次用哪一家登录就自动注册（租户 `<前缀>-<登录名>`：gh、gitee、gl、gitea、bb、google）；登录后在左下角菜单的“账号与平台”里连接别家，之后用其中任何一家登录都是同一个账号，工作区里可以混放各家的仓库。新建工作区时从已连接的平台里选仓库（私有仓库用你的授权克隆）。会话里的任务跑完，在“改动”里点“推送并开草稿 PR”（GitLab 是 Merge Request）：给这个会话新建 `agent/…` 分支、提交、只推这个分支、开草稿；之后再推会更新同一个；合并后点“回到 main”开始下一轮（或者开一个新会话）。

令牌只存在服务端（按账号与平台加密），不进浏览器、日志与 `.git/config`；git 用环境变量传认证头；GitLab、Gitee、Gitea、Bitbucket 的令牌会过期，用前自动续期。

## 界面

采用 HeroUI v3 的组件样式与布局（集成说明见 [HeroUI integration](docs/ui/heroui-integration.md)）。一个**工作区**可以包含一个或多个代码仓库，下可以开很多**会话**；每个会话一段对话加一份自己的工作副本（从工作区本地克隆），会话之间互不干扰、可以同时跑，一个会话一个分支、一个 PR。

左栏上半部分是新建会话、搜索（Ctrl K）、工作区、进行中，下半部分按工作区分组列出会话，底部的个人信息块打开账号、外观与语言的菜单；中间是会话的对话（或工作区一览、进行中的任务）；右栏是当前会话的状态（环境、上下文、输出、改动、后台任务），窄屏时收进抽屉。diff、文件与提交历史在宽抽屉里打开。界面有简体中文与英文（计划第 7.2 节）。

## 组成

| 部分 | 位置 | 说明 |
| --- | --- | --- |
| coding-web | `cmd/coding-web`、`internal/server` | Go，只用标准库与 Runtime 的 `libs/go-core`：登录与账号（开发登录、各家 OAuth）、工作区与会话、经 Task API 下任务、代理 SSE、diff、提交与推送 |
| 网页 | `internal/web/static` | 原生 JS 单页应用，无构建步骤，CSP 只允许同源脚本与样式 |
| 示例仓库 | `internal/samples` | 内嵌的“CI 修复”示例 |
| 本机拉起 | `scripts/dev.py` | 调 Runtime 的 `dev_stack.py`，再拉起 coding-web |

## 已知限制

GitHub 用的是 OAuth App（用户级令牌、权限按 scope 而不是按仓库），以后换 GitHub App；不支持 fork 后提 PR（要有仓库的写权限）；Gitee、Bitbucket 的一些细节只对着替身测过，还没在真平台上验；每家只能配一个实例；令牌加密的密钥由开发签名密钥派生，生产要换 SecretStore；元数据存 JSON 文件，单实例；进度只到节点级，没有 token 级流式输出。工作区与 Worker 共用一块盘（沙箱等级已改为逐个任务经 `effect_ceiling` 传给 Runtime）；上云的镜像与清单在 `deploy/`，还要去掉的耦合见计划第 10 节。
