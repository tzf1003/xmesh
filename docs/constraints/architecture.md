# 架构与角色约束

## CON-ARCH-01

对云端客户端，xmesh 只提供 MCP Streamable HTTP 传输，以及 OAuth 2.1 保护的资源。禁止为云端另定义 RPC、WebSocket 私有协议或自定义鉴权头。

tailnet 内节点之间也使用 MCP。`api/xmesh/v1` 若存在，只用于 MCP 工具表达不了的节点控制，并且只监听 tailnet，不对入口公开。

验收：入口的路由表上，业务调用只有 MCP 端点；OAuth 发现端点按 [CTR-OAUTH-01](../contracts/oauth-authz.md#ctr-oauth-01) 提供。

## CON-ARCH-02

Go 内核是一个 module，发布物名为 `xmesh`，Windows、macOS、Linux 由同一份源码交叉编译。Agent、Gateway、Ingress 不得拆成必须同时部署的多个服务。可选的 computer 能力允许平台原生 `xmesh-computer` 执行器，由图形用户会话中的 `xmesh computer host` 监督；它们只负责本机桌面接入，不成为第二个 mesh 节点或资源权威。见 [Computer](../contracts/computer.md)。

验收：`serve` 在三个 GOOS 上都能至少启动 Agent 的本机监听（阶段 1 可以先不做 tsnet）。

## CON-ARCH-03

每个节点实例的常驻内核是一个 `xmesh` 进程。启用哪些角色由 `node.roles` 决定，而不是由不同的二进制决定。同一进程可以同时是 agent、gateway 和 ingress。启用 computer 时，可由独立的图形用户会话 host 和原生执行器提供受保护的本机桌面通道；控制租约仍只由该节点 Agent 仲裁。

验收：配置 `roles: [agent, gateway]` 时，tailnet（或阶段 1 的本地）MCP 与 `127.0.0.1:8080/mcp` 同时可用，且工具状态一致。

## CON-ARCH-04

PTY 表、exec 槽、幂等缓存、输出分页、后端 actor、租约表的读写只发生在拥有该资源的 Agent 进程。Gateway 收到这些资源的冲突时，原样转达 Agent 的结构化错误，不得在本地仲裁「帮调用方抢锁」或改写 `held_by`。

验收：停掉 Gateway 再拉起，Agent 上已有的 PTY owner 与租约 token 不变。

## CON-ARCH-05

冲突、策略拒绝和配额拒绝都必须在同一次工具调用里返回结构化错误。禁止把请求放进待审批队列，禁止返回「请用户在某个 UI 上点击允许」作为成功路径。

模型收到错误后自行决定等待、换设备或换做法。错误码表见 [CTR-ERR-01](../contracts/errors.md#ctr-err-01)。

## CON-ARCH-06

Agent 不创建、不删除、不枚举「当前工作区」，也不操作 git worktree。配置里的 `workspaces` 只是路径别名。调用方每次用 `cwd` 或 `@别名` 指明目录。

验收：仓库中不存在 worktree 管理工具；`sys.info` 的 `workspaces` 等于配置别名，而不是扫描磁盘得到的仓库列表。

## CON-ARCH-07

入口的授权服务器只有一个登录主体。禁止租户 id、组织切换、多用户目录。Codex 与 Claude 是两个 OAuth client，不是两个用户系统。

tailnet 内的身份是节点与 MCP 会话，不是这个登录主体的子用户。

## CON-ARCH-08

本机或已加入 tailnet 的客户端调用另一台设备时，路径是「本机 Gateway → 目标 Agent」。禁止为了鉴权或审计把这条路径强制绕到入口节点。入口的审计只覆盖经过入口的调用；节点本地审计覆盖到达本 Agent 的调用。

验收：断开入口进程后，两台已入网设备之间的 `<peer>/<tool>` 仍然成功。

## CON-ARCH-09

内核可以提供一条仅限本机用户的控制通道（[CTR-CTL-01](../contracts/control.md#ctr-ctl-01)）：macOS 与 Linux 用 Unix 域套接字，Windows 用命名管道。禁止监听 TCP，禁止挂在 Gateway、Ingress、tailnet 监听器上，禁止作为 MCP 工具暴露。

与 [CON-ARCH-01](#con-arch-01) 的关系：云端客户端只见 MCP。本通道不面向云端客户端，也不是 `api/xmesh/v1` 那类节点间控制。

验收：枚举内核的所有监听器，控制通道不在 TCP 上、不在 Gateway 路由表里，`tools/list` 里没有它。

## CON-ARCH-10

内核不依赖 UI 存在。没有 UI 时，全部角色、drain、配置热加载与 CLI 状态读取照常工作。UI 退出、崩溃或升级不得改变 PTY owner、租约 token、进行中的 exec。

验收：强杀并重启 UI 进程，已有 PTY owner、租约 token 与 exec 不变；内核重启后 UI 自动重连并重新订阅事件。
