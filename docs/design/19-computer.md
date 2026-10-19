# Computer：三端远程桌面能力

状态：实现中。基线：2026-10-19，`origin/master` 的 `854ac20`。本文件同时记录设计和验收清单；未勾选项不能作为已交付能力。

## 目标与结构

Windows、macOS、Linux 上的 Agent 经现有 Streamable HTTP MCP、Gateway / Ingress 和 mesh 调用目标桌面。Go 内核管理身份、会话、排他控制权、限额、审计；原生执行器在用户图形会话中采集屏幕并注入输入；Electron 面板负责初始化权限、状态和本机接管。关闭面板不终止执行器。资源权威始终属于目标 Agent。

调用链：`Agent → Gateway / Ingress → mesh → 目标 Agent → Computer Manager → 受保护本机 IPC → 原生执行器 → 用户桌面`。

`computer` 默认关闭。首次系统授权由本机用户完成；调用时缺少权限立即返回结构化错误。已授权且可用的图形会话支持无人值守操作。系统登录、解锁、安全桌面及虚拟桌面须按实际支持状态报告。

## 工具与状态

工具：`computer.info`、`computer.open`、`computer.observe`、`computer.act`、`computer.action_status`、`computer.renew`、`computer.close`、`computer.sessions`。

观察和控制会话绑定传输层 caller。每个交互桌面仅一个控制者，多个显示器共享控制权。会话和每次输入均验证 `desktop_epoch`、租约代次、有效期、运行状态和权限。会话断开、撤销、锁屏、注销、执行器重建、禁用配置与 kill drain 终止控制，清理按键和待执行动作。一个图形桌面只绑定一个管理内核实例。

坐标以返回图片的像素为准。frame 记录显示器、图形会话代次、布局版本、裁剪区域、采集时间和缩放；服务端转换为系统坐标。布局变化使旧 frame 失效。截图不能保证界面没有发生变化，执行器仍须检查当前桌面状态。

动作有调用方提供的 `action_id`，相同 ID / 参数返回既有结果；参数改变拒绝。状态区分 accepted、executed、failed、unknown。输入发出与记录结果间崩溃可能产生 unknown，调用方须重新观察，不能盲目重试。每批最多八个动作、执行预算五秒；动作类型为 move、click、drag、scroll、key、text、wait，按键和按钮必须在单个有限动作内释放。

图片经 MCP ImageContent 返回，元数据经 structuredContent 和精简文本返回。执行成功表示系统接受输入；业务结果由后续观察验证。

## 权限、隐私与平台

设备 scope：`computer:observe:<device>`、`computer:control:<device>`，剪贴板读写为独立显式能力。Gateway 按工具和参数检查 scope，Agent 按节点 tag 检查授权，执行器检查 OS 权限及桌面身份。完整桌面控制具有登录用户的 GUI 权限，不受 fs cwd 围栏自动限制；受限工作环境使用专用系统账号、桌面或虚拟机。

日志只记录类型、动作 ID、大小、状态和耗时。输入文本、截图、剪贴板及这些敏感内容的可猜测摘要不进入审计。证据留存单独配置保留期限和容量。Ingress 会处理截图和动作，是信任边界的一部分。

原生执行器采用按平台编译的 Rust 工程：macOS ScreenCaptureKit / CoreGraphics；Windows 图形采集 / SendInput，用户会话执行器与 SYSTEM 服务分离；Linux Wayland Portal / PipeWire / EIS，X11 采集 / XTEST。权限和后端可用性按运行时探测结果声明。IPC 版本化、检查对端身份、限制消息大小；不兼容时仅关闭 computer。

## 实施与验收

- [ ] C0：工具、错误、配置、scope、IPC 和版本化契约及 Schema。
- [ ] C1：会话管理、排他租约、串行有界队列、幂等、action_status、撤销、断连、配置重载、布局失效；故障与并发测试。
- [ ] C2：MCP 图片结果及 Agent / Gateway / Ingress 端到端测试；参数级 scope、动态工具发现、配额与审计过滤。
- [ ] C3：macOS 执行器；真机截图、中文输入、点击、拖动、权限撤销、锁屏与升级。
- [ ] C4：Windows 执行器；标准用户和 SYSTEM 内核连接用户桌面；多 DPI、权限边界、会话切换及原生测试。
- [ ] C5：Linux X11 与 Wayland 执行器；固定版本 GNOME / KDE 授权、恢复、截图与输入测试。
- [ ] C6：本机 Control API、CLI 和 UI：权限引导、状态、暂停、撤销、接管、紧急停止与用户输入暂停。
- [ ] C7：安装、自启、签名配置、打包、版本握手和升级恢复，复用现有缓存。
- [ ] C8：跨 NAT / 禁 UDP / DERP、丢失响应、两个 Agent 争用、混合 DPI / 屏幕旋转 / 热插拔、锁屏 / 注销 / 崩溃 / 超时、客户端图片交付验收。
- [ ] C9：现有工具及跨平台构建回归；PR 的 CI 和独立评审；合并主工作分支及清理任务工作区。

性能指标为待测目标：截图编码本机 p95 ≤300ms，输入提交 p95 ≤100ms，本机紧急停止 p95 ≤200ms，断连后持有输入清理 ≤2s。测试须分别记录模型、网络、排队和系统执行耗时。

增强阶段：低频实时预览后增加 WebRTC 媒体通道、独立媒体凭证 / TURN、辅助功能树和语义操作。实现时继续共享同一桌面仲裁；每项独立声明能力和平台验收证据。

## 验证记录

开发过程中在此追加实际命令、测试范围和平台证据；编译和夹具通过不等同真机桌面验收。

2026-10-19 第一轮：

- `go test -race -p 2 ./... -timeout=300s`：34 个包、1239 项测试通过；随后补充的共享错误信封测试通过，相关包 vet 通过。
- Windows amd64 与 Linux amd64 的 `go build ./cmd/xmesh` 交叉编译通过；Windows 管道客户端验证服务器进程 SID。
- macOS arm64 原生执行器 cargo test、clippy、fmt 和发布构建通过；三项 Rust 测试验证协议、键名与布局版本。
- `TestNativeDesktopRoundTrip` 真机经本机 broker 和 Go 管理器采集 1280×831、331171 字节截图，调试构建耗时 7.336s。该证据覆盖截图链路，输入和性能目标仍需验证。
- 同一链路使用发布构建复测：1280×831、445803 字节，单次截图 730ms；尚未达到 300ms 的目标，后续按采集、缩放、编码拆分优化。
- 契约正反例和文档相对链接校验通过；符合性检查 145/145 通过。
- 原生适配已接 macOS / Windows 的 ScreenCaptureKit / Windows Graphics Capture 与输入库，并加入 Linux X11 适配源码；Windows / Linux 原生构建交由新工作流验证。Wayland 的共用授权会话、屏幕与输入坐标映射仍在实现。

第二轮：本机 Control API 1.9 增加 desktop 状态、暂停和撤销，CLI 增加 `xmesh control computer`。面板加入桌面控制页、能力开关、权限初始化指引、会话撤销、接管与恢复；主进程注册紧急暂停快捷键。UI 460 项单元测试及两项 Electron 交互测试通过，截图人工检查通过；交互测试覆盖真实窗口中的撤销、接管、恢复和旧内核提示，使用假内核，尚不代表三端真人接管验收。新控制路由在真实本机套接字上的测试通过。

第一轮原生 CI 的 macOS 作业通过；Linux 报告两处类型错误，Windows 报告 scap 的 published Windows 接口不匹配。后续提交修正 Linux 类型并将 Windows 采集改为直接调用 windows-capture。新增当前权限和桌面状态复查，授权撤销、系统锁定和桌面代次改变时回收控制权。

原生第三方接口通过锁定版本的 cidre、windows-capture 与 enigo 调用。Linux X11 采集使用 x11rb。本机 Cargo target 通过 `--target-dir` 指向共享缓存；Cargo registry 与 Go 缓存按用户全局复用。

第三轮：全仓库 1248 项 Go 竞态测试通过。Windows 原生构建、clippy 和 IPC 在 GitHub runner 上通过；Linux Wayland 源码完成编译，随后修正 clippy 的枚举大小和类型复杂度检查。Wayland 共用 RemoteDesktop / ScreenCast 授权和 PipeWire stream，支持 Portal 输入、持久恢复令牌、本机显式授权与关闭信号；GNOME / KDE 真机行为仍待验收。

macOS 采集改用 SCScreenshotManager 的单帧接口，发布构建单次截图为 203ms，尚需多次采样确认 p95。独立 Electron 测试窗口经真实 broker 和原生执行器验证点击、中文文本、组合键、拖动及滚动，连续三次通过；测试也修正了滚动定位和方向。测试窗口使用独立用户数据目录，不保存桌面图片或输入到工作区。

新增本机 `computer setup|install|start|stop|restart|uninstall|authorize`、图形会话自启配置、面板初始化按钮，以及原生执行器在 UI 和六平台归档中的打包路径。macOS 执行器采用 `app.xmesh.computer` 应用标识。该轮安装包和跨平台输入 CI 仍待完成，正式签名和公证依赖发布凭据。

第四轮：macOS arm64 本机 UI 安装目录构建通过，包内原生应用的签名结构检查、内核与执行器版本握手、release fuses，以及应用对假内核控制通道的启动冒烟检查通过。该证据覆盖解包后的应用目录，Windows 安装程序与 DMG 的安装 / 升级仍待 CI 和平台验收。

macOS 发布构建对 1280×831 截图连续采样 10 次，中位数 115ms、p95 122ms、最大 122ms；该结果只代表本机环境。Linux 截图为每次采集重新取得授权会话的 PipeWire remote socket，防止重复使用已经消费的协议连接。执行器限制同时存活的原始采集任务，超时后继续返回忙状态，直至原始任务结束。

第五轮：提交 `b39240f` 的三端原生 CI（运行 39208061767）全部通过，Windows runner 与 Linux Xvfb 独立窗口均验证点击、中文输入、组合键、拖动、滚动，随后通过 IPC 测试。相同提交的 UI 打包运行 39208472484 四项通过（Windows x64/arm64、macOS arm64/x64），校验包内内核、原生执行器、fuses 和本机架构的启动冒烟。此证据不包含系统安装后的升级或正式签名 / 公证。

新增 `pause_on_user_input`，在支持监测的桌面上检测本机点击或按键后撤销控制租约，并阻断后续动作；Wayland 暂不声明全局输入监测能力，启用该策略时申请控制明确失败，观察仍可用。macOS 的外部进程修饰键输入测试验证撤销控制通过。监测开启后的完整输入复测遇到本机用户活动，记录为中断 / 跳过；不得把该次视为完整输入通过。

第六轮：整合主分支 `9f3ccb1` 后，1288 项 Go 竞态测试通过；467 项 UI 单元测试与六项 Electron 交互测试通过。整合提交为 `79e858a`。该提交的 Linux 原生输入与 macOS 原生 IPC CI 已通过；Windows 原生输入测试被输入监测判定为本机活动而失败，需继续验证合成事件标记与监测行为。

后续补充：输入管道写入受调用上下文约束，执行器停止读取时终止该执行器并使旧桌面代次失效；新增阻塞管道回归。共享截图缓存总预算为 64 MiB，按采集时间淘汰旧图；暂停只撤销控制，关闭能力才同时撤销观察。启用本机输入暂停策略时，旧控制租约立即回收，新租约建立当前输入基线。本机状态在同一次响应中投影权限变化和会话撤销。相关五个 Go 包 267 项竞态测试通过，macOS Rust 三项测试及 clippy 通过。

Windows 原生输入改用带 VIRTUALDESK 的 SendInput 物理坐标，支持负原点和副屏；执行器嵌入 PerMonitorV2 manifest，并报告显示器 DPI 和旋转。新增 Windows manifest 提取校验与 SYSTEM 内核到用户桌面的截图 / 原生执行验收。这些新增 Windows 改动仍须等待最新提交的 CI。

执行器的自有输入标记收敛到 32 位可表示范围，以保持 Windows 跨 WOW64 输入路径的标记一致性；Windows 输入监测的本次失败是否由该边界导致，仍以新 CI 结果为准。执行器异常退出后，Host 保留当前内核绑定并按新 epoch 重建原生子进程，回归测试验证其他内核无法趁重建接管。

第七轮：更新器接入独立 Computer Host 生命周期，替换安装包前停止 Host 并等待原生子进程退出，安装后核对恢复的 Host 版本，再启动节点；交接失败尝试恢复已停止的 Host，失败则报告更新未完成。本机 Host 状态查询不取得桌面绑定。新增 Unix 自定义 state-dir 端点一致性，以及 Windows 桌面服务卸载前停止与重启等待。474 项 UI 单元测试、更新与桌面页面九项 Electron 交互测试通过；契约正反例及文档链接校验通过，符合性 146/146。Go 全量 1297 项竞态测试通过；这些本机测试尚未证明安装程序完成真实 OS 升级。

第八轮：提交 `fe7b094` 的原生工作流运行 39212197400 三端全部通过。Windows 除完整输入夹具外，还通过嵌入 PerMonitorV2 manifest 提取检查，以及 SYSTEM 内核经用户 SID 管道取得真实 MCP 截图、执行原生 wait 动作并关闭会话的验收；该 wait 动作验证服务到原生执行链，点击等输入由独立窗口夹具覆盖。macOS 运行实际用户 LaunchAgent 完成安装、启动、原生子进程创建、重启（PID 与 epoch 均改变）、停止和卸载；任务注册和端点清理读回通过。新增同一验收测试并接入 macOS / Windows CI，Windows 新增外部进程修饰键介入测试，等待最新提交运行。

面板增加经真实 MCP 客户端的按需与两秒桌面预览，观察租约逐帧关闭；取消、清屏、离页和后台暂停有明确生命周期。476 项 UI 单元测试与桌面页面三项 Electron / Playwright 交互测试通过；交互使用协议夹具提供的应用窗口截图，证明图片渲染和会话清理，尚未证明真实远端桌面预览。人工检查常规窗口与 900×700 窄窗口，控制与接管按钮位于预览之前，图片没有造成横向溢出。原生采集新增 32 Mi 像素上限，限制应用侧原始像素分配。

第九轮准备：新增真实目标验收脚本 accept_computer.py，校验连续截图的 MCP 图片与元数据、有限控制执行、动作去重 / 查询、续租和关闭；脚本不留存画面。Headscale 一次性环境支持 --computer-helper，由 hs-b 连接用户 Host，分别从 hs-a 的对等 Gateway 和 hs-i 的 OAuth 入口调用真实桌面。Linux 原生 CI 接入该链路；结果待最新工作流验证。既有脚本 24 项与验收脚本 13 项回归通过。

本机 macOS 通过真实 Gateway PAT → Agent → Host → 原生执行器链路运行该脚本，连续收到两张 1280×831 截图（626290 / 626341 字节），有限控制、动作去重 / 查询、续租及关闭通过。默认 Gateway PAT 的 scope 未包含桌面权限时调用被拒绝；验收使用显式授予 computer:control scope 的独立 PAT。该记录证明本机完整 MCP 链路，跨 tailnet 与 OAuth 桌面链路仍待 CI。

原生执行器补充活跃用户会话检查：Windows 查询 WTS Active，macOS 同时检查登录完成和当前控制台会话，Linux logind 返回 inactive 时阻断输入。macOS 发布构建经真实 broker 返回 1280×831 截图通过，单次 350ms（含 -race 的 Go 验收链路），本次不是性能分位数证据。控制会话创建与策略热更新并发读取现统一在管理锁内，新增竞态回归连续三次通过。Headscale 桌面作业首轮因配置显式 PAT 后不再生成默认 PAT 而失败；夹具改为分别使用显式对等工具 PAT 与桌面 PAT，待复测。

第十轮：本机 macOS 在附加自定义 user-data 的滚轮事件上复现投递失败；使用非变参 CGEventCreateScrollWheelEvent2 并保留系统提供的事件源信息后，连续三轮隔离窗口完整输入与本机介入撤销均通过。本机输入暂停的监测范围为点击和按键，滚轮不在该监测集合内。Windows CI 安装服务重启遇到已关闭管道连接的 EOF；Host 状态查询改为一次新连接复核，仅 OS 端点确实不存在才返回停止，持续 EOF 保持失败；两项回归及相关三个包 82 项竞态测试通过。

Headscale 桌面作业第二轮可通过身份和 scope 校验，但裸 Xvfb 桌面输入初始化未就绪。验收现使用保持运行的隔离 Electron 窗口提供图形会话，等待 CI 复测。原生后端在启动时初始化输入失败后每秒进行无提示重试；Wayland 仍由显式本机授权恢复。macOS / Windows 安装服务生命周期与后续网络步骤以最新提交 CI 为准。

MCP 工具发现补全 Agent 所需动作枚举、键名、输入图片坐标、必填字段、租期和预算，以及响应不确定时先查询 / 观察的恢复说明；新增 schema 正反例回归，运行时边界继续由目标管理器校验。

第十一轮：整合主分支 `bdc5814` 的提交 `d65f9f2`，本机 1314 项 Go 竞态测试、478 项 UI 单元测试及桌面 / 更新 / DERP 策略页 27 项交互测试通过。原生 CI 运行 39217790440 的 Linux 作业已通过：真实 Headscale / tsnet 对等 Gateway 与 OAuth 入口分别连续交付两张 1280×800 原生图片（每张 41265 字节），完成有限控制、动作去重 / 查询、续租和关闭。该环境为同机 tsnet 直连，DERP 及跨主机故障仍待验收。

补充本机 Host 紧急停止与恢复：绕过管理内核直接关闭原生执行器，暂停共享标记跨 Host 重启保留，远程请求无法恢复；本机明确恢复后生成新 epoch。macOS 实测单次停止 11ms，该样本不代表 p95；实际安装的 LaunchAgent 验收增加重启后仍暂停、显式恢复检查并通过。相关三个 Go 包 85 项竞态测试、479 项 UI 单元测试、桌面页三项交互测试通过。新增 Host 紧急停止的 Windows 行为等待最新 CI。

第十二轮：整合提交 `d65f9f2` 的三端原生运行 39217784355 全部通过，包括 Windows 原生输入、本机介入、SYSTEM 到用户桌面、安装用户 Host 生命周期。其后新增的持久紧急停止继续由最新提交复测。六平台原生归档工作流增加 PR 构建和架构 / 版本 / 可执行权限 / macOS 应用身份校验；本机 macOS arm64 打包后的 ad-hoc 签名结构与版本校验通过，完整六架构产物待 CI。
