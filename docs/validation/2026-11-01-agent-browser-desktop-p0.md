# agent-browser 与 agent-desktop P0 对照验证（2026-11-01）

对应方案：`plans/agent-browser-desktop-integration.html` 的 P0。
本文只记录实测到的事实；没测到的标“未验证”并写明原因，不用推测填空。

## 环境与版本

| 设备 | 系统 | agent-browser | agent-desktop | 结果 |
| --- | --- | --- | --- | --- |
| xiaosong-mac | macOS 27.0.1 arm64 | 0.41.0（`brew install agent-browser`，Chrome 156 通过 `agent-browser install` 下载） | 1.0.2（`npm i -g agent-desktop`） | 浏览器全部实测；桌面端被系统授权挡住 |
| home-windows | Windows 10.0.26100 | 0.41.0（`npm i -g`） | 1.0.2（`npm i -g`） | 辅助功能为 granted；仅部分实测 |
| ubuntu-cloud | Ubuntu 24.04 x64 | 未安装 | 未安装 | 无 node，无图形会话，无 at-spi 库，不适合桌面验证 |
| ubuntu-aws、xiaosong-home-ubuntu | Linux | 未测 | 未测 | 本轮未覆盖 |

安装注意：xmesh `exec.run` 的 `timeout` 参数是毫秒整数，字符串如 `"30s"` 会报 invalid_args。xmesh 客户端对单次调用约 60 秒超时，Windows 上 `npm i -g` 需要后台启动再轮询。

## 对照矩阵

| 项目 | agent-browser | agent-desktop | 对方案的影响 |
| --- | --- | --- | --- |
| 中文文本输入 | 通过（Mac）。`fill` 写入“你好，世界 xmesh”，`type` 追加“中文输入”，页面 `input` 事件读回为“你好，世界 xmesh中文输入”，无乱码 | 未验证。Mac 缺授权；Windows 未做输入测试 | 浏览器侧中文无阻碍；桌面侧 `type`/IME 仍是 P2 前的开放项 |
| 拖动 | 通过（Mac）。`drag <源> <目标>` 后落点页面显示 `dropped` | 未验证（同上） | 浏览器可直接用；桌面拖动仍待测 |
| 截图延时 | Mac 10 次整条 CLI 往返：148、173、174、164、131、146、176、154、120、128 ms，中位约 150 ms，最大 176 ms，在目标 p95 ≤ 300 ms 内。这是 CLI 起进程加截图的总耗时，复用常驻 daemon | 未验证。Mac 的 `screenshot` 返回 `PERM_DENIED`（Screen Recording 未授权）。Windows 的 `list-windows` 单次约 1.0 s（含 PowerShell 启动） | 桌面截图是否达标不能下结论 |
| 真实应用 ref 覆盖 | 本轮只测了自建页面，`snapshot` 给出 textbox 的 `@e1`，draggable 的 div 只作为无名 group 出现，没有 ref，所以拖动要用选择器而不是 ref | Mac：`list-apps` 可用，`snapshot` 返回 `PERM_DENIED`（Accessibility 未授权）。Windows：`list-windows` 可列出终端与记事本窗口，但对 Notepad 的 `snapshot` 在 3 s 默认期限内 `TIMEOUT`（实际耗时约 32 s） | 真实应用覆盖率没有数据；Windows 记事本超时需要带参数复测，可能说明树遍历在该窗口上很慢 |
| daemon 清理 | 有风险。运行后 daemon 进程（`agent-browser-darwin-arm64`）的 PPID 为 1，自己是进程组组长（PGID=自身 PID）；Chrome 主进程的 PGID 也不同于其他子进程。部分 Chrome 辅助进程 PPID 为 1。说明 daemon 脱离了启动者的进程组 | 未验证 | 方案里 `process_cleanup: tree` 若按进程组或父子关系回收，**很可能回收不到** daemon 与 Chrome。需要后端 supervisor、每调用方独立 session 加显式 `agent-browser close`，或设短空闲超时。此点需要在 xmesh 后端代码上用真实 per-caller 流程再证一次 |
| 后端 scope 过滤 | 读代码：`internal/oauth/scopes.go` 的 `Allows` 对普通后端只要求 `backend:<设备>:<后端名>`，**没有按工具名过滤** | 同左（若作为后端） | 方案第 6 节的“浏览器 scope”不是纯配置。想限制到只读工具子集，只能靠 `agent-browser mcp --tools` 的 profile（core、tabs、state 等），或新增按工具名的 scope 代码 |

## 其他实测发现

- `agent-browser mcp` 存在，默认 `--tools core`，并支持 `mcp call <tool> <json>` 单次调用，stdio 后端形态成立。
- Homebrew 已有 `agent-browser` 公式（0.41.0），方案中“Homebrew 仍在提交”已过时；它会连带安装 node。
- agent-desktop 的权限身份取决于“启动它的进程”。通过 xmesh 的 exec 启动时，macOS 授权对象是 xmesh agent 进程，不是我们现有的 `app.xmesh.computer`，印证方案 5.4 里“用户需重新授权一次”。macOS 27 的入口是“设备控制与数据访问”。
- 在 Mac 上，授权需要本机有人点系统设置，远程无法完成。

## 未完成项与建议

1. Mac 上给 xmesh agent（或最终的 Host）授予 Accessibility 与 Screen Recording 后，补测 agent-desktop 的中文输入、拖动、截图延时、真实应用 ref 覆盖。
2. Windows 补测：对 Notepad 提高 snapshot 超时重试；再测一个原生应用与一个 Electron 应用。
3. Linux 需要带图形会话与 at-spi2-core 的设备才能验证。
4. 在 xmesh 里按 per-caller 流程起 `agent-browser mcp`，实证 `process_cleanup: tree` 后 daemon 与 Chrome 是否残留，再决定 supervisor 方案。

## 保留决定

macOS 后台坐标输入模块保留为回退（已由使用者确认）。本轮结果没有推翻这个决定：agent-desktop 的坐标路径本轮仍无法验证。
