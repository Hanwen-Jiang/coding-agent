# 20项Goal中的 coding-web 交付

生产镜像：`ghcr.io/agentruntime/coding-web:goal-0fbf18e`。统一交付以
[Runtime最终20项交付结果](https://github.com/Hanwen-Jiang/agent-runtime/blob/main/docs/plan/full-delivery-2026-11-08.md)
记录全部状态和证据。实现项已部署验收；N16-R/EV-REAL原11对及统一22份评分、Memory50题已完成，但门槛未过和费用未知均保留。EV-TB新可比40题、EV-DE新轨迹重判仍受外部历史账单阻塞，总Goal未全部完成。

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

最终收尾（2026-11-08）：两仓所有相关代码/正式报告统一合入main并推送，作者和提交者邮箱使用haven-just@qq.com；未重写已推历史。本次paired的8个Deployment、3个Service、3个ConfigMap和临时Task API令牌已按原UID清理，专用Pod为0，68个本次sandbox环境/目录destroyed且busy为0；生产与共享资源、所有账本/历史/原始结果保留。coding-web生产镜像仍为goal-0fbf18e，此后变化仅为评测工具和交付记录，无需滚动网页服务。

本轮相关可核模型账本/Jev成本为4.686462294美元，5笔未知预占0.331718美元另列；完整供应商实付和基础设施账单不伪造零值。具体费用范围、负面结果及TB/DeepEval所缺历史记录详见Runtime最终报告。实际生产组件清单、回退点和最终主线SHA/远端核对保存在宿主`/root/ar-release/goal-full-20261106/final-delivery-state.json`及同目录回退证据中。
