# 审计

## CTR-AUD-01

每次到达 Agent 的工具调用写一条记录。经过入口的调用在入口再写一条，两层都记，因为入口看得到 OAuth client，Agent 看得到最终是否执行。Gateway 在 tailnet 模式下同样写一条转发记录。

PTY 的 `read` 默认不写每条记录，只在 `pty.read` 返回非空数据时按分钟合并成一条 `pty_io` 摘要，避免 long-poll 刷满磁盘。`open`、`close`、`transfer`、`signal`、`write` 各自一条，`write` 只记字节数。

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
| `event` | `tool_call`、`pty_open`、`pty_close`、`pty_transfer`、`pty_signal`、`pty_write`、`pty_io`、`authz_deny`、`policy_reload_failed` |
| `caller` | `caller_id`，入口层若还没有下游会话，填 OAuth 会话 id 并使用字段 `oauth_session` |
| `oauth_client` | 仅入口层。预注册名或动态 `client_id` |
| `device` | 节点名 |
| `tool` | 对外工具名 |
| `args_digest` | 见下 |
| `status` | `ok` 或 `error` |
| `error` | 失败时的 `error` 码，成功为 null |
| `duration_ms` | 整数 |
| `output_sha256` | 成功时对结构化结果做规范 JSON 后的 SHA-256。失败时对错误对象哈希 |

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
