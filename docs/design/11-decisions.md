# 11. 已决事项与风险

## 已决事项

下列五项都已确认，并同步进正文。实现不得再把它们当成可选项。

- [x] **控制面选 Headscale 还是官方 Tailscale。** 两者都兼容。官方 Tailscale 走 Funnel，Headscale 走 VPS 入口。Headscale 完全自主，但没有 Funnel，需要自备 VPS。
- [x] **是否只服务自己。** 只服务自己（单租户）。入口节点的 OAuth 仍要保留，因为 claude.ai 强制 OAuth。授权服务器做成单用户最小实现，不做租户隔离和用户管理。tailnet 内与 CLI 用节点身份 + PAT 即可。
- [x] **后端工具默认暴露范围。** 默认全部暴露。`favorite` 过滤保留为可选开关（`expose_backends: favorite`）。权衡点是全部暴露时云端工具表变大。
- [x] **审批门。** 不做人工审批。全靠策略引擎自动放行或拦截。禁止出现「等待用户点批准」的挂起状态。
- [x] **PTY 录像。** 不录像。只保留调用日志（open / close / transfer / signal，以及谁在何时写过）。不记录终端字节流。

契约层另外补了若干原文未写死的行为，编号 `RUL-*`，集中在 [契约裁定](../contracts/rulings.md)。那些裁定是为了让测试有唯一期望，不是新的产品方向。若要推翻某条裁定，改裁定文档和受影响的契约，并在本文件留一条记录。

后续修订：

- [RUL-02](../contracts/rulings.md#rul-02)：本机环回在节点已入网后使用本节点的 tailnet ID，`local` 只用于还没有该身份的时候。
- [RUL-06](../contracts/rulings.md#rul-06)：renew / release 的请求参数也叫 `_lease_token`。租约结果里的字段仍叫 `token`。
- [RUL-11](../contracts/rulings.md#rul-11)：命令黑名单匹配规范化命令行，并额外匹配 `sh -c` 的脚本原文。
- [RUL-12](../contracts/rulings.md#rul-12)：`closed` 不占 `max_sessions`，另有 `closed_retain_max`。`pty_limit` 的计数字段是 `in_use`。
- [RUL-13](../contracts/rulings.md#rul-13)：带 `oauth_client` 的 orphaned 会话只能由同一个 OAuth client 接管。
- [CON-SEC-11](../constraints/security.md#con-sec-11)：入口的注册和登录必须限速。

## 风险

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| claude.ai 自定义连接器已连接，但工具未传给模型（社区有此类反馈） | 阶段 4 验收失败 | 阶段 4 先用 Claude Code 验证 Gateway，再在目标 plan 上实测 claude.ai |
| 云沙箱禁止出站 UDP，或限制出站域名 | 沙箱内 tsnet 直连不可用 | 该路径仅作可选。主路径始终是云端 → 公网 Gateway |
| 官方 DERP 国内延迟高 | 中继流量慢 | 优先保证打洞成功率。延迟不可接受时自建 DERP，并在管理后台加为 region |
| IDA 等 GUI 后端需要用户登录会话 | 无人值守时后端不可用 | macOS / Windows 用用户级服务。文档化：设备需保持登录 |
| ConPTY 行为与 Unix PTY 差异（信号、resize、控制序列） | Windows 终端体验不一致 | `pty` 包按平台单独测试。默认 `pwsh` |
| `exec` 被诱导执行破坏性命令 | 设备损坏 | 命令策略自动拦截 + L1/L2 隔离 |
| 工具表膨胀导致模型选错设备或工具 | 调用质量下降 | 设备前缀 + `devices.list`。工具表过大时再开 `favorite` 过滤 |
