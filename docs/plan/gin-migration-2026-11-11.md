# coding-web 改用 Gin（2026-11-11）

> 状态：2026-11-11 已在分支 `gin-migration` 实现，验收结果见第 9 节；还没合并、没部署。来源：人提的要求（技术栈要有 Gin）。
> 范围：只有本仓库的 coding-web。Runtime 的服务不动，它们的对外契约在 proto 上，用 Connect。

## 1. 目标

把 coding-web 的 HTTP 层从标准库的 `http.ServeMux` 换成 [Gin](https://github.com/gin-gonic/gin)：路由、路由组、中间件、处理函数的写法都用 Gin 的。对浏览器与其他调用方，接口的路径、方法、状态码与响应内容不变。

不做的：

- 不改业务逻辑、存储、与 Runtime 的调用方式。
- 不改接口的形状，不借机重命名路径或字段。
- 不把手写的校验换成 Gin 的 `binding` 标签：给用户看的报错必须有中英两种（`CLAUDE.md` 的硬性规则），校验库的报错只有英文。
- `internal/github` 的 Webhook 收件箱与 `internal/web` 的静态页仍是 `http.Handler`，用 `gin.WrapH` 挂上去。它们各自是完整的小模块，换写法没有收益。

## 2. 依赖

- 加一个直接依赖：`github.com/gin-gonic/gin v1.12.0`（2026-02-28 发布）。它带进来 25 个间接依赖（校验库、几种编解码库、HTTP/3 的库等），都列在 `go.mod` 里。
- `CLAUDE.md` 的“不引入新的 Go 依赖”改成“除 Gin 之外不引入”，经人同意。
- JSON 仍用标准库的 `encoding/json`（Gin 的默认；不加 `sonic` 等构建标签）。

## 3. 引擎的配置

`Server.Handler()` 仍然返回 `http.Handler`，`main.go` 里的 `http.Server`、超时与优雅退出不变。里面改成：

```go
r := gin.New()                  // 不用 gin.Default()：它的日志与恢复中间件都会把请求细节写进日志
r.UseRawPath = true             // 按转义后的路径分段，%2F 不会把一个参数拆成两段（与 ServeMux 相同）
r.UnescapePathValues = true     // 参数值照常解码
r.RedirectTrailingSlash = false // ServeMux 对这些路径不做重定向
r.RedirectFixedPath = false
r.HandleMethodNotAllowed = true // 路径对、方法不对时回 405
```

包的 `init` 里 `gin.SetMode(gin.ReleaseMode)`，不打印调试输出。

| 原来 ServeMux 的行为 | Gin 里怎么保持 |
| --- | --- |
| `GET` 的路由同时接 `HEAD` | 注册时用一个小函数同时登记 `GET` 与 `HEAD`；响应体由 `net/http` 按 HEAD 丢弃 |
| `GET /` 兜底：没匹配上的 GET 都给静态页处理（单页应用自己管路由） | `NoRoute`：GET、HEAD 交给静态页；其余方法回 405，`Allow: GET, HEAD` |
| 路径只登记了别的方法时，GET 落到兜底的静态页（对 `/api/` 是 404） | `NoMethod`：GET、HEAD 同样交给静态页；其余回 405，`Allow` 是 `GET, HEAD` 加上登记过的方法 |
| `{name}`、`{tail...}` | `:name`、`*tail`。`*tail` 取出的值带开头的 `/`，用到的地方去掉 |

## 4. 中间件

全局（`r.Use`，按顺序）：

1. **恢复**：自己写的，捕获 panic 后记方法、路由模板与堆栈，回 500。不用 `gin.Recovery()`，它会把请求头（含 Cookie）写进日志。
2. **安全头**：原来的 `securityHeaders`，内容不变。

没有访问日志中间件（原来也没有）；OAuth 回调的查询串里有授权码，不能进日志。

路由组：

| 组 | 中间件 | 内容 |
| --- | --- | --- |
| 根 | 仅全局 | `/healthz`、`/.well-known/jwks.json`、静态页兜底 |
| `/api` 公开 | 仅全局 | `/api/config`、登录与退出、OAuth 的跳转与回调、两个 Webhook |
| `/api` 登录后 | `requireSession` | 其余全部 |

`requireSession` 就是原来的 `authed`，判断顺序不变：没有 Cookie → 401；会话失效 → 清 Cookie 并 401；非 GET 请求没带 `X-Coding-Web` 头 → 403。通过后把身份放进 `gin.Context`。旧 Cookie（里面还是令牌）换成会话的过渡逻辑保留。

## 5. 处理函数

登录后的处理函数保留类型化的身份参数，不让每个函数自己去 `c.Get`：

```go
type authedHandler func(c *gin.Context, id identity)

func withIdentity(h authedHandler) gin.HandlerFunc // 从 Context 取出 requireSession 放进去的身份
```

身份由中间件负责，处理函数拿到的是编译期就确定的类型，漏挂中间件时 `withIdentity` 直接回 401 而不是空身份。

写法对照：

| 原来 | 现在 |
| --- | --- |
| `func (s *Server) handleX(w http.ResponseWriter, r *http.Request, id identity)` | `func (s *Server) handleX(c *gin.Context, id identity)` |
| `r.PathValue("project")` | `c.Param("project")` |
| `r.URL.Query().Get("q")` | `c.Query("q")` |
| `r.Header.Get("X")`、`w.Header().Set("X", v)` | `c.GetHeader("X")`、`c.Header("X", v)` |
| `http.Redirect(w, r, url, code)` | `c.Redirect(code, url)` |
| `writeJSON(w, status, v)` | `writeJSON(c, status, v)`：设 `Cache-Control: no-store` 后 `c.JSON` |
| `readJSON(w, r, &v)` | `readJSON(c, &v)`：限制大小后 `c.ShouldBindJSON` |
| `writeMsg(w, r, …)`、`writeErr(w, r, …)` | `writeMsg(c, …)`、`writeErr(c, …)` |
| `r.Context()` | `c.Request.Context()` |

只读请求、不写响应的辅助函数（取令牌、调 Runtime、判语言等）继续收 `*http.Request`，调用处传 `c.Request`；它们不属于 HTTP 层，不该依赖框架。

`gin.Context` 会被复用：处理函数返回后不能再用它。后台 goroutine 只带走需要的值，不带走 `c`。

## 6. 几处要单独处理的

| 位置 | 情况 | 做法 |
| --- | --- | --- |
| 工作区操作的日志（`workspace_http.go`） | 异步模式下，处理函数在后台 goroutine 里对着一个合成的请求与缓冲的响应重放；进程重启后还能从磁盘上的记录续跑 | 用 `gin.CreateTestContext` 包住缓冲的响应造一个 `Context`，路径参数从记录里的 `Paths` 填进 `c.Params`；状态码读缓冲里记下的。磁盘上旧的记录格式不变，照样能续 |
| 任务事件的 SSE 代理（`tasks.go`） | 逐条转发上游的事件并刷新 | 写 `c.Writer` 后 `c.Writer.Flush()`；不用 `c.SSEvent`，上游的事件要原样转发，不重新编码 |
| 上传（`sources.go`） | 用 `MultipartReader` 流式读，不落临时文件 | 保留，`c.Request.MultipartReader()`；`c.FormFile` 会先把整个表单读完 |
| 下载与预览 | `io.Copy` 到响应 | `io.Copy(c.Writer, …)` |
| GitHub 的 Webhook | `http.Handler`（收件箱验签） | `gin.WrapH` |
| 静态页 | `http.Handler` | 在 `NoRoute`、`NoMethod` 里调 |

## 7. 会变的行为

都不影响现有的浏览器端代码：

- JSON 响应末尾不再有换行（`c.JSON` 用 `Marshal`，原来用 `Encoder`）。
- 路径里多余的斜杠（`//`）与 `..`，ServeMux 会先 307 到整理过的路径，Gin 不整理，直接按 404 处理。网页不会发这样的路径。
- 请求体不是合法 JSON 时的报错文字不变；但 `ShouldBindJSON` 之后如果给结构体加了 `binding` 标签就会多一层校验，现在一个都没加。

## 8. 步骤与验收

1. `go get` 引入 Gin，改 `CLAUDE.md` 的依赖规则。
2. `server.go`：引擎、中间件、路由登记；辅助函数改签名。
3. 各文件的处理函数与带 `w, r` 的辅助函数改写（先用脚本做机械替换，再逐个文件过编译）。
4. `workspace_http.go` 的重放。
5. 已有测试里直接调处理函数、用 `SetPathValue` 的地方改成造 `gin.Context`。不加新测试（v1 的规矩）。
6. `go build ./...`、`go vet ./...`、`go test ./...` 全过；本机拉起来在网页上走一遍主要流程。

回退：这是一个分支上的一组提交，没有数据迁移，回退就是换回上一个镜像。

## 9. 实现与验收（2026-11-11）

改了什么：

- 新文件 `internal/server/routes.go`：引擎、三个路由组、恢复与安全头两个全局中间件、`requireSession`、`withIdentity`。
- 121 个函数改成收 `*gin.Context`（约 100 个处理函数，其余是会写响应的辅助函数）；11 个函数里原来叫 `c` 的局部变量改了名（`conn`、`changes`、`cursor`）。
- `workspace_http.go` 的重放按第 6 节改；`sourceBase` 原来从 `*http.Request` 取路径参数，改成从 `Context` 取。
- 测试：6 个文件里直接调处理函数的地方改用 `testContext`（新文件 `gin_test.go`）；`TestEveryMessageHasEnglish` 的扫描器认新的参数位置与 `abortMsg`。没有加新测试。

验收：

| 检查 | 结果 |
| --- | --- |
| `go build ./...`、`go vet ./...` | 通过（Windows 与 Linux 容器） |
| `go test ./...`（Windows，照旧跳过 `TestWorkspaceHTTP`、`InBrowser`、`TestConversationMotion`） | 全过 |
| `go test`（Linux 容器，`golang:1.27-bookworm`）：`internal/server`、`internal/web`、`cmd` | 全过，含 `TestWorkspaceHTTP*` 8 条（覆盖重放与重启续跑）；只有 1 条要浏览器的用例因为容器里没有浏览器失败。`internal/github` 要 Postgres，容器里没有，在 Windows 上跑过 |
| 新旧对照：同样的 99 条请求分别打到改之前的代码与这个分支，比较状态码、`Content-Type`、`Cache-Control`、`Allow`、`Location`、安全头与响应体开头 | 97 条一致；不一致的 2 条是 `GET`、`HEAD /api//me`（第 7 节的第二条）。对照覆盖：静态页兜底、HEAD、405 与 `Allow`、未登录的 401、缺 `X-Coding-Web` 的 403、`%2F`、末尾斜杠、两个 Webhook |

没做的：

- 没有在本机拉起整套、在网页上走一遍；没有部署。
- 没有在真实的长连接上验证 SSE 的逐条刷新（代码路径与原来相同：写一块刷一次；有测试覆盖转发内容，没有覆盖到达的时间）。
- `internal/web` 的测试替身、`internal/github` 的收件箱仍是 `net/http` 的写法（第 1 节）。
