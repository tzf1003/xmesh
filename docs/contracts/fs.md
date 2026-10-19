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

### fs.move

参数：`from`、`to`、`overwrite`（默认 false）。两端都要通过 Resolver。`overwrite` 为 false 且目标存在 → `conflict`。跨设备时允许复制后删除源；复制未完成时不得删除源。成功：`resolved_path` 为目标路径。

### fs.delete

参数：`path`、`recursive`（默认 false）。

- 文件：删除，返回 `removed: 1`。
- 目录且 `recursive` 不为 true：`policy_denied`，规则 `fs.delete.recursive_default_deny`，不删除子项。
- 目录且 `recursive` 为 true：路径必须通过 cwd 策略。成功返回 `removed` 计数。中途失败时错误里带 `removed` 已删除数量，调用方不得假设原子。目录递归删除做不到与文件写一样的原子性，契约明确这一点。

符号链接：删除链接本身，不删除目标。

## CTR-FS-08

### fs.download

参数：`path`（必须）、`cwd`（可选）。下载普通文件的原始字节，支持二进制、图片、UTF-16、BOM 和混合换行；成功结果：`resolved_path`、`encoding: "base64"`、`data`（标准 Base64）、`sha256`（原始字节哈希）、`bytes`（原始字节数）。空文件返回空的 `data` 和空内容的 SHA-256。

使用同一打开句柄读取并复核哈希、大小和修改时间；发现源文件在读取期间变化时返回 `conflict`，带 `path`、`op: "fs.download"`。原子替换源路径时，打开的句柄仍返回完整的原文件。外部程序持续原地写入时，调用方应先协调停止写入，以取得稳定版本。接收端应解码 `data` 并比对 `sha256`、`bytes` 后再安装文件。

### fs.upload

| 参数 | 要求 |
| --- | --- |
| `path` | 必须，目标文件路径，父目录须已存在 |
| `cwd` | 可选，相对路径的起点 |
| `data` | 必须，文件原始字节的标准 Base64，空文件使用空字符串 |
| `sha256` | 必须，原始字节的小写十六进制 SHA-256 |
| `force` | 可选布尔值，默认 `false`。为 `true` 时允许覆盖已有普通文件 |

- 先解码并校验哈希；Base64 无效、哈希格式错误或不匹配返回 `invalid_args`，`field` 指向 `data` 或 `sha256`，目标保持原样。
- 目标已存在（即使内容相同）且 `force` 为 false：`conflict`，带 `path`、`op: "fs.upload"`。最终发布也原子检查目标不存在，并发创建只允许一个成功。
- `force: true` 先在同目录写临时文件、保留目标权限（新文件为 `0600`），`fsync` 并关闭，再原子替换。并发读者看到旧文件或新文件的完整内容。目录及其他特殊文件不允许覆盖。
- 取消发生在最终发布之前时清理临时文件；失败不留下部分目标。成功结果：`resolved_path`、`sha256`、`bytes`。
- 路径通过统一 Resolver，符号链接按解析后的目标路径处理，allow/deny 仍生效。父目录不存在返回 `invalid_cwd` / `parent_missing`。

两种工具的原始文件上限均为 **10 MiB**，使 Base64 上传及 MCP 信封可容纳在 16 MiB HTTP 请求上限内。超限返回 `too_large`（`path`、`size`、`limit`；编码长度预检时 `size` 是解码大小上界）。上传使用 `fs:write:<设备名>` scope，下载使用 `fs:read:<设备名>`；节点授权与 `disabled_tools` 同样适用。审计只记录摘要。

Gateway 调用示例（原始文件内容为 `hello`）：

```json
{"name":"local/fs.upload","arguments":{"path":"artifact.bin","data":"aGVsbG8=","sha256":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}}
{"name":"local/fs.download","arguments":{"path":"artifact.bin"}}
{"name":"local/fs.upload","arguments":{"path":"artifact.bin","data":"aGVsbG8=","sha256":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824","force":true}}
```
