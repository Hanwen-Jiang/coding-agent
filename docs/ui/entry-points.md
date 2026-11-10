# 接入点管理界面

实现 `docs/plan/entry-points.md` 的 EP-02，入口是设置 → 接入点（`#/settings/entry`）。

- 表单读取 `/api/entry/kinds` 的字段定义；按字段类型生成必填、数值范围、枚举、文本与多行输入。GitHub 的 `mention` 显示实时触发与确认示例。
- 连接创建后默认停用，保存后再启用。类型与工作区创建后不能修改。独立配置自主程度、回复语言、每小时次数与预算。
- GitHub 评论和邮件默认回复触发位置；定时与告警默认仅网页。每个投递方式可选择受理、确认、结果、失败等通知阶段。
- 告警与邮件的密钥只在创建和轮换响应后显示一次，不写入浏览器存储。关闭、退出账号或切换账号时清除显示内容；旧账号的迟到响应不展示密钥。
- 最近触发显示状态、拒绝原因、来源链接和对应会话。会话顶栏可查看来源与投递详情；进行中列表支持来源筛选。
- 死信可以手动重发；结果未知的投递只显示核对提示，不能在网页强制重发。需要发布确认时，可从运行详情确认。
- 界面沿用现有中性色、HeroUI 按钮样式与原生表单，支持中文、英文和窄屏。

## 验证（2026-11-10）

已在真实 Chromium 中使用本地 API fixture 检查 UI 的完整交互。该验证证明浏览器请求和展示行为，不替代真实服务端的幂等、鉴权或投递验收。

- 中文 1440px 桌面、英文 390px 手机：默认停用、mention 预览和提交、连接开关、不可变类型、一次性密钥、密钥轮换、网页发布确认、死信重发、未知结果不提供重发、来源筛选；均无 JavaScript 异常。
- `node --check` 检查 `app.js`、`entry-ui.js`、`i18n.js`；`git diff --check` 通过。
- 原有 `TestPersonalConfiguration*`、`TestTheAccountPage*`、`Test*Translation` 浏览器与翻译回归通过。指定浏览器为 `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`。

证据：`evidence/entry-points-20261110/results.json`；同目录的桌面/手机连接页与手机表单截图。截图不包含密钥。

### 服务端闭环探针

使用现有 `newTriggerFixture` / `fakeWorkspaces` / `cancelRecorder` 搭配临时验收探针跑过 `go test ./internal/server -run '^TestEntryProbe' -count=1 -race -v`，五项全部通过：

- 定时任务生成一个只读 Task；重复推进与重建 Server 不重复建立会话、工作区或 Task；停用连接后原任务照常完成，结果发件箱带正确会话深链。
- 未认证告警返回 401，读取请求体次数为 0，无触发落盘；精确事件与同指纹窗口去重；恶意告警正文只在资料块里。
- 受理后修改连接的指令、自主程度和目的地，旧任务仍按原快照执行和投递，不推送改动。
- 两个 Server 共享存储并发推进，只创建一个工作区和一个 Task。
- 停机只补最近计划时刻；上一轮未完成时跳过；下一时刻可再次启动。八条告警在 2.50 美元连接上限下只启动两项，各预留 1 美元，其余六项拒绝。

这组探针使用进程内存储、替身 Runtime 与本地替身 GitHub；不代表真实 Postgres、远端 Runtime 或外部投递验收。临时测试已移出仓库，没有新增长期测试；记录见 `evidence/entry-points-20261110/backend-probe.log`。

### 提交前定向复查

近期修复另以临时 `TestEntryFinal*` 探针在 `-race` 下通过：

- MemoryStore 与真实临时 Postgres：删除连接后列表/所属用户查询不可见，后台仍可读取墓碑；并发创建同一用户/工作区/触发名仅一个成功；过时编辑不会恢复轮换前的密钥。
- 两个 Server 并发迁移包含 running、accepted、done 的旧触发 JSON，重复迁移无额外记录；原文件完整保留为 `.migrated`，新连接停用；已有运行的 Task 保持原 ID，只有旧 accepted 触发启动一次。
- 两个用户使用同一触发名不再吞确认；改名后仍按运行冻结的旧名称接受确认；GitHub 评论与网页入口均拒绝超过 24 小时的确认。

记录：`evidence/entry-points-20261110/final-fixes-probe.log`。临时测试与 `TestMain` 已移出仓库。
