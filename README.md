# xmesh

XmeSh（X mesh，也是 X me sh：我的 sh）让 Codex Cloud、Claude Cloud 等云端 Agent 通过标准 Remote MCP，调度真实开发设备（Windows / macOS / Linux）上的终端、文件和本机已有的 MCP 工具。

每台机器运行同一个 `xmesh` 进程，同时承担 Agent 与 Gateway 角色。设备只在 tailnet 内可达；需要对云端暴露时，由带 `--ingress` 的节点提供带 OAuth 2.1 的公网 MCP 端点。

## 文档

方案、约束和契约都在 [`docs/`](docs/README.md)。

| 想知道什么 | 从这里读 |
| --- | --- |
| 要解决什么问题、系统长什么样 | [概述](docs/design/01-overview.md) · [架构](docs/design/02-architecture.md) |
| 实现时哪些规则不能破 | [约束](docs/constraints/README.md) |
| 工具、错误、租约、OAuth 的精确行为 | [契约](docs/contracts/README.md) |
| 某一条规则落到哪一阶段的测试 | [符合性矩阵](docs/conformance.md) |
| 如何构建、配对、入网和运行 | [运行手册](docs/operations.md) · [实现记录](docs/implementation-status.md) |

阅读规范用语、编号和裁定方式见文档库首页。

## 本机开发

需要 Go 1.27。先在 `xmesh.local.yaml` 中设置允许访问的目录，然后运行：

```bash
go run ./cmd/xmesh serve --local --config xmesh.local.yaml
go run ./cmd/xmesh status --json
go test -race ./...
```

本机 MCP 地址为 `http://127.0.0.1:7433/mcp`，提供 `sys.info`、`fs.*`、`pty.*`、`exec.*`。客户端使用有会话的 Streamable HTTP MCP。`--local` 是显式开发模式；当前实现进度和外部验收状态见 [实现记录](docs/implementation-status.md)。
