# 实现记录

本文件记录代码与验证状态；规范行为仍以契约和[符合性矩阵](conformance.md)为准。

## 2026-10-15 实现范围

| 范围 | 已实现行为 |
| --- | --- |
| 文件与路径 | 允许目录、别名、软链检查、UTF-8/BOM、原子写入、哈希冲突、精确编辑、删除预检、glob/grep 双引擎及嵌套忽略规则 |
| 命令 | 公平队列、shared/exclusive、幂等隔离、超时清理、输出分页、L1 策略、Linux bwrap L2 |
| PTY | 四状态、游标读取、配额、owner、断线孤儿、OAuth client 接管约束、转移串行化、Unix 会话进程组清理 |
| 后端 | stdio/HTTP MCP、actor 串行、exclusive fencing lease、空闲回收、故障失效、策略重载保留有效租约 |
| MCP 多路复用 | 上下游请求映射、通知、进度、sampling 路由、断线取消 |
| 设备网络 | tsnet 注册与 WhoIs、tag 授权、LAN 证书配对与续期、mTLS、mDNS 发现 |
| Gateway | 设备前缀、能力缓存、搜索、连接默认值、断线停路由、持续 drain 状态 |
| OAuth 与入口 | 元数据、动态注册、PKCE S256、单主体口令、CSRF、scope、令牌轮转、客户端注册及回调 URI 持久化、Funnel/隧道/HTTPS 入口装配 |
| 治理与运行 | 元数据审计、热加载失败记录、重启提示、调用预算、Agent 客户端 PTY 预算、keep/kill drain、CLI 与服务失败退出 |

25 个 Agent 内置工具已接入；Gateway 另提供设备列表、搜索和会话默认值。操作方法见[运行手册](operations.md)。

## 已执行验证

- macOS：`go test -race ./... -timeout=60s`，20 个包共 79 项通过；`go vet ./...` 通过。HTTP 集成覆盖文件、exec、PTY、后端及 OAuth→Gateway→Agent 所有权隔离。
- Windows amd64 与 Linux amd64：`go build ./...` 交叉编译通过。
- 配置、错误、租约、审计的正反例及文档链接：`check_examples.py` 通过。
- Ubuntu 24.04 amd64 原生：PTY 6 项、exec 6 项、文件搜索 5 项及 policy 3 项通过。使用实际二进制的 L1、L2 MCP 冒烟均通过文件、exec、PTY、审计正文排除及正常退出检查。
- macOS 实际二进制：L1 MCP 冒烟通过，同一脚本可用 `scripts/smoke_local.py` 复跑。

Linux 测试机为本任务安装了发行版 `bubblewrap` 与 `ripgrep`。该机 AppArmor 限制非特权 user namespace：普通用户 L2 验证使用只绑定测试目录中 bwrap 可执行文件、允许 `userns` 的临时 profile，测试结束已卸载。root 下的独立 L2 用例也通过。普通用户结果依赖该临时 profile，部署时须配置对应主机策略。

官方 Tailscale 上的 macOS 与 Linux xmesh 节点均已注册，使用独立 tsnet 状态。当前策略未授予 `tag:device` 相互访问 Agent 端口，双向跨主机工具调用待授权后验证。

## 待实现与待验收

| 项目 | 当前边界 |
| --- | --- |
| PAT | 契约已定义；本机 Gateway 的 PAT 配置、存储及 scope 认证待实现 |
| 入口 PTY 总预算 | Agent 内按 OAuth client/节点跨会话计数已实现；跨设备入口聚合待实现 |
| macOS/Windows L2 | 当前请求 L2 会拒绝执行；平台沙箱待实现 |
| Windows 用户服务 | 当前使用 SCM 服务，需要管理员权限；用户级启动待实现 |
| 原生 Windows | ConPTY、进程清理、服务安装待实机验证 |
| 三设备互联 | 官方 Tailscale、Headscale、DERP 连接状态、真实 LAN 多播待验收 |
| 云端连接器 | claude.ai 与 Codex 的 OAuth、实际工具可见性及调用待验收 |
| 公网入口 | 三种监听路径已装配；真实域名、Funnel/隧道及云客户端待验收 |
| 云沙箱入网 | ephemeral key 入网及调用待实验 |
| 本机客户端 | Claude Code 人工连接观察待执行 |

以上自动测试覆盖已实现行为；各门的外部验收仍按[符合性矩阵](conformance.md)逐项执行。
