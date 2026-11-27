# 云端 Claude → ubuntu-cloud ingress → Mac 的鉴权链路

只读梳理，基于 tzf1003/xmesh main（9e5d784）。每条标「代码确认」或「推断」。

## 一句话
一次调用要连过 4 道关：①OAuth 令牌与 scope（ingress 上）→ ②设备发现与就绪（ingress 的 gateway）→ ③网络准入与身份（ingress→Mac:7433）→ ④Mac 上的 agent.authz / disabled_tools / 本机策略。
Mac 的「stale、工具表为空」卡在第 ④ 关的 sys.info（见下「关键机制」）。

## 链路与每一步的检查

### ① 云端 → ingress（身份：OAuth client + scope）
| 检查 | 失败表现 | 位置 |
|---|---|---|
| Host/Origin 必须是 issuer | 421 misdirected_request | internal/oauth/server.go:201,241 |
| Bearer 令牌有效、未过期（访问令牌 1 小时） | 401 invalid_token | server.go:645,263 |
| 令牌只存内存，ingress 重启后要重新登录 | 401，重新授权 | internal/app/ingress_role.go 注释 |
| 授权时请求的 scope 必须是 client 允许 scope 的子集；动态注册的 client 上限是 ingress.oauth.dynamic_client_scopes | 400 invalid_scope | server.go:471,548；config.go:157 |
| 同一 MCP 会话身份（client/scope）不可变 | 403 会话身份不匹配 | internal/role/gateway/gateway.go:~215 |
| 每次 tools/call 按 scope 检查：devices.list 等要 `devices:read`；sys.* 要 `sys:<设备>`；exec.*→`exec:<设备>`；fs.*→`fs:read|write:<设备>`；pty.*→`pty:<设备>` | 工具返回 unauthorized「scope 不允许此工具」；tools/list 里直接看不到 | internal/oauth/scopes.go:Allows；gateway.go:~420 |

注意：scope 里的设备名必须和设备名完全一致（或 `*`），`sys:xiaosong-mac` 与 `exec:*` 是两回事。以上均为代码确认。

### ② ingress 的 gateway：发现设备并等就绪
- 发现：只收 online、未过期、带 `tag:device`、有 IP 的 peer（tailnet）；名字须合法。没出现在 devices.list = 这一步没过。（internal/mesh/tailnet.go:Peers；gateway.go:validName）代码确认
- 调用前：设备不在线 → `not_found 设备不在线`；设备「能力尚未就绪」→ `not_found 设备能力尚未就绪`。（gateway.go:~430）代码确认
- 「就绪」的定义：该设备的 tools/list **和** sys.info 两个都成功，才会把工具表写入缓存（refreshPeer，gateway.go:~620）。sys.info 失败时 d.tools 保持空，devices.list 该行 `stale: true`（control.go devices.list；`stale` = 从没成功读过 info，或失败持续中）。代码确认

### ③ ingress → Mac（网络准入 + 身份）
- 网络层：控制面 ACL 必须放行 `tag:ingress → tag:device:7433`。personal/team 模板放行；restricted 模板要求 Mac 另带 `tag:ingress-target`。（meshadmin/template.go:BuildTemplate）代码确认
- Mac 端 Agent 通过 `WhoIs(RemoteAddr)` 取对端节点 tags（tailnet；internal/mesh/tailnet.go:Authenticate），失败 `unauthenticated: WhoIs 无法识别对端`。配对（LAN/device link）模式则用本机信任记录里授予的 tags（internal/mesh/lan.go:419；internal/connectivity/node.go:215）。代码确认
- ingress 节点带 `tag:ingress`，Agent 要求请求头 `X-Xmesh-Oauth-Client`，否则 401「入口会话缺少 OAuth client 身份」（internal/identity/identity.go:FromPeer）。云端会话的拨号会带上这个头（gateway.go:headerTransport）。代码确认
- 配对模式下，带 OAuth client 的会话，tags 会被**收窄为只有 `tag:ingress`**，且对端必须本来就有 tag:ingress，否则 `unauthorized` 被拒（internal/devicelink/carrier.go:delegatedPrincipal）。因此 Mac 上给 `tag:peer` / `tag:device` 的授权对云端来的调用**不生效**。代码确认

### ④ Mac 上的 Agent
按顺序（internal/role/agent/agent.go:installBuiltins）：
1. 计算机/桌面开关、`agent.disabled_tools` → `policy_denied 工具已在配置中关闭`，并且这个工具从 tools/list 消失。
2. 节点正在下线（drain）→ `policy_denied mesh.draining`（仅 exec.run / pty.open / backend.acquire / computer.open|act）。
3. **agent.authz**：键是 tag（`path.Match`，`tag:*` 匹配任意），按调用方 tags 取并集，没有匹配的键＝拒绝；检查 `tools` 列表（backend 另查 `backends`）→ `unauthorized 调用方的 tag 不允许此工具`。tools/list 也按同一规则过滤（agent.go:~575，backends.go:toolAllowed）。
4. 参数校验、cwd/workspaces、policy（allow_dangerous、deny 文件、沙箱）→ `policy_denied`/`args` 类错误。
5. 预算：每 OAuth client 每小时 10000 次等；`sys.info` 免计数（agent.go quota 段）。
以上代码确认。

## 关键机制：Mac 的 stale 是怎么来的
sys.info 本身也是一个工具，也要过第 ④ 关的 agent.authz。若 Mac 的 `agent.authz` 里没有任何键匹配 `tag:ingress`（或没把 `sys.info` 放进该键的 tools），则：tools/list 返回空、sys.info 返回 unauthorized → gateway 认为设备没就绪 → `stale: true`、工具表空、调用报「设备能力尚未就绪」。
这与「ubuntu-cloud 改了授权后通了、Mac 仍 stale」吻合（推断：Mac 上还没做同样的授权，或授权键写成了 tag:peer）。PR #130（默认 `tag:*`）解决的就是这一关，这里不重复。

## 一个可能造成误判的点（推断，未实测）
面板里的「读取失败」来自 gateway 的后台 sys.info 读取（internal/role/gateway/deviceinfo.go:sysInfo），它**不带 OAuth client**（`Principal{}`）。发起方 ubuntu-cloud 带 `tag:ingress`，按 FromPeer 规则，Mac 会回「入口会话缺少 OAuth client 身份」。所以：
- ubuntu-cloud 面板上看到的 Mac「读取失败」，可能是这个后台读取的问题，不一定等于云端 Claude 真实调用的失败原因。
- 以云端 Claude 真实调用的结果（devices.list 的 stale、tools.search、具体报错）和 Mac 上的审计为准。
- 该后台读取的错误文案只保留在面板（sys.info 返回 IsError 时只显示泛化的「sys.info 暂不可用」，不含具体 code）。

## 排查清单（按顺序，每项：在哪看 → 怎么判断）
1. **能连上 ingress 且令牌有效**：云端 `devices.list` 能返回。返回 401/421 → 查 OAuth（issuer 域名、重新授权；ingress 重启过也会要求重登）。
2. **scope 够不够**：云端 `tools.search`/调用 Mac 工具得到 `unauthorized scope 不允许此工具` → 看 ingress 面板「公网入口 / OAuth clients」里该 client 的 scope，需要 `devices:read` 和 `sys:xiaosong-mac`（或 `sys:*`）等，设备名要写对。
3. **Mac 是否被发现**：devices.list 有没有 xiaosong-mac、online 是否 true。没有 → Mac 缺 `tag:device`、离线或名字不合法。面板「设备」页点开该设备看 tags（缺 tag:device 会有警告标记）。
4. **网络 ACL**：面板「Tailscale 管理 → 权限矩阵」（或 `xmesh mesh matrix`）看 ubuntu-cloud → xiaosong-mac 的 Network 列是否 allow；restricted 模板还要有 tag:ingress-target。矩阵里「network allows … but agent.authz has no usable entry」就是第 ⑤ 项问题。
5. **Mac 的 agent.authz（最可能卡点）**：Mac 的「访问与授权」页（或 xmesh.yaml 的 `agent.authz`）。要有键匹配 `tag:ingress`（或 `tag:*`），且 tools 含 `sys.info` 以及要用的 exec.*/fs.*/pty.*。配对模式下云端来的调用只认 `tag:ingress`。判断：devices.list 里 xiaosong-mac `stale: true` 且 tools.search 为空 ＝ 基本就是这一项。
6. **Mac 的 disabled_tools**：配置里 `agent.disabled_tools` 是否关了 sys.info 等；被关的工具在 tools/list 里不存在，调用得到 `policy_denied tool.disabled`。
7. **看 Mac 的审计**：Mac 面板「审计」页，找 caller 形如 `<ingress 节点>/<会话>`、tool=sys.info 的记录，status/error 能区分 unauthorized（authz）与 policy_denied（disabled/策略）。完全没有记录＝没到第 ④ 关，回头查 ③（ACL、tag、OAuth client 头）。
8. **最后看本机策略**：exec/fs 失败再看 Mac 的 policy、workspaces、cwd 限制、沙箱，错误是 `policy_denied`，不是鉴权问题。

## 面板位置速查
设备页（设备详情的 tags 与 info 读取状态）、拓扑页（读取失败标记）、访问与授权（agent.authz）、公网入口（OAuth client/scope）、Tailscale 管理 → 权限矩阵、审计。
