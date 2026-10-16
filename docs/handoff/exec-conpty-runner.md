# exec/ConPTY 工具层接入 AppContainer 与 helper 退出码 66 调查

分支 `claude/xmesh-exec-conpty`（基于 `199f7a3`，当前头 `2632168`）。本文件为独立交接说明，不修改 `docs/implementation-status.md`；整合线程按需摘录。

## 1. 实现了什么（代码）

- 新增平台无关接口 `internal/sandbox`：`Runner{Start, StartTerminal}`。托管型 runner 自己创建并清理进程，不改写 argv，区别于 bwrap / sandbox-exec 一类启动器式隔离。
- `internal/policy`：`Engine.UseSandboxRunner` / `SandboxRunner()`，仅当 `Isolation == "L2"` 且已安装 runner 时返回非空。`Sandbox` / `SandboxPTY` 未改，Windows 的默认返回仍是 `sandbox.unavailable`。
- `internal/exec`、`internal/pty`：存在 runner 时走托管路径（`managed.go`），否则保持原有启动器路径（`launched.go` 为原流程原样搬移）。超时、取消、清理失败（`exec.sandbox_cleanup`）都有明确语义；清理使用与调用上下文脱钩、带 90 秒上限的上下文。
- `internal/winbox/runner_windows.go`、`runner_terminal_windows.go`：`Runner` 每次调用一个完整的 `workspaceExecution`（受信 writer guard → journal → profile → 工作区授权 → USN → 受监督 Job → 监视器），结束时按既有 `closeContext` 顺序回收。清理失败的执行被保留并在下一次准入前重试，失败即关闭（不在未回收的 profile / ACL 之上继续准入）。ConPTY 终端在整个执行被终止并回收之后才关闭伪控制台，输出持续排空。
- `WorkspaceCoordinator` 是调用方必须提供的受信 writer 协调边界；本包不提供生产实现。

## 2. 验证层次

| 层次 | 内容 | 证据 |
| --- | --- | --- |
| 单元（模拟） | `internal/exec`、`internal/pty` 用假 runner / 假终端覆盖超时、取消、清理失败、stdin、相对路径解析 | 三平台常规检查，`2632168` 的 [Go 实现 38871936002](https://github.com/tzf1003/xmesh/actions/runs/38871936002) 通过 |
| 原生 Windows（真实 AppContainer，`-race`） | 7 项 `Test_WindowsManagedRunner*`：exec 证明（真实 AppContainer token、工作区内可写、工作区外读取被拒、stdin/stdout/stderr/退出码/环境）、进程树超时与正常退出后后代全部消失、并发执行、失败路径、ConPTY 终端（含 `mode con` 调整到 137 列）、终端 KILL 后后代进程消失；每项之后核对 journal / ACL / profile 准确回收 | [压测工作流 38871936071](https://github.com/tzf1003/xmesh/actions/runs/38871936071) 的 `runner` 作业通过 |
| 真实环境验收 | 未做：三台物理局域网设备、真实 OAuth、公网入口、云出口 | 需要用户环境 |

`Test_WindowsManagedRunner*` 有意不叫 `Test_WindowsAppContainer*`：主 `go.yml` 的原生步骤按 `WindowsAppContainer` 过滤且总超时 180 秒，原有用例已占 90–125 秒，加入后曾触发超时。这些用例由 `.github/workflows/windows-race-stress.yml` 的 `runner` 作业运行。整合时可选择把 `go.yml` 的超时调大并改名纳入主门禁。

## 3. 明确未完成 / 未提升

- **Windows L2 仍为 `sandbox.unavailable`**。`cmd/xmesh` 不安装 runner；仓库内没有生产 `WorkspaceCoordinator`，测试里的 coordinator 是测试替身（由夹具自行保证外部 writer 静止），不构成生产证明。
- 未做：外部 namespace / reparse / ACL writer 的生产执行机制；宿主异常终止后 guard 的存续；完整对象 census 与未知导入；跨用户 / 跨会话。
- 每次调用做完整准入（profile、授权、USN、监督器），单次约 1.4 秒（原生 exec 用例），重负载与 `node_modules` 类大工作区、硬链接等对象上限未评估。
- 终端 `Signal("INT")` 以向伪控制台写入 0x03 实现，未对所有前台程序做兼容验证。

## 4. 退出码 66 调查

结论依据 `windows-race-stress.yml` 的 `stress` 作业，在 windows-latest 的真实 AppContainer 中运行（完整日志可按下列运行取回）。

事实：

1. 66 是 Go race 检测器（ThreadSanitizer）的默认退出码，报告到数据竞争和 TSan 运行时自身内部中止都返回 66，单看退出码无法区分。
2. 同一个有真实数据竞争的子进程：容器外退出 66，并写出 `DATA RACE` 报告；在 AppContainer 内 3 次均退出 66，但没有任何竞争报告，只留下 `ThreadSanitizer: failed to restore address 0x…` 的内部中止（作业 116877823672 与 116873429361 一致）。即：AppContainer 内报告竞争的路径本身会中止，把真实竞争变成一个无法读出原因的 66。
3. 不含竞争的 race 构建子进程：在 AppContainer 内 300 次、容器外 300 次（作业 116877823672）均退出 0；另一轮 AppContainer 内 300 次中有 1 次 66，日志为 `ThreadSanitizer failed to allocate 0x000000800000 bytes at 0x028000000000 (error code: 487)`（作业 116873429361，launch 81）。487 是固定地址映射失败，发生在任何测试代码运行之前。两轮合计 AppContainer 内 1/600，容器外 0/300，样本不足以判断是否与 AppContainer 相关。
4. 大量同步事件（40 万次加锁）和 256 MiB 分配在 AppContainer 内各 10/10 退出 0，未复现。
5. `Test_WindowsAppContainerWorkspaceDeletionRecovery` 在 race 构建下重复 25 次、两轮均通过，未再出现 `e747cb8` 的 66。

对 `e747cb8` 的结论：

- 可确认存在两种在 AppContainer 内产生与它一致的"66 且无可读原因"的机制：(a) race 运行时启动期固定地址映射失败（约 1/600 的观察率）；(b) 子进程内真实数据竞争，其报告在 AppContainer 内被运行时中止吞掉。
- 因 `e747cb8` 的失败日志没有保留 TSan 输出，**无法证明它属于哪一种**，也没有证据表明是产品逻辑的确定性错误；该路径在后续多轮与 25 次重复中均通过。
- 产品二进制不使用 `-race`，这些现象只影响 race 构建的测试 helper。
- 建议（未在本分支实现，涉及他人拥有的 `object_windows_test.go`）：AppContainer 内运行的 helper 把 `GORACE=log_path=<相对名>` 写入已授权的工作目录并在失败时输出；对"启动期映射失败"签名（`failed to allocate` 且无 `DATA RACE`）至多重试一次；其余 66 仍判为失败，因为它可能是被吞掉的真实竞争。
- 注意：TSan 选项以冒号分隔，`GORACE=log_path=C:\…` 带盘符路径是非法选项并会直接退出 66，需使用相对路径。

## 5. 复核命令

- `GOOS=windows go vet ./internal/winbox`
- `go test ./internal/exec ./internal/pty ./internal/policy ./internal/sandbox`
- 原生：`XMESH_TEST_WINDOWS_SANDBOX=1 go test -race ./internal/winbox -run Test_WindowsManagedRunner -v`
- 压测：`XMESH_TEST_WINDOWS_SANDBOX=1 XMESH_TEST_WINDOWS_RACE_STRESS=300 go test -race ./internal/winbox -run 'Test_WindowsAppContainerRace' -v`
