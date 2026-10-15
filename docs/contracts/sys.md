# sys

## CTR-SYS-01

工具名：`sys.info`。无参数。任何通过授权的调用方都可以调用。它不占用 exec 槽，不占用 PTY，不读取工作区文件内容。

### 结果

```json
{
  "os": "windows",
  "arch": "amd64",
  "hostname": "win-dev-01",
  "home": "C:\\Users\\x",
  "path_sep": "\\",
  "shells": ["pwsh"],
  "workspaces": [
    {"alias": "foo", "path": "C:\\Users\\x\\repos\\foo", "desc": "主项目 (Rust)"}
  ],
  "backends": [
    {"name": "ida", "policy": "exclusive", "state": "running", "favorite": true}
  ],
  "limits": {
    "exec_shared": 4,
    "exec_exclusive": 1,
    "pty_max_sessions": 20,
    "pty_max_per_caller": 8,
    "pty_active": 1
  }
}
```

| 字段 | 约束 |
| --- | --- |
| `os` | `windows`、`darwin`、`linux` 之一，与 `runtime.GOOS` 一致 |
| `arch` | `runtime.GOARCH` |
| `hostname` | 配置的 `node.name`，不是操作系统主机名。操作系统主机名若需要，放在后续字段，本期不返回，避免两个名字混用 |
| `home` | Resolver 使用的主目录 |
| `path_sep` | 单字符 `\` 或 `/` |
| `shells` | 该平台实际会选用的 shell 列表，第一项是 `auto` 的解析结果 |
| `workspaces` | 与配置别名一一对应，`path` 已解析为绝对路径。解析失败的别名仍然列出，并带 `error` |
| `backends[].state` | `stopped`、`running`、`crashed` 之一 |
| `limits` | 来自当前配置与当前计数，不是历史最大值 |

### 错误

只返回 `unauthorized`、`unauthenticated`、`internal`。`sys.info` 不因某个后端崩溃而失败；崩溃反映在 `state` 里。

### 可见性

`backends` 按调用方 tag 的 `authz.backends` 过滤。调用方看不到自己无权使用的后端名称。`limits` 仍返回，方便模型判断设备是否还有 PTY 余量。
