# Windows L2（AppContainer）联合验证与普通账户验收

范围：`agent.policy.windows_appcontainer: true` 时，生产启动链把 `isolation: L2` 的 exec 与 PTY 交给托管 AppContainer 运行器。开关默认关闭；本页记录哪些结论已由 CI 在真实 Windows 上证明，哪些必须在你的机器上验，以及是否可以考虑改默认值。状态汇总归 `docs/implementation-status.md`，这里不重复。

## CI 已证明（`windows-l2-joint.yml`，每轮三组，共三轮）

| 组 | 命令 | 证明什么 |
| --- | --- | --- |
| 托管运行器 | `go test -race ./internal/winbox -run Test_WindowsManagedRunner` | exec 与 ConPTY 经运行器：进程树清理、并发、失败路径、终端中断 |
| serve 接入 | `go test -race ./internal/app ./cmd/xmesh -run WindowsL2` | 真实 `xmesh serve` 在开关打开时：沙箱外文件读不到、工作目录内可写、完成后日志与 ACL 恢复、PTY、进程被杀后的恢复（有 keeper 与无 keeper）、日志损坏时 L2 继续 `sandbox.unavailable`、AppContainer 内的命令打不开控制管道而本账户可以 |
| 反例 | `go test -race ./internal/winbox -run WindowsAppContainer` | 外部写入者（命名空间活动、junction）、描述符被改、写入围栏、删除恢复、硬链接与重解析点、凭据目录别名等 |

三轮要全绿；任何一轮失败，日志里会按轮次和用例标出。

本轮顺带修掉两个偶发：

- `Test_WindowsAppContainerTreeExternalWriters/junction`：监视器每个周期按对象 ID 重开新建对象。外部写入者在对象被记录之后、被重开之前把它改成 junction 时，重开先于 USN 的重解析记录被看到，故障原因变成「对象类型不符」而不是链接变更。现在重开发现重解析点或多重链接，同样报告链接变更（仍是写入围栏违规，沙箱进程同样被终止）。断言没有放宽，新增 `Test_WindowsAppContainerIdentityReopenReportsLinkChange` 固定这一路径。
- `Test_WindowsAppContainerWorkspaceChangeMonitor/delayed-delete` 的 20 秒就绪超时：一个阶段正常只要不到 1 秒，所以是 helper 没有起来。之前的日志只有「sandbox readiness context deadline exceeded」。现在 helper 提前退出会立即带着退出码、race 运行时日志和 helper 自己写下的失败原因报错；只有退出码 66 且日志是「failed to allocate」（race 运行时启动时映射不到内存，没有 DATA RACE 报告）才整阶段重来一次，与仓库里其他 helper 测试同一规则。若再出现，失败信息会带原因。

## 还不能证明的

- 真实物理设备与真实用户登录会话：CI 的 runner 是内置 Administrator，普通账户与 UAC 分离令牌下「被拒绝并给出原因」的整条链（托盘、面板）需要你在自己的 Windows 上验（下节）。
- 其他杀软或企业策略（Defender 实时扫描、AppLocker）对 AppContainer 子进程的影响。
- 工作区所在卷不是 NTFS、或位于网络路径时的行为：运行器只在本地 NTFS 上验证过。
- 长时间运行（小时级）的 USN 监视器、日志写满、大工作区的准入成本（T10）。

因此**不建议现在把开关默认改为打开**。建议保持默认关闭、标为实验性，等下面两节在真实机器上通过后再由你决定。

## 账户与预期

读取 NTFS 变更日志要打开原始卷设备，Windows 只授予管理员（已提权的令牌）和 LocalSystem；未提权的管理员账户同样被拒。这是 L2 的安全要求（写入围栏靠它发现工作目录里的外部写入），不为普通账户放松。因此：

| 节点运行身份 | 开关打开后的预期 |
| --- | --- |
| 普通账户，或未提权的管理员令牌 | **被拒绝并给出原因**：节点启动时就探测，不创建任何运行器状态；`exec.run`、`pty.open` 返回 `policy_denied` / `sandbox.unavailable`，`reason` 是「当前账户无法读取 NTFS 变更日志，Windows L2 需要以管理员（提权）或 LocalSystem 服务运行节点」；`sys.info.warnings` 与控制通道 `status.warnings`（`sandbox_unavailable`，托盘变黄、面板概览显示）带同一原因；其他工具照常 |
| 以管理员身份提权运行，或作为 LocalSystem 服务（`install --system`） | 成功路径，下面「成功路径验收」 |

## 普通账户验收：被拒绝并给出原因（约 10 分钟）

需要：一台 Windows，普通（非管理员）账户登录并保持登录。节点安装方式见[运行手册 F](real-environment-runbook.md#f-windows-普通用户登录会话)，以下只写开关打开后的差异。

1. 按运行手册 F 第 1 步装好节点并注销再登录，确认任务在运行。
2. 用 L2 配置（`--isolation L2`）重新生成配置后，在 `agent.policy` 里加上 `"windows_appcontainer": true`，重启任务：

   ```powershell
   $ACC = "$HOME\xmesh-accept"
   py -3 scripts\accept_config.py lan --name win-a --out "$ACC\win-a" --pat-file "$ACC\gateway.pat" --isolation L2 --force
   $cfg = "$ACC\win-a\config.json"
   $j = Get-Content $cfg -Raw | ConvertFrom-Json
   $j.agent.policy | Add-Member -NotePropertyName windows_appcontainer -NotePropertyValue $true -Force
   $j | ConvertTo-Json -Depth 20 | Set-Content $cfg -Encoding utf8
   Stop-ScheduledTask -TaskName "<任务名>"; Start-ScheduledTask -TaskName "<任务名>"
   ```

   改完配置若又用 `xmesh control pats` 等命令写过配置，文件可能已不是严格 JSON，`ConvertFrom-Json` 会失败；此时直接编辑文件，或重新生成。

3. 检查被拒绝并带原因：

   ```powershell
   py -3 scripts\accept_windows_l2.py --expect-refused --gateway 127.0.0.1:8080 --pat-file "$ACC\gateway.pat" `
     --workspace "<default_cwd>" --state-dir "$ACC\win-a\state"
   py -3 scripts\accept_expect_refusal.py --gateway 127.0.0.1:8080 --pat-file "$ACC\gateway.pat"
   xmesh status --control --json --state-dir "$ACC\win-a\state"
   ```

   期望：两个脚本都是 `"result": "PASS"`（`sys.info` 的 `warnings` 含 `sandbox.unavailable` 与原因，`exec.run` 被拒绝且 `reason` 非空）；`status` 输出的 `warnings` 里有 `{"code":"sandbox_unavailable","severity":"warning",...}`，托盘图标变黄，面板概览显示同一条原因。`$ACC\win-a\state\sandbox` 下不应有任何文件，工作目录的 ACL 与之前一致。

**交回：** 第 3 步三条命令的输出；托盘与面板概览的截图；`systeminfo` 里的系统版本。

## 成功路径验收（管理员提权或 LocalSystem 服务，约 20 分钟）

需要：同一台机器，以管理员身份打开提权的 PowerShell 运行 `xmesh serve`，或按运行手册安装为系统服务（`install --system`）。配置同上节第 2 步。

1. 跑联合检查（工作目录是配置里 `agent.default_cwd` 的值）：

   ```powershell
   py -3 scripts\accept_windows_l2.py --gateway 127.0.0.1:8080 --pat-file "$ACC\gateway.pat" `
     --workspace "<default_cwd>" --state-dir "$ACC\win-a\state"
   ```

   期望 `"result": "PASS"`，四项：运行器已启动、读不到工作目录外的文件、能在工作目录内写、打不开控制管道。
2. 崩溃恢复：执行一个长命令的同时结束 `xmesh.exe`（任务管理器），重启后再跑第 1 步。期望仍然 PASS，且 `$ACC\win-a\state\sandbox` 下没有遗留日志文件，工作目录的 ACL 与之前一致（`icacls <default_cwd>` 前后对比）。
3. 失败关闭：停止节点，往 `$ACC\win-a\state\sandbox` 放一个 512 字节的随机文件 `0123456789abcdef0123456789abcdef.journal`，启动节点。期望 `sys.info` 的 `warnings` 含 `sandbox.unavailable`，控制通道 `status.warnings` 含 `sandbox_unavailable`，exec 返回 `policy_denied`，其他工具照常。验完删掉该文件。

**交回：** 第 1 步输出；第 2、3 步的结论和命令输出；`systeminfo` 里的系统版本。
