# 本机控制通道

本篇是 [桌面 UI 与本机控制通道](../design/12-ui-and-control.md) 的契约部分：内核向本机用户暴露的控制通道 v1。UI（Electron）、`xmesh` CLI 的 `--control` 读法、内核实现和假内核都以这里为准。方案与契约冲突时以契约为准。

机器可读的部分是 JSON Schema，它是唯一来源：

| 文件 | 内容 |
| --- | --- |
| [schemas/control-common.schema.json](schemas/control-common.schema.json) | 版本、时间戳、ETag、能力、警告等公共类型 |
| [schemas/control-endpoint.schema.json](schemas/control-endpoint.schema.json) | `control.json` |
| [schemas/control-error.schema.json](schemas/control-error.schema.json) | 错误体、错误码与 HTTP 状态 |
| [schemas/control-status.schema.json](schemas/control-status.schema.json) | `GET /v1/status` |
| [schemas/control-events.schema.json](schemas/control-events.schema.json) | `GET /v1/events` 的事件 |
| [schemas/control-peers.schema.json](schemas/control-peers.schema.json)、[control-sessions](schemas/control-sessions.schema.json) | 对端与会话视图 |
| [schemas/control-drain.schema.json](schemas/control-drain.schema.json) | drain 与 resume |
| [schemas/control-config.schema.json](schemas/control-config.schema.json) | 配置读写 |
| [schemas/control-ci.schema.json](schemas/control-ci.schema.json) | GitHub Actions 运行器托管 |
| [schemas/control-pair.schema.json](schemas/control-pair.schema.json)、[control-pats](schemas/control-pats.schema.json)、[control-ingress](schemas/control-ingress.schema.json)、[control-audit](schemas/control-audit.schema.json) | 配对、PAT、入口、审计 |
| [schemas/control-backends.schema.json](schemas/control-backends.schema.json) | stdio 后端的环境变量 |
| [schemas/control-routes.json](schemas/control-routes.json) | 路由表：方法、路径、能力、请求与响应类型、错误码 |

下文引用的类型名（如 `StatusResponse`）就是这些文件 `$defs` 里的定义名，全局唯一。

## CTR-CTL-01

### 传输与发现

控制通道是 HTTP/1.1 over 本机流式套接字，不走 TCP。

| 平台 | 端点 | 访问控制 |
| --- | --- | --- |
| macOS、Linux | 状态目录下 `control.sock`，目录 `0700`，套接字 `0600`。路径超过 UDS 长度上限（100 字节）时落到 `/tmp/xmesh-<uid>/<节点名>.sock`，这个位置固定，不随 `$TMPDIR` 变化；回退路径仍超限时启动失败，错误给出路径长度与上限 | 每个连接用对端凭据（`LOCAL_PEERCRED` / `SO_PEERCRED`）核对 uid 等于内核运行用户，不符立即关闭连接 |
| Windows | 命名管道 `\\.\pipe\xmesh-<sid>-<节点名>` | 创建时设置 DACL：运行用户 SID、`SYSTEM`、`Administrators`；`install --system` 时额外授权安装者的 SID；拒绝远程客户端（`PIPE_REJECT_REMOTE_CLIENTS`） |

内核在开始监听之后写 `control.json`（`ControlEndpointFile`），位置是状态目录（[`app.StateDir`](../../internal/app/runtime.go)，即 `<UserConfigDir>/xmesh/<节点名>/`）。写法是同目录临时文件加重命名；正常退出时删除。`endpoint` 永远是实际使用的路径，因此 UDS 回退到 `/tmp/xmesh-<uid>/` 时 UI 也不需要猜，CLI 与 UI 都从这个字段取路径。

UI 通过枚举 `<UserConfigDir>/xmesh/*/control.json` 发现本机所有节点实例；Windows 上还要枚举 `%ProgramData%\xmesh\*\control.json`，`install --system` 的 SYSTEM 服务默认把状态目录放在 `%ProgramData%\xmesh\<节点名>`。这个节点目录只对 `SYSTEM` 和 `Administrators` 开放（见 [操作手册](../operations.md) 的「状态目录权限」），登录用户不能列出它，只靠对 `control.json` 这一个文件的只读授权读取：内核每次写入该文件时把只读授权给 `--control-allow-sid` 的账户，客户端按精确路径 `%ProgramData%\xmesh\<节点名>\control.json` 打开它（Windows 允许穿越自己不能列出的目录）。需要列出的只有上一级 `%ProgramData%\xmesh`：它由 `privdir.Ensure` 的 `MkdirAll` 创建，沿用 `%ProgramData%` 的继承 ACL（通常允许普通用户列出和读取），这一级只暴露节点名。如果管理员收紧了这一级，枚举会得到空列表，此时 UI 需要用户给出节点名，或由安装方改用 `--state-dir` 指向用户可列出的位置。文件存在不代表内核还活着：以连接是否成功为准，`pid` 只作提示。连接被拒绝或文件缺失都按「未运行」处理，发现和连接过程保留文件。桌面 UI 在控制接口连接失败且记录的 PID 已不存在时，可提供用户主动触发的「清理残留状态」：清理先将文件原子移至同目录的临时位置，再检查 PID 与内容；只删除捕获的旧文件，恢复操作也保留同时发布的新文件。节点配置继续保留。系统服务目录提示由管理员清理。PID 无法确认或已被复用时，继续按「内核无响应」处理。

### 身份与边界

- 访问控制全部在连接层，请求里没有令牌、没有 cookie。同一 OS 用户下的任何进程都能连接，这与它本来就能改 `xmesh.yaml`、读 PAT 文件相符，见 [CON-SEC-12](../constraints/security.md#con-sec-12)。
- 服务端忽略 `Host`。带 `Origin` 头的请求返回 `forbidden`：浏览器不可能连到套接字，出现 `Origin` 说明有东西在转发。
- 控制通道不监听 TCP，不挂在 Gateway、Ingress、tailnet 监听器上，也不是 MCP 工具，见 [CON-ARCH-09](../constraints/architecture.md#con-arch-09)。
- L2 沙箱里的进程必须连不上它，见 CON-SEC-12。
- 内核不依赖 UI 存在，UI 退出或崩溃不影响任何资源，见 [CON-ARCH-10](../constraints/architecture.md#con-arch-10)。

### 请求限制

| 项 | 限制 |
| --- | --- |
| 请求体 | `PUT /v1/config` 最大 1 MiB，其余最大 64 KiB，超过返回 `payload_too_large` |
| 内容类型 | 有请求体时必须是 `application/json`，UTF-8 |
| 并发 | 同一资源的写操作在内核内串行；读不被写阻塞 |

## CTR-CTL-02

### 约定

- 路径前缀 `/v1`。成功响应体与请求体是对应路由的 JSON，字段见路由表。失败响应体是 `ControlError`，HTTP 状态取自 [control-error.schema.json](schemas/control-error.schema.json) 的 `x-http-status`。
- 信封与 [CTR-ERR-01](errors.md#ctr-err-01) 相同（`error`、`message`、`retry_after_ms`），错误码集合不同，因为这里的错误不是工具错误。`message` 是中文一行，调用方不得据此分支。
- 每个响应带头 `X-Xmesh-Control-Version: <主>.<次>`，值也在 `control.json` 与 `status.control.api_version`。
- 服务端拒绝请求体里的未知字段（`invalid_args`，`field` 指出位置），因为请求模式是封闭的。客户端必须忽略响应里的未知字段，并把未知枚举值当作「未知」处理。
- 所有时间戳都是 UTC、固定毫秒的 RFC3339，如 `2026-10-16T15:30:00.123Z`。

### 版本与兼容

- 只加不改。次版本增加字段、路由、枚举值与能力；删除、改名、改语义必须升主版本并换前缀，内核同时提供上一主版本至少一个发布周期。
- 客户端遇到主版本不同的内核，进入「版本不匹配」状态，只保留打开日志目录与升级入口。
- 同一主版本内，新旧内核与 UI 混用靠能力协商：内核在 `control.json` 与 `status.control.capabilities` 里声明已实现的能力（`Capability`），UI 只对已声明的能力启用入口。路由表里每条路由属于一个能力；未声明能力的路由返回 `not_implemented`（501）。
- 客户端不得发送比内核 `api_version` 更新的请求字段。

### 并发控制

只有配置有 ETag。`GET /v1/config` 的响应头 `ETag: "sha256-<64 位十六进制>"`（强校验器）与体内 `etag` 相同，是磁盘上配置文件字节的 SHA-256。`PUT /v1/config` 必须带 `If-Match`：缺失返回 `precondition_required`（428），不匹配返回 `precondition_failed`（412）并在 `current_etag` 给出当前值。其他变更路由是幂等或按名字寻址的，不使用 ETag。

### 审计

经控制通道发生的每个非 `GET` 请求写一条审计，格式是 [CTR-AUD-01](audit.md#ctr-aud-01) 的事件：`event` 为 `control_call`，`actor` 为 `control`，`caller` 为 `control:<连接身份>`（Unix 为 `uid-<数字>`，Windows 为 SID），`tool` 为路由编号（如 `control.config.put`），`args_digest` 只含键名与字节数。成功与失败都记。读取类路由不写审计，避免 UI 轮询刷满磁盘。

### 内容边界

事件流、会话视图、审计视图沿用 [INV-10](../constraints/invariants.md#inv-10)：模式封闭，没有任何字段可以放 stdout、stderr、stdin、终端数据、文件正文、命令参数或环境变量值。令牌正文只出现在 `PatSecret`（见 [CTR-CTL-09](#ctr-ctl-09)），登录口令只出现在 `IngressPasswordRequest` 的请求体。

## CTR-CTL-03

### GET /v1/status

响应 `StatusResponse`，也是 `status` 事件的 `data`。字段包含 [CTR-SYS-01](sys.md#ctr-sys-01) 的全部字段，语义相同，只有 `warnings` 不同，见 [RUL-15](rulings.md#rul-15)。新增字段：

| 字段 | 含义 |
| --- | --- |
| `control` | `api_version`、`kernel_version`、`boot_id`、`capabilities` |
| `lifecycle` | `starting`、`running`、`stopping` |
| `started_at`、`pid` | 进程启动时间与进程号 |
| `drain` | `DrainState`，见 [CTR-CTL-06](#ctr-ctl-06) |
| `paths` | `config_file`、`state_dir`、`log_dir`、`audit_dir`（不可用时为 `null`），供「打开配置文件、日志目录、审计目录」 |
| `gateway` | `roles` 含 `gateway` 时存在：`listen`（配置原样）、`url`（本机 MCP 地址）、`auth`（恒为 `pat`，每个请求都要令牌）、`access` 与 `hosts`（配置原样）、`addresses`（当前真正可达的地址，见 [CTR-GW-07](gateway.md#ctr-gw-07)） |
| `ingress` | `roles` 含 `ingress` 时存在：`listen`、`issuer` |
| `peers` | 不含本机的对端总数与在线数 |

### 警告

`warnings` 的元素是 `{code, severity, message}`。`code` 稳定，`message` 给人看。

| code | severity | 何时 |
| --- | --- | --- |
| `restart_required` | `info` | `restart_required` 为真 |
| `policy_reload_failed` | `error` | 配置文件当前无法加载，继续使用上一份有效配置；或 `agent.cwd_policy.deny_file` 指向的文件无法加载，继续使用上一份成功的禁止目录列表（`message` 说明是哪一个）。文件重新可加载（改回合法内容，或恢复成生效中的那一份）后清除，不保留到重启 |
| `allow_dangerous` | `info` | 配置启用了 `allow_dangerous`（[CON-SEC-10](../constraints/security.md#con-sec-10) 要求提示） |
| `tailnet_not_ready` | `warning` | 模式是 tailnet 但尚未加入 |
| `authkey_unreadable` | `warning` | tailnet 模式下 `authkey_file` 不可读 |
| `peer_offline` | `warning` | 有已知对端离线（每个离线对端一条或合并为一条，由内核决定，UI 不依赖条数） |
| `audit_dropped` | `warning` | 有审计记录因含正文被拒绝写入 |
| `quota_exhausted` | `warning` | PTY 或 exec 配额已满 |
| `sandbox_unavailable` | `warning` | 配置打开了 Windows L2（`agent.policy.windows_appcontainer`），但运行器不可用，L2 的 exec 与 PTY 一律以 `sandbox.unavailable` 拒绝。`message` 带原因（例如账户不是管理员或 LocalSystem，读不了 NTFS 变更日志）；原因只含可对用户说的内容，细节在节点日志。不设置该开关或运行器正常时不出现 |
| `firewall_blocked` | `warning` | Windows 防火墙有一条启用的入站阻止规则针对本节点的程序（例如取消防火墙提示时留下的规则），且该规则适用于本节点当前所在网络的配置文件（域、专用、公用），其他设备可能连不上本节点。只在 Windows 上检查，每分钟重新读取一次；读取失败时保留上一次的结果，首次读取就失败时不出现；不是没有规则的证明 |
| `unclassified` | 任意 | 客户端遇到不认识的 code 时的归类 |

### 托盘状态判定

托盘图标与状态由下列规则按顺序判定，取第一条命中的，见 [RUL-18](rulings.md#rul-18)：

1. 找不到 `control.json` 或连接被拒绝：未运行。
2. `X-Xmesh-Control-Version` 主版本与 UI 支持的不同：版本不匹配。
3. `drain.draining` 为真：排空中。
4. `restart_required` 为真：需重启。
5. `warnings` 里存在 `severity` 为 `warning` 或 `error` 的项：降级。
6. 其他：正常。

## CTR-CTL-04

### GET /v1/events

`Content-Type: text/event-stream`，长连接。每个 SSE 事件是：

```text
id: 9f3c21aa.42
event: peer
data: {"id":"9f3c21aa.42","seq":42,"time":"2026-10-16T15:30:00.123Z","type":"peer","data":{...}}

```

`data` 行是 `ControlEvent` 的紧凑 JSON（单行），`event` 与其中的 `type` 相同，`id` 与其中的 `id` 相同。客户端只需要解析 `data`。

| type | data | 何时 |
| --- | --- | --- |
| `hello` | `HelloData` | 每次连接的第一条，不进环形缓存，没有 `id`/`seq` |
| `reset` | `ResetData` | 见下，不进环形缓存 |
| `status` | `StatusResponse` | 状态有变化。内核合并短时间内的连续变化，至多每秒 4 条 |
| `peer` | `PeerEventData` | 对端加入、更新、移除、上线、下线 |
| `call` | `CallEventData` | 一次工具调用结束：工具名、设备、调用方、状态、错误码、耗时。不含参数与输出 |
| `session` | `SessionEventData` | PTY、exec、租约的建立、变化、结束、释放 |
| `warning` | `WarningEventData` | 警告出现或消失 |
| `config` | `ConfigEventData` | 配置重新加载、加载失败、经控制通道写入；只含变更位置（JSON Pointer），不含取值 |
| `lifecycle` | `LifecycleEventData` | 启动完成、进入 drain、恢复、开始停止 |

### 续传与重置

- 事件 id 是 `<boot_id>.<seq>`。`boot_id` 在内核每次启动时随机生成；`seq` 在一次启动内从 1 单调递增。
- 内核保留最近 1024 条事件。客户端带 `Last-Event-ID` 重连时，若该 id 属于当前启动且仍在缓存内，内核先补发其后的全部事件；否则先发 `reset`：`boot_changed`（id 属于上一次启动）或 `gap`（id 比缓存里最旧的更早）。
- 客户端收到 `reset` 必须丢弃增量状态，重新读取 `/v1/status`、`/v1/peers`、`/v1/sessions`，再从 `reset.data.head` 之后继续。首次连接（无 `Last-Event-ID`）同样应该先读快照再订阅，用 `hello.data.head` 对齐。
- 空闲时内核每 15 秒发一行 SSE 注释 `: ping`。客户端 45 秒收不到任何字节，应断开并带 `Last-Event-ID` 重连，退避重试。
- 每个连接有有限的发送队列；消费者太慢时内核关闭该连接，不丢弃事件、不无限缓存。客户端重连即可续传或重置。

## CTR-CTL-05

### GET /v1/peers

响应 `PeersResponse`。`self` 为真的恰有一项（本机）。`connection` 是 `direct`、`derp`、`unknown`（离线或尚未探测）、`local`（本机自己）。数据来自 mesh 层，不触发任何工具调用。

自 1.1 起，每个在线的对端可带 `info`（`PeerInfo`），即网关在后台对它读取 `sys.info` 的结果：`refreshed_at` 是最近一次成功读取的时间，`error` 是最近一次读取失败的原因。两者都为 null 表示第一次读取尚未完成，面板显示「正在获取设备信息」。本节点自己、离线设备，以及没有网关角色的节点上省略 `info`。网关每分钟对每个在线对端读取一次，读取失败后同样每分钟重试；这个读取与 `devices.list` 按 MCP 会话缓存的 `stale` 相互独立。

### GET /v1/sessions

响应 `SessionsResponse`：`pty`（[CTR-PTY-01](pty.md#ctr-pty-01) 里除 `released` 外的会话）、`exec`（排队中与运行中）、`leases`（后端租约，`token` 是 fencing 序号，不是密钥）。只读视图。PTY 的字段与 [`pty.list`](pty.md#ctr-pty-02) 对应，加上 `oauth_client` 与 `created_at`；`exec.cmd` 只有可执行文件基名。没有终端缓冲、参数、stdin、环境变量。

## CTR-CTL-06

### POST /v1/drain、POST /v1/resume

请求 `DrainRequest`（`mode` 缺省 `keep`），响应 `DrainResponse`。语义与 [CTR-GW-05](gateway.md#ctr-gw-05) 的 drain 相同，作用于本进程的 Agent 与同进程的 Gateway：

- 进入 drain 后拒绝新的 `exec.run`、`pty.open`、`backend.acquire` 与后端工具，错误码 `policy_denied`，规则 `mesh.draining`。
- `keep`：现有 PTY 与租约按原超时结束。
- `kill`：另按 `pty.close` 的顺序终止本机全部 PTY 并释放全部租约，数量返回在 `closed_pty` 与 `released_leases`。
- `resume` 清除 drain，不恢复被 `kill` 终止的资源。
- drain 状态只在内存，内核重启后复位。

规则见 [RUL-16](rulings.md#rul-16)：对已在 drain 的进程重复 `keep` 是 no-op；`keep` 升级到 `kill` 允许，且 `since` 不重置；`kill` 降级到 `keep` 返回 `conflict`，`reason` 为 `drain_downgrade`；未在 drain 时 `resume` 是 no-op。两个路由都返回完整的 `DrainState`。

## CTR-CTL-07

### GET /v1/config

响应 `ConfigResponse`：`content` 是文件原文（含注释），`parsed` 是 YAML 1.2 转 JSON 数据模型后的对象（形状见 [config.schema.json](schemas/config.schema.json)），`valid` 与 `load_error` 说明磁盘上的内容能否通过 `config.Parse`。`effective_etag` 是正在生效的那份配置对应的文件 ETag；与 `etag` 不同，说明磁盘上的新内容尚未生效（热加载失败，或属于需要重启的变更）。

### PUT /v1/config

请求 `ConfigPutRequest`，必须带 `If-Match`。`dry_run: true` 只校验并计算 diff，不写文件，供 UI 保存前展示。

内核的处理顺序：

1. 检查体积与 `If-Match`（对磁盘上当前文件字节算 ETag，在写锁内比较）。
2. 用与启动相同的 `config.Parse` 校验 `content`。失败返回 `config_invalid`（422），`diagnostics` 逐条给出位置，不写任何东西。
3. 与当前磁盘内容比较，得到 `diff`。没有变化时 `applied: false`，不写文件。
4. 写同目录临时文件（保持原文件权限）、`fsync`、重命名覆盖。Windows 使用等价的原子替换。注释与格式由客户端提交的 `content` 原样保留，内核不重新序列化。
5. 返回 `applied: true`、新 `etag`、`diff`、`restart_required`，发 `config` 事件（`actor: control`），写审计。

### PATCH /v1/config

请求 `ConfigEditRequest`，必须带 `If-Match`，与 `PUT` 的 ETag 规则相同。`edits` 是按 JSON Pointer 依次应用到磁盘当前文件上的修改（数组整体替换，缺失的映射自动创建，`delete: true` 删除路径），注释与未修改的部分原样保留。内核先在当前文件上应用全部修改，再走与 `PUT` 相同的 `config.Parse` 校验、diff、原子写入与事件；任一步失败整批都不写入。无法应用的修改（例如路径穿过标量）返回 `conflict`（409）。`dry_run: true` 只返回 diff。UI 的表单式设置（MCP 服务器、内置工具开关、授权）都经此路由写入，避免客户端整文件改写 YAML。

写入后不直接改内存配置：生效走与手工改文件相同的热加载路径，因此「需要重启」的段（`config.RestartPaths`：`node.name`、`node.roles` 里 `agent` 的增减、`mesh`、`audit`、`agent.policy.windows_appcontainer`）只置 `restart_required`，与 [CTR-CFG-01](config.md#ctr-cfg-01) 的热加载规则一致。`gateway.listen`、`gateway.expose_backends`、`ingress` 段和 `node.roles` 里 `gateway`、`ingress` 的增减不在其中：运行中的进程自己应用它们。diff 里 `node.roles` 是一项变更（整个数组），`restart_required` 只看哪些角色变了。

### POST /v1/config/reload

请求没有体。立即重读磁盘上的配置文件，不等待文件监视器的下一次轮询，响应 `ConfigReloadResponse`（自 API 1.4 起；`apply_errors` 自 1.6 起）。能力为 `config.write`。UI 在它自己写入配置文件之外的途径修改了文件后调用它，或要确认当前生效的是哪一份。

| 字段 | 含义 |
| --- | --- |
| `applied` | 本次调用是否使磁盘上的新内容成为生效的配置。内容未变，或文件监视器已经采纳过，为 `false` |
| `valid` | 磁盘上的内容能否通过与启动相同的校验。为 `false` 时 `load_error` 给出第一条错误，生效的配置保持不变 |
| `etag`、`effective_etag` | 磁盘上当前内容，与正在生效的那份对应的文件 ETag。需重启的段待生效时两者不同 |
| `changed_paths` | 本次生效的变更的 JSON Pointer，与 `config` 事件的 `changed_paths` 相同；未生效时为空数组 |
| `restart_required`、`restart_paths` | 磁盘上是否有需重启的段尚未运行，以及它们的路径（`config.RestartPaths` 中的一项或多项，例如 `/mesh`） |
| `apply_errors` | 有效但没能在运行中的进程里生效的部分，`{section, message}`；目前 `section` 只有 `gateway`（新的 `listen` 绑不上，或 Gateway 没能启动）。进程保持原样，不进入 `restart_paths`。调用本接口时会先重试上一次失败的部分，再等本代次应用完（最多约 10 秒）。没有时为空数组 |

行为：

- 有效的新内容：`applied: true`，发 `config` 事件（`change: reloaded`，`actor: control`）。
- 无效的内容：`applied: false`、`valid: false`、给出 `load_error`，继续使用上一份有效配置；首次出现时发 `config` 事件（`change: reload_failed`），`policy_reload_failed` 计数加一。
- 没有变化：`applied: false`，不发事件，只返回当前状态。

`POST /v1/config/reload` 是幂等的：连续两次调用，第二次的 `applied` 为 `false`。

### diff 与 `weakens_security`

`diff` 的每项是 `{pointer, op, before, after, restart_required, weakens_security}`，`pointer` 是对 `parsed` 的 RFC 6901 JSON Pointer。`weakens_security` 由内核按下列规则计算，仅供 UI 弹确认对话框：

- `agent.allow_dangerous` 变为 `true`。
- `agent.cwd_policy.allow` 增加条目，或 `deny` 减少条目。
- `agent.authz` 放宽（增加 tag 的工具授权或后端授权）。
- `gateway.pats` 里某个 PAT 的 `scopes` 变宽，或 PAT 被整体删除后 `gateway.pats` 为空（认证关闭）。
- `ingress` 监听方式变化。

这是 UI 的体验保护，**不是内核的授权边界**：内核不会因为 `weakens_security` 拒绝写入，文档与 UI 文案不得让用户误以为有这层拦截。

## CTR-CTL-08

### 连接密码（1.5 起）

每台 `lan` 节点可以设置一个连接密码。另一台设备输入它即可直接配对，无需交换文件。

- `GET /v1/pair/password` 返回 `PairPasswordState`：`set`、`updated_at`、`failed_attempts`、`locked_until`。从不包含口令或派生值。
- `PUT /v1/pair/password` 的请求是 `PairPasswordRequest {password}`，长度 10 到 128 个 Unicode 码点，首尾不能有空白，违反返回 `invalid_args`（字段 `password`）。设置或修改立即生效，并清除锁定。
- `DELETE /v1/pair/password` 清除口令，204。之后这台设备不接受密码配对，已配对的设备不受影响。
- 口令只写不读：没有任何响应包含它。磁盘上只保存 Argon2id 的派生密钥与盐。

### 内网发现与连接（1.5 起）

- `GET /v1/pair/discovered` 返回 `PairDiscoveredResponse {devices[]}`：尚未配对、宣告了 `pairing=1` 的设备，每项 `name`、`fingerprint`、`address`（主机:端口）、`seen_at`。不含证书，已配对的设备不出现。
- `POST /v1/pair/connect` 的请求是 `PairConnectRequest {address?, fingerprint?, password}`，`address` 与 `fingerprint` 至少给一个。只给 `fingerprint` 时地址取自发现列表，找不到返回 `not_found`。同时给出时，对端证书与 `fingerprint` 不符则在确认前停止，返回 `invalid_args`（字段 `fingerprint`，`reason` 为 `fingerprint_mismatch`），不会钉入任何证书。成功返回 `PairConnectResponse {name, fingerprint, address, tags}`，立即生效，无需重启。

连接失败时，`pair_rejected`（422）的 `reason` 为 `wrong_password`、`no_password`、`unsupported_peer`、`unreachable` 或 `bad_peer`。`wrong_password` 同时表示对端不是待配对的 xmesh 设备，客户端无法区分两者。`rate_limited`（429）带 `retry_after_ms`，表示对端正在限流：每台节点按连续失败次数锁定，最长 30 分钟，成功配对后清零。

这些路由只在 `mesh.mode` 为 `lan` 时可用，否则返回 `unsupported_mode`。

### 配对文档（高级用法）

`POST /v1/pair/export`（无请求体）返回本机公开配对文档 `PairDocument`，等价于 `xmesh pair --export`，只含节点名、证书指纹、公开证书与 `tags`（导出时为空数组）。

`POST /v1/pair/import` 的请求是 `PairImportRequest`：对端的 `pair`，加上 UI 向用户展示并由用户确认过的 `fingerprint`。内核比较它与 `pair.fingerprint`（忽略冒号与大小写）以及证书本身算出的指纹，不一致返回 `fingerprint_mismatch`（422）。`tags` 只允许 `tag:peer`、`tag:device`、`tag:ingress`，缺省 `["tag:peer","tag:device"]`。成功后 `restart_required` 与 `xmesh pair --import` 一致。

两个路由只在 `mesh.mode` 为 `lan` 时可用，否则返回 `unsupported_mode`（409）。导入的指纹必须来自用户，UI 不得自动填入。

## CTR-CTL-09

### PAT

PAT 是 `gateway.pats`（[CTR-CFG-01](config.md#ctr-cfg-01)）里的条目加上令牌文件。控制通道修改它们时，对 `xmesh.yaml` 的改动走与 [CTR-CTL-07](#ctr-ctl-07) 相同的校验与原子写入，并发 `config` 事件；Gateway 逐请求读取凭据，所以创建、轮转、撤销立即生效。

| 路由 | 作用 |
| --- | --- |
| `GET /v1/pats` | `PatList`：名字、scope、令牌文件路径、文件修改时间；`enforced` 表示 `gateway.pats` 非空。不含令牌 |
| `POST /v1/pats` | 创建。`name` 同节点名字符集，`scopes` 用 [CTR-OAUTH-02](oauth-authz.md#ctr-oauth-02) 的语法。内核生成至少 32 字符的 URL 安全随机令牌，写入 `0600` 文件，并在配置里加条目。名字已存在返回 `conflict`。响应 `201 PatSecret` |
| `POST /v1/pats/{name}/rotate` | 生成新令牌并原子替换令牌文件，配置不变，旧令牌立即失效。响应 `PatSecret` |
| `DELETE /v1/pats/{name}` | 删除配置条目与令牌文件，`204`。这是最后一个 PAT 时返回 `conflict`，`reason` 为 `last_pat`：Gateway 每个请求都要令牌，至少保留一个 |

令牌正文**只**出现在 `PatSecret` 里，即创建与轮转的那一次响应；之后任何接口都读不回。

**默认令牌。** 配置里没有任何 PAT 而节点有 `gateway` 角色时，内核启动时自动创建名为 `default` 的 PAT（scope 为 `devices:read`、`exec:*`、`pty:*`、`fs:*`、`sys:*`、`backend:*:*`），令牌文件在状态目录的 `pats/` 下。已经有 PAT 时从不创建，也从不改动：升级、更新、重装保留原有令牌，只有用户要求的轮转才会替换它。重装与卸载不删除状态目录，除非用户明确选择删除数据。UI 只能在内存里展示一次，不得写入日志、事件或磁盘。`PatSecret` 以外的类型不得出现 `token` 字段，由 Go 与 TS 两侧的测试把关。

## CTR-CTL-10

### 入口

`roles` 不含 `ingress` 时本组路由返回 `role_disabled`（409）。

| 路由 | 作用 |
| --- | --- |
| `GET /v1/ingress/clients` | `IngressClientsResponse`：issuer 与已注册的 OAuth 客户端（不含 `secret_hash`） |
| `DELETE /v1/ingress/clients/{client_id}` | 撤销客户端，并使其已签发的 access / refresh token 失效。`204`；不存在返回 `not_found` |
| `PUT /v1/ingress/password` | 轮转登录口令。只写不读：请求体是新口令，响应只有 `rotated_at`。口令写入内核启动参数 `--oauth-password-file` 指向的文件（`0600`，原子替换）并更新内存。口令不是来自文件时返回 `conflict`，`reason` 为 `password_source_not_file` |

口令轮转不撤销已有 token；要让现有客户端全部失效，UI 对每个客户端调用 `DELETE`。口令长度 12 到 256 字符，不在响应、事件、审计、日志中出现。

## CTR-CTL-11

### GET /v1/audit

查询串对应 `AuditQuery`（所有值都是字符串）；按时间从新到旧返回，`limit` 默认 100、最大 1000，`next_cursor` 是对客户端不透明的游标，没有更多时为 `null`。`events` 的元素就是 [CTR-AUD-01](audit.md#ctr-aud-01) 的事件，原样透传，不加字段，所以继续满足「不含正文」。审计未启用时 `enabled` 为 `false`、`events` 为空。内核只读 `audit/*.jsonl`，被保留期删除的记录读不到。

## CTR-CTL-12

### 路由表与生成物

[control-routes.json](schemas/control-routes.json) 是路由的唯一来源。每条路由有编号、方法、路径、所属能力、引入版本、查询串/请求体/响应体类型、成功状态、是否事件流、`If-Match` 要求、可能返回的错误码。所有路由共有的 `internal`、`unavailable`（带 `retry_after_ms`）、`forbidden`、`not_implemented`、`payload_too_large` 不逐条列出。

| 产物 | 位置 | 如何保持一致 |
| --- | --- | --- |
| 夹具 | `schemas/examples/control/<定义名>.<变体>.json` 与 `invalid/` | [`check_control.py`](schemas/check_control.py)（由 `check_examples.py` 调用，接入 `contracts.yml`）：正例通过同名定义，反例失败；路由表里出现的每个请求与响应定义至少有一个正例 |
| Go 类型 | `api/control/v1`；校验辅助 `api/control/v1/schemacheck` | 测试把每个正例解码进 Go 类型（拒绝未知字段）再编码，结果必须与原文件语义相同；Go 常量必须覆盖模式里的枚举，错误码的 HTTP 状态必须与 `x-http-status` 相同 |
| TypeScript 类型 | `api/control/v1/ts/control.ts` | `scripts/gen-control-ts` 从模式生成，含类型、`CONTROL_ROUTES`、`ControlRouteTypes`（路由编号到请求/响应类型）、`CONTROL_ERROR_STATUS`。`contracts.yml` 重新生成后要求工作区无变化，并过 `tsc --strict` |

改契约的顺序：先改本文与模式，再改夹具，跑 `python3 docs/contracts/schemas/check_examples.py`、`go test ./api/...`、`node scripts/gen-control-ts/generate.mjs`，把生成物一并提交。加路由时必须同时：在路由表加一行、选一个已有或新增的能力、为请求与响应各补正例、在内核的路由注册测试里断言它出现。

### 实现方的验收

- 内核的路由注册必须与 `schemas.ControlRoutes`（`control-routes.json` 的嵌入）一一对应，路径与方法相同，未声明的能力返回 501。
- 假内核（`ui/testserver`）按 `examples/control/` 的夹具回放，走与真实内核相同的传输。
- 隐私：向事件流与视图注入含 stdout 的对象必须被编码器拒绝（沿用 INV-10 的测试方法）。

## CTR-CTL-13

### PUT /v1/gateway

`roles` 不含 `gateway` 时返回 `role_disabled`（409）。请求体 `GatewayUpdateRequest` 整份替换 `gateway.listen`、`gateway.access`、`gateway.hosts`，写入与 [CTR-CTL-07](#ctr-ctl-07) 相同的校验与原子写入，并发 `config` 事件。校验失败返回 `config_invalid`（422），不写任何东西。响应 `GatewayUpdateResponse`：`access` 与 `hosts` 立即生效（每个请求读取）；自 API 1.6 起 `listen` 也在运行中生效：Gateway 先绑定新地址再放弃旧地址，Agent 与会话不受影响，所以 `restart_required` 恒为 `false`（字段保留给旧客户端）。新地址绑不上（例如端口被占用）时写入仍然完成，Gateway 留在旧地址，原因在 `apply_errors`，之后再调用一次 `POST /v1/config/reload` 可重试。能力为 `gateway`（API 1.2 起）。

## CTR-CTL-14

### 后端环境变量

给 stdio 后端（`agent.backends.<name>`，`transport: stdio`）设置 API Key 之类的环境变量。值只写不读：写入后任何接口都读不回，响应和 `GET /v1/status` 只给变量名。能力为 `config.write`（API 1.7 起）。

| 路由 | 作用 |
| --- | --- |
| `PATCH /v1/backends/{name}/env` | 请求体 `BackendEnvPatch`：`unset`（变量名列表，先生效）与 `set`（变量名到值的对象）。至少一项；每次最多 64 项，每个后端最多 64 个变量，值不含空字符且不超过 8192 字节。响应 `BackendEnvResponse`：修改之后这个后端还有哪些变量名，按字母序 |
| `DELETE /v1/backends/{name}/env` | 清空这个后端的所有变量，`204`；没有变量也是 `204` |

后端名要符合配置里的后端名语法，但不要求已经在配置里：先存变量、再添加后端的客户端，后端第一次启动就带着变量。变量名是 `[A-Za-z_][A-Za-z0-9_]{0,127}`。节点策略不允许的名字（见 [CON-TOOL-12](../constraints/tools.md#con-tool-12)）返回 `invalid_args`，`field` 指到那个变量，`reason` 为 `env_denied`，整个请求不生效。内核没有环境变量存储时（状态目录不可写、文件损坏）返回 `unavailable`。

存储在状态目录的 `backend-env.json`，`0600`，原子替换，不是 `xmesh.yaml` 的一部分：`GET/PUT/PATCH /v1/config`、配置差异、`config` 事件都不含它。变量改动立即生效，只重启受影响的后端；手工编辑这个文件的结果内核在一秒内应用。通过控制通道把后端从配置里删除时，它的变量一并删除；从没出现在配置里的名字保持原样，由 `DELETE` 清理。

`StatusResponse.backends[].env_names` 列出 stdio 后端已设置的变量名（没有时省略）。审计记录沿用 [CTR-CTL-02](#ctr-ctl-02)：只含请求体的键名（`set`、`unset`）与大小，不含变量名与值。

## CTR-CTL-15

### GitHub Actions 运行器托管

内核在本机监督官方 `actions/runner` 进程：一台机器可以绑定多个仓库，每个绑定可以有多个并发实例，进程崩溃自动重启，随内核服务开机拉起。方案见 [15 GitHub Actions 运行器托管](../design/15-github-runner.md)。能力为 `ci`（API 1.8 起）；内核没有这项能力时整组路由返回 `not_implemented`。类型在 [control-ci.schema.json](schemas/control-ci.schema.json)。

绑定与账号的非密钥部分存在 `xmesh.yaml` 的 `ci` 段（[CTR-CFG-01](config.md#ctr-cfg-01)），经与 [CTR-CTL-07](#ctr-ctl-07) 相同的校验与原子写入修改，并发 `config` 事件；GitHub 凭据存在状态目录的 `ci-accounts/` 私有文件里（与网关 PAT 同样的目录保护），不在配置、响应、状态、事件、审计和日志里出现。运行状态不另设事件：`GET /v1/status` 的 `ci`（`CiSummary`）随每次状态采样变化，UI 从 `status` 事件得到更新。

| 路由 | 作用 |
| --- | --- |
| `GET /v1/ci` | `CiOverview`：账号（只有掩码）、绑定与每个实例的状态、需要用户处理的提示（`notices`）、运行器包缓存、睡眠与停止超时设置 |
| `POST /v1/ci/accounts` | 请求体 `CiAccountCreate`：令牌与可选主机。内核用令牌调用 `GET /user` 取得登录名，成功后保存令牌，响应 `CiAccount`（令牌只有掩码）。同一主机同一登录名再次提交就是更换令牌。GitHub 拒绝令牌时 `invalid_args`，`field` 为 `token`，`reason` 为 `github_rejected`；连不上 GitHub 时 `unavailable`，`reason` 为 `github_unreachable` |
| `DELETE /v1/ci/accounts/{id}` | 删除账号与令牌，`204`。仍有绑定引用它时 `conflict`，`reason` 为 `account_in_use` |
| `GET /v1/ci/accounts/{id}/repos` | 查询串 `CiReposQuery`。列出账号有管理员权限的仓库（只有这些能注册运行器），带私有或公开标记与已有绑定，分页用 `next_page` |
| `POST /v1/ci/bindings` | 请求体 `CiBindingCreate`，三种方式恰好用一种：`target` 加 `account`（内核签发注册令牌），`target` 加 `registration_token`（用户从仓库网页复制的一次性令牌，不留长期凭据），或 `import_dir`（接管已有运行器目录，仓库与运行器名读自目录里的 `.runner`）。响应 `CiBinding`，实例在后台依次 `installing`、`registering`、`starting`、`idle`。目标是公开仓库而 `confirm_public` 不为真时 `invalid_args`，`reason` 为 `public_repo_requires_confirm`；同一仓库已有绑定、`id` 重复时 `conflict`；账号不存在 `not_found`；`import_dir` 里没有运行器、运行器是一次性的、装着官方服务或正在运行时 `invalid_args`，`reason` 分别为 `not_a_runner`、`ephemeral_runner`、`service_installed`、`runner_running` |
| `PATCH /v1/ci/bindings/{id}` | 请求体 `CiBindingPatch`：并发数、标签、暂停与恢复、补上账号。立即生效：加实例、排空后减实例、暂停时排空 |
| `DELETE /v1/ci/bindings/{id}` | 解绑。查询串 `CiDeleteQuery`：`force=true` 不等正在跑的任务。响应 `CiBinding`，`removing` 为真；实例排空后内核向 GitHub 注销运行器（绑定有账号时）并删除目录与配置项。没有凭据时只做本地清理，GitHub 上的运行器留给网页删除或 14 天后自动消失 |
| `POST /v1/ci/bindings/{id}/restart` | 重启这个绑定的所有实例，`204` |
| `POST /v1/ci/bindings/{id}/repair` | 重新注册（运行器被 GitHub 删除、会话冲突排除后、注册信息损坏）。有账号时内核自己签发令牌，没有时请求体必须带 `registration_token`。响应 `CiBinding` |
| `POST /v1/ci/import/inspect` | 请求体 `CiImportInspectRequest`：目录。只读检查，不改任何东西：仓库、运行器名、标签、是否装着官方服务（带卸载命令）、是否正在运行、是否一次性。目录里没有 `.runner` 时 `not_found` |

实例状态机和退出码分流见设计文档。契约只规定可观察的部分：

- `busy` 时 `job` 给出 job 名与开始时间；`last_result` 给出最近一次跑完的 job 与结果。两者来自运行器 stdout 的固定状态行，不含任务日志、参数或环境变量（沿用 [INV-10](../constraints/invariants.md#inv-10)）。
- `broken` 的实例带 `problem`，`code` 稳定、`message` 中文一行；`backoff` 的实例带上一次退出的原因。同一个 `code` 之下用户的下一步固定：`session_conflict` 关掉另一处的运行器后调用 `restart`；`runner_removed`、`registration_failed` 调用 `repair`；`auth_failed` 更换账号令牌；`download_failed`、`missing_dependency`、`start_failed` 按 `message` 处理后 `restart`。
- 节点排空（`POST /v1/drain`）时运行器不再接新任务，`CiSummary.draining` 为真；`POST /v1/resume` 恢复。排空只停空闲实例，正在跑的任务跑完再停；超过 `ci.stop_timeout` 仍未结束的任务被结束。
- `notices` 描述会让运行器扛不过重启或退出登录的环境条件，各带可复制的 `command`（有的话）：Linux 没有 `loginctl enable-linger`、Windows 没有自动登录、PATH 里没有 `git`、缺系统库、官方运行器不支持当前系统。
- 公开仓库默认拒绝（见上），因为公开仓库的 fork PR 可以在这台机器上执行代码。CI 任务以内核运行用户的完整权限执行，不受 `cwd_policy`、禁止目录和 L2 沙箱约束；这些只约束 MCP 工具调用。运行器目录默认写入禁止目录文件，工具读不到运行器自己的凭据。

审计记录沿用 [CTR-CTL-02](#ctr-ctl-02)：只含请求体的键名与大小，不含令牌；绑定、解绑、重启、重新注册各记一条。job 的开始与结束只出现在 `GET /v1/status` 的 `ci`，不写审计：运行器自己的日志留在运行器目录。

## 桌面管理（API 1.9）

能力 `computer` 表示节点持有 Computer Manager。`GET /v1/computer` 返回开启 / 暂停状态、系统权限和显示器信息以及本机所有桌面会话；该视图省略控制租约 token、截图和输入正文。`POST /v1/computer/pause` 接受 `{ "paused": true|false }`，暂停立即撤销所有会话与排队输入；恢复允许调用方重新获取租约。`POST /v1/computer/revoke` 接受 `{ "session_id": "cs-…" }`，关闭指定会话。两条写路由返回当前状态，均经本机控制通道身份校验并记录元数据审计。

类型、路由和模式见 `control-computer.schema.json` 与 `control-routes.json`。远程桌面工具及执行器协议见 [Computer](computer.md)。
