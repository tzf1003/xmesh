# 1. 概述与目标

XmeSh（X mesh，也是 X me sh：我的 sh）让 Codex Cloud、Claude Cloud 等云端 Agent 通过标准 Remote MCP，直接调度真实开发设备（Windows / macOS / Linux），获得终端、文件，以及设备上已有 MCP 工具（如 IDA MCP）的能力。

## 要解决的问题

- 云端 Agent 只有沙箱，碰不到真机上的 IDA、Xcode、MSVC、GPU 和本地环境。
- 设备通常在 NAT 后、没有公网 IP，不能直接被云端访问。
- 多个调用方（本地 Gateway、云端 Gateway、多个会话）同时操作同一台设备时，需要明确的资源归属和并发规则。

## 设计原则

1. **标准优先。** 对云端只暴露 Streamable HTTP + OAuth 2.1 的 MCP，不发明新协议。约束：[CON-ARCH-01](../constraints/architecture.md#con-arch-01)。
2. **设备只在 tailnet 内可达。** Agent 不监听公网，互联由 tsnet 承担。约束：[CON-NET-01](../constraints/network.md#con-net-01)、[CON-NET-02](../constraints/network.md#con-net-02)。
3. **Agent 是设备级 MCP Hub。** 内置 `sys` / `exec` / `pty` / `fs`，并挂载本机 MCP 后端。不管理工作区，`cwd` 只是参数。约束：[CON-ARCH-06](../constraints/architecture.md#con-arch-06)。
4. **Agent 是自身状态的唯一权威，Gateway 无状态。** 所有冲突以结构化错误返回，让模型自行决策。约束：[CON-ARCH-04](../constraints/architecture.md#con-arch-04)、[CON-ARCH-05](../constraints/architecture.md#con-arch-05)。
5. **单一代码库、单一二进制（Go）。** 每台机器都是对等节点，同时承担 Agent 与 Gateway 角色。任意节点可以开启入口角色给云端用。三平台交叉编译。约束：[CON-ARCH-02](../constraints/architecture.md#con-arch-02)、[CON-ARCH-03](../constraints/architecture.md#con-arch-03)。

这些原则在 [不变量](../constraints/invariants.md) 里变成运行时性质，在 [契约](../contracts/README.md) 里变成可调用的接口。
