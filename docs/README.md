# XmeSh 文档库

本文档库是 XmeSh 的方案说明，以及实现与测试必须遵守的约束和契约。方案正文按主题拆开；约束回答「什么必须始终成立」；契约回答「跨边界的请求、响应、错误和副作用长什么样」。

日期：2026-10-15。原文里用嵌入图表达的三张图，在本库中改写成图示：架构见 [整体架构](design/02-architecture.md)，PTY 状态机见 [并发](design/06-concurrency.md)，路线图见 [落地路线](design/10-roadmap.md)。这些图是 Mermaid 源码，GitHub 会渲染；其他 Markdown 阅读器可能只显示代码块。

## 规范用语

| 用语 | 含义 |
| --- | --- |
| **必须** | 实现不满足就不算符合本库。测试要能抓住违反。 |
| **禁止** | 必须不发生。出现即缺陷。 |
| **应该** | 强烈建议。偏离要在实现说明里写原因。 |
| **可以** | 允许的选择，不是要求。 |

约束编号 `CON-*`，不变量编号 `INV-*`，契约编号 `CTR-*`，契约裁定编号 `RUL-*`。代码、测试和评审引用这些编号，不复述大段正文。

## 文档库和原文的关系

- `design/` 保留方案中的架构、配置、工具面、路线和已决事项，并补上原文图所表达的结构。
- `constraints/` 把方案里的设计原则和分散的「必须 / 不能」收成可检查的条目。
- `contracts/` 把工具、错误、身份、租约、多路复用、OAuth 和配置收成可实现的接口。
- 原文没有写死、但没有唯一行为就无法实现或测试的点，记在 [契约裁定](contracts/rulings.md)。裁定是本库的一部分，后续改行为要改裁定和对应契约，不能只改代码。

## 阅读顺序

1. [概述与目标](design/01-overview.md)
2. [整体架构](design/02-architecture.md)
3. [约束索引](constraints/README.md) 与 [不变量](constraints/invariants.md)
4. 按自己要写的包读契约：[工具与错误](contracts/README.md)
5. 开工前对照 [符合性矩阵](conformance.md) 里该阶段的编号

## 目录

### 方案

| 文档 | 内容 |
| --- | --- |
| [01 概述与目标](design/01-overview.md) | 问题、设计原则 |
| [02 整体架构](design/02-architecture.md) | 三角色、调用路径、组件边界 |
| [03 互联与部署](design/03-mesh.md) | 控制面、网络模式、入口、接入矩阵 |
| [04 节点与 Agent](design/04-agent.md) | 工具来源、`xmesh.yaml`、常驻方式 |
| [05 工具面](design/05-tools.md) | `sys` / `exec` / `pty` / `fs` / 后端工具 |
| [06 并发与资源](design/06-concurrency.md) | PTY、exec、租约、MCP 多路复用 |
| [07 Gateway 与入口](design/07-gateway.md) | 发现、命名、会话默认值、OAuth、接入 |
| [08 安全与授权](design/08-security.md) | 双层授权、策略引擎、配额、审计 |
| [09 Go 实现](design/09-implementation.md) | 仓库骨架、依赖、跨平台 |
| [10 落地路线](design/10-roadmap.md) | 五个阶段与验收，以及阶段 6 概览 |
| [11 已决事项与风险](design/11-decisions.md) | 五项已决、风险与缓解 |
| [12 桌面 UI 与本机控制通道](design/12-ui-and-control.md) | Windows / macOS 托盘与面板、UI 与内核解耦、拆分与验收 |
| [13 tailnet 管理与 DERP 提供商路径](design/13-mesh-admin-and-derp.md) | UI 端管理 tag、权限、第三方 DERP；国内 DERP 提供商的研究与风险 |
| [14 设备连接密码与发现](design/14-password-pairing.md) | LAN / Tailnet 统一 SPAKE2 配对、设备公钥信任与发现 |
| [15 GitHub Actions 运行器托管](design/15-github-runner.md) | 内核监督官方运行器：面板绑定仓库、多项目、保活、导入已有运行器；CTR-CTL-15 |
| [17 快速安装与首次配置](design/17-quick-setup.md) | 统一安装入口、agent 协议、首次向导、恢复与连接验收 |
| [19 Computer 三端远程桌面](design/19-computer.md) | Agent 桌面观察与输入、租约、三端执行器和验收进度（实现中） |
| [术语表](glossary.md) | 角色、身份、资源名词 |

### 约束

| 文档 | 内容 |
| --- | --- |
| [索引](constraints/README.md) | 全部 `CON-*` / `INV-*` 一览 |
| [不变量](constraints/invariants.md) | 运行时必须保持的性质 |
| [架构与角色](constraints/architecture.md) | 协议、进程、状态归属 |
| [网络](constraints/network.md) | tailnet、入口、禁止公网监听 |
| [工具与路径](constraints/tools.md) | 路径解析、文件与命令语义 |
| [并发与资源](constraints/concurrency.md) | 配额、所有权、串行与缓存 |
| [安全](constraints/security.md) | 授权、策略、审计、凭证 |
| [实现](constraints/implementation.md) | 包边界、锁、平台、开发顺序 |

### 契约

| 文档 | 内容 |
| --- | --- |
| [索引](contracts/README.md) | 全部 `CTR-*` 与模式文件 |
| [裁定](contracts/rulings.md) | 原文未写死、本库补上的行为 |
| [错误](contracts/errors.md) | 结构化错误信封与错误码 |
| [调用方身份](contracts/identity.md) | `caller_id` 的构成与生命周期 |
| [路径](contracts/cwd.md) | `cwd.Resolver` |
| [sys](contracts/sys.md) | `sys.info` |
| [exec](contracts/exec.md) | `exec.run` / `exec.output` |
| [pty](contracts/pty.md) | 会话池与八个工具 |
| [fs](contracts/fs.md) | 文件工具 |
| [后端与租约](contracts/backend-lease.md) | 透传、acquire、fencing |
| [MCP 多路复用](contracts/mcpmux.md) | id、进度、sampling、list_changed |
| [Gateway](contracts/gateway.md) | 命名、发现、会话默认值 |
| [OAuth 与授权](contracts/oauth-authz.md) | 端点、scope、双层判定 |
| [审计](contracts/audit.md) | 日志记录的字段与禁止项 |
| [配置](contracts/config.md) | `xmesh.yaml` 数据模型 |
| [模式](contracts/schemas/) | JSON Schema，供校验与测试夹具使用 |

### 符合性

[符合性矩阵](conformance.md) 把每条约束和契约映射到落地阶段和建议测试名。阶段验收时按该表勾选，不另起一套标准。
