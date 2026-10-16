# 实现与真实验收清单（2026-10-16 交接核验）

本清单按[符合性矩阵](../conformance.md)、[路线图](../design/10-roadmap.md)、[运行手册](../operations.md)和[实现状态](../implementation-status.md)整理，把每一项分成三个层次：

- **代码实现**：功能在仓库中，由 Go 单元测试覆盖。
- **模拟/CI 验证**：三平台 race/vet/build 与 Windows 原生 CI（GitHub 托管 runner）通过。
- **真实环境验收**：目标设备、目标账户、真实云端客户端或真实网络下的观察。

只有三层都满足，才算该项完成。以下状态以 `199f7a3` 的 push CI 38862655646、PR CI 38862662436 及契约工作流 38862655545、38862662445 为基线。

## 核验结论

- 远端工作分支 `codex/implementation-acceptance` 的 HEAD 为 `199f7a391c31b506ac8257b0117663d0abb452f1`，draft PR #2（目标 `master`）指向同一提交，状态为 draft，合并状态 clean。
- 本地复核：`go test -race ./...` 在 Linux 容器内除 `internal/pty` 的 `Test_JobControlInterruptAndClose` 外全部通过；该用例失败原因是容器 PID 1 不回收孤儿进程，被终止的后台任务保持 zombie，`kill(pid,0)` 仍返回成功。测试现在读取 `/proc/<pid>/stat` 把 zombie 视为已退出，容器内三次重复通过。`go vet`、Windows amd64/arm64 vet、`internal/winbox` 测试交叉编译与契约夹具检查通过。
- Windows 原生验收只能通过 GitHub CI 的 Windows runner 取得；本容器无 Windows 主机。

## 一、门 1 至门 4 与阶段 5

| 门 | 代码实现 | 模拟/CI 验证 | 真实环境验收 | 缺口 |
| --- | --- | --- | --- | --- |
| 门 1 工具基础 | 25 个内置工具、PTY 配额、策略 | 三平台 race；Windows Server 2025 原生进程、ConPTY、文件、HTTP MCP | 完成 | Claude Code 人工连接观察待执行 |
| 门 2 后端与并发 | mcpmux、backend、lease | 三平台 race；macOS 与 Ubuntu 的 HTTP 集成覆盖后端与多路复用 | 仓库中没有门 2 固定场景（脚本模拟 IDA、杀后端后旧 token 失效）的独立验收记录 | 核对并补记该场景 |
| 门 3 互联 | tsnet、LAN mTLS、mDNS | 官方 Tailscale 与 Headscale 的两台物理主机、隔离 Linux 沙箱、DERP | 两主机加沙箱通过 | 三台物理设备与真实 LAN 多播待验收 |
| 门 4 Gateway 与 OAuth | 元数据、动态注册、PKCE、PAT、三种监听 | 本机与 Ubuntu 流程通过 | 未完成 | claude.ai 与 Codex 真实 OAuth、模型工具表可见性、一次真实 `exec.run`；公共域名、Funnel 或隧道 |
| 阶段 5 治理 | 审计、预算、drain、策略热加载 | 三平台 race；Linux bwrap L2 在甲骨文环境通过 | 部分 | Codex Cloud 与 Claude 云沙箱的真实出站与入网记录 |

## 二、Windows 生产 L2（保持 `sandbox.unavailable`）

下列是解除 `sandbox.unavailable` 之前必须同时成立的条件。

| 条件 | 代码实现 | 模拟/CI 验证 | 剩余 |
| --- | --- | --- | --- |
| AppContainer profile、空能力启动、Job 创建期归属 | 完成 | Windows 原生通过 | 无 |
| 工作区授权与按身份回收（根/新增/移出对象，DACL/控制位/标签准确恢复） | 完成 | 原生通过 | 无 |
| 持久 journal、USN 补读与 checkpoint、删除回执、恢复调度 | 完成 | 原生通过 | 无 |
| 独立进程 supervisor 与持久退出回执 | 完成 | 原生通过 | 无 |
| 卷级 namespace lease（参与协议的操作互斥） | 完成 | 原生通过 | 只约束参与协议的操作 |
| 受管标准流与 ConPTY 启动 | 完成（内部原语） | 原生通过 | 工具层 `exec`/`pty` 接入 |
| 生命周期所有者与协调导入（start/import/close 串行，来源 guard 上限 256） | 完成（内部） | 原生通过，291 个 `Test_` PASS 标识 | 接入生产路径 |
| **外部 namespace/reparse/ACL 写入者强制约束** | 未实现 | 无 | 子讨论串 1 |
| **宿主崩溃后仍存活、覆盖恢复阶段的外部 guard** | keeper 进程已实现（`1f2616b`） | Windows 原生通过：强杀宿主后 keeper 存活、接管、恢复完成后释放 | 接入生产启动路径；不冻结树内写入；整机重启/外层 Job 终止时失败关闭 |
| **完整对象 census 与未知导入识别** | 未知导入归并与 `workspace-unknown-import` 已实现（`37645b5`） | Windows 原生通过（五个子场景加生命周期场景） | 事后归并不证明子树完整，仍需完整 census 证明 |
| **exec/ConPTY 工具层接入** | 未实现 | 无 | 子讨论串 4 |
| 普通用户登录任务、服务联网、跨用户/会话 ACL 与 mutex | 登录任务与可选 SCM 安装已实现 | 本机模式通过 | 需要真实 Windows 普通账户会话 |
| `e747cb8` 的 helper exit 66 原因 | — | 之后各轮未复现；helper 已保留私有 race 诊断 | 子讨论串 4 调查 |

## 三、跨主机与外部环境

| 项目 | 状态 | 需要的外部条件 |
| --- | --- | --- |
| 跨主机 ingress/PTY 预算 | 同机共享预算文件与多入口准入已实现并通过三平台原生测试；跨主机协调未实现 | [网关契约](../contracts/gateway.md)明确当前不承诺跨主机原子总额，跨主机入口需要独立协调服务；实现前须先做设计决策（协调服务形态与失败语义），需用户确认 |
| 三台物理设备互联、真实 LAN 多播 | 待验收 | 三台物理设备与同一局域网 |
| claude.ai 与 Codex 真实 OAuth 及工具可见性 | 待验收 | 公共可达的 ingress、真实账号授权；凭据只经配置好的安全环境提供 |
| 公共域名、隧道或 Funnel | 三种监听已装配，待验收 | 域名、Funnel 或隧道账号 |
| 云沙箱入网与出站 | bwrap（甲骨文）已通过；云厂商沙箱待验收 | Codex Cloud 与 Claude 云沙箱实例 |
| Claude Code 人工连接观察 | 待执行 | 人工操作 |

## 四、本轮并行分工

| 子任务 | 内容 | 集成方式 |
| --- | --- | --- |
| 1 | 外部 namespace/reparse/ACL 写入者强制约束（进行中） | 子分支完成后由集成者合并到工作分支，Windows 原生结果以 GitHub CI 为准 |
| 2 | 宿主崩溃后独立存活的外部 guard（已合入 `1f2616b`） | 同上；与子任务 1 同改 `lifecycle_windows.go`，由集成者解决冲突 |
| 3 | 完整 census 与未知导入识别（已合入 `37645b5`） | 同上 |
| 4 | exec/ConPTY 工具层接入；exit 66 调查 | 同上 |

`docs/implementation-status.md` 只由集成者修改；子任务把文档更新内容交给集成者。
