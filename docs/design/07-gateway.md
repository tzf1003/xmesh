# 7. Gateway 角色与入口节点

Gateway 角色是一个无状态的 MCP 多路复用器，每个节点都跑。对本机暴露 `localhost:8080/mcp`。工具表 = `local/*` + 所有在线对等节点的 `<peer>/*`。内部为每个对等节点维护一个 MCP client（经 tsnet 拨号）。转发时只做剥前缀、鉴权、审计。

入口节点是同一套代码再加一个公网监听器和 OAuth 服务，并把 `local/` 换成自己的节点名。云端因此永远看到 `<节点>/<工具>`，看不到 `local/`。

## 设备发现

- 定期 `LocalClient().Status()` 读取 tailnet peers，过滤 `tag:device`。上线即连，下线即摘。
- 对每台设备调 `sys.info` 获取能力标签（OS、后端列表），缓存 30 秒。
- 设备工具表变化时，向已连接的上游发 `notifications/tools/list_changed`。

下线节点的工具不得继续被路由，见 [INV-09](../constraints/invariants.md#inv-09)。发现的时序和缓存失效见 [CTR-GW-04](../contracts/gateway.md#ctr-gw-04)。

`lan` 模式下用 mDNS / DNS-SD 代替 `Status()`，身份来自配对时交换的节点证书，而不是 `WhoIs`。授权结论仍必须落到与 tag 等价的角色上，见 [CTR-AUTHZ-01](../contracts/oauth-authz.md#ctr-authz-01)。

## 工具命名与暴露

- 命名 `<device>/<tool>`：`win-dev-01/ida.decompile_function`、`mac-m3/pty.open`。设备名前缀是必要的，因为 tailnet 内直连的 Claude Code 会同时看到本地 `Read` 和 `win-dev-01/fs.read`。
- 额外提供 `devices.list`（id、OS、标签、在线、负载）和 `tools.search(device, pattern)`。
- 默认全部暴露（`expose_backends: all`）。设备多到工具表膨胀时，可以切到只暴露内置工具和标记为 `favorite` 的后端工具，其余按需搜索。

Gateway 自身的工具（不带设备前缀，因为它们描述的是聚合视图）：

| 工具 | 作用 |
| --- | --- |
| `devices.list` | 在线设备与能力摘要 |
| `tools.search` | 按设备与模式查找未默认展开的工具 |
| `session.set_defaults` | 为本 MCP 连接设置默认 device / cwd / env |

契约见 [CTR-GW-01](../contracts/gateway.md#ctr-gw-01) 至 [CTR-GW-03](../contracts/gateway.md#ctr-gw-03)。

## 会话默认值

`session.set_defaults({device, cwd, env})`：每个 MCP 连接设一次。后续调用省略 `device` / `cwd` 时，由 Gateway 在转发前补全。Agent 不保存这个状态。连接关闭后默认值消失。

入口节点上的默认 device 同样只活在该公网 MCP 会话里。

## OAuth 2.1（仅入口节点启用）

| 端点 | 用途 |
| --- | --- |
| `/.well-known/oauth-protected-resource` | 资源元数据。客户端据此发现授权服务器 |
| `/.well-known/oauth-authorization-server` | 授权服务器元数据 |
| `POST /oauth/register` | 动态客户端注册（RFC 7591）。claude.ai 与 Codex 都依赖此流程 |
| `GET /oauth/authorize` · `POST /oauth/token` | 授权码 + PKCE |

每个 OAuth client 绑定一组 scope：`devices:read`、`exec:<device>`、`pty:<device>`、`fs:write:<device>`、`backend:<device>:<name>`。配置示例里的 `exec:*`、`fs:*` 是通配写法，语法见 [CTR-OAUTH-02](../contracts/oauth-authz.md#ctr-oauth-02)。

入口只接受 OAuth access token，没有 PAT 通道。PAT 与节点身份只用于绑定在 `127.0.0.1` 的本机 Gateway（来自回环以外地址的请求一律拒绝），不走浏览器登录，也不限定 `mesh.mode`。单租户：授权服务器只有一个账号和一个登录页。Codex 与 Claude 各注册为一个 client。不做租户隔离，也不做用户管理。

端点、PKCE、动态注册的字段级契约见 [CTR-OAUTH-01](../contracts/oauth-authz.md#ctr-oauth-01)。

## 审计与配额

- 每次 tool 调用记录 caller、device、tool、参数摘要、结果状态、耗时。
- PTY 不录像，只记 open / close / transfer / signal 事件，以及谁在什么时候写过。不记录写入内容。
- 配额按 client：每小时调用数、并发 PTY 数、单任务时长。

记录的字段和明确不记录的内容见 [审计契约](../contracts/audit.md)。

## 接入步骤

| 客户端 | 操作 |
| --- | --- |
| claude.ai | Settings → Connectors → Add custom connector，填 Gateway URL，完成 OAuth |
| Claude Code | `claude mcp add --transport http xmesh <url>` |
| Codex CLI / App | `codex mcp add xmesh --url <url>` 后 `codex mcp login xmesh` |
| Codex Cloud | 环境设置中配置同一 URL（以当前 Codex 文档为准） |

阶段 4 的验收顺序：先用 Claude Code 验证 Gateway，再在目标 plan 上实测 claude.ai。社区有「连接器已连接但工具未传给模型」的反馈，不能把连接器显示已连接当成工具可见。
