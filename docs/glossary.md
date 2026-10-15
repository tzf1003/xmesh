# 术语表

| 术语 | 含义 |
| --- | --- |
| 节点 | 运行一个 `xmesh` 进程的机器。在 tailnet 上有稳定节点身份。 |
| Agent 角色 | 把本机内置工具和 MCP 后端暴露为 MCP server。本机资源状态的唯一权威。 |
| Gateway 角色 | 无状态 MCP 多路复用器。对本机客户端暴露 `localhost:8080/mcp`，聚合 `local/*` 与 `<peer>/*`。 |
| 入口节点 | 打开 `--ingress` 的节点。在 Gateway 之外增加公网监听和 OAuth 授权服务器。 |
| tailnet | Tailscale 或 Headscale 协调的网络。节点用 tsnet 以用户态加入。 |
| 控制面 | 官方 Tailscale 或自建 Headscale。负责节点密钥、endpoint 交换、ACL、MagicDNS；官方控制面还提供 Funnel。 |
| DERP | 打洞失败时的 HTTPS 中继。可以是官方中继或自建 derper。 |
| 调用方 `caller_id` | `tailnet 节点 ID + MCP session id`。配额、租约和 PTY 所有权都挂在这个身份上。见 [CTR-ID-01](contracts/identity.md)。 |
| owner | 当前拥有某 PTY 会话或某排他租约的 `caller_id`。 |
| fencing token | 排他租约上单调递增的整数。调用携带的 token 必须与当前租约一致，否则拒绝。 |
| 后端 | Agent 挂载的本机 MCP server，传输为 stdio 或 HTTP。工具名形如 `<name>.<tool>`。 |
| 策略 `shared` / `exclusive` / `per-caller` | 后端并发策略。与 exec 的 `mode` 同名但不是同一张表，见 [并发](design/06-concurrency.md)。 |
| `cwd` | 一次工具调用的工作目录参数。Agent 不创建、不管理工作区。 |
| 工作区别名 | 配置里的路径别名，调用时写成 `@别名`，由路径解析器展开。 |
| 入口前缀 | 云端看到的工具名永远是 `<节点>/<工具>`。入口转发时把 `local/` 换成该节点名。 |
| scope | OAuth 客户端被允许的设备与工具前缀。语法见 [CTR-OAUTH-02](contracts/oauth-authz.md)。 |
| tag | Tailscale 节点标签，例如 `tag:device`、`tag:ingress`、`tag:peer`。Agent 用它做第二层授权。 |
| 单租户 | 授权服务器只有一个账号、一个登录页。不为多个租户或用户做隔离。 |
