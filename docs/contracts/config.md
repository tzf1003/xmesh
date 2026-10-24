# 配置

## CTR-CFG-01

设备运行配置使用 `schema_version: 2`；旧配置可由 `xmesh migrate` 转换，转换后重新配对。配置文件是 YAML 1.2，路径由 `--config` 指定，默认 `xmesh.yaml`。解析后的数据必须满足 [schemas/config.schema.json](schemas/config.schema.json)。校验失败时进程拒绝启动，错误信息指出 JSON Pointer 路径，例如 `/agent/backends/ida/policy`。

### 角色联动

| 条件 | 要求 |
| --- | --- |
| `roles` 含 `gateway` | `gateway.listen` 的主机为空、`0.0.0.0` 或 `[::]` 表示全部网卡，为 `127.0.0.1` 或 `[::1]` 表示只绑本机；其他地址拒绝启动。默认 `:8080`。`gateway.access` 缺省 `[localhost, lan, tailnet]`，`gateway.hosts` 缺省为空。每个请求都要令牌，见 [CTR-GW-07](gateway.md#ctr-gw-07) |
| `roles` 含 `ingress` | `ingress` 对象必须存在，且 OAuth issuer 为 https |
| `ingress.max_pty_per_client` | 正整数，默认 8；同一入口按 OAuth client 聚合各设备的 active/orphaned PTY，详见 [CTR-GW-06](gateway.md#ctr-gw-06) |
| `ingress.pty_budget_file` | 可选绝对路径；默认使用状态目录中的 `ingress-pty.json`。同一主机的入口可指定同一文件进行跨进程准入，且 `max_pty_per_client` 必须一致 |
| `ingress.pty_budget_authority` | 可选设备名（与 `node.name` 同样的字符集）；多台主机上的入口须配置同一设备，由该 Agent 托管跨主机准入租约，详见 [CTR-GW-06](gateway.md#ctr-gw-06)。留空只在本机协调 |
| `roles` 含 `agent` | `agent.cwd_policy.allow` 必须存在，可以为空；空表示除 `deny` 外的全部目录都可访问 |
| `agent.fs.max_file_size` | 单个上传/下载文件的上限，默认 `1GiB`；支持正整数尺寸（例如 `512MiB`、`2GiB`）及 `unlimited`。面板「设置 → 文件互传」可修改，保存后热更新生效，见 [CTR-FS-08](fs.md#ctr-fs-08) |
| `agent.cwd_policy.deny_file` | 可选绝对路径，指向一个字符串列表的 YAML 文件，其中的目录与 `deny` 一样始终拒绝，与 `allow` 是否为空无关。文件在每次工具调用时重读，改动在下一次调用生效，不需要重启。空列表要写成 `[]`，没有内容的文件按写了一半处理。启动时文件缺失或无法解析，agent 不能启动，不会视为空列表；运行中文件不再能加载（缺失、不可读、不是字符串列表），继续使用上一份成功的列表，`policy_reload_failed` 计数加一，状态里出现同名警告，文件恢复后清除。向导默认创建该文件并写入凭证目录 |
| `mesh.tailnet.enabled = true` | `mesh.tailnet.control_url` 必须为 HTTPS；密钥登录需要 `authkey_file`，浏览器登录使用 `auth_method: browser` 并复用本机网络状态 |
| `mesh.tailnet.ephemeral = true` | Tailnet 网络身份存于内存，每次进程启动重新入网。xmesh 设备公钥仍独立持久化 |
| `mesh.cloudflare.enabled = true` | 经 Cloudflare Tunnel 互连，设备不需要公网地址。`hostname` 是本机隧道主机名，留空表示只拨出；`listen` 必须是回环地址（默认 `127.0.0.1:7434`），cloudflared 用 `tcp://` 规则转发到它；`access_client_id` 与 `access_client_secret_file`（绝对路径）须同时填写；`tunnel_token_file`（绝对路径）让内核自己运行 `cloudflared`（强制 `--protocol http2`，令牌经 `--token-file` 传入，需要 `hostname`），`cloudflared` 可指定可执行文件；`peers` 把已配对设备 ID 映射到它的隧道主机名。隧道只转发字节，设备身份仍由内层固定公钥的 mTLS 决定。默认只放行已配对设备，`allow_pairing: true` 才允许未配对设备经隧道用连接密码配对。局域网、Tailnet、Cloudflare 可任意组合同时启用：只启用一条时状态里的网络模式是 `lan`/`tailnet`/`cloudflare`，两条及以上是 `coexist`，全部关闭是 `offline`（局域网默认开启，仅用 Cloudflare 时须设 `mesh.lan.enabled: false`） |
| `mesh.lan.enabled = true` | 开启局域网路径；`mdns` 缺省 true。可指定物理网卡；网卡暂不可用时该路径独立重试，其他路径与本机管理继续运行。两个 enabled 都为 true 即共存；网络开关热应用 |
| `ingress.listen = funnel` | `mesh.tailnet.control_url` 必须是官方 Tailscale 控制面：`https://controlplane.tailscale.com` 或 `https://login.tailscale.com`（端口只能是 443，不含用户信息）。其他主机，包括 Headscale 和形似官方域名的后缀，一律拒绝启动。issuer 必须是 `ts.net` 源。禁止同时配置 `domain`、`tls`、`cert_file`、`key_file`。域名和证书由 Tailscale Funnel 提供，issuer 使用该节点的 `ts.net` 源 |
| 本机首次配置 | 关闭 LAN 与 Tailnet 路径，以 `--local-only` 开放需认证的回环 Gateway；设备 Agent 仅供该 Gateway 内部调用 |
| `ingress.listen = public:443` | 必须有 `domain` 和 `tls`，且 `ingress.oauth.issuer` 的主机等于 `domain`。`tls: file` 时还必须有 `cert_file` 与 `key_file`。`tls: autocert` 只在这种监听下使用 |
| `ingress.listen = tunnel` | 与 funnel 一样禁止 `domain` 和 `tls`。这是 [入口方式](../design/03-mesh.md) 里的反向隧道，证书由 Cloudflare Tunnel 或 frp 终止处提供。可选 `ingress.tunnel_listen` 指定隧道到达入口的本机地址，必须是 `127.0.0.1:端口`，只在 `tunnel` 下允许；命令行 `--tunnel-listen` 优先，两者都没有时为 `127.0.0.1:8443`。入口角色热应用，改端口后入口重新监听 |
| `ingress.oauth.dynamic_client_scopes` | 可选 scope 数组，语法与 `clients.*.scopes` 相同。动态注册客户端的 scope 上限，缺省 `["devices:read"]`，空数组表示只能完成 OAuth，见 [CTR-OAUTH-01](oauth-authz.md#ctr-oauth-01) |
| `ingress.oauth.refresh_token_ttl` | 可选 Go 时长，刷新令牌的有效期，范围 `1h` 到 `8760h`（365 天），缺省 `720h`（30 天）。刷新令牌每次使用都会旋转并重新计时，客户端只要在这段时间内回来一次就不必重新授权；访问令牌固定 1 小时。令牌保存在入口进程内存，入口重启后须重新授权，见 [CTR-OAUTH-01](oauth-authz.md#ctr-oauth-01) |
| `agent.quota`、`gateway.quota` | 可选对象，字段 `calls_per_hour`（正整数，缺省 10000）、`max_concurrent`（正整数，缺省 64）、`max_call_duration`（Go duration，缺省 `10m`）。`agent.exec.default_timeout` 不得超过 `agent.quota.max_call_duration`，见 [CON-SEC-10](../constraints/security.md#con-sec-10) |
| `mesh.mode = relay` | 拒绝启动，见 [CON-NET-08](../constraints/network.md#con-net-08) |

新版 `mesh` 配置热应用；变更控制面或登录参数只重启对应网络组件。局域网和 Tailnet 共享设备身份及配对信任，路径故障不会撤销设备。

### 时长与尺寸

配置里的时长使用 Go duration 语法：`120s`、`30m`、`720h`。尺寸使用十进制或二进制后缀：`256KiB`、`1MiB`、`10MiB`。模式文件把它们表示成字符串，并给出正则。实现解析失败等同配置校验失败。

### 热加载与冷加载

配置文件的生效方式有两条路，结果相同：

- 文件监视：内核约每秒重读一次文件，有效的新内容立即成为生效的配置，并通过控制通道发 `config` 事件。
- 显式重读：`POST /v2/config/reload`（见 [CTR-CTL-07](control.md#post-v1configreload)）立即重读，返回本次是否生效、变更路径、需重启的段与没能热应用的部分。

内容无法通过校验时，继续使用上一份有效配置，`policy_reload_failed` 计数加一，`load_error` 给出第一条错误。

运行中的 Agent 与 Gateway 在每次调用或每个新代次上读取配置，因此下表的热加载段在下一次调用或下一个代次生效，不需要重启进程。下表中的需重启段由 `config.RestartPaths` 统一列出，控制通道的 diff 与 `restart_required` 都以它为准。

| 配置段 | 变更如何生效 |
| --- | --- |
| `agent.policy` | 下一次工具调用，见 [CON-SEC-06](../constraints/security.md#con-sec-06) |
| `agent.cwd_policy`、`agent.authz` | 与 policy 一样热加载。`deny_file` 指向的文件也在每次工具调用时重读，不需要重启；该文件无法加载时保留上一份成功的列表，见字段速查 |
| `agent.disabled_tools` | 可选的内置工具名数组，取值限定为内置工具表（见 config.schema.json）。列出的工具不出现在 `tools/list` 中，调用返回 `policy_denied`；缺省或空数组表示全部开启。与 `agent.authz` 一样热加载，无需重启 |
| `agent.exec` 的配额数字、`agent.pty` 的上限、`agent.quota` | 热加载，`agent.quota` 对之后的调用生效。已经存在的会话不因为上限调低而被杀掉；新的 open 使用新上限 |
| `agent.backends` 增删 | 下一轮后端监督循环。删除配置会关闭对应进程 |
| `gateway.pats` | 每个 HTTP 请求读取当前凭据和 scope；scope 变更后创建新 MCP 会话 |
| `gateway.quota` | 热加载。对之后的调用生效，已经放行的调用保留原来的截止时间 |
| `gateway.expose_backends` | 热加载。已连接的客户端会被通知重新读取工具列表 |
| `gateway.access`、`gateway.hosts` | 每个请求读取，立即生效；见 [CTR-GW-07](gateway.md#ctr-gw-07) |
| `gateway.listen` | 热加载。运行中的 Gateway 先绑定新地址，成功后再放弃旧地址，Agent、PTY 与 exec 会话不受影响；新地址被占用时 Gateway 留在旧地址上，在 `apply_errors` 与状态警告里报告。同一个端口只是放宽绑定范围（例如 `127.0.0.1:8080` 改为 `:8080`）而系统不允许两个监听并存时，先关旧监听再绑新的，新的失败就恢复旧的 |
| `node.roles` 中的 `gateway` | 热加载。加入时创建 Gateway（令牌不存在才创建，见 [CTR-GW-07](gateway.md#ctr-gw-07)）并开始监听，去掉时停止监听并关闭它的 MCP 会话；Agent、网络和其他角色不受影响。启动失败（例如端口被占用）时节点照常运行，在 `apply_errors` 与状态警告里报告 |
| `node.roles` 中的 `ingress` 与 `ingress` 段 | 热加载。加入时检查 `tag:ingress`、登录口令文件并开始监听，去掉时停止监听并关闭它的 MCP 会话；`ingress` 段有任何改动时入口先停再按新配置启动，因为新的监听可能要用同一个端口。已通过入口登录的客户端需要重新授权（OAuth 令牌只在入口进程内存里，见 [CTR-OAUTH-01](oauth-authz.md#ctr-oauth-01)）。新配置在启动前就能判定不可用（例如读不到证书）时，入口保持原样；启动时才失败则恢复原来的配置，两种情况都在 `apply_errors` 与状态警告里报告。Agent、网络和其他角色不受影响 |
| `ci` 段 | 热加载。新增、删除绑定，改并发数、标签、暂停：内核只动变化的绑定，其他绑定的运行器不受影响；改账号只影响之后的注册与注销。段里没有密钥（[CTR-CTL-15](control.md#ctr-ctl-15)） |
| `node.name`、`node.roles` 里 `agent` 的增减、`audit`、`agent.policy.windows_appcontainer` | 需要重启（`config.RestartPaths`）。文件保存后 `restart_required` 为真，`restart_paths` 列出这些段；运行中的进程继续使用启动时的值，直到下一次启动 |

#### 热应用失败时

有效的配置可以在应用时失败：新的监听地址被占用，Gateway 没能启动，入口的证书读不到或端口拿不到。此时运行中的进程保持原样，该段不进入 `restart_paths`（重启也不会让占用的端口变空），而是：

- `POST /v2/config/reload` 的 `apply_errors` 列出 `{section, message}`；
- 状态警告 `policy_reload_failed` 带同样的文字，`policy_reload_failures` 计数加一，并写一条 `policy_reload_failed` 审计事件；
- 之后的新配置，或再次调用 `POST /v2/config/reload`（它会重试上一次失败的部分），应用成功时清除。

文件监视不会自行反复重试，所以端口一直被占用时不会刷屏。

#### 为什么这几段需要重启

`node.name` 是启动时的节点名称；`node.roles` 里的 `agent` 持有 PTY、exec、租约与沙箱运行器；`audit` 在启动时创建审计目录与保留期的清理任务。这些启动资源的重建会影响正在服务的会话。版本 2 的网络组件独立重启，逻辑会话由设备链路服务保留。因此这些段只置 `restart_required`，由运维或 UI 在合适的时机重启。`agent.policy.windows_appcontainer` 的运行器在启动时安装并做启动恢复，同样只在启动时生效。Gateway 与入口只是进来的路径，不保存会话状态（[INV-03](../constraints/invariants.md#inv-03)），所以可以在进程内启停和换地址；入口只丢掉自己进程里的 OAuth 令牌。

#### 字段速查

| 字段 | 热加载 | 需重启 |
| --- | --- | --- |
| `node.name` | | 是 |
| `node.roles`（`gateway`、`ingress` 的增减） | 是 | |
| `node.roles`（`agent` 的增减） | | 是 |
| `mesh.lan.*`、`mesh.tailnet.*`、`mesh.cloudflare.*`（版本 2） | 是 | |
| `schema_version` 与旧版 `mesh.*` | | 是 |
| `gateway.listen`、`gateway.expose_backends` | 是 | |
| `gateway.quota` | 是 | |
| `gateway.access`、`gateway.hosts` | 是 | |
| `ingress.*` | 是 | |
| `agent.policy.windows_appcontainer` | | 是 |
| `agent.policy.*`（除上一项）、`agent.cwd_policy`、`agent.authz`、`agent.exec`、`agent.pty`、`agent.quota`、`agent.backends`、`agent.workspaces`、`agent.default_cwd` | 是 | |
| `gateway.pats` | 是 | |
| `audit.*` | | 是 |

### Windows L2 开关

`agent.policy.windows_appcontainer`（布尔，默认 `false`）控制 Windows 节点是否在启动时安装托管 AppContainer 运行器。它是实验性开关：

- 为 `false` 或节点不是 Windows 时不安装运行器，Windows 上的 L2 请求保持 `policy_denied`，规则名 `sandbox.unavailable`，行为与未引入此开关时一致。
- 为 `true` 时，启动流程在状态目录的 `sandbox` 子目录建立运行器，并先完成一次启动恢复，回收上一次进程异常退出遗留的日志、ACL 授权与 AppContainer profile。运行器创建或恢复失败时服务照常启动，L2 继续失败关闭，`sys.info` 的 `warnings` 给出原因，细节写入节点日志。
- 运行器只在 `isolation: L2` 的调用中使用，`isolation` 仍可热加载。L1 与 L2 之间的切换从下一次工具调用起生效。
- stdio 后端的 L2 不经过托管运行器，仍然失败关闭。
- 运行器为工作目录里的每个对象授权并回收，所以工作目录（根、文件和子目录合计）最多 10000 个对象。超过时该次调用返回 `policy_denied`，规则名 `sandbox.workspace_too_large`，字段 `limit` 为上限；检查先于 profile 与围栏的创建，不留下状态。`agent.default_cwd: ~` 之类指向用户主目录的取值通常超限，应改为具体的项目目录或在调用中指定 `cwd`。
- 非 Windows 节点配置为 `true` 不会报错，`sys.info` 的 `warnings` 提示该开关不生效。

### 密钥

`authkey_file` 指向的文件内容不写入日志、审计或 `status` 的默认输出。`status --json` 只报告该文件是否可读。OAuth 客户端密钥若由动态注册生成，只在注册响应里出现一次，落盘时权限为仅所有者可读。

`gateway.pats` 是名称到 `{token_file, scopes}` 的映射，scope 使用 OAuth 相同语法。配置非空时，本机 Gateway 的所有请求均须携带有效 Bearer。凭据文件为普通文件、最多 4096 字节，token 至少 32 字符且无内部空白；Unix 文件禁止 group/other 权限。凭据轮转和撤销逐请求生效，凭据名称参与 MCP 会话绑定。操作示例见[运行手册](../operations.md#本机-gateway-pat)。

### 示例

方案中的完整示例位于 [节点设计](../design/04-agent.md)。它应当能通过本模式校验。仓库里的正例文件是 [schemas/examples/xmesh.config.json](schemas/examples/xmesh.config.json)（YAML 的 JSON 等价物，便于不引入 YAML 解析器的校验）。
