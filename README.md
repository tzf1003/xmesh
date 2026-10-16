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
go run ./cmd/xmesh status
go run ./cmd/xmesh status --endpoint http://127.0.0.1:8080/mcp --tool local/sys.info
go test -race ./...
```

`xmesh.local.yaml` 启用 Agent 与 Gateway 两个角色，各占一个本机地址：

| 角色 | 地址 | 工具 |
| --- | --- | --- |
| Agent | `http://127.0.0.1:7433/mcp` | `sys.info`、`fs.*`、`pty.*`、`exec.*` |
| Gateway | `http://127.0.0.1:8080/mcp` | 同一批工具，名字带 `local/` 前缀（`local/exec.run`），并聚合已入网的其他设备（`<设备名>/exec.run`）以及 `devices.list` 等 |

客户端使用有会话的 Streamable HTTP MCP。`--local` 是显式开发模式；当前实现进度和外部验收状态见 [实现记录](docs/implementation-status.md)。

## 命令

同一个 `xmesh` 二进制提供以下子命令，参数运行 `xmesh help` 查看：

| 命令 | 作用 |
| --- | --- |
| `xmesh serve --config xmesh.yaml` | 按配置启动已启用的角色（`agent`、`gateway`、`ingress`）。`--local` 为本机开发模式，`--ingress` 临时加上入口角色 |
| `xmesh install --config xmesh.yaml` | 安装为用户级常驻服务：macOS LaunchAgent、Linux systemd 用户服务、Windows 当前用户的登录计划任务。Windows 加 `--system` 改为注册管理员 SCM 服务 |
| `xmesh pair --config xmesh.yaml --export 文件`<br>`xmesh pair ... --import 文件 --fingerprint 指纹` | `lan` 模式下导出本节点证书、导入并核对对端证书 |
| `xmesh status` | 读取运行中节点的 `sys.info`（不读配置文件）。`--endpoint` 指向 Gateway 时要加 `--tool local/sys.info`；`--json` 输出原始 JSON |
| `xmesh version` | 打印版本号 |

构建、配对、入网和运行的完整步骤见 [运行手册](docs/operations.md)；测试版（pre-release）的发布方式见运行手册的“测试版发布”。
