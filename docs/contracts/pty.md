# pty

## CTR-PTY-01

一条会话在任意时刻处于且只处于下列一个状态。

| 状态 | 进入 | 离开 | 占用 `max_sessions` | 占用原 caller 的 `max_per_caller` |
| --- | --- | --- | --- | --- |
| `active` | `open` 成功，或从 `orphaned` 被 `transfer` | `close`、进程退出、idle 超时、所属会话 `Orphan` | 是 | 是 |
| `orphaned` | `Orphan` | `transfer` 成功、`orphan_grace` 到期被 Kill、进程自行退出 | 是 | 否 |
| `closed` | 进程已退出且缓冲仍保留 | 保留期结束，默认 `closed_retain: 5m`；或 `closed` 数量超过 `closed_retain_max` 时被提前释放 | 否 | 否 |
| `released` | 保留期结束 | 终态，表项删除 | 否 | 否 |

关闭顺序必须是：请求终止 → 等待 PTY 读循环遇到 EOF → 进入 `closed` → 广播，使阻塞中的 `read` 返回 `eof: true` → 定时进入 `released`。禁止在读循环仍可能写缓冲时删除会话。

reaper 至少每 30 秒运行一次，也允许在状态变更时提早运行。扫描对象是 idle 的 `active`（`idle_timeout`）、超过 `orphan_grace` 的 `orphaned`、超过保留期的 `closed`，以及使 `closed` 数量回到 `closed_retain_max` 以内。配额语义见 [RUL-12](rulings.md#rul-12)。

idle 的定义：`lastIO` 距今超过 `idle_timeout`。`open`、`write`、`read` 返回非空数据、`resize`、`signal` 都刷新 `lastIO`。单纯的空读超时不刷新，否则轮询会把会话永远保住。

## CTR-PTY-02

### pty.open

| 参数 | 要求 |
| --- | --- |
| `shell` | 可以。缺省按平台解析 `default_shell`。Windows 见 [CON-IMPL-04](../constraints/implementation.md#con-impl-04) |
| `cwd` | 可以。经 Resolver |
| `env` | 可以。与 exec 同一环境过滤器 |
| `cols` | 必须。整数 1 到 512 |
| `rows` | 必须。整数 1 到 512 |
| `name` | 可以。展示名，最长 64 字符，不要求唯一 |

成功：`session_id`、`pid`、`resolved_path`、`owner`（等于调用方 `caller_id`）。

失败：`pty_limit`、`invalid_cwd`、`invalid_args`、`policy_denied`、`unauthorized`。失败时计数与失败前相同。

`session_id` 由 Agent 生成，匹配 `p-[0-9a-z]{8,}`。调用方不得指定 id。

### pty.write

参数：`session_id`、`data`（字符串）。仅 owner，且状态为 `active`。`data` 按 UTF-8 编码写入 PTY。单次最大 64 KiB，超过则 `invalid_args`，一次都不写。

成功：`written`（字节数）。`orphaned` 或 `closed` 返回 `not_owner`。未知 id 返回 `session_not_found`。

### pty.read

参数：`session_id`、`cursor`（默认 0）、`max_bytes`（默认 32768，最大 1 MiB）、`wait_ms`（默认 1000，最大 30000）。

任何通过工具授权的调用方都可以读。不要求是 owner。

成功：

| 字段 | 含义 |
| --- | --- |
| `data` | 从有效起点起的新输出 |
| `next_cursor` | 读完后的逻辑偏移 |
| `eof` | 会话已 `closed` 且没有未读的保留输出 |
| `truncated` | 请求的 `cursor` 已经落在被环形缓冲覆写的区域 |

`cursor` 太旧时，`truncated` 为 true，`data` 从缓冲中现存的最小偏移开始，`next_cursor` 指向该数据之后。这是成功结果。

`wait_ms` 耗尽且没有新数据：`data` 为空字符串，`next_cursor` 等于请求的 cursor（若已被覆写，则等于现存最小偏移，且 `truncated` 为 true），`eof` 为 false。

### pty.resize

参数：`session_id`、`cols`、`rows`。仅 `active` 的 owner。成功返回同样的 `cols` 与 `rows`。Windows 与 Unix 都要调用平台的 resize API；失败返回 `internal`，不改变记录中的尺寸。

### pty.signal

参数：`session_id`、`signal`（`INT`、`TERM`、`KILL`）。仅 `active` 的 owner。Windows 映射见 [CON-IMPL-04](../constraints/implementation.md#con-impl-04)。成功：`sent: true`。信号不保证进程已经退出；退出由随后的 `read` 看到 `eof`，或 `list` 看到 `closed`。

### pty.transfer

参数：`session_id`、`to_caller`。

- `active`：调用方必须是 owner。owner 改为 `to_caller`，双方的每调用方计数相应减增。若接收方已达 `max_per_caller`，返回 `pty_limit`，`scope: caller`，owner 不变。这一步是 owner 自愿交接，不要求同一个 `oauth_client`，见 [RUL-13](rulings.md#rul-13)。
- `orphaned`：调用方的 `node_id` 必须等于 `to_caller` 的 `node_id`，且 `to_caller` 必须是当前仍连接着的会话。会话若在 open 时记下了 `oauth_client`，新 owner 的会话必须是同一个 `oauth_client`，见 [RUL-13](rulings.md#rul-13)。成功后状态回到 `active`。

`to_caller` 的校验见 [CTR-ID-01](identity.md#ctr-id-01)。成功返回 `owner` 与 `state`。

### pty.close

仅 `active` 的 owner。执行 [CTR-PTY-01](#ctr-pty-01) 的关闭顺序。成功返回 `state: closed`。重复 close 在保留期内返回当前 `closed` 状态，不算错误。`released` 之后返回 `session_not_found`。

### pty.list

无参数。返回调用方有权看到的会话。授权规则与 `pty.read` 相同：能读终端的 tag 能看到列表。每项：

| 字段 | 含义 |
| --- | --- |
| `id` | session_id |
| `name` | 可空 |
| `owner` | `orphaned` 时为空字符串 |
| `pid` | 进程已退出则为 0 |
| `state` | `active`、`orphaned`、`closed` |
| `idle_s` | 距 `lastIO` 的秒数 |
| `cols` / `rows` | 当前尺寸 |
| `cwd` | 启动时的 `resolved_path`，不跟踪后续 `cd` |

列表不包含环形缓冲里的文本。
