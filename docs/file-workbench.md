# 文件工作台集成

React/HeroUI Pro 文件工作台位于 `ui/chat/file-workbench.tsx`，从来源面板或文件项打开。支持预览、读取选区、结构/引用、SQL、表格与文档编辑表单、多文件检索集合、候选发布、版本下载、权限与回收站。动画遵循 reduced-motion。

`internal/server/file_workbench.go` 将请求限定在已鉴权的会话 workspace，并代理 Runtime 文件/检索 API。权限由 Runtime `details.permissions` 决定；缺权限字段时管理按钮保持禁用。客户端没有独立文件权威或隐式执行引擎。原件引用和已发布版本保持不可变。

## 当前界面范围

文件工作台现分为文件、检索集合、回收站三个入口，文件内按预览、结构与原文、计算与编辑、版本与权限组织。按钮随真实文件类型与权限显示；Office 上传被探测成 ZIP 时仍按扩展名呈现对应候选功能，后端继续按内容验证。

本轮补齐工作台内上传/重试/筛选、PDF 与 Office 页图导航、图片区域框选和 OCR、Notebook 显式选中 cells 执行及安全文本输出预览、归档成员选择、跨文件 SQL 的逐源范围、Office/Notebook 编辑表单、L1/L2 核验和候选审核。详细的补齐前后矩阵见 [UI 覆盖清单](ui/file-intelligence-ui-coverage.md)。

“我的候选修改”只展示当前用户可读取的 job；共享文件的已发布历史版本可依权限查看。发布仍需用户审核、验证通过、当前 head 匹配及服务端写权限。版本元数据对比不冒充任意两份文件的全文或像素差异分析。

## 历史版本与下一条任务

工作台选择历史版本后，“用于下一条任务”保留版本 ID，输入框显示版本说明，提交通过 `source_versions` 指定不可变版本。服务端校验所选版本确实属于该文件和当前会话，并把该版本的对象引用交给 Runtime 固定。一个文件在输入框重复选择时替换其选定版本，不偷偷添加两份。已记录版本的历史任务再次打开时定位同一版本；没有版本元数据的旧任务不伪造历史版本。

## 集合与下一条任务

知识集合可选择最多 500 份已读取资料，实际索引还受 Runtime 的 20 MiB 表示载荷上限限制。索引仅覆盖已读取的页面或选区；文件数量不代表全文已经解析。单 Task 的直接附件仍限制为 10 文件、200 MiB，不随集合容量变化。

“在下一条任务使用此集合”将当前集合加入输入框，显示来源数量，并可在发送前移除。每次任务最多显式选择一个当前会话知识集合。请求携带 `file_collection_ids`；仅使用集合时不携带 `source_ids`，不会把集合展开成原件附件或原生图像授权。Runtime 在提交时固定集合版本，后续重建不改变旧任务的检索范围。选择草稿按账号/会话隔离，发送成功后清除；退出登录后不会保留旧账号选择。

## 异步设置保存

任务发送会等待同一会话正在保存的沙箱/网络设置。保存失败时不发送并保留草稿；等待期间编辑草稿、切换会话或重新登录会取消旧发送。返回同一会话后新发起的发送会使用已确认设置，旧请求不能清掉新草稿。实现用账号与路由代次校验，避免仅比较相同的会话字符串而复活旧等待。

## 验收与部署边界

本次在项目服务器的独立容器中运行真实 Chromium；集合按钮、参数、移除、跨会话和跨账号隔离的证据见 `docs/ui/evidence/collection-task-ui-20261110.json`。设置保存/发送的六种交错情形使用实际应用 JavaScript 与明确的 HTTP fixture，证据见 `docs/ui/evidence/pending-settings-send-20261110.json`。这些浏览器 fixture 不冒充真实文档处理结果。

此前集成版本的服务器完整 `go test ./...` 和 `go vet ./...` 通过：257 项顶层测试（335 条含子测试的通过事件）；默认跳过的只读公开来源集成另外显式开启后通过。140 个实际编译输入逐文件 SHA 与本地一致。完整 Coding Go/浏览器回归与源码校验记录见 `docs/ui/evidence/server-browser-complete-20261110.json`。旧的首响应测试现在观察 DOM 插入顺序，避免把轮询间隔内渲染的旧轮次误判为“先显示”；运行详情测试等待真实 React 组件挂载后仍检查唯一、默认折叠状态，原断言未取消。

本轮新工作台的整页 28 项浏览器 fixture、预览控件、12 项编辑/候选请求、四类异步状态交错和 Notebook 字节/HTML 边界检查分别记录在 [整页证据](ui/evidence/file-ui-parity/README.md)、[预览证据](ui/evidence/file-preview-controls/README.md)、[编辑证据](ui/evidence/file-actions-20261110.md)、[状态交错证据](ui/evidence/file-workbench-state-races/README.md) 和 [Notebook 边界](ui/evidence/notebook-output-bounds/README.md)。实际运行的是 React/HeroUI bundle，数据明确为 synthetic HTTP fixture；真实文档引擎、SQL 与版本授权验证另见 Runtime `docs/plan/evidence/file-ui-contracts/`。新一轮服务器完整回归结果由本次最终交付记录单列，以上旧集成计数不自动适用于这批新增代码。

后端必须配套部署 Runtime 的文件集成与服务器验收修复。Linux 引擎构建、UNO 重算、Temporal 恢复/取消和文件/检索评测证据保存在 Runtime 仓库。完整状态与限制以 `docs/plan/file-intelligence-integration.md` 和最终服务器验收记录为准。代码合并与服务器隔离测试均不表示生产服务已经升级。


## 新功能 UI 补齐验收

新增按格式的预览/结构/计算编辑与候选审核模块，详见 `docs/ui/file-intelligence-ui-coverage.md`。最终工作台 bundle 的 28 项整页交互、图片区域/Notebook 安全输出、候选发布门及四种异步状态竞态均有独立真实 Chromium 验证。历史版本用于任务时显式绑定不可变版本，任务历史也保存服务端实际解析的版本。

本次最终服务器 Coding 全套为 257 项顶层测试、335 条通过事件、0 失败；`go vet` 通过。最新 Runtime 文件/Task API 的 `-race` 与 vet 同样通过。证据位于 `docs/ui/evidence/file-ui-parity/server/`，140 个 Coding、35 个 Runtime 编译输入的本地/服务器 SHA 一致。

全套回归暴露了已打开任务设置弹窗在延迟 React 挂载时因强制工具条刷新失去锚点的问题。现在即使强制刷新，也保留当前弹窗锚点，关闭后再刷新；临时红绿探针及原 unlimited 用例十次复跑通过，没有增加超时或删除断言。该修复不改变网络授权或账号/会话清理行为。
