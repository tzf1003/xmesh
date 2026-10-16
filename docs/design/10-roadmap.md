# 10. 落地路线图

五个阶段。工具层先于互联层：前两段完全在本机开发测试，不依赖网络。原文图为 5 个阶段、4 个验收门。本库把阶段 1 到阶段 4 结束时的验收当作四道门；阶段 5 是过门之后的硬化，仍有自己的验收标准，但不作为进入下一阶段的门槛。

```mermaid
flowchart LR
  p1["阶段 1<br/>工具基础"] --> g1["门 1<br/>本机工具与 PTY 配额"]
  g1 --> p2["阶段 2<br/>后端与并发"]
  p2 --> g2["门 2<br/>租约与崩溃拉起"]
  g2 --> p3["阶段 3<br/>互联"]
  p3 --> g3["门 3<br/>三设备 WhoIs"]
  g3 --> p4["阶段 4<br/>Gateway"]
  p4 --> g4["门 4<br/>OAuth 与跨设备工具"]
  g4 --> p5["阶段 5<br/>治理与硬化"]
```

每道门要勾选的约束和契约编号见 [符合性矩阵](../conformance.md)。

| 阶段 | 交付 | 验收标准 |
| --- | --- | --- |
| 1 工具基础 | `cwd` / `fs` / `pty` / `exec` 四个包。Agent 以本机 MCP server 运行 | 单元测试全绿。20 个 PTY 并发 open/read/write，以及超限拒绝测试通过。Claude Code 本地连上能读写文件、开终端 |
| 2 后端与并发 | `mcpmux`、`backend`、`lease`。IDA MCP 以 `exclusive` 挂载 | 两个 MCP client 同时调 `ida.*`，无租约方收到 `resource_busy`。后端崩溃自动拉起，且旧 token 失效 |
| 3 互联 | 接入控制面。官方 Tailscale 与 Headscale 都要跑通，可选自建 DERP。Agent 用 tsnet 监听。三平台服务安装 | 三台设备 `tailscale ping` 直连或走 DERP。`WhoIs` 按 tag 正确过滤后端 |
| 4 Gateway | MCP mux、设备发现、OAuth 2.1 + 动态客户端注册。三种监听模式 | claude.ai 自定义连接器和 Codex 都能完成 OAuth，并调通 `win-dev-01/exec.run`。工具在对话中实际可见 |
| 5 治理与硬化 | 策略拦截规则、配额、沙箱分级、调用日志。云沙箱内 tsnet 节点直连实验 | 高危操作被策略自动拒绝并返回结构化错误。Codex Cloud / Claude Code 云沙箱至少一家能加入 tailnet |

阶段 4 不要把「连接器显示已连接」当成通过。工具必须出现在模型可调用的工具表里，并完成一次真实的 `exec.run`。若 claude.ai 未把工具传给模型，门 4 失败，按 [风险](11-decisions.md) 先用 Claude Code 定位是 Gateway 问题还是连接器问题。

阶段 5 的云沙箱直连是实验项。失败不推翻主路径（云端 → 公网入口），但必须记录该沙箱的出站限制。

## 阶段 6：桌面 UI 与本机控制通道

阶段 6 在五个阶段之外，不是进入下一阶段的门，也不改变阶段 1 到 5 的契约。目标是给 Windows 和 macOS 做托盘 / 菜单栏图标与管理面板，并让 UI 与内核通过本机控制通道解耦：

| 子阶段 | 交付 |
| --- | --- |
| U0 | 控制通道契约、约束与裁定、`api/control/v1` 类型 |
| U1 | 内核只读通道：状态、事件、设备、会话、审计；服务启停子命令 |
| U2 | 内核写操作：drain / resume、配置读写、配对、PAT、入口客户端 |
| U3 | 托盘壳与假内核回放测试 |
| U4 | 本机 web 面板与首次运行向导 |
| U5 | Windows 与 macOS 的自启、签名、安装包，并入 beta 预发布 |
| U6 | 沙箱隔离验收、可访问性、英文资源、升级回滚 |
| U7 到 U10 | UI 端 tailnet 管理：主机 tag、权限划分、第三方 DERP 设置，以及 DERP 提供商集成的客户端基础。见 [13 tailnet 管理与 DERP 提供商路径](13-mesh-admin-and-derp.md) |

U0、U1、U3 不依赖网络环境，可以与阶段 5 并行。方案、控制通道与拆分见 [12 桌面 UI 与本机控制通道](12-ui-and-control.md)。
