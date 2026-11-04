# GitHub App 接入计划

> 状态：2026-10-30 设计稿；2026-11-03 第 5 节各条人已定，App 已注册（第 6 节），G-01～G-06 都做（登记在 Runtime 仓库的 `docs/plan/work-dag-2026-11-03.md`）。来源：人给的六块设计建议，逐条对照本仓库与 Runtime 仓库的代码；GitHub 的行为查了官方文档（第 7 节）。
> 现在 GitHub 走的是 OAuth App（`repo read:user`）：令牌能碰用户名下的所有仓库，也不过期。接 GitHub App 就是为了按仓库授权、用短期令牌。

## 1. 已有与缺失

| 块 | 已有 | 缺 |
| --- | --- | --- |
| 1 密钥与配置 | Secret `coding-oauth`（在服务器的 SOPS 文件里，含 `CODING_GITHUB_CLIENT_ID`、`_SECRET`）；“凭据不进日志、错误、浏览器”的规定 | App ID、Slug、私钥、Webhook secret 与轮换；令牌的加密密钥由开发签名密钥派生（`tokenKey`，注释写着“生产应换成 SecretStore”），不能单独轮换 |
| 2 Webhook 接收 | 无 | 路由、验签、收件、去重、后台处理、死信、重放、对账。存储只有 PVC 上的 JSON 文件，没有数据库；Deployment 是 1 副本、`Recreate` |
| 3 身份与令牌 | OAuth 与 `state` 校验；`identities.json` 按平台不变的编号认人；令牌按（租户、用户、平台）AES-256-GCM 加密；到期前续期（`connToken`）。凭据经 Workspace API 逐次传，Runtime 不保存，只对远端主机生效，错误信息脱敏；Workspace 操作用单独的环境（`reuse=False`），不与 Agent 的命令共用 | App JWT、安装令牌、安装与仓库的记录、操作前的权限核对；`Project` 只有 `owner/repo` 字符串，没有不变的仓库 ID；Git 用户名按 OAuth 写死（`x-oauth-basic`）；续期有并发竞争（第 2 节第 5 条） |
| 4 事件到动作 | 动作的一半已有：`SyncWorkspace`、`Session` 上的分支与 PR 字段 | 事件处理、按仓库 ID 更新、PR 与 CI 状态的显示 |
| 5 防失控 | 只推 `agent/*`、不强推、由人点按钮（CLAUDE.md）；Runtime 的 `workspace.push_branches` | 现在没有自动触发，所以还没有循环的问题；要做自动触发时这一块全部要做 |
| 6 运维与体验 | “账号与平台”页、连接与断开、`CODING_ALLOWED_LOGINS` | coding-web 没有任何指标；对账；安装状态与权限不足的界面；卸载清理；测试用的 App |

## 2. 与原建议不同的地方

1. **安装不能挂在一个租户下。** 建议写的是“installations 关联租户、核对租户拥有这个 installation”。这里的租户是个人（`gh-<登录名>`，计划 coding-agent-mvp.md），一个组织的安装会被很多租户用，“拥有”的说法不成立。
   改成：安装是全局记录；每次操作用**这个用户的令牌**问 GitHub 他能不能访问（`GET /user/installations/{id}/repositories`，只返回他有权限的仓库，并带权限级别）。这一步防的是：用户借一个覆盖面更大的安装，碰到他自己没权限的仓库。
2. **按 `X-GitHub-Delivery` 去重要看状态。** GitHub 重新投递时这个值不变。只要见过就丢弃的话，处理失败的那一条永远修不回来。已处理完或正在处理的才忽略；死信再次投递时重新入队。
3. **断线恢复必须在上线前做，不能放到后面。** GitHub **不会自动重投**失败的投递，只保留 3 天；而 coding-web 是 `Recreate` 部署，每次发版都有一段时间收不到。所以对账与收件箱放在同一个工作项（G-02）。
4. **Setup URL 的 `installation_id` 不可信。** GitHub 明确说可能被伪造。回调里只把它当线索，用用户令牌确认这个用户确实能访问这个安装，才关联。
5. **令牌会过期之后，续期的并发竞争就成了真问题。** 用户令牌 8 小时过期，续期令牌用一次就失效，旧的访问令牌也同时失效。`connToken` 现在读、续、写之间没有锁：两个并发请求同时续，后一个会失败，用户被要求重新授权。另外 `gitCredentials` 吞掉错误、返回空凭据，续不上时变成无凭据克隆，报的是别的错。
6. **令牌的加密密钥要先分出来。** 用户令牌多了续期令牌，放在开发签名密钥派生的密钥下面不合适，而且换签名密钥会让所有令牌解不开。
7. **“三张表”放 Postgres**（2026-11-03 人定，原稿是先用 JSON 文件）。用集群里已有的数据层（`data` 命名空间），coding-web 用单独的库与角色；要加一个 Postgres 驱动（与 Runtime 用同一个），是本仓库第一个数据库依赖。接口照样隔开，测试用替身。
8. **事件只当提示。** GitHub 不保证顺序。收到 `pull_request`、`workflow_run` 后按 API 取当前状态，不用负载里的状态覆盖，否则迟到的旧事件会把状态改回去。
9. **建议里没提、要加的：** `github_app_authorization`（用户撤销授权，要删他的令牌）；校验 `X-GitHub-Hook-Installation-Target-ID` 等于本 App（防测试与生产配错）；`code.cgent.link` 在 Cloudflare 后面，第一次投递要确认 Hookshot 没被 Browser Integrity Check 或 WAF 拦下，被拦就给这个路径加跳过规则；改 `.github/workflows/` 的推送要 `workflows` 权限，没有就会被拒，要给明确的提示。

建议里其余的（表结构以外的令牌分工、事件表、防注入、监控、卸载、测试 App）都采用。顺序也采用，只把对账提前。

## 3. 设计

**配置**

| 变量 | 内容 | 放哪儿 | 2026-11-03 线上 |
| --- | --- | --- | --- |
| `CODING_GITHUB_CLIENT_ID`、`_SECRET` | 旧 OAuth App 的，并存期（30 天）内照用 | `coding-oauth` | 有 |
| `CODING_GITHUB_APP_ID`、`CODING_GITHUB_APP_CLIENT_ID` | App ID、App 的 Client ID（不是密钥） | `coding-oauth` | 有 |
| `CODING_GITHUB_APP_CLIENT_SECRET` | App 的 Client secret：用户授权（G-03 的用户令牌）要用 | `coding-oauth` | **没有**，G-03 之前在 App 设置页生成并存进去 |
| `CODING_GITHUB_APP_SLUG` | `coding-agent-link`，不是密钥 | Chart 的 values | — |
| `CODING_GITHUB_WEBHOOK_SECRET`、`_PREVIOUS` | Webhook secret；`_PREVIOUS` 只在轮换期间有 | `coding-oauth` | 有（没有 `_PREVIOUS`） |
| `CODING_GITHUB_APP_PRIVATE_KEY` → `CODING_GITHUB_APP_PRIVATE_KEY_FILE` | 私钥。Secret 里是 PEM 原文；G-01 只把这一项按文件挂进 coding-web（`items`，模式 0400），变量给路径 | `coding-oauth` | 有 |
| `CODING_TOKEN_KEY`、`_PREVIOUS` | 令牌加密密钥（32 字节）；旧格式读得出，下次写入时换新 | `coding-oauth` | 没有，G-01 生成 |
| `CODING_GITHUB_DSN` | 收件箱与安装记录的库（第 2 节第 7 条） | 新 Secret 或 `coding-oauth` | 没有，G-02 建库时给 |

服务器上：K8s Secret `coding-oauth`（`agent-runtime` 命名空间）是 coding-web 读的；`/root/ar-secrets/coding-oauth.env` 是同样几项的副本，私钥一项叫 `CODING_GITHUB_APP_PRIVATE_KEY_B64`（一行 base64）；PEM 另存在 `/root/ar-secrets/github-app.private-key.pem`。**SOPS 文件（`/root/ar-secrets/secrets.enc.yaml`）里还只有旧 OAuth 的两项**：按 SOPS 重建 Secret 会丢掉 App 的密钥，G-01 上线前补进去。只注入 coding-web。全不配时功能关闭、行为不变；配了一部分时启动失败，错误里只写变量名。

私钥轮换（2026-11-03 13:51 服务器时间轮换过一次，旧的已在 GitHub 上作废）：在 App 设置页生成新私钥，放到 `/root/ar-secrets/github-app.private-key.new`，执行 `bash /root/ar-secrets/rotate-github-app-key.sh`。脚本更新 PEM 文件、`coding-oauth.env` 与 Secret 三处，各留一份备份，不打印密钥；G-01 部署后还要 `kubectl -n agent-runtime rollout restart deploy/coding-web` 才读到新私钥。GitHub 上的旧私钥要手工删，等 coding-web 用新私钥签出过令牌再删。

**收件箱**（Postgres，第 2 节第 7 条）：表 `github_deliveries`（`delivery` 主键、event、action、installation id、负载、状态、次数、下次时间、收到时间）。状态 `received → processing → done`，失败 `retry`（退避，5 次），之后 `dead`。流程：边读边算 HMAC（请求体限长读入）→ 校验 → `INSERT … ON CONFLICT (delivery)` 入箱（已有且是 `dead` 的改回 `received`，别的不动）→ 提交 → 2xx（GitHub 要求 10 秒内）→ 后台处理。后台用 `FOR UPDATE SKIP LOCKED` 领取，`processing` 带租约，超时的重新领取（进程崩溃的情形），这样多副本也不会重复处理。上限 25 MiB（GitHub 不投更大的）。原始负载留 7 天（之后清空负载列、保留行做去重与统计），库连不上时回 503（GitHub 不自动重投，靠对账补）。日志只有 delivery、event、action、installation id。

**记录**：与收件箱同一个库（随第 2 节第 7 条一起放 Postgres，没有单独问）：`github_installations`（账号 ID 与登录名、是否暂停、仓库范围、权限、卸载时间）、`github_repos`（仓库 ID、当前全名、安装 ID）。`Project` 加 `RepoID`、`InstallationID`；旧工作区第一次用到时按 `GET /repos/{owner}/{repo}/installation` 补上。

**一次操作的授权（`authorizeRepo`）**：工作区属于这个租户（已有）→ 取用户令牌（按用户串行续期）→ 安装没被暂停 → 用用户令牌确认他对这个仓库有所需的权限（读：克隆；写：推送、开 PR；不可达一律拒绝，结果缓存不超过 60 秒）→ 签安装令牌（`repository_ids` 只有这个仓库，`permissions` 只给这次要的）→ 经 Workspace API 传入（用户名 `x-access-token`）→ 操作结束调 `DELETE /installation/token`，失败不影响结果。令牌不落盘。

**JWT**：RS256，`iat` 提前 60 秒，`exp` 不超过 10 分钟，`iss` 用 Client ID。只用标准库，不加依赖。

## 4. 工作项与验收

顺序：G-01 → G-02 → G-03 → G-04 → G-05，G-06 在 G-03、G-04 之后（2026-11-03 人定一起做，不再单独确认）。上线前必须有 G-01～G-03，G-02 含对账。都先写测试（替身 GitHub，本仓库已有这种做法；Postgres 的测试像 Runtime 那样自己拉起临时实例）。

### G-01 密钥与配置

> 2026-11-03 已完成（代码，没部署）：`internal/server/githubapp.go`（配置、Webhook 验签、App 的 JWT），`CODING_TOKEN_KEY_PREVIOUS`。与上面第 3 节不同的一处：私钥**单独放 Secret `coding-github-app`**（键 `private-key.pem`），只按文件挂进 coding-web（0400）。原因是 coding-web 对 `coding-oauth` 用的是 `envFrom`，同一个 Secret 里的私钥也会成为环境变量，并被 coding-web 起的 git 子进程继承。环境变量里出现 `CODING_GITHUB_APP_PRIVATE_KEY` 时 coding-web 拒绝启动。部署前：`kubectl -n agent-runtime create secret generic coding-github-app --from-file=private-key.pem=<PEM>`，再从 `coding-oauth` 删掉那一项（`deploy/k8s/coding-web.yaml` 的注释里也写了）。Client secret 到 G-03 才必填。

1. 全不配时行为与现在相同；只配一部分时启动失败，错误写变量名不写值。
2. 用带标记的假密钥跑完登录、签令牌、Webhook、失败路径，日志、错误信息、`/api/config`、`/api/me` 与计量里都找不到它们（断言，不靠人看）。
3. 配了 `_PREVIOUS` 时新旧 Webhook secret 都能过验签；去掉后旧的被拒。
4. 旧格式的令牌能读出，下次写入时换成新格式；换掉开发签名密钥不影响新格式的令牌。
5. 静态检查：私钥只挂进 coding-web，Runtime 的 Chart 与沙箱都没有。

### G-02 Webhook 收件与对账

> 2026-11-03 已完成（代码，没部署）：`internal/github`（收件箱、后台处理、对账、清理），路由 `POST /api/webhooks/github`，`coding-web github-replay <delivery>`，`CODING_GITHUB_DSN`（配了 App 而没有它时 Webhook 回 503）。第 1～8 条有测试（Postgres 用 Runtime 的 pgtest）。还要等后面的：第 6 条“每个处理函数执行两次结果相同”要等 G-04 有了处理函数才测得了；第 8 条的告警指标现在只是计数（`Stats`），G-05 接进 Telemetry 与 Grafana；第 9 条要上线后看。

1. 签名错或缺：401，不落盘，不解析 JSON。只认 `X-Hub-Signature-256`，常量时间比较。
2. 目标 App ID 不符：拒绝。
3. 超过上限：413，不留临时文件；大请求不把内存吃满。
4. 处理函数卡住时，接收仍在 1 秒内回 2xx。
5. 同一个 delivery：`done`、`processing` 的回 2xx 不重复处理；`dead` 的重新入队。进程重启后去重仍生效；两个副本并发领取，同一条只处理一次。
6. 处理失败按退避重试，5 次后进死信；`coding-web github-replay <delivery>` 能重放；进程崩溃时 `processing` 的租约到期后重新领取；每个处理函数执行两次结果相同。
7. **对账**：替身 GitHub 里有 3 个投递失败，启动与定时（每 10 分钟）调 `GET /app/hook/deliveries` 重投；窗口不超过 3 天，每轮有上限，不会重复重投同一条。
8. 负载 7 天后清掉；库连不上时回 503 并记告警指标。
9. 线上第一次真实投递成功（确认没被 Cloudflare 拦）。

### G-03 身份、安装与令牌

> 2026-11-03 coding-agent 一侧已完成（代码，没部署）：`internal/server/githubrepo.go`（`authorizeRepo`、Setup URL `GET /api/github/setup`、App 的 JWT 复用、按用户串行续期），克隆、拉取、推送与开 PR 都经它；配了 Client secret 后 GitHub 的登录与连接走 App 的 OAuth，旧 OAuth 的连接切换期内照用（`legacyGitHub` 给界面用）。第 1～7、10、11 条有测试。第 8 条（Workspace 操作的环境与 Agent 的命令不共用）是 Runtime 已有的做法；第 9 条在 Runtime 仓库验（见那边的总表）。没装 App 的**公开**仓库照旧不带凭据克隆、拉取；私有的提示去安装。上线要 `CODING_GITHUB_APP_CLIENT_SECRET`。

1. 伪造 Setup URL 的 `installation_id`（用户看不到这个安装）：不关联。
2. 安装覆盖了仓库、但用户本人没权限：拒绝，不调签发接口。
3. 只有读权限的用户：能克隆，推送与开 PR 被拒，提示写清原因与去哪儿调整。
4. 安装被暂停或删除：立刻不再签发。
5. 签发请求体只有这个仓库的 ID 与最小权限；令牌不落盘、不进日志；结束时调了吊销，吊销失败不影响结果。
6. JWT 的算法与时间符合第 3 节，过期前自动换。
7. 10 个并发请求同时遇到用户令牌到期：只续一次，全部成功。续不上时报“要重新授权”，不再发空凭据。
8. Workspace 操作的环境与 Agent 的命令不共用（已有，加回归）。
9. **要核对（在 Runtime 仓库）**：Agent 能改工作副本的 `.git/config`，之后的推送会读它。验证 `core.fsmonitor`、`credential.helper`、`http.proxy` 这类配置拿不到令牌。这一条还没验证过；现在的 OAuth 令牌同样受影响，而且危害更大。
10. 旧工作区第一次使用时补上仓库 ID；仓库改名后照常工作。
11. 旧 OAuth 连接在切换期内可用；重新连接后旧令牌被删除。

### G-04 事件到动作

> 2026-11-03 已完成（代码，没部署）：`internal/server/githubevents.go`，经 G-02 的收件箱注册（`Service.Handle`）。表里各行都有测试，每个处理函数执行两次与执行一次结果相同。远端状况记在项目的 `remote_state`、`remote_updated`，PR 与 CI 记在会话的 `pr_state`、`ci`、`reviews`；不正常时 `authorizeRepo` 不签发。界面上的展示在 G-05。App 现在没有 Actions 权限：`workflow_run` 读不到 API 时用事件里的状态，靠 `updated_at` 挡住迟到的旧事件。

| 事件 | 动作 | 验收 |
| --- | --- | --- |
| `installation` deleted、suspend | 停用，取消相关 Task，不再签发 | 运行中的 Task 被取消；之后的操作提示清楚 |
| `installation` unsuspend、created | 恢复或登记 | 恢复后能再签发 |
| `installation_repositories` | 同步可访问的仓库 | 移除的仓库操作被拒，工作区保留 |
| `repository` renamed、transferred、deleted | 按仓库 ID 更新 | 改名后推送到新名字；删除后工作区标记“远端已不存在” |
| `push` | 标记“远端有更新”，不触发 Task | 只有基线分支会标记；界面可同步 |
| `pull_request`、`workflow_run` | 按 API 取当前状态后更新 | 迟到的旧事件不覆盖新状态 |
| `pull_request_review` | 记录并展示 | 不触发 Agent |
| `github_app_authorization` revoked | 删这个用户的令牌 | 之后要重新授权，别的用户不受影响 |

### G-05 运维与界面

> 2026-11-03 服务端已完成（代码，没部署；界面等 WEB-SUB 放开 `app.js`、`i18n.js`）：
> - 第 1 条：`internal/github` 计 `coding.github.deliveries`（`event`、`result`：accepted、signature_failed、wrong_app、too_large、bad_request、unavailable；验签之前 `event` 记 `unverified`）、`coding.github.processed`（done、retry、dead），每 5 秒报一次 `coding.github.backlog` 与 `coding.github.dead_letters`；服务端计 `coding.github.token_refresh_failures`、`coding.github.issue_failures`（`use`：operation、reconcile）。设了 `OTEL_EXPORTER_OTLP_ENDPOINT` 时经 `libs/go-core` 的 Telemetry 发出（平台作用域，不带负载；`deploy/k8s/coding-web.yaml` 指向 Runtime 的 Collector）。Grafana 的告警在 Runtime 仓库的 `deploy/data/values/grafana.yaml`（“Coding Web” 组：一小时内有新死信、积压持续 15 分钟超过 50 条、15 分钟内签名失败超过 10 次；阈值是起步值）。
> - 第 2 条：`internal/server/githubops.go` 的 `ReconcileGitHubInstallations`，coding-web 启动后一分钟、之后每小时跑一次。记着的安装没了、或仓库不在它里面时，按仓库查一次现在的安装（卸载后重新安装、仓库转到别的安装之后安装 ID 会变）。
> - 第 3 条的服务端：`GET /api/github/status`（连接状态、是不是旧 OAuth 的连接、关联的安装、安装入口、各项目的远端状况）。
> - 第 4 条：卸载时关联到各账号的这个安装立即去掉。安装令牌本来就不存（每次签发、用完吊销）；用户令牌在 `github_app_authorization` revoked 时删（G-04）。
> - 顺带修了 G-04 的一处：卸载后重新安装、移除后又加回仓库，而事件丢了时，项目会一直停在 `app_removed`、`repo_removed`。现在 `authorizeRepo` 遇到这两种标记先按 API 再查一次这个仓库现在的安装，签得出来就改记新的安装 ID、清掉标记，查不到才按原来的提示拒绝。
> - 没做：第 3 条的界面；第 5 条的测试 App 要人注册；第 4 条“安装记录保留到卸载后 30 天”（第 5 节第 6 条）现在没有到期清理，项目上留着安装 ID 与 `app_removed`。

1. 指标（经 `libs/go-core` 的 Telemetry，不带负载）：投递数与结果、积压、死信数、签名失败、令牌续期失败、签发失败。Grafana 上死信、积压、签名失败突增会告警。
2. 对账：每小时用 `GET /app/installations` 与仓库列表纠正记录，覆盖超过 3 天的停机。
3. 界面：连接状态、已授权仓库、“去 GitHub 调整权限”的入口、没装时的安装入口、权限不足时的明确提示。两种语言（`i18n.js`、`messages_en.go`）。
4. 卸载与删除账号：令牌立即清掉，负载与记录按第 5 节第 6 条处理。
5. 单独注册一个测试 App，配置与生产不共用；本机与测试只用它。

### G-06 自动触发（2026-11-03 人定一起做）

> 2026-11-03 已完成（代码，没部署）：`internal/server/githubtrigger.go`，经收件箱登记 `issue_comment`，后台每 10 秒推进一次（`AdvanceGitHubTriggers`）；记录在 `<数据目录>/github-triggers.json`，按触发评论的 ID。下面 8 条都有测试（`githubtrigger_test.go`，每个事件投递两次）。计划没写死、实现时定的（都取保守的一边，要改告诉我）：
> 1. 触发评论以 `@coding-agent-link` 开头（后面跟空白或冒号、逗号），后面的文字是指令；确认是 `@coding-agent-link confirm`（或“确认”）开头的评论。只认 `issue_comment` 的 created，编辑过的评论不算。
> 2. GitHub 一侧全用安装令牌：核对权限（`metadata: read`）、拉基线分支（`contents: read`）、推送与开 PR（`contents`、`pull_requests: write`）、回复（`issues: write`），每个都只含这个仓库、用完吊销。PR 由 App 开（草稿），正文写明谁触发的；提交的作者是触发者（noreply 地址）。不用触发者的用户令牌。
> 3. 触发者要关联着 Coding Agent 的账号（按 GitHub 用户 ID），这个账号下要已经有这个仓库的项目；Task 记在这个账号下、花它的额度，每次开一个新会话。没有账号或项目时回复一次说明，不启动。
> 4. 每个仓库每小时最多 3 次、花费 5 美元（按这些 Task 记下的花费），所有租户合计；超了回复说明。数字现在写死在代码里。
> 5. fork 来的 PR 一律不运行（比“不用安装令牌跑它的代码”更严）。
> 6. 改了 `.github/workflows/`，或 Agent 自己做了提交（列不出文件）时请求确认；24 小时没有确认就结束。推送或开 PR 失败、推到一半进程没了，都不重试，回复里让人在 Coding Agent 里手动推。
> 7. Issue 的标题与正文放进 `<github-issue>` 资料块（正文里的结束标记换掉），指令只取触发评论；提交之后记录里不再留评论与 Issue 的内容。
>
> 还要人做的：在 App 设置里加 Issues 读写与 `issue_comment` 事件（各安装要重新同意）；第 5 条“带注入样本的回归进评测集”没做（评测集的标注要人确认，现在只有单元测试核对资料块）；本仓库 CLAUDE.md 里“由人点按钮触发”那句按第 5 节第 7 条由人改。

授权的规则（人定）：有写权限的成员在 Issue 或 PR 里发出触发评论，就算授权**这一次**推到 `agent/*` 并开 PR；改了 `.github/workflows/` 下的文件时，推送前先在那个 PR 或 Issue 里再问一次，等触发者回复确认。

1. 过滤本 App 与其他 Bot 产生的事件（`sender.type`、本 App 的 `<slug>[bot]`）：测试里不会形成循环。
2. 只有写权限的成员能触发，按 `GET /repos/{owner}/{repo}/collaborators/{username}/permission` 查，不信 `author_association`。没有写权限的评论不启动 Task，也不回复（不给刷屏的机会）。
3. 一条触发评论只授权一次推送与一个 PR：同一个 Task 第二次推送、或别的评论想复用这次授权，都被拒。
4. 改动包含 `.github/workflows/` 时，第一次推送之前停下，在 PR 或 Issue 里列出改了哪些工作流文件并请求确认；只有触发者本人（或另一位有写权限的成员）的确认评论能放行；没有确认时不推，超时后在评论里说明并结束。
5. Issue、评论、PR 与代码内容都作为数据传给 Task，不进系统提示；带注入样本的回归进评测集。
6. 单独的预算与限流（每仓库、每小时），超了不启动并在 PR 上说明。
7. fork 来的 PR 不用安装令牌跑它的代码。
8. 推送仍只到 `agent/**`，默认分支推不了（Runtime 的租户策略）。

## 5. 人定的（2026-11-03）

| # | 问题 | 决定 |
| --- | --- | --- |
| 1 | 开 PR 用谁的令牌 | 用户令牌，PR 记在这个人名下；安装令牌留给克隆、推送与自动触发 |
| 2 | App 可以装在哪些账号 | 测试 App 只装本人账号；正式 App（`coding-agent-link`）谁都能装 |
| 3 | 旧 OAuth 用户怎么切 | 并存 30 天：新连接走 App，旧连接照用并提示，到期后删除旧令牌与旧路径 |
| 4 | 给不给 `workflows` 权限 | 给（App 已有 Workflows 读写）。改工作流文件要人确认过：对话里由人确认；自动触发时在 PR 或 Issue 里再问一次（G-06） |
| 5 | 收件箱放哪儿 | Postgres，不用 JSON 文件（第 2 节第 7 条、第 3 节） |
| 6 | 数据保留多久 | 原始负载 7 天；安装记录保留到卸载后 30 天；令牌卸载后立即删除 |
| 7 | 自动触发与“推送由人点按钮”这条规定冲突 | 作废：推送与改工作流不再要求点按钮，人在对话里确认过就可以代为操作；自动触发按 G-06 的授权规则。本仓库 CLAUDE.md 里“由人点按钮触发”那句要跟着改（人改） |

## 6. App 的设置

已注册（2026-11-03）：`coding-agent-link`，App ID 5542758，Client ID `Iv23liBSYi00ZcKTWpxk`，所有者 Hanwen-Jiang。

GitHub 上的实际设置（`gh api apps/coding-agent-link`，2026-11-03）与原计划的差别：

| 项 | 实际 | 原计划 | 要做的 |
| --- | --- | --- | --- |
| 仓库权限 | Contents 读写、Metadata 只读、Pull requests 读写、Workflows 读写 | 另有 Actions 只读、Commit statuses 只读，没有 Workflows | G-04 显示 CI 状态前核对缺 Actions、Commit statuses 时 `workflow_run` 与状态接口能不能用，不能就加上 |
| 账号权限 | Email addresses 只读 | — | 正好给开放注册的防滥用（有验证过的邮箱）用 |
| 订阅事件 | create、delete、pull_request、pull_request_review、pull_request_review_comment、pull_request_review_thread、push、repository、workflow_run | repository、push、pull_request、pull_request_review、workflow_run | G-06 的触发评论（Issue 与 PR 的普通评论）是 `issue_comment`，现在没订阅，要加 Issues 读写与 `issue_comment` 事件 |

加权限要每个安装重新同意，所以 G-04、G-06 要的权限最好一次加齐。

注册时用的设置：

- 回调：`https://code.cgent.link/api/auth/github/callback`。打开 Expire user authorization tokens 与安装时请求用户授权。
- Setup URL：`https://code.cgent.link/api/github/setup`，打开 Redirect on update。
- Webhook：`https://code.cgent.link/api/webhooks/github`，配 secret（至少 32 字节的随机值）。
- 仓库权限：Contents 读写、Metadata 只读、Pull requests 读写、Actions 只读、Commit statuses 只读。Issues 与评论等 G-06 再加（改权限要用户重新同意）。
- 订阅事件：`repository`、`push`、`pull_request`、`pull_request_review`、`workflow_run`（`installation`、`installation_repositories` 不用选）。

密钥只进 SOPS 文件。`$env:WEBHOOK_SECRET` 这类环境变量只在当前终端有效，线上读不到；Client secret 与私钥也不要贴进聊天或工单。

## 7. 依据

查过官方文档的（2026-10-30）：响应 10 秒、不自动重投、重投时 delivery 不变、保留 3 天、负载上限 25 MB；用户令牌 8 小时、续期令牌 6 个月且一次性；安装令牌 1 小时、最多指定 500 个仓库、`DELETE /installation/token`；JWT 的要求；Setup URL 的 `installation_id` 可能被伪造；`/user/installations` 系列只返回用户有权限的内容。

没有逐条查、实现时要核对的：`github_app_authorization` 事件、`workflows` 权限的报错、`installation` 与 `installation_repositories` 是否默认投递、各权限的准确名字。
