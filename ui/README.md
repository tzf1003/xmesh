# xmesh-ui

xmesh 的桌面托盘壳（Electron，路线图阶段 6 的 U3）。独立的 TypeScript 项目，不 import 任何 Go 代码，和内核之间只有一份契约：本机控制通道（Control API v2，Unix 域套接字或 Windows 命名管道）。设计见 [docs/design/12-ui-and-control.md](../docs/design/12-ui-and-control.md)。

这一步交付的是壳：托盘 / 菜单栏图标和菜单、与内核的连接和重连、受限 IPC、窗口安全约束，以及对着假内核的端到端测试。面板页面见下文「面板页面」；平台集成是另一个子任务；打包发布见下文「打包与发布」。

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
| `src/main/system/` | 平台相关的主进程模块：`binary`（找 `xmesh` 可执行文件）、`autostart`（登录项）、`notifier`（通知节流）、`firstrun`（只提示一次的标记） |
| `src/main/` | 主进程：`discovery`（找实例）、`client`（Control API 客户端，含 SSE）、`controller`（连接状态机，重连、`Last-Event-ID`）、`menu`（菜单模型，纯数据）、`tray`、`actions`（菜单与 IPC 共用的操作）、`ipc`、`window`、`policy`（安全策略常量）、`protocol`（`app://panel` 页面服务）、`panel-service`（面板的数据读写，固定路由）、`mesh-service`（tailnet 管理：校验过的请求 → `xmesh mesh --json`）、`mesh-fake`（测试用的命令行替身）、`setup`（首次运行向导的落盘与 `xmesh install/start`） |
| `src/preload/` | 沙箱预加载脚本，只暴露白名单函数 |
| `src/shared/` | 主进程、预加载、渲染进程共用的类型：控制接口类型（`generated/` 是契约生成物的副本）、状态、IPC 白名单、字符串表 |
| `testserver/` | 假内核：回放契约夹具，同样走 UDS / 命名管道，可编排状态、事件、断流、重启、版本不兼容、缺失能力 |
| `src/renderer/` | 面板页面（Preact + TSX），由 `scripts/build-renderer.mjs`（esbuild）打成 `dist/renderer/app.js`，主进程经 `app://panel` 提供；页面、`lib/`（纯函数）、`messages.ts`（中英字符串表）见下文 |
| `assets/tray/` | 托盘图标，`npm run icons` 生成（无图像依赖） |
| `scripts/` | `flip-fuses.mjs`、`fuses.config.mjs`（release / test 两档 fuses），`gen-icons.mjs` |
| `test/unit/`、`test/e2e/` | 单元测试与 Playwright `_electron` 端到端 |

## 面板页面

首页采用设备拓扑与右侧详情区，连接和运行配置可切换查看。左侧导航按任务分组，高级功能默认折叠，Ctrl/Cmd+K 可搜索设备或功能。配置页默认表单，提供 YAML 源码标签并保护切换前的草稿。交互与数据语义见 [拓扑面板](../docs/design/16-topology-panel.md)。

渲染进程只通过 `window.xmesh`（预加载白名单）与外界交互，没有 `fetch`、没有 Node。依赖只有 `preact` 和 `esbuild`，都是 devDependency：esbuild 把 preact 打进 `app.js`，安装包里不带 `node_modules`。页面不使用内联脚本和样式表，`index.html` 自带与响应头同等严格的 CSP。

| 页面 | 内容 | 需要的能力 |
| --- | --- | --- |
| 概览 | 状态、负载、警告、最近调用（事件流 `call`）、暂停 / 恢复 / 强制排空 | `status`，排空需要 `drain` |
| 设备 | 对端列表；配对导出 / 导入（导入必须手敲指纹） | `peers`，配对需要 `pair` |
| 会话 | 在线会话与进程；没有会话时给空状态 | `sessions` |
| 授权 | PAT 创建（密钥只显示一次）/ 轮换 / 删除；权限矩阵只读展示 | `pats`、`config.read` |
| 入口 | 入口客户端撤销、入口密码轮换（12–256 字符，只写） | `ingress` |
| 配置 | YAML 编辑，先 `dry_run` 看差异和 `weakens_security`，再带 `If-Match` 写入；`config_invalid` 诊断逐行显示，`precondition_failed` 提示重新载入 | `config.read`，写入需要 `config.write` |
| 审计 | 事件 / 时间 / 主体过滤，游标翻页 | `audit` |
| 首次运行向导 | 内核未运行且无实例时的默认页：节点名、角色、工作区、入口，主进程重新校验后用 `0600` 写配置，再执行 `xmesh install --config` 和 `xmesh start --config` | 无 |

内核缺某项能力时，对应页面显示「内核不支持」，不会发出请求。`weakens_security` 只是 UI 层的二次确认，不是内核边界（契约如此规定）。

### 关于与更新

面板的「关于与更新」页和顶部提示条负责版本检测与一键更新，不需要终端，内核没在运行时也能用。面板和内核在同一个安装包里，所以一起更新。

- 检测：主进程（Electron 的 `net.fetch`，跟随系统代理）读 `https://api.github.com/repos/tzf1003/xmesh/releases`，按语义化版本选出比当前新、并且带有本机安装包和 `SHA256SUMS.txt` 的最新一版。启动 20 秒后检查一次，之后每 6 小时一次；「自动检查」可关，「包含测试版」默认开（现在发布的都是 beta）。发现新版本时，面板顶部出现提示条，托盘菜单多一项，并发一条系统通知（每个版本只通知一次）。
- 下载与校验：只从 `github.com/tzf1003/xmesh/releases/download/<tag>/` 取 `xmesh-ui-<版本>-<win|mac>-<x64|arm64>.<exe|dmg>`，边下边算 SHA-256，必须和 `SHA256SUMS.txt`（以及 API 给出的 digest，两者都在时必须一致）相符才继续；不符就丢弃，不会停任何东西。
- 替换：先对每个在运行的节点执行 `xmesh stop`，记下停掉的节点；写下 `pending.json`；启动一个脱离的脚本后退出。Windows 的脚本（`cmd`）等应用进程结束，静默运行 NSIS 安装程序（沿用原安装目录），成功后启动新应用；macOS 的脚本（`/bin/sh`）等应用退出，挂载 dmg，把 `xmesh.app` 换成新的（先改名保留旧的，复制失败就放回去），清除隔离属性后打开。路径都经环境变量或参数传入，脚本文本里没有路径。
- 恢复：新应用启动时读 `pending.json`，重新启动被停掉的节点（`xmesh start`），版本对上就提示「已更新到 X」，没对上就带着脚本留下的原因提示失败，旧版本仍可用。配置、令牌、节点状态都在安装目录之外，更新不碰它们。
- 限制：macOS 要求应用在可写的位置（通常是「应用程序」）；从 dmg 或 AppTranslocation 运行时只提示先拖进「应用程序」；没有写权限时打开 dmg 让用户手动覆盖。内核若来自 `XMESH_BIN` 或 `PATH`（不是安装包里的那个），面板会说明它不会被更换。Linux 没有 UI 安装包：用 `install-linux.sh` 装的网页管理界面通过同一个更新面板自更新，见下文「Linux 网页管理界面」。
- 试用：`XMESH_UPDATE_FEED=<地址>` 把检测和下载指向一个仿 GitHub 的目录（`<地址>/releases` 返回发布列表，`<地址>/download/<tag>/<文件>` 提供文件），用来不发版本就演练一次更新。

### Tailscale 管理页面

首次运行向导默认提供「浏览器登录 Tailscale」：主进程运行 `xmesh login --json`，识别经过控制面来源校验的授权地址并自动打开系统浏览器。完成登录后，节点身份保存在节点的 `network/tsnet` 目录，再安装服务，之后复用该身份连接。启用设备审批或标签限制的 tailnet 需要管理员批准；只有获得 `tag:device` 后才继续安装。浏览器登录用于设备入网，管理整个 tailnet 仍需管理凭据。向导同时支持粘贴已有 key、选择 key 文件，或用管理凭据创建预授权 key。

侧栏的「Tailscale 管理」通过 `xmesh mesh … --json` 管理控制面。登录表单默认选择读写权限；密钥经主进程通过 stdin 交给 CLI，验证通过后原子保存到 `<状态根>/mesh/mesh-credential.json`（Unix `0600`）。每次调用带 `--credential-file`，页面展示凭据文件路径与权限。复制的登录命令带 `--panel`，使用同一个文件。选择只读权限时，CLI 在写操作前拒绝该凭据；控制面的实际 scope 和标签限制仍生效。密钥不进入配置、命令行参数或日志。

| 标签页 | 内容 |
| --- | --- |
| 设备与标签 | `mesh status`：设备、在线、角色、问题；改标签先预览（`mesh tags`），策略 `tagOwners` 里没有属主的标签不允许写 |
| 接入新设备 | `mesh authkey --out <文件>`：预授权 key 由命令行直接写进用户选的文件（0600，另存为对话框），界面只拿到路径 |
| 策略 | 当前策略、xmesh 模板（`mesh policy --template`）预览 → 确认 → 写入，写入带预览时的版本（`--expect-version`），策略期间被改动则拒绝；每次写入自动留快照，可预览后回滚；文件模式的控制面（`policy_write` 为 false）只显示完整新策略供手工放到服务器上 |
| 权限矩阵 | `mesh matrix`：网络层（控制面策略）与工具层（节点 `agent.authz`）两层的放行结果，两层不一致的格子高亮；连着内核时把节点配置传给命令行 |
| DERP 中继 | 探测（`derp probe`，可对比官方延迟）、预览、写入（`derp apply`，探测失败自动回滚）；Headscale 生成 `derp.paths` 文件和 `config.yaml` 片段；准入同步（`derp sync`，本机放行文件或远端准入服务，token 只传文件路径） |

主进程里的 `MeshService`（`src/main/mesh-service.ts`）只接受 `parseMeshRequest`（`src/shared/mesh.ts`）校验过的请求，转成固定的 argv：子命令写死，每个值都用 `--flag=value` 贴在旗标上，不会被当成旗标；DERP 地域里不允许出现命令行自己的分隔符 `,` 和 `=`。命令行输出的 JSON 先经容错的归一化（Go 的 `null` 切片、零值时间）再交给页面。测试用 `src/main/mesh-fake.ts` 在进程内模拟命令行（`XMESH_UI_TEST=1` 才会接入）。

构建：`npm run build` 先 `tsc`（主进程、预加载、测试用库），再 `scripts/build-renderer.mjs` 打包渲染进程；`XMESH_UI_MINIFY=1` 压缩。

## 状态与图标

系统托盘图标反映本机内核连接：未运行（灰）、版本不匹配（红叉）、已连接（对号）。成功连接时，对端离线、告警、排空和待重启信息显示在面板、菜单及提示文本中。面板内状态按下表判定。

| 面板状态 | 条件 |
| --- | --- |
| 未运行（灰） | 没有 `control.json`，或连不上 |
| 版本不匹配（红叉） | `control.json` 的 `api_version` 或响应头 `X-Xmesh-Control-Version` 的主版本不是 2 |
| 排空中（暂停） | `drain.draining` |
| 需重启（带点） | `restart_required` |
| 降级（黄） | 有 `severity` 为 `warning` 或 `error` 的警告；`info` 级（`restart_required`、`allow_dangerous`）不变黄 |
| 正常（绿） | 以上都不满足 |

多个条件同时成立时按上表自上而下取第一个（契约 CTR-CTL-03、RUL-18），菜单里仍逐条列出。

控制接口连接失败且状态文件记录的 PID 已退出时，面板和托盘提供「清理残留状态」。清理只移除 `control.json`，保留节点配置；有配置的节点可使用「启动内核」。清理将状态文件原子移入临时目录，重新检查进程和文件内容后删除；若捕获到新内核的文件则恢复，并保留同时发布的更新文件。系统服务状态提示由管理员清理。PID 无法确认或被复用时仍按「内核无响应」处理。


## 紧凑托盘面板

点击系统托盘图标打开 400 × 580 DIP 的自定义浮层，右键打开更多操作。面板包括概览、设备、服务、活动四页，支持设备搜索、连接配置复制、暂停 / 恢复、节点切换、登录启动和内核维护。主题跟随系统，字体与缩放复用显示偏好。

主窗口与浮层共用一条内核事件连接。停止 / 重启先显示当前节点和任务影响，确认后再次校验目标；退出界面保留后台内核。连接配置包含访问凭据，令牌由主进程直接写入剪贴板。

实现及兼容说明见 [自定义紧凑托盘面板](../docs/design/20-tray-popover.md)。需要原生菜单时可设置 `XMESH_TRAY_UI=native`；Wayland 默认使用该兼容路径。

## 安全约束与对应的测试

| 约束 | 实现 | 断言 |
| --- | --- | --- |
| `contextIsolation`、`sandbox`、无 `nodeIntegration`、无 webview | `policy.ts` 的 `panelWebPreferences` | `test/unit/security.test.ts`；端到端里渲染进程 `typeof require / process / Buffer` 均为 `undefined` |
| 预加载只暴露白名单 | `src/preload/index.ts`，名单在 `shared/ipc.ts` 的 `PANEL_API_METHODS` | `test/unit/preload.test.ts` 对着假 `electron` 加载预加载；端到端检查 `window.xmesh` 的键且已冻结 |
| IPC 分别认可登记的主窗口和托盘顶层页面 | `ipc.ts` 的 `isTrustedSender`、`tray-ipc.ts` 的独立校验 | 单元测试覆盖判定；端到端里用同一预加载开第二个窗口调用 `drain()`，被拒绝且内核没收到请求 |
| IPC 参数校验 | `shared/ipc.ts` 的 `parse*` | `test/unit/ipc.test.ts`；端到端传非法参数得到 `invalid_args` |
| 严格 CSP，只加载打包页面 | `policy.ts` 的 `PANEL_CSP`，`app://panel` 响应头带上 | 单元测试逐指令检查无 `unsafe-*`、无远程源；端到端里内联脚本、`eval`、远程脚本都被拦并触发 `securitypolicyviolation` |
| 禁止导航与新窗口 | `window.ts` 的 `hardenWebContents`，对所有 `WebContents` 生效 | 端到端：改 `location`、`window.open` 都无效，页面不重载，窗口数仍为 1 |
| 路径不越界 | `policy.ts` 的 `resolvePanelFile` | 单元测试覆盖 `..`、编码分隔符、NUL、他域 |
| Fuses | `scripts/fuses.config.mjs` | 单元测试断言两档配置；端到端读取被测 Electron 二进制的 fuse 线；`ui.yml` 对 release 档做一次 flip + verify |
| 内核给的路径和地址不直接交给系统 | `actions.ts` 的 `checkOpenablePath`、`connect.ts` | 单元测试：配置只能是 `.yaml/.json/…` 文件，日志审计只能是目录，网关地址含 shell 元字符时拒绝复制 |

Fuses 有两档。`release` 是发布用的，由后续的打包任务在 `afterPack` 里调用 `node scripts/flip-fuses.mjs --profile release --binary <app>`，之后用 `--verify` 复核。`test` 档只放开三项：Playwright 要靠 `--inspect` 连接（`EnableNodeCliInspectArguments`），测试从 `dist/` 的松散文件启动而不是 asar（`EnableEmbeddedAsarIntegrityValidation`、`OnlyLoadAppFromAsar`）。`RunAsNode`、`NODE_OPTIONS`、`file://` 额外权限在两档里都是关闭的。

## Windows 与 macOS 平台集成

| 项 | Windows | macOS |
| --- | --- | --- |
| UI 自启 | 写 `HKCU\...\Run`，读写都带可执行文件路径；用户在任务管理器「启动应用」里关掉时，菜单项保持未勾选并提示 | 登录项（`SMAppService`）；`requires-approval` 时保持未勾选并提示去「系统设置 > 登录项」允许 |
| 开发模式 | 未打包时不写任何登录项，只提示「只有安装后的 xmesh 才能设置」 | 同左 |
| 找 `xmesh` | `XMESH_BIN`，应用内 `resources/bin/xmesh.exe`，`PATH`，`%LOCALAPPDATA%\Programs\xmesh`、`%LOCALAPPDATA%\xmesh\bin`、`%ProgramFiles%\xmesh`、`~\go\bin` | 同序；登录项启动的 PATH 只有 `/usr/bin:/bin:…`，所以补查 `/opt/homebrew/bin`、`/usr/local/bin`、`~/.local/bin`、`~/go/bin` |
| 找内核实例 | `%APPDATA%\xmesh\*`，再加 `%ProgramData%\xmesh\*`：`install --system` 的服务在 Session 0 以 SYSTEM 运行，它自己的用户配置目录普通用户读不到，需要用 `--state-dir` 放到 `%ProgramData%\xmesh\<节点名>`；管道本身由 `control.json` 的 `endpoint` 给出，UI 在用户会话里连接 | `<UserConfigDir>/xmesh/*` |
| 通知 | 首次运行（仅安装版）提示一次：图标在溢出区时怎么固定到任务栏；通知需要开始菜单快捷方式带 AUMID（`app.xmesh.ui`，即 electron-builder 的 `appId`） | 用户通知中心；菜单栏图标是模板图像，无 Dock 图标（`app.dock.hide()`，Info.plist 的 `LSUIElement` 由 `electron-builder.config.mjs` 写入） |
| 通知节流 | 同一条 60 秒内不重复；一批超过 3 条合并成一条；60 秒内最多 6 条；点击打开面板 | 同左 |
| 睡眠与锁屏 | 唤醒、解锁时立刻重连事件流，不等 45 秒空闲超时 | 同左 |

## 打包与发布

安装包由 electron-builder 生成，配置在 `electron-builder.config.mjs`，工作流是 `.github/workflows/ui-package.yml`。

| 平台 | 产物 | 内置内核 |
| --- | --- | --- |
| Windows x64、arm64 | `xmesh-ui-<版本>-win-<arch>.exe`（NSIS，用户级安装，可改目录） | `resources\bin\xmesh.exe` |
| macOS arm64、x64 | `xmesh-ui-<版本>-mac-<arch>.dmg`（`.app` 设 `LSUIElement`，无 Dock 图标） | `Contents/Resources/bin/xmesh` |

Linux 没有托盘支持，不出安装包；Linux 使用网页管理界面（见下文）。

- **内核查找顺序**：`XMESH_BIN`，其次安装包内置的内核（`process.resourcesPath/bin/xmesh[.exe]`，`src/main/cli.ts`），最后 `PATH` 里的 `xmesh`。托盘调用 `xmesh restart|stop --json` 时用它。
- **Fuses**：`electronFuses` 直接取自 `scripts/fuses.config.mjs` 的 release 档，由 electron-builder 在写入 asar 摘要之后、签名之前翻转，所以 ASAR 完整性校验有效。`scripts/verify-package.mjs` 对产物再复核一遍。
- **AUMID**：`appId` 是 `app.xmesh.ui`，与主进程的 `setAppUserModelId` 相同（有单元测试保证），NSIS 开始菜单快捷方式带这个 ID，Windows 通知才能归属到 xmesh。
- **图标**：`build/icon.png`（`npm run icon` 生成），electron-builder 从它派生 `.ico` 和 `.icns`。

本地打包（以 Windows x64 为例，先在仓库根目录构建内核）：

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o ui/build/kernel/xmesh.exe ./cmd/xmesh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags=-H=windowsgui -o ui/build/kernel/xmesh-background.exe ./cmd/xmesh
cd ui && npm ci
XMESH_KERNEL_BIN="$PWD/build/kernel/xmesh.exe" npm run package -- --win --x64
node scripts/verify-package.mjs --dir release/win-unpacked --kernel   # 本机能运行时再加 --run-kernel --smoke
```

`verify-package.mjs --smoke` 启动打包后的应用，对着假内核（`dist/testserver`）确认它真的连上并取到 `/v2/status`，这一步覆盖 asar 完整性、`app://panel` 协议和控制通道客户端。

### 签名与未签名 beta

现在没有证书，beta 安装包是**未签名**的，系统会提示：

- Windows：SmartScreen 显示「Windows 已保护你的电脑」，点「更多信息」再点「仍要运行」。
- macOS：Gatekeeper 拒绝打开。把 `xmesh.app` 拖进「应用程序」后执行 `/usr/bin/xattr -dr com.apple.quarantine /Applications/xmesh.app`，或在「系统设置 > 隐私与安全性」里点「仍要打开」。若 xmesh 在清除隔离属性前已经打开过，macOS 会从只读副本（AppTranslocation）运行它，清除命令对这个副本无效：先退出 xmesh，执行命令，再从「应用程序」重新打开。

有证书后只需要在仓库里配置 secrets，不改任何文件，下一次构建自动签名：

| Secret | 内容 |
| --- | --- |
| `WIN_CSC_LINK`、`WIN_CSC_KEY_PASSWORD` | Windows Authenticode 证书（`.pfx` 的 base64）与口令 |
| `MAC_CSC_LINK`、`MAC_CSC_KEY_PASSWORD` | Developer ID Application 证书（`.p12` 的 base64）与口令 |
| `APPLE_ID`、`APPLE_APP_SPECIFIC_PASSWORD`、`APPLE_TEAM_ID` | 公证；三项都在才公证 |

macOS 签名时启用 Hardened Runtime，内置的内核二进制也会一并被签名。

### 并入发布流水线

`release.yml` 的 `ui` 作业调用本工作流（`workflow_call`），同样排在 `verify` 之后。`release` 作业等待 `build` 与 `ui` 两者，所以安装包和内核归档进入同一个预发布。`release` 作业的 `download-artifact` 用 `merge-multiple` 把 `xmesh-ui-*` 收进 `dist/`，`SHA256SUMS.txt` 随之列入安装包。版本号取自标签，所以文件名形如 `xmesh-ui-0.1.0-beta.1-win-x64.exe`。

## Linux 网页管理界面

Linux 主机上用浏览器打开和桌面端相同的面板，前面多一个管理密钥登录页。设计与安全约束见 [docs/design/12](../docs/design/12-ui-and-control.md) 的「Linux 网页管理界面」。

需要 Node 22.18 或更新，以运行内核的同一个用户运行（面板从该用户的 `~/.config/xmesh` 找内核）。

**一次装好内核和网页界面**：用发布页的 `install-linux.sh`，它把两者各装成一个 systemd 用户服务（`xmesh-<节点名>` 和 `xmesh-webui`），见 [安装说明](../docs/agent/install.md#linux)。

**只装网页界面**：发布页提供 `xmesh-webui-<版本>-linux.tar.gz`，解开后用它自带的 `install` 子命令注册服务：

```sh
node xmesh-webui/xmesh-webui.js install             # 写 systemd 用户单元并启动；首次生成并打印管理密钥
node xmesh-webui/xmesh-webui.js install --key-stdin # 用自己的密钥：从标准输入读第一行（不会回显）
node xmesh-webui/xmesh-webui.js uninstall           # 停止并移除服务（--purge 同时删除密钥和数据目录）
```

也可以不装服务，前台运行：

```sh
node xmesh-webui/xmesh-webui.js                     # 首次启动打印管理密钥，默认监听 http://127.0.0.1:7480/
node xmesh-webui/xmesh-webui.js --print-key         # 再次查看密钥
node xmesh-webui/xmesh-webui.js --rotate-key        # 更换密钥（旧会话全部失效）
```

从源码运行：`cd ui && npm ci && npm run web`（产物在 `dist/web/`）。

| 选项 | 说明 |
| --- | --- |
| `--listen ADDR` | 监听地址，默认 `127.0.0.1:7480`（环境变量 `XMESH_WEBUI_LISTEN`）；`:7480` 表示所有地址 |
| `--tls-cert FILE --tls-key FILE` | 以 HTTPS 提供，Cookie 带 `Secure`；监听非本机地址时请使用 |
| `--allowed-origin ORIGIN` | 反向代理对外的来源，如 `https://panel.example.com:8088`（可重复；环境变量 `XMESH_WEBUI_ALLOWED_ORIGINS`，逗号分隔）。面板对登录等 POST 要求 `Origin` 等于 `scheme://Host`，经 TLS 终止的反代访问时浏览器发的是 `https://…` 而后端是 HTTP，需用它放行（含 `https://` 来源时登录 Cookie 同时带 `Secure`）；反代须保留带端口的 `Host`（nginx 用 `$http_host`，不要用 `$host`） |
| `--data-dir DIR` | 面板偏好与密钥文件所在目录，默认 `$XDG_CONFIG_HOME/xmesh-webui`（`XMESH_WEBUI_DATA_DIR`） |
| `--key-file FILE` | 密钥文件，默认 `<data-dir>/webui.key`（`XMESH_WEBUI_KEY_FILE`）；手写的密钥至少 16 个字符，文件必须是 `0600` |
| `--lang zh\|en` | 未保存偏好时的面板语言 |

**自动更新**：由 `install-linux.sh` 安装的网页界面（`<前缀>/webui` 加 `xmesh-webui` 用户服务）会像桌面端一样检测新发布，在面板的更新页一键更新。下载的是发布里的 `install-linux.sh`（先按 `SHA256SUMS.txt` 校验），面板退出后由一个临时的 systemd 用户单元以 `install-linux.sh --update --version <标签>` 运行它：按发布清单校验并替换内核与网页界面文件，重启运行中的内核服务和 `xmesh-webui`，服务配置、管理密钥和节点配置不动；失败时旧文件保持原样，面板重新启动并显示原因。手工解压运行的网页界面（没有 `xmesh-webui` 服务）仍需自己替换发布归档。

远程访问推荐 SSH 隧道：`ssh -L 7480:127.0.0.1:7480 <主机>`，再在本机打开 `http://127.0.0.1:7480/`。若直接对外监听，请配置 TLS；没有 TLS 时启动日志会警告，密钥和会话会以明文传输。修改密钥文件后发送 `SIGHUP`（`systemctl --user reload xmesh-webui`）即可生效，所有会话随之退出。

`install` 子命令的选项与前台运行相同（`--listen`、`--tls-cert/--tls-key`、`--allowed-origin`、`--data-dir`、`--key-file`、`--lang`），另有：

| 选项 | 说明 |
| --- | --- |
| `--key-stdin` | 从标准输入第一行读取管理密钥并保存（至少 16 个字符，不含空白）；自己输入的密钥不会被回显，只有自动生成的才会打印一次 |
| `--rotate-key` | 生成新密钥并替换，旧会话全部失效 |
| `--xmesh-bin PATH` | 面板调用的 `xmesh`（重启、停止内核等）；默认取 `XMESH_BIN` 或 PATH 中的。用户服务的 PATH 很短，所以单元里固定写成 `Environment=XMESH_BIN=…` |
| `--insecure` | 监听非本机地址又没有 TLS 时必须显式给出，否则 `install` 拒绝 |
| `--no-start` / `--linger` / `--print` | 只启用不启动 / 同时执行 `loginctl enable-linger`，让服务在退出登录后继续运行 / 只打印单元文件，不改动任何东西 |

单元里写的是 node 和脚本的**绝对路径**：移动安装目录或用 nvm 切换 Node 版本后，重新执行一次 `install` 即可。密钥本身不会写进单元文件，只有它的文件路径。服务日志用 `journalctl --user -u xmesh-webui` 查看。

与桌面端的差别：

| 桌面端 | 网页端 |
| --- | --- |
| 写剪贴板（连接配置、令牌、命令） | 浏览器写剪贴板；非 HTTPS 的远程地址浏览器不允许时，弹出文本框供手动复制 |
| 原生确认框、文件选择框 | 浏览器对话框；文件路径是**服务器上**的绝对路径 |
| 工具调试台「保存结果」 | 由浏览器下载到访问者的电脑 |
| 打开配置文件、日志目录 | 不能打开服务器上的文件，改为复制路径 |
| 托盘、登录项、自动更新、桌面控制紧急停止快捷键 | 不提供；更新时替换归档后重启服务 |

顶栏多一个「退出登录」按钮。实现：`src/web/`（服务端：`auth.ts` 密钥与会话，`server.ts` 路由，`bridge.ts` 把桌面能力转给页面，`service.ts` systemd 用户服务的安装与卸载，`main.ts` 组装服务），`src/renderer/web/`（登录页与 `window.xmesh` 的 HTTP 实现），`scripts/build-web.mjs`（打包，并检查服务端没有引入 electron）。测试见 `test/unit/web.test.ts` 和 `test/unit/web-service.test.ts`；安装脚本 `scripts/install-linux.sh` 的测试在 `scripts/test_install_distribution.py`。

## 与契约的关系

UI 以 [docs/contracts/control.md](../docs/contracts/control.md) 为准。

- 类型：`src/shared/generated/control.ts` 是 `api/control/v2/ts/control.ts`（由 `scripts/gen-control-ts` 从模式生成）的副本，用 `npm run sync-types` 更新，`npm run check-types` 在 CI 里检查两份一致。这样 `ui/` 不需要越出自己的目录编译。契约改了而副本没跟上，`ui.yml` 会失败。
- 夹具：`testserver/` 的假内核从 `docs/contracts/schemas/examples/control/` 读取契约夹具，走与真实内核相同的传输（Unix 域套接字 / 命名管道）。`test/unit/contract.test.ts` 用 JSON Schema 校验它发出的每个响应和事件，所以假内核不会和契约漂移。
- 能力协商：UI 读 `status.control.capabilities`，没有 `drain` 的内核上，暂停和恢复菜单项置灰；没有 `events` 的内核改为轮询状态；没有 `peers` 的内核不取对端列表，在线数仍取 `status.peers`。
- 事件流：重连带 `Last-Event-ID`；内核重启（`boot_id` 变化）或缺口时会发 `reset`，无论哪种，控制器都重读状态和对端，所以快照不依赖收到每一条事件。45 秒无任何字节（内核每 15 秒发一次 `: ping`）则断开重连，反复立刻断开时指数退避，上限 10 秒。

## 还没做

- 面板的结构化配置表单：目前是 YAML 编辑 + 差异预览；终端会话没有关闭路由（契约未定义），孤儿终端只展示
- Tailscale 管理页使用一个当前控制面的凭据文件；切换控制面时重新保存对应凭据。Headscale 多用户入网在向导中填写用户名。
- 有证书后的签名与公证只差配置 secrets，尚未用真实证书验证过
- 登录项和通知的运行时部分已在 `src/main/system/`，但只有安装版才生效，真机验证要等安装包
- `xmesh restart` / `stop` 子命令由内核通道线程提供，托盘只在菜单里调用 `xmesh <cmd> --json`
- 事件里的入口新客户端通知：契约的事件类型里没有入口事件，等内核提供
- 菜单里「执行 n/m」目前只显示共享池，独占池（`exec_exclusive`）在面板里展示

本地下载在校验完成后发布私有临时文件。覆盖已有目标时，先将目标保留在同目录私有临时目录，核对其身份后以独占硬链接发布；发布失败会恢复原文件。若恢复时目标路径已被其他程序占用，保留双方文件并在错误中给出原文件的保留位置。支持硬链接的文件系统使用独占链接发布；其他文件系统使用独占创建后复制。复制阶段失败会保留部分目标和原文件备份，并在错误中给出位置。

工具调试台的历史记录保留参数结构，全部参数值替换为脱敏占位符；重新执行前需填写参数。
