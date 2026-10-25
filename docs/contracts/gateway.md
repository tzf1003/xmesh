# Gateway

Gateway 与入口不保存设备资源，见 [INV-03](../constraints/invariants.md#inv-03)。它们保存发现结果、到对端的 MCP 客户端，以及每条上游连接上的会话默认值。

## CTR-GW-01

### 名字

对端工具在 Gateway 上的名字是：

```text
<node.name> + "/" + <agent 上的工具名>
```

本机 Agent 的工具在本机 Gateway 上使用前缀 `local/`。入口对外发布时，把本机会被叫成 `local/` 的那一组改成 `<node.name>/`。

工具名的字母、数字、`_`、`-`、`.` 来自 Agent。前缀只用一个 `/` 分隔。Agent 工具名本身不得包含 `/`。

### 转发

1. 若本连接设置过默认 device，且工具名没有 `/`，则补上默认 device 的前缀。`session.set_defaults`、`devices.list`、`tools.search` 不加前缀。
2. 鉴权，见 [CTR-AUTHZ-01](oauth-authz.md#ctr-authz-01)。
3. 补全默认 `cwd` 与 `env`：仅当参数里没有这两个键，且目标是 `sys.`、`exec.`、`pty.`、`fs.` 这类内置工具并在其输入模式里声明了该参数。后端工具（如 `ida.*`）的参数原样透传，哪怕它也有名为 `cwd` 或 `env` 的参数。调用方显式传入的值，包括空字符串，都不覆盖。
4. 写审计开始记录。
5. 把请求发给目标 Agent。
6. 把 Agent 的结果原样返回，包括 `isError` 与 `structuredContent`。Gateway 不把 `resource_busy` 翻译成别的码。

目标不在线：`not_found`，`name` 为节点名。

Gateway 三个自有工具在 `tools/list` 里带完整的输入模式和说明，模型据此填写 `device`、`pattern`、`cwd`、`env`。

设置过默认 device 的连接，`tools/list` 额外展开该设备的完整工具表（含 `inputSchema`），不受 `expose_backends: favorite` 限制。默认设备是未带前缀调用的落点，客户端不应为了拿到 schema 再猜子串；其他设备仍按 favorite 过滤。

## CTR-GW-02

### devices.list

无参数。结果：

```json
{
  "devices": [
    {
      "id": "win-dev-01",
      "os": "windows",
      "online": true,
      "tags": ["tag:device"],
      "backends": ["ida", "frida"],
      "load": {"pty_in_use": 2, "pty_max": 20, "exec_shared_in_use": 1}
    }
  ]
}
```

`id` 是 `node.name`。在线数据来自最近一次成功的 `sys.info`。从未成功过的新节点 `online: true` 但 `os` 可为空，并带 `stale: true`。离线节点保留在列表里最多 24 小时，`online: false`，方便模型解释「刚才那台没了」。超过 24 小时删除。

scope 不含 `devices:read` 的客户端得到 `unauthorized`。

### tools.search

参数：`device`（节点名或 `local`）、`pattern`（大小写不敏感的子串，不是正则；传 `*` 列出全部）。

在该设备的完整工具表里查找，包括当前 `expose_backends: favorite` 下没有放进默认 `tools/list` 的工具。返回 `tools`：`name`（已带前缀）、`description`、`inputSchema`。上限 100 条，带 `truncated`。

`pattern` 为空 → `invalid_args`。设备不存在 → `not_found`。

## CTR-GW-03

工具名：`session.set_defaults`。

参数：`device`、`cwd`、`env`，至少提供一个。`device` 必须出现在当前 `devices.list` 的在线集合，或为 `local`。`cwd` 在 Gateway 侧只做字符串保存，真正的路径合法性由目标 Agent 在调用时判定。Gateway 不因为 cwd 看起来不存在而拒绝默认值，它没有那台机器的磁盘权威。

成功：`{"ok": true}`。只影响当前 MCP 连接。同一连接再次调用则整体替换先前提交的键；未提交的键保持不变。传入 JSON `null` 表示清除该键。

Agent 上不存在这个工具。若客户端直连 Agent 并调用它，得到 `not_found`。

## CTR-GW-04

### tailnet

每隔不超过 15 秒调用一次 `LocalClient().Status()`。对 `tag:device` 的新节点建立 MCP 客户端；对消失的节点关闭客户端并触发 [CTR-MUX-04](mcpmux.md#ctr-mux-04)。

对每个在线节点，`sys.info` 的缓存时间不超过 30 秒。调用失败时保留上一份缓存并标记 `stale: true`，连续失败超过 90 秒则把工具表视为空并通知上游，但节点仍显示在线直到 Status 说它不在。

各设备独立刷新连接与能力表。首次 `tools/list` 等待本机 Agent（及默认设备）的能力就绪，并对其余每台在线设备的首份能力表合计最多再等 5 秒，因为 claude.ai 等客户端只读一次 `tools/list`、不处理 `tools/list_changed`；同一会话里每台设备只等一次，5 秒内未就绪的设备就绪后仍通过 `tools/list_changed` 加入；对指定设备的首次调用或 `tools.search` 仅等待该设备。已有能力缓存的调用可在后台刷新时继续执行。单次刷新最长 10 秒，失败后至少间隔 5 秒重试；设备身份或地址变化时重新连接。上游会话结束时取消其待完成刷新并关闭下游连接。

### lan

用 mDNS 的出现与消失代替 Status。证书不是 `tag:device` 的节点不连接。

### 路由失效

一旦到某节点的 MCP 客户端断开，立即停止路由，不等待 30 秒缓存。这满足 [INV-09](../constraints/invariants.md#inv-09)。

## CTR-GW-05

控制面下线不是云端 MCP 工具。它是 tailnet 内的控制消息：每台 Agent 在设备网络监听上提供 `POST /control/drain`，请求体为下表的 JSON。端点只接受可信身份带 `tag:admin` 的调用（认证失败 401，缺少 tag 403），请求体最大 4096 字节，字段无效返回 400（`invalid_args`），`scope=device` 而 `device` 不是本节点名返回 404；成功返回 `{"ok": true}`。`--local` 开发模式不提供该端点。最小载荷：

| 字段 | 含义 |
| --- | --- |
| `scope` | `device` 或 `mesh` |
| `device` | `scope=device` 时的节点名 |
| `drain` | `keep`（默认）或 `kill` |

Agent 收到后拒绝新的 `exec.run`、`pty.open`、`backend.acquire` 和后端工具，错误码 `policy_denied`，规则名 `mesh.draining`。`drain: keep` 时已有 PTY 与租约按原超时结束。`drain: kill` 时按 `pty.close` 的顺序终止本机全部 PTY，并 release 全部租约。

`scope=mesh` 只作用于收到请求的这一个进程：该进程的 Agent 与同进程内的 Gateway 进入 drain，不会向其他节点转发。要下线全网，由发起方向每个节点分别发送一次请求，`xmesh drain --scope mesh` 即做这件事（见下）。drain 状态只在内存里，进程重启后重置。

Gateway 收到后对该设备（或全网）停止路由，并发送 `tools/list_changed`。Gateway 有两条途径得知：同一进程的 Agent 收到控制消息时直接通知；任何 Gateway 从目标 Agent 的响应里看到 `policy_denied`、`rule: mesh.draining` 时立刻对该设备停止路由，不要求控制消息到达该 Gateway。设备离开网络后再回来视为新进程，清除这条标记。

### 命令行

```text
xmesh drain --scope device --device <节点名> [--mode keep|kill] --config xmesh.yaml
xmesh drain --scope mesh [--mode keep|kill] --config xmesh.yaml
```

命令以管理员身份入网，向每台目标设备的 `POST /control/drain` 发送上述载荷：`scope: device` 只发给 `--device` 指定的设备；`scope: mesh` 向所有在线的 `tag:device` 设备逐台发送 `scope: mesh`，各设备据此让自己的 Agent 与本机 Gateway 进入 drain。逐台输出结果，任何一台失败都使命令以非零状态退出。tailnet 模式用 `--authkey-file` 指向带 `tag:admin` 的 auth key，命令以临时节点入网；LAN 模式使用本机的配对证书，对端须用 `xmesh pair --import ... --tags tag:admin` 导入本机公开配对文件。

## CTR-GW-06

### 入口 PTY 总预算

`ingress.max_pty_per_client` 默认 8。入口把各 Agent 的同一 OAuth client 的 `active` 与 `orphaned` 终端相加；`closed` 释放额度。不同 MCP 会话共享额度。`pty.transfer` 使用目标 Agent 记录的接收方身份，向另一主体转移时检查接收方总额；同主体接管保持原有额度。

入口在整个查询、准入与调用完成期间串行化 `pty.open` 和 `pty.transfer`。查询使用 Agent 的 MCP 控制资源 `xmesh://control/pty-budget`，由已认证且获 PTY 授权的 `tag:ingress` 读取。该资源包含当前终端 reservation、对应预算主体、在线 caller 的主体及进程 epoch，不含终端正文；Gateway 不向公网转发此资源。Agent 的启动 reservation 和断线孤儿均计数。

入口状态目录的 `ingress-pty.json` 保存需要重新查询的设备 ID、Agent epoch 和待完成调用标志。发送有副作用的调用前原子落盘。该记录用于准入治理，终端归属与资源状态始终由 Agent 决定。入口重启后重新查询 Agent。已记载资源的设备离线、旧版 Agent 缺少控制资源或快照无效时，新增与转移返回 `policy_denied`、`rule: quota.unavailable`。读写、关闭与信号仍可作用于在线资源。

调用结果丢失时，待完成标志保留；同一 Agent epoch 的快照无法证明旧调用已经完成，因此继续拒绝新增。重启对应 Agent 并重新查询到新 epoch 后可恢复准入。状态写入失败时停止新增；保留状态目录，修复存储后重启入口。总额已满返回 `policy_denied`、`rule: quota.exceeded`。

入口使用独立的持久化 `.lock` 文件锁住预算文件，在锁内重读记录、查询 Agent、准入和完成调用。同一主机的多个入口通过 `ingress.pty_budget_file` 指向同一本地文件时共享原子准入；进程退出后由操作系统释放锁，待完成标志仍保留。共享文件记录配额值，各入口配置不一致时拒绝启动或新增。版本 1 的既有记录会在下一次写入时补充配额值。

共享入口必须使用支持文件锁的版本、同一 OAuth client 身份域和本地预算文件，并保留预算文件及其 `.lock` 文件。不要把预算文件放在网络文件系统上。直接调用 Agent 使用 Agent 上的设备与主体配额。

**跨主机入口。** 多台主机各运行入口时，在每个入口配置相同的 `ingress.pty_budget_authority`（一台 Agent 的设备名）。该 Agent 通过 `tag:ingress` 专用的控制资源模板 `xmesh://control/pty-budget-authority{?q}` 托管一把带围栏令牌的互斥租约，`q` 是 base64url 编码的 JSON 请求（`acquire`、`mark`、`release`）。入口在每次 `pty.open`/`pty.transfer` 的查询、准入与调用期间持有该租约，因此不同主机上的入口按同一顺序准入；总额仍由各 Agent 的实时快照得出。要求：

- 租约所有者由经过认证的入口节点身份加进程随机数组成；一台入口不能使用、释放或代替另一台入口的租约。读取该资源与读取 `xmesh://control/pty-budget` 使用同一授权：已认证的 `tag:ingress`、带 OAuth client 并获得 `pty.open` 或 `pty.transfer` 授权，策略重载立即生效。
- 租约有效期 30 秒。入口只用其中 25 秒：持有者在发出请求前开始计时，调用的上下文截止时间早于权威侧到期时间，因此沉默的持有者最多阻塞其他入口 30 秒，且不依赖主机间时钟一致。权威 Agent 启动后一个有效期内不发放租约，使上一进程发出的租约一定已经过期。
- 发送有副作用的调用前，持有者向权威侧登记待完成记录（目标设备与 Agent epoch）。调用完成并确认后释放时清除；结果不确定（传输中断、超时、持有者崩溃或租约过期）时记录保留，其他入口读取到同一 epoch 的记录时，与本机待完成标志一样拒绝新增，直到目标 Agent 重启并出现新 epoch。释放消息未送达但调用已确认完成时，持有者在下一次获取租约时携带已完成声明，由权威侧清除。
- 权威 Agent 离线、不可达、租约在 10 秒内无法取得，或响应无效时，新增与转移返回 `policy_denied`、`rule: quota.unavailable`，不创建终端。读写、关闭与信号不受影响。权威 Agent 重启会丢失待完成记录，因此须先确认没有结果不确定的调用，再重启；重启后一个有效期内不发放租约。
- 权威设备必须是所有入口都能连到的 Agent，且应长期在线。它同时是 Agent，其自身的终端也计入总额。未配置 `ingress.pty_budget_authority` 时行为与此前一致：只在本机协调。

跨主机协调只约束 `pty.open` 与 `pty.transfer` 的准入顺序；不影响 exec 配额，也不改变 Agent 上的设备与主体限额。

## CTR-GW-07

本机 Gateway 的每个请求都要令牌，包括回环地址。它按三层判断，任一层拒绝即返回 `401`，不返回 MCP 内容：

1. **来源。** 对端地址必须在 `gateway.access` 允许的范围内。回环地址恒开，不论列表如何。`lan` 指 RFC 1918 的 IPv4 私网与 `fc00::/7`，`tailnet` 指 `100.64.0.0/10` 与 `fd7a:115c:a1e0::/48`，其余条目是单个 IP 或 CIDR。缺省 `[localhost, lan, tailnet]`。
2. **Host 头。** 必须是 IP 字面量、`localhost`，或 `gateway.hosts` 里列出的主机名。其余名字一律拒绝，这也挡住 DNS rebinding。
3. **令牌。** 恰好一个 `Authorization: Bearer`，与 `gateway.pats` 中某个令牌文件的内容匹配。没有任何 PAT 时，任何请求都被拒绝。

`gateway.listen` 决定实际绑定：`:8080` 绑全部网卡，`127.0.0.1:8080` 只绑本机，此时地址列表只有 `localhost`。tailnet 地址由 tsnet 在 tailnet 上提供，不经过操作系统网卡（CON-NET）。`gateway.listen` 在运行中改动不需要重启：Gateway 先绑定新地址再放弃旧地址，已经建立的连接继续使用，Agent 与 PTY、exec 会话不受影响（见 [config.md 的热加载表](config.md#热加载与冷加载)）。新地址绑不上时 Gateway 留在旧地址，原因通过 `apply_errors` 与状态警告报告。

**地址列表（`GatewayInfo.addresses`）。** 内核列出当前真正可达的地址，每项带 `kind`、`host` 与 `url`：`localhost`（恒有）、`lan`（`lan` 条目开启时，每个启用网卡的私网 IPv4）、`tailnet_ip` 与 `tailnet_name`（`tailnet` 条目开启、节点已入网时，tailnet 的 IP 与 MagicDNS 名）、`host`（每个 `gateway.hosts`）。绑定回环时只有 `localhost`。自定义 IP 与 CIDR 只是来源，不作为地址列出。

**域名。** `gateway.hosts` 只用于 Host 头校验与地址展示，不带 TLS，也不改变来源范围。

**令牌的生命周期。** 首次启动或升级时若没有任何 PAT，内核创建 `default`。已有令牌从不因启动、升级、更新或重装而改动。轮转只能由用户经 [CTR-CTL-09](../contracts/control.md#ctr-ctl-09) 的轮转路由发起。删除最后一个 PAT 被拒绝。

验收：`internal/config/access_test.go` 覆盖来源与 Host 判定；`internal/role/gateway/gateway_access_test.go` 覆盖三层拒绝；`internal/control/gateway_token_test.go` 覆盖升级不轮换与 `PUT /v2/gateway`；`internal/app/gateway_info_test.go` 覆盖地址列表。
