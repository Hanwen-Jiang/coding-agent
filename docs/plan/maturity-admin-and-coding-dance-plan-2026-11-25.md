# Coding-Agent Wave 1–3 Maturity Plan: `#/admin` Console & `<coding-dance>` Companion Integration

- 关联总纲：`agent-runtime/docs/plan/maturity-and-self-evolution-dag-2026-11-25.md`
- 作用域：`coding-agent`（Go Web BFF / Entry Adapters / Embedded Console UI）
- 目标节点：`MAT-ADMIN-UI`、`CD-ENTRY-V1`

---

## 1. Wave 1 对齐说明

Wave 1 的核心工具扩展（`repo_glob`、`repo_git`、`apply_patch`、`todo_write`、`file_inspect`、`memory_search`、`web_search`）、统一系统提示词 `SYSTEM_PROMPT` V2 以及 10 个平台 Skill 主要落在 `agent-runtime` Worker 与 Registry 中。`coding-agent` 保持现有前端工具调用渲染器与 `runtime` 代理契约兼容，待 Wave 3 启动时基于稳定的工具与 Eval 底座统一交付管理面板与 `<coding-dance>` 接入点。

---

## 2. Wave 3-A：`MAT-ADMIN-UI`（管理员可观测与自进化控制台）

### 2.1 RBAC 与路由契约
- 新增配置项 `CODING_ADMINS`（逗号分隔的管理员 user ID 列表或 OIDC role 映射）；未配置或非管理员访问一律返回 `403 Forbidden`，不在普通用户导航栏露出入口。
- 前端在 `coding-web` 内嵌 `#/admin` hash 路由（独立视图组件，与用户工作区状态完全隔离）。

### 2.2 后端 BFF 聚合接口（`/api/admin/v1/*`）
1. `GET /api/admin/v1/telemetry/summary`：聚合 OTel Trace 错误率、P95 工具延时、VictoriaMetrics Worker QPS 与沙箱冷启动分布，并返回配好权限的 Grafana 深度面板链接。
2. `GET /api/admin/v1/evolution/candidates`：读取 `agent-runtime` 中 `sys-evolution` 隔离租户与平台作用域 `EvolutionStore`（`evolution_candidates`）产出的 `pending_approval` / `promoted` / `rolled_back` Skill/Prompt 更新包，展示基线 vs 候选 Matrix（SWE / DocEval）对比分数与完整 Unified Diff。
3. `POST /api/admin/v1/evolution/candidates/{id}/promote`：经管理员显式确认后调用 `EvolutionStore.approve_and_promote`，将 `cand-*` 候选项提升为平台默认版本（并保留基线快照）。
4. `POST /api/admin/v1/evolution/candidates/{id}/rollback`：调用 `EvolutionStore.rollback` 一键回滚至上一个已记录基线版本并写入审计日志。

---

## 3. Wave 3-B：`CD-ENTRY-V1`（`<coding-dance>` 双通道合拍伴侣接入）

### 3.1 双通道架构
- **异步入口通道**：新增 `internal/entry/codingdance`（复用 `internal/entry` 的 `Kind` + `Sink` + 签名校验框架），接收 `<coding-dance>` 训练事件、代码提交事件与卡顿信号，映射为受控 Runtime Task 并回写结果。
- **实时交互接口**：暴露 `/api/integrations/coding-dance/v1/*`：
  - `POST /api/integrations/coding-dance/v1/hint-check`：检查当前做题/编码上下文是否存在高置信建议，严格按 `consent_mode` 返回结果；
  - `POST /api/integrations/coding-dance/v1/hint-reveal`：在用户点击同意并选择提示等级后，才调用 Agent/Runtime 生成具体建议；
  - `POST /api/integrations/coding-dance/v1/history-analyze`：结合用户历史提交与做题记录生成代码优化对照、盲点诊断与个性化巩固练习提案。

### 3.2 Consent-First 状态机（硬性产品约束）

| 模式 (`consent_mode`) | 行为规范 |
| --- | --- |
| `off` | 完全静默。禁止主动触发任何 AI 分析、禁止展示主动弹窗或提示角标。 |
| `on_demand` | 仅在用户主动点击“需要提示”或选择某档提示等级时调用后端生成对应层级反馈。 |
| `proactive_consent` | 检测到明显卡点或高价值优化机会时，**只允许返回不含答案、不含具体思路剧透的征询文案**（例如：“发现一个可能的卡点 / 我有个小想法，需要现在提醒你吗？”）。仅当用户显式点击确认并确认档位后，才透出对应内容。 |

### 3.3 渐进式提示等级（Hint Levels `L1`–`L4`）
- `L1`（方向提问）：只提出一个引导思考的关键问题，不点破算法/结构。
- `L2`（切入点与思路线索）：指出关键约束、数据结构选择或容易忽略的边界，不写代码。
- `L3`（伪代码 / 局部框架）：给出关键步骤骨架与复杂度提示，仍由用户补全实现。
- `L4`（完整分析与对照示例）：展示完整修复思路、优化后对比与单元测试验证建议。

---

## 4. Wave 3 落地与验收记录（2026-11-26）

- **后端运维与进化治理（`internal/adminops/*`、`internal/server/admin.go`、`cmd/coding-web/main.go`）**：
  - 实现 `coding-web admin-hash` 标准输入子命令与加盐 `scrypt` 认证、IP 级防爆破限流、`cw_admin` 会话及 `CODING_ADMINS` RBAC 双路放行。
  - 实现 VictoriaMetrics + Grafana + Task API + Registry 内部端口（`8092`）状态聚合，以及基于 `BindPlatform("evolution")`（迁移 `301`）的候选导入、审批晋升（`Promote`）与基线回滚（`Rollback`）。
- **独立 `#/admin` 前端与双语/CSP 约束（`internal/web/static/{index.html,admin-ui.css,admin-ui.js,entry-ui.js,i18n.js}`）**：
  - 保持现有工作区 `app.js` 零侵入；全部 `style-src 'self'; script-src 'self'` 无内联脚本样式；`ZH`/`EN` 各 `1076` 键完整对齐；后端 `TestEveryMessageHasEnglish` 全绿。
- **`<coding-dance>` 双通道（`internal/entry/codingdance/*`、`internal/server/coding_dance.go`）**：
  - 注册 `coding_dance` 入口适配器并暴露 `/api/integrations/coding-dance/v1/{hint-check,hint-reveal,history-analyze}`，HMAC `consent_token` 与 `L1`–`L4` 渐进提示全部通过 `kind_test.go` 与 `TestWave3CodingDanceCompanionAPI`。
