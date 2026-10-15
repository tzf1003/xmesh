# 契约裁定

这些裁定补上方案里没有写死、但缺少唯一行为就无法测试的点。它们从 2026-10-15 起成为契约的一部分。推翻某条裁定时，修改本节、受影响的 `CTR-*`，并在 [已决事项](../design/11-decisions.md) 追加一条记录。

## RUL-01

`exec.run` 在槽位不足时，在本次调用剩余的 `timeout` 内阻塞等待。队列按 FIFO，并在有多个等待者时按 `caller_id` 轮转，避免一个调用方占满等待队列。

若剩余超时不够排到队首，或等待队列长度已经达到 `exec.max_concurrent`（默认与槽位数相同，即再排队一个就会超过「槽 + 等长队列」），立即返回 `exec_saturated`，字段 `queue_position` 是若接受本次调用它将处在的位置（从 1 开始）。命令进程不得已启动。

不返回「先排队、稍后另开一次调用来取结果」的异步句柄。幂等重试靠 `idempotency_key`。

## RUL-02

`caller_id` 的规范形式是：

```text
caller_id = node_id + "/" + mcp_session_id
```

- `node_id` 是对端的 Tailscale 稳定节点 ID（WhoIs 的 Node.ID），不是 MagicDNS 主机名。主机名会变，节点 ID 用来做 owner。
- `mcp_session_id` 是 Streamable HTTP 的 `Mcp-Session-Id`。没有会话头的初始化请求使用 `init` 这个字面量，且不能持有 PTY 或租约。
- 经公网入口转发时，Agent 看到的 `node_id` 是入口节点的节点 ID，`mcp_session_id` 是入口与目标 Agent 之间的 MCP 会话。入口必须为每个上游 OAuth 会话使用独立的下游 MCP 会话，使不同云端用户会话不会共享 owner。审计在入口侧另记 `oauth_client`。

阶段 1 还没有 tailnet 时，`node_id` 使用字面量 `local`，仅当对端是 `127.0.0.1`。

## RUL-03

`backend.acquire`、`backend.renew`、`backend.release` 是 Agent 的内置工具，不是后端进程列出的工具。Gateway 可以替当前 MCP 会话调用它们，模型也可以直接调用。exclusive 后端的业务工具不隐含自动加锁：没带 token 就返回 `lease_required`，避免两个模型交替调用把 IDA 上下文串掉。

心跳是调用方或 Gateway 周期性 `backend.renew`。Agent 不根据业务工具调用顺便续期，以免只读探测把租约无限延长。业务工具成功不改变 `Expires`。

## RUL-04

幂等缓存的键是 `(caller_id, idempotency_key)`。其他调用方提交同一个字符串不会拿到这份 stdout。这避免跨会话泄漏命令输出。同一 caller 的缓存命中跳过策略引擎之外的执行，但**不**跳过授权与策略：第二次调用若已被新策略拒绝，返回 `policy_denied`，不返回缓存。

## RUL-05

`exec.run` 的 stdout 与 stderr 各自内联上限为 `exec.inline_output_bytes`，默认 256 KiB。超过的部分不放进工具结果，`truncated: true`，并返回 `output_ref`。合并后的溢出内容保留到 `exec.output`，保留时间与幂等缓存相同，重启即失。方案原文只要求分页句柄，没有写内联阈值，故阈值做成配置，默认值写死在契约里，避免实现各选一个。

## RUL-06

`_lease_token` 是工具参数里的保留字段，类型为十进制字符串或 JSON 整数，规范比较时按无符号整数。Agent 在授权通过后、策略与租约检查时读取它，转发给后端之前删除。后端若自己的工具也有同名字段，仍然被剥离；后端工具不得使用这个名字。文档在接入后端时说明这一保留名。

## RUL-07

PTY 四个状态的规范名是 `active`、`orphaned`、`closed`、`released`。`pty.list` 不返回 `released`。语义见 [CTR-PTY-01](pty.md#ctr-pty-01)。

## RUL-08

工具失败时，MCP `tools/call` 的结果使用：

- `isError: true`
- `structuredContent` 等于错误对象，必须通过 [error.schema.json](schemas/error.schema.json)
- 一条 `type: text` 的 content，正文是该对象的紧凑 JSON，供只读文本的客户端使用

成功结果放在 `structuredContent`，并附一条文本 JSON。禁止只返回人类可读句子而不给结构化对象。

## RUL-09

scope 语法以 [CTR-OAUTH-02](oauth-authz.md#ctr-oauth-02) 为准。配置示例中的 `fs:*` 等价于同时授予 `fs:read:*` 与 `fs:write:*`。`exec:*` 等价于 `exec:*`（设备通配，没有更细的读与写）。实现接受简写，内部展开成规范 scope 再比较。

## RUL-10

`fs.edit` 零匹配与多匹配使用单独错误码 `edit_not_found` 与 `edit_ambiguous`，不用 `conflict`。`conflict` 只表示 `expected_sha256` 与当前内容不一致，或 `fs.move` 的目标已存在。这样模型能区分「字符串没找到」和「文件被别人改了」。
