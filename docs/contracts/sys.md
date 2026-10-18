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
  "roles": ["agent", "gateway"],
  "network": {"mode": "tailnet", "node_id": "win-dev-01", "tags": ["tag:device"]},
  "restart_required": false,
  "policy_reload_failures": 0,
  "authkey_readable": true,
  "warnings": [],
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
    "exec_shared_in_use": 0,
    "exec_exclusive_in_use": 0,
    "pty_max_sessions": 20,
    "pty_max_per_caller": 8,
    "pty_in_use": 1,
    "pty_active": 0,
    "pty_closed": 1,
    "pty_closed_retain_max": 20
  }
}
```

| 字段 | 约束 |
| --- | --- |
| `os` | `windows`、`darwin`、`linux` 之一，与 `runtime.GOOS` 一致 |
| `arch` | `runtime.GOARCH` |
| `hostname` | 配置的 `node.name`，不是操作系统主机名。操作系统主机名若需要，放在后续字段，本期不返回，避免两个名字混用 |
| `home` | Resolver 使用的主目录 |
| `roles` | 配置的 `node.roles`，原样返回 |
| `network` | 本次调用实际使用的网络身份：`mode`（`tailnet`、`lan` 或 `local`）、`node_id`、`tags`。`--local` 开发模式固定为 `mode: local`、`node_id: local`、`tags: ["tag:peer"]` |
| `restart_required` | `config.RestartPaths` 中的段（`node.name`、`node.roles` 里 `agent` 与 `ingress` 的增减、`mesh`、`ingress`、`audit`、`agent.policy.windows_appcontainer`）在热加载中变更过，需要重启才生效；对应的 `warnings` 有文字提示 |
| `policy_reload_failures` | 配置热加载失败的累计次数；大于 0 时继续使用最近一份有效配置，`warnings` 有提示 |
| `authkey_readable` | `mesh.mode = tailnet` 时 `authkey_file` 当前是否可读；其他模式恒为 `false`。不返回 key 内容 |
| `warnings` | 面向人的警告字符串数组，可为空：需要重启、热加载曾失败、`allow_dangerous` 已启用。调用方不得依赖具体措辞，机器判断用上面的布尔与计数字段 |
| `path_sep` | 单字符 `\` 或 `/` |
| `shells` | 该平台实际会选用的 shell 列表，第一项是 `auto` 的解析结果 |
| `workspaces` | 与配置别名一一对应，`path` 已解析为绝对路径。解析失败的别名仍然列出，并带 `error` |
| `backends[].state` | `stopped`、`running`、`crashed` 之一 |
| `limits.exec_shared_in_use`、`limits.exec_exclusive_in_use` | 当前占用的 exec 共享槽与独占槽数；[CTR-GW-02](gateway.md#ctr-gw-02) 的 `load.exec_shared_in_use` 取自前者 |
| `limits.pty_in_use` | `active` 与 `orphaned` 之和，即占用 `max_sessions` 的数量 |
| `limits.pty_active` | 仅 `state` 为 `active` 的会话数。它和 `pty_limit.in_use` 不是同一个数 |
| `limits.pty_closed` | 仍保留缓冲的 `closed` 会话数 |
| `limits` 其余字段 | 来自当前配置与当前计数，不是历史最大值 |

### 错误

只返回 `unauthorized`、`unauthenticated`、`internal`。`sys.info` 不因某个后端崩溃而失败；崩溃反映在 `state` 里。

### 可见性

`backends` 按调用方 tag 的 `authz.backends` 过滤。调用方看不到自己无权使用的后端名称。`limits` 仍返回，方便模型判断设备是否还有 PTY 余量。
