# Localaw 系统设计文档（Go 重构）

> 状态：**已定稿（依据审核 Q1–Q16 结论）**
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
| Q16 | 依赖沿用 `openai-go v1.12.0`、Go 1.22 |

---

## 1. 目标与非目标

**目标**
1. Go 化，项目名 Localaw，单一跨平台二进制（纯 Go，无 CGO）。
2. 指令调用重写为标准 Tool Calling（删除提示词 JSON 解析）。
3. 多会话（各自历史 / 轮数 / 授权 / 停止）。
4. CLI 与 Web 交互对齐 C2Agent；Web 视觉沿用原 Localaw 风格。

**非目标**
- 不引入任何远程受控端、网络协议、加解密、心跳。
- 不做多 Agent、Agent 注册表、Backend 抽象。

---

## 2. 总体架构

```
CLI / Web(SSE + go:embed)
        │
     Engine（sessions map / active / authMode / eventHub）
        │  Submit(ctx, action, params)
   线性队列 Queue（单条 FIFO dispatcher；排队不超时、执行受 cmd_timeout）
        │
    Executor → FileModule / exec_cmd（本机）
```

与 C2Agent 的差异：**去掉** Registry / Agent / Backend / Kind / 远程传输；
Engine 直接持有单个本地 `Queue`。会话不再绑定 Agent。

---

## 3. 模块设计

### 3.1 `internal/config`
分区：`llm`（`api_base/api_key/model/temperature/stream/system_prompt`）、
`web`（`listen_host/listen_port`）、`policy`（`auth_mode/round_limit/cmd_timeout/max_sessions/queue_capacity`）。
缺失字段用默认值；`auth_mode` 非法回退 0；端口范围校验。默认提示词为工具调用式英文。

### 3.2 `internal/command`
动作表的单一来源：14 个动作（名称 / 参数 / 必填 / 低风险 / 描述）。
- 低风险：`get_cwd`、`list_dir`、`read_file`。
- `RequiresAuth(mode, name)`：0 全需 / 1 仅高风险 / 2 全免。
- `CheckSafety(action, params)`：仅对 `exec_cmd`，黑名单 `format c:`、`mkfs.`、`dd if=/dev/zero`，
  以及 `rm` 命中根/`*` 的正则拦截（不区分大小写、折叠空白）。
- LLM tools schema 由 `Specs` 自动生成（`llm.BuildTools`）。

### 3.3 `internal/local`
- `FileModule`：`GetCwd/ListDir/ListEntries/CreateDir/CreateFile/Delete/Rename/ReadFile/WriteFile/EditFile/Copy/Move`。
  业务错误以 Go error 返回，由 Executor 统一转为 `Error: ...` 结果串。
  `read_file` 整文件截断 **51200 字符**。
- `Executor.Execute`：`exec_cmd` 走 Shell，其余走 `FileModule.Dispatch`。
  - Windows：`cmd /C "chcp 65001>NUL & <cmd>"`，新进程组，`taskkill /F /T` 终止进程树。
  - Unix：`/bin/sh -c`，`Setpgid`，`kill(-pgid, SIGKILL)`。
  - 非 UTF-8 输出以替换字符兜底；`cmd.WaitDelay` 兜底。
- `Queue`：`jobs chan *Job` + 单 dispatcher goroutine。
  - `Submit` 入队后等待结果；**入队等待不受超时**，仅受调用方 ctx 取消影响。
  - dispatcher 出队后用 `context.WithTimeout(ctx, cmd_timeout)` 包裹执行；超时 → `TimeoutError`。
  - 业务错误（`Error:` 开头）作为正常结果回灌；取消 → `ErrCancelled`。

### 3.4 `internal/llm`
封装 `github.com/openai/openai-go`：流式 / 非流式 chat completions + tools；
按 `index` 聚合流式 `tool_calls`（含端点不返回 index 的兼容处理）；`BuildTools` 生成 schema。

### 3.5 `internal/engine`
- `Engine`：`sessions map[string]*Session`、`active`、`authMode`、`eventHub`、`queue`、`tools`。
- 会话：`NewSession/EnsureSession/ListSessions/ActiveSession(ID)/SetActiveSession/CloseSession`；
  `BeginChat` 后台 goroutine 运行 `runConversation`，重复运行拒绝。
- `runConversation`（每轮 < `round_limit`）：
  1. 推 `answering`；流式 LLM，逐 delta 推 `chunk`。
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
标准库 `net/http`（Go 1.22 方法路由）+ `go:embed ui/index.html`；origin 校验防 CSRF。
接口：
- `GET /api/config`
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

---

## 4. 关键流程

### 4.1 授权与轮数
授权模式全局可变；授权等待为**会话级**（多会话可同时等待，Web 分别弹窗，CLI 处理活动会话）。
授权通过清零本会话轮数；拒绝不下发且结束本轮。

### 4.2 超时
- 排队不超时；执行受 `cmd_timeout`。
- 超时 → 终止进程树 → `TimeoutError` → 以 tool 错误回灌并结束本轮（`fail` 语义）。
- 会话停止 → 取消 Job（持 Job 引用逐个 cancel）。

### 4.3 文件管理
- 浏览/新建/删除/复制/移动：本机直连，返回结构化 JSON。
- 上传：浏览器 multipart 直接写入当前目录。
- 下载：`http.ServeFile` 直接回传本机文件。

---

## 5. 项目结构

见 `README.md` §3。module `localaw`；入口 `cmd/localaw`。

## 6. 构建与测试

- `go build ./...`、`go vet ./...`、`go test ./...`。
- `build/build.bat`、`build/build.sh`：`CGO_ENABLED=0` 交叉编译
  windows(amd64/386)、linux(amd64/386/arm64)，产物在 `dist/`。
- 测试覆盖：命令表/授权/安全、文件操作、队列串行与超时终止、引擎工具调用与授权拒绝。

## 7. 风险

- 不同 OpenAI 兼容端点的流式 `tool_calls` 表现差异（已按 index 聚合 + 合成槽兼容）。
- Windows 命令输出编码（以 `chcp 65001` + 非 UTF-8 兜底处理）。
- 进程树终止在各平台的差异（Windows `taskkill /T`，Unix 进程组）。
