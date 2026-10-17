# xmesh-ui

xmesh 的桌面托盘壳（Electron，路线图阶段 6 的 U3）。独立的 TypeScript 项目，不 import 任何 Go 代码，和内核之间只有一份契约：本机控制通道（Control API v1，Unix 域套接字或 Windows 命名管道）。设计见 [docs/design/12-ui-and-control.md](../docs/design/12-ui-and-control.md)。

这一步交付的是壳：托盘 / 菜单栏图标和菜单、与内核的连接和重连、受限 IPC、窗口安全约束，以及对着假内核的端到端测试。面板页面、平台集成是后续子任务；打包发布见下文「打包与发布」。

## 运行

需要 Node 22.18 或更新。

```sh
cd ui
npm ci
npm run check-types   # 副本与契约的生成物一致
npm run typecheck     # tsc，含端到端用例
npm test              # 构建并跑单元测试（node:test）
npm run test:e2e      # 构建、给 node_modules 里的 Electron 写入 test 档位 fuses、跑 Playwright
```

手动看托盘：

```sh
npm run fake-kernel   # 在状态目录写 control.json，起假内核，stdin 里敲命令（warn / drain / mismatch / drop …）
npm start             # 另开终端，启动 Electron
```

`XMESH_UI_STATE_ROOT` 可以改变查找 `*/control.json` 的目录（默认是 `<UserConfigDir>/xmesh`），`XMESH_UI_LANG=en` 切英文，`XMESH_BIN` 指定 `xmesh` 可执行文件（重启、停止内核时调用）。

## 目录

| 路径 | 内容 |
| --- | --- |
| `src/main/` | 主进程：`discovery`（找实例）、`client`（Control API 客户端，含 SSE）、`controller`（连接状态机，重连、`Last-Event-ID`）、`menu`（菜单模型，纯数据）、`tray`、`actions`（菜单与 IPC 共用的操作）、`ipc`、`window`、`policy`（安全策略常量）、`protocol`（`app://panel` 页面服务） |
| `src/preload/` | 沙箱预加载脚本，只暴露白名单函数 |
| `src/shared/` | 主进程、预加载、渲染进程共用的类型：控制接口类型（`generated/` 是契约生成物的副本）、状态、IPC 白名单、字符串表 |
| `testserver/` | 假内核：回放契约夹具，同样走 UDS / 命名管道，可编排状态、事件、断流、重启、版本不兼容、缺失能力 |
| `static/placeholder-panel/` | 面板页面落地前的占位页，只用来证明 IPC 边界可用。`dist/renderer/index.html` 存在时优先用它 |
| `assets/tray/` | 托盘图标，`npm run icons` 生成（无图像依赖） |
| `scripts/` | `flip-fuses.mjs`、`fuses.config.mjs`（release / test 两档 fuses），`gen-icons.mjs` |
| `test/unit/`、`test/e2e/` | 单元测试与 Playwright `_electron` 端到端 |

## 状态与图标

| 图标 | 条件 |
| --- | --- |
| 未运行（灰） | 没有 `control.json`，或连不上 |
| 版本不匹配（红叉） | `control.json` 的 `api_version` 或响应头 `X-Xmesh-Control-Version` 的主版本不是 1 |
| 排空中（暂停） | `drain.draining` |
| 需重启（带点） | `restart_required` |
| 降级（黄） | 有 `severity` 为 `warning` 或 `error` 的警告；`info` 级（`restart_required`、`allow_dangerous`）不变黄 |
| 正常（绿） | 以上都不满足 |

多个条件同时成立时按上表自上而下取第一个（契约 CTR-CTL-03、RUL-18），菜单里仍逐条列出。

## 安全约束与对应的测试

| 约束 | 实现 | 断言 |
| --- | --- | --- |
| `contextIsolation`、`sandbox`、无 `nodeIntegration`、无 webview | `policy.ts` 的 `panelWebPreferences` | `test/unit/security.test.ts`；端到端里渲染进程 `typeof require / process / Buffer` 均为 `undefined` |
| 预加载只暴露白名单 | `src/preload/index.ts`，名单在 `shared/ipc.ts` 的 `PANEL_API_METHODS` | `test/unit/preload.test.ts` 对着假 `electron` 加载预加载；端到端检查 `window.xmesh` 的键且已冻结 |
| IPC 只接受面板的顶层页面 | `ipc.ts` 的 `isTrustedSender` | 单元测试覆盖判定；端到端里用同一预加载开第二个窗口调用 `drain()`，被拒绝且内核没收到请求 |
| IPC 参数校验 | `shared/ipc.ts` 的 `parse*` | `test/unit/ipc.test.ts`；端到端传非法参数得到 `invalid_args` |
| 严格 CSP，只加载打包页面 | `policy.ts` 的 `PANEL_CSP`，`app://panel` 响应头带上 | 单元测试逐指令检查无 `unsafe-*`、无远程源；端到端里内联脚本、`eval`、远程脚本都被拦并触发 `securitypolicyviolation` |
| 禁止导航与新窗口 | `window.ts` 的 `hardenWebContents`，对所有 `WebContents` 生效 | 端到端：改 `location`、`window.open` 都无效，页面不重载，窗口数仍为 1 |
| 路径不越界 | `policy.ts` 的 `resolvePanelFile` | 单元测试覆盖 `..`、编码分隔符、NUL、他域 |
| Fuses | `scripts/fuses.config.mjs` | 单元测试断言两档配置；端到端读取被测 Electron 二进制的 fuse 线；`ui.yml` 对 release 档做一次 flip + verify |
| 内核给的路径和地址不直接交给系统 | `actions.ts` 的 `checkOpenablePath`、`connect.ts` | 单元测试：配置只能是 `.yaml/.json/…` 文件，日志审计只能是目录，网关地址含 shell 元字符时拒绝复制 |

Fuses 有两档。`release` 是发布用的，由后续的打包任务在 `afterPack` 里调用 `node scripts/flip-fuses.mjs --profile release --binary <app>`，之后用 `--verify` 复核。`test` 档只放开三项：Playwright 要靠 `--inspect` 连接（`EnableNodeCliInspectArguments`），测试从 `dist/` 的松散文件启动而不是 asar（`EnableEmbeddedAsarIntegrityValidation`、`OnlyLoadAppFromAsar`）。`RunAsNode`、`NODE_OPTIONS`、`file://` 额外权限在两档里都是关闭的。

## 打包与发布

安装包由 electron-builder 生成，配置在 `electron-builder.config.mjs`，工作流是 `.github/workflows/ui-package.yml`。

| 平台 | 产物 | 内置内核 |
| --- | --- | --- |
| Windows x64、arm64 | `xmesh-ui-<版本>-win-<arch>.exe`（NSIS，用户级安装，可改目录） | `resources\bin\xmesh.exe` |
| macOS arm64、x64 | `xmesh-ui-<版本>-mac-<arch>.dmg`（`.app` 设 `LSUIElement`，无 Dock 图标） | `Contents/Resources/bin/xmesh` |

Linux 没有托盘支持，不出安装包。

- **内核查找顺序**：`XMESH_BIN`，其次安装包内置的内核（`process.resourcesPath/bin/xmesh[.exe]`，`src/main/cli.ts`），最后 `PATH` 里的 `xmesh`。托盘调用 `xmesh restart|stop --json` 时用它。
- **Fuses**：`electronFuses` 直接取自 `scripts/fuses.config.mjs` 的 release 档，由 electron-builder 在写入 asar 摘要之后、签名之前翻转，所以 ASAR 完整性校验有效。`scripts/verify-package.mjs` 对产物再复核一遍。
- **AUMID**：`appId` 是 `app.xmesh.ui`，与主进程的 `setAppUserModelId` 相同（有单元测试保证），NSIS 开始菜单快捷方式带这个 ID，Windows 通知才能归属到 xmesh。
- **图标**：`build/icon.png`（`npm run icon` 生成），electron-builder 从它派生 `.ico` 和 `.icns`。

本地打包（以 Windows x64 为例，先在仓库根目录构建内核）：

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o ui/build/kernel/xmesh.exe ./cmd/xmesh
cd ui && npm ci
XMESH_KERNEL_BIN="$PWD/build/kernel/xmesh.exe" npm run package -- --win --x64
node scripts/verify-package.mjs --dir release/win-unpacked --kernel   # 本机能运行时再加 --run-kernel --smoke
```

`verify-package.mjs --smoke` 启动打包后的应用，对着假内核（`dist/testserver`）确认它真的连上并取到 `/v1/status`，这一步覆盖 asar 完整性、`app://panel` 协议和控制通道客户端。

### 签名与未签名 beta

现在没有证书，beta 安装包是**未签名**的，系统会提示：

- Windows：SmartScreen 显示「Windows 已保护你的电脑」，点「更多信息」再点「仍要运行」。
- macOS：Gatekeeper 拒绝打开。把 `xmesh.app` 拖进「应用程序」后执行 `xattr -dr com.apple.quarantine /Applications/xmesh.app`，或在「系统设置 > 隐私与安全性」里点「仍要打开」。

有证书后只需要在仓库里配置 secrets，不改任何文件，下一次构建自动签名：

| Secret | 内容 |
| --- | --- |
| `WIN_CSC_LINK`、`WIN_CSC_KEY_PASSWORD` | Windows Authenticode 证书（`.pfx` 的 base64）与口令 |
| `MAC_CSC_LINK`、`MAC_CSC_KEY_PASSWORD` | Developer ID Application 证书（`.p12` 的 base64）与口令 |
| `APPLE_ID`、`APPLE_APP_SPECIFIC_PASSWORD`、`APPLE_TEAM_ID` | 公证；三项都在才公证 |

macOS 签名时启用 Hardened Runtime，内置的内核二进制也会一并被签名。

### 并入发布流水线

`release.yml` 的 `ui` 作业调用本工作流（`workflow_call`），同样排在 `verify` 之后。`release` 作业等待 `build` 与 `ui` 两者，所以安装包和内核归档进入同一个预发布。`release` 作业的 `download-artifact` 用 `merge-multiple` 把 `xmesh-ui-*` 收进 `dist/`，`SHA256SUMS.txt` 随之列入安装包。版本号取自标签，所以文件名形如 `xmesh-ui-0.1.0-beta.1-win-x64.exe`。

## 与契约的关系

UI 以 [docs/contracts/control.md](../docs/contracts/control.md) 为准。

- 类型：`src/shared/generated/control.ts` 是 `api/control/v1/ts/control.ts`（由 `scripts/gen-control-ts` 从模式生成）的副本，用 `npm run sync-types` 更新，`npm run check-types` 在 CI 里检查两份一致。这样 `ui/` 不需要越出自己的目录编译。契约改了而副本没跟上，`ui.yml` 会失败。
- 夹具：`testserver/` 的假内核从 `docs/contracts/schemas/examples/control/` 读取契约夹具，走与真实内核相同的传输（Unix 域套接字 / 命名管道）。`test/unit/contract.test.ts` 用 JSON Schema 校验它发出的每个响应和事件，所以假内核不会和契约漂移。
- 能力协商：UI 读 `status.control.capabilities`，没有 `drain` 的内核上，暂停和恢复菜单项置灰；没有 `events` 的内核改为轮询状态；没有 `peers` 的内核不取对端列表，在线数仍取 `status.peers`。
- 事件流：重连带 `Last-Event-ID`；内核重启（`boot_id` 变化）或缺口时会发 `reset`，无论哪种，控制器都重读状态和对端，所以快照不依赖收到每一条事件。45 秒无任何字节（内核每 15 秒发一次 `: ping`）则断开重连，反复立刻断开时指数退避，上限 10 秒。

## 还没做

- 完整面板页面和首次运行向导（W1）
- Windows 自启、macOS 登录项（S2、S3）；有证书后的签名与公证只差配置 secrets，尚未用真实证书验证过
- `xmesh restart` / `stop` 子命令由内核通道线程提供，托盘只在菜单里调用 `xmesh <cmd> --json`
- 事件里的入口新客户端通知：契约的事件类型里没有入口事件，等内核提供
- 菜单里「执行 n/m」目前只显示共享池，独占池（`exec_exclusive`）在面板里展示
