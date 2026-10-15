# 实现记录

本文件记录代码与验证状态；规范行为仍以契约和[符合性矩阵](conformance.md)为准。

## 2026-10-15 实现范围

| 范围 | 已实现行为 |
| --- | --- |
| 文件与路径 | 允许目录、别名、软链检查、UTF-8/BOM、原子写入、哈希冲突、精确编辑、删除预检、glob/grep 双引擎及嵌套忽略规则 |
| 命令 | 公平队列、shared/exclusive、幂等隔离、超时清理、输出分页、L1 策略、Linux bwrap 与 macOS Seatbelt L2 |
| PTY | 四状态、游标读取、配额、owner、断线孤儿、OAuth client 接管约束、转移串行化、Unix 会话进程组清理 |
| 后端 | stdio/HTTP MCP、actor 串行、exclusive fencing lease、空闲回收、故障失效、策略重载保留有效租约 |
| MCP 多路复用 | 上下游请求映射、通知、进度、sampling 路由、断线取消 |
| 设备网络 | tsnet 注册与 WhoIs、tag 授权、临时节点内存身份与退出注销、LAN 证书配对与续期、mTLS、mDNS 发现 |
| Gateway | 设备前缀、每设备独立能力刷新与重试退避、搜索、连接默认值、断线停路由、持续 drain 状态、本机 PAT 文件认证与 scope、凭据轮转和会话绑定 |
| OAuth 与入口 | 元数据、动态注册、PKCE S256、单主体口令、CSRF、scope、令牌轮转、客户端注册及回调 URI 持久化、Funnel/隧道/HTTPS 入口装配 |
| 治理与运行 | 元数据审计、热加载失败记录、重启提示、调用预算、Agent 客户端 PTY 预算、keep/kill drain、CLI 与服务失败退出、Windows 用户登录任务与可选 SCM 安装 |

25 个 Agent 内置工具已接入；Gateway 另提供设备列表、搜索和会话默认值。操作方法见[运行手册](operations.md)。

## 已执行验证

- macOS：`XMESH_TEST_L2=1 go test -race ./... -timeout=60s` 全量通过；`go vet ./...` 通过。HTTP 集成覆盖文件、exec、PTY、后端及 OAuth→Gateway→Agent 所有权隔离。
- Windows amd64 与 Linux amd64：`go build ./...` 交叉编译通过。
- 配置、错误、租约、审计的正反例及文档链接：`check_examples.py` 通过。
- Ubuntu 24.04 amd64 原生：PTY 6 项、exec 6 项、文件搜索 5 项及 policy 3 项通过。使用实际二进制的 L1、L2 MCP 冒烟均通过文件、exec、PTY、审计正文排除及正常退出检查。
- macOS 与 Ubuntu 实际二进制：带 PAT 的 L1 MCP 冒烟通过；覆盖 CLI 凭据读取、文件、exec、PTY、审计及退出。Gateway 认证测试覆盖 scope 过滤、写入拒绝、凭据轮转和跨凭据会话隔离。脚本为 `scripts/smoke_local.py --pat`。
- macOS 27.0.1 L2：原生测试验证目录外读取和写入拒绝、符号链接越界拒绝、网络拒绝、父进程信号与环境读取拒绝，以及含引号目录的参数传递。带 PAT 的真实 MCP L2 冒烟通过 exec 与 zsh PTY、审计及退出清理。
- Ubuntu 24.04 L2 PTY：在 PID 命名空间内通过 util-linux `setsid --ctty` 建立控制终端；原生测试连续三次通过窗口调整、API SIGINT、键盘 Ctrl-C、shell 持续交互与关闭后的后台进程清理。Linux L2 PTY 需要系统提供 `setsid`。
- Windows 用户任务：XML 注册参数、用户隔离命名、普通权限与登录会话配置、持续运行和失败重试、注册失败传播及临时文件清理的跨平台测试通过；原生运行验收用例已加入并完成交叉编译。
- 甲骨文 Ubuntu 的 bwrap 隔离环境：使用共享主机网络、独立 PID/文件系统与临时目录，连续两轮临时节点注册获得不同身份；每轮成功调用 macOS 和既有 Linux 节点，既有 Linux 节点反向调用成功。退出正常、节点离线、身份状态不落盘、临时工作目录清理均通过。此环境与既有 Linux 节点共用主机。

Linux 测试机为本任务安装了发行版 `bubblewrap` 与 `ripgrep`。该机 AppArmor 限制非特权 user namespace：普通用户 L2 验证使用只绑定测试目录中 bwrap 可执行文件、允许 `userns` 的临时 profile，测试结束已卸载。root 下的独立 L2 用例也通过。普通用户结果依赖该临时 profile，部署时须配置对应主机策略。

官方 Tailscale 上的 macOS 与 Ubuntu xmesh 节点使用独立 tsnet 状态，双向发现、节点身份、文件读写、exec、PTY owner 和资源清理均已通过。独占节点状态的网络探测确认使用区域 900 `oracle-jp` DERP，且远端 Agent 正常响应。已保存用户授权的 `tag:device → tag:device tcp:7433` 规则；私有 DERP 的系统节点通过定向 ICMP 规则获得 xmesh 节点可见性，客户端校验正常工作。

## 待实现与待验收

| 项目 | 当前边界 |
| --- | --- |
| 入口 PTY 总预算 | Agent 内按 OAuth client/节点跨会话计数已实现；跨设备入口聚合待实现 |
| Windows L2 | 当前请求 L2 会拒绝执行；受限 token 沙箱待实现 |
| 原生 Windows | 用户任务登录触发与原生运行、ConPTY、进程清理、SCM 服务安装待实机验证 |
| 三设备互联 | 官方 Tailscale、Headscale、DERP 连接状态、真实 LAN 多播待验收 |
| 云端连接器 | claude.ai 与 Codex 的 OAuth、实际工具可见性及调用待验收 |
| 公网入口 | 三种监听路径已装配；真实域名、Funnel/隧道及云客户端待验收 |
| 云沙箱入网 | 甲骨文 bwrap 环境已通过；Codex Cloud 与 Claude 云沙箱的实际出站策略仍待验收 |
| 本机客户端 | Claude Code 人工连接观察待执行 |

以上自动测试覆盖已实现行为；各门的外部验收仍按[符合性矩阵](conformance.md)逐项执行。
