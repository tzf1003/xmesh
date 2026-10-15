# 实现记录

本文件记录代码与验证状态；规范行为仍以契约和[符合性矩阵](conformance.md)为准。

## 2026-10-15 实现范围

| 范围 | 已实现行为 |
| --- | --- |
| 文件与路径 | 允许目录、别名、软链检查、UTF-8/BOM、原子写入、哈希冲突、精确编辑、删除预检、glob/grep 双引擎及嵌套忽略规则 |
| 命令 | 公平队列、shared/exclusive、幂等隔离、超时清理、输出分页、L1 策略、Linux bwrap 与 macOS Seatbelt L2 |
| PTY | 四状态、游标读取、配额、owner、断线孤儿、OAuth client 接管约束、转移串行化、Unix 会话进程组清理、Windows Job Object 进程树清理与创建期归属 |
| 后端 | stdio/HTTP MCP、actor 串行、exclusive fencing lease、空闲回收、故障失效、策略重载保留有效租约 |
| MCP 多路复用 | 上下游请求映射、通知、进度、sampling 路由、断线取消 |
| 设备网络 | tsnet 注册与 WhoIs、tag 授权、临时节点内存身份与退出注销、LAN 证书配对与续期、mTLS、mDNS 发现 |
| Gateway | 设备前缀、每设备独立能力刷新与重试退避、搜索、连接默认值、断线停路由、持续 drain 状态、本机 PAT 文件认证与 scope、凭据轮转和会话绑定 |
| OAuth 与入口 | 元数据、动态注册、PKCE S256、单主体口令、CSRF、scope、令牌轮转、客户端注册及回调 URI 持久化、Funnel/隧道/HTTPS 入口装配 |
| 治理与运行 | 元数据审计、热加载失败记录、重启提示、调用预算、Agent 主体与入口跨设备 PTY 预算、keep/kill drain、CLI 与服务失败退出、Windows 用户登录任务与可选 SCM 安装 |

25 个 Agent 内置工具已接入；Gateway 另提供设备列表、搜索和会话默认值。操作方法见[运行手册](operations.md)。

## 已执行验证

- macOS：`XMESH_TEST_L2=1 go test -race ./... -timeout=60s` 全量通过；`go vet ./...` 通过。HTTP 集成覆盖文件、exec、PTY、后端及 OAuth→Gateway→Agent 所有权隔离。
- Windows amd64 与 Linux amd64：`go build ./...` 交叉编译通过。
- 配置、错误、租约、审计的正反例及文档链接：`check_examples.py` 通过。
- Ubuntu 24.04 amd64 原生：PTY 6 项、exec 6 项、文件搜索 5 项及 policy 3 项通过。使用实际二进制的 L1、L2 MCP 冒烟均通过文件、exec、PTY、审计正文排除及正常退出检查。
- macOS 与 Ubuntu 实际二进制：带 PAT 的 L1 MCP 冒烟通过；覆盖 CLI 凭据读取、文件、exec、PTY、审计及退出。Gateway 认证测试覆盖 scope 过滤、写入拒绝、凭据轮转和跨凭据会话隔离。脚本为 `scripts/smoke_local.py --pat`。
- macOS 27.0.1 L2：原生测试验证目录外读取和写入拒绝、符号链接越界拒绝、网络拒绝、父进程信号与环境读取拒绝，以及含引号目录的参数传递。带 PAT 的真实 MCP L2 冒烟通过 exec 与 zsh PTY、审计及退出清理。
- Ubuntu 24.04 L2 PTY：在 PID 命名空间内通过 util-linux `setsid --ctty` 建立控制终端；原生测试连续三次通过窗口调整、API SIGINT、键盘 Ctrl-C、shell 持续交互与关闭后的后台进程清理。Linux L2 PTY 需要系统提供 `setsid`。
- Windows 用户任务：XML 注册参数、用户隔离命名、普通权限与登录会话配置、持续运行和失败重试、注册失败传播及临时文件清理的跨平台测试通过；新增权限检查通过 Windows amd64/arm64 vet 与 amd64 测试交叉编译，`63fd0e3` 的原生任务通过 XML 注册、身份、Session 2、目录、复杂参数和 token 类型检查；Runner 为内置 Administrator，非提升运行仍需标准或 UAC 分离 token 账户验收。
- Windows Server 2025 ConPTY：生产环境过滤器下，窗口调整、Ctrl-C 后持续交互、TERM/KILL/shell 退出后的后代清理、输出排空、重复关闭和启动失败清理均通过原生 race 回归。
- Windows Server 2025 exec：准入前挂起、加入 Job 后恢复、正常退出与超时后的进程树及阻塞 stdin 清理通过。命令启动使用创建期 Job 属性与显式标准句柄继承；`d4e96c4` 的参数、目录对象身份、输入、环境、退出码及宿主崩溃原生回归通过。PowerShell 5.1/7 标准与 Job 启动通过，包括父进程未设置或清空模块路径的场景；HTTP MCP 集成通过。
- Windows Server 2025 文件：共享读取期间原子替换、锁定目标保留与临时文件清理、长路径读取和替换通过原生 race 测试。Windows vet 与构建通过；逐项证据见 [Windows 验收记录](validation/2026-10-15-windows.md)。用户任务的登录触发与目标登录会话仍需验收。
- 三平台 CI：`3fd6730` 的 push 与 PR 工作流均通过 macOS、Ubuntu、Windows 全量 race 测试、vet 和构建，Windows 独立原生回归通过。MCP 进度在响应完成前捕获并保留转发路由；慢转发与同后端查询回归连续 100 轮通过。
- 入口 PTY 预算：两 Agent 集成测试覆盖跨设备并发准入、跨 MCP 会话主体计数、独立 client、入口重启后的孤儿计数与接管、关闭释放、向满额接收方转移拒绝、设备离线、结果不确定的持久化阻断与 Agent 重启恢复、状态写入失败的副作用阻断。控制资源授权与快照一致性测试通过。
- Ubuntu 原生入口预算：上述两 Agent 集成场景在甲骨文主机连续两轮通过；使用独立临时工作目录与真实 Linux PTY。该验证使用主机内两个 Agent，跨物理主机的入口验收仍待执行。
- 同机多入口预算：共享本地预算文件，通过操作系统文件锁串行查询和调用，每次准入重读持久化状态；共享配额不一致时拒绝。macOS 两独立入口子进程对两 Agent 的并发回归连续 5 轮通过，每轮 12 次请求只创建限额内的 3 个终端；进程崩溃释放锁、等待取消与待完成标志重读测试通过。
- Ubuntu 同机多入口原生复验：`4e3be0b` 测试二进制的双入口并发、共享待完成状态、配额冲突、锁等待取消与崩溃释放连续 5 轮通过。Windows 原生文件锁回归通过。Windows 重命名缓冲区保留 UTF-16 NUL 终止符后，100 文件精确名称回归及 Gateway 连续 50 轮回归通过。
- Windows 双入口：`ec78006` 的 push 与 PR 原生任务均通过 ConPTY 预算集成连续 3 轮，包括两独立入口进程、共享待完成状态和配额冲突、跨设备并发、孤儿、转移、离线及状态写入失败阻断；三平台全量 race、vet 与构建在 PR 工作流通过。
- Windows SCM：`07d5adf` 的 PR 原生任务通过实际 CLI 安装和启动构建出的二进制，验证 LocalSystem、Session 0、MCP 文件读写、正常停止、进程与监听退出、卸载及注册项删除；三平台全量 race、vet 与构建通过。验收配置由共享夹具经过生产解析器检查。用户任务 CI 按 Runner 会话条件启用，运行时增加非提升权限检查。
- 甲骨文 Ubuntu 的 bwrap 隔离环境：使用共享主机网络、独立 PID/文件系统与临时目录，连续两轮临时节点注册获得不同身份；每轮成功调用 macOS 和既有 Linux 节点，既有 Linux 节点反向调用成功。退出正常、节点离线、身份状态不落盘、临时工作目录清理均通过。此环境与既有 Linux 节点共用主机。

Linux 测试机为本任务安装了发行版 `bubblewrap` 与 `ripgrep`。该机 AppArmor 限制非特权 user namespace：普通用户 L2 验证使用只绑定测试目录中 bwrap 可执行文件、允许 `userns` 的临时 profile，测试结束已卸载。root 下的独立 L2 用例也通过。普通用户结果依赖该临时 profile，部署时须配置对应主机策略。

官方 Tailscale 上的 macOS 与 Ubuntu xmesh 节点使用独立 tsnet 状态，双向发现、节点身份、文件读写、exec、PTY owner 和资源清理均已通过。独占节点状态的网络探测确认使用区域 900 `oracle-jp` DERP，且远端 Agent 正常响应。已保存用户授权的 `tag:device → tag:device tcp:7433` 规则；私有 DERP 的系统节点通过定向 ICMP 规则获得 xmesh 节点可见性，客户端校验正常工作。

Headscale v0.29.5 的 macOS 与 Ubuntu 节点双向工具调用通过，独立探测确认 DERP `7/tok` 路由；Ubuntu 隔离沙箱连续两轮完成对两端的调用及反向调用，重启获得新身份。临时控制面、转发、节点、凭据与 AppArmor profile 已清理。环境、观测值和物理设备边界见[Headscale 验收记录](validation/2026-10-15-headscale.md)。

`874ea35` 的 push 与 PR 工作流均通过三平台全量 race、vet、构建及 Windows 独立原生任务。创建期 Job 绑定的挂起子进程在宿主被强制终止后退出，未绑定对照继续存活；AppContainer 准确 SID 的环回豁免策略、宿主可达对照和监听端读回组合通过。

`d4e96c4` 的 push 与 PR 工作流均通过三平台全量 race、vet、构建及 Windows 独立原生任务。普通命令原生契约验证显式相对路径与扩展名、Unicode 和引号参数、含 NUL 的 stdin、业务环境传入和父进程凭证过滤、stderr 与非零退出码；启用 macOS L2 的 exec、PTY 和 policy 本地 race 回归通过。

`4a00a00` 的 push 工作流通过三平台全量 race、vet、构建和 Windows 独立原生任务；PR 的 Windows 原生与 Windows/Ubuntu 全量检查通过。单对象 AppContainer 授权验证 Medium 文件读写与目录创建、完整性标签保持、已有子目录及目录外拒绝、另一 profile 的 ACE 保留、文件与目录改名和路径替换后的准确回收，以及硬链接、重解析点和 null DACL 拒绝。PR macOS 审计测试跨分钟轮转时只读取首个日志分片；断言改为读取全部分片、检查正文排除及两类事件各一条，本地连续 200 轮与全量 race、vet 通过。

`c7bf24e` 的 push 与 PR 工作流均通过三平台全量检查和 Windows 独立原生任务。工作树检查原语已实现句柄相对扫描、根与祖先绑定、128 位对象身份核对及失败释放。`8bb5f11` 的三平台全量检查通过；原生扫描、拒绝条件的正常目录对照和路径替换检测通过，包含保留子文件句柄的目录改名返回拒绝。原生用例继续验收文件编辑、空目录改名与替换后的句柄身份，以及关闭后恢复目录改名；完整执行期授权生命周期待实现。逐项证据见 [Windows 验收记录](validation/2026-10-15-windows.md)。

`81adb07` 的 push 与 PR 工作流全部通过三平台全量检查、契约夹具和 Windows 独立原生任务；工作树扫描的 703 对象、多批枚举、根与祖先绑定、文件编辑、空目录改名/路径替换身份及关闭后目录改名通过。已选对象授权实现非继承句柄复制与再次核对对象身份，Windows 双架构 vet 和 amd64 测试交叉编译通过；原路径替换后授权、快照独立关闭与准确回收的新原生用例待 CI。完整工作树授权与生产执行接入继续推进。

`beae5de` 的 push 与 PR 工作流全部通过，按已选句柄授权的 file/directory 原生用例通过。ACL 更新现按对象身份临时取得 `READ_CONTROL|WRITE_DAC`，直接设置所选对象的 DACL；保留根目录绑定的授权与回收、描述符控制与标签保持、已有子对象 ACL 保持的新原生用例待 CI。Windows 双架构 vet 和 amd64 测试交叉编译通过。

## 待实现与待验收

| 项目 | 当前边界 |
| --- | --- |
| 入口 PTY 总预算 | 单入口跨设备及同机共享预算文件的多入口准入已实现；macOS、Ubuntu、Windows 原生多入口与文件锁通过，跨主机入口协调待实现 |
| Windows L2 | 当前请求 L2 会拒绝执行；profile、启动属性与准确对象 ACL 授权/回收原语已实现，通过 Windows amd64/arm64 vet、amd64 交叉编译与 `4a00a00` 原生验收；完整工作目录绑定、exec/ConPTY 接入、profile/ACL 异常退出恢复待实现 |
| 原生 Windows | Server 2025 的 ConPTY、进程清理、文件、PowerShell 5.1/7、HTTP MCP 及本机模式 SCM 生命周期已验证；用户任务的登录触发、目标会话与生产服务设备网络待验收，其他 Windows 版本待验证 |
| 三设备互联 | 官方 Tailscale 与 Headscale 的双主机及隔离沙箱、DERP 路由已验证；三台物理设备与真实 LAN 多播待验收 |
| 云端连接器 | claude.ai 与 Codex 的 OAuth、实际工具可见性及调用待验收 |
| 公网入口 | 三种监听路径已装配；真实域名、Funnel/隧道及云客户端待验收 |
| 云沙箱入网 | 甲骨文 bwrap 环境已通过；Codex Cloud 与 Claude 云沙箱的实际出站策略仍待验收 |
| 本机客户端 | Claude Code 人工连接观察待执行 |

以上自动测试覆盖已实现行为；各门的外部验收仍按[符合性矩阵](conformance.md)逐项执行。
