# xmesh

XmeSh（X mesh，也是 X me sh：我的 sh）让 Codex Cloud、Claude Cloud 等云端 Agent 通过标准 Remote MCP，调度真实开发设备（Windows / macOS / Linux）上的终端、文件和本机已有的 MCP 工具。

每台机器运行同一个 `xmesh` 进程，同时承担 Agent 与 Gateway 角色。设备通过统一公钥配对，可在局域网与 Tailnet 上独立连接；需要对云端暴露时，由带 `--ingress` 的节点提供带 OAuth 2.1 的公网 MCP 端点。

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

## 安装与连接

Windows 和 macOS 用户从[安装首页](https://tzf1003.github.io/xmesh/)选择系统、芯片和版本，下载当前用户安装包，打开 xmesh 设置向导。首页提供版本固定的 Agent 提示词、三步连接说明与排查入口。也可直接查看[发布列表](https://github.com/tzf1003/xmesh/releases)与 [Agent 安装文档](docs/agent/README.md)。

向导会收集用途（本机、多设备、云端）、工作目录、开发或只读权限以及 Codex / Claude Code 客户端。确认计划后执行，遇到中断可按 job ID 恢复；从客户端发起真实请求后完成连接验证。

每个新版本提供 `manifest.json`（平台、架构、SHA-256、大小、签名状态）、`install.sh`、`install.ps1` 和平台离线 ZIP。下载并检查脚本后运行：

```bash
bash install.sh --channel stable --json
# 固定版本：bash install.sh --version v1.2.3 --json
```

```powershell
.\install.ps1 -Channel stable -Json
# 固定版本：.\install.ps1 -Version v1.2.3 -Json
```

稳定版要求 Windows Authenticode 和 macOS 签名、公证、装订验证。beta/rc 属于 testing；安装明确未签名的测试包须加 `--allow-unsigned` / `-AllowUnsigned`，系统信任检查仍生效。安装器支持重试、`--download-only` / `-DownloadOnly`、离线目录和 JSON 输出，并返回应用内长期可用的 CLI 路径。完整参数、升级与恢复见[安装说明](docs/agent/install.md)。版本示例需替换为实际已发布版本。

Agent 可沿 [llms.txt](llms.txt) 阅读版本对应的文档，使用 `setup inspect`、`plan`、`apply`、`status`、`resume`、`verify` 与 `doctor`。

Linux 使用 `install-linux.sh`：校验发布文件后，把内核和网页管理界面各装成一个 systemd 用户服务，并在终端里协助你设置工作目录、权限和网页登录的管理密钥。网页管理界面在浏览器中提供与桌面端相同的面板，需要 Node.js 22.18 或更新版本：

```bash
bash install-linux.sh --channel stable
# 固定版本：bash install-linux.sh --version v1.2.3
# 只装内核：加 --no-webui；离线：--offline DIR
```

细节见[安装说明](docs/agent/install.md#linux)和 [Linux 网页管理界面](ui/README.md#linux-网页管理界面)。

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

## 文件互传

通过 MCP 调用 `<设备名>/fs.download` 下载原始字节，或用 `<设备名>/fs.upload` 上传到指定路径。本机 Gateway 的前缀为 `local/`。文件分块传输并验证 SHA-256，上传完成前保留原目标；默认拒绝同名冲突，`force: true` 才允许原子覆盖。

单文件默认上限 **1 GiB**，可在「设置 → 文件互传」修改大小或选择「无限制」，保存后立即生效。配置项为 `agent.fs.max_file_size`，例如 `2GiB` 或 `unlimited`。分块上限固定 1 MiB，支持大文件且内存有界。路径受 allow/deny 策略约束，父目录须已存在。参数见[文件互传契约](docs/contracts/fs.md#ctr-fs-08)，客户端示例见 [`scripts/file_transfer.py`](scripts/file_transfer.py)。

## 命令

同一个 `xmesh` 二进制提供以下子命令，参数运行 `xmesh help` 查看：

| 命令 | 作用 |
| --- | --- |
| `xmesh serve --config xmesh.yaml` | 按配置启动已启用的角色（`agent`、`gateway`、`ingress`）。`--local` 为本机开发模式，`--ingress` 临时加上入口角色 |
| `xmesh install --config xmesh.yaml` | 安装为用户级常驻服务：macOS LaunchAgent、Linux systemd 用户服务、Windows 当前用户的登录计划任务。Windows 加 `--system` 改为注册管理员 SCM 服务 |
| `xmesh pair --config xmesh.yaml --export 文件`<br>`xmesh pair ... --import 文件 --fingerprint 指纹` | 导出设备公钥、导入并独立核对对端公钥指纹，即时建立设备信任 |
| `xmesh status` | 读取运行中节点的 `sys.info`（不读配置文件）。`--endpoint` 指向 Gateway 时要加 `--tool local/sys.info`；`--json` 输出原始 JSON |
| `xmesh version` | 打印版本号 |

Windows 发行包同时提供命令行 `xmesh.exe` 和无控制台的 `xmesh-background.exe`。内核及桌面宿主的登录任务使用后者，桌面原生执行器通过无窗口进程启动，不显示前台黑框。旧任务在下一次 `start` / `restart` 时自动切换；两份内核必须放在同一目录。

新版配置使用 `schema_version: 2`，通过 `mesh.lan.enabled` 与 `mesh.tailnet.enabled` 开启共存。首次连接和断线恢复优先 LAN，健康连接保持当前路径。一台设备在界面中显示一次。旧节点停止后运行 `xmesh migrate --config xmesh.yaml` 备份并转换配置，再按新版重新配对。

构建、配对、入网和运行的完整步骤见 [运行手册](docs/operations.md)；测试版（pre-release）的发布方式见运行手册的“测试版发布”。
