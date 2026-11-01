# agent-browser 浏览器后端验证记录

日期：2026-11-01。交付入口：`packages/agent-browser/README.md`。对应方案 P1。

环境：Linux x86_64 容器（Node v22.23.4，Go 1.24.7，agent-browser 0.41.0，Chrome for Testing 156.0.8078.80，无头）。macOS 与 Windows 未实测；安装脚本与包装器不含平台特定调用，但 Windows 的 Job 回收、`node.exe` 直连方式需要在真机复验。

## 上游实测结论（决定了包装器的设计）

| 检查 | 结果 |
| --- | --- |
| `agent-browser mcp --tools core,tabs` | 35 个工具，名为 `agent_browser_*`；`core` 含 `agent_browser_eval` |
| MCP 进程退出后 | daemon 为独立进程组，Chrome 仍存活。`process_cleanup: tree` 在 Unix 上不会回收，需包装器执行 `close --all` |
| `AGENT_BROWSER_CONFIRM_ACTIONS=eval,download` | 对 MCP 调用的 `agent_browser_eval` **不生效**（直接返回 `2`） |
| 动作策略文件 `{"default":"allow","deny":["evaluate",...]}` | 动作名是 `evaluate`（不是 `eval`）；写 `eval` 或 `script` 不生效。默认拒绝加 allow 列表时 `launch`、`navigate` 等都要列出，易随版本失效，所以用 deny 列表 |
| `AGENT_BROWSER_ALLOWED_DOMAINS` | 越界域名被拒；`localhost` 与 `127.0.0.1` 视为不同域名 |
| 截图 | 响应含文本路径和 base64 PNG 图片块；文件落在 `~/.agent-browser/tmp/screenshots` |
| 环境变量名 | `XMESH_*` 被节点策略拒绝，包装器开关改用 `BROWSER_BACKEND_*` |
| 作用域 | 后端工具需要 `backend:<设备>:browser`；用 `backend:local:browser` 时本机节点不放行，验收使用 `backend:*:browser` |

## 本次验证

| 检查 | 结果 |
| --- | --- |
| `scripts/accept_browser.py`（本机 xmesh 节点、两个 PAT） | 通过：工具出现、两个调用方页面互不可见、填写点击读取、PNG 图片块、`localhost` 被拒、`evaluate` 被拒、输入值和页面正文不在审计里、节点停止后两个命名空间目录和 daemon 被回收 |
| 回收检查的负对照 | 去掉包装器的清理后，同一脚本因命名空间目录残留而失败，证明该检查有效 |
| `scripts/test_agent_browser.py`（桩二进制） | 7 个测试通过：必填白名单、`*` 需显式开启、默认值与策略、节点所有者设置优先、工具分级校验、拒绝连接用户浏览器、每次启动独立命名空间与清理 |
| `scripts/test_scripts.py`、`conformance_check.py`、`test_ci_changes.py` | 通过 |
| `install.mjs --prefix` | 干净前缀安装通过，已有 Chrome 时不重复下载，打印配置 |

## 未覆盖

- Windows、macOS 真机：Job 回收、`node.exe` 直连、浏览器启动。
- 离线设备使用 `AGENT_BROWSER_EXECUTABLE_PATH`。
- `state` 工具组的加密登录态；本期默认不启用。
- 经 tailnet/DERP 的跨设备调用；验收只走本机 Gateway。
