# 文件新能力 UI 对照与验收

日期：2026-11-10。本记录将后端已有能力与实际界面操作逐项对应；界面交互使用真实 Chromium 加载编译后的 React/HeroUI Pro bundle，HTTP 数据为明确标注的合成 fixture。真实解析/OCR/计算/权限行为另有 Runtime 探针，二者不混为“线上已部署”。

| 能力 | 补齐前缺口 | 本次 UI 与真实 API 对应 |
|---|---|---|
| 文件库 | 上传入口只在对话输入框，工作台缺完整刷新/类型筛选 | 上传、失败重试、文件搜索、类型筛选、限额说明、私有/共享权限显示 |
| 文件类型 | Office 原件常被探测为 ZIP，按 MIME 判断导致编辑/范围入口不可见 | 扩展名决定候选功能；实际解析仍由后端按内容验证，不把文件名当成功证据 |
| PDF / Office 预览 | PDF 页面范围曾允许 50 页；Office 后端仅返回 PDF，没有页图 | 单次最多 10 页；DOCX/XLSX/PPTX 经真实 LibreOffice+Poppler生成带原页/slide锚点的 PNG 及原 PDF；缩略导航与下载 |
| 图片与区域 OCR | 无可视区域选取 | 指针拖动或百分比输入；规范化 bbox；裁剪和 OCR 分开操作；原件不改 |
| 结构与引用 | outline/anchor 主要作为 JSON 展示，无法自然定位 | 标题/页/正文段落/表格/worksheet/shape/cell 可读定位；引用跳转固定版本与选区；页眉/页脚无法精确定位时明确提示预览或下载核对；低置信警告可见 |
| DOCX 范围 | body 中 table 导致 paragraph index 错位 | 保留 block_index，另返回 paragraph_index/table_index；按段落或表格定位，不改旧字段含义 |
| Notebook | 后端可以执行，界面缺选择/确认/结果路径 | 保存的代码与输出只读；选择 code cells 并明确确认后发送 notebook_execute(cell_indices)，原件不执行；输出为候选版本 |
| 归档 | 需要手写成员字符串 | 成员清单 checkbox，显式提取所选成员并下载；目录/链接不作为可执行输入 |
| 跨文件表格 | 单数据源 SQL；额外 XLSX worksheet/range 无法传给引擎 | 1–10 个授权版本、data/data_2 等别名、每个来源独立 selector/header；只读参数化 SQL 与真实返回表格/下载 |
| 文档修改 | 主要只有简单文本表单或 JSON | XLSX 值/公式、DOCX 正文/run/table、PPTX 文字/已上传图片版本、Notebook cell source；高级 JSON 保留为可选 |
| 核验与发布 | 只显示 digest/发布按钮 | 修改前后、验证状态、公式缓存/真实重算区别、L1/L2 与固定允许变化区域；用户审核候选后明确发布，冲突保持可见 |
| 历史与取消 | details 丢掉 inputs/request/consumer，无法准确恢复 | 固定版本/selector 操作历史；重新连接实际 job；只取消当前 actor 的一个活跃 consumer；expired 是终态 |
| 知识集合 | 同一文件多个读取选区不易选择；引用不能回原文 | 选择多个已读取 representation、实际索引修订；检索片段定位固定版本；只检索已覆盖部分 |
| 历史版本用于任务 | UI 选了旧版本，原提交却总解析当前 head | 显式 source_versions 由服务端校验归属，输入框和历史任务保留所选版本；集合仍与直接附件分开授权 |
| 权限与回收站 | 基本功能已有，但弱提示与状态 | 只读禁止编辑/发布/授权；删除先撤权的确认；恢复说明仍为私有，不复活旧共享 |
| 动画与窄屏 | 新区域未整体测量 | 简约状态/展开动画，尊重 reduced-motion；390px 手机/light/dark 无横向溢出 |

## 后端连接修复的边界

- `Catalog.Detail` / job GET 返回 `inputs`、`request`、稳定的当前用户活跃 `consumer_id`。一个消费者取消后，另一个仍能继续。按文件筛选后取最近100条历史，避免别的文件新任务挤掉当前文件记录。
- Query bridge 只把 `input_selectors` / `input_headers` 绑定到已授权输入顺序。长度、类型与 selector 键受限；第一项必须和主 selector 一致；路径仍由可信桥生成。子 Agent scope 拒绝这两项扩展。
- Office 页图保留原文件来源；页码越界、超过10页和冲突 selector 均拒绝。
- OCR 坐标在生成 block ID 前转换到原图/原页，避免 ID 与后置改写 anchor 不一致。原先已有的坐标回映射没有被重复应用。

## 可复现证据

Runtime `docs/plan/evidence/file-ui-contracts/` 保存真实 LibreOffice/Poppler、RapidOCR、DOCX 和 DuckDB 对照结果。临时 SQLite `-race` Detail/consumer 探针通过后从源码包移除；没有新增永久 pytest/Go 测试文件。

最终整页 28 项浏览器验收和截图已归档至 `docs/ui/evidence/file-ui-parity/`，保留清楚的 synthetic fixture 水印、请求载荷与实际 bundle SHA。它只证明界面和参数连接；不能据此声称生产服务器已升级、商业模型已看图或 OCR/检索质量全通过。

## 最终有限审查说明

本轮完成的是当前已实现文件能力的 UI 操作入口，不是任意格式的完整桌面编辑器：

- 图片区域选择适用于原始图片与未裁剪的 PDF 页图；Office 页图可以导航、下载，但不把任意版面区域转换为 Office 编辑权限。DOCX 页眉/页脚的引用保持版本和位置说明，无法精确跳转时提示预览或下载核对。
- 修改只限已登记操作：DOCX 段落/run/table、XLSX 值或公式、PPTX 文本或已上传 PNG/JPEG 图片、Notebook cell source。L2 的用户允许区域提交后固定；验证不足时只能查看/下载候选，不可发布。版本“对比信息”显示字节大小和摘要；逐项修改前后对比来自候选的实际验证报告，不声称能自动比较任意两份完整文档版面。
- “我的候选修改”只列当前用户的处理记录。共享文件的已发布版本仍可按权限查看或下载；不向其他用户提供无法访问的私有 job 审核按钮。
- 取消作用于当前用户一个活跃 consumer。共享执行仍有其他 consumer 时明确提示其继续；界面不把取消一个等待等同于终止所有人的处理。
- Notebook 读取不会执行代码；执行要求选中 code cells、明确确认和后端写权限。保存的执行产物预览只取选中 cells 的纯文本，最大 2 MiB/30 秒；HTML、JavaScript 和 iframe 不渲染或执行，完整文件另提供下载。
- `source_versions` 从选定不可变版本传到服务端；版本归属、当前 ACL、SHA/大小由 Runtime 再校验。没有显式版本选择的旧 API 客户端仍兼容当前 head 解析。旧历史若从未记录版本 ID，UI 不声称能够反推它当时的不可变版本。

HeroUI MCP 重新可用后核对了现用 Input/Button API：Input 采用原生 `disabled`，Button 采用 `isDisabled` 和 `onPress`。所有已存在的安全与权限校验仍在服务端；禁用 UI 控件本身不被视为授权边界。
