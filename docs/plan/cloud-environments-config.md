# Coding Agent 云环境配置切片

这次切片把“环境配置”和 Git 工作区（`Project`）分开。环境是租户内由用户创建的独立配置，创建会话时通过 `environment_id` 选择；同一个环境可以被该用户在多个项目和会话中复用。

## 已实现的契约

- `GET/POST /api/environments`：列出或创建当前用户的环境。
- `GET/PUT/PATCH/DELETE /api/environments/{id}`：读取、替换或删除环境配置。删除写墓碑，列表和读取不再返回；有会话在用也可以删（见下面“删除与快照”）；Runtime 后续负责异步回收其运行资源。
- 环境字段：`name`、`description`（最多 4 KiB）、`privacy`（当前固定 `only_me`）、`setup_script`（最多 64 KiB）、`start_command`（最多 64 KiB）和普通 `.env` 文本 `env`（最多 16 KiB、64 个变量，与 Runtime 的 `SetWorkspaceEnvironment` 一致）。
- `.env` 每个非空、非注释行必须是 `KEY=value`，键名符合环境变量规则且不能重复。明显的 `TOKEN`、`SECRET`、`PASSWORD`、`APIKEY` 等机密键名会被拒绝；网络密钥必须走 Runtime 的 SecretStore/代理。
- 同一用户的活动环境名不区分大小写且必须唯一。`only_me` 由 API 层按创建者过滤，其他用户无法读取、修改或绑定。
- `POST /api/projects/{project}/sessions` 可选 `environment_id`。服务端校验配置归属和未删除，记录引用，并记下这份配置此刻的快照（见下）；项目仓库与环境配置没有持久化耦合。
- `.env` 的密钥 `secrets`（名字 + 值）只写不读：值按（租户、用户、环境、名字）AES-GCM 加密存在 coding-web 的服务端，响应里只有名字；没给值的名字保留原来的值，不带这个字段时不动。普通 `.env` 里像机密的名字被拒绝，提示放到密钥里。配置带 `version`（每次修改加一）与 `updated_at`。

## 删除与快照（2026-11-03 人定）

原来的问题：有会话在用的环境配置，删除前要不要先改绑（Runtime 侧草案）还是直接写墓碑（人工验收 Q35）。决定：

- 开会话时把选定配置的非机密设置（安装脚本、启动命令、普通变量、联网模式与允许的域名）连同配置 id、`version`、`updated_at` 与密钥的名字记成会话的快照（`env-snapshot-<workspace>.json`，不进会话的 JSON）。会话从此只用快照：之后改配置不影响已开的会话（编辑框里写明）。复制会话沿用原会话的快照。
- 删除不要求先改绑，有会话在用也可以删；删掉的配置从“环境”列表与新会话的选择里消失。确认框说明还有几个会话在用：它们保留创建时的设置，但 `.env` 里的密钥立即删除，之后启动的沙箱拿不到；没有会话在用时是普通的确认。
- 删除时密钥立即删掉（常常是因为密钥泄露才删）：墓碑里不留加密后的值，浏览器与日志里本来就没有。快照从不含密钥，所以用它的会话从此没有密钥。右栏写“这个环境已删除，本会话用的是创建时的设置；密钥已删除”。
- 交给 Runtime 的接缝（`environmentHandoff`，ENV-02）收的是会话的快照，配置还在时再加上快照里列着名字的密钥（轮换过的取新值）；删掉之后没有密钥（`environmentHandoffInput`）。
- 密钥现在暂存在 coding-web 的服务端；Runtime 的个人密钥管理（Runtime 仓库 `docs/plan/environment-config.md` 第 4.2 节，SecretStore）就绪后改为交给它，coding-web 只留引用。

## 边界

本切片只保存和校验配置，不执行安装/启动脚本，也不把普通变量注入进程。`ready`、`reused`、`expiry`、健康状态和网络代理结果必须来自 Runtime 的 `EnvRecord`/EnvironmentService；coding-web 不从日志、HTTP 健康检查或本地 JSON 推断这些状态。

后续 Runtime 接入需要把 `Session.EnvironmentID` 转换成 EnvironmentService 请求，并在 `/env` 里投影 Runtime 的权威记录。网络模式、允许域名、OIDC/VPN、网络密钥与脚本缓存属于 Runtime 侧实现，不能在本配置存储里复制一份状态机。

## 界面与 /env（2026-11-03）

- 网页：左栏“环境”（`#/environments`）列出、新建、编辑、删除自己的环境配置；字段是名称、描述、安装脚本、启动命令、`.env` 与联网需求
  `network`（`deny` 默认、`allowlist` + `allowed_hosts`（`*.example.com` 匹配子域名，不收 IP、裸 `*`、协议、端口、路径）、`open`）。
  联网需求只是配置，放行与否由 Runtime 按租户策略核对（只能收窄）。UTF-8 字节上限前后端同一个数。
- 新会话：输入框下的“环境”在草稿时选（默认环境或一份配置），开出会话后固定。复制会话沿用原会话的快照（配置删掉了也照用，没有密钥）。
- 交给 Runtime：接缝 `environmentHandoff`（`internal/server/envhandoff.go`），输入是会话的快照加（配置还在时的）密钥。ENV-02 的契约
  定下之前不发任何请求，会话记 `environment_handoff=pending`，界面写明“未交给 Runtime”。契约定下后只换这一个实现。
- `/env`：只读投影，不再调 Workspace API（那会借沙箱、动环境的生命周期；分支与改动网页从“改动”取）。沙箱环境经 task-service
  上只读的 EnvironmentService（`ListEnvironments`，按工作副本与 Runtime Session 限定）取：就绪只看 `ready`，复用、健康确认、
  空闲到期、最长到原样显示，没有的写“未知”；还没有 Runtime Session 时 `unknown`；服务连不上、没有或说暂时读不到时
  `blocked_external`；别的拒绝 `unknown`。不认识的状态是 `unknown`。
