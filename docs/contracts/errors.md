# 错误契约

## CTR-ERR-01

所有工具的失败都是一个 JSON 对象。字段 `error` 是稳定的机器码，蛇形命名。`message` 是给模型读的中文说明，可以变，调用方不得依赖 `message` 做分支。其余字段由错误码决定，未知字段应被忽略，以便以后加字段。

### 承载

见 [RUL-08](rulings.md#rul-08)。HTTP 传输层的 401/403 用于 OAuth，不用于工具语义。工具语义上的拒绝仍然是 MCP 结果里的 `isError`，HTTP 状态保持 200。这与 MCP 的工具错误模型一致，避免客户端把策略拒绝当成传输失败而盲目重试。

### 公共字段

| 字段 | 类型 | 要求 |
| --- | --- | --- |
| `error` | 字符串 | 必须，下表之一 |
| `message` | 字符串 | 必须，中文，一行 |
| `retry_after_ms` | 整数 | 可以。有明确等待价值时给出 |

### 错误码

| error | 何时 | 额外字段 |
| --- | --- | --- |
| `invalid_args` | 参数类型或必填项不对 | `field`；Windows 上命令或 shell 是 Store 应用执行别名时另带 `reason: "app_execution_alias"` 与 `path` |
| `invalid_cwd` | 路径解析或策略前的路径不成立 | `path`、`reason`：`not_found` / `parent_missing` / `symlink_escape` / `denied` / `not_directory` |
| `policy_denied` | 策略引擎拒绝 | `rule`、`reason` |
| `conflict` | 乐观并发失败，或移动目标已存在 | `path`、`current_sha256`、`mtime` |
| `edit_not_found` | `old_string` 零匹配 | `path` |
| `edit_ambiguous` | `old_string` 多匹配且未 `replace_all` | `path`、`lines`（行号数组） |
| `binary_rejected` | `fs.read` 遇到二进制 | `path`、`hint` |
| `too_large` | 超过 10 MiB | `path`、`size`、`limit` |
| `pty_limit` | 设备或调用方 PTY 配额 | `scope`：`device` 或 `caller`；`limit`、`in_use`、`oldest_idle` |
| `not_owner` | 非 owner 做写操作 | `session_id`、`owner` |
| `session_not_found` | PTY id 不存在或已 `released` | `session_id` |
| `exec_saturated` | 超时内拿不到 exec 槽 | `queue_position`、`mode`、`retry_after_ms` |
| `lease_required` | exclusive 工具缺少 token | `resource` |
| `resource_busy` | 租约被占用、token 过期或不匹配 | `resource`、`held_by`、`expires_in_s`、`reason`：`held` / `token_mismatch` / `token_stale` |
| `backend_unavailable` | 后端进程拉起失败或 HTTP 后端不可达 | `resource`、`attempts` |
| `unauthenticated` | WhoIs 失败或没有 token | 无 |
| `unauthorized` | scope 或 tag 不允许 | `scope` 或 `tag`、`tool` |
| `not_found` | 设备、工具、output_ref 不存在 | `name` |
| `timeout` | 命令或后端调用超过 timeout | `timeout_ms` |
| `internal` | 未分类的本机故障 | `op`；Windows 上底层是 Win32 错误时另带 `win32_error`（错误码）与 `reason`（系统给出的文字） |

`invalid_cwd.reason = denied` 与 `policy_denied` 的分工：路径根本不在 allow/deny 规则允许的范围内，用 `invalid_cwd`。路径合法，但某条命名规则拒绝这次操作（递归删除、命令黑名单、环境变量），用 `policy_denied`。

`pty_limit.in_use` 是这次比较所用的占用数，不是状态名 `active` 的计数。`scope` 为 `device` 时它等于 `active` 与 `orphaned` 之和，不含 `closed`。`scope` 为 `caller` 时它等于该调用方名下 `state` 为 `active` 的会话数。模型要看每个会话的状态时调用 `pty.list`。禁止再使用字段名 `active` 表示这个计数。

`exec_saturated.retry_after_ms` 是建议的最早重试间隔，算法见 [RUL-01](rulings.md#rul-01)。

### 示例

```json
{
  "error": "resource_busy",
  "message": "后端 ida 正被其他会话占用",
  "resource": "backend:ida",
  "held_by": "node-abc/s7f",
  "expires_in_s": 180,
  "reason": "held"
}
```

```json
{
  "error": "pty_limit",
  "message": "这台设备上计入配额的终端已满",
  "scope": "device",
  "limit": 20,
  "in_use": 20,
  "oldest_idle": { "id": "p-3a", "idle_s": 1100 }
}
```

```json
{
  "error": "invalid_cwd",
  "message": "工作目录不存在",
  "path": "/home/x/nope",
  "reason": "not_found"
}
```

```json
{
  "error": "conflict",
  "message": "文件在读取后被修改",
  "path": "src/main.rs",
  "current_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "mtime": "2026-10-15T05:30:00Z"
}
```

```json
{
  "error": "policy_denied",
  "message": "命令命中拒绝规则",
  "rule": "exec.deny.curl_pipe_sh",
  "reason": "不允许把下载内容直接送进 shell"
}
```

模式文件：[schemas/error.schema.json](schemas/error.schema.json)。正例与反例在 [schemas/examples/](schemas/examples/)。

### 重试

| error | 模型应如何理解 |
| --- | --- |
| `resource_busy` | 可以等到 `expires_in_s` 后再 acquire，或换设备 |
| `pty_limit` | 可以 `close` 自己的会话或 `transfer`，不要立刻重试 open |
| `exec_saturated` | 可以缩短并发，或等 `retry_after_ms` |
| `conflict` / `edit_ambiguous` | 重新读取再决定，不要原样重试 |
| `policy_denied` | 换做法。同一参数重试结果相同 |
| `unauthorized` | 换工具或换设备。重试不会扩大 scope |
| `backend_unavailable` | 可以一次重试。连续失败则换设备 |
| `internal` | 可以一次重试。不得把 `internal` 当成已执行成功 |
