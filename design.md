# Localaw 系统设计文档（Go 重构）

> 状态：**已定稿（依据审核 Q1–Q16 结论；MCP 扩展 D1–D8 已确认）**
> 范围：将原 Python 单会话 Localaw 重构为 Go 编写的**纯本地单 Agent、多会话**实现，
> 指令调用改为 OpenAI 原生 Tool Calling，CLI/Web 交互参考 C2Agent、界面基于原 Localaw 前端。

---

## 0. 审核结论（Q1–Q16）

| # | 结论 |
| - | ---- |
| Q1 | 新工程保存于根目录（`Demo` 的上级目录），module `localaw`；`Demo/localaw_old_version` 仅作参考 |
| Q2 | 使用 C2Agent 的 14 个工具 |
| Q3 | 安全黑名单沿用 C2Agent 版本 |
| Q4 | 分层配置，与旧扁平配置**不兼容**；**无** `files`/下载中转目录 |
| Q5 | **隐藏/删除 Agent 概念**，完全本地单 Agent，不做多 Agent 与最大复用设计 |
| Q6 | 保留线性指令队列 |
| Q7 | 仅保留 `fail` 语义，超时即终止进程树并失败 |
| Q8 | 删除 `/agents`、`/target`、`/shutdown` |
| Q9 | 文件上传/下载**直接读写**，无中间转存目录 |
| Q10 | 仅保留 `/exec`（直连命令） |
| Q11 | 英文默认系统提示词，`{system_name}` 替换 |
| Q12 | 前端以 **Localaw 的 html 为主线**修改 |
| Q13 | 编写核心单元测试 |
| Q14 | 提供交叉编译脚本，目标平台同步 C2Agent |
| Q15 | 会话仅内存，不持久化 |
| Q16 | LLM 依赖沿用 `openai-go v1.12.0`；MCP 使用官方 `go-sdk v1.8.0`；Go 最低版本提升到 **1.25** |

---

## 0.1 MCP 扩展决策（已确认）

| 编号 | 决策 |
| ---- | ---- |
| D1 | Localaw 作为 **MCP Client / Host**，连接外部 MCP Server；不对外提供 MCP Server |
| D2 | 使用官方 `github.com/modelcontextprotocol/go-sdk` v1.8.0；Go 最低版本提升到 **1.25** |
| D3 | MCP 调用与本地动作共用**同一条 FIFO 队列**，保持串行与 stop/clear 语义 |
| D4 | MCP 工具默认风险为 `high`，默认不信任 Server 注解 |
| D5 | 本期**仅支持 tools**；不支持 resources / prompts / sampling / elicitation / roots |
| D6 | `call_timeout=0` 表示继承 `mcp.default_timeout`，再回退到 `policy.cmd_timeout`；可按 Server/工具覆盖 |
| D7 | HTTP 认证先支持静态 headers/Bearer，暂不做 OAuth |
| D8 | Web 端提供只读 MCP Server 状态列表；不做在线配置编辑 |

---

## 1. 目标与非目标

**目标**
1. Go 化，项目名 Localaw，单一跨平台二进制（纯 Go，无 CGO）。
2. 指令调用重写为标准 Tool Calling（删除提示词 JSON 解析）。
3. 多会话（各自历史 / 轮数 / 授权 / 停止）。
4. CLI 与 Web 交互对齐 C2Agent；Web 视觉沿用原 Localaw 风格。
5. 支持 MCP Client/Host：接入 stdio / Streamable HTTP / HTTP+SSE Server 的 tools。

**非目标**
- 不引入远程受控端、Agent 注册表、Backend 抽象；MCP 仅作为 **Client** 连接外部工具 Server，不提供远程控制通道。
- 不实现 MCP 的 resources / prompts / sampling / elicitation / roots。

---

## 2. 总体架构

```
CLI / Web(SSE + go:embed)
        │
     Engine（sessions map / active / authMode / eventHub）
        │  OpenAITools() / Get() / RequiresAuth()
    ToolRegistry（内置 command 表 + MCP 工具快照）
        │  Submit(ctx, action, params)
   线性队列 Queue（单条 FIFO dispatcher；排队不超时、执行受工具/全局超时）
        │
    Executor → FileModule / exec_cmd（本机） / MCP Host
                                          │
                              stdio / Streamable HTTP / SSE
                                          │
                                  外部 MCP Servers
```

与 C2Agent 的差异：**去掉** Agent / Backend / Kind / 远程受控传输；
Engine 直接持有单个本地 `Queue`。会话不再绑定 Agent。
MCP 扩展只引入 **MCP Client/Host**（连接外部 Server 的 tools），不改变“纯本地单 Agent”的定位。

---

## 3. 模块设计

### 3.1 `internal/config`
分区：`llm`（`api_base/api_key/model/temperature/stream/system_prompt`）、
`web`（`listen_host/listen_port`）、`policy`（`auth_mode/round_limit/cmd_timeout/max_sessions/queue_capacity`）、
`mcp`（全局开关/限额 + `servers[]`，见 README「MCP 配置」）。
缺失字段用默认值；`auth_mode` 非法回退 0；端口范围校验；MCP 校验 Server 名/传输类型/风险/超时并展开 `${VAR}`。
默认提示词为工具调用式英文。

### 3.2 `internal/command`
动作表的单一来源：14 个动作（名称 / 参数 / 必填 / 低风险 / 描述）。
- 低风险：`get_cwd`、`list_dir`、`read_file`。
- `RequiresAuthRisk(mode, lowRisk)`：0 全需 / 1 仅高风险 / 2 全免。
- `CheckSafety(action, params)`：仅对 `exec_cmd`，黑名单 `format c:`、`mkfs.`、`dd if=/dev/zero`，
  以及 `rm` 命中根/`*` 的正则拦截（不区分大小写、折叠空白）。
- 内置工具 schema 由 `command.Schema` 生成，最终由 `toolreg.OpenAITools` 与 MCP 工具合并后交给 LLM。

### 3.3 `internal/local`
- `FileModule`：`GetCwd/ListDir/ListEntries/CreateDir/CreateFile/Delete/Rename/ReadFile/WriteFile/EditFile/Copy/Move`。
  业务错误以 Go error 返回，由 Executor 统一转为 `Error: ...` 结果串。
  `read_file` 整文件截断 **51200 字符**。
  `edit_file` 采用 Zed 式**精确文本匹配**（`old_text`/`new_text`）：要求 `old_text` 在文件中唯一出现，命中失败或歧义时**拒绝写入**（不再使用易漂移的行号编辑，避免重复行/错位编辑）；写入经临时文件原子替换。
- `Executor.Execute`：`exec_cmd` 走 Shell，其余走 `FileModule.Dispatch`。
  - Windows：`cmd.exe /c <cmd>`（通过 `SysProcAttr.CmdLine` 保留原始引号），`taskkill /F /T` 终止进程树，输出按控制台代码页解码。
  - Unix：`/bin/sh -c`，`Setpgid`，`kill(-pgid, SIGKILL)`。
  - 非 UTF-8 输出以替换字符兜底；`cmd.WaitDelay` 兜底。
- `Queue`：`jobs chan *Job` + 单 dispatcher goroutine。
  - `Submit` 入队后等待结果；**入队等待不受超时**，仅受调用方 ctx 取消影响。
  - dispatcher 出队后用 `context.WithTimeout(ctx, timeout)` 包裹执行；`Job.Timeout` 优先于 `cmd_timeout`；超时 → `TimeoutError`。
  - 业务错误（`Error:` 开头）作为正常结果回灌；取消 → `ErrCancelled`。

### 3.4 `internal/llm`
封装 `github.com/openai/openai-go`：流式 / 非流式 chat completions + tools；
按 `index` 聚合流式 `tool_calls`（含端点不返回 index 的兼容处理）。
Engine 使用 `toolreg.OpenAITools()` 合并内置工具与 MCP 工具；`llm` 包只负责 API 客户端与流式 tool_calls 聚合。

### 3.5 `internal/engine`
- `Engine`：`sessions map[string]*Session`、`active`、`authMode`、`eventHub`、`queue`、`toolreg.Registry`。
  - `Close()`：禁止新会话/新对话，取消所有运行会话并等待（总时长有上限），最后关闭 eventHub 订阅。
- 会话：`NewSession/EnsureSession/ListSessions/ActiveSession(ID)/SetActiveSession/CloseSession`；
  `BeginChat` 后台 goroutine 运行 `runConversation`，重复运行拒绝。
- `runConversation`（每轮 < `round_limit`）：
  1. 取 `registry.OpenAITools()` 作为本轮 tools；推 `answering`；流式 LLM，逐 delta 推 `chunk`。
  2. assistant 消息（含 `tool_calls`）原样入 history；无 tool_calls → 结束。
  3. 逐个 tool_call：解析参数 → 未知动作/安全检查/非法参数 → 直接回灌结果；
     需授权 → 注册 `authCh` → 推 `auth_required` → `awaitAuth`（超时 5 分钟视为拒绝）；
     拒绝 → **不下发**，回灌拒绝结果，未处理 tool_call 补 skip，结束本轮；
     通过 → `round = 0`，`queue.Submit` 执行，结果以 `role=tool` 回灌，推 `execution_done`。
  4. `finish`：统一推一次 `done`，phase 回 `idle`，清除停止标志（可继续对话）。
- 事件带 `session` + `seq`；`eventHub` 支持按会话过滤、慢消费者丢弃。
- `RunDirect(cmd)`：操作员直连命令，经同一队列执行。

### 3.6 `internal/cli`
事件驱动循环；命令：`/sessions /new /use /close /auth /reset /exec /help /quit`。
`auth_required` 时在同一循环读 stdin（`/y`、`/n`、`/auth 0|1|2` 即时重评估）。
普通输入进入活动会话。

### 3.7 `internal/web`
标准库 `net/http`（1.22+ 引入的方法路由）+ `go:embed ui/index.html`；origin 校验防 CSRF。
接口：
- `GET /api/config`
- `GET /api/mcp/servers`（只读状态，不含 env/headers）
- `GET /api/sessions`、`POST /api/sessions/{new,close,activate}`
- `GET /api/history?session_id=`、`POST /api/send`、`GET /api/chat-stream`(SSE，附加时先发 snapshot)
- `POST /api/set-auth`、`POST /api/authorize-execute`、`POST /api/stop`、`POST /api/reset`
- `POST /api/exec-cmd`
- 文件管理：`GET /api/files/list`、`POST /api/files/{parent,chdir,new,mkdir,delete,copy,move}`、
  `GET /api/files/download`、`POST /api/files/upload`
- 文件管理使用服务端维护的当前目录（进程 cwd 起步），**本机直连读写**，无中转目录。

SSE 事件：`snapshot/idle/answering/chunk/response_done/auth_required/executing/execution_done/
execution_error/execution_denied/stopped/llm_error/error/done/reset`（`reset` 用于会话重置在任务结束后被延迟应用时通知前端清空）。

### 3.8 前端（`ui/index.html`）
以原 Localaw 页面为基线：保留 CSS 变量、主题、聊天气泡、Command/FileManager/Controls 面板与模态框；
新增左侧 Sessions 抽屉（新建/切换/关闭、运行状态），
消息发送改为 `POST /api/send` + `EventSource` 附加会话（支持多会话切换与断线重连、snapshot 重放）。
MCP 扩展：Controls 侧栏增加只读 Server 状态列表；授权弹窗与结果块显示 `server / tool`。

### 3.9 `internal/mcphost`

MCP Client/Host 的唯一实现，只依赖 `config` 与官方 SDK。

- `Manager`：持有所有 `serverConn`、全局工具索引与暴露名映射。
  - `Start()`：并行连接 enabled Server，最多等 `init_timeout`；失败不阻塞主程序。
  - `Tools/Lookup/Has/Call`：供 `toolreg` 与 `Executor` 使用。
  - `Servers()`：返回不含密钥的状态，供 Web/CLI；`error` 仅返回错误类别，完整错误只写本地日志。
- `serverConn` 状态机：`connecting → ready → degraded → connecting … → failed/closed`。
- 传输：
  - stdio：自管 `exec.Cmd` + `mcpsdk.IOTransport`，关闭时先关 stdin，超时后 `procutil.KillTree`；
  - http：`mcpsdk.StreamableClientTransport` + 自定义 `RoundTripper` 仅对同源请求注入静态 headers；
  - sse：`mcpsdk.SSEClientTransport`，同样仅同源注入；
  - 跨域重定向移除认证头，HTTPS → HTTP 降级被拒绝；
  - stdio 默认只传 PATH/HOME/TEMP 等最小环境，`inherit_env=true` 才继承完整环境。
- 工具发现：`ClientSession.Tools()` 自动翻页；按 allow/deny、风险、schema 限额解析。
- 工具刷新：`ToolListChangedHandler` → 去抖 `refreshCh` → 重新 `tools/list` → 原子替换快照。
- 重连：指数退避 1→2→4…→60s，达到 `max_attempts` 后 `failed`。
- 能力：`ClientOptions.Capabilities` 设为空，不注册 sampling/elicitation handler，不宣告 roots。

命名与转换：

- `mcp__<server>__<tool>`；非 `[A-Za-z0-9_-]` 折叠为 `_`；总长 ≤64，超长/冲突追加 8 位哈希。
  冲突集合变化时（例如移除其中一个冲突工具），存活工具的哈希/暴露名可能变化；README 已注明。
- `inputSchema` 必须根为 object；移除 `$schema/$id`；按 `max_schema_bytes` 限制。
- 结果统一转文本：文本直传；图片/音频为大小占位；resource 为可读占位；structured 追加 JSON；
  `isError` 加 `Error:` 前缀；按 `max_result_chars` 截断。

### 3.10 `internal/toolreg`

内置工具与 MCP 工具的统一注册表，Engine 不直接依赖 MCP。

- `Tools()`：内置 14 个 + 当前 MCP 快照。
- `OpenAITools()`：每轮 LLM 请求前生成 schema，因而能感知 `tools/list_changed`。
- `Get()`：按暴露名查询，返回 `Source/Server/MCPName/Timeout` 等字段。
- `RequiresAuth(mode,name)`：使用每个工具解析后的 `LowRisk`，底层复用 `command.RequiresAuthRisk`。
- 工具级超时由返回的 `Def.Timeout` 字段携带（0 = 队列默认），不提供单独的 `Timeout()` 方法。
- `MCPStatus()`：委托 `mcphost.Manager.Servers()`。

---

## 4. 关键流程

### 4.1 授权与轮数
授权模式全局可变；授权等待为**会话级**（多会话可同时等待，Web 分别弹窗，CLI 处理活动会话）。
授权通过清零本会话轮数；拒绝不下发且结束本轮。
MCP 工具使用解析后的 `LowRisk`（工具覆盖 → Server 默认 → 全局默认）参与同一套授权模式；
默认不信任 Server 注解，`trust_annotations=true` 时才可能把只读封闭世界工具判为 low。

### 4.2 超时
- 排队不超时；执行受 Job/工具超时，未指定时回退到 `policy.cmd_timeout`。
- 超时 → 终止进程树 → `TimeoutError` → 以 tool 错误回灌并结束本轮（`fail` 语义）。
- 会话停止 → 取消 Job（持 Job 引用逐个 cancel）。
- 进程退出 → 先 `Queue.Close()`：取消 dispatcher 当前 Job 并失败所有排队 Job；再关闭 MCP Host，避免退出等待长 MCP 调用。

### 4.3 文件管理
- 浏览/新建/删除/复制/移动：本机直连，返回结构化 JSON。
- 上传：浏览器 multipart 直接写入当前目录。
- 下载：`http.ServeFile` 直接回传本机文件。

### 4.4 MCP 工具调用

1. `toolreg.OpenAITools()` 取当前内置 + MCP 快照，随请求发送给 LLM。
2. LLM 返回 `mcp__server__tool` 调用。
3. `toolreg.Get` 校验工具存在，`command.CheckSafety` 对 MCP 直接通过。
4. `toolreg.RequiresAuth` 按解析后的风险与当前模式判定；授权通过清零本轮轮数，拒绝不下发并结束本轮。
5. `Executor` 识别 MCP 暴露名 → `mcphost.Call`。
6. `mcphost` 从当前 session 调用 `CallTool`；`isError` / 协议错误作为普通工具结果回灌；超时/stop 返回 ctx 错误，由队列统一处理。
7. 结果按 content 类型转换、截断后作为 `role=tool` 回灌，进入下一轮。

### 4.5 MCP 生命周期

- 启动：并行连接，`init_timeout` 内尽量就绪；未就绪的 Server 后台重连，工具在后续轮次自动出现。
- 断线：移除该 Server 的工具，状态 `degraded`，指数退避重连；重连后重新发现工具。
- `tools/list_changed`：重新 `tools/list`，引擎下一轮生效；工具被移除后旧调用得到普通 unknown action 错误。
- 关闭：取消重连 → 关闭 session → stdio 关闭 stdin/等待退出/超时杀进程树；HTTP 发送 DELETE。

---

## 5. 项目结构

见 `README.md` §3。module `localaw`；入口 `cmd/localaw`。

## 6. 构建与测试

- `go build ./...`、`go vet ./...`、`go test ./...`。
- `build/build.bat`、`build/build.sh`：`CGO_ENABLED=0` 交叉编译
  windows(amd64/386)、linux(amd64/386/arm64)，产物在 `dist/`。
- 测试覆盖：命令表/授权/安全、文件操作、队列串行与超时终止、引擎工具调用与授权拒绝；
  MCP 配置、命名/截断、schema/结果转换、风险解析、stdio 与 HTTP 集成、`tools/list_changed` 刷新、
  未配置 MCP 的回归行为。

## 7. 风险

- 不同 OpenAI 兼容端点的流式 `tool_calls` 表现差异（已按 index 聚合 + 合成槽兼容）。
- Windows 命令输出编码（以 `chcp 65001` + 非 UTF-8 兜底处理）。
- 进程树终止在各平台的差异（Windows `taskkill /T`，Unix 进程组）。
- MCP Server 的流式分页、工具名长度/字符集、注解可信度差异；已做命名空间化、schema 限额与“默认不信任注解”。
- 慢 MCP 调用会占用共享队列；通过 Server/工具级 `call_timeout` 限制影响，后续如需并行再引入执行域分队列。
- stdio Server 的孙进程清理依赖自管进程 + 进程树终止；HTTP Server 依赖 SDK 的会话 DELETE。
- 远程 MCP 的 OAuth 尚未实现；本期仅静态 headers/Bearer。
