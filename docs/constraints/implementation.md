# 实现约束

## CON-IMPL-01

交付顺序固定，前一阶段的测试未绿时不把下一阶段的网络依赖混进默认测试：

1. `cwd`
2. `fs`
3. `pty`
4. `exec`
5. `mcpmux`、`backend`、`lease`
6. `mesh`（tsnet）
7. `gateway`（含 ingress 的 OAuth）

阶段 5 的策略引擎可以在阶段 1 就以最小拒绝规则出现，因为 [CON-SEC-10](security.md#con-sec-10) 是工具语义的一部分。沙箱 L2 可以留到阶段 5。

## CON-IMPL-02

前缀拆分、JSON-RPC id 重映射、progressToken 重映射、`tools/list_changed` 聚合，只实现在 `internal/mcpmux`。Gateway、Ingress 和 Agent 后端转发调用这个包。禁止在三个角色里各写一份 id 映射。

验收：重映射的单元测试不依赖 tsnet，也不依赖真实后端进程。用管道模拟即可。

## CON-IMPL-03

`pty.Manager` 的互斥锁保护 `sessions` 与 `byCaller`。每个 `Session` 的条件变量使用 Session 自己的锁，保护 `ring` 与 `cursor`。持有 Manager 锁时禁止等待 Session 条件变量。读环形缓冲与写环形缓冲可以并发，由 ring 自身的同步保证读者不看到撕裂的记录边界。

## CON-IMPL-04

Windows 上 `pty.default_shell: auto` 解析为 `pwsh`，`pwsh` 不存在时解析为 `powershell`。禁止回退到 `cmd.exe`。信号映射：

| 请求 | Windows 行为 |
| --- | --- |
| `INT` | 向该 ConPTY 进程组 `GenerateConsoleCtrlEvent(CTRL_C_EVENT)` |
| `TERM` | `CTRL_BREAK_EVENT`，若进程不处理则在短超时后 `TerminateProcess` |
| `KILL` | `TerminateProcess` |

WSL 发行版不由 Windows 节点的 `exec` 隐式进入。需要 Linux 工具时，把该发行版注册成另一台 xmesh 设备。

## CON-IMPL-05

`fs.grep` 在 `PATH` 中找到 `rg` 时执行 `rg --json`，并把 JSON 事件转换成 [CTR-FS-05](../contracts/fs.md#ctr-fs-05) 的结果。找不到 `rg` 时用 `WalkDir`、RE2 兼容的正则和 `.gitignore` 规则产生同一结构。禁止一条路径返回 `files` 模式、另一条路径返回纯文本行。

正则方言是 RE2。`rg` 收到 RE2 不支持的语法时，应与纯 Go 路径一样返回 `invalid_args`，而不是让 `rg` 用自己的方言「尽力」匹配。实现可以在调用 `rg` 前用 Go 的 RE2 引擎编译一次模式，编译失败即拒绝。
