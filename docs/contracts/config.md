# 配置

## CTR-CFG-01

配置文件是 YAML 1.2，路径由 `--config` 指定，默认 `xmesh.yaml`。解析后的数据必须满足 [schemas/config.schema.json](schemas/config.schema.json)。校验失败时进程拒绝启动，错误信息指出 JSON Pointer 路径，例如 `/agent/backends/ida/policy`。

### 角色联动

| 条件 | 要求 |
| --- | --- |
| `roles` 含 `gateway` | `gateway.listen` 必须是回环地址。默认 `127.0.0.1:8080`。非回环地址拒绝启动 |
| `roles` 含 `ingress` | `ingress` 对象必须存在，且 OAuth issuer 为 https |
| `roles` 含 `agent` | `agent.cwd_policy.allow` 至少一条 |
| `mesh.mode = tailnet` | `control_url` 与 `authkey_file` 必须存在 |
| `mesh.mode = lan` | `mesh.lan.mdns` 为 true，且不读取 auth key |
| `ingress.listen = funnel` | `control_url` 必须是官方 Tailscale。Headscale 的主机名拒绝启动 |
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
| `node`、`mesh`、`ingress.listen` | 不热加载。`status` 提示需要重启 |

### 密钥

`authkey_file` 指向的文件内容不写入日志、审计或 `status` 的默认输出。`status --json` 只报告该文件是否可读。OAuth 客户端密钥若由动态注册生成，只在注册响应里出现一次，落盘时权限为仅所有者可读。

### 示例

方案中的完整示例位于 [节点设计](../design/04-agent.md)。它应当能通过本模式校验。仓库里的正例文件是 [schemas/examples/xmesh.config.json](schemas/examples/xmesh.config.json)（YAML 的 JSON 等价物，便于不引入 YAML 解析器的校验）。
