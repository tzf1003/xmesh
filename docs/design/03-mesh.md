# 3. 互联与部署形态

互联层复用 Tailscale 开源栈。控制面两种都兼容：官方 Tailscale（有 Funnel，可以零 VPS）或自建 Headscale（内嵌 DERP，入口走 VPS）。xmesh 只是 `control_url` 不同。

xmesh 用 tsnet 以用户态加入 tailnet，不需要安装 Tailscale，不需要 root，也不需要 TUN 设备。tsnet 自带打洞和 DERP 中继，跨 NAT、跨网络都能连。官方控制面下不用 VPS 也能跑；Headscale 或自建 DERP 时需要一台机器承担控制面或中继。

约束见 [网络](../constraints/network.md)。

## 互联组件

| 组件 | 作用 | 说明 |
| --- | --- | --- |
| 控制面（官方 Tailscale 或 Headscale） | 协调服务器：节点密钥、endpoint 交换、ACL、MagicDNS、Funnel | 官方：托管、免维护、有 Funnel。Headscale：自主、内嵌 DERP、没有 Funnel，入口走 VPS |
| derper | 自建 DERP 中继。UDP 打洞失败时走 HTTPS 443 兜底 | 国内延迟远好于官方 DERP。配置为唯一 region |
| tsnet（Agent / Gateway 内嵌） | Go 程序作为 tailnet 节点监听与拨号 | 用 `LocalClient().WhoIs()` 识别对端身份 |
| Pre-auth key（ephemeral + tag） | 云沙箱里的 tsnet 节点一次性加入，会话结束自动消失 | 给 Codex Cloud / Claude Code 云沙箱的 setup 脚本用 |

## 网络模式（`mesh.mode`）

| 模式 | 发现与传输 | 适用 |
| --- | --- | --- |
| `tailnet`（默认） | tsnet + 控制面（官方 Tailscale 或 Headscale）。打洞失败走 DERP（官方或自建） | 跨网络、跨 NAT |
| `lan` | mDNS / DNS-SD 发现 + mTLS 直连。首次配对交换节点证书 | 同网段、零基础设施的兜底 |
| `relay`（本期不做） | 脱离 Tailscale 协议时的替代。候选 go-libp2p（Circuit Relay v2 + DCUtR） | 未来如需去掉 Tailscale 依赖 |

`relay` 在本期禁止出现在实现和默认配置中，见 [CON-NET-08](../constraints/network.md#con-net-08)。

## 入口节点（ingress）

云端 Codex / Claude 没有 tailnet，只能访问一个 URL。任意 xmesh 节点加上 `--ingress` 就成为公网 MCP 端点：它仍是普通对等节点，多开一个公网监听器和 OAuth 服务，按前缀把请求路由到 tailnet 内的目标节点。云端完全不知道 tailnet 存在。

两种入口都要兼容：

- 官方控制面下用 Funnel，让本机直接对外。零 VPS，但流量始终经过 Tailscale 中继服务器，吞吐和延迟一般。
- Headscale 或已有 VPS 时用 VPS 入口节点。公网直达，性能最好。

| 暴露方式 | 地址 | 条件 | 适用 |
| --- | --- | --- | --- |
| VPS 入口节点 | `https://mcp.example.com/mcp` | 公网 VPS，Let's Encrypt 证书 | 性能最好；Headscale 下的唯一入口 |
| Tailscale Funnel（`tsnet.ListenFunnel`） | `https://mac-m3.<tailnet>.ts.net/mcp` | 仅官方控制面。流量始终经 Tailscale 中继服务器，带宽与延迟一般 | 零基础设施、低流量 |
| 反向隧道（Cloudflare Tunnel / frp） | 自定义域名 | 任意节点出站即可 | 没有 VPS，也不用官方 Tailscale。多一个依赖 |

入口节点必须实现 OAuth 2.1 + 动态客户端注册。claude.ai 自定义连接器默认走 OAuth 并尝试动态客户端注册，没有无认证选项；Codex 同样走 OAuth。契约见 [CTR-OAUTH-01](../contracts/oauth-authz.md#ctr-oauth-01)。

转发时把 `local/` 前缀换成节点名。云端看到的永远是 `<节点>/<工具>`，见 [INV-08](../constraints/invariants.md#inv-08)。

入口节点在 Tailscale ACL 中打 `tag:ingress`。目标节点按该 tag 做第二层授权，见 [CTR-AUTHZ-01](../contracts/oauth-authz.md#ctr-authz-01)。

## 接入矩阵

| 调用方 | 入口节点在本地（仅 tailnet） | 入口节点在 VPS（公网） | 直连对等节点 |
| --- | --- | --- | --- |
| Claude Code / Codex CLI（本机或 tailnet 内） | 可 | 可 | 可 |
| Codex Cloud / Claude Code 云沙箱（tsnet 加入 tailnet） | 可 | 可 | 可 |
| claude.ai 自定义连接器 / Codex App 云端 | 否，需 Funnel 或公网 | 可 | 否 |

云沙箱是否能真正加入 tailnet，取决于其出站策略。UDP 不通时自动退到 DERP（HTTPS 443），通常可连，但走中继。两家沙箱的实际出站策略在阶段 3 实测，见 [路线图](10-roadmap.md)。这条路径是可选的；主路径始终是云端 → 公网入口。
