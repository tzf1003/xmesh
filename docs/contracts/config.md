# 配置

## CTR-CFG-01

配置文件是 YAML 1.2，路径由 `--config` 指定，默认 `xmesh.yaml`。解析后的数据必须满足 [schemas/config.schema.json](schemas/config.schema.json)。校验失败时进程拒绝启动，错误信息指出 JSON Pointer 路径，例如 `/agent/backends/ida/policy`。

### 角色联动

| 条件 | 要求 |
| --- | --- |
| `roles` 含 `gateway` | `gateway.listen` 的主机为空、`0.0.0.0` 或 `[::]` 表示全部网卡，为 `127.0.0.1` 或 `[::1]` 表示只绑本机；其他地址拒绝启动。默认 `:8080`。`gateway.access` 缺省 `[localhost, lan, tailnet]`，`gateway.hosts` 缺省为空。每个请求都要令牌，见 [CTR-GW-07](gateway.md#ctr-gw-07) |
| `roles` 含 `ingress` | `ingress` 对象必须存在，且 OAuth issuer 为 https |
| `ingress.max_pty_per_client` | 正整数，默认 8；同一入口按 OAuth client 聚合各设备的 active/orphaned PTY，详见 [CTR-GW-06](gateway.md#ctr-gw-06) |
| `ingress.pty_budget_file` | 可选绝对路径；默认使用状态目录中的 `ingress-pty.json`。同一主机的入口可指定同一文件进行跨进程准入，且 `max_pty_per_client` 必须一致 |
| `ingress.pty_budget_authority` | 可选设备名（与 `node.name` 同样的字符集）；多台主机上的入口须配置同一设备，由该 Agent 托管跨主机准入租约，详见 [CTR-GW-06](gateway.md#ctr-gw-06)。留空只在本机协调 |
| `roles` 含 `agent` | `agent.cwd_policy.allow` 必须存在，可以为空；空表示除 `deny` 外的全部目录都可访问 |
| `agent.cwd_policy.deny_file` | 可选绝对路径，指向一个字符串列表的 YAML 文件，其中的目录与 `deny` 一样始终拒绝，与 `allow` 是否为空无关。文件缺失时 agent 不能启动，不会视为空列表。向导默认创建该文件并写入凭证目录 |
| `mesh.mode = tailnet` | `control_url` 与 `authkey_file` 必须存在 |
| `mesh.ephemeral = true` | 仅 tailnet；请求临时节点注册，节点身份只存内存，每次进程启动重新注册，退出时有界尝试注销 |
| `mesh.mode = lan` | `mesh.lan.mdns` 为 true，且不读取 auth key。可选 `mesh.lan.interface` 指定监听、mDNS 通告和浏览用的网卡名，mDNS 组播只经这块网卡收发（Windows 上显式固定组播出口，不受 Clash/Tailscale 等低 metric 隧道网卡影响）；缺省选第一块带私有 IPv4 的启用网卡，并跳过名字像虚拟机、容器、WSL、Hyper-V、VPN 隧道的网卡（只有它们时才退回使用）。指定的网卡不存在或没有私有 IPv4 时拒绝启动 |
| `ingress.listen = funnel` | `control_url` 必须是官方 Tailscale 控制面：`https://controlplane.tailscale.com` 或 `https://login.tailscale.com`（端口只能是 443，不含用户信息）。其他主机，包括 Headscale 和形似官方域名的后缀，一律拒绝启动。issuer 必须是 `ts.net` 源。禁止同时配置 `domain`、`tls`、`cert_file`、`key_file`。域名和证书由 Tailscale Funnel 提供，issuer 使用该节点的 `ts.net` 源 |
| `ingress.listen = public:443` | 必须有 `domain` 和 `tls`，且 `ingress.oauth.issuer` 的主机等于 `domain`。`tls: file` 时还必须有 `cert_file` 与 `key_file`。`tls: autocert` 只在这种监听下使用 |
| `ingress.listen = tunnel` | 与 funnel 一样禁止 `domain` 和 `tls`。这是 [入口方式](../design/03-mesh.md) 里的反向隧道，证书由 Cloudflare Tunnel 或 frp 终止处提供 |
| `ingress.oauth.dynamic_client_scopes` | 可选 scope 数组，语法与 `clients.*.scopes` 相同。动态注册客户端的 scope 上限，缺省 `["devices:read"]`，空数组表示只能完成 OAuth，见 [CTR-OAUTH-01](oauth-authz.md#ctr-oauth-01) |
| `agent.quota`、`gateway.quota` | 可选对象，字段 `calls_per_hour`（正整数，缺省 10000）、`max_concurrent`（正整数，缺省 64）、`max_call_duration`（Go duration，缺省 `10m`）。`agent.exec.default_timeout` 不得超过 `agent.quota.max_call_duration`，见 [CON-SEC-10](../constraints/security.md#con-sec-10) |
| `mesh.mode = relay` | 拒绝启动，见 [CON-NET-08](../constraints/network.md#con-net-08) |

### 时长与尺寸

配置里的时长使用 Go duration 语法：`120s`、`30m`、`720h`。尺寸使用十进制或二进制后缀：`256KiB`、`1MiB`、`10MiB`。模式文件把它们表示成字符串，并给出正则。实现解析失败等同配置校验失败。

### 热加载与冷加载

| 配置段 | 变更如何生效 |
| --- | --- |
| `agent.policy` | 下一次工具调用，见 [CON-SEC-06](../constraints/security.md#con-sec-06) |
| `agent.cwd_policy`、`agent.authz` | 与 policy 一样热加载 |
| `agent.disabled_tools` | 可选的内置工具名数组，取值限定为内置工具表（见 config.schema.json）。列出的工具不出现在 `tools/list` 中，调用返回 `policy_denied`；缺省或空数组表示全部开启。与 `agent.authz` 一样热加载，无需重启 |
| `agent.exec` 的配额数字、`agent.pty` 的上限、`agent.quota` | 热加载，`agent.quota` 对之后的调用生效。`gateway.quota` 不热加载，随 `gateway` 一起需要重启。已经存在的会话不因为上限调低而被杀掉；新的 open 使用新上限 |
| `agent.backends` 增删 | 下一轮后端监督循环。删除配置会关闭对应进程 |
| `gateway.pats` | 每个 HTTP 请求读取当前凭据和 scope；scope 变更后创建新 MCP 会话 |
| `agent.policy.windows_appcontainer` | 不热加载。只在启动时读取，变更后 `status` 提示需要重启；见下方 [Windows L2 开关](#windows-l2-开关) |
| `gateway.access`、`gateway.hosts` | 每个请求读取，立即生效；见 [CTR-GW-07](gateway.md#ctr-gw-07) |
| `node`、`mesh`、`ingress`、`gateway.listen`、`gateway.expose_backends` | 不热加载。`status` 提示需要重启，运行中的进程继续使用启动时的值。`gateway.listen` 的变更也要重启 |

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
