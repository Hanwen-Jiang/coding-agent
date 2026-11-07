# 20项Goal中的 coding-web 交付

生产镜像：`ghcr.io/agentruntime/coding-web:goal-0fbf18e`。统一交付以
[Runtime执行记录](https://github.com/Hanwen-Jiang/agent-runtime/blob/main/docs/plan/goal-full-20-2026-11-07.md)
记录全部20项状态；网页实现与生产验收完成不代表尚在运行或受费用限制的评测已经完成。

- D-25：多仓工作区入口、逐仓操作、只读提交拒绝、复制与卸载；dirty detach默认拒绝，明确discard才丢弃。凭据按现有平台授权逐仓取得，不放入仓库。
- NET-OPEN：按租户策略请求本Task的公网授权，保留范围、时限、撤销、配额及内网/元数据地址拦截。
- WS-CF-524：浏览器变更请求先返回202和持久操作号，随后短请求轮询；重复请求、断连、服务重启恢复和失败查询保留原结果及归属检查。
- D-27：个人配置编辑、不可变版本/digest、历史no-store、默认引用及会话私有选择；账号切换不会沿用另一账号的默认环境或工具选择。
- WK-LITE-02：真实交付文件经网页列表和下载路由取得，SHA核验与安全响应头通过；同租户共享会话按既有权限可读，跨租户及错误Task返回404。

验证：完整Web server套件、关键race检查、中英多仓Chromium流程、个人配置/账号隔离Chromium流程均通过。
生产Web E八组验证在原Kata策略下通过，异步受理最大51.262ms；包括session和duplicate复制、只读403、dirty默认409及显式discard200。
独立复核活动测试workspace/env/model_usage均为0、租户对象为空、测试项目/会话已删、个人默认已退役。
不可变配置历史和操作审计有意保留，三个测试用户的浏览器会话已撤销，私有Cookie文件已删除。无模型调用。

完整原始失败对照、最终报告、截图、复现脚本及SHA索引位于Runtime的
`docs/plan/evidence/production-smoke-20261107/`；历史空清单/临时块上传/Taskless审计缺口均修复后按原条件复验，没有弱化断言。

回退与最终版本清单在宿主 `/root/ar-release/goal-full-20261106/`。
部署采用既有本机镜像构建与K3s导入流程；Git代码按普通合并推送，不force。
