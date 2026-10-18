# 路径解析

## CTR-CWD-01

包 `internal/cwd` 提供唯一的解析器。输入是调用参数里的路径字符串、可选的基准目录、以及配置中的别名与 allow/deny。输出是绝对的、已求值符号链接的路径，或 [CTR-ERR-01](errors.md#ctr-err-01) 的 `invalid_cwd`。

### 输入

| 名称 | 含义 |
| --- | --- |
| `raw` | 调用方给出的路径或 cwd |
| `base` | 相对路径的起点。工具调用的 cwd 解析结果。`fs` 工具的 `path` 为相对路径时，base 是本次 `cwd`，缺省则是 `agent.default_cwd` |
| `aliases` | `workspaces` 映射 |
| `allow` / `deny` | 尚未解析的前缀列表，解析器会先把它们各自展开再比较 |

### 展开顺序

对 `raw`、对每条 allow、对每条 deny，使用同一函数：

1. 若字符串以 `@` 开头，余下部分必须是别名，替换为该别名的 `path`，再继续。未知别名 → `invalid_cwd` / `not_found`。
2. 把前导 `~` 或 `~/` 换成当前用户主目录。主目录取自操作系统，不取自调用方 env。
3. 展开 `$HOME` 与 `%USERPROFILE%`。不展开其他环境变量，避免把凭证路径拼接进来。
4. 把 `/` 当作分隔符。Windows 上再转成 `\` 交给系统 API。
5. 相对路径加上 `base`。绝对路径不用 `base`。
6. `filepath.Clean`。
7. `EvalSymlinks`。路径不存在时：若父目录存在且最终成分是尚不存在的文件名（写入场景），则只对父目录 `EvalSymlinks`，再拼接最终成分。父目录也不存在 → `parent_missing`。读取场景下目标不存在 → `not_found`。
8. 得到候选绝对路径。

### 策略比较

1. 若候选路径等于某条已展开的 deny，或位于其下级，拒绝，`reason: denied`。deny 包括配置里的 `deny`，以及 `deny_file` 指向的列表文件中的每一条。文件缺失、不可读或内容不是字符串列表时解析器创建失败，不会当作空列表。
2. 否则若 allow 非空，且候选路径不等于任何 allow、也不在其下级，拒绝，`reason: denied`。
3. allow 为空：除上一条拒绝的路径外全部允许。deny 仍然生效，因此凭证目录须写进 deny 或 deny_file（向导默认把 `~/.ssh`、`~/.gnupg`、`~/.aws`、`~/.config/gcloud`、`~/.azure`、`~/.kube`、`~/.docker` 写入 deny_file 指向的文件）。

比较时路径都已是符号链接求值之后的结果，见 [CON-TOOL-02](../constraints/tools.md#con-tool-02)。

### 输出

成功时返回：

| 字段 | 含义 |
| --- | --- |
| `resolved_path` | 绝对路径 |
| `alias` | 若输入使用了 `@别名`，回显别名，否则省略 |

工具响应必须带上 `resolved_path`，见 [CON-TOOL-03](../constraints/tools.md#con-tool-03)。

### 不做什么

- 不创建目录。
- 不跟随调用方提供的 `HOME`。
- 不把 PTY 会话随后的 `cd` 纳入解析。
- 不接受 NUL 字节。出现 NUL → `invalid_args`。
