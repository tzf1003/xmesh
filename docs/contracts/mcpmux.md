# MCP 多路复用

`internal/mcpmux` 同时服务于三件事：Gateway 把多个上游合成一个工具表、入口把 `local/` 换成节点名、Agent 把多个上游接到一个后端连接。三处共用映射与前缀代码，见 [CON-IMPL-02](../constraints/implementation.md#con-impl-02)。

## CTR-MUX-01

### 下行请求

上游 JSON-RPC 请求的 `id` 记为 `up_id`，类型可能是字符串或数字。多路复用器分配下游 `down_id`，在该下游连接上唯一。保存：

```text
down_id → (upstream_conn, up_id, progress_tokens)
```

下游响应的 `id` 替换回 `up_id` 后只写回 `upstream_conn`。禁止按 `up_id` 的值在所有连接上查找，因为两个上游可以同时使用 `id = 1`。

上游断开时，删除该连接的全部映射。若下游请求仍未完成，不把迟到响应发给别的连接；直接丢弃。

### 前缀

工具列表聚合时，每条后端或对端工具加上前缀。调用时只剥离一次最外层前缀：

| 视图 | 列表中的名字 | 转发目标 |
| --- | --- | --- |
| 本机 Gateway | `local/fs.read` | 本机 Agent 的 `fs.read` |
| 本机 Gateway | `win-dev-01/ida.decompile_function` | 该节点的同名工具 |
| 入口 | `win-dev-01/fs.read` | 该节点。若目标就是本机，把对外名字中的节点前缀还原成 Agent 的无前缀工具 |
| Agent 后端 | `ida.decompile_function` | 后端工具 `decompile_function` |

未知前缀 → `not_found`。剥离后如果还剩下空名字 → `invalid_args`。

## CTR-MUX-02

请求参数中的 `progressToken`（位于 `_meta`）逐个登记到该 `down_id`。后端发出的进度通知若携带已知 token，改写回上游原来的 token，并只发给对应上游。未知 token 的进度通知丢弃并记一条审计 `progress_unrouted`，不广播。

## CTR-MUX-03

后端作为客户端发出的 `sampling/createMessage` 或 elicitation 请求：

- 该后端存在未过期 exclusive 租约：把请求转给 owner 的上游连接，并把这次反向请求的 id 单独映射。
- `shared` 后端，或 exclusive 但没有持有者：拒绝该反向请求，错误码 `-32601` 或 MCP 规定的「方法不可用」，不向任意上游碰运气。
- `per-caller`：转给拥有该进程的 `caller_id`。

上游拒绝或断线时，把拒绝原样交回后端，不换成另一个模型会话。

## CTR-MUX-04

### 工具表版本

每个聚合视图有一个单调的世代号。下列事件使世代加 1，并向所有仍连接的上游发送 `notifications/tools/list_changed`：

- 后端 `tools/list` 的结果变化
- 后端从 `running` 变为 `crashed` 或相反
- 对等节点上线或下线
- `expose_backends` 或 favorite 标记变化

发送失败的上游留到下一次请求时再发。不得因为一个上游堵住而停掉其他上游的通知。

### 崩溃顺序

与 [CTR-BE-01](backend-lease.md#ctr-be-01) 一致，并且：

1. 正在等待该后端响应的全部上游，收到 `backend_unavailable`，而不是一直挂到自己的超时。
2. 然后才尝试拉起。
3. 拉起完成后发 `tools/list_changed`。
4. 旧 fencing token 在步骤 1 之前已经失效，因此这些错误返回与新的 acquire 不会使用同一个 token。

### 测试要求

用三条内存中的 JSON-RPC 管道即可验证：两个上游 id 都是 `1` 时响应不串线；进度只回到发起者；无租约时 sampling 被拒绝；下游断开时两个上游都收到 `backend_unavailable`。这些测试属于阶段 2 的门，不依赖网络。
