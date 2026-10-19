# 调用方身份

## CTR-ID-01

### 形式

```text
caller_id = device_id "/" logical_session_id
```

字符集：`device_id` 与 `logical_session_id` 各自匹配 `[A-Za-z0-9._:-]{1,128}`。拼接后的字符串禁止再被截断。谁产生这两个部分见 [RUL-02](rulings.md#rul-02)。

### 谁可以声称身份

Agent 只从传输层推导 `caller_id`：

- 远端 `device_id` 来自双方公钥配对后的 mTLS 身份验证，在 LAN 与 Tailnet 路径上相同。网络地址、WhoIs、控制面节点 ID 和网络 tags 只描述路径。联网节点到自身的环回使用本设备 ID，`--local` 开发模式使用保留身份 `local`。
- `logical_session_id` 由设备会话服务生成。物理链路断线或换路时保留同一个 MCP 实例与逻辑会话标识。
- 经入口的会话另记 `oauth_client`。只有本地信任记录授予 `tag:ingress` 的设备可以提供该身份；内部链路把它绑定到被认证的逻辑会话，外部 HTTP 入口从 OAuth 授权推导。

工具参数里如果出现 `caller_id`、`owner` 或 `node_id`，一律忽略，不得覆盖传输层身份。唯一的例外是 `pty.transfer` 的 `to_caller`：那是目标身份，不是调用方身份，而且目标必须是 Agent 已经见过的、尚未过期的 `caller_id`，或者与调用方具有相同 `node_id` 的会话。禁止把会话转给任意字符串，以免成为伪造 owner 的入口。

### 生命周期

| 事件 | 身份上的资源 |
| --- | --- |
| MCP 会话建立 | 新的 `caller_id` 开始存在。此时还没有配额占用 |
| 会话结束 | PTY `Orphan`；`per-caller` 后端回收；exclusive 租约停止心跳，TTL 到点释放；该 caller 的幂等缓存可以立即丢弃 |
| 一条网络路径断开 | 逻辑会话进入恢复流程，优先 LAN、再尝试 Tailnet；原调用方身份和资源归属保持 |
| 全部路径失联超过恢复窗口、设备重启、服务停止或 xmesh 撤销设备信任 | 对应逻辑会话结束并执行资源清理。恢复窗口为 120 秒；已有请求截止时间和租约 TTL 继续生效 |

### 相等

比较是字节相等，区分大小写。设备 ID 使用 `dev-` 加公钥 SPKI 的小写十六进制 SHA-256；会话标识由会话服务生成，比较时不做大小写折叠。

### 在错误里的出现

`held_by` 与 `owner` 使用完整 `caller_id`。对模型展示时不要替换成主机名；主机名放在 `sys.info` 和 `devices.list`。
