# 14. 连接密码配对与内网发现

本篇记录 `lan` 模式下设备配对的新主路径：每台设备设置一个连接密码，另一台设备输入这个密码即可直接配对；内网里探测到的、尚未配对的设备在面板上列出，可以一键连接。配对文档（`xmesh pair --export` / `--import`）保留，作为高级用法和命令行后备。本篇是方案，实现分在同一个分支的几次提交里，契约版本升到 `1.4`（`1.2` 已被网关地址占用，`1.3` 已被工具授权页占用）。

日期：2026-10-17。基线：master `7a1b079`。

## 目标与非目标

目标：

- 用户在面板上为本机设置或修改「连接密码」，不需要终端，也不需要手工编辑 YAML。
- 在设备 A 上输入设备 B 的连接密码，密码正确时 A 与 B 互相信任，不需要再点确认按钮。
- 手动添加（输入对端地址 + 密码）与自动发现（内网探测到的设备 + 密码）走同一个协议。
- 探测到未配对的设备时，面板提示可以连接。
- 密码不能被离线暴力破解，也不能被中间人利用；内核只保存派生值，不保存明文。

非目标：

- 不改 tailnet 模式。配对只在 `lan` 模式可用，与现在一致。
- 不做一次性配对码或扫码。
- 不做多设备共享一个密码的管理，每台设备一个密码。
- 不把配对文档方式删掉；它仍是 CLI 与控制 API 的高级后备。

## 用户流程

1. 在设备 B 的面板「配对」页设置连接密码（至少 10 个字符）。
2. 在设备 A 的面板「发现的设备」里看到 B，点「连接」，输入 B 的连接密码。
3. 或者在 A 上「手动添加」，填 B 的地址与密码。
4. 密码正确：A 与 B 同时把对方的证书钉入信任库，标签为 `tag:peer`、`tag:device`，设备列表立即出现对方，无需重启。
5. 密码错误：A 显示「连接密码不正确，或对端不是待配对的 xmesh 设备」。B 记一次失败，连续失败会被限流（见下）。

## 协议：SPAKE2 over ristretto255

密码不离开设备，网络上只传与密码有关的盲化群元素。采用 SPAKE2（RFC 9382 的结构），群是 ristretto255，实现用 `github.com/cloudflare/circl/group`（RFC 9380 哈希到群，标量与点运算）。不自己写群运算。

M、N 两个固定点由 `HashToElement` 从两个域分隔字符串派生，离散对数未知。这是 RFC 9382 里 M、N 常量的替代，安全性论证不变。输出不与其他 SPAKE2 实现互通，这是有意的：配对只在 xmesh 之间进行。

**密码派生。** B 在设置密码时生成 16 字节随机盐 `salt`，计算

```
key = Argon2id(password, salt, t=3, m=64 MiB, p=4, 长度 64)
w0  = HashToScalar(key[0:32],  "xmesh-pair-w0")
w1  = HashToScalar(key[32:64], "xmesh-pair-w1")
```

B 只保存 `salt` 与 `key`。`key` 是派生值，不是明文；但它与密码等价：拿到它的人可以冒充这台设备与他人配对。这与配对依赖的节点私钥（`lan/node.key`）同处一个状态目录，威胁模型上没有新增风险。网络上的攻击者拿到的只是对话记录，只能对密码做在线猜测，每次猜测都要一次完整往返并计入限流。

**三轮消息。** 盐必须先于 A 的第一个群元素到达 A，所以多一轮。

```
A（输入密码的一方）                           B（设置了密码的一方）
--------------------------------------------------------------------------
POST /lan/pair/challenge  {}          ->
                                      <-  {name: B, salt, kdf: "argon2id-v1"}
A 计算 w0、w1，选 x，X = x·G + w0·M
POST /lan/pair/exchange   {X, certA}  ->
                                      B 计数一次尝试（见限流）
                                      选 y，Y = y·G + w1·N
                                      Z = y·(X − w0·M)
                                      TT = transcript(…, certA, certB)
                                      <-  {sid, Y, certB, confirmB}
A 计算 Z = x·(Y − w1·N)
A 算 TT，核对 confirmB；不符则失败（密码错或中间人）
A 计算 confirmA
POST /lan/pair/confirm    {sid, confirmA} ->
                                      B 核对 confirmA，成功则钉入 A，重置限流
                                      <-  204
A 核对 confirm 成功后钉入 B
```

**转录与确认码。** `TT` 把下列字段依次用 4 字节大端长度前缀拼接，然后进入 MAC：`"xmesh-pair/v1"`、`w0`、`w1`、`M`、`N`、`X`、`Y`、`Z`、A 的证书（DER）、B 的证书（DER）。两份证书都进入转录，所以 MAC 同时认证了双方要钉入的身份，中间人无法替换证书而不被发现。

```
K        = SHA-256(TT)
confirmA = HMAC-SHA256(K, "A" || TT)
confirmB = HMAC-SHA256(K, "B" || TT)
```

比较全部用 `hmac.Equal`。元素必须能解码为有效的 ristretto255 点，且不是单位元；`Y`、`X` 为单位元时拒绝。

**为什么 TLS 层仍然不做身份认证。** 配对连接的 TLS 只提供加密，证书不校验（`InsecureSkipVerify`），身份完全由 PAKE 的确认码提供。配对之后的所有连接仍然走严格的 mTLS，证书钉在信任库里。

## CON-NET-09 修订（需要确认）

[CON-NET-09](../constraints/network.md#con-net-09) 要求「未配对的对端连接必须在 TLS 握手阶段失败，而不是连上后再用应用层口令补救」。连接密码配对要求未配对的设备能完成一次握手并交换 PAKE 消息，所以这条约束必须修订。修订后的规则：

- 主监听器对未配对证书的握手仍然失败。`Test_CONNET09_PairedMTLS` 继续断言这一点。
- 只有客户端在 ALPN 里声明 `xmesh-pair/1` 的握手，才会换用一个只请求客户端证书、不做信任校验的 TLS 配置。`GetConfigForClient` 按 ALPN 选择配置，普通客户端的握手路径不变。服务端不在 ALPN 里选中 `xmesh-pair/1`：`net/http` 遇到未知的应用层协议会直接断开，所以服务端不声明它。
- 请求的路由由信任决定，不由 ALPN 决定。`/lan/pair/*` 对任何完成的握手开放，它的安全来自 PAKE 与限流。其余路径要求客户端证书在信任库中，否则返回 404，所以未配对的握手只能到达配对路由。
- 配对是唯一的应用层口令路径，它的安全来自 PAKE，而不是口令本身；限流见下。

这一修订在 PR 中单独成一个提交，并同步更新 `docs/constraints/network.md` 与 `docs/conformance.md`。**它改变了一条已有的 MUST，需要项目负责人确认。** 不确认则只能退回到配对文档方式，连接密码功能不能上线。

监听端口仍是 TCP 7433，不新增端口，所以防火墙规则不变。

## 限流与锁定

限流在 B 上计数，因为 A 在第二轮答错密码时就会中止，B 只能从收到的 `exchange` 数量判断尝试次数。

- 每次 `exchange` 计为一次尝试。`confirm` 成功后清零。
- 连续 5 次未成功尝试后锁定。锁定时长 30 秒起，每多一次翻倍，上限 30 分钟。
- 锁定期间 `exchange` 返回 HTTP 429，错误码 `rate_limited`，`retry_after_ms` 为剩余时间。
- 限流是节点级的，不按来源地址。局域网上的攻击者可以让正在配对的用户等待，但不能冒充设备或猜出密码。
- 待完成的会话最多 16 个，每个 60 秒后过期。超过时淘汰最早的。
- 面板的连接密码状态显示 `failed_attempts` 与 `locked_until`，用户能看到是否正在被猜测。

密码长度 10 到 128 个字符，首尾不能有空白。这比配对文档更严，因为密码是人输入的；在线猜测的上限由限流控制。

## 存储

`<状态目录>/lan/pair-password.json`，`0600`，原子写入（与 `peers.json` 同一个 `writePrivate`）：

```json
{ "version": 1, "salt": "<base64, 16 字节>", "key": "<hex, 64 字节>", "kdf": "argon2id-v1", "updated_at": "2026-10-17T15:00:00Z" }
```

文件不存在即表示未设置密码，此时 `challenge` 返回 `pair_rejected`（原因 `no_password`），设备不会在发现列表里被标为可配对。失败尝试计数只在内存里，重启后清零；这不削弱限流，因为重启需要本机权限。

## 发现

LAN 模式本来就强制开启 mDNS（[config.schema.json](../contracts/schemas/config.schema.json)）。节点在 TXT 记录里增加 `pairing=1`，表示它实现了连接密码配对协议，且 `fingerprint=` 不变。

`discover()` 目前只处理已信任的对端，并忽略其余条目。改动：未信任、带 `pairing=1`、指纹不是本机的条目记入 `found` 表，按 TTL（上限 30 秒）过期，收到 goodbye 时删除。已信任的对端不出现在这个列表里。

`pairing=1` 不表示该设备已经设置了密码。发现列表不探测对端的密码状态，避免额外的未认证请求；用户点击连接后，`challenge` 的 `no_password` 会显示为「对端尚未设置连接密码」。

由于 TXT 在 `Advertise` 时固定，设置或清除密码不需要重新广播。

## 控制 API 1.4

契约版本 `1.3` → `1.4`。`1.2` 已被 #73（网关地址）占用，`1.3` 已被 #72（工具授权页）占用，所以本篇的路由落在 `1.4`。只增加路由、字段、错误码与能力值，不改现有路由。能力 `pair` 覆盖新路由。

| 路由 | 请求 | 响应 | 说明 |
| --- | --- | --- | --- |
| `GET /v1/pair/password` | — | `PairPasswordState` | `set`、`updated_at`、`failed_attempts`、`locked_until`。从不返回口令或派生值 |
| `PUT /v1/pair/password` | `PairPasswordRequest {password}` | `PairPasswordState` | 设置或修改。立即生效，清除锁定 |
| `DELETE /v1/pair/password` | — | `204` | 清除密码，关闭被动配对 |
| `GET /v1/pair/discovered` | — | `PairDiscoveredResponse {devices[]}` | 每项 `name`、`fingerprint`、`address`、`seen_at`。不含证书 |
| `POST /v1/pair/connect` | `PairConnectRequest {address?, fingerprint?, password}` | `PairConnectResponse {name, fingerprint, address, tags}` | `address` 或 `fingerprint` 至少给一个；只给 `fingerprint` 时地址取自发现列表，找不到返回 `not_found`；同时给出时对端证书与 `fingerprint` 不符则在确认前停止，返回 `invalid_args`（字段 `fingerprint`，`reason` 为 `fingerprint_mismatch`）。成功即生效，无需重启 |

`PairPasswordRequest.password` 的长度与空白规则与上面一致，违反返回 `invalid_args`，字段为 `password`。

新增错误码（[CTR-ERR-01](../contracts/errors.md#ctr-err-01) 的封闭集合，增加两项）：

| 错误码 | HTTP | 用途 | `reason` |
| --- | --- | --- | --- |
| `pair_rejected` | 422 | 配对被对端或本机拒绝 | `wrong_password`、`no_password`、`unsupported_peer`、`unreachable`、`bad_peer` |
| `rate_limited` | 429 | 对端正在限流 | 无；`retry_after_ms` 必填 |

`unsupported_peer` 表示对端没有 `/lan/pair/*`，多半是旧版本。`unreachable` 表示 TCP 或 TLS 失败。`bad_peer` 表示对端返回的消息格式不对或证书无效。`wrong_password` 与中间人攻击在客户端无法区分，错误消息对两者给同一句话。

`POST /v1/pair/export` 与 `POST /v1/pair/import` 不变。导入之后仍需重启。

## 对端路由

以下路由挂在 agent 监听器上，路径前缀 `/lan/pair/`。它们对任何完成的握手开放，已配对的对端也能访问；未配对的对端只能访问这三个路由：

- `POST /lan/pair/challenge`：返回对端名、盐与 KDF 标识。未设置密码时返回 `pair_rejected` 原因 `no_password`，HTTP 409。这一步不计入限流。
- `POST /lan/pair/exchange`：计入限流；返回 `sid`、`Y`、B 的证书与 `confirmB`。
- `POST /lan/pair/confirm`：校验 `confirmA`，成功则钉入 A，返回 204。

请求与响应都是 JSON，体积上限 16 KiB。证书字段是 PEM。

## 兼容性

- 旧版本（beta.2 及更早）的节点没有 `pairing=1`，不出现在发现列表里；手动输入它们的地址时，旧版本的握手会因为证书不在信任库中而在 TLS 层失败，UI 显示「无法连接到对端」，并提示对端可能需要升级。
- 旧版本节点之间仍可用配对文档互相导入。
- 本版本的节点仍可与旧版本的已配对节点做 mTLS，信任库格式没有变化。

## 配置与文件

- 不新增配置项。密码状态只在 `lan/pair-password.json` 里。
- `mesh.lan.mdns` 仍是 LAN 模式的强制项。
- 不改变 `agent` 角色的启用条件；配对只在运行 agent 的节点上可用，与 `/mcp` 的监听器相同。

## CLI

`xmesh control pair` 增加子命令，与控制 API 一一对应：

```
xmesh control pair password show
xmesh control pair password set (--password-stdin | --password-file 文件)
xmesh control pair password clear
xmesh control pair discover
xmesh control pair connect (--address 地址 | --fingerprint 指纹) (--password-stdin | --password-file 文件)
```

口令不接受 `--password` 参数：命令行参数会出现在进程列表与 shell 历史里。`--password-file` 读取后去掉末尾换行。旧的 `xmesh pair --export/--import` 保持原样。

## 面板

「配对」页分为：

1. 本机连接密码：状态（已设置 / 未设置）、设置或修改表单（两次输入一致）、清除按钮、`failed_attempts` 与锁定时间的提示。
2. 发现的设备：列表，每项有「连接」按钮，点击后弹出口令输入框，显示对端名与指纹。空时显示「内网中暂未发现其他设备」。
3. 手动添加：地址、口令、提交。
4. 高级：导出配对文档、导入配对文档（原有的指纹核对流程不变）。

「设备」页的列表不变。

## 测试计划

Go：

- `internal/spake2`：双方一致的会话；错误密码失败；替换任一证书失败；替换任一群元素失败；单位元拒绝；确认码篡改失败；同一密码不同盐的派生不同。
- `internal/mesh`：盐与派生值的文件格式；未设置时的 `no_password`；限流的计数、锁定时长与清零；`exchange` 超过待定会话上限；两个进程内的 LAN 实例完成配对后互相可见（`tag:device`），且无需重启；未配对证书在普通 ALPN 下仍握手失败（`Test_CONNET09_PairedMTLS` 保持不变）；未配对证书访问 `/mcp` 返回 404，`/lan/pair/challenge` 可达。
- `internal/control`：新路由的成功与错误路径；`PUT` 的口令规则；`connect` 的 `fingerprint` 核对；`unsupported_mode`。
- 契约：`check_control.py` 对每个新路由定义的正例与反例；`control-routes.json` 与内核注册的路由一致。

UI：

- 单元：口令表单的校验、发现列表的排序与空状态、连接错误码到文案的映射、`PairConnectRequest` 的解析器只收合法字段。
- Playwright（假内核）：设置密码、发现列表、连接成功、连接失败与限流提示、手动添加、高级导入仍可用。

人工：在 Mac 与 Windows 两台真机上做一次双向配对、断网后重连、错误密码、限流，结果写进 `docs/validation/` 下的运行手册。

## 决策记录

- 协议：SPAKE2 over ristretto255（circl），不使用 OPAQUE。原因：配对是对等的，两端都要把对方当作设备信任，不需要注册服务端。
- KDF：Argon2id，参数同 RFC 9106 的第二推荐，盐由被配对的一方生成。
- 密码长度下限 10 个字符。
- 限流节点级，不按来源。
- 设置密码后立即生效，不需要重启。
- 不新增端口。
- CON-NET-09 修订需要确认（见上）。
