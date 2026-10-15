# 并发与资源约束

## CON-CONC-01

PTY 会话池、exec 槽、后端 actor、文件乐观并发各自加锁。禁止用一把进程级互斥锁把四类操作串成全局队列。某一类内部的串行（例如单个后端 actor）不是全局大锁。

## CON-CONC-02

PTY owner、每调用方 PTY 计数、exec 公平队列、幂等缓存键、租约 owner、审计里的 caller，全部使用 [CTR-ID-01](../contracts/identity.md) 定义的 `caller_id`。禁止用主机名、OAuth client 名或模型自行上传的字符串代替，除非该字符串已经被入口映射进 `caller_id` 的节点部分并在文档中说明映射。

公网入口上的 OAuth client 不是 `caller_id`。入口在转发时使用入口节点自己的 tailnet 身份作为节点部分，MCP 会话 id 作为会话部分，并在审计里同时记下 OAuth client。见 [RUL-02](../contracts/rulings.md#rul-02)。

## CON-CONC-03

`Open` 在同一临界区内读取设备计数与调用方计数，两者都允许时才递增并插入会话。设备计数是 `active` 与 `orphaned` 之和，不含 `closed`。临界区之外启动 PTY 失败时，必须回滚计数并移除会话。并发 21 次 open 在上限 20 时，恰好 1 次得到 `pty_limit`，且 `active` 与 `orphaned` 之和不超过 20。

## CON-CONC-04

`pty.write`、`pty.resize`、`pty.signal`、`pty.close` 的调用方必须等于会话 owner。否则返回 `not_owner`，不产生写入、尺寸变化、信号或关闭。`pty.transfer` 仅当前 owner 可发起；`orphaned` 会话例外，见 [CTR-PTY-02](../contracts/pty.md#ctr-pty-02)。

## CON-CONC-05

`pty.read` 不检查 owner。`wait_ms` 大于 0 且没有新数据时，调用阻塞到有数据、eof、上下文取消或等待超时。超时返回空 `data`、原 cursor 作为 `next_cursor`、`eof: false`，这是成功结果，不是错误。

## CON-CONC-06

每个 `shared` 或 `exclusive` 后端有且仅有一个 actor，所有发往该后端的 MCP 请求在这个 actor 里串行。`per-caller` 是每个 `caller_id` 一个进程、一个 actor。禁止为了吞吐对同一个 stdio 后端并发写 JSON-RPC。

## CON-CONC-07

fencing token 在单个 Agent 进程、单个资源上严格递增。后端进程崩溃并被重新拉起时，先使当前租约失效并递增 token，再接受新的 `backend.acquire`。持有旧 token 的请求返回 `resource_busy`，`reason` 为 `token_stale`。

## CON-CONC-08

租约表是进程内存中的 map。禁止把租约写入磁盘后在重启时恢复。重启后的第一次 acquire 必须成功（在后端本身可用的前提下），并且不承认重启前发出的 token。

## CON-CONC-09

`idempotency_key` 的缓存键是 `(caller_id, key)`。不同 caller 使用相同 key 不得命中对方的结果。每个 caller 保留最近 `idempotency_cache` 条，默认 200，超出时丢掉最旧的。缓存命中不再次执行命令，即使第一次的退出码非零。

缓存条目在 Agent 重启后消失。调用方必须把重复执行的后果考虑进 key 的使用方式；Agent 不承诺跨重启去重。

## CON-CONC-10

`mode: exclusive` 的命令开始执行前，共享槽中的命令必须已经结束。等待期间不再启动新的 shared 命令。已经在跑的 shared 命令不被杀掉。若在调用方 `timeout` 内等不到排空，返回 `exec_saturated`，命令未曾启动。

同时运行的 exclusive 命令数 ≤ `exec.exclusive_slots`，默认 1。

## CON-CONC-11

Agent 观察到某个 MCP 会话结束时，对该 `caller_id` 调用 `Orphan`：其全部 `active` PTY 变为 `orphaned`，owner 清空，每调用方计数减少。同一会话持有的 exclusive 租约不立即删除，等到 TTL，除非会话结束路径同时停止了续期。租约没有「连接一断立刻释放」的额外魔法，避免网络抖动拆掉 IDA 上下文；TTL 由 acquire 时的 ttl 与心跳决定。

`per-caller` 后端进程在会话结束时回收，不等 idle。

## CON-CONC-12

后端只有一条 MCP 连接、上游有多个会话时，Agent 分配自己的 JSON-RPC id，并保存 `(上游连接, 上游 id) → 后端 id`。后端响应和带 `progressToken` 的通知必须送回原来的上游，不得广播给其他会话。映射在请求完成或上游断开时删除。细节见 [CTR-MUX-01](../contracts/mcpmux.md#ctr-mux-01)。
