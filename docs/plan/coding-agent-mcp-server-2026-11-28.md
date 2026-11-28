# Coding Agent MCP Server 实施计划（2026-11-28）

> 状态：草案（已规划待实施），2026-11-28。
> 总览方案：`agent-runtime/docs/plan/mcp-servers-platform-plan-2026-11-28.md`。
> 目标：在 `coding-web`（Go / Gin）中增加标准 Streamable HTTP **MCP Server** 操作单元（`/mcp`），将编码工作台核心能力对外暴露为可被 Claude Code、Antigravity、Codex 等外部 Agent 客户端安全遥控的标准工具集。

---

## 1. 模块定位与架构

```text
External AI Client (Claude Code / Antigravity / Codex)
        │  Streamable HTTP (JSON-RPC 2.0, 2025-06-18)
        │  Authorization: Bearer <token>  /  Mcp-Session-Id
        ▼
coding-web: POST/DELETE /mcp   (internal/server/mcp_server.go)
        ├── 身份校验与租户绑定（复用 identity / JWT / API Token）
        ├── 幂等键检查与并发防重（复用 singleflight / Valkey）
        └── 委托给既有业务能力：
              ├── Project / Session Store & workspaceHTTP
              ├── Agent Runtime TaskService / WorkspaceService (gRPC)
              └── GitHub App / Draft PR 交付模块
```

---

## 2. 暴露工具与既有代码映射

| Tool 名称 | Annotations | 对应现有实现 | 备注 |
| --- | --- | --- | --- |
| `list_projects` | `readOnlyHint: true` | `store.go` 项目查询 | 仅返回当前 `identity` 所属项目 |
| `list_sessions` | `readOnlyHint: true` | `store.go` 会话查询 | 按 `project_id` 过滤并校验归属 |
| `create_session` | `readOnlyHint: false, idempotentHint: true` | `workspaceHTTP("create_session")` | 透传 `_meta["agentruntime/idempotency_key"]` |
| `submit_task` | `readOnlyHint: false` | Runtime `SubmitTask` 客户端 | 支持可选 `effect_ceiling`（只降不升） |
| `get_task_status` | `readOnlyHint: true` | Runtime `GetTask` 客户端 | 返回阶段、工具摘要、耗时与成本 |
| `get_workspace_diff` | `readOnlyHint: true` | Runtime Workspace `GetDiff` | 支持按路径前缀过滤与分块截断 |
| `list_workspace_files` | `readOnlyHint: true` | `handleFileWorkbench` 只读路径 | 仅浏览当前会话工作区 |
| `create_draft_pr` | `readOnlyHint: false, destructiveHint: true` | GitHub App / Workspace Push | 需显式启用且强制校验幂等键 |

---

## 3. 代码改动清单

1. **新增 `internal/server/mcp_server.go`**：
   - 实现 JSON-RPC 2.0 请求解析、错误码映射（`-32700` / `-32600` / `-32601` / `-32602`）、`initialize` / `tools/list` / `tools/call` 调度与 `Mcp-Session-Id` 会话管理；
   - 定义 8 个 Tool 的 JSON Schema、Annotations 及统一 `structuredContent` 输出包装；
   - 接入统一鉴权中间件（支持用户 API Token / JWT）与 Valkey 频控。
2. **更新 `internal/server/routes.go`**：
   - 挂载 `POST /mcp` 与 `DELETE /mcp` 路由（由配置开关 `CODING_MCP_SERVER_ENABLED` 控制，默认开启或按环境配置）。
3. **新增 `internal/server/mcp_server_test.go`**：
   - 协议握手与错误码单测；
   - 未鉴权 `401` 与跨租户越权 `not_found` 隔离测试；
   - `create_session` / `submit_task` 同幂等键并发调用 `-race` 测试；
   - `get_task_status` / `get_workspace_diff` 结构化输出断言。

---

## 4. 验收与上线检查

1. `go test -race ./internal/server -run TestMCPServer` 全绿，`GOOS=linux GOARCH=amd64 go build ./cmd/coding-web` 通过。
2. 线上 Traefik 路由透传 `/mcp`，确认 `POST https://code.cgent.link/mcp` 无令牌返回 `401`，带合法令牌成功返回 `tools/list`。
3. 使用 Claude Code 与 Antigravity 实测配置 `https://code.cgent.link/mcp`，跑通 `list_projects → list_sessions → get_workspace_diff` 全流程。
