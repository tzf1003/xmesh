# 后端与租约

## CTR-BE-01

Agent 把后端的每个工具暴露为 `<backend>.<tool>`。`backend` 是配置键，`tool` 是后端 `tools/list` 返回的名字。两者用第一个 `.` 分割配置键，因此后端名不得包含 `.`。

### 转发

1. 解析工具名，找到后端与原始工具名。
2. 读取并删除参数中的 `_lease_token`，见 [RUL-06](rulings.md#rul-06)。
3. `exclusive`：按 [CTR-BE-02](#ctr-be-02) 校验 token。失败则不向后端发送任何请求。
4. `per-caller`：选择该 `caller_id` 的进程，没有则拉起。
5. 经 [CTR-MUX-01](mcpmux.md#ctr-mux-01) 把请求送入该后端的 actor。
6. 后端的成功或工具错误原样返回。Agent 不包裹后端自己的 `isError` 正文，除非传输失败。

### 启动

stdio 后端的 `command` 数组直接作为进程 argv，不经 shell。工作目录是 `agent.default_cwd`，除非该后端配置了 `cwd`。环境过滤器与 exec 相同，避免 IDA 插件进程继承到 auth key。

stdio 后端可配置 `shutdown_timeout`（正 duration，最大 30 秒）控制关闭 stdin 后的退出宽限期，缺省沿用 250 毫秒。`process_cleanup: tree` 启用受管进程树：Windows 在创建时绑定 Job，Unix 跟踪进程组及具有匹配启动身份的后代。关闭连接后先允许后端保存结果，再回收所属进程。HTTP 后端不接受这两个字段。

UI 测试后端使用 `policy: per-caller`、`shutdown_timeout: 10s` 与 `process_cleanup: tree`。长测试通过后端内部任务 ID 查询，工具提交立即返回，使串行转发期间仍可查询和取消。安装、协议和环境配置见 [UI 测试 MCP](../../packages/ui-mcp/README.md)。

HTTP 后端连接配置的 `url`。该 URL 必须是回环地址或 tailnet 地址。禁止配置公网 URL，以免 Agent 成为开放代理。

### 崩溃

stdio 进程退出或 HTTP 连续失败时：

1. 把后端标为 `crashed`。
2. 若是 `exclusive`，使当前租约失效并递增 fencing 计数，使旧 token 不能再被使用。
3. 按指数退避拉起，最多连续 5 次。仍失败则保持 `crashed`，后续调用返回 `backend_unavailable`。
4. 拉起成功后对所有上游发送 `notifications/tools/list_changed`，见 [CTR-MUX-04](mcpmux.md#ctr-mux-04)。

## CTR-BE-02

内置工具，仅对 `policy: exclusive` 的后端有意义。对 `shared` 或 `per-caller` 调用它们返回 `invalid_args`，并带 `reason: not_exclusive`；后端名不存在时同样 `invalid_args`，带 `reason: unknown_backend`。

### backend.acquire

参数：`name`（后端名）、`ttl`（duration 字符串，必须，最大不超过配置的 `idle_timeout`，默认上限 30 分钟）。

- 当前没有未过期租约：创建租约，token 加 1，owner 为调用方，`Expires = now + ttl`。返回租约对象。
- 已有未过期租约且 owner 是调用方：把 TTL 延长到 `now + ttl`，**不**更换 token。返回同一租约。这使重试 acquire 是幂等的。
- 已有未过期租约且 owner 不是调用方：`resource_busy`，`reason: held`。

### backend.renew

参数：`name`、`_lease_token`、`ttl`。`_lease_token` 必须等于当前租约的 `token` 且 owner 匹配，否则 `resource_busy`（`token_mismatch` 或 `token_stale`）。成功则延长 `Expires`，`token` 不变。参数名见 [RUL-06](rulings.md#rul-06)。

### backend.release

参数：`name`、`_lease_token`。owner 与 `_lease_token` 都匹配时删除租约。token 计数器不减小。重复 release 同一个 `_lease_token` 返回成功，`released: true`，便于重试。不匹配返回 `resource_busy`。

业务工具校验：

| 情况 | 错误 |
| --- | --- |
| 未带 `_lease_token` | `lease_required` |
| 没有有效租约，或 `_lease_token` 不等于当前 `token` | `resource_busy` / `token_stale` |
| `_lease_token` 等于当前 `token` 但 owner 不是调用方 | `resource_busy` / `token_mismatch` |
| `_lease_token` 与 owner 都匹配，但已过期 | `resource_busy` / `token_stale`，并删除租约 |

## CTR-LEASE-01

租约对象：

```json
{
  "resource": "backend:ida",
  "owner": "node-abc/s7f",
  "token": 3,
  "expires": "2026-10-15T06:00:00Z"
}
```

| 字段 | 规则 |
| --- | --- |
| `resource` | `backend:` 加后端名 |
| `owner` | [CTR-ID-01](identity.md#ctr-id-01) |
| `token` | 正整数。同一进程、同一 resource 内只增不减 |
| `expires` | RFC3339 UTC |

存储是 `sync.Mutex` 保护的 map。过期检查发生在 acquire、renew、release 和业务工具入口，也发生在每 30 秒的清扫。清扫删除过期项，不回收 token 计数器。

Agent 进程退出后 map 消失。新进程的第一个 token 是 1。旧 token 自然失效。

模式：[schemas/lease.schema.json](schemas/lease.schema.json)。

### 谁来心跳

Gateway 若代表会话持有租约，应在 TTL 的一半时刻 `renew`，直到上游 MCP 会话结束。Agent 不因为看到业务流量而自动 renew，见 [RUL-03](rulings.md#rul-03)。
