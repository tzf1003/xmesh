# 调用方身份

## CTR-ID-01

### 形式

```text
caller_id = node_id "/" mcp_session_id
```

字符集：`node_id` 与 `mcp_session_id` 各自匹配 `[A-Za-z0-9._:-]{1,128}`。拼接后的字符串禁止再被截断。谁产生这两个部分见 [RUL-02](rulings.md#rul-02)。

### 谁可以声称身份

Agent 只从传输层推导 `caller_id`：

- `node_id` 来自 `WhoIs`。本机 Gateway 到本机 Agent 的环回，在已有 tailnet 身份时用本节点 ID；还没有 tailnet 身份时才用 `local`。见 [RUL-02](rulings.md#rul-02)。
- `mcp_session_id` 来自该 MCP 连接的会话标识。
- 经入口进来的会话另外记下 `oauth_client`，来源是请求头 `X-Xmesh-Oauth-Client`，不是工具参数。

工具参数里如果出现 `caller_id`、`owner` 或 `node_id`，一律忽略，不得覆盖传输层身份。唯一的例外是 `pty.transfer` 的 `to_caller`：那是目标身份，不是调用方身份，而且目标必须是 Agent 已经见过的、尚未过期的 `caller_id`，或者与调用方具有相同 `node_id` 的会话。禁止把会话转给任意字符串，以免成为伪造 owner 的入口。

### 生命周期

| 事件 | 身份上的资源 |
| --- | --- |
| MCP 会话建立 | 新的 `caller_id` 开始存在。此时还没有配额占用 |
| 会话结束 | PTY `Orphan`；`per-caller` 后端回收；exclusive 租约停止心跳，TTL 到点释放；该 caller 的幂等缓存可以立即丢弃 |
| 节点从 tailnet 消失 | 该 `node_id` 前缀下的全部会话按「会话结束」处理 |

### 相等

比较是字节相等，区分大小写。Tailscale 节点 ID 的规范形式由 WhoIs 给出，实现不得再做大小写折叠。

### 在错误里的出现

`held_by` 与 `owner` 使用完整 `caller_id`。对模型展示时不要替换成主机名；主机名放在 `sys.info` 和 `devices.list`。
