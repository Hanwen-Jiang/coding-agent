# 文件工作台集成

React/HeroUI Pro 文件工作台位于 `ui/chat/file-workbench.tsx`，从来源面板或文件项打开。支持预览、读取选区、结构/引用、SQL、表格与文档编辑表单、多文件检索集合、候选发布、版本下载、权限与回收站。动画遵循 reduced-motion。

`internal/server/file_workbench.go` 将请求限定在已鉴权的会话 workspace，并代理 Runtime 文件/检索 API。权限由 Runtime `details.permissions` 决定；缺权限字段时管理按钮保持禁用。客户端没有独立文件权威或隐式执行引擎。原件引用和已发布版本保持不可变。

依赖 Runtime 的 `codex/file-intelligence-m0` 集成实现（`33465f99` 或之后版本）；只发布前端而未部署后端/引擎时会显示真实不可用错误。对应 Runtime 文档：`docs/plan/file-intelligence-integration.md`。Linux file-engines/UNO、真实 Temporal 恢复验收、完整规模评测尚有发布门，当前提交不代表生产上线。

验收：`internal/server` 与 `internal/web` Go/真实 Chrome 测试通过，UI 手工交互见 `docs/evidence/file-workbench/README.md`。截图明确标为合成 fixture，实际处理结果与权限/版本并发验证位于 Runtime 仓库。
