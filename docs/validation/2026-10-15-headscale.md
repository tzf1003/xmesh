# Headscale 互联验收记录

日期：2026-10-15。代码基线：`128372d`。

## 环境

- macOS 27.0.1 arm64 与甲骨文 Ubuntu 24.04 amd64，各运行独立的 xmesh Agent/Gateway。
- Headscale [v0.29.5](https://github.com/juanfont/headscale/releases/tag/v0.29.5)，可执行文件与该版本发布资产的 SHA-256 一致。
- 控制面监听 `127.0.0.1:18090`，Ubuntu 经 SSH 回环端口转发访问；控制流量通过 SSH 隧道传输。此拓扑用于临时验收，公网部署须按 [Headscale TLS 文档](https://headscale.net/stable/ref/tls/)配置入口。
- 独立的 SQLite、Noise key、策略和用户。测试注册 key 为一小时、可复用、ephemeral，赋予 `tag:device`。策略允许这些测试节点相互访问 TCP 7433。
- 两端均启用 `mesh.ephemeral`，分别使用独立的 xmesh 状态目录；DERP map 来自 Tailscale 公共列表。
- Ubuntu bwrap 沙箱共享主机网络，隔离 PID 与文件系统；只读绑定运行库、测试程序与注册 key，工作目录位于临时 tmpfs。使用限定到测试 bwrap 路径的 AppArmor userns profile。

## 结果

| 场景 | 结果 |
| --- | --- |
| macOS → Ubuntu | 发现、Headscale 节点身份、文件读写删除、exec、PTY owner/交互/关闭、清理通过 |
| Ubuntu → macOS | 同上，双向验证通过 |
| DERP 探测 | 独立临时节点查询实际连接状态；`direct` 为空，`DERPRegionID=7`、`DERPRegionCode=tok`，一次观测延迟 1.772404 秒；Agent HTTP 响应检查通过 |
| 沙箱 → macOS、既有 Ubuntu | 连续两轮通过文件、exec、PTY 和清理检查 |
| 既有 Ubuntu → 沙箱 | 每轮反向工具调用通过 |
| 沙箱重启 | 两轮 Headscale 稳定节点 ID 分别为 `4`、`5`，每轮重新注册身份 |
| 沙箱退出 | 正常退出、观察到离线、未生成 `tailscaled.state*`、临时工作目录清理通过 |

双向工具检查使用 [smoke_peer.py](../../scripts/smoke_peer.py)。沙箱使用 [smoke_sandbox.py](../../scripts/smoke_sandbox.py)，指定本次控制面、两端设备及 Ubuntu observer Gateway。DERP 使用 `Test_TailnetPeerConnectivity`，设置 `XMESH_TEST_TAILNET_REQUIRE_DERP=1`；测试独占新节点状态。

## 清理与验收边界

临时节点均已注销，Headscale 节点列表为空；临时注册 key 已过期并删除。临时控制面、SSH 转发及本任务的 xmesh 验收进程已停止，AppArmor 测试 profile 已卸载。测试机现有 `tailscaled.service` 与 `derper.service` 回读为 active。

本次覆盖两台物理主机及其上的三个逻辑节点。隔离沙箱与既有 Ubuntu 节点共用物理主机，三台物理设备的[门 3 验收](../conformance.md#门-3--阶段-3-互联)仍需补齐；真实 LAN 多播和云产品的出站策略由对应环境单独验收。
