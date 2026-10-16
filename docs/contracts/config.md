# 配置

## CTR-CFG-01

配置文件是 YAML 1.2，路径由 `--config` 指定，默认 `xmesh.yaml`。解析后的数据必须满足 [schemas/config.schema.json](schemas/config.schema.json)。校验失败时进程拒绝启动，错误信息指出 JSON Pointer 路径，例如 `/agent/backends/ida/policy`。

### 角色联动

| 条件 | 要求 |
| --- | --- |
| `roles` 含 `gateway` | `gateway.listen` 必须是回环地址。默认 `127.0.0.1:8080`。非回环地址拒绝启动 |
| `roles` 含 `ingress` | `ingress` 对象必须存在，且 OAuth issuer 为 https |
| `ingress.max_pty_per_client` | 正整数，默认 8；同一入口按 OAuth client 聚合各设备的 active/orphaned PTY，详见 [CTR-GW-06](gateway.md#ctr-gw-06) |
| `ingress.pty_budget_file` | 可选绝对路径；默认使用状态目录中的 `ingress-pty.json`。同一主机的入口可指定同一文件进行跨进程准入，且 `max_pty_per_client` 必须一致 |
| `ingress.pty_budget_authority` | 可选设备名（与 `node.name` 同样的字符集）；多台主机上的入口须配置同一设备，由该 Agent 托管跨主机准入租约，详见 [CTR-GW-06](gateway.md#ctr-gw-06)。留空只在本机协调 |
| `roles` 含 `agent` | `agent.cwd_policy.allow` 至少一条 |
| `mesh.mode = tailnet` | `control_url` 与 `authkey_file` 必须存在 |
| `mesh.ephemeral = true` | 仅 tailnet；请求临时节点注册，节点身份只存内存，每次进程启动重新注册，退出时有界尝试注销 |
| `mesh.mode = lan` | `mesh.lan.mdns` 为 true，且不读取 auth key |
| `ingress.listen = funnel` | `control_url` 必须是官方 Tailscale。Headscale 的主机名拒绝启动。禁止同时配置 `domain`、`tls`、`cert_file`、`key_file`。域名和证书由 Tailscale Funnel 提供，issuer 使用该节点的 `ts.net` 源 |
| `ingress.listen = public:443` | 必须有 `domain` 和 `tls`。`tls: file` 时还必须有 `cert_file` 与 `key_file`。`tls: autocert` 只在这种监听下使用 |
| `ingress.listen = tunnel` | 与 funnel 一样禁止 `domain` 和 `tls`。这是 [入口方式](../design/03-mesh.md) 里的反向隧道，证书由 Cloudflare Tunnel 或 frp 终止处提供 |
| `mesh.mode = relay` | 拒绝启动，见 [CON-NET-08](../constraints/network.md#con-net-08) |

### 时长与尺寸

配置里的时长使用 Go duration 语法：`120s`、`30m`、`720h`。尺寸使用十进制或二进制后缀：`256KiB`、`1MiB`、`10MiB`。模式文件把它们表示成字符串，并给出正则。实现解析失败等同配置校验失败。

### 热加载与冷加载

| 配置段 | 变更如何生效 |
| --- | --- |
| `agent.policy` | 下一次工具调用，见 [CON-SEC-06](../constraints/security.md#con-sec-06) |
| `agent.cwd_policy`、`agent.authz` | 与 policy 一样热加载 |
| `agent.exec` 的配额数字、`agent.pty` 的上限 | 热加载。已经存在的会话不因为上限调低而被杀掉；新的 open 使用新上限 |
| `agent.backends` 增删 | 下一轮后端监督循环。删除配置会关闭对应进程 |
| `gateway.pats` | 每个 HTTP 请求读取当前凭据和 scope；scope 变更后创建新 MCP 会话 |
| `node`、`mesh`、`ingress`、`gateway.listen`、`gateway.expose_backends` | 不热加载。`status` 提示需要重启，运行中的进程继续使用启动时的值 |

### 密钥

`authkey_file` 指向的文件内容不写入日志、审计或 `status` 的默认输出。`status --json` 只报告该文件是否可读。OAuth 客户端密钥若由动态注册生成，只在注册响应里出现一次，落盘时权限为仅所有者可读。

`gateway.pats` 是名称到 `{token_file, scopes}` 的映射，scope 使用 OAuth 相同语法。配置非空时，本机 Gateway 的所有请求均须携带有效 Bearer。凭据文件为普通文件、最多 4096 字节，token 至少 32 字符且无内部空白；Unix 文件禁止 group/other 权限。凭据轮转和撤销逐请求生效，凭据名称参与 MCP 会话绑定。操作示例见[运行手册](../operations.md#本机-gateway-pat)。

### 示例

方案中的完整示例位于 [节点设计](../design/04-agent.md)。它应当能通过本模式校验。仓库里的正例文件是 [schemas/examples/xmesh.config.json](schemas/examples/xmesh.config.json)（YAML 的 JSON 等价物，便于不引入 YAML 解析器的校验）。
