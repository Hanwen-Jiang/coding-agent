# 文件工作台浏览器交互验收

日期：2026-11-10。截图来自真实 Chrome 加载本仓库编译后的 HeroUI Pro React bundle，顶部保留“合成 fixture 数据，非真实文件处理”水印。此处验证的是 UI、请求参数和交互状态，不将 fixture 的文档、SQL、图片、编辑验证结果当成真实处理引擎验收。

## 已验证

- 桌面结构视图与 390×844 手机布局；手机 body、主内容、侧栏均为 390px，无页面横向溢出。
- PDF 用户输入页码 `1-3` 转成后端 `selector.pages:[0,1,2]`；启动后展示真实返回的 running 状态，每 2 秒轮询，完成后显示 blocks/outline/coverage/warnings。
- 处理过程中切换文件，旧文件结果没有出现在新文件视图；切版本和异步操作使用当前选择代次隔离。
- SQL 查询发送所选 sheet/range、SQL、parameters 和 max_rows；结果表与实际返回的行数对应。
- XLSX 简单编辑表单生成 `set_formula`，已捕获参数：sheet=`收入`、cell=`B2`、formula=`=SUM(B3:B10)`，同时发送 base_sha256 与 L1 保真等级。高级 JSON 保留为可选入口。
- 多文件索引：勾选 PDF 与尚未读取的 Markdown 时明确显示缺少读取记录的文件名，不发起 index；PDF 与读取完成的 XLSX 则提交两个 job_ids 与 expected_revision。
- 检索显示返回片段及 version/representation/anchor。索引区常驻说明仅覆盖已读取选区，未读取页面或范围不参与检索。
- 发布 fixture 返回 HTTP 409 时保留并显示服务端冲突，不显示成功。
- 真实权限投影 `write=false/manage=false` 时，读取、检索、只读查询仍可用；编辑、重算、发布、重命名、移入回收站不可用。缺失权限字段同样 fail closed。
- 检查时 Chrome 控制台无脚本异常。

## 截图

- `desktop-structure.jpg`：桌面结构、覆盖范围与部分读取提示。
- `desktop-multisource.jpg`：多文件选集及已完成读取资料的集合入口。
- `mobile-data.jpg`：手机数据结果、编辑区与正常纵向滚动。
- `readonly-permissions.jpg`：只读文件的发布及管理按钮禁用。

临时 fixture 与请求记录保存于 `/tmp/file-workbench-qa/`，不作为生产依赖。截图在最终覆盖提示补充之前拍摄；其他界面与该次实际验收一致。完整业务处理、租户/ACL、版本 CAS 和文件计算结果另见 Runtime 的集成验收记录。
