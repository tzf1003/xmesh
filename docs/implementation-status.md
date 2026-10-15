# 实现记录

本文件记录代码与验证状态；规范行为仍以契约和[符合性矩阵](conformance.md)为准。

## 阶段 1

已实现一个 Go module 内的路径解析、JSON Schema 配置校验、文件工具、PTY、命令执行和本机 Streamable HTTP MCP Agent。`serve --local` 显式绑定 `127.0.0.1`；`status` 从运行中的 MCP 读取设备信息，支持中文标签及 JSON。

自动验证覆盖路径策略与符号链接、BOM 与二进制拒绝、原子写入、哈希冲突、精确编辑、递归删除预检、两种搜索引擎结果、20 路 PTY 配额、所有权与 OAuth client 接管约束、输出游标与退出排空、公平执行队列、exclusive 屏障、幂等隔离、输出分页、超时子进程清理和下一次调用的策略热加载。

本机 HTTP MCP 集成测试使用官方 Go SDK 客户端，执行文件读写、命令执行、PTY 读写及两会话隔离。macOS 本机运行通过；Windows 与 Linux 已交叉编译。三平台实际运行以 CI 和平台验收结果为准。Claude Code 人工连接验收待执行。

## 后续阶段

按文档顺序推进后端与租约、多路复用、tsnet / LAN 配对、Gateway / OAuth 和治理。官方 Tailscale、Headscale 三设备及 DERP 验收、云端连接器工具可见性与执行、L2 沙箱和云沙箱加入 tailnet 需要对应环境的实测证据。
