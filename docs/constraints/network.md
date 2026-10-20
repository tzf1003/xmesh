# 网络约束

## CON-NET-01

设备链路监听器使用 tsnet 或所选 LAN 网卡地址；两者独立启用，均要求 xmesh 设备信任与 mTLS。禁止把 Agent 端口绑定到 `0.0.0.0` 后直接暴露到公网，也禁止对 Agent 端口做端口映射来代替入口。

LAN 网卡默认仅接受私有 IPv4。显式配置 `mesh.lan.interface` 时，也允许使用系统 Tailscale 网卡的 `100.64.0.0/10` IPv4；该路径沿用 LAN 的设备信任与 mTLS，须关闭 mDNS 并按地址配对。

需要公网 URL 时，只允许 Ingress 角色监听，见 [CON-NET-06](#con-net-06)。

## CON-NET-02

设备须在可达的 LAN 或 Tailnet 路径上完成 xmesh 配对，才能调用远端 Agent。网络可达不授予应用权限。开发用本机验收监听 `127.0.0.1`，该监听不得成为默认对外地址。

## CON-NET-03

tailnet 接入使用 `tailscale.com/tsnet`。安装文档和 `install` 子命令不得要求先安装官方 Tailscale 客户端，不得要求 root，不得创建 TUN。

已有系统 Tailscale 的用户可选择上述显式网卡监听方式，复用操作系统路由；这不改变默认 tsnet 接入，也不增加安装前置依赖。

Tailnet 的网络准入、ACL、MagicDNS 和网络密钥由控制面负责。xmesh 设备身份来自独立的持久公钥，配对信任及应用角色适用于 LAN 与 Tailnet，网络节点 ID、WhoIs 或控制面标签不授予 xmesh 信任。

## CON-NET-04

切换官方 Tailscale 与 Headscale 时，只改变 `mesh.tailnet.control_url` 和入网凭据。业务代码使用统一设备链路。Funnel 只在官方控制面可用；Headscale 下把 `ingress.listen: funnel` 视为配置错误并拒绝启动。管理控制面的 `xmesh mesh` 子命令与 `meshadmin` 使用各控制面的管理接口，见 [CON-MESH-01](meshadmin.md#con-mesh-01)。

## CON-NET-05

tsnet 的打洞失败时，流量必须能够经由 DERP（官方或 `derper`）完成 MCP 请求。实现不得把「UDP 不通」当成节点离线。

自建 DERP 配置为唯一 region 时，节点不得再优先尝试境外官方 region。这是部署配置，不是另一套传输代码。

## CON-NET-06

`--ingress` 或 `roles` 含 `ingress` 的节点，在 Agent/Gateway 之外增加公网监听和 OAuth 授权服务器。入口使用已配对设备的逻辑链路调用目标 Agent，路径遵循 LAN 优先与健康连接保持规则，转发时剥前缀、鉴权、审计。禁止把入口实现成反向代理到某个固定 IP 的独立项目。

`ingress.listen` 取值：

| 值 | 行为 |
| --- | --- |
| `funnel` | `tsnet.ListenFunnel`。仅官方控制面 |
| `public:443` | 在 VPS 上监听公网 443，证书按 `tls: autocert` 或显式证书文件 |
| `tunnel` | 本进程仍只监听本机，由 Cloudflare Tunnel 或 frp 把该端口公布出去。文档把隧道标为额外依赖 |

## CON-NET-07

目标 Agent 在 xmesh 设备信任中授予入口 `tag:ingress`，并应用 `agent.authz` 中该角色的后端与工具允许列表，即使用户已经在入口完成 OAuth。入口被攻破时，没有在 authz 里放开的后端仍然不可达。

控制面 ACL 可进一步限制网络角色能访问的设备端口；Agent 始终按 xmesh 公钥信任验证设备及本地授予的应用角色。撤销 Tailnet 准入只影响网络路径，撤销 xmesh 信任结束该设备全部会话。

## CON-NET-08

本期禁止实现 `mesh.mode: relay`，禁止依赖 go-libp2p。配置出现 `relay` 时进程拒绝启动并说明该模式未实现。

## CON-NET-09

LAN 发现使用 mDNS / DNS-SD，Tailnet 从网络状态取得候选地址，再通过 TLS 取得公开身份。业务数据面使用 mTLS。首次配对有两条流程，都在授予公钥信任之前由双方核实对方身份：

- 交换配对文档：`xmesh pair` 或面板的高级选项，用户在另一台设备上亲自核对指纹。
- 连接密码：一台设备设置口令，另一台输入它。SPAKE2 确认 transcript 绑定双方版本 2 身份文档，口令本身不离开设备，也不落盘。错误口令与中间人攻击无法在客户端区分，两者都不会授予设备信任，并计入节点限流。

未配对对端的普通握手仍在 TLS 层失败。引导 ALPN `xmesh-pair/2` 只开放 `/mesh/v2/pair/*`，其余路径返回 404。详见 [设备连接密码与发现](../design/14-password-pairing.md)。

Funnel 要求启用 Tailnet。云端 MCP 客户端通过入口的公网 URL 与 OAuth 接入；入口向设备转发仍使用同一份 xmesh 信任及设备链路。
