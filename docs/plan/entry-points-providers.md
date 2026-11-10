# 接入点类型与邮件服务配置

实现：`internal/entry/{githubcomment,schedule,alert,email}`；来源核对日期：2026-11-10。连接的入站 Webhook 地址由 API 的 `webhook_url` 给出：`POST /api/webhooks/entry/{connection}`。

## GitHub 评论

连接配置 `mention` 是触发别名，默认 `coding-agent`，填写时不带 `@`，允许字母、数字、短横线，最多 39 个字符；无须改 GitHub App 的实际名称。

例如设为 `coding-agent`，评论 `@coding-agent 你来做xxx` 才触发；`@coding-agent-other` 不匹配。名称不分大小写；名称后接受空白、中英文冒号或逗号。确认评论也用相同别名：`@coding-agent confirm` 或 `@coding-agent 确认`。Bot 过滤仍按 GitHub 的 `sender.type` 和实际 App slug，不能由别名绕过。

可选 `keywords` 为必须包含的文字。权限、已关联账号、仓库、fork 和 workflow 确认仍由应用核对，解析器不代替授权。

## 定时任务

`cron` 使用 UTC 的五段数字格式：分钟、小时、日、月、星期。支持 `*`、数字、范围、列表与步长；不接受本地时区或别名。限制至少一小时一次，因此分钟字段只允许一个实际值。

- `0 * * * *`：每小时整点。
- `30 1 * * *`：每日 UTC 01:30，即北京时间 09:30。
- `0 0 * * 1-5`：工作日 UTC 零点。

日与星期同时受限时遵循 cron 的“任一匹配”；其中一个用通配时遵循两字段同时匹配。重启只取当前时刻之前最近一次计划点；连接创建前的计划不补。触发 ID 含连接 ID 和计划时间，重复扫描不再起一个任务。连接有未结束运行时，由持久化准入层记录跳过。

## Alertmanager 与 Grafana

使用各自默认 JSON 格式：`alerts` 数组，每项有 `status`、`fingerprint`、RFC3339 的 `startsAt`、`labels`、`annotations`。只处理 `firing`；`resolved` 不触发。单次不超过 100 条告警，负载不超过 2 MiB。触发 ID 固定于连接、fingerprint、startsAt；额外的 `dedup_minutes` 限制同一 fingerprint 在窗口内只起一次任务，默认 60 分钟。

配置项 `severity` 精确匹配 `labels.severity`，空值不限；`match_labels` 使用 `env=prod,team=backend` 形式，所有条件同时满足。`instruction` 是用户写的分析指令；外部告警 JSON 只作为资料，不能覆盖指令。默认 `autonomy=analyze`，只有连接明确选择其他自主程度后才放宽。

服务端先核对 `Authorization: Bearer <连接密钥>` 的 SHA-256，再读取和解析请求体；密钥只在生成时返回，页面及后续 API 都不再显示。使用 TLS 传送。若需要重配，生成新密钥后同步更新发送方。

Alertmanager 配置示例（密钥放入独立文件）：

```yaml
receivers:
  - name: coding-agent
    webhook_configs:
      - url: https://YOUR_HOST/api/webhooks/entry/CONNECTION_ID
        send_resolved: false
        http_config:
          authorization:
            type: Bearer
            credentials_file: /etc/alertmanager/coding-entry-token
```

Grafana：Webhook 联系点 → Authorization Header Scheme 填 `Bearer`，Credentials 填连接密钥；保留默认 payload，禁用 resolved 通知。Grafana 自定义 JSON 模板必须仍输出上述字段。此适配器使用认证头，不启用 Grafana 的 HMAC 模式（仅存密钥哈希无法重新计算 HMAC）。

官方依据：[Alertmanager 配置](https://prometheus.io/docs/alerting/latest/configuration/)、[Grafana Webhook](https://grafana.com/docs/grafana/latest/alerting/configure-notifications/manage-contact-points/integrations/webhook-notifier/)。

## 邮件入站（Webhook）

已选方案是入站邮件服务推送。服务商适配层在其信任边界内完成 SPF/DKIM/DMARC 验证，再将结果和邮件转换为下列 JSON，携带连接 Bearer 密钥发送；需要配置实际收信域名或地址。`recipients` 是连接允许接收的地址，逗号分隔。

```json
{
  "message_id": "<unique-message-id@example.org>",
  "from": "verified-owner@example.org",
  "to": ["agent@YOUR_INBOUND_DOMAIN"],
  "subject": "检查最近的构建失败",
  "text": "请解释错误并给出修复建议。",
  "dmarc": "pass",
  "headers": {"Auto-Submitted": "no"},
  "attachments": [
    {"name": "context.txt", "content_type": "text/plain", "data": "aGVsbG8="}
  ]
}
```

`to` 也可以是地址字符串。附件 `data` 是 Base64；最多 8 个、解码后合计不超过 4 MiB，整个 JSON 不超过 6 MiB。另一种格式为 `{ "raw_mime": "完整 RFC 5322 邮件", "dmarc": "pass" }`；`raw_mime` 是 JSON 字符串，不是 Base64。MIME 支持 multipart、Base64、quoted-printable，文本部分接受 UTF-8/US-ASCII。附件通过现有 Runtime File API 进入所属会话，固定版本后作为 Task attachments 提交，重试按相同名称和内容去重。

DMARC 必须来自已认证服务商的顶层 `dmarc` 字段；邮件正文或原始邮件的 `Authentication-Results` 头不能证明认证成功。实际发件人还必须匹配连接主人在账号中验证过的邮箱。无匹配账号、DMARC 不通过、自动回复邮件或收件地址不匹配时，既不起任务也不回信。不会因 `Reply-To` 指定任意第三方而将回复转发出去。

服务商必须移除不可信的认证结果并自行验证；仅把用户提交的 `dmarc` 或 MIME 头原样转发不是合格的适配层。具体服务商、域名和凭据由运营配置提供，不假定某一家，也不从个人账号搜集。

## 邮件出口

启动配置：`CODING_ENTRY_DSN` 指向 GitHub 收件箱同一个 Postgres 库；未设时沿用 `CODING_GITHUB_DSN`。两者都没有则关闭接入点。仅在本地开发登录且没有 JWT 时，可以显式设 `CODING_ENTRY_MEMORY=1` 做非持久验收；生产配置拒绝该选项。

SMTP 环境变量为 `CODING_SMTP_ADDR`、`CODING_SMTP_FROM`、`CODING_SMTP_USERNAME`、`CODING_SMTP_PASSWORD`、`CODING_SMTP_IMPLICIT_TLS`。全空则关闭邮件出口；ADDR 与 FROM 必须同时给，用户名/密码必须成对。Deployment 可从可选的 Secret `coding-entry` 注入，禁止将密码写入版本库。启动在对外服务前迁移旧 GitHub 触发记录；接入点调度器独立于 GitHub App 运行。

邮件出口接现有 SMTP 服务；标准库实现，无新增依赖。`SMTPConfig` 需要 `Addr`（host:port）、`From`、可选成对的 `Username`/`Password`；`ImplicitTLS=true` 使用 TLS 直连（常用 465），否则要求 STARTTLS（常用 587）。不允许降级明文，校验服务器证书，最低 TLS 1.2。

邮件使用确定的 Message-ID（由投递幂等键生成），回复保留 `In-Reply-To` 和 `References`，添加 `Auto-Submitted: auto-replied` 与 `X-Auto-Response-Suppress: All`。单封仅向连接明确配置的一个地址或经过验证的原始发件人投递。

SMTP 在 DATA 完成后失去确认可能已发送成功：标为 `unknown`，不自动重发。收到明确的拒绝则可进入发件箱正常重试。已收到成功确认后 QUIT 失败不把成功改为失败。进程恢复遇到 SMTP 的不明尝试，同样不能盲重发。

依据：[RFC 5321（SMTP DATA 超时）](https://datatracker.ietf.org/doc/html/rfc5321/)、[RFC 3834（自动回复）](https://www.rfc-editor.org/rfc/rfc3834.html)、[RFC 8601（认证结果信任边界）](https://www.rfc-editor.org/info/rfc8601/)。

## 适配器验证

2026-11-10：`go test ./internal/entry/...` 和 `go vet ./internal/entry/...` 均通过。另以临时程序完成 24 项确定性检查：cron 频率边界、闰日、最近一次补跑、可配置别名与边界/确认、验签前拒绝、告警恢复过滤与稳定 ID、资料隔离、DMARC/已验证发件人与自动回复过滤、附件指令隔离、线程头、出站循环抑制、主题与 Message-ID 注入。临时程序已删除，没有向真实邮件地址发送验证信。

以上仅记录类型适配层验证；持久化并发、Task 提交、网页和真实服务商联通需要主流程的集成验收。

随后使用临时 HTTP 验收程序（启动真实 coding-web handler 与隔离的 Runtime/GitHub 替身）验证：POST 强制禁用；PUT 局部更新保留配置；零限额、过密 cron、错误标签拒绝；缺密钥时在 JSON 解析前返回 401；禁用连接拒绝留痕；同指纹时间窗去重；列表不泄露密钥；跨租户写入/历史访问拒绝；无 SMTP 时允许保存禁用邮件连接但不能启用邮件出口；已验证邮箱接受、DMARC 失败/未验证邮箱/自动回复静默丢弃。全部通过，临时程序已删除。

SMTP 另通过本机 TLS 回环服务器验收：证书验证开启、成功收取 DATA、发送后丢失确认、明确 550 拒绝三条路径分别得到成功、`unknown`、明确失败；同时核对线程头与自动回复头。自签根仅注入临时测试进程，未修改系统信任、未放宽生产 TLS 验证；没有真实邮件发出。最初夹具在 macOS 未正确注入测试根，修正夹具初始化顺序后全部通过。

结果投递读取 `ListSessionEvents` 的 `TASK_FINISHED.payload.value`（`TaskWorkflowResult.deliverable.value`），不把 `TaskState` 当最终答案。取 QueryTask 游标末尾 1000 个事件并固定快照上界；保留完整正文、中止原因与部分结果，严格忽略 thinking/token/tool 内容；仍待回复的问题按网页相同的 quiet-event 规则识别。只有旧 Runtime 的 404/unimplemented 走兼容后备，真实上游错误、缺终态、错任务/用户不被吞掉。临时真实 HTTP 协议验证覆盖超过 10000 字的最终答案、abort、failed wrapper、有效/已答/撤回问题、上游失败、兼容路径及身份隔离，全部通过并删除夹具。
