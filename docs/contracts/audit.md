# 审计

## CTR-AUD-01

每次到达 Agent 的工具调用写一条记录。经过入口的调用在入口再写一条，两层都记，因为入口看得到 OAuth client，Agent 看得到最终是否执行。Gateway 在 tailnet 模式下同样写一条转发记录。

MCP 接收层为每次调用写入执行编号、开始时间与层级，`pty.read` 的逐调用记录只保留会话编号和返回字节数。使用旧版 `Record` 接口的 PTY 读取仍按分钟合并成 `pty_io` 摘要。`open`、`close`、`transfer`、`signal`、`write` 各自一条，`write` 只记字节数。

### 事件

```json
{
  "time": "2026-10-15T05:30:00.123Z",
  "event": "tool_call",
  "caller": "node-abc/s7f",
  "oauth_client": "claude",
  "device": "win-dev-01",
  "tool": "exec.run",
  "args_digest": {
    "keys": ["cmd", "args", "cwd"],
    "resolved_path": "C:\\Users\\x\\repos\\foo",
    "cmd": "cargo",
    "args_len": 2,
    "stdin_sha256": null
  },
  "status": "ok",
  "error": null,
  "duration_ms": 842,
  "output_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
}
```

| 字段 | 规则 |
| --- | --- |
| `time` | RFC3339，毫秒，UTC |
| `event` | `tool_call`、`task_complete`、`pty_open`、`pty_close`、`pty_transfer`、`pty_signal`、`pty_write`、`pty_io`、`authz_deny`、`policy_reload_failed`、`progress_unrouted`、`audit_dropped`、`control_call`、`device_link_online`、`device_link_reconnecting`、`device_link_offline`、`device_link_revoked`、`device_link_identity_conflict` |
| `actor` | 可选，`mcp`（缺省）或 `control`。`control` 表示本机控制通道（[CTR-CTL-02](control.md#ctr-ctl-02)），此时 `event` 为 `control_call`，`caller` 形如 `control:uid-1000`，`tool` 是路由编号，见 [RUL-17](rulings.md#rul-17) |
| `caller` | `caller_id`。Gateway 与入口写入本节点的网络身份加到目标 Agent 的下游会话，与该 Agent 自己审计里的 `caller` 一致；Gateway 自己的工具（`devices.list` 等）和还没有下游会话的调用，填本节点身份加上游 MCP 会话 |
| `oauth_client` | 预注册名或动态 `client_id`，由入口及接受该身份的 Agent 记录 |
| `oauth_session` | 仅入口层。该请求所属 OAuth 授权的摘要（授权标识的 SHA-256 前 8 字节，十六进制），用于关联同一授权的调用，不是可用凭据 |
| `device` | 目标节点名；Gateway 自己的工具填本节点名 |
| `tool` | 对外工具名 |
| `args_digest` | 见下 |
| `status` | `ok` 或 `error` |
| `error` | 失败时的 `error` 码，成功为 null |
| `duration_ms` | 整数 |
| `output_sha256` | 成功时对结构化结果做规范 JSON 后的 SHA-256。失败时对错误对象哈希 |
| `execution_id`、`started_at`、`layer` | 可选，接收层的执行编号、开始时间及 `agent` 或 `gateway`，与 `/v2/sessions.executions` 关联 |
| `run_id`、`task_status` | 可选，UI MCP 异步任务的运行编号及已知状态，逐调用记录表示工具响应，`task_complete` 表示结果轮询确认的终态 |

异步任务 `accepted` 响应与任务终态分别写入历史。结果轮询使用接受任务的同一调用方与连接；读取失败或结果声明 `outcome_unknown` 时，实时状态保持 `unknown`，直到后续确认结果。任务完成事件仅携带标识、状态、来源和耗时，结果正文不进入该事件。

`progress_unrouted` 记录一条被丢弃的进度通知（[CTR-MUX-02](mcpmux.md#ctr-mux-02)）：`tool` 为 `notifications/progress`，`device` 是发出通知的节点，不含参数或 token。同一条下游连接每秒最多记一条，以免异常后端刷满磁盘。

设备链路状态变更写 `device_link_*` 事件，`device` 为持久设备 ID，`tool` 为 `device.link.lan`、`device.link.tailnet` 或 `device.link`。它们只记录状态和路径类型，用于关联首次连接、路径恢复、信任撤销及身份冲突。

### args_digest 允许的内容

- 参数的键名。
- `resolved_path`。
- `cmd` 与 `args` 的长度。`cmd` 的基名可以记录，完整参数值不记录。
- `session_id`、`signal`、`mode`、后端名、`lease` 的 token 数值（token 不是密钥，是fencing 序号）。
- 任意字符串值的长度与 SHA-256。

### 禁止出现的内容

文件正文、`content`、`old_string`、`new_string`、stdout、stderr、stdin、`pty` 的 `data`、环境变量的值、Authorization 头、PAT、OAuth token、auth key。

校验：审计编码器在写入前扫描对象。若原始参数对象被误放进记录，拒绝写这条审计并返回 `internal` 给调用方，同时用一条不含参数的 `audit_dropped` 记录代替。宁可少一条细节，也不落正文。这满足 [INV-10](../constraints/invariants.md#inv-10) 与 [CON-SEC-08](../constraints/security.md#con-sec-08)。

### 保留

`audit.retention` 缺省 `720h`。到期的文件删除。审计文件的目录本身受 cwd 策略之外的固定权限：只允许运行 xmesh 的用户读取。

模式：[schemas/audit-event.schema.json](schemas/audit-event.schema.json)。

面板工具调用复用 Gateway 的现有 PAT，通过 `X-Xmesh-Origin: panel` 在初始化时声明来源。Gateway 的调用审计可带 `origin: "panel"`，Control 审计视图保留此字段。它是客户端声明的展示标志，不参与授权；未声明来源不能据此判定为 AI。远端 Agent 保持自身的调用审计格式。
