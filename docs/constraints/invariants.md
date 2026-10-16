# 不变量

不变量描述系统在任何成功或失败的调用之后都必须保持的性质。它们跨多条契约，单测应直接针对性质，而不是只针对某一次工具的快乐路径。

## INV-01

同一 exclusive 资源在任意时刻至多有一个未过期持有者，且该持有者的 fencing token 是这个资源上当前唯一被接受的 token。

- 过期、显式释放、进程重启、后端崩溃重拉，都会使旧 token 永久失效。
- 新 token 严格大于该资源曾经发出过的任何 token。进程重启后可以从 1 重新计，因为旧进程的 token 已全部作废；同一进程生命周期内禁止回绕到已用过的值。uint64 耗尽视为不可恢复错误并拒绝新租约。
- 关联：[CTR-BE-02](../contracts/backend-lease.md#ctr-be-02)、[CON-CONC-07](concurrency.md#con-conc-07)。

## INV-02

在任意观察点，一台 Agent 上状态为 `active` 或 `orphaned` 的 PTY 会话数 ≤ `pty.max_sessions`，且每个 `caller_id` 名下的 `active` 会话数 ≤ `pty.max_per_caller`。

`closed` 不占用 `max_sessions`，也不占用 `max_per_caller`。进程已经退出，留下的只是环形缓冲。`closed` 的数量 ≤ `pty.closed_retain_max`，超出时立刻释放最老的 `closed`，见 [RUL-12](../contracts/rulings.md#rul-12)。`orphaned` 占用设备总额，但不占用原 caller 的 `max_per_caller`，否则断线会把个人配额卡死到宽限结束。关联：[CON-CONC-03](concurrency.md#con-conc-03)、[CTR-PTY-01](../contracts/pty.md#ctr-pty-01)。

在按会话（`caller_id`）计数的上限之外，Agent 还按主体统计 `active` 与 `orphaned` 终端：主体优先取可信的 OAuth client，否则取节点 ID，同一主体的多个 MCP 会话共享，本机 Gateway 转发的会话共用一个节点主体。该合计达到 `pty.max_per_caller` 时 `open` 返回 `policy_denied`、规则 `quota.exceeded`；`transfer` 的接收方同样计入。因此断线后的 `orphaned` 终端仍占着原主体的额度，直到被接管、回收或进程退出。

## INV-03

Gateway 角色和 Ingress 角色重启后，不需要恢复 PTY、租约、幂等缓存、文件锁或 exec 输出分页。这些状态只存在于目标 Agent。

Gateway 持有到对等节点的 MCP 连接、发现缓存、本连接上的 `session.set_defaults`，以及审计写入缓冲。入口另外持有 [CTR-GW-06](../contracts/gateway.md#ctr-gw-06) 的准入治理记录：设备 ID、Agent epoch 与待完成标志；实际 PTY 数量和归属均从 Agent 重新查询。发现缓存过期或连接断开后可以重建，丢失不改变设备上的资源归属。准入记录保存在入口状态目录，用于在入口重启后继续核对离线设备和结果未确认的调用。

## INV-04

发往后端 MCP 进程或后端 HTTP 端点的 JSON-RPC 参数里，不包含 `_lease_token`，也不包含任何由 Agent 注入的租约字段。

token 只在 Agent 边界校验。关联：[CTR-BE-01](../contracts/backend-lease.md#ctr-be-01)、[CON-TOOL-11](tools.md#con-tool-11)。

## INV-05

下列失败返回之前，目标路径的文件内容、进程表、PTY 会话表、租约表和后端进程的 MCP 会话状态与调用开始时一致：

- `policy_denied`
- `invalid_cwd`（解析失败，未启动命令、未打开文件写入）
- 授权拒绝（scope 或 tag）
- `lease_required` / token 不匹配导致的 `resource_busy`
- `pty_limit`
- `conflict`（哈希不匹配）

读操作和 `pty.read` 不在此列。`exec.run` 一旦获得槽位并启动进程，之后的非零退出码是结果，不是「无副作用」。

## INV-06

`fs.write` 与 `fs.edit` 的结果对并发读者要么是旧的完整文件，要么是新的完整文件。禁止读者观察到临时文件内容或半截新内容。实现途径是同一目录下的临时文件加 `rename`。跨文件系统的 rename 失败时必须删除临时文件并返回错误，而不是退化为原地覆盖。

## INV-07

一条 PTY 会话的 `cursor` 等于该会话启动以来写入环形缓冲的总字节数，单调不减。读者提交的 cursor 若小于缓冲中仍保留的最小偏移，响应 `truncated: true`，并且 `data` 从当前保留的最小偏移开始。实现不得用错误码代替 `truncated`，也不得改写已经被读者读过的历史偏移的含义。

## INV-08

从入口节点看到的工具名匹配 `<节点名>/<工具名>`。`local/` 只出现在直连该节点 Gateway 的本机客户端视图里。入口在转发前把 `local/` 换成自己的 `node.name`。云端客户端的 `tools/list` 结果里禁止出现 `local/`。

## INV-09

对等节点从 tailnet 状态中消失，或 `lan` 发现超时后，Gateway 在下一次 `tools/list` 以及随后的调用中不再暴露或路由该节点的工具。已经发出、尚未返回的调用可以以传输错误结束，不得改派到另一台设备。

发现缓存的 30 秒上限是能力标签的缓存，不是「节点已离线仍继续路由」的许可证。连接断开即摘除路由。

## INV-10

审计记录可以包含工具名、路径的规范化形式、参数键名、长度、哈希和错误码。禁止包含：`fs.read` / `fs.write` / `fs.edit` 的文件正文、`exec` 的 stdout/stderr/stdin、`pty.write` 的 data、`pty.read` 返回的 data、环境变量的值。关联：[CTR-AUD-01](../contracts/audit.md)、[CON-SEC-07](security.md#con-sec-07)、[CON-SEC-08](security.md#con-sec-08)。
