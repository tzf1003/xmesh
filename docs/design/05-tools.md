# 5. 工具面规范

所有工具的路径参数走同一个 `cwd.Resolver`：支持 `~`、`$HOME` / `%USERPROFILE%`、`@别名`，统一接受 `/` 分隔符，Windows 上再转成本地分隔符。解析后做 `EvalSymlinks`，再比对 allow/deny。响应回显解析后的绝对路径。

精确算法、拒绝条件和回显字段见 [CTR-CWD-01](../contracts/cwd.md)。各工具的参数、结果和错误码见对应契约，本节保留方案中的语义表。

错误都是结构化对象，不是字符串。模型据此决定等待、换设备或换方式。信封见 [CTR-ERR-01](../contracts/errors.md)。

## sys

| 工具 | 参数 | 返回 |
| --- | --- | --- |
| `sys.info` | 无 | `os`、`arch`、`hostname`、`home`、`path_sep`、`shells[]`、`workspaces[]`（别名、路径、描述）、`backends[]`（名称、策略、状态）、`limits`（exec/pty 配额） |

## exec（非交互）

| 工具 | 参数 | 返回 |
| --- | --- | --- |
| `exec.run` | `cmd`、`args[]`、`cwd?`、`env?`、`stdin?`、`timeout?`、`mode?`（`shared` 默认 / `exclusive`）、`idempotency_key?` | `exit_code`、`stdout`、`stderr`、`duration_ms`、`truncated`、`output_ref?`（大输出分页句柄） |
| `exec.output` | `output_ref`、`offset`、`limit` | 分页拉取被截断的输出 |

`mode: exclusive` 用于全局安装、改系统配置一类命令：它等所有 shared 命令排空后执行，同时只允许 1 个。并发槽、排队和幂等缓存见 [第 6 节](06-concurrency.md) 与 [CTR-EXEC-01](../contracts/exec.md)。

## pty（交互式终端，会话池）

| 工具 | 参数 | 返回 / 语义 |
| --- | --- | --- |
| `pty.open` | `shell?`、`cwd?`、`env?`、`cols`、`rows`、`name?` | `session_id`、`pid`。超配额返回 `pty_limit` |
| `pty.write` | `session_id`、`data` | 仅 owner 可写 |
| `pty.read` | `session_id`、`cursor?`、`max_bytes?`、`wait_ms?` | `data`、`next_cursor`、`eof`、`truncated`。long-poll。任何调用方可读 |
| `pty.resize` | `session_id`、`cols`、`rows` | 仅 owner |
| `pty.signal` | `session_id`、`signal`（`INT` / `TERM` / `KILL`） | Windows 映射为 `GenerateConsoleCtrlEvent` / `TerminateProcess` |
| `pty.transfer` | `session_id`、`to_caller` | 显式移交 owner |
| `pty.close` | `session_id` | 仅 owner。退出后缓冲保留 5 分钟 |
| `pty.list` | 无 | 所有会话：id、name、owner、pid、状态、idle 秒数、cols×rows、cwd |

状态机见 [并发](06-concurrency.md#pty-状态机)。工具契约见 [CTR-PTY-02](../contracts/pty.md)。

## fs（对齐 Claude Code 默认工具）

| 工具 | 对应 | 参数要点 |
| --- | --- | --- |
| `fs.download` | — | `path`、`cwd?`。下载原始字节，返回 Base64、SHA-256 和字节数；最大 10 MiB |
| `fs.upload` | — | `path`、`data`（Base64）、`sha256`、`cwd?`、`force?`。校验后原子写入；目标存在默认冲突，`force: true` 才覆盖；最大 10 MiB |
| `fs.read` | Read | `path`、`offset?`、`limit?`。`cat -n` 风格带行号。图片返回 base64 + mime。二进制或超过 10 MiB 拒绝，并提示用 `exec` |
| `fs.write` | Write | `path`、`content`、`expected_sha256?`。整文件覆盖。临时文件 + rename 原子写。父目录不存在则报错 |
| `fs.edit` | Edit | `path`、`old_string`、`new_string`、`replace_all?`。`old_string` 必须唯一匹配，否则返回匹配行号且不写入 |
| `fs.glob` | Glob | `pattern`、`path?`、`limit?`。支持 `**`。按 mtime 倒序。尊重 `.gitignore` |
| `fs.grep` | Grep | `pattern`、`path?`、`glob?`、`type?`、`output_mode`（`files` / `content` / `count`）、`context?`、`ignore_case?`、`multiline?`、`head_limit?`。RE2 语义。本机有 `rg` 则调用 `rg --json` |
| `fs.list` | LS | `path`、`depth?`、`show_hidden?`。返回 name / type / size / mtime |
| `fs.stat` | — | 存在性、类型、大小、权限、是否软链 |
| `fs.sha256` | — | 配合 `expected_sha256` 做乐观并发 |
| `fs.mkdir` / `fs.move` / `fs.delete` | Bash | `delete` 默认拒绝目录递归，需 `recursive: true`。受策略引擎拦截 |

统一规则：

- UTF-8 读写。文件若带 BOM 或本身是 UTF-16，原样保留，不擅自转码。
- 换行风格保留，不把 CRLF 改成 LF，也不反向改写。
- glob / grep 结果设上限，并带 `truncated` 标志。

契约见 [fs](../contracts/fs.md)。

## 后端工具

`<name>.<tool>`：参数与返回原样透传。仅在该后端 `policy: exclusive` 时，调用必须携带 `lease_token`。token 的位置、剥离时机和错误码见 [CTR-BE-01](../contracts/backend-lease.md#ctr-be-01)。

## 错误形态

```json
{"error":"resource_busy","resource":"backend:ida","held_by":"gw-cloud/s7f","expires_in_s":180}
{"error":"pty_limit","scope":"device","limit":20,"in_use":20,"oldest_idle":{"id":"p-3a9f00ab","idle_s":1100}}
{"error":"invalid_cwd","path":"/home/x/nope","reason":"not_found"}
{"error":"conflict","path":"src/main.rs","current_sha256":"…","mtime":"2026-10-15T05:30:00Z"}
```

完整错误码、公共字段，以及它们在 MCP `tools/call` 结果里的承载方式，见 [错误契约](../contracts/errors.md)。`policy_denied` 的形状在 [安全](08-security.md) 中规定，并列入同一信封。
