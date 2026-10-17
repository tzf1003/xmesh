# 2026-10-17 用户实机实测摘要

本页是用户在自己机器上首轮实测的脱敏摘要，供审阅和后续复测对照。原始报告（`macos-test-report.md`、`windows-test-findings.md`）含本机路径和环境细节，保存在项目共享文件里，没有放进仓库。本页只记结论和可复现的步骤，不含个人路径、令牌或账号信息。

两次实测都只覆盖单机、本机模式（`serve --local`），没有三台设备互联，没有真实 OAuth、公网入口、云沙箱或真实 tailnet。因此它们是真实环境证据，但不改变[验收清单](2026-10-16-acceptance-checklist.md)里其余行的状态。

## macOS（Apple 芯片，macOS 27，被测提交 `4f95718`）

测试方式：从源码构建内核和未签名的安装包，在隔离目录用 `serve --local` 起节点，未装服务、未写登录项。

通过：

- 内核构建与 `xmesh version`；UI 类型检查、契约同步和单元测试；UI 端到端对假内核通过；打包产物校验（fuses、内置内核可运行、打包版连上假内核）。
- 打包版应用真实启动并连上真实内核；内核 SIGTERM 重启和 `kill -9` 后重启（残留套接字）都能自动重连。
- `xmesh control` 的 status、peers、sessions、audit、config get；PAT 创建、列出、轮转、删除及各类错误；PAT 对网关的效果（无令牌或错令牌 401，正确令牌 200，轮转后旧令牌 401，删除后 401），令牌文件权限 600。
- `pair export` 与 `pair import`：指纹不符被拒，正确指纹导入成功并提示需要重启。ingress 子命令在无 ingress 角色的节点上全部返回 `role_disabled`。
- 面板各页对真实内核都能渲染且无控制台报错；面板里创建、轮转、删除令牌，令牌只显示一次；暂停、终止、恢复；改配置的预览、保存和非法 YAML 诊断；审计页；单实例行为。
- 仓库自带冒烟 `scripts/smoke_local.py`（含 `--pat`）通过。

发现并已由 #41 修复：`Test_ControlAuditView` 在正时区机器上失败；面板无法保存只改注释的修改；启用 PAT 后复制的连接命令不带令牌占位符；内核被强杀后 `xmesh control` 报原始 Go 错误；`pair --export` 写 `"tags": null` 导致面板配对页拒绝；导入对端证书后托盘不显示需要重启。

未改动：删除最后一个 PAT 后残留空的 `pats: {}`（与缺省等价）；TMPDIR 过长导致套接字路径超限（环境问题）。

未测：Gatekeeper 与 `xattr` 放行（测试机已整体关闭 Gatekeeper）、托盘图标与菜单的实际外观、登录项自启、`install` 与 launchd 用户服务、向导最后的安装步骤、lan 模式下面板配对页与 mDNS、真实 ingress 角色、Tailnet 真实数据。

## Windows 11 24H2（x64，管理员账户的未提权令牌，被测提交 `28ffeef`）

发现的问题与处理状态：

| 编号 | 现象 | 状态 |
| --- | --- | --- |
| P1 | 商店版 `pwsh`（App Execution Alias）带 `PROC_THREAD_ATTRIBUTE_JOB_LIST` 创建进程时 `ERROR_ACCESS_DENIED`，`exec.run` 和 `pty.open`（`default_shell` 为 auto）返回 `internal`；`powershell.exe` 与 `cmd.exe` 正常 | #42 的第一版没有过用户复测（沿用失败创建里的 Job，加入全被拒）；#47 改为换新 Job，并拒绝应用执行别名、auto 跳过别名。Windows 真机复测 17/17 通过（经协调线转述），真实 pwsh 7 后代回收尚未验证 |
| P2 | `drain --mode kill` 之后 `resume`，`pty.open` 一直返回“终端管理器正在关闭”，直到重启节点；`exec.run` 与 `drain --mode keep` 不受影响 | 已修（#42），用户复测通过 |
| P3 | 面板配对页拒绝 `pair --export` 导出的 `tags: null` | 已由 #41 修复 |
| P4 | 未提权账户打开 `agent.policy.windows_appcontainer` 后，运行器“启动成功”，之后每次 exec 都是 `policy_denied`（`sandbox.runner_failed`）且没有原因，原因只在节点日志里 | 已由 #43 修复：启动前探测，走 `sandbox.unavailable` 并带原因，验收页改为普通账户预期被拒 |
| P5 | 控制通道 `status.warnings` 没有沙箱不可用告警，托盘与面板仍显示正常 | 已由 #43 修复：新增 `sandbox_unavailable` 告警 |
| P6 | 按 `ui/README` 本地打包，安装包生成后退出码仍为 1（缺 `repository`） | 已由 #44 修复 |
| S1 | 控制通道改写 JSON 配置后文件变成带未加引号键的 YAML flow | 已由 #44 修复（JSON 保持合法 JSON）；空 `pats: {}` 残留未清理 |
| S2 | 导入对端证书后 `restart_required` 不进 status，托盘不显示需要重启 | 已由 #41 修复 |
| S3 | `policy_reload_failed` 告警在配置恢复合法后一直保留到重启 | 已由 #43 修复 |
| S4 | `xmesh control events --count 2` 在空闲节点上收到 hello 后无限阻塞 | 已由 #44 修复：增加 `--timeout`，文档写明 `--count` 把 hello 也算在内 |
| S5 | `Test_ControlAuditView` 在 UTC+8 失败（#41 已修）；无符号链接权限账户上 `Test_WindowsPrivdirRefusesLinks`、`Test_CTRCWD01` 失败而不是跳过；`Test_CTRFS06_07_MetadataAndDelete` 同类 | 已由 #41、#44、#46 修复（#46 在创建符号链接失败时跳过该段并记日志，其余检查照常；#58 之后只在缺符号链接权限时跳过）|
| S6 | LAN 监听和 mDNS 取第一块私有网卡，多网卡或虚拟网卡机器可能选错 | 已由 #44 修复：新增 `mesh.lan.interface`，缺省跳过虚拟网卡 |
| S7 | 中文代码页输出 | 已由 #47 修复（非 UTF-8 输出按 OEM 代码页转 UTF-8），含在上述 17/17 真机复测里 |

对照项（用户机上观察到、行为符合预期）：开关关闭时 L2 请求返回 `policy_denied`/`sandbox.unavailable`；损坏的 journal 使启动恢复失败，exec 与 pty 仍返回 `sandbox.unavailable`，`sys.info` 的 warnings 有提示；失败后状态目录与工作目录 ACL 无残留。

## 仍待用户本机复测

- Windows：未安装服务时 `xmesh stop --json`；多网卡机器的 LAN 选网卡；无符号链接权限账户上 `go test ./internal/privdir ./internal/cwd`；未提权账户下 L2 被拒绝时托盘与面板的显示。
- macOS：#41 之后配对导入的“需要重启”托盘图标；上面“未测”一节的各项。
