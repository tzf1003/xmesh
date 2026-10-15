# exec

## CTR-EXEC-01

工具名：`exec.run`。在设备上启动一条非交互命令，等待它结束或超时，返回退出码与输出。

### 参数

| 字段 | 类型 | 要求 |
| --- | --- | --- |
| `cmd` | 字符串 | 必须。可执行文件名或路径，不经过 shell |
| `args` | 字符串数组 | 可以。缺省为空数组。每一项是一个参数，不做 glob，不拆分空格 |
| `cwd` | 字符串 | 可以。缺省 `agent.default_cwd`，经 [CTR-CWD-01](cwd.md#ctr-cwd-01) |
| `env` | 对象 | 可以。字符串到字符串。经过 [CON-SEC-04](../constraints/security.md#con-sec-04) |
| `stdin` | 字符串 | 可以。UTF-8 文本，写入进程标准输入后关闭 |
| `timeout` | 字符串或整数 | 可以。Go duration 字符串，或毫秒整数。缺省 `exec.default_timeout` |
| `mode` | 字符串 | 可以。`shared`（默认）或 `exclusive` |
| `idempotency_key` | 字符串 | 可以。长度 1 到 200。匹配 `[A-Za-z0-9._:-]+` |

`cmd` 禁止包含空字节。实现不得把 `cmd` 和 `args` 拼成一条字符串交给 `sh -c` 或 `cmd /C`。需要 shell 语法时，调用方显式把 `cmd` 设为 shell，把脚本作为 `args` 的一项。进程按 argv 直接启动。命令黑名单不按单个参数分别匹配，而对 [RUL-11](rulings.md#rul-11) 的规范化命令行做正则搜索；`sh -c` 的脚本参数再单独搜一次。

### 执行顺序

1. 校验参数。
2. 解析 `cwd`。失败则返回，进程未创建。
3. 构造环境。被拒绝则 `policy_denied`，进程未创建。
4. 用当前策略匹配 [RUL-11](rulings.md#rul-11) 的规范化命令行，以及其中识别出的 shell 脚本原文。命中拒绝则 `policy_denied`，进程未创建。
5. 若带 `idempotency_key` 且 `(caller_id, key)` 命中缓存：在授权与策略仍通过的前提下返回缓存，见 [RUL-04](rulings.md#rul-04)。
6. 按 [RUL-01](rulings.md#rul-01) 获取槽位。`exclusive` 还要满足 [CON-CONC-10](../constraints/concurrency.md#con-conc-10)。
7. 启动进程。启动失败返回 `internal` 或 `invalid_args`（找不到可执行文件时用 `invalid_args`，`field` 为 `cmd`），并释放槽位。
8. 等待退出或超时。超时向进程组发终止信号，等待短暂宽限后强制结束，返回 `timeout`。此时进程已启动过，错误里带 `duration_ms`，不带不完整的大段输出；已产生的输出仍可通过 `output_ref` 读取。
9. 截断内联输出，见 [RUL-05](rulings.md#rul-05)。写入幂等缓存。释放槽位。

### 成功结果

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `exit_code` | 整数 | 进程退出码。被信号杀死时 Unix 使用 128+信号号，Windows 使用进程退出码 |
| `stdout` | 字符串 | 内联部分。非法 UTF-8 用替换字符，原始字节仍在 `output_ref` |
| `stderr` | 字符串 | 同上 |
| `duration_ms` | 整数 | 从进程启动到退出 |
| `truncated` | 布尔 | 任一流发生了内联截断则为 true |
| `output_ref` | 字符串 | 仅当 `truncated` 为 true |
| `resolved_path` | 字符串 | cwd 的解析结果 |
| `queue_wait_ms` | 整数 | 在槽位队列里等待的时间，没有等待则省略 |

退出码非零是成功的工具结果，`isError` 为 false。模型需要看 `exit_code`。只有无法形成一次完整执行时才用错误信封。

### 错误

`invalid_args`、`invalid_cwd`、`policy_denied`、`exec_saturated`、`timeout`、`unauthorized`、`unauthenticated`、`internal`。

### 并发

槽位计数在启动前增加，在进程退出或启动失败后减少。幂等缓存命中不加槽位。

## CTR-EXEC-02

工具名：`exec.output`。分页读取某次 `exec.run` 溢出的输出。

### 参数

| 字段 | 类型 | 要求 |
| --- | --- | --- |
| `output_ref` | 字符串 | 必须。由 `exec.run` 发给同一 `caller_id` |
| `offset` | 整数 | 可以。默认 0。负值 → `invalid_args` |
| `limit` | 整数 | 可以。默认 65536。最大 1 MiB，超过则按 1 MiB |
| `stream` | 字符串 | 可以。`stdout`、`stderr` 或 `both`（默认） |

### 结果

| 字段 | 含义 |
| --- | --- |
| `stdout` / `stderr` | 本次分页的字节解码成 UTF-8 |
| `next_offset` | 下一次 `offset` |
| `eof` | 该流已读完 |

### 授权

`output_ref` 只对创建它的 `caller_id` 有效。其他调用方得到 `not_found`，避免用「猜不到 ref」之外的方式泄露输出。ref 在缓存淘汰或进程重启后同样 `not_found`。

### 错误

`invalid_args`、`not_found`、`unauthorized`。
