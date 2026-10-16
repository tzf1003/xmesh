# 约束索引

约束是实现必须遵守的规则。每条有稳定编号，测试和评审直接引用编号。不变量是运行时必须保持的性质，违反即缺陷，即使没有任何一条请求显式失败。

级别只用「必须」「禁止」「应该」。条目里的「验收」是这条约束最少要有的观察，完整用例名见 [符合性矩阵](../conformance.md)。

## 不变量

| 编号 | 陈述 |
| --- | --- |
| [INV-01](invariants.md#inv-01) | 同一资源的有效 fencing token 至多一个持有者 |
| [INV-02](invariants.md#inv-02) | PTY 会话数不超过设备上限，且不超过单调用方上限 |
| [INV-03](invariants.md#inv-03) | Gateway 与入口不持久化设备资源状态 |
| [INV-04](invariants.md#inv-04) | `_lease_token` 不会出现在发往后端进程的请求里 |
| [INV-05](invariants.md#inv-05) | 授权或策略拒绝的调用没有副作用 |
| [INV-06](invariants.md#inv-06) | 文件写对读者要么完整可见，要么不可见 |
| [INV-07](invariants.md#inv-07) | PTY cursor 单调；覆写只通过 `truncated` 表达 |
| [INV-08](invariants.md#inv-08) | 云端工具名稳定为 `<节点>/<工具>`，不出现 `local/` |
| [INV-09](invariants.md#inv-09) | 已下线设备的工具不会被继续路由 |
| [INV-10](invariants.md#inv-10) | 审计日志不含文件内容与命令输出正文 |

## 架构与角色

| 编号 | 级别 | 陈述 |
| --- | --- | --- |
| [CON-ARCH-01](architecture.md#con-arch-01) | 必须 | 对云端只暴露 Streamable HTTP MCP + OAuth 2.1 |
| [CON-ARCH-02](architecture.md#con-arch-02) | 必须 | 单一 Go 模块、单一二进制，三平台交叉编译 |
| [CON-ARCH-03](architecture.md#con-arch-03) | 必须 | 每台机器一个进程，角色由配置开关 |
| [CON-ARCH-04](architecture.md#con-arch-04) | 必须 | Agent 是本机资源状态的唯一权威 |
| [CON-ARCH-05](architecture.md#con-arch-05) | 禁止 | 人工审批挂起；冲突必须是结构化错误 |
| [CON-ARCH-06](architecture.md#con-arch-06) | 禁止 | Agent 管理工作区或 worktree |
| [CON-ARCH-07](architecture.md#con-arch-07) | 必须 | 单租户，授权服务器不做用户目录 |
| [CON-ARCH-08](architecture.md#con-arch-08) | 必须 | tailnet 内对等调用不经过入口节点 |

## 网络

| 编号 | 级别 | 陈述 |
| --- | --- | --- |
| [CON-NET-01](network.md#con-net-01) | 禁止 | Agent 监听公网地址 |
| [CON-NET-02](network.md#con-net-02) | 必须 | 设备 MCP 只在 tailnet 或已配对的 lan 内可达 |
| [CON-NET-03](network.md#con-net-03) | 必须 | 用 tsnet 用户态加入，不依赖系统 Tailscale、root、TUN |
| [CON-NET-04](network.md#con-net-04) | 必须 | 官方 Tailscale 与 Headscale 仅 `control_url` 不同 |
| [CON-NET-05](network.md#con-net-05) | 必须 | UDP 打洞失败后退到 DERP |
| [CON-NET-06](network.md#con-net-06) | 必须 | 入口是对等节点加上公网监听和 OAuth |
| [CON-NET-07](network.md#con-net-07) | 必须 | 入口节点带 `tag:ingress`，目标按 tag 二次授权 |
| [CON-NET-08](network.md#con-net-08) | 禁止 | 本期实现 `relay` 模式 |
| [CON-NET-09](network.md#con-net-09) | 必须 | `lan` 首次配对交换节点证书并使用 mTLS |

## 工具与路径

| 编号 | 级别 | 陈述 |
| --- | --- | --- |
| [CON-TOOL-01](tools.md#con-tool-01) | 必须 | 所有路径参数经过同一个 `cwd.Resolver` |
| [CON-TOOL-02](tools.md#con-tool-02) | 必须 | `EvalSymlinks` 之后才做 allow/deny |
| [CON-TOOL-03](tools.md#con-tool-03) | 必须 | 成功响应回显解析后的绝对路径 |
| [CON-TOOL-04](tools.md#con-tool-04) | 必须 | BOM、UTF-16 和换行风格原样保留 |
| [CON-TOOL-05](tools.md#con-tool-05) | 必须 | glob / grep 有上限且带 `truncated` |
| [CON-TOOL-06](tools.md#con-tool-06) | 必须 | `fs.delete` 默认拒绝目录递归 |
| [CON-TOOL-07](tools.md#con-tool-07) | 必须 | 二进制或超过 10 MiB 的 `fs.read` 拒绝 |
| [CON-TOOL-08](tools.md#con-tool-08) | 必须 | `fs.write` / `fs.edit` 用临时文件 + rename |
| [CON-TOOL-09](tools.md#con-tool-09) | 必须 | `fs.edit` 的 `old_string` 不唯一则不修改 |
| [CON-TOOL-10](tools.md#con-tool-10) | 必须 | 后端工具参数与返回原样透传 |
| [CON-TOOL-11](tools.md#con-tool-11) | 必须 | exclusive 调用携带 `_lease_token`，转发前剥离 |

## 并发与资源

| 编号 | 级别 | 陈述 |
| --- | --- | --- |
| [CON-CONC-01](concurrency.md#con-conc-01) | 禁止 | 跨 PTY、exec、后端、文件的全局大锁 |
| [CON-CONC-02](concurrency.md#con-conc-02) | 必须 | 配额、租约、owner 都键入 `caller_id` |
| [CON-CONC-03](concurrency.md#con-conc-03) | 必须 | `pty.open` 原子检查并递增两级计数 |
| [CON-CONC-04](concurrency.md#con-conc-04) | 必须 | 非 owner 对 PTY 只读 |
| [CON-CONC-05](concurrency.md#con-conc-05) | 必须 | `pty.read` 对任何调用方 long-poll |
| [CON-CONC-06](concurrency.md#con-conc-06) | 必须 | 每个后端逻辑实例一个 actor 串行 |
| [CON-CONC-07](concurrency.md#con-conc-07) | 必须 | fencing token 单调递增，崩溃后旧 token 作废 |
| [CON-CONC-08](concurrency.md#con-conc-08) | 必须 | 租约只在进程内存，重启即清空 |
| [CON-CONC-09](concurrency.md#con-conc-09) | 必须 | 幂等缓存有上界，默认 200，按 caller 隔离 |
| [CON-CONC-10](concurrency.md#con-conc-10) | 必须 | exclusive exec 等 shared 排空后执行 |
| [CON-CONC-11](concurrency.md#con-conc-11) | 必须 | MCP 会话关闭时 Orphan 该 caller 的全部 PTY |
| [CON-CONC-12](concurrency.md#con-conc-12) | 必须 | 后端连接上重映射 JSON-RPC id 与 progressToken |

## 安全

| 编号 | 级别 | 陈述 |
| --- | --- | --- |
| [CON-SEC-01](security.md#con-sec-01) | 必须 | 双层授权都通过才执行 |
| [CON-SEC-02](security.md#con-sec-02) | 必须 | 公网按 OAuth scope 过滤，tailnet 模式可按节点身份 |
| [CON-SEC-03](security.md#con-sec-03) | 必须 | Agent 用 WhoIs 的节点与 tag 做授权 |
| [CON-SEC-04](security.md#con-sec-04) | 禁止 | 把 Agent 凭证、SSH key、云凭证传入子进程 env |
| [CON-SEC-05](security.md#con-sec-05) | 禁止 | 用 cwd 策略拦截 PTY 里面的 `cd` |
| [CON-SEC-06](security.md#con-sec-06) | 必须 | 修改 `agent.policy` 无需重启 |
| [CON-SEC-07](security.md#con-sec-07) | 禁止 | 录制 PTY 字节流 |
| [CON-SEC-08](security.md#con-sec-08) | 禁止 | 审计日志记录文件内容或命令输出正文 |
| [CON-SEC-09](security.md#con-sec-09) | 必须 | 入口实现 OAuth 2.1、动态客户端注册和 PKCE |
| [CON-SEC-10](security.md#con-sec-10) | 必须 | 默认拦截递归删除、命令黑名单和超配额；越界写由路径解析以 `invalid_cwd` 拒绝。黑名单匹配规范化命令行 |
| [CON-SEC-11](security.md#con-sec-11) | 必须 | 入口的动态注册和登录口令必须限速 |

## 实现

| 编号 | 级别 | 陈述 |
| --- | --- | --- |
| [CON-IMPL-01](implementation.md#con-impl-01) | 必须 | 按 cwd、fs、pty、exec、mcpmux 与后端、mesh、gateway 的顺序交付 |
| [CON-IMPL-02](implementation.md#con-impl-02) | 必须 | Gateway、Ingress、后端转发共用 `mcpmux` |
| [CON-IMPL-03](implementation.md#con-impl-03) | 必须 | PTY 的 map 锁与 ring 锁分离，读写不互斥 |
| [CON-IMPL-04](implementation.md#con-impl-04) | 必须 | Windows 默认 `pwsh`；WSL 注册为独立设备 |
| [CON-IMPL-05](implementation.md#con-impl-05) | 必须 | `fs.grep` 优先 `rg --json`，否则纯 Go，结果同构 |

## 控制面管理

| 编号 | 级别 | 陈述 |
| --- | --- | --- |
| [CON-MESH-01](meshadmin.md#con-mesh-01) | 禁止 | 内核、Gateway、Agent 引用 `meshadmin` 或持有管理凭据；两个控制面的差异只在 Provider 里 |
| [CON-MESH-02](meshadmin.md#con-mesh-02) | 禁止 | 管理凭据出现在配置、命令行参数、日志、输出里；只存钥匙串或 `0600` 文件 |
| [CON-MESH-03](meshadmin.md#con-mesh-03) | 必须 | 写策略默认只预览；`--apply` 才写，写前快照、带版本、可回滚 |
| [CON-MESH-04](meshadmin.md#con-mesh-04) | 必须 | 只改带 `xmesh:managed` 标记的策略元素，其余字节原样保留 |
| [CON-MESH-05](meshadmin.md#con-mesh-05) | 必须 | 自建 DERP region id 在 900 到 999；写入后探测，失败即回滚 |
| [CON-MESH-06](meshadmin.md#con-mesh-06) | 必须 | DERP 准入对放行名单失败即关闭；管理端点需要 bearer token |
