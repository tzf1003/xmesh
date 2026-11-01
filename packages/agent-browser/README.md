# agent-browser 浏览器后端

把 [agent-browser](https://github.com/vercel-labs/agent-browser)（Rust CLI，经 CDP 控制 Chrome）作为 xmesh 的 stdio 后端接入。云端 Agent 经 Gateway 调用 `<设备>/browser.agent_browser_*`，不需要改协议。设计背景见项目方案「接入 agent-browser 与 agent-desktop」（P1）。

固定版本：agent-browser **0.41.0**（`package.json` 与 `package-lock.json` 锁定，Apache-2.0）。升级时同时更新验收记录。

## 为什么要一个启动包装器

直接把 `agent-browser mcp` 写进 `command` 能用，但有三处不安全或不干净，包装器 `bin/xmesh-agent-browser.mjs` 负责补上：

| 问题 | 实测（0.41.0） | 包装器的处理 |
| --- | --- | --- |
| MCP 进程退出后，daemon 与 Chrome 仍在运行，默认空闲 1 小时才退出，且不在 MCP 的进程组内，`process_cleanup: tree` 在 Unix 上收不到 | 退出后 daemon 进程组独立，Chrome 继续存活 | 每次启动用独立 `AGENT_BROWSER_NAMESPACE`，退出时执行 `agent-browser close --all` 并删除命名空间目录；daemon 空闲超时缺省 5 分钟 |
| `eval` 默认可用，`AGENT_BROWSER_CONFIRM_ACTIONS=eval` 对 MCP 调用不生效 | 设置后 `agent_browser_eval` 仍直接执行 | 生成动作策略文件，`deny` 掉 `evaluate`、`download`、`upload` |
| 不设域名白名单就能浏览任意站点；可连接日常浏览器 | 白名单对越界域名、子资源有效 | 未设置 `AGENT_BROWSER_ALLOWED_DOMAINS` 时拒绝启动；`CDP`、`AUTO_CONNECT`、`PROFILE`、`STATE` 默认拒绝 |

### 强杀后的残留

包装器被 `kill -9`、节点崩溃或断电时来不及清理，daemon 与 Chrome 会留到空闲超时（缺省 5 分钟）。每个包装器启动时和之后每分钟，会扫描 `~/.agent-browser/namespaces/` 里 `xmesh-<pid>-<随机>` 形式、且 pid 对应的包装器已不存在的命名空间，对其执行 `close --all` 并删除目录，同时清理对应的临时策略目录。pid 仍存活（含被复用）的命名空间不会被碰。没有任何新包装器启动时，仍由空闲超时兜底；对强杀后需要立即回收的场景，把 `AGENT_BROWSER_IDLE_TIMEOUT_MS` 调小。

## 安装

需要 Node.js 20 以上（包装器与安装脚本）。agent-browser 的包声明要求 Node 24，但 0.41.0 实际携带各平台原生二进制，Node 22 下安装与运行均已验证；npm 会打印 `EBADENGINE` 警告，可忽略。

```sh
node packages/agent-browser/install.mjs --prefix /opt/xmesh-browser
```

脚本会：把包装器、`package.json`、锁文件复制到 `--prefix`；`npm ci --ignore-scripts`（不执行上游安装脚本，只设置原生二进制可执行位）；运行 `agent-browser doctor`，**只在找不到 Chrome 时**执行 `agent-browser install` 下载 Chrome for Testing；最后打印可直接粘贴的 xmesh 配置。`--skip-chrome` 不下载，`--check` 只检查已有安装。离线设备用已装的浏览器：在后端环境里设 `AGENT_BROWSER_EXECUTABLE_PATH`。Chrome 不打进 xmesh 安装包。

Windows 用用户目录下的前缀（如 `%LOCALAPPDATA%\xmesh-browser`），`command` 写 `node.exe` 加包装器的绝对路径，不经 `.cmd`。

## 配置

模板见 [xmesh.backend.example.yaml](xmesh.backend.example.yaml)：

```yaml
agent:
  backends:
    browser:
      transport: stdio
      command: ["/usr/bin/node", "/opt/xmesh-browser/bin/xmesh-agent-browser.mjs", "--tools", "core,tabs"]
      cwd: /opt/xmesh-browser/work
      policy: per-caller
      idle_timeout: 10m
      shutdown_timeout: 15s
      process_cleanup: tree
```

- `per-caller`：每个调用方一个进程，会话、cookie、标签页互不可见。不用 `exclusive`，避免浏览器这种“每个任务一个窗口”的用法被租约流程拖重。
- 调用方需要 `backend:<设备>:browser` scope（PAT 或 OAuth），并在 `agent.authz` 里被允许使用 `browser` 后端。
- 工具名形如 `<设备>/browser.agent_browser_open`。上游的 `agent_browser_` 前缀无法去掉，名字偏长但不冲突；工具按首个 `.` 前的 `browser` 归入“browser 组”，与渐进式披露方案的分组一致，不需要额外适配。
- 结果原样透传（CON-TOOL-10）：`agent_browser_screenshot` 返回的图片块直接到达调用方。

### 环境变量

启动包装器自己的开关用 `BROWSER_BACKEND_` 前缀：节点策略把 `XMESH_*` 列为被拒绝的环境变量名（CON-SEC-04），后端环境也不能用。

放进后端环境（控制通道 `PATCH /v2/backends/browser/env`，见 CTR-CTL-14；保存在状态目录的 `backend-env.json`，0600，不进 `xmesh.yaml`、审计和日志；读接口只给变量名）。

| 变量 | 缺省 | 说明 |
| --- | --- | --- |
| `AGENT_BROWSER_ALLOWED_DOMAINS` | **必填** | 逗号分隔，如 `example.com,*.example.com`。同时限制子资源、WebSocket、`sendBeacon`，并关闭 WebRTC。`*` 需另设 `BROWSER_BACKEND_UNRESTRICTED_DOMAINS=1` |
| `AGENT_BROWSER_CONTENT_BOUNDARIES` | `1` | 页面输出包进带随机 nonce 的边界标记，便于区分不可信页面文本 |
| `AGENT_BROWSER_MAX_OUTPUT` | `50000` | 单次页面输出字符上限 |
| `AGENT_BROWSER_IDLE_TIMEOUT_MS` | `300000` | daemon 空闲退出 |
| `AGENT_BROWSER_ENCRYPTION_KEY` | 无 | 使用 `state` 工具组保存登录态时必须设置（64 位十六进制），状态文件加密存放 |
| `AGENT_BROWSER_EXECUTABLE_PATH` | 无 | 使用已安装的 Chrome |
| `BROWSER_BACKEND_TOOLS` | `core,tabs` | 工具分级，`--tools` 参数优先 |
| `BROWSER_BACKEND_EXTRA_DENY` | 无 | 追加到动作策略 `deny` 的动作名，如 `cookies,network` |
| `BROWSER_BACKEND_ALLOW_ALL_TOOLS` | 无 | 设 `1` 才允许 `--tools all` |
| `BROWSER_BACKEND_ALLOW_USER_BROWSER` | 无 | 设 `1` 才允许 `CDP` / `AUTO_CONNECT` / `PROFILE` / `STATE`，等于把该浏览器的登录态交给云端 Agent |
| `BROWSER_BACKEND_BIN` | 自动 | 指定 agent-browser 二进制；缺省找前缀内的 `node_modules`，再找 `PATH` |

### 工具分级

默认 `core,tabs`：打开、快照、点击、填写、截图、标签页。`state`（登录态）、`network`、`debug`、`react`、`mobile` 需要显式写进 `--tools`；`debug` 含 `confirm`/`deny`，会让调用方自己批准待确认动作，不要和不受信任的调用方一起开。`all` 默认不可用。

## 安全默认值与已知边界

- 域名白名单是浏览器层的限制，不是系统防火墙；需要硬隔离时叠加主机或容器的出网控制。
- 动作策略默认 `deny`：`evaluate`（任意页面脚本）、`download`（落盘绕过 `cwd_policy`）、`upload`（读取本机文件）。文件取回仍走 `fs.download`。
- 页面文本、无障碍树、截图都是不可信内容，可能含提示注入；内容边界只是标记，不是过滤。
- 截图文件由上游写到 `~/.agent-browser/tmp/screenshots`（同一用户下的所有实例共用），响应里同时带图片块；该目录不受 `cwd_policy` 约束。用专用 OS 账户运行节点。
- 审计只记工具名、调用方和字节数，不含参数值与页面正文（INV-10）。
- 本期不做有头模式与实时预览（agent-browser 的 dashboard/stream 默认只绑回环地址）；需要图形会话的场景留给后续。

## 验收

```sh
python3 scripts/accept_browser.py --binary ./xmesh --prefix /opt/xmesh-browser
```

脚本启动一个本机节点，经 Gateway 用两个 PAT（两个调用方）验证：工具出现；两个调用方页面互不可见；填写、点击、读取；截图为 PNG 图片块；`localhost`（不在白名单）被拒；`evaluate` 被策略拒绝；输入的值和页面正文不进审计；节点停止后命名空间目录和 daemon 被回收。要求本机有 Node.js 与 Chrome。启动包装器自身的行为由 `scripts/test_agent_browser.py`（桩二进制，CI 运行）覆盖。记录见 [docs/validation/2026-11-01-agent-browser.md](../../docs/validation/2026-11-01-agent-browser.md)。
