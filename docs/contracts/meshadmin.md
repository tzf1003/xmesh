# meshadmin

`meshadmin` 是控制面管理客户端库（根 module 的公开包 `github.com/tzf1003/xmesh/meshadmin`，不依赖 `internal/`）。它的入口是 `xmesh mesh ... --json`，桌面 UI 的 Electron 主进程调用该子命令，不重写一份。内核、Gateway、Agent 不引用它，也不持有它用的凭据，见 [CON-MESH-01](../constraints/meshadmin.md#con-mesh-01)。

方案与研究记录见 [13 tailnet 管理与 DERP 提供商路径](../design/13-mesh-admin-and-derp.md)。本篇是行为契约。

## CTR-MESH-01

### Provider

两个实现，行为用同一组场景验收（见 [conformance](../conformance.md)）：

| Provider | 控制面 | 接口 | 核实版本 |
| --- | --- | --- | --- |
| `tailscale` | 官方 Tailscale | API v2：`/tailnet/{t}/devices`、`/device/{id}/tags`、`/tailnet/{t}/keys`、`/tailnet/{t}/acl`（`ETag` / `If-Match`）、`/acl/validate` | 对照官方 Go 客户端的请求形状，用夹具测试；未对真实 tailnet 联调 |
| `headscale` | Headscale | REST `/api/v1`：`/node`、`/node/{id}/tags`、`/preauthkey`、`/user`、`/policy`（GET、PUT）、`/policy/check` | v0.29.5，用真实二进制做集成测试（`policy.mode=database`） |

接口（Go）：`Devices`、`SetTags`、`CreateAuthKey`、`GetPolicy`、`ValidatePolicy`、`SetPolicy(text, version)`、`Capabilities`。`Capabilities` 说明此刻能做什么：

| 字段 | 含义 |
| --- | --- |
| `policy_write` | 能否远程写策略。Headscale `policy.mode=file` 时为 `false`：服务器拒绝 `PUT /policy`（`update is disabled for modes other than 'database'`），只能生成文本交给运维 |
| `atomic_policy_write` | 写入时由控制面检查版本。Tailscale 为真（`If-Match`）；Headscale 为假，见 [RUL-21](rulings.md#rul-21) |
| `derp_via_policy` | DERP 地域写进策略（Tailscale `derpMap`）；Headscale 为假，DERP 是服务器配置 |

错误按类分，调用方用 `errors.Is`：

| 错误 | 条件 |
| --- | --- |
| `ErrUnauthorized`（`ScopeError`） | 凭据被拒（HTTP 401、403）。Tailscale 的 `ScopeError` 带所需 OAuth scope，例如 `devices:core`、`policy_file`、`auth_keys` |
| `ErrConflict` | 策略版本已变（Tailscale 412；Headscale 客户端比对） |
| `ErrInvalid`（`ValidationError`、`InvalidError`） | 输入或策略未通过校验，带控制面原话 |
| `ErrNotFound`、`ErrUnsupported`、`ErrNotConfirmed` | 对象不存在；该控制面或模式做不到；写入未确认 |

### 连接约束

- 控制面地址必须是 `https`；`http` 只允许环回地址（Headscale 本机验收）。不跟随重定向，凭据不会转发到另一个源。
- 响应体上限 8 MiB。错误信息不含凭据。

### Headscale 的 tag 与预授权 key（已核实）

- 改 tag：`POST /api/v1/node/{id}/tags`，体为 `{"tags": [...]}`，返回新节点。tag 须在策略 `tagOwners` 里有属主，否则服务器拒绝。
- 预授权 key：`POST /api/v1/preauthkey`，体含 `user`（数字 ID）、`reusable`、`ephemeral`、`expiration`、`aclTags`。`user` 由名字解析，只有一个用户时可省略。key 正文只在创建响应出现一次，列表接口返回的是带 `***` 的前缀。
- 策略：数据库模式下 `GET`、`PUT /api/v1/policy` 与 `POST /api/v1/policy/check` 可用；没有存过策略时 `GET` 返回 500 `acl policy not found`，按「没有策略」处理。`updatedAt` 在文件模式为 `null`，用来判定模式。

## CTR-MESH-02

### 凭据

桌面凭据文件保存一份当前控制面的凭据，包含 `access`。读写凭据覆盖查看与修改，选择只读时 CLI 拒绝写操作；OAuth 请求使用保存的 access 对应 scope。旧版未包含 `access` 的文件保持兼容，由控制面决定权限。

CLI 的旧有系统钥匙串 profile 保留 `read`、`write` 两份凭据；读操作优先 `read`、回退 `write`，写操作使用 `write`。

| 控制面 | 类型 | 存储内容 | scope |
| --- | --- | --- | --- |
| Tailscale | `oauth` | client id 与 secret，换取短期 token | read：`devices:core:read`、`policy_file:read`、`auth_keys:read`；write：`devices:core`、`policy_file`、`auth_keys` |
| Tailscale | `token` | API access token | 由签发时决定 |
| Headscale | `apikey` | API key | 无 scope，等同完整权限 |

存放位置有两种，二选一：

- **`0600` 凭据文件**（桌面面板默认）：`--credential-file <路径>`，内容是 `Credential` 的 JSON。`mesh login` 与 `mesh logout` 都认这个参数（`--panel` 自动选择桌面凭据文件），写入是原子替换，在 Unix 上不得对 group 或 other 可读（`0600`；Windows 不检查 mode）。没有桌面会话的 Linux 也照常工作。
- **系统钥匙串**：服务名 `xmesh-meshadmin`，账户 `<profile>/<read|write>`（Windows 凭据管理器、macOS 钥匙串、Linux Secret Service）。不给 `--credential-file` 时才用。

两种方式下密钥都只从标准输入、`--secret-file` 或终端无回显输入读取，不接受命令行参数；`mesh status --json` 只回报 `access`、`profile`、`source` 与文件路径，不回报密钥。`Credential` 的 `String()` 与 `GoString()` 不输出密钥。

### 策略写入协议

所有写策略的操作（模板、DERP、回滚）走同一条路径：

1. 读当前策略和版本。
2. 在内存里生成新文本（见下）。
3. 交控制面校验（Tailscale `/acl/validate`；Headscale `/policy/check`）。失败则不写，返回控制面的原话。
4. 返回预览：`before`、`after`、统一 diff、`base_version`、`changed`、`warnings`、`writable`。
5. `--apply`（库里是 `Apply(confirmed=true)`）才写。写前把旧文本存为本机快照（目录 `0700`，文件 `0600`，id 形如 `20261016T153000Z-1a2b3c4d`）。
6. 带版本写入；版本已变得到 `ErrConflict`，不覆盖别人的修改。调用方可传 `--expect-version <base_version>`，保证写入的正是用户预览过的那一版。
7. 回滚读取快照，走同一条路径写回，并给被替换的策略再存一份快照，因此回滚可以再回滚。

### 只改自己管理的片段

xmesh 写入的每个元素前带一行注释 `// xmesh:managed <section>`。策略是带注释的 HuJSON，编辑在语法树上进行：

- 只增删带标记的元素；其余字节（注释、空白、键顺序、尾逗号）原样保留。
- 重复应用同一内容得到相同文本；应用空片段会移除全部带标记的元素，并还原原有文本。
- 用户自己写的 `tagOwners` 项与 xmesh 同名时，保留用户的，并返回警告。
- 用户自己写的、与 xmesh 同号的 DERP region 阻止写入，返回 `ErrInvalid`。

可管理的段：`tagOwners`、`acls`、`derpMap`。

### 权限模板

`xmesh mesh policy --template <name> --owner <属主>`。属主是 `tagOwners` 的值：`autogroup:admin`、`group:x`、`tag:x` 或 `user@`。端口默认 7433（Agent 端口）。

| 模板 | tagOwners | 访问规则 | 用途 |
| --- | --- | --- | --- |
| `personal` | `tag:device`、`tag:peer`、`tag:ingress`、`tag:admin` | 四个 tag → `tag:device:7433` | 设备互通；入口与管理端只连设备 |
| `team` | 同上 | `tag:peer`、`tag:ingress`、`tag:admin` → `tag:device:7433` | 按角色隔离：只有调用方角色能发起连接，普通设备不能 |
| `restricted` | 上面加 `tag:ingress-target` | `tag:peer`、`tag:admin` → `tag:device:7433`；`tag:ingress` → `tag:ingress-target:7433` | 入口只能调用打了 `tag:ingress-target` 的设备 |

模板只写网络层。工具层仍由各节点的 `agent.authz` 决定，两层是否一致看权限矩阵。

## CTR-MESH-03

### 设备与 tag 检查

`xmesh mesh status` 列出控制面所有节点，按 tag 推断角色：`tag:device` → agent；`tag:ingress` → ingress；`tag:admin` → admin；`tag:peer`、`tag:cloud`、`tag:gw-*` → peer（调用方）。

给出 `--node-config xmesh.yaml`（可重复）时，再与该节点自己声明的 `node.roles` 对照。检查项：

| code | 级别 | 条件 |
| --- | --- | --- |
| `no-tags` | warning | 节点没有任何 tag，`agent.authz` 与网络策略都认不出它 |
| `unauthorized` | warning | 控制面尚未授权该节点 |
| `key-expired`、`key-expiring` | error、warning | 节点密钥已过期，或 7 天内过期 |
| `missing-role-tag` | error | 配置里 `roles` 含 agent、ingress、gateway，节点缺对应 tag（agent 缺 `tag:device` 就不会出现在发现里） |
| `unexpected-device-tag` | warning | 有 `tag:device`，配置里却没启用 agent 角色 |

改 tag（`xmesh mesh tags --device ID --set ... --apply`）前检查：tag 语法 `tag:<字母数字和->`，且策略 `tagOwners` 里有该 tag，否则拒绝并指出缺哪个。

### 新设备接入

`xmesh mesh onboard --tags tag:device --authkey-file F [--config xmesh.yaml --install]`：创建预授权 key（默认一次性、1 小时），写入 `F`（`0600`，不覆盖已有文件，除非 `--overwrite`），输出只含 key 的 ID、到期时间和文件路径。随后提示设置 `mesh.mode: tailnet` 与 `mesh.authkey_file`，`--install` 时调用 `xmesh install`。key 正文不进 stdout、日志或审计；`authkey --out -` 是唯一输出到标准输出的方式，由调用方负责。

### 权限矩阵

`xmesh mesh matrix` 对每个（来源，agent 目标）给出一格：

| 字段 | 含义 |
| --- | --- |
| `network` | `allow`、`deny`、`unknown`：控制面策略是否放行来源到目标的 tcp/7433 |
| `tools` | 目标节点 `agent.authz` 中，来源所带 tag 对应条目的并集是否非空；没有该节点的配置时为 `unknown` |
| `allowed_tools`、`backends` | 上述并集 |
| `mismatch` | 两层不一致：网络放行但工具层无条目，或工具层放行但网络层挡住 |

网络层按 `acls`（accept，tcp 或未指定协议）与 `grants` 计算，支持 `*`、`tag:`、`group:`、用户、`hosts` 别名、IP 与 CIDR、`autogroup:member`、`autogroup:tagged`、`autogroup:self`、端口列表与范围。遇到不能解析的别名（如 `autogroup:admin`）得到 `unknown`，不猜测。

## CTR-MESH-04

### DERP 地域

桌面「策略」与「DERP 中继」共用一份完整策略草稿。初次读取云端策略及版本后，
从草稿的 `derpMap` 派生已有地域列表，保留节点数组、扩展字段、注释和地域编号。
已有地域支持多个节点及只配置 IP 的节点。页面切换保留本次应用会话中的草稿；
关闭应用前提示待应用的修改。重新加载云端策略需要确认丢弃当前草稿。

桌面调用的文档命令：

- `xmesh mesh policy document --json`：标准输入为 `DocumentEdit` JSON，解析或编辑本地完整 HuJSON，不访问控制面。
  `region: {id,text}` 编辑已有地域的完整对象；`removeID` 删除明确选择的地域；
  `regions` 追加新地域，为未指定编号的地域分配空闲编号；`omitDefault` 修改官方中继开关；
  `template` 合并 xmesh 管理的模板段落。返回完整策略和派生地域列表。
- `xmesh mesh policy publish --json --expect-version VERSION`：标准输入仅为 `{"text":"完整策略"}`，
  根据草稿来源的版本校验并预览完整差异，`--apply --yes` 才提交同一份完整正文。
  写前保存快照，写入沿用 Provider 的版本条件，写后回读并比较策略内容。
  新增或修改连接参数的 DERP 节点执行真实探测；失败恢复前检查写后版本，
  避免覆盖其他管理员的后续修改。错误传回调用边界并保留桌面草稿。

显式文档编辑允许用户修改自己已有的地域及策略；自动模板和原 `derp plan/apply`
仍遵循 `xmesh:managed` 所有权规则。Headscale 的中继仍由服务端配置提供。

`xmesh mesh derp plan|apply --region "code=lab,name=Lab,host=derp.example.com,ipv4=...,ipv6=...,id=900"`。

- 官方控制面：写 `derpMap.regions`，region id 限 900 到 999，未指定时取最小的空闲 id（避开策略里已有的所有 id）；每个 region 一个节点，节点名 `<id>a`；写入 IPv4、IPv6 以抵御 DNS 失败；`--omit-default` 写 `omitDefaultRegions: true`。只有一个 region 且去掉官方 region 时给出单点警告。
- 写入后默认探测每个 region，探测失败自动用写前快照回滚，并以 `ErrProbeFailed` 退出。`--no-probe` 跳过。
- Headscale：DERP 是服务器配置，不经 API。`xmesh mesh derp headscale ...` 生成 `derp.paths` 指向的 YAML 文件和 `config.yaml` 片段（`--omit-default` 时 `derp.urls: []`）。文件由 Headscale 自己的加载器验收（集成测试用生成的文件启动 v0.29.5）。改后重启 Headscale。

### 探测

`xmesh mesh derp probe --region ...`，对每个 region 按客户端的用法检查，每项给出通过与否、原因和耗时：

| 检查 | 内容 |
| --- | --- |
| `tcp` | 连接配置的 IP（没有则主机名）的 DERP 端口（默认 443） |
| `tls` | 以主机名（或 `cert_name`）校验证书；证书与主机名不符时报 x509 的具体原因 |
| `http` | `GET /derp/probe` 返回 200 |
| `stun` | UDP 3478 上的 STUN binding 请求得到匹配响应；`--skip-stun` 跳过 |

`--compare-official` 另测官方 DERP 各 region 的 TCP 连接延迟作对照（尽力而为）。

## CTR-MESH-05

### DERP 准入

自建 derper 用 `--verify-client-url` 指向准入服务。请求与响应沿用上游的 `tailcfg.DERPAdmitClientRequest`（`NodePublic`、`Source`）与 `DERPAdmitClientResponse`（`Allow`），在 `tailscale.com` v1.104.1 的 `derpserver` 上验证：

- derper 对每个新连接 POST 一次，5 秒超时。非 200 或 `Allow=false` 拒绝；准入服务不可达时是否放行由 `--verify-client-url-fail-open` 决定，上游默认 `true`。xmesh 的运行手册要求设为 `false`。
- 准入只在连接建立时判断；已建立的连接要重连后才受影响。

`xmesh mesh derp admit-serve --listen A --allowlist F [--admin-token-file T] [--cert-file C --key-file K]`：

| 端点 | 作用 |
| --- | --- |
| `POST /admit` | derper 调用；节点公钥在放行文件里则 `Allow: true`。不认证，只监听环回或内网 |
| `GET`、`PUT /admin/keys` | 读取与整体替换放行列表；需要 `Authorization: Bearer <token>`；没有配置 token 时关闭 |
| `GET /healthz` | 存活检查 |

放行文件是 JSON（`0600`，原子替换），每次请求核对修改时间，因此 `sync` 的结果立即生效。文件损坏或不可读时拒绝所有连接（对放行名单来说，失败即关闭）。

`xmesh mesh derp sync --allowlist F` 或 `--admission-url U --admission-token-file T`：把控制面里已授权、带 xmesh 角色 tag 的设备节点公钥（`--all-devices` 为全部已授权设备）整体写入放行列表；设备被移除或撤销授权后，下次同步从列表删除。UI 只上传节点公钥和设备名，不上传任何工具调用内容。

范围：这是内部自建 DERP 的准入。按租户计量、计费、多租户目录与签名不在本契约内；收费与对外提供前的法律结论由用户另行处理（用户 2026-10-16 取消了内部使用阶段的法律前置）。
