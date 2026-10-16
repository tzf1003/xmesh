# 12. 桌面 UI 与本机控制通道

本篇记录一条未来开发路径：给 Windows 和 macOS 做系统托盘 / 菜单栏图标和管理面板，并且让 UI 与内核解耦。它是路线图阶段 6，不是新的验收门，也不改变阶段 1 到 5 的任何契约。本篇只写方案和拆分，不含实现。

日期：2026-10-16。基线：master `a91cac6`。

「工具栏」按 Windows 通知区域托盘图标和 macOS 菜单栏图标理解。窗口内的顶部工具栏属于面板，见 [面板](#面板)。

## 为什么要解耦

- 内核（`xmesh serve`）本来就是常驻无界面进程：Windows 用户任务或 SCM 服务，macOS LaunchAgent，Linux systemd 用户服务。`install --system` 的服务运行在 Session 0，根本不能弹窗。UI 只能是另一个进程。
- 发布流水线 `release.yml` 在 Ubuntu 上以 `CGO_ENABLED=0` 交叉编译六个目标。macOS 菜单栏需要调用 AppKit，这类库通常经 cgo 构建，必须在 macOS runner 上编译。把 UI 并入内核二进制会破坏纯 Go 交叉编译。
- [CON-ARCH-02](../constraints/architecture.md#con-arch-02) 要求内核发布物是单个 `xmesh` 二进制，三个角色同进程。UI 是可选的第二个发布物，内核没有它必须完整可用，所以不与该约束冲突，但需要写成裁定。
- UI 崩溃、升级、被用户退出，都不能影响节点上的 PTY、租约和进行中的 exec。

## 现状：UI 现在能依赖什么

| 能力 | 现状（master） | 对 UI 的影响 |
| --- | --- | --- |
| 读状态 | `xmesh status` 通过 MCP 调 `sys.info`，`--local` 时是 `127.0.0.1:7433` 无认证；Gateway 需 PAT | 这是面向模型的工具面，不能当 UI 控制通道。字段够做概览，但没有变更推送 |
| 设备列表 | Gateway 的 `devices.list` | 够用，但要经 Gateway 与 PAT |
| 排空 | 仅 tailnet Agent 上的 `/control/drain`，要 `tag:admin`；排空状态只在内存，没有「恢复」，重启才重置 | 本机 UI 没有入口；需要新增 resume |
| 配置 | `config.Store` 只读文件，文件变更后下一次 `Get()` 热加载，失败保留旧配置 | 没有写入路径；UI 写配置需要校验、原子写入和并发冲突检测 |
| 事件 | 没有事件总线。审计只写 `audit/*.jsonl`，只含元数据 | UI 要实时状态，需要新增事件流 |
| 配对、PAT、OAuth 口令 | `xmesh pair`、`gateway.pats` 的文件、`--oauth-password-file`，全是 CLI 或手工文件 | UI 要把这三件事做成向导，内核侧需要对应接口 |
| 服务安装 | `xmesh install`；没有 `uninstall`、`start`、`stop`、`restart` | UI 要管理生命周期，需要补子命令 |

## 总体结构

```mermaid
flowchart LR
  subgraph kernel["xmesh 内核进程（现有，纯 Go）"]
    roles["agent / gateway / ingress"]
    ctl["control 监听<br/>UDS 或命名管道"]
    roles --- ctl
  end
  subgraph ui["xmesh-ui（新，可选发布物）"]
    tray["托盘 / 菜单栏"]
    bff["本机面板服务<br/>127.0.0.1 随机端口"]
    cli["xmesh 子命令调用<br/>install / start / stop"]
  end
  tray -->|"Control API v1"| ctl
  bff -->|"Control API v1"| ctl
  cli -->|"服务管理器"| svc["launchd / 任务计划 / SCM"]
  svc -.启停.-> kernel
  browser["面板窗口<br/>浏览器，之后可换 WebView"] --> bff
  cloud["云端客户端 / 其他设备"] -->|"MCP only"| roles
```

两个接口，各自稳定：

1. **Control API v1**：内核对本机用户暴露的 JSON over HTTP，走 Unix 域套接字（macOS、Linux）或命名管道（Windows），不走 TCP。UI 的所有读写都经它。
2. **`xmesh` CLI**：服务安装、启停、卸载、配对这类内核进程不能自己做的事，UI 调用同一个二进制的子命令，并要求这些子命令提供 `--json` 输出。CLI 与 UI 共用同一份行为，不在 UI 里复制安装逻辑。

云端和其他设备仍然只看见 MCP。Control API 不挂在 Gateway、Ingress、tailnet 监听器上，也不是 MCP 工具。

## 控制通道

### 传输与身份

| 平台 | 端点 | 访问控制 |
| --- | --- | --- |
| macOS、Linux | 状态目录下 `control.sock`，目录 `0700`，套接字 `0600`。路径超过 UDS 长度上限时落到 `$TMPDIR/xmesh-<uid>/` | 连接时用对端凭据（`LOCAL_PEERCRED` / `SO_PEERCRED`）核对 uid 等于内核运行用户 |
| Windows | 命名管道 `\\.\pipe\xmesh-<sid>-<节点名>` | 创建时设置 DACL：运行用户 SID、`SYSTEM`、`Administrators`；`install --system` 时额外授权安装者的 SID；拒绝远程客户端（`PIPE_REJECT_REMOTE_CLIENTS`） |

状态目录里写 `control.json`（端点、内核版本、Control API 版本、PID），UI 通过枚举 `<UserConfigDir>/xmesh/*/control.json` 发现本机所有节点实例。一个用户可以有多个节点，面板按实例切换。

### 为什么不用 loopback TCP

任何本机进程和任何网页都能向 `127.0.0.1` 发请求，DNS rebinding 与 CSRF 都要靠额外防护。OS 级的套接字和管道权限更直接。同样的理由，现有 `127.0.0.1:7433` 的 MCP 监听也不能兼作控制通道，它面向模型，无认证。

### 同用户信任边界

内核运行用户下的任何进程都能连控制通道，这包括该用户的 L1 `exec.run` 子进程。但 L1 下这类进程本来就能直接改 `xmesh.yaml`（热加载生效）、读 PAT 文件，控制通道没有增加权限。要点：

- L2 沙箱必须挡住控制端点：Seatbelt 规则拒绝连接该套接字，bwrap 不绑定状态目录，Windows AppContainer 不在管道 DACL 中。这写成沙箱验收项。
- 配置写入只返回「改了什么」和「是否需要重启」，所有通过控制通道发生的变更，审计记 `actor=control`，仍只含元数据。
- 削弱安全的变更（`allow_dangerous`、放宽 `cwd_policy`、放宽 `authz`）由 UI 弹确认对话框。这是 UI 侧的体验保护，不是内核的授权边界，写入文档时必须讲清，不得让用户误以为内核会拦截。
- UI 不得变成工具调用的审批通道。[CON-ARCH-05](../constraints/architecture.md#con-arch-05) 仍然成立：冲突、策略拒绝、配额拒绝在同一次调用里返回结构化错误，UI 只展示，不放行。

### API 范围（v1）

路径前缀 `/v1`，请求与响应是 JSON，错误沿用 [CTR-ERR-01](../contracts/errors.md#ctr-err-01) 的信封。变更使用 `ETag` / `If-Match` 防并发覆盖。

| 端点 | 作用 | 内核侧状态 |
| --- | --- | --- |
| `GET /v1/status` | 节点、角色、网络身份、健康、`restart_required`、`warnings`、配额占用。字段是 `sys.info` 的超集 | 大部分已有，需汇总 |
| `GET /v1/events` | SSE 事件流，支持 `Last-Event-ID` 续传，环形缓存 1024 条 | 新增 |
| `GET /v1/peers` | 设备、在线状态、tag、直连或 DERP | 新增（数据来自 mesh） |
| `GET /v1/sessions` | PTY、进行中 exec、后端租约，只读 | 新增只读视图 |
| `POST /v1/drain`、`POST /v1/resume` | 排空（keep / kill）与恢复 | drain 已有；resume 新增 |
| `GET/PUT /v1/config` | 读取当前生效配置；写入前经 `config.Parse` 校验，临时文件加重命名原子写，返回 diff 与 `restart_required` | 新增。保留 YAML 注释，使用 yaml.Node 往返 |
| `POST /v1/pair/export`、`POST /v1/pair/import` | LAN 配对；导入必须带 UI 用户确认过的指纹 | 逻辑已有（`xmesh pair`），抽成库 |
| `GET/POST/DELETE /v1/pats` | 列出、创建、轮转、撤销本机 Gateway PAT。令牌正文只在创建响应里出现一次，并直接写入 `0600` 文件，之后不可读回 | 新增 |
| `GET/DELETE /v1/ingress/clients` | 查看已注册 OAuth 客户端，撤销；口令轮转只写不读 | 新增 |
| `GET /v1/audit` | 按时间与过滤条件读审计元数据 | 新增只读视图，沿用 [CTR-AUD-01](../contracts/audit.md) 字段，禁止正文 |

事件类型：`status`、`peer`、`call`（工具名、设备、状态、耗时，不含参数与输出）、`session`、`warning`、`config`、`lifecycle`。

### 版本与兼容

- API 只加不改。破坏性变化升到 `/v2`，内核同时提供 `/v1` 至少一个发布周期。
- `control.json` 与每个响应带 `X-Xmesh-Control-Version`。UI 遇到内核版本不兼容时进入「内核版本不匹配」状态，只保留打开日志目录和升级入口。
- 内核与 UI 版本独立，但前期同一个 tag 一起发布。

### 契约与解耦的工程保证

- 契约落在 `docs/contracts/control.md` 和 `schemas/control-*.schema.json`，正反例夹具接入现有 `check_examples.py`。
- 内核新增 `internal/control`，客户端类型与 Go 客户端放在公开包 `api/control/v1`。内核与 UI 共用该包的类型。
- UI 是独立 Go module（`ui/go.mod`），只允许 import `api/control/v1`。Go 的 `internal` 规则按导入路径判定，嵌套 module 不会自动拦住，所以 CI 加一条检查：`go list -deps` 中出现任何 `internal/` 包就失败。
- 提供 `ui/testserver`：按夹具回放的假内核。UI 开发与 CI 不依赖真实内核，也能覆盖「内核离线」「版本不兼容」「事件断流重连」。

## 托盘与菜单栏

### 状态

| 图标状态 | 触发 |
| --- | --- |
| 未运行（灰） | 找不到 `control.json` 或连接被拒绝 |
| 正常（绿） | 连通，无 `warnings` |
| 降级（黄） | 有 `warnings`：热加载失败、tailnet 未就绪、有对端离线 |
| 排空中（暂停图标） | 处于 drain |
| 需重启（带点） | `restart_required` |
| 版本不匹配（红叉） | Control API 主版本不兼容 |

### 菜单

两个平台一致，原生控件由各自平台的托盘实现渲染：

- 头部：节点名、状态文字、网络模式、在线对端数。
- 活动：PTY 与 exec 占用，例如「终端 3/20」。
- 动作：打开面板；暂停接收新调用（drain keep）与恢复；复制本机 Gateway 地址；复制 Claude Code 与 Codex 的 MCP 连接命令；打开配置文件、日志目录、审计目录。
- 设置：登录时启动 UI；重启内核。
- 退出：「退出托盘」只结束 UI；「停止内核」是另一项，带确认，因为它会终止本机 PTY。

通知：对端上线或离线、热加载失败、需要重启、配额耗尽、入口新客户端注册。通知只带设备名和状态，不带命令或路径内容。

### 平台差异

| 项 | Windows | macOS |
| --- | --- | --- |
| 内核常驻 | 默认用户登录任务；`--system` 的 SCM 服务在 Session 0，UI 仍在用户会话中经管道连接 | LaunchAgent |
| UI 自启 | 在 `HKCU` 的 Run 项或启动文件夹注册 | `SMAppService` 登录项 |
| 图标 | 通知区域；溢出区需引导用户固定；高 DPI、深浅主题 | `NSStatusItem`，模板图像随菜单栏明暗变色；以代理应用运行，无 Dock 图标 |
| 通知 | Windows 通知 | 用户通知中心 |
| 签名分发 | 代码签名证书，避免 SmartScreen 警告 | Developer ID 签名加公证。不上 App Store，因为沙箱会阻止连接状态目录下的套接字 |

## 面板

面板是同一个 UI 进程里的本机 web 应用。第一版在默认浏览器里打开；之后可换成内嵌 WebView（Windows WebView2，macOS WKWebView），不改前端。

- 本机面板服务只绑定 `127.0.0.1` 随机端口，每次启动生成一次性令牌，通过 URL 片段交给浏览器后换成同站 Cookie；校验 `Host` 与 `Origin`，防 DNS rebinding 与 CSRF。令牌不写盘。
- 面板服务是纯代理：把浏览器的请求翻译成 Control API 调用。它不持有任何内核状态。

页面：

| 页面 | 内容 |
| --- | --- |
| 概览 | 状态、角色、配额、最近调用的元数据 |
| 设备 | 对端、tag、直连或 DERP、在线状态；LAN 配对向导（显示指纹，用户确认后才导入） |
| 会话 | PTY、exec、后端租约；终端 owner；一键关闭孤儿终端 |
| 授权 | `authz`、tag 与工具授权的只读视图和编辑；PAT 创建与撤销 |
| 入口 | OAuth issuer、已注册客户端、撤销、口令轮转；公网入口状态 |
| 配置 | 结构化表单加原文编辑，保存前显示 diff 和「需重启」提示 |
| 审计 | 按时间、设备、工具过滤；只展示元数据 |
| 首次运行向导 | 选择模式（lan / tailnet）、生成配置、调用 `xmesh install`、验证连通 |

默认语言中文，字符串走资源表，预留英文。

## 技术选型

| 方案 | 结论 |
| --- | --- |
| Go 托盘库加本机 web 面板（默认） | 选用。语言与内核一致，一套代码覆盖两平台，前端可后续换 WebView。macOS 需要在 macOS runner 上构建 |
| 两套原生：macOS Swift 菜单栏，Windows C# 或 Win32 | 原生体验最好，但两套代码、两套 CI。作为托盘细节不够用时的退路，Control API 是 JSON，任何语言都能接 |
| Electron / Tauri 整包 | 体积或工具链过重，托盘能力仍要原生补。不选 |
| 把托盘并入 `xmesh` 二进制 | 破坏 CGO=0 交叉编译和 CON-ARCH-02。不选 |

Linux 托盘不在本期范围。控制通道和 CLI 对 Linux 同样可用，之后补托盘不需要改内核。

## 阶段与拆分

没有时间估计，只有依赖关系。每个子任务一个分支，从 master 切出，开 draft PR，CI 全绿后自动合并；`config.go`、schema、`go.yml` 只追加，`docs/implementation-status.md` 由集成方（讨论串「xmesh 生产实现推进」）维护。

| 阶段 | 交付 | 依赖 |
| --- | --- | --- |
| U0 契约先行 | `docs/contracts/control.md`、模式与夹具、新增约束与裁定（见下）、`api/control/v1` 类型 | 无 |
| U1 内核只读通道 | `internal/control`：监听、身份校验、`status` / `events` / `peers` / `sessions` / `audit`；`xmesh status --control`；服务子命令 `uninstall` / `start` / `stop` / `restart` 与 `--json` | U0 |
| U2 内核写操作 | drain / resume、`config` 读写、配对库化、PAT、入口客户端与口令；审计 `actor=control` | U1 的骨架 |
| U3 托盘壳 | 实例发现、Control API 客户端、状态机、菜单模型、`ui/testserver`、通知；先在假内核上跑通 | U0，可与 U1 并行 |
| U4 面板 | 本机面板服务、前端七个页面、首次运行向导 | U3，写页面依赖 U2 |
| U5 平台集成与分发 | Windows：管道 DACL 原生测试、自启、安装包与签名。macOS：登录项、`.app`、签名与公证。`release.yml` 增加 UI 构建，输出到同一个 beta 预发布 | U3 |
| U6 硬化 | L2 沙箱拒绝连接控制端点的验收、可访问性、高 DPI、英文资源、升级与回滚 | U2、U5 |

### 并行子任务与文件归属

| 子任务 | 范围 | 独占文件 | 共享文件 |
| --- | --- | --- | --- |
| C0 契约 | U0 | `docs/contracts/control.md`、`docs/contracts/schemas/control-*`、`api/control/v1/` | `docs/conformance.md`、`docs/constraints/*` 只追加 |
| K1 内核通道 | U1，之后 U2 | `internal/control/**`、`cmd/xmesh/control.go`、`cmd/xmesh/service_cmd.go` | `internal/app/runtime.go`（只追加装配）、`cmd/xmesh/main.go`（只加子命令分支） |
| S1 托盘壳 | U3 | `ui/**`（除 platform 与 web 目录） | `.github/workflows/go.yml` 追加 UI 作业 |
| S2 Windows | U5 的 Windows 部分 | `ui/platform/windows/**` | 无 |
| S3 macOS | U5 的 macOS 部分 | `ui/platform/darwin/**` | 无 |
| W1 面板 | U4 | `ui/panel/**`、`ui/web/**` | 无 |
| R1 发布 | U5 的分发 | `.github/workflows/release.yml`（独占）、`scripts/package-*` | 无 |

K1 里 U2 的写操作拆成独立文件（`config.go`、`pats.go`、`pair.go`），不与 U1 文件冲突。Windows 安全闭环相关线程仍在改 `internal/winbox`、`internal/app` 的 L2 接线，K1 对 `runtime.go` 只追加监听器装配，冲突交集成方处理。

## 待写入的约束与裁定

下列条目随 U0 写入对应文件，本篇不直接修改约束库，以免与进行中的线程冲突。

| 编号（暂定） | 内容 |
| --- | --- |
| CON-ARCH-09 | 内核可以提供仅限本机用户的控制通道（UDS 或命名管道）。它禁止监听 TCP，禁止挂在 Gateway、Ingress、tailnet 监听器上，禁止作为 MCP 工具暴露。与 CON-ARCH-01 的关系：云端客户端只见 MCP；本通道不面向云端客户端，也不是 `api/xmesh/v1` 那类节点间控制 |
| CON-ARCH-10 | 内核不依赖 UI 存在：无 UI 时全部角色、drain、配置热加载与 CLI 状态读取照常工作。UI 退出、崩溃、升级不得改变 PTY owner、租约 token、进行中的 exec |
| CON-SEC-12 | 控制通道只接受内核运行用户（Windows 另含 DACL 授权者）。L2 沙箱内的进程必须不能连接它。经控制通道的变更写审计 `actor=control`（审计模式需追加 `actor` 字段），只含元数据 |
| RUL-（暂定） | 发布物：`xmesh`（内核，纯 Go）与 `xmesh-ui`（可选，托盘加面板）。CON-ARCH-02 的「单一二进制」指内核；UI 是可选的第二发布物，不属于必须同时部署的服务 |
| CTR-CTL-01 至 | 状态、事件、会话、drain、配置、配对、PAT、入口、审计的请求与响应、错误码、`ETag` 语义 |

## 验收

| 观察点 | 方法 |
| --- | --- |
| 无 UI 也完整 | 不启动 `xmesh-ui`，跑现有三平台全量测试；再对控制通道用纯 HTTP 客户端跑 U1 与 U2 的契约测试 |
| 解耦 | UI 进程被强杀并重启，已有 PTY owner、租约 token 与 exec 不变；内核重启后 UI 自动重连并重新订阅事件 |
| 不对外 | 测试枚举内核的所有监听器，断言控制通道不在 TCP 上、不在 Gateway 路由表里、`tools/list` 中没有它 |
| 身份 | 另一个 OS 用户连接被拒；Windows 原生 CI 验证管道 DACL 与远程客户端拒绝 |
| 沙箱 | L2 的 exec 子进程连接控制端点失败（macOS、Linux 原生测试；Windows AppContainer 原生测试） |
| 隐私 | 事件流与审计视图的编码器沿用 INV-10：注入含 stdout 的对象时拒绝 |
| 夹具 | 控制契约的正例过模式，反例失败，接入 `contracts.yml` |
| UI | 假内核回放：内核离线、版本不匹配、事件断流重连、warnings 出现与消失，对应的图标状态与菜单项断言 |
| 分发 | beta 预发布包含两个平台的 UI 包；Windows 签名通过 SmartScreen，macOS 公证通过 `spctl` 检查 |

## 需要用户提供或确认

下面两项不阻塞 U0 到 U4，影响 U5 的分发。

- Windows 代码签名证书与 macOS Apple Developer ID（含公证用凭据）。没有时 beta 仍可发布，但会有系统警告，需要用户手动放行。
- 技术选型默认用 Go 托盘加 web 面板。若更想要原生 Swift 菜单栏，需要在 S1 开工前告知。

## 与路线图的关系

阶段 6 在门 4 通过后启动，但 U0、U1、U3 不依赖网络环境，可以与阶段 5 的硬化并行。它不改变任何已有门的验收标准。
