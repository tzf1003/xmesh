# CI 运行策略

`.github/workflows/go.yml` 是日常验证入口，工作流名称为 **CI**。PR 每次更新执行一次，新的更新取消该 PR 的旧运行；`master` 推送验证合并结果，新提交替换旧提交的检查。发布标签各自完成完整验证。组件工作流由 CI 按改动路径调用。

| 改动或事件 | 验证范围 |
| --- | --- |
| 普通文档 | 契约、文档链接、符合性矩阵、生成类型和 CI 脚本 |
| Go 内核 | 三平台 race/vet/进程冒烟和原生用户服务；网络、Windows 原生、Unix L2 按受影响包追加 |
| UI renderer、样式 | 三平台单测与类型检查、Windows/macOS Electron 端到端 |
| UI MCP、主进程或桥接层 | 追加 UI MCP 浏览器与内核集成；主进程及桌面接入相关改动追加原生桌面验收 |
| 桌面执行器及其接入代码 | 三平台 Rust 与真实桌面输入、IPC、安装服务、升级和网络验收 |
| 组件工作流 | 对应组件验证 |
| CI 入口、路径选择、发布门禁或 Go 依赖 | 所有组件的日常验证 |
| 发布标签、手动 CI、每周定时 CI | 所有组件，并执行 1 GiB 文件传输、六目标交叉编译、Windows 准入成本测量和 50 次网关回归 |

路径选择由 `scripts/ci_changes.py` 使用完整 Git diff 计算，包含删除和重命名前后的路径；diff 基线缺失时运行完整检查。多个组件可能同时命中。Markdown 文档由契约与链接检查覆盖。网络、沙箱和桌面接入的依赖包清单在选择脚本中显式维护；新增内核包先运行全部日常组件。每周定时执行时间为周一 03:23（Asia/Shanghai）。

`CI result` 汇总所有选中的任务：选中任务必须成功，未选中任务应为 skipped。如果配置分支保护，可将 `CI result` 设为必需检查。契约任务始终执行，因此普通文档 PR 也会得到明确结果。

## 合并后的任务

- Onboarding 单测随三平台 Go 全量测试执行；原生用户服务验收复用同平台冒烟二进制，并保留服务清理和 Windows 交互会话检查。
- 路径选择、契约夹具与 TypeScript 生成检查共用一个 Linux 作业，组件检查在该作业成功后启动。
- Headscale 与 meshadmin 共用一次工具链安装和经过校验的 Headscale 下载。
- UI 单测与 Electron 端到端共用同平台的依赖安装和 UI 构建。
- `ui-mcp.yml`、`ui.yml`、`computer.yml`、`headscale.yml` 是可复用组件工作流。

## 诊断与打包

**Windows diagnostics** 支持手动选择 `joint`、`race`、`flakes` 或 `all`。`joint` 保留三轮 AppContainer 联合验收；`race` 和 `flakes` 保留高次数压测及命名管道复现。日常 Go 检查继续运行一轮 Windows L2 验收。

桌面安装包在打包配方、依赖、图标或验证脚本变化的 PR 上执行两种本机架构的构建与冒烟；完整四目标安装包和六目标桌面执行器包在发布或手动验证时构建。发布前由 `scripts/release_gate.py` 等待同一 SHA、同一发布标签的完整 CI 成功，再开始构建发布产物。

本次配置将 15 个工作流文件合并为 10 个。以下为配置推算的日常 CI 作业数，包含矩阵展开、合并了路径选择的契约任务和汇总任务；不包含独立的安装包或安装站点检查：

| 示例 | 作业数 |
| --- | ---: |
| 仅修改 `README.md` | 2 |
| 仅修改 `internal/ci/manager.go` | 5 |
| 仅修改 `internal/fs/download.go` | 7 |
| 仅修改 UI renderer 或 CSS | 5 |
| 全部组件 | 18 |

实际耗时还取决于 runner 排队、缓存和测试时长，应以合并后的 GitHub Actions 运行记录衡量。
