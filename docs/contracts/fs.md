# fs

所有路径经 [CTR-CWD-01](cwd.md#ctr-cwd-01)。成功响应都包含 `resolved_path`。文本规则见 [CON-TOOL-04](../constraints/tools.md#con-tool-04)。写操作的原子性见 [INV-06](../constraints/invariants.md#inv-06)。

哈希一律是小写十六进制 SHA-256，共 64 个字符。

## CTR-FS-01

工具名：`fs.read`。

| 参数 | 要求 |
| --- | --- |
| `path` | 必须 |
| `cwd` | 可以。工作目录，经 [CTR-CWD-01](cwd.md#ctr-cwd-01) 解析（支持别名、`~`、绝对路径），必须是已存在的目录；相对 `path` 以它为起点，缺省为 `agent.default_cwd`。本章所有 `fs.*` 工具都接受同名的可选参数 `cwd`，下面各表不再重复 |
| `offset` | 可以。从 1 开始的行号，默认 1 |
| `limit` | 可以。行数，默认 2000，最大 5000 |

行为：

- 文件大小 `>` 10 MiB：`too_large`，不返回正文。
- MIME 为 `image/png`、`image/jpeg`、`image/gif`、`image/webp`：返回 `encoding: base64`、`mime`、`data`。不按行切。
- 含 NUL 或非法 UTF-8：`binary_rejected`，`hint` 为「使用 exec 处理该文件」。
- 其他：按 UTF-8 文本返回 `content`。每行前缀为右对齐 6 位行号加 `|`，与 `cat -n` 的阅读习惯一致。`offset` 与 `limit` 作用在原始行，不作用在前缀上。
- 结果带 `truncated`。因 `limit` 未读完文件时为 true。

不存在：`invalid_cwd` / `not_found`。目录：`invalid_cwd` / `not_directory`。

## CTR-FS-02

工具名：`fs.write`。整文件覆盖。

HTTP MCP 请求的完整 JSON 信封最多 16 MiB，Gateway 与 Agent 均执行此传输上限；JSON 转义也计入信封大小。更大的文件通过 [CTR-FS-08](#ctr-fs-08) 的 `fs.upload` 分块写入，其整文件上限由 `agent.fs.max_file_size` 配置。

| 参数 | 要求 |
| --- | --- |
| `path` | 必须。父目录必须已存在 |
| `content` | 必须。字符串，按 UTF-8 写入，不改换行 |
| `expected_sha256` | 可以。若给出，必须等于写入前整文件的 SHA-256 |

`expected_sha256` 省略：创建或覆盖。给出但文件不存在：`invalid_cwd`，`reason` 为 `not_found`，不创建文件。给出但与当前内容不一致：`conflict`，带上当前的 `current_sha256` 与 `mtime`，文件不变。

成功：`resolved_path`、`sha256`（写入后）、`bytes`。

并发：比较哈希与 rename 之间，若实现做不到原子比较，必须在写入前再次计算哈希；rename 之后其他读者只看见旧文件或新文件。

## CTR-FS-03

工具名：`fs.edit`。

| 参数 | 要求 |
| --- | --- |
| `path` | 必须，文件必须存在 |
| `old_string` | 必须，非空 |
| `new_string` | 必须，可以为空字符串，表示删除匹配文本 |
| `replace_all` | 可以，默认 false |
| `expected_sha256` | 可以，语义同 `fs.write` |

`replace_all` 为 false 时，匹配数必须为 1。零匹配 → `edit_not_found`。多于一处 → `edit_ambiguous`，`lines` 是每个匹配起点的 1-based 行号，文件不变。`replace_all` 为 true 时替换全部，零匹配仍是 `edit_not_found`。

匹配是字节级的 UTF-8 子串，不忽略空白，不使用正则。成功后的写入路径与 `fs.write` 相同。成功返回 `resolved_path`、`sha256`、`replacements`。

## CTR-FS-04

工具名：`fs.glob`。

| 参数 | 要求 |
| --- | --- |
| `pattern` | 必须。支持 `**`，由 doublestar 语义解释 |
| `path` | 可以。搜索根，默认是本次 cwd |
| `limit` | 可以。默认 1000，最大 10000 |

结果按 mtime 倒序，同 mtime 再按路径字节升序，保证稳定。跳过匹配 `.gitignore` 的路径，除非模式显式写在被忽略目录之内且用户把 `include_ignored` 设为 true。该参数默认 false。

成功：`matches`（`path`、`mtime`）、`truncated`。达到 `limit` 时 `truncated` 为 true。

## CTR-FS-05

工具名：`fs.grep`。

| 参数 | 要求 |
| --- | --- |
| `pattern` | 必须。RE2 |
| `path` | 可以。默认 cwd |
| `glob` | 可以。文件名过滤 |
| `type` | 可以。与 `rg --type` 相同的文件类型名。纯 Go 兜底必须识别与本机 `rg` 相同的一组内置类型；做不到的类型返回 `invalid_args` |
| `output_mode` | 必须。`files`、`content`、`count` |
| `context` | 可以。内容模式下匹配行上下的行数，默认 0，最大 5 |
| `ignore_case` | 可以，默认 false |
| `multiline` | 可以，默认 false |
| `head_limit` | 可以。默认 200，最大 2000 |

优先 `rg --json`，否则纯 Go。两种实现返回同一结构，见 [CON-IMPL-05](../constraints/implementation.md#con-impl-05)。

成功：

| output_mode | 字段 |
| --- | --- |
| `files` | `files`：路径数组 |
| `content` | `matches`：`path`、`line`、`text`，以及可选的 `context_before` / `context_after` |
| `count` | `counts`：`path`、`count` |

一律包含 `truncated`。`.gitignore` 的处理与 glob 相同。

## CTR-FS-06

### fs.list

参数：`path`（必须，目录）、`depth`（默认 1，最大 5）、`show_hidden`（默认 false）。

返回 `entries`：`name`、`type`（`file` / `dir` / `symlink` / `other`）、`size`、`mtime`。`depth > 1` 时 `name` 使用相对 `path` 的 `/` 分隔路径。不跟随目录符号链接向下遍历，避免循环。隐藏文件指基名以 `.` 开头；Windows 上同时排除设置了隐藏属性的项。

### fs.stat

参数：`path`。返回 `exists`、`type`、`size`、`mode`（八进制字符串）、`symlink`（布尔）、`mtime`。不存在时 `exists: false`，其余字段省略，这是成功结果，不是 `not_found`。这样模型可以探测路径。`stat` 仍然要经过 allow/deny，越界是 `invalid_cwd`，不能用 stat 探测 deny 里的路径是否存在。

### fs.sha256

参数：`path`。对文件内容计算 SHA-256。目录返回 `invalid_cwd` / `not_directory`。大小超过 128 MiB 返回 `too_large`。成功：`sha256`、`bytes`。

## CTR-FS-07

### fs.mkdir

参数：`path`、`parents`（默认 false）。`parents` 为 false 且父目录不存在 → `invalid_cwd` / `parent_missing`。已存在且是目录：成功，`created: false`。已存在且不是目录：`conflict`。

目标存在冲突返回 `path`、`mtime`。此检查只读取元数据，省略 `current_sha256`；需要文件哈希时调用 `fs.sha256`。

### fs.move

参数：`from`、`to`、`overwrite`（默认 false）。两端都要通过 Resolver。`overwrite` 为 false 且目标存在 → `conflict`。跨设备时允许复制后删除源；复制未完成时不得删除源。成功：`resolved_path` 为目标路径。

目标存在冲突返回目标的 `path`、`mtime`，省略 `current_sha256`，源和目标保持原状。

### fs.delete

参数：`path`、`recursive`（默认 false）。

- 文件：删除，返回 `removed: 1`。
- 目录且 `recursive` 不为 true：`policy_denied`，规则 `fs.delete.recursive_default_deny`，不删除子项。
- 目录且 `recursive` 为 true：路径必须通过 cwd 策略。成功返回 `removed` 计数。中途失败时错误里带 `removed` 已删除数量，调用方不得假设原子。目录递归删除做不到与文件写一样的原子性，契约明确这一点。

符号链接：删除链接本身，不删除目标。

## CTR-FS-08

### 文件大小与分块

`agent.fs.max_file_size` 默认 `1GiB`（1,073,741,824 字节）。可在面板「设置 → 文件互传」填写正数大小（例如 `512MiB`、`2GiB`），或选择「无限制」（配置值 `unlimited`）。设置热更新生效，也会检查已开始传输的后续分块。限制针对整文件大小；每个原始分块最多 **1 MiB**，独立于整文件上限，内存和 MCP 信封保持有界。

两种工具均接受 `path`（必须）、`cwd`（可选）、`transfer_id`（后续分块必须）、`byte_offset`（下一块的字节偏移，首块默认 0）。每次调用重新检查路径策略与工具权限。会话绑定真实调用方，ID 使用随机值；传入身份字段不能改变归属。后续请求必须携带同一路径及准确偏移，错误偏移返回 `invalid_args` 和 `next_offset`，调用方可据此继续。传输失败时客户端应重新开始。

每个调用方最多 8 个活动传输，节点最多 64 个；空闲 10 分钟的会话关闭并清理上传暂存文件，正常停机也清理。进程崩溃可能留下隐藏暂存文件，目标文件保持提交前状态。服务重启后需重新开始传输。

### fs.download

新增可选参数 `chunk_size`，范围 1..1048576，默认 1048576。

首块打开普通文件并保留句柄。每块返回：`resolved_path`、`encoding: "base64"`、`data`、`sha256`（**该块**原始字节哈希）、`bytes`（该块大小）、`file_bytes`（整文件大小）、`next_offset`、`eof`。未到末尾时返回 `transfer_id`；末块额外返回 `file_sha256`（按顺序收到的完整文件的哈希），并关闭会话。空文件直接返回末块。

下载支持二进制、BOM、UTF-16 和混合换行，原样保留字节。每块读取前后复核同一句柄的大小和修改时间；末块返回前再次流式计算同一句柄的完整哈希，与已发送分块的累计哈希比较，元数据保持不变的原地改写也会被拒绝。源文件变化返回 `conflict`（`path`、`op: "fs.download"`），并关闭会话。源路径被原子替换时，句柄继续读取完整原文件。外部程序原地写入源文件时应先协调停止写入，以取得稳定版本。接收端应逐块校验，并核对 `file_sha256`、`file_bytes`，最后原子安装本地文件。

### fs.upload

| 参数 | 要求 |
| --- | --- |
| `path` | 必须，目标文件路径，父目录须已存在 |
| `cwd` | 可选，相对路径的起点 |
| `data` | 必须，该块原始字节的标准 Base64，空文件使用空字符串 |
| `sha256` | 必须，**整文件**原始字节的小写 SHA-256，每块一致 |
| `force` | 可选，默认 false；为 true 时允许覆盖已有普通文件，每块一致 |
| `transfer_id` | 首块省略，后续分块使用上一响应的 ID |
| `byte_offset` | 首块为 0，后续使用上一响应的 `next_offset` |
| `final` | 默认 true。多块上传时除末块外设为 false |

首块创建同目录暂存文件，后续分块按顺序追加并累计哈希。非末块返回 `resolved_path`、`transfer_id`、`next_offset`、`bytes`（累计大小）、`complete: false`，目标文件保持原样。末块核对整文件 SHA-256，通过后设置权限、fsync、关闭并原子发布；成功返回 `resolved_path`、`sha256`、`bytes`、`complete: true`。新文件权限为 `0600`，覆盖时保留目标权限。读者看到旧或新文件的完整内容。

目标存在（即使内容相同）且 force 为 false：`conflict`（`path`、`op: "fs.upload"`）。首块与最终发布都检查冲突，默认通过排他重命名或硬链接发布；并发创建只允许一个成功。force 为 true 才允许原子覆盖，目录及特殊文件不允许覆盖。校验、大小限制、写入、取消或最终发布失败时清理会话及暂存文件，保留原目标；参数错误允许修正后继续。

超限返回 `too_large`（`path`、`size`、`limit`；编码长度预检时 size 是解码大小上界）。整文件与单块限制分别生效，`unlimited` 解除整文件上限。所有路径通过统一 Resolver；符号链接按解析后的目标处理，allow/deny 仍生效。上传需要 `fs:write:<设备名>`，下载需要 `fs:read:<设备名>`；授权、工具开关、调用配额与审计摘要同样适用。

单块 Gateway 调用示例（文件内容 `hello`）：

```json
{"name":"local/fs.upload","arguments":{"path":"artifact.bin","data":"aGVsbG8=","sha256":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}}
{"name":"local/fs.download","arguments":{"path":"artifact.bin"}}
```

大文件上传按上述 `final: false → transfer_id / next_offset → final: true` 循环；下载持续请求直到 `eof: true`。保存客户端示例见 [`scripts/file_transfer.py`](../../scripts/file_transfer.py)，它通过标准 MCP 使用分块传输，并在本地下载时校验后原子安装。
