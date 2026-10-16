# 网络约束

## CON-NET-01

Agent 角色的 MCP 监听地址必须是 tsnet 监听器，或 `lan` 模式下只绑定已配对对端可连的地址。禁止把 Agent 端口绑定到 `0.0.0.0` 后直接暴露到公网，也禁止对 Agent 端口做端口映射来代替入口。

需要公网 URL 时，只允许 Ingress 角色监听，见 [CON-NET-06](#con-net-06)。

## CON-NET-02

未加入同一 tailnet、且未完成 `lan` 配对的客户端，不能拨到 Agent。阶段 1 的本机验收监听 `127.0.0.1`，该监听不得成为阶段 3 之后的默认对外地址。

## CON-NET-03

tailnet 接入使用 `tailscale.com/tsnet`。安装文档和 `install` 子命令不得要求先安装官方 Tailscale 客户端，不得要求 root，不得创建 TUN。

ACL、MagicDNS 和密钥仍由控制面负责。xmesh 不自建第二套节点身份。

## CON-NET-04

切换官方 Tailscale 与 Headscale 时，只改变 `mesh.control_url` 和 auth key 的签发方。业务代码里禁止出现「如果是 Headscale 就走另一套 RPC」的分支。Funnel 只在官方控制面可用；Headscale 下把 `ingress.listen: funnel` 视为配置错误并拒绝启动。管理控制面的 `xmesh mesh` 子命令与 `meshadmin` 不在数据通路上，是该规则唯一的例外，见 [CON-MESH-01](meshadmin.md#con-mesh-01)。

## CON-NET-05

tsnet 的打洞失败时，流量必须能够经由 DERP（官方或 `derper`）完成 MCP 请求。实现不得把「UDP 不通」当成节点离线。

自建 DERP 配置为唯一 region 时，节点不得再优先尝试境外官方 region。这是部署配置，不是另一套传输代码。

## CON-NET-06

`--ingress` 或 `roles` 含 `ingress` 的节点，在原有 Agent/Gateway 之外增加公网监听和 OAuth 授权服务器。它仍用 tsnet 拨号到目标 Agent，转发时剥前缀、鉴权、审计。禁止把入口实现成反向代理到某个固定 IP 的独立项目。

`ingress.listen` 取值：

| 值 | 行为 |
| --- | --- |
| `funnel` | `tsnet.ListenFunnel`。仅官方控制面 |
| `public:443` | 在 VPS 上监听公网 443，证书按 `tls: autocert` 或显式证书文件 |
| `tunnel` | 本进程仍只监听本机，由 Cloudflare Tunnel 或 frp 把该端口公布出去。文档把隧道标为额外依赖 |

## CON-NET-07

入口节点在控制面 ACL 里使用 `tag:ingress`。目标 Agent 对来自该 tag 的连接应用 `agent.authz` 中 `tag:ingress` 的后端与工具允许列表，即使用户已经在入口过了 OAuth。入口被攻破时，没有在 authz 里放开的后端仍然不可达。

`tag:gw-*` 与 `tag:cloud` 的 ACL 只能访问 `tag:device` 的 Agent 端口。这是网络层约束，Agent 仍要做 WhoIs 检查。

## CON-NET-08

本期禁止实现 `mesh.mode: relay`，禁止依赖 go-libp2p。配置出现 `relay` 时进程拒绝启动并说明该模式未实现。

## CON-NET-09

`mesh.mode: lan` 时，节点发现使用 mDNS / DNS-SD。数据面使用 mTLS。首次配对由 `xmesh pair` 交换节点证书，未配对的对端连接必须在 TLS 握手阶段失败，而不是连上后再用应用层口令补救。

`lan` 模式不提供 Funnel，也不实现 OAuth。云端客户端要接入时，仍需一台走公网的入口，入口背后可以是 tailnet。
