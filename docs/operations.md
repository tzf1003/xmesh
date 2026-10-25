# 运行手册

## 设备网络版本 2

设备公钥配对、双路径选择和逻辑会话恢复见 [设备连接设计](design/19-device-connectivity.md)。局域网与 Tailnet 独立启用，共用同一份设备信任：

```yaml
schema_version: 2
node:
  name: workstation
  roles: [agent, gateway]
mesh:
  lan:
    enabled: true
    mdns: true
  tailnet:
    enabled: true
    control_url: https://controlplane.tailscale.com
    auth_method: browser
gateway:
  listen: 127.0.0.1:8080
agent:
  cwd_policy:
    allow: []
  authz:
    "tag:*":
      tools: ["*"]
      backends: ["*"]
```

旧节点先停止，运行 `xmesh migrate --config xmesh.yaml`。命令保留原配置及旧信任/网络状态备份，并维持原来的路径启用选择。之后启动新版并重新配对。旧信任文件保留用于回滚；新版设备信任写入独立的 `identity-v2`。

在目标节点设置连接密码，再从调用节点通过任一可用路径配对：

```bash
xmesh login --config xmesh.yaml
xmesh serve --config xmesh.yaml
xmesh control pair password set --password-stdin --node workstation
xmesh control pair connect --path lan --address https://192.168.1.20:7433 --password-stdin --node client
xmesh control pair connect --path tailnet --address https://100.100.1.20:7433 --password-stdin --node client
xmesh control pair revoke --device-id dev-<公钥摘要> --node client
```

两条 connect 命令是可选连接方式，同一设备只需完成一次配对。首次连接或断线恢复优先局域网，连接成功后保持当前路径。Tailnet 网络授权撤销不改变 xmesh 设备信任；撤销设备信任会结束该设备全部路径的会话。下面部署材料中的单模式配置可先运行 migrate 转成版本 2。

### 复用系统 Tailscale

已安装并联网的系统 Tailscale 可作为可选的设备通信路径，无需再启动 xmesh 内置的 tsnet 节点。在接收连接的设备上，显式指定持有 Tailscale IPv4 的网卡：

```yaml
mesh:
  lan:
    enabled: true
    interface: Tailscale
    mdns: false
  tailnet:
    enabled: false
```

`interface` 填操作系统中的真实网卡名：Windows 常见为 `Tailscale`，Linux 常见为 `tailscale0`，macOS 通常为持有 Tailscale IPv4 的 `utunN`，请实际核对。此配置让设备端口仅监听该网卡的 `100.64.0.0/10` IPv4 地址及 TCP 7433；它替代本节点原来的局域网监听，并不会同时监听两块网卡。默认不指定网卡时仍只自动选择私有 IPv4，不会自动改用系统 Tailscale。Tailscale 不传递 mDNS 组播，须关闭 `mdns`，按地址手动配对。

配置生效后，在对端设置连接密码，然后从已通过系统 Tailscale 联网的调用节点连接：

```bash
xmesh control pair connect --path lan --address https://100.100.1.20:7433 --password-stdin --node client
```

面板手动连接也选择「LAN / 局域网」路径，填写对端的系统 Tailscale 地址。这里的 `lan` 使用操作系统路由；`tailnet` 则使用 xmesh 内置的独立 tsnet 节点，不能混用。调用节点须启用 `mesh.lan`；为支持双方发起连接，可在双方都指定各自的系统 Tailscale 网卡。系统 Tailscale 在线不代表 TCP 7433 已开放，还需确保对端监听就绪、Tailnet 访问规则和主机防火墙允许该连接。设备信任、连接密码及 mTLS 验证仍由 xmesh 执行。

真实接口验收可运行 `XMESH_TEST_SYSTEM_TAILSCALE_INTERFACE=tailscale0 go test ./internal/connectivity -run '^TestSystemTailscaleListenerPairingAndAgent$' -v -count=1`。PowerShell 中先设置 `$env:XMESH_TEST_SYSTEM_TAILSCALE_INTERFACE='Tailscale'`，再运行同一条 `go test`。该检查在临时状态目录启动节点，使用本机系统 Tailscale IPv4 的 TCP 7433，验证未配对连接被拒绝、密码配对及 Agent `sys.info` 调用；需要该 IP 的 7433 空闲，不代表不同物理主机间的网络验收。

## Cloudflare Tunnel（没有公网地址）

每台要被连接的设备运行自己的隧道。可以让 xmesh 通过 Cloudflare API 自动创建，也可以在 Cloudflare Zero Trust 里手工建。

### 自动创建（推荐）

先在 Cloudflare 创建一个专用 API 令牌，权限：Account 的 Cloudflare Tunnel（编辑）、Access: Apps and Policies（编辑）、Access: Service Tokens（编辑），Zone 的 DNS（编辑），资源范围限定到用于网格的那个域名。令牌只保存在系统钥匙串（或 `--credential-file`），不进 `xmesh.yaml`，内核不持有。

```bash
echo "$TOKEN" | xmesh cloudflare login --account <账号ID> --zone <区域ID> --domain mesh.example.com
xmesh cloudflare setup --name laptop --config xmesh.yaml            # 预览
xmesh cloudflare setup --name laptop --config xmesh.yaml --apply    # 创建并写好配置
```

`setup` 为这台设备创建远程配置的隧道（转发 `tcp://127.0.0.1:7434`）、代理 CNAME、只放行网格服务令牌的 Access 应用；同一网格的所有设备共用一个服务令牌（首台设备时创建，记录在本机状态文件里）。隧道令牌与 Access 密钥写入配置文件旁 `cloudflare/` 下的 0600 文件，`mesh.cloudflare` 同时写好 `tunnel_token_file`，内核启动后自己运行 `cloudflared tunnel --no-autoupdate --protocol http2 run --token-file …`，所以只需要装好 `cloudflared`。`xmesh cloudflare remove --name laptop --apply` 删除这台设备的全部资源，最后一台删完时一并删除网格服务令牌。

国内网络强制 HTTP/2（走 TCP 443/7844）是写死的，不是选项；隧道连边缘的 7844 端口须可达。

### 手工创建

在 Cloudflare Zero Trust 里为每台设备建一条隧道，公共主机名各一个（如 `mac.link.example.com`），服务类型选 **TCP**，目标 `tcp://127.0.0.1:7434`；再建一个只放行服务令牌（Service Auth）的 Access 应用覆盖 `*.link.example.com`，并创建服务令牌。自己运行 cloudflared：`cloudflared tunnel --protocol http2 run --token <隧道令牌>`。

手工创建时把服务令牌的密钥写入 0600 文件，然后配置每台设备：

```yaml
mesh:
  cloudflare:
    enabled: true
    hostname: mac.link.example.com            # 只拨出的设备不填
    access_client_id: xxxxxxxx.access
    access_client_secret_file: /home/me/.xmesh/cf-access-secret
    peers:                                    # 已配对设备 ID -> 它的隧道主机名
      dev-<64 位十六进制>: win.link.example.com
```

配对：隧道只在被连接的一端设置了连接密码期间接受未配对设备，其余时间只放行已配对设备（`allow_pairing: true` 让它一直允许，不建议）。局域网和 tailnet 都拨不通时才会尝试这条路径。

### 邀请新设备加入

已经用 `xmesh cloudflare login` 和 `setup` 配好一台设备后，可以让第二台设备一步加入，不需要在新设备上登录 Cloudflare：

```
# 已有设备（xmesh 需正在运行）
xmesh cloudflare invite --name phone --config xmesh.yaml          # 先预览
xmesh cloudflare invite --name phone --config xmesh.yaml --apply  # 为 phone 建隧道，打印加入码并等待

# 新设备（已有 schema_version 2 的 xmesh.yaml）
xmesh join xmesh-join-… --config xmesh.yaml
```

新设备配置里的 `node.name` 必须与 `--name` 相同（邀请方靠它认出是谁加入）。加入码 15 分钟内有效、只能用一次，内含新设备自己的隧道令牌、网格 Access 凭据和一次性连接密码，按密钥对待，只通过你信任的渠道交给新设备（加入码本身就是信任的起点，被整个替换时新设备无从分辨）；它不含 Cloudflare API 令牌。已有设备会临时设置连接密码，新设备配对成功后立刻清除；到期或失败会清除密码并回收为新设备创建的 Cloudflare 资源；若新设备配对成功后才在本机写文件失败，邀请方已认为它加入了，需要在邀请方运行 `xmesh cloudflare remove --name 新设备名 --apply` 再重新邀请。邀请方会把新设备写入自己的 `mesh.cloudflare.peers`（重启后生效）。配对仍是逐对的：新设备只信任发出邀请的设备，其他设备需要各自再配对。加入后重启 xmesh 即可上线。

### 把 Cloudflare 连接方式发给已配对的设备

设备 B 没有接入 Cloudflare，但已经通过局域网、Tailnet 或别的方式和 A 配过对时，A 可以通过它们之间已有的加密连接，帮 B 也接入 Cloudflare，不需要加入码：

```
# A（xmesh 需正在运行，已 login 和 setup）
xmesh cloudflare share --device phone --config xmesh.yaml          # 先预览
xmesh cloudflare share --device phone --config xmesh.yaml --apply  # 为 phone 建隧道并下发

# B：查看并确认（xmesh 需正在运行）
xmesh control offers list
xmesh control offers accept <编号>     # 或 reject
```

下发的内容只有 B 自己那条隧道的令牌、网格的 Access 服务令牌、B 的主机名和 A 的主机名，不含 Cloudflare API 令牌，也不会出现在 `offers list` 里。B 上的用户确认之前不会改任何文件；确认后凭据写进状态目录下的私有文件，`mesh.cloudflare` 写入主机名、凭据文件和 A 的地址，重启 xmesh 后 B 经自己的隧道上线，A 已把 B 写进自己的 `peers`（同样重启后生效）。A 和 B 因此都有自己的公网主机名，可以互相拨号。

A 拨不通 B 时（例如 B 只拨出到 A），提议会留在 A，B 经自己到 A 的连接约一分钟内取走并确认，此时 `share` 输出“已留在本机”，隧道保留。收件箱最多 16 条（单个发送者 4 条），24 小时不处理就自动删除。B 已经配置了 Cloudflare，或 `mesh.cloudflare.listen` 不是默认的 `127.0.0.1:7434` 时不能接受。下发地址和凭据不建立任何信任：B 与其他设备之间仍须各自配对。

### 把 Tailscale 连接方式发给已配对的设备

A 已经在 Tailscale 或 Headscale 的 tailnet 上，B 还没有，但两台已经通过别的方式配过对时，A 可以把加入 tailnet 的预授权 key 经同一条加密连接发给 B：

```
# A（xmesh 需正在运行，mesh.tailnet 已启用）
xmesh mesh share --device phone --config xmesh.yaml          # 先预览
xmesh mesh share --device phone --config xmesh.yaml --apply  # 生成 key 并下发

# B：查看并确认
xmesh control offers list
xmesh control offers accept <编号>     # 或 reject
```

key 的来源按顺序是：已用 `xmesh mesh login` 保存了写权限的管理凭据时自动生成（默认单次使用、48 小时有效，覆盖“排队等对方来取”和“对方确认”两段各 24 小时；Headscale 服务器上有多个用户时加 `--user`，还可加 `--tags`、`--reusable`、`--ephemeral`）。保存的管理凭据必须就是本机所在控制面的那一个，否则不会用它，并提示原因；否则用 `--authkey-file` 指定的文件；再否则加 `--reuse-local-key` 复用本机 `mesh.tailnet.authkey_file` 里的 key（它必须是可重复使用的，否则 B 会注册失败）。三者都没有时命令不会发送，只会提示去哪里生成：Tailscale 是 `https://login.tailscale.com/admin/settings/keys`，Headscale 是 `headscale preauthkeys create --user <用户> --expiration 24h`。

管理凭据不会发给 B，B 只拿到这一个 key。`--tags` 是 Tailscale 控制面的标签，和设备在 xmesh 里的角色标签互不相干。B 确认后，key 写进状态目录下的私有文件，`mesh.tailnet` 启用并指向 A 所在的控制面（认证方式改为 key）；B 已经启用或配置了 Tailnet 时不能接受，提议保留。其余行为（离线排队、收件箱上限、24 小时过期、不建立信任）与 Cloudflare 的下发相同。

## Linux 网页管理界面

Linux 没有托盘应用，用 `xmesh-webui` 在浏览器里提供与桌面端相同的面板。推荐用发布页的 `install-linux.sh`：它把内核和网页界面各装成一个 systemd 用户服务（`xmesh-<节点名>`、`xmesh-webui`），并在终端里协助设置工作目录、权限和管理密钥，见 [安装说明](agent/install.md#linux)。

只装网页界面时，以运行内核的同一用户执行 `node xmesh-webui/xmesh-webui.js install`：写入 systemd 用户单元并启动，首次生成管理密钥（保存在 `~/.config/xmesh-webui/webui.key`，`0600`），默认只监听 `127.0.0.1:7480`。远程使用 SSH 隧道或 `--tls-cert/--tls-key`。选项和与桌面端的差别见 [ui/README.md](../ui/README.md#linux-网页管理界面)，安全设计见 [12 · Linux 网页管理界面](design/12-ui-and-control.md#linux-网页管理界面)。

## 构建与本机验证

需要 Go 1.27.1。复用 Go 模块和构建缓存：

```bash
go build -o bin/xmesh ./cmd/xmesh
go test -race ./... -timeout=60s
go vet ./...
python3 docs/contracts/schemas/check_examples.py
python3 scripts/smoke_local.py --binary bin/xmesh
```

模式检查脚本需要 Python `jsonschema`。冒烟脚本仅用标准库，创建临时工作目录，启动 Agent 与 Gateway，验证文件、exec、PTY、审计和退出后清理。

先修改 `xmesh.local.yaml` 的允许目录，再启动。该文件启用 `agent` 与 `gateway` 两个角色，所以同时有 Agent（`127.0.0.1:7433`）和本机 Gateway（`127.0.0.1:8080`）；只写 `roles: [agent]` 时不会启动 Gateway，下面第三条 `8080` 的命令会连不上：

```bash
bin/xmesh serve --local --config xmesh.local.yaml
bin/xmesh status --json
bin/xmesh status --endpoint http://127.0.0.1:8080/mcp --tool local/sys.info --json
```

开发 Agent 默认 `127.0.0.1:7433/mcp`（可用 `--listen` 改，必须在 `127.0.0.1` 上）；Gateway 默认 `127.0.0.1:8080/mcp`（`gateway.listen`），经它调用本机工具时工具名带 `local/` 前缀，所以 `status` 打 Gateway 要加 `--tool local/sys.info`。客户端使用带会话的 Streamable HTTP MCP，协议版本 `2025-11-25` 或 `2025-06-18`。PTY owner 与租约属于对应 MCP 会话。

配置文件顶层的 `node`、`mesh`、`agent` 三段对所有角色都必填：只启用 `gateway` 或 `ingress` 的节点也要写 `agent` 段，且 `agent.cwd_policy.allow` 必须存在（可以是空列表，表示除 `deny` 外全部目录可访问）；未启用 `agent` 角色时这一段不会被使用。

## tailnet 节点

每个进程使用独立的 `node.name`、状态目录及 tsnet 身份。主机已有的系统 Tailscale 与 xmesh tsnet 是独立节点。配置示例：

```yaml
node:
  name: linux-dev
  roles: [agent, gateway]
mesh:
  mode: tailnet
  control_url: https://controlplane.tailscale.com
  authkey_file: /secure/xmesh-authkey.txt
gateway:
  listen: 127.0.0.1:8080
agent:
  default_cwd: /srv/workspace
  cwd_policy:
    allow: [/srv/workspace]
  authz:
    "tag:*":
      tools: ['*']
      backends: ['*']
```

注册 key 文件设为 `0600`，状态目录由程序设为 `0700`。当前启动流程每次读取 key 文件，已注册后仍需保留其可读路径。

首次运行向导有四种入网授权方式：

- **浏览器登录**（默认）：自动打开 Tailscale 授权页，完成后保存节点身份并安装服务；下次启动复用身份。设备审批和标签授权要求继续生效。命令行为 `xmesh login --config <配置> --state-dir <节点状态目录> --json`。配置使用 `mesh.auth_method: browser`，省略 `authkey_file`；必须使用持久节点。

- **用管理凭据自动创建**：先用「Tailnet 管理」页保存一份读写凭据，向导会调用 `xmesh mesh authkey` 生成预授权（`preauthorized`）、带 `tag:device` 的一次性 key，写进 `<状态根>/<节点>/authkey`（`0600`）。该方式签发的 Tailscale key 设置预授权；Headscale 多用户场景还需在向导填写用户名。
- **粘贴 key**：直接粘贴 Tailscale 或 Headscale 生成的 key，向导会把它写进同一个 `0600` 文件，配置文件只引用路径。
- **使用 key 文件**：填写已有的绝对路径。

网络策略须允许调用节点访问目标 TCP 7433。完成 xmesh 公钥配对后，Gateway 按设备身份合并已验证路径；Agent 使用本地信任记录授予的应用标签检查 `agent.authz`。控制面的网络标签只决定路径准入，生产环境应分别收窄网络规则和应用授权。

```bash
bin/xmesh serve --config /secure/xmesh.yaml --state-dir /secure/xmesh-state
bin/xmesh status --endpoint http://127.0.0.1:8080/mcp --tool devices.list --json
```

远端工具名为 `<node.name>/exec.run` 等；本机 Gateway 上为 `local/exec.run`。`sys.info` 返回实际网络身份、角色、重载失败及待重启提示。

设备能力表独立后台刷新，已连接设备通过缓存提供工具。远端首次就绪后会通知客户端更新工具列表；不可达设备的连接尝试具有独立超时和重试间隔。`devices.list` 中的 `stale` 表示元数据仍待刷新，针对某设备的首次工具调用会等待该设备的能力查询。

两端运行后，可在各自 Gateway 验证对端：

```bash
python3 scripts/smoke_peer.py --gateway 127.0.0.1:8080 --device linux-dev --expected-os linux --token-file <PAT的token_file路径>
```

脚本检查发现、身份、文件、exec 和 PTY，最后关闭会话并清理测试文件。配置 PAT 的 Gateway 应使用具备对应 scope 的 MCP 客户端执行同等调用；此脚本面向默认本机访问。

诊断 DERP 时，私有 derper 的客户端校验依赖其系统 tailscaled 能识别连接节点。若日志提示节点不存在，应检查该系统节点的控制面可见性和定向访问策略。网络集成测试会打开指定 tsnet 状态，运行前须停止使用同一状态的 xmesh 进程：

```bash
XMESH_TEST_TAILNET_CONFIG=/secure/xmesh.yaml \
XMESH_TEST_TAILNET_STATE=/secure/xmesh-state/network \
XMESH_TEST_TAILNET_PEER=linux-dev \
XMESH_TEST_TAILNET_REQUIRE_DERP=1 \
go test ./internal/mesh -run Test_TailnetPeerConnectivity -v -timeout=60s
```

测试输出目标设备和实际 disco 路由；`REQUIRE_DERP=1` 要求探测经 DERP，否则也接受直连。`XMESH_TEST_TAILNET_VERBOSE=1` 可开启供应商诊断日志。

## 临时沙箱节点

短生命周期环境可设置 `mesh.ephemeral: true`，示例见[临时节点配置](contracts/schemas/examples/xmesh.ephemeral.json)。控制面注册 key 应配置 ephemeral 与所需 tag；重复启动的验收需要可复用 key。节点身份仅存内存，复用同一状态目录也会重新注册。正常退出会在最多 5 秒内尝试注销并关闭 tsnet；注销失败返回运行错误。异常退出后的临时节点回收由控制面负责。

在待验收的沙箱内执行：

```bash
python3 scripts/smoke_sandbox.py --binary /opt/xmesh \
  --auth-key-file /run/secrets/xmesh-authkey \
  --peer mac-dev:darwin --peer linux-dev:linux \
  --peer-password-file mac-dev=/run/secrets/mac-device-password \
  --peer-password-file linux-dev=/run/secrets/linux-device-password \
  --observer-gateway 127.0.0.1:8080 --observer-token-file <observer的token_file路径>
```

`--observer-gateway` 可选，指向当前环境能访问的现有回环 Gateway，用于反向工具调用和退出离线观察；须同时给出 `--observer-token-file`（该 Gateway 通过 xmesh control pats list 返回的实际 token_file）。每个对端先设置连接密码，并通过 `--peer-password-file 名称=文件` 提供私有口令文件，用于首次建立双向 xmesh 信任。观察节点也须列在配对对端中。脚本启动两个生命周期，验证 Tailnet 临时入网、持久设备 ID 保持、远端文件/exec/PTY、网络凭据状态不落盘和临时工作目录清理；入网 key 与配对口令均通过文件路径引用。需要沙箱允许出站访问控制面和 DERP；测试环境中的设备授权应允许临时节点调用所选工具。各云产品的出站限制须在对应环境验收。

## 本机 Gateway PAT

Gateway 每个请求都要令牌，包括本机。节点有 `gateway` 角色而配置里没有任何 PAT 时，内核启动时自动创建名为 `default` 的令牌，文件通常在状态目录的 `pats/default.token`；恢复残留文件时使用随机后缀。请通过 `xmesh control pats list --state-dir <状态目录>` 的 `token_file` 字段取得实际路径。升级、更新与重装不会改动已有令牌，只有在面板里轮换（或 `xmesh control pats rotate`）才会替换。删除最后一个 PAT 会被拒绝。

谁能连接由 `gateway.access` 决定（`localhost`、`lan`、`tailnet`，或 IP、CIDR），域名由 `gateway.hosts` 列出；`gateway.listen` 为 `:8080` 绑全部网卡，为 `127.0.0.1:8080` 只绑本机。下面的例子演示额外配置一个只读令牌：



```yaml
gateway:
  listen: ":8080"
  access: [localhost, lan, tailnet]
  pats:
    reader:
      token_file: /secure/xmesh-reader.pat
      scopes: [devices:read, 'fs:read:linux-dev', 'sys:linux-dev']
```

使用密码学随机源生成至少 32 字符的 token，文件最多 4096 字节，仅包含 token 和可选首尾空白。Unix 文件权限须为 `0600` 或更严格；Windows 部署须通过文件 ACL 限制读取者。客户端向本机 Gateway 发送 `Authorization: Bearer <token>`。配置任意 PAT 后，每个请求均须认证；空 PAT 配置保留本机完整访问。携带无效 Bearer 的请求会被拒绝。

```bash
bin/xmesh status --endpoint http://127.0.0.1:8080/mcp --tool devices.list --pat-file /secure/xmesh-reader.pat --json
python3 scripts/smoke_local.py --binary bin/xmesh --pat
```

认证逐请求读取当前配置与凭据文件，替换文件即可轮转 token，删除配置项即可撤销该凭据。任一已配置文件无效或同一 token 对应多个名称时，认证失败。scope 变更后应创建新 MCP 会话；每个 PAT 名称具有独立会话身份与调用预算。PAT 的 scope 在 Gateway 检查，Agent 按 Gateway 的可信节点身份和 tag 再检查授权。

## 设备配对

局域网通过 mDNS 发现，Tailnet 通过网络候选发现；两种路径共用设备公钥信任并进行 mTLS 验证。最常用的方式是连接密码，在面板的「设备」页完成：

1. 在 A 设备上设置一个连接密码（10 到 128 个字符，首尾不能有空白）。
2. 在 B 设备的发现列表里看到 A，点「连接」，输入 A 的连接密码。或者在「手动添加设备」里输入 A 的地址和密码。
3. 密码正确即刻互信，无需重启。

连接密码不会在界面上再次显示，也不写入配置文件。错误的密码会被计数，连续失败后对端暂停接受密码配对一段时间，成功一次即清零。清除连接密码后，对方不能再用它配对，已配对的设备不受影响。

命令行等价于：`xmesh control pair password set --password-stdin`（口令从标准输入读取，不接受命令行参数），`xmesh control pair discover`，`xmesh control pair connect --address 地址 --password-stdin`。

配对文档（`xmesh pair --export` / `--import`，面板里的「高级」）保留作为后备：无法使用连接密码时，双方交换公开导出文件，并通过独立可信渠道核对指纹：

```bash
bin/xmesh pair --config xmesh.yaml --state-dir /secure/state --export /tmp/node-public.json
bin/xmesh pair --config xmesh.yaml --state-dir /secure/state --import /tmp/peer-public.json --fingerprint VERIFIED_SHA256 --tags tag:peer,tag:device
```

两种配对方式都立即生效。设备目录按已配对公钥合并路径，调用权限由接收方的 xmesh 信任标签及 `authz` 决定。局域网的设备监听使用所选网卡的 TCP 7433；同一地址只能运行一个 LAN 监听器。Funnel 需要启用 Tailnet。

## 状态目录权限

状态目录存放节点私钥、OAuth 凭据、PAT 计数与审计日志，只允许运行用户使用。启动（`serve`、`pair`）时由 `internal/privdir` 保证：

- macOS、Linux：目录 `0700`，已存在的目录会收紧到 `0700`。
- Windows：`os.MkdirAll` 不设置 ACL，目录只继承父目录，放在 `C:\` 根下新建的目录里时其他本地用户默认可读写。现在新目录创建时就带受保护 DACL（禁用继承），只授予运行用户、`SYSTEM`、`Administrators` 完全控制，并带对象/容器继承，之后创建的文件沿用。`install --system` 时运行用户就是 `SYSTEM`，实际只有 `SYSTEM` 与 `Administrators`。
- Windows 已存在的目录：不是真实目录（符号链接、junction、文件）或属主不是运行用户、`SYSTEM`、`Administrators` 时拒绝启动，因为另一个属主可以改回 ACL；否则 DACL 不是上述受保护形态时直接改写并向已有子对象传播，随后扫描整棵树：遇到链接或其他属主的条目报错，显式授权给其他账户的子对象重置为私有。这不能撤销目录开放期间别人已经读走的内容，开放过的状态目录里的密钥应视为已泄露并轮换。
- 状态目录的 ACL 不授予安装者或登录用户。`install --system` 后桌面 UI 以普通用户读取 `%ProgramData%\xmesh\<节点名>\control.json` 时，需要由写该文件的一方调用 `privdir.GrantRead(path, sid...)` 给文件单独加只读 ACE（Windows 允许任何账户穿越自己不能列目录的上级目录，按精确路径打开该文件即可）；目录本身与其他文件仍保持私有。内核控制通道每次写 `control.json`（临时文件改名之后）都会调用它，授权者就是命名管道 DACL 的 `--control-allow-sid`（`Test_ControlEndpointFileIsReadableByAllowedSIDs`）。

## 后端与入口

后端启动命令、工作目录和策略配置在 `agent.backends`。exclusive 后端先 `backend.acquire`，后续请求以 `_lease_token` 携带返回的 fencing token；Agent 转发业务参数前剥离此字段。普通业务 `token` 字段照常转发。续期必须显式调用 `backend.renew`。只改策略时有效后端进程和租约保留，后端配置变更或故障会使旧租约失效。

公网入口节点需要控制面授予 `tag:ingress`，并配置对应 Agent 权限。登录口令来自 `--oauth-password-file`，默认状态目录内的 `oauth-password`，建议权限 `0600`。`ingress.oauth.issuer` 必须匹配客户端访问的 HTTPS 外部地址。Funnel 需要官方控制面；反向隧道监听回环地址，缺省 `127.0.0.1:8443`，端口被占用时用 `ingress.tunnel_listen`（如 `127.0.0.1:8444`）或 `--tunnel-listen` 更换，命令行优先，外层隧道把 HTTPS 指向它并负责证书；public:443 使用证书文件或 ACME。

动态注册客户端提供允许的回调 URI，只获得 `ingress.oauth.dynamic_client_scopes`（缺省 `devices:read`）里的 scope；要让 claude.ai 或 Codex 的动态注册客户端调用工具，须把需要的 scope 写进该上限，或把客户端预注册在 `clients`。入口只响应 issuer 主机上的请求（其他 `Host` 返回 421）；`tunnel` 的外层隧道须转发真实 `Host` 或 `X-Forwarded-Host`，并追加 `X-Forwarded-For`，限速才按真实客户端计数。配置文件中的客户端只定义 scope，其首次通过口令与 CSRF 校验的授权会绑定并持久化回调 URI。授权码采用 PKCE S256、一次性、最长 60 秒；访问令牌有效 1 小时，刷新令牌缺省 30 天且轮转（每次使用都重新计时，可用 `ingress.oauth.refresh_token_ttl` 调到最长 365 天）。令牌保存在内存，入口重启后须重新授权。scope 收窄后建立新的 MCP 会话，使工具表和 owner 使用新的权限上下文。

## Linux L2

安装发行版 `bubblewrap` 和提供 `setsid` 的 `util-linux`，设置 `agent.policy.isolation: L2`。bwrap 隔离网络、提供临时 HOME，并绑定本次调用的工作目录。PTY 在 PID 命名空间内建立会话和控制终端，支持前台作业中断与窗口调整；终止启动器会清理该命名空间中的进程。主机必须允许相应 user namespace。受 AppArmor 限制的主机可为实际 bwrap 路径配置仅允许 `userns` 的执行 profile；管理员应按主机策略审核并加载。环境不满足时工具返回隔离失败。

```bash
XMESH_TEST_L2=1 go test ./internal/policy -run L2 -v
XMESH_TEST_L2=1 go test ./internal/pty -run L2JobControl -v
python3 scripts/smoke_local.py --binary bin/xmesh --isolation L2
```

## macOS L2

设置 `agent.policy.isolation: L2` 后，exec 与 PTY 使用系统 `/usr/bin/sandbox-exec`。配置按本次工作目录生成：目录可读写，系统程序和运行库目录只读，网络、Mach 服务查询和其他进程信息默认受限。`HOME`、`USERPROFILE`、`TMPDIR` 均指向工作目录，目录参数独立传递给沙箱启动器。需要访问主目录配置或系统服务的程序可能无法在此级别运行。

```bash
XMESH_TEST_L2=1 go test -race ./internal/policy -run L2 -v
python3 scripts/smoke_local.py --binary bin/xmesh --isolation L2 --pat --shell /bin/zsh
```

`sandbox-exec` 已被 Apple 标记为废弃，系统升级后应重新执行上述验收。系统缺少启动器时拒绝 L2 启动。当前实现允许读取 `/bin`、`/sbin`、`/usr`、`/System`、`/opt/homebrew`，以及必要的设备与目录元数据。

Linux 与 macOS 均拒绝把凭证目录或其祖先用作 L2 工作目录，检查 `.ssh`、`.config/gcloud`、`.azure` 和 `.aws`，包括已存在的符号链接目标。工作目录中的既有硬链接仍共享主机 inode；部署时应使用独立工作目录。Unix PTY 关闭会清理同一终端会话的进程组；主动创建独立会话的守护进程需要额外的生命周期约束。Windows 默认配置 L2 时拒绝命令和终端启动，只有显式开启实验开关才会使用 AppContainer，见下节。

## Windows L2

默认情况下 Windows exec 与 PTY 的 L2 请求返回 `sandbox.unavailable`。设置 `agent.policy.windows_appcontainer: true`（实验性，改动后需重启）后，`xmesh serve` 在状态目录的 `sandbox` 子目录建立 AppContainer 运行器，`isolation: L2` 的 exec 与 PTY 经它在独立 profile、工作目录授权和变更日志下执行。运行器使用守护 keeper 加内核写入围栏，并在启动时先回收上一次异常退出遗留的日志、授权与 profile：keeper 仍在时由它接管，keeper 已不存在（重启、注销或被终止）时用新围栏冻结工作目录后再回收。运行器创建或恢复失败时节点照常启动，L2 继续返回 `sandbox.unavailable`，`sys.info` 的 `warnings` 给出提示，原因写入节点日志。运行器失败的单次调用返回 `policy_denied`，规则名 `sandbox.runner_failed` 或 `sandbox.cleanup_pending`。stdio 后端的 L2 不经过托管运行器，仍然失败关闭。停止服务时先排空工具调用，再最多等待两分钟回收沙箱，服务停止等待相应延长。

该开关不代表安全条件已经满足：工作目录树内写入冻结、同用户 WRITE_DAC 阻断、后代安全描述符变更纳入围栏与完整对象普查仍在 [实现状态](implementation-status.md) 列出，普通账户和真实环境验收也尚未完成。

### Windows L2 的准入成本与工作目录规模

每次 `isolation: L2` 的 exec 或 PTY 调用都重新做完整准入与回收：建立独立 AppContainer profile，为工作目录里的每个对象授权并记入日志，读取一遍 USN 基线，启动监督进程和监控；结束时核对、回收每个对象的授权并删除 profile。成本随工作目录的对象数（目录、文件和根本身）近似线性增长，与命令本身无关。

GitHub `windows-latest` 上、不带竞态检测器、使用测试替身协调器的单次调用实测：

| 工作目录对象数 | 单次调用总耗时 | 其中准入 | 其中回收 |
| --- | --- | --- | --- |
| 约 2（空目录） | 0.17 至 0.22 秒 | 约 0.09 秒 | 约 0.05 秒 |
| 约 1000 | 0.9 至 1.5 秒（首次调用更慢） | 约 0.4 至 0.5 秒 | 约 0.4 至 0.5 秒 |
| 约 4000 | 约 3.2 秒 | 约 1.4 秒 | 约 1.6 秒 |
| 约 9000 | 约 7 至 8 秒 | 约 3 秒 | 约 3.4 至 4.6 秒 |

生产协调器（守护 keeper 加写入围栏）启动时多约 0.03 秒，关闭时在大目录上多至 0.2 秒。早先实现状态里记录的“单次约 1.4 秒”来自带 `-race` 的 CI 测试运行，该数字包含竞态检测器的开销，不代表发布二进制的耗时。

主要成本在逐对象的系统调用：约 9000 个对象时，授权（读取并写回 DACL）约占准入的三分之一到四成，重新打开每个对象取得身份句柄和第二次扫描各约五分之一，回收阶段的普查、DACL 回写和二次核对合计占回收的八成以上。这些步骤是 [CON-SEC](constraints/security.md) 要求的证明本身，不能为速度省略。

运行器因此不复用 profile，也不在两次调用之间保留已准入的执行：保留执行意味着工作目录在空闲期间一直被写入围栏冻结，用户自己的编辑会被判为外部写入者；复用 profile 则让并发的调用共享同一个沙箱身份，破坏调用之间的隔离。需要降低成本时缩小工作目录，而不是放宽证明。

逐对象的日志追加、授权意图与释放确认的落盘和描述符校验已是线性，不随对象数平方增长；授权意图整批一次落盘，先于第一次描述符修改；释放确认在对应对象回收之后整批落盘。一次调用的各阶段耗时在节点日志的 debug 级别以 `sandbox_execution_timing` 输出（`objects`、`admit_ms`、`launch_ms`、`cleanup_ms` 和各阶段明细）。在 Windows 上复现表中数字：

```powershell
$env:XMESH_TEST_WINDOWS_SANDBOX = "1"
$env:XMESH_TEST_WINDOWS_ADMISSION_COST = "1"
go test ./internal/winbox -run Test_WindowsAdmissionCost -v -count=1 -timeout=1500s
```

**对象上限。** 工作目录（含根、所有文件和子目录）最多 10000 个对象，这是运行器能为一次执行授权并逐个回收的上限。超过时调用返回 `policy_denied`，规则名 `sandbox.workspace_too_large`，附 `limit` 字段；检查在创建 profile、keeper 和围栏之前完成，不会留下状态。执行期间新增的对象使总数越界同样视为越界：监控终止这次执行，调用按 `sandbox.cleanup_pending` 报告。

`agent.default_cwd` 的示例值 `~` 指向用户主目录，几乎一定超过上限。启用 `agent.policy.windows_appcontainer` 时请把 `default_cwd` 设为具体的项目目录，或在每次调用里传入较小的 `cwd`；`node_modules`、构建产物等大目录也计入对象数。

`internal/winbox` 提供独立 AppContainer profile、能力为空的启动属性、profile 删除、工作目录绑定与回收原语。原语的原生核验使用临时 profile，在 Windows 会话中执行：

```powershell
$env:XMESH_TEST_WINDOWS_SANDBOX = "1"
go test -race ./internal/winbox -run WindowsAppContainer -v -timeout=90s
```

经 `xmesh serve` 的生产启动链核验（开关、运行器构建、启动恢复、每次调用挂接）需要先构建二进制：

```powershell
go build -o $env:TEMP\xmesh-l2.exe ./cmd/xmesh
$env:XMESH_TEST_WINDOWS_SANDBOX = "1"
$env:XMESH_TEST_WINDOWS_BINARY = "$env:TEMP\xmesh-l2.exe"
go test ./internal/app ./cmd/xmesh -run WindowsL2 -v -count=1 -timeout=900s
```

AppContainer 启动环境通过 `winbox.Environment` 从 Windows Known Folder 补全 `LOCALAPPDATA`；调用方继续提供经过策略过滤的其他变量。该用例验证父进程环境哨兵未继承、profile 内目录写入、目录外文件读写拒绝、父进程内存读取拒绝、已知可达的环回监听访问拒绝，以及进程 token 的 AppContainer 身份与空 capability 集合。环回网络拒绝核对 helper 的准确 AppContainer SID，通过 Windows 网络隔离 API 确认该 SID 在探测前后均没有环回豁免，并验证宿主在探测前后均能连接、监听端未接受沙箱连接；单独的连接超时不作为通过证据。清理后检查 profile 目录已删除。原语的原生结果见 [Windows 验收记录](validation/2026-10-15-windows.md)。

内部单对象授权原语为准确 profile SID 添加显式、无继承的读写执行 ACE，并通过保留的元数据句柄跟踪对象；回收重读当前 DACL，只删除本次 ACE。原生用例验证普通 Medium 完整性文件与目录的授权写入、标签保持、未授权子目录与目录外拒绝，以及文件和目录改名、原路径替换后的对象回收。最终重解析点、硬链接和 null DACL 会拒绝授权。授权前按对象身份取得并保留独立的 `READ_CONTROL|WRITE_DAC` 句柄，直至准确 ACE 回收成功；对象进入 delete-pending 后可通过该句柄回收。ACL 通过 `NtSetSecurityObject` 直接更新所选对象，并显式保留 DACL 保护与自动继承状态。根路径固定时的更新、控制位、标签和子对象保持已通过 Windows 原生验收；delete-pending 文件与目录用例在 `0f0536a` 的 PR 原生任务通过。同进程 ACL 更新串行，其他 ACL 写入方需与工作目录所有者协调。进程退出并释放授权后才能删除 profile。

工作树检查原语从本地卷根逐组件按句柄打开目录，固定工作目录及祖先的绑定；目录枚举中的 128 位对象 ID 必须与打开后的对象一致。扫描拒绝重解析点、硬链接、重复身份、对象超限及不完整结果，支持取消，关闭时释放全部句柄，扫描本身保持 ACL 不变。枚举缓冲区显式按八字节对齐。保留的子文件句柄会使 Windows 拒绝包含这些文件的目录改名；执行期授权需要独立处理这一生命周期，当前检查原语尚未接入工具启动路径。

已选对象的单对象授权通过非继承句柄复制保留实际对象，授权前重新核对卷号、对象 ID、类型与链接属性。快照关闭与授权释放各自管理所持句柄；调用方应串行完成选择、授权和快照关闭。内部工作树授权覆盖既有与未来新对象，按准确 profile SID 回收；失败保留 profile 和根 guard 供重试。`c827749` 原生验收通过新对象读写、准确内核 DACL/控制位恢复及回收后的访问拒绝。当前授权仍保留子句柄，可编辑目录的执行期寿命、跨进程协调、持久异常退出恢复和工具执行接入继续作为生产门。

工作树内部准入会以当前进程 token 的 profile 与 RoamingAppData 定位默认凭证目录，按实际卷号和对象 ID 排除相互包含及目录别名。已存在前缀以目录数据访问和禁止 delete sharing 的句柄固定，保留至完整回收。调用方须协调外部命名空间、reparse 数据及缺失凭证组件创建；Windows L2 生产启动仍等待继承回收、寿命、跨进程协调及恢复验收。

## 治理与生命周期

exec 与 PTY 只继承程序搜索路径、用户目录、系统目录及语言设置，并过滤凭证名称和 `.ssh`、`gcloud`、`.azure`、`.aws` 路径。Windows 还保留运行时所需的 `WINDIR`、`SYSTEMDRIVE`、`COMSPEC`、`PATHEXT`、`TEMP`、`TMP`、`APPDATA`、`LOCALAPPDATA` 和 `ProgramFiles`，同样经过路径过滤。Windows 保留 `PSModulePath` 中经过拒绝规则的模块目录；缺少该值且 SystemRoot 可用时传入空值，由所启动的 PowerShell 运行时初始化模块目录；拒绝变量名的策略仍然生效。继承的 `PATH` 和 Windows `PSModulePath` 按平台分隔符逐项过滤，保留其余搜索目录；调用方显式传入的搜索路径包含拒绝目录时返回策略错误。调用方的 `env` 经过同一过滤器。Windows 按大小写不敏感的变量名覆盖继承值，调用方同时提供 `Path` 和 `PATH` 等重复键时返回参数错误；Unix 保持大小写敏感。

Agent 与 Gateway 的工具调用预算缺省为每主体每小时 10000 次、并发 64、单次最长 10 分钟，可分别用 `agent.quota`、`gateway.quota` 的 `calls_per_hour`、`max_concurrent`、`max_call_duration` 调整（`agent.quota` 热加载）。`exec.run` 的 `timeout` 超过 `max_call_duration` 时被拒绝（`invalid_args`），需要长命令时调高该值。Agent 的 `sys.info` 免计数。Agent 主体优先采用可信 OAuth client，其余按节点 ID 计数，多个 MCP 会话共享预算。PTY 的设备与 caller 限额之外，同一主体还共享 `max_per_caller` 的 active/orphaned 额度；转移计入接收方额度。

入口按 `ingress.max_pty_per_client`（默认 8）聚合各设备的终端占用，串行准入 open/transfer。所有发现设备须支持并授权入口读取 PTY 控制资源；资源不可确认时返回 `quota.unavailable`。保留状态目录中的 `ingress-pty.json`，入口重启后会据此核对仍有资源的设备。设备离线时恢复连接；调用结果丢失且留下待完成标志时，重启对应 Agent 后重新确认。关闭现有终端仍可执行。多入口进程的协调边界见 [CTR-GW-06](contracts/gateway.md#ctr-gw-06)。

同一主机运行多个入口时，各入口可设置相同的 `ingress.pty_budget_file` 绝对路径，例如 `/var/lib/xmesh/ingress-pty.json`。使用独立的入口状态目录、相同的 `max_pty_per_client`、同一 OAuth client 身份域和支持文件锁的版本；预算目录须仅允许受信任的入口进程写入，并位于本地文件系统。运行期间保留该文件及相邻的 `.lock` 文件。升级多个入口时先全部停止，升级完成后再启动。等待预算锁可随请求取消；退出进程自动释放锁，预算记录继续参与恢复核对。

多台主机各运行入口时，在每台入口的配置里设置相同的 `ingress.pty_budget_authority`，值为一台长期在线、所有入口都能连到的 Agent 的设备名（例如 `"pty_budget_authority": "home-server"`）。该 Agent 无需额外配置；入口须是 `tag:ingress` 并获得 `pty.open`/`pty.transfer` 授权。各入口仍须使用相同的 `max_pty_per_client` 和同一 OAuth client 身份域。权威 Agent 离线时所有入口的新增与转移返回 `quota.unavailable`；重启权威 Agent 前先确认没有结果不确定的终端调用，重启后约 30 秒内不会发放租约。续见 [CTR-GW-06](contracts/gateway.md#ctr-gw-06)。

审计写入状态目录的 `audit`，默认保留 720 小时；记录工具、路径、状态、耗时等元数据。PTY 读操作按分钟汇总字节量，审计编码器限制可写字段。

`/control/drain` 只接受可信 `tag:admin` 控制请求。`keep` 拒绝新的 exec、PTY 与后端占用，已有资源按期限结束；`kill` 清理本机 PTY 与租约。`scope:mesh` 使收到消息的进程的 Agent 与 Gateway 进入 drain。用 `xmesh drain --scope mesh` 向所有在线设备逐台发送，`--scope device --device NAME` 只下线一台，`--mode kill` 同时终止 PTY 并释放租约；版本 2 由运行中的管理节点通过本机控制通道发送远程排空；在各目标设备的 xmesh 信任中为管理设备授予 `tag:admin`，网络控制面标签不授予该权限。其他节点的 Gateway 一旦在调用中看到 `mesh.draining` 就停止路由该设备。重启重置内存 drain 状态。

`install` 使用持久位置的二进制与配置。macOS 使用 LaunchAgent，Linux 使用 systemd 用户服务。Windows 默认注册当前用户的登录计划任务，以 `InteractiveToken` 和 `LeastPrivilege` 配置运行，登录后启动；保持该用户登录以运行 GUI 后端。任务直接执行二进制，保留配置所在工作目录，持续运行并在失败后每分钟重试，最多 999 次。同一用户的同名任务已存在时安装失败，应在任务计划程序中检查后再更新。任务名会打印在安装结果中，可在任务计划程序中立即运行、停止和删除。

Windows 可用 `install --system` 选择管理员 SCM 服务，默认以 LocalSystem 身份运行于 Session 0，状态目录默认 `%ProgramData%\xmesh\<节点名>`（可用 `--state-dir` 覆盖）；配置、状态和工作目录须允许该账户访问。已有 SCM 安装迁移到用户任务时，先停止并卸载原服务，避免同一节点的两个进程同时占用状态与监听端口。用户任务运行于登录会话；SCM 适用于系统服务场景。

Windows 用户服务（内核和 `computer host`）的任务程序为同目录的 `xmesh-background.exe`，使用 Windows GUI 子系统，启动时不创建控制台。CLI 仍使用 `xmesh.exe`；安装检查后台二进制存在且确为 GUI 子系统，旧任务在 `start` / `restart` 时仅更新程序路径，保留参数、工作目录、账户、自启和重试设置。原生桌面子进程使用 `CREATE_NO_WINDOW`，保留匿名协议管道。桌面服务仍运行在交互用户会话，不能放到 Session 0。从源码构建时需同时运行 `go build -o bin/xmesh.exe ./cmd/xmesh` 和 `go build -ldflags=-H=windowsgui -o bin/xmesh-background.exe ./cmd/xmesh`。

用户任务设置 `LeastPrivilege`；[Windows 的 RunLevel 规则](https://learn.microsoft.com/en-us/windows/win32/taskschd/taskschedulerschema-runlevel-principaltype-element)规定内置 Administrator、LocalSystem、LocalService 或关闭 UAC 时忽略该设置。需要普通权限时使用标准账户或启用 UAC 的普通管理员账户。

服务运行异常以非零状态退出，供管理器处理。Windows 原生验收从目标用户的登录会话执行：

```powershell
$env:XMESH_TEST_WINDOWS_USER_SERVICE = "1"
go test ./cmd/xmesh -run WindowsUserTaskRuntime -v
```

该用例创建临时任务并立即运行，核对用户 SID、会话 ID、token 提升类型、工作目录与复杂参数，最后删除任务。有 UAC 分离 token 时要求非提升运行；无分离 token 时核对账户原有提升状态并记录环境边界。Windows 原生 CI 记录 Runner 会话号；处于交互会话时运行此用例，Session 0 时标记环境条件未满足。登录事件触发仍需目标用户环境验收。自动测试覆盖与各平台原生安装的验收状态见[实现记录](implementation-status.md)。

SCM 原生验收在管理员 PowerShell 中执行：

```powershell
go build -o .\bin\xmesh-scm-test.exe ./cmd/xmesh
$env:XMESH_TEST_WINDOWS_BINARY = (Resolve-Path .\bin\xmesh-scm-test.exe).Path
$env:XMESH_TEST_WINDOWS_SYSTEM_SERVICE = "1"
go test -race ./cmd/xmesh -run WindowsSystemService -v -timeout=180s
```

用例通过 CLI 安装带独立名称的临时 SCM 服务，读回安装参数与账户配置，再以显式本机模式启动实际二进制。验证 LocalSystem SID、Session 0、MCP 文件读写、正常停止与进程/监听退出，最后卸载服务并确认注册项删除。该用例验证本机服务生命周期；生产设备网络连接与用户登录任务仍按对应场景验收。

Windows 普通命令与 ConPTY 进程通过 `PROC_THREAD_ATTRIBUTE_JOB_LIST` 在创建时加入专属 Job Object；shell 自行退出、TERM 宽限结束和 KILL 均清理其后代进程。普通命令只继承显式标准输入、输出和错误句柄，主进程退出后先关闭 Job 再等待输入复制结束。创建期 Job 属性需要 Windows 10 / Server 2016 或更新系统。Microsoft Store 的应用执行别名（`WindowsApps` 下的 `pwsh.exe` 等）会拒绝该属性（`ERROR_ACCESS_DENIED`）；此时改为换一个从未被引用过的新 Job，不带该属性挂起创建，在恢复主线程之前加入 Job 并核对成员关系，核对失败则终止仍挂起的进程，不会在 Job 之外运行任何指令（沿用失败创建中引用过的 Job 会让之后每次加入都被拒绝）。Store 包激活会把进程的后代放进该包自己的 Job，关闭 exec 或终端时无法结束它们，所以应用执行别名（重解析点标记 `IO_REPARSE_TAG_APPEXECLINK`）不作为命令启动：`exec.run` 的 `cmd` 和显式指定的 `default_shell` 指向别名时返回 `invalid_args`，`reason` 为 `app_execution_alias`，并带 `path`；改用 `%ProgramFiles%\PowerShell\7\pwsh.exe` 这类真实安装路径。`default_shell: auto` 跳过别名形式的 `pwsh`，依次试 PATH 上的真实 `pwsh`、`%ProgramFiles%\PowerShell\7\pwsh.exe`、`powershell`；选中的 `pwsh` 无法启动时仍退回 `powershell`，显式指定的 shell 不替换。后代逃出 Job 的现象依赖真实的 Store 包，CI 无法安装，需在装有 Store 版 pwsh 的机器上人工验证：以别名路径调用应被拒绝，以真实路径启动的 pwsh 在关闭后其 `Start-Process` 子进程应一并结束。exec 与 PTY 的 `internal` 错误带 `win32_error` 与 `reason`。

Windows 上 exec 的 stdout/stderr 不是合法 UTF-8 时（简体中文系统的 cmd.exe、Windows PowerShell 5.1 默认写 GBK），内联结果和 `exec.output` 分页按系统 OEM 代码页转成 UTF-8；用 `python -X utf8` 等写 UTF-8 的程序不受影响。原始字节仍在输出文件里。程序实际写的是 ANSI 而非 OEM 代码页且两者不同（例如西欧系统上的部分 Python 程序）时，字符可能转错，需要程序自己输出 UTF-8。INT 通过终端输入 Ctrl-C，应用的原始输入模式可能改变其行为。Windows 10 1809 或更新系统上的原生验收：

```powershell
$env:XMESH_TEST_WINDOWS_PTY = "1"
go test ./internal/pty -run WindowsPTY -v -timeout=120s
```

该用例检查 Ctrl-C 后继续交互、窗口调整、TERM/KILL/shell 退出后的后台进程清理，以及退出输出读取和重复关闭。

Windows Job 创建期归属的原生回归：

```powershell
$env:XMESH_TEST_WINDOWS_EXEC = "1"
go test -race ./internal/winjob -run WindowsJobAtomicAdmission -v -timeout=60s
```

用例在宿主 `CreateProcess` 返回、子进程仍挂起时直接终止宿主，检查已绑定 Job 的子进程退出；独立对照保留未绑定的挂起子进程，随后按精确进程句柄清理。机制和最低系统版本见 [Windows 进程属性说明](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute)。原生结果见 Windows 验收记录。

内部 Job 退出确认原语可用 `$env:XMESH_TEST_WINDOWS_EXEC = "1"; go test -race ./internal/winjob -run WindowsJob -v -timeout=60s` 验收。`WaitEmpty` 核对内核活动计数和每个生命周期进程句柄的退出信号，`TerminateAndWait` 在核对清单后请求终止并等待；等待支持取消并最多 30 秒。可信调用方须证明每个句柄属于该 Job，自准入起保留所有根及后代的句柄（包括已退出成员），总数须与内核累计分配计数一致，最多 10000 个独立身份；缺失、重复和未追踪的短生命周期成员保留错误。调用方保持句柄、禁止新进程加入，并在回收关联资源前持续保持准入静止。终止请求已发出后取消等待仍保留恢复义务，调用方可使用原句柄重试；单 Job 的空状态不能证明同一 profile 在其他 Job 中的进程已退出。AppContainer 测试 helper 已采用整棵 Job 退出确认；生产执行协调器接入与新增用例的原生结果继续验收。

同机多入口 PTY 原生验收可使用同一 `XMESH_TEST_WINDOWS_PTY` 环境运行 `go test -race ./internal/role/gateway -run IngressPTY -count=3 -v -timeout=180s`。用例覆盖两个入口子进程对两个 Agent 的并发准入、共享状态、转移、孤儿恢复和持久化阻断。

向 Windows 交互终端输入命令时，以 `\r` 表示 Enter。发送 INT 后等待 shell 输出新的提示符，再发送后续命令。

Windows exec 同样挂起创建主进程，加入专属 Job 后恢复主线程。主进程退出会立即清理后代，包括继承 stdin 管道的后台进程。若系统无法唯一识别挂起主线程、校验其所属进程或加入 Job，命令启动失败并回收进程。原生验收：

```powershell
$env:XMESH_TEST_WINDOWS_EXEC = "1"
go test ./internal/exec -run WindowsExec -v -timeout=120s
```

用例检查准入前暂停、恢复后执行、正常退出和超时清理，以及大输入被后代持有时执行调用的结束。

Windows 的 `fs.write` 和 `fs.edit` 使用 `FileRenameInfoEx` 的 POSIX 替换语义。Agent 文件读取句柄允许删除共享：已有读取句柄继续读取旧内容，之后打开的句柄读取完整新内容。其他应用以禁止删除共享的句柄占用目标、文件系统或系统版本不支持该操作时，写入返回错误，保留目标并清理临时文件。

现有快照对象可组合授权：按选择顺序准入，取消或某对象失败时按逆序回滚。集合释放会继续清理其他对象，保留失败对象的句柄供重试，并保持 profile 的活动授权计数；命名空间 guard 由快照独立持有。该组合仍只覆盖所选已有对象，新对象继承、执行期工作树寿命与异常退出恢复仍需完成。

内部工作树授权入口 `grantWorkspace` 使用独占 profile，为所有已选对象添加显式 ACE，并让目录 ACE 向后续对象继承。关闭前调用方必须结束全部沙箱进程并协调外部文件及 ACL 修改。关闭先撤下显式授权，再在固定根下重扫、按对象句柄清理该 profile 的已知继承 allow ACE；其他 profile 和不透明 ACE 保持。扫描或清理失败时继续保留 profile 引用及根 guard，调用方必须保留返回对象并重试 `Close`。新对象也受扫描数量上限约束。准入完成前事务式切换后代授权为按卷/文件 ID 打开的安全句柄与不可继承的窄权限身份副本，再释放按路径打开的后代句柄；准备失败保留原句柄供回滚。根、祖先及按 token 实际身份检查的凭证边界 guard 保留至完整回收。真实 AppContainer 的非空目录改名、路径替换、新对象创建及准确描述符回收已通过原生验收。该入口尚未接入生产 L2；执行期完整状态追踪、命名空间协调和持久恢复继续实现。


AppContainer ACL 读取、合并、写入使用按卷号和 128 位文件 ID 命名的 Global mutex，在获取与释放之间固定 OS 线程。多个进程对同一对象的授权/回收串行，改名不改变协调身份；无法打开 mutex 或等待超过 30 秒时停止操作。默认 DACL 决定可参与协调的安全上下文，跨用户或服务会话部署须独立验收。外部 ACL 写入方须使用同一协议或暂停写入；外部命名空间/reparse 修改和缺失凭证组件创建仍由调用方协调。进程退出后 mutex 可交给下一所有者并报告 abandoned，后续编辑重读当前原子描述符；profile/授权 journal 的崩溃恢复仍是独立生产门。

内部持久恢复 journal 使用独占、非继承句柄和仅当前用户/SYSTEM 可访问的受保护文件 DACL。`openJournalDirectory` 通过已固定父句柄创建带 owner/SYSTEM 完全访问、OICI 继承标志及受保护 DACL 的私有目录；现有目录须通过相同安全描述符验证。`openBoundGrantJournal` 按目录句柄相对打开文件，并保持目录及全部祖先绑定至 journal 关闭，损坏文件的已验证前缀也持有绑定。多个独立所有者可共用目录，单个 journal 仍独占；关闭文件失败也释放目录绑定并保留错误。工作区和状态目录按卷/ID 双向排除相互包含，独立显式对象同样排除状态子对象和目录祖先。调用方仍需协调原地 reparse/ACL 写入。绑定入口的目录寿命、安全/边界及九种强制退出恢复已通过原生验收。

每条意图先同步落盘，再创建 profile 或修改 ACL，单项清理与工作区继承清理分别刷盘回执，最后删除 profile 并记录 complete。损坏、截断、写入失败和未能核实的对象身份保留恢复错误与原文件。journal 存储、显式对象异常退出恢复及工作区快照/初始身份与当前树并集回收均已通过 Windows 原生验收；工作区三种强制退出阶段的工作流与逐项证据见 [Windows 验收记录](validation/2026-10-15-windows.md)。

工作区 journal 以 `workspace-created` 批次累计已核实的新增卷号/128 位文件 ID/类型；初始身份与全部历史新增身份合计最多 10000，和当前树的并集也受同一限额约束。内部 `checkpointJournalWorkspace` 要求调用方串行日志并协调外部 ACL/命名空间/reparse 写入，只读预检后刷盘已观察身份，可用于待确认执行期间；它不能证明完整事件覆盖。清理在全部身份和描述符预检后、首次 ACL 修改前刷盘本次发现的新增身份，重试按 ID 重开移到根外的历史对象。恢复前必须结束并等待所有 profile 进程，保持外部 writer 协调，并确认全部新对象均已覆盖；无法核实的缺失或删除身份保留义务。恢复回调收到独立复制的新增清单。根路径替换、硬链接、重解析点、不完整扫描、合并超限及日志追加失败均在首次 ACL 修改前拒绝。已有日志仍可读取；含新增记录的日志须用本版本或更新的恢复二进制，旧版本会拒绝新记录并保留错误。完整事件追踪、漏事件检测、删除证明、生产协调器和 exec/ConPTY 接入完成前，生产 Windows L2 保持 `sandbox.unavailable`。


内部 `recoverJournalDirectory` 对私有状态目录执行有界 sweep：目录仅接受普通 `.journal` 文件，最多 256 份，批次已验证记录合计不超过 64 MiB/65536 条。绑定入口的文件创建与恢复扫描按目录卷号/ID 使用同一 Global mutex，等待支持取消并限时 30 秒；跨用户/服务会话部署仍须验收。扫描、枚举身份核对与 profile/SID 重复检查先于恢复。调用方须提供协调回调，确认并保持进程已结束、外部写入协调和完整新对象清单，返回持有这些条件及所需卷句柄的非空 guard；回调不得在同一目录创建 journal 或重入恢复。guard 持有至完成记录刷盘后，失败回调已返回的 guard 也会释放。每份记录返回 retained/busy/complete/recovered 与错误；busy、损坏或未完成记录会使整体调用返回错误，独立有效记录仍可完成。完成 journal 保留，归档须在准入与恢复均静止时进行。调度器已在 Windows 原生回归中通过混合状态、预检/guard、跨进程创建协调及九种强制退出恢复验收；实际执行协调器仍须接入，生产 L2 门禁保持。


内部 `NewProcessCensus` 在空私有 Job 准入前收集并保留真实成员句柄，包含系统自动加入的 console host；通知 PID 按 Job 归属核实，句柄保留至确认及资源回收结束。普通通知可能丢失，累计分配数与保留身份清单不一致时，确认返回错误并保持恢复义务。调用方须停止所有新准入；Close 仅释放观察器资源，不能代替退出确认或 ACL/profile 回收，且其独立 Job 句柄参与最后句柄关闭时的终止策略。生产协调器接入继续验收。

### Windows 活动执行进程所有者

内部 `executionProcesses` 为一个活动 profile 管理有界启动 Job 集合，串行化创建与准入封闭。根进程创建时绑定 Job/AppContainer，并在恢复前固定身份句柄；回收先向全部 Job 请求终止，再确认完整清单与所有退出信号。部分失败继续保留所有者及 profile 引用，须重试成功后释放句柄；`Profile.Close` 会拒绝尚有进程所有者的 profile。使用过独立启动属性的 profile 不能转入该受管模式。ACL/journal 与外部 writer guards 须由调用方保持到回收完成。该入口已用于 AppContainer 工作区 helper；异常宿主恢复、完整新对象追踪、外部 ACL/命名空间/reparse 写入协调及 exec/ConPTY 接入仍待完成，生产 Windows L2 继续返回 `sandbox.unavailable`。


内部 NTFS 变更追踪在初始工作树授权完整且尚未执行时调用 `beginWorkspaceChangeTracking`；调用方自初始快照起协调外部 writer，并串行日志操作。入口核对精确初始清单，通过保留卷根的 GUID 打开既有可读 USN 日志，刷盘日志 ID/起始游标。`checkpointWorkspaceChanges` 可在执行期间有界消费流，持久登记按 ID 核实的新增对象后推进游标；每次至多 65536 条事件，累计身份上限 10000。读取前后验证日志身份及保留区间，日志重建、截断、格式异常、限额和不可打开身份保留错误。该路径要求 NTFS 与相应卷读取权限，并不创建 USN 日志或启用特权。

启用追踪后，目录观察和清理先消费到当前 USN 水位，扫描发现流未覆盖的身份会拒绝，恢复可找回两次扫描间移出根的已创建对象；历史目录移出根后仍是继承源。恢复回调收到复制的游标。删除、硬链接/reparse 变更和未知导入对象当前保留错误，须继续实现删除与导入子树证明、周期 checkpoint 及生产执行协调。外部 writer guard 和进程退出回执仍是 ACL 回收前提。含追踪记录的日志需保留本版本或更新恢复二进制；已有日志可继续读取。Windows L2 生产门禁保持 `sandbox.unavailable`。

内部 USN 删除回执要求完整流中的最终 `FILE_DELETE | CLOSE`、准确身份和当前 NTFS 占用记录查询证明原槽位未占用或序列号已更替同时成立。每次 checkpoint 先读取完整有界水位，再依次刷盘新增身份、`workspace-deleted` 与游标；重放支持每个完整前缀。回收重新确认删除 ID 不存在，清理其他对象后确认已删除初始对象的 grant。查询权限、几何/记录格式异常与仍占用的原身份保留义务；初始对象的存活身份句柄延迟最终 close 时须由生产协调器处理句柄生命周期，当前强退恢复可在宿主句柄关闭后重试。累计上限包含已删除对象。恢复清单中的删除回执独立复制；新记录需本版本或更新恢复二进制。导入子树证明、周期 checkpoint、外部 writer 和生产执行接入继续实现，Windows L2 保持 `sandbox.unavailable`。

追踪工作区的内部正常关闭路径先确认执行退出并验证持久所有权，再释放后代身份/安全句柄，补读最终 close 事件并按持久 ID 回收；根/祖先/凭证绑定和 profile 引用持续保持到成功。外部数据句柄、无法重开身份或追加失败使关闭保留义务，重试继续使用持久身份清单；句柄移交后的新进程和授权准入持续封闭，完成后所有 grant 引用只释放一次。执行期间的独立 checkpoint 仍可能等待最终 close，生产协调器须串行日志操作、保持外部 writer guard 并接入本关闭协议。

内部 `workspaceChangeMonitor` 在追踪已启用、唯一执行所有者尚未准入 Job 时建立，先执行一次补读，再周期刷盘 USN 身份和游标。调用方须持续持有外部命名空间/reparse/ACL writer guard。后台错误或生命周期取消会封闭新准入并以独立期限终止/确认 Job，写盘失败保留执行回执义务；根与凭证 guards/profile 引用留到回收成功。仍受初始句柄阻塞的最终 close 和缺少导入/链接证明会触发该故障路径。正常 Stop 先要求持久退出确认，并等待在途 checkpoint；等待超时保留附着，完成后才可 Close 工作区。Wait 期限只影响观察，故障结果可重复读取。生产协调器仍待接入，Windows L2 保持 `sandbox.unavailable`。


监视器先核实保留的初始身份句柄，读取内核 `DeletePending`，将观测刷盘为 `workspace-delete-pending`；NTFS 可延迟发出删除事件。执行期 USN checkpoint 对尚未最终关闭的已登记删除对象，先持久记录 `workspace-delete-intent` 再推进游标。意图保持 live census 与回收义务，监视器继续有界追踪；进程退出确认前保留 ACL 和初始身份句柄。后续最终 `FILE_DELETE | CLOSE` 与准确 NTFS 不存在证明共同构成删除回执。撤销 disposition 后仍存活的对象按正常身份核实回收 ACL。两类观测历史保持在 journal 和独立恢复清单中，每个完整写盘前缀可重放；含此记录需本版本或更新恢复二进制。尚未登记且无法按 ID 核实的 delete-pending 对象、最终关闭时不存在证明不足及历史/导入/link/reparse 证明异常保留错误；生产协调器和 Windows L2 门禁继续按实现状态文档执行。


受协调导入先调用内部 `admitImport`，完整扫描同卷源目录子树或单文件，核对 namespace/journal/凭证边界并刷盘 `workspace-import`；成功后由可信协调器执行移动与授权。协调器须从扫描前冻结所有源 namespace/reparse/ACL writer 并保持到移动结束，以及保持工作区外部 writer guard。已持久准入的源及其后代在移动取消、再次移出或宿主退出后仍按卷/ID 保留回收义务。原语不修改命名空间或权限。完整导入证明包含源根、全部身份和准确 USN 游标，恢复清单独立复制；重叠身份只计一次，累计限额含导入和删除对象。未知的未经协调导入继续保留错误。新记录需本版本或更新恢复二进制；生产协调事务、writer 生命周期与 exec/ConPTY 接入完成前，Windows L2 继续按实现状态文档保持门禁。

内部 `moveImport` 组合完整源清单、目标父身份绑定、同卷句柄移动和逐对象访问授权。调用方从进入前到返回后持续冻结源和目标 namespace/reparse/ACL writer，并保留工作区外部 writer guard。目标必须是实际工作区内已追踪目录的空闲名称；现有目标保留。返回的 moved 标志反映实际移动；移动后的错误封闭执行并终止受管 Job，清单保留回收义务，ACL 回收保留 namespace 移动。生产外部 writer 生命周期与 exec/ConPTY 接入仍待完成。


内部 Windows 工作区操作以卷序列号协调参与协议的 namespace writer。`grantWorkspace` 在完整扫描前取得 Global mutex；活动对象的顺序固定为 operation gate、卷级 mutex、profile mutex，覆盖导入、监视 checkpoint 和回收。同一 goroutine 在操作结束释放 mutex；等待可取消且最长 30 秒。持锁进程退出会返回错误，须完成原有 journal/对象身份恢复条件再继续。该协议只约束参与的操作，部署仍须冻结其他 namespace/reparse/ACL writer，并在进程退出及完整 ACL 回收前保留外部 guard；生产 Windows L2 继续返回 `sandbox.unavailable`。跨用户或服务 session 的 mutex 访问权限仍须在目标环境验收。

工作区 journal 恢复同样先核对并固定原始根身份，再取得卷级 namespace lease，持有至 ACL 回收和 profile 完成记录刷盘。目录恢复 sweep 的 context 用于 namespace、USN、身份清单与 ACL 锁等待；取消后 journal 保留尚未完成的义务，调用方继续保留外部 guard 并重试。已持久 workspace-released 的日志可直接继续 profile 收尾。参与协议的互斥不能替代外部 writer 协调和完整 census 证明；生产 Windows L2 的门禁条件继续适用。


受管 AppContainer 的内部标准流入口要求可信调用方显式提供三个有效文件/管道句柄。启动使用私有可继承副本及创建时句柄白名单；调用方负责选择流的权限、管理原句柄和排空输出。输出流可能访问工作区 ACL 以外的对象，应作为执行能力一并审查。stdout/stderr 可共用文件；原句柄的继承标记和寿命保持。无效流在新建 Job 前拒绝，创建进程失败后的 Job 仍须回收。该原语支持独立监督路径；ConPTY 与生产 L2 执行准入继续按实现状态文档推进。


受管 AppContainer 的内部 ConPTY 入口接受调用方持有的有效 HPCON，创建时同时应用 pseudoconsole、Job 和 AppContainer 属性；身份登记完成后才恢复进程。调用方负责输入、窗口调整和持续排空输出，保持 HPCON/管道至全部附着进程退出；关闭 HPCON 时仍须并发读取输出以完成 flush。受管回收先封闭准入并终止/确认全部 Job，再由调用方关闭终端资源，独立监督路径使用相同协议。该能力原语的原生验收和生产工具接入状态见实现状态及 Windows 验收记录，生产 Windows L2 门禁继续适用。

内部 `workspaceExecution` 要求可信调用方在准备开始前取得并持续持有外部 namespace/reparse/ACL writer guard；该 guard 覆盖 journal、profile、授权、USN 追踪、进程与监视器的完整寿命。准备返回所有者及错误时，保留所有者并调用带 context 的关闭接口重试。关闭封闭新准入，确认全部 Job 与持久进程清单回执，停止监视器并释放进程所有者，再按身份回收 ACL、删除 profile、刷盘 complete、关闭 journal，最后释放 guard。取消或期限到达保留清理义务；guard 关闭失败保留重试状态，poisoned journal 保留供恢复。普通进程与 ConPTY 启动检查生命周期取消并串行准入。外部 writer 围栏、异常宿主后的 keeper、未知导入归并和工具层 exec/ConPTY 集成已由 `agent.policy.windows_appcontainer=true` 接入生产启动路径；该开关默认关闭，关闭时 Windows L2 保持 `sandbox.unavailable`。开启后仍有的限制：完整 census 不能证明子树完整，普通账号读取变更日志未做真机验收，整机重启或外层 Job 终止时失败关闭。

协调导入由生命周期串行入口调用受信源协调器；协调器接收源、目标路径，在 census/移动前协调外部 namespace/reparse/ACL 写入者，并持续保护移动后的导入对象身份，覆盖完整清理期间直至 guard 成功释放。guard 随准备错误一并返回时仍由生命周期接管；最多保留 256 个。关闭完成进程回收、ACL 恢复及 profile/journal 持久化清理后，逆序释放源 guard，再释放原 workspace guard；失败时保留所有者并重试关闭。宿主故障后的保护须由独立存续的协调机制提供。生产外部协调及工具层 exec/ConPTY L2 集成已装配在 `agent.policy.windows_appcontainer`（默认关闭）之后，默认情况下 Windows 生产 L2 仍保持 `sandbox.unavailable`。


### Windows 独立 guard keeper 与未知导入

内部 `launchGuardKeeper` 在 journal 与 profile 创建前启动 `xmesh __sandbox-guard`，固定工作区根、祖先链与 journal 目录链并在宿主退出后保持；宿主死亡后恢复入口 `recoverJournalDirectoryWithKeepers` 通过仅当前用户与 SYSTEM 可访问的私有命名管道接管，keeper 仅在读到已绑定 journal 的持久 `complete` 记录后同意释放。缺少 keeper 时默认失败关闭。keeper 不冻结树内 ACL/reparse 写入，整机重启或外层 Job 的 kill-on-close 会连同 keeper 一起结束；该机制由 `agent.policy.windows_appcontainer`（默认关闭）装配进生产启动路径。

活动执行观察到未知导入（未登记且无创建记录的对象，例如被外部移入的目录）时，监视器以 `errWorkspaceUnknownImport` 终止执行，journal 持久记录 `workspace-unknown-import`；关闭仍可完成回收并向调用方返回该错误，之后不再接受协调导入。含该记录的 journal 需要本版本或更新的恢复二进制。该归并不证明子树完整；这是开启 `agent.policy.windows_appcontainer` 之后仍然存在的限制，开关默认关闭，关闭时 Windows 生产 L2 保持 `sandbox.unavailable`。

内部 `workspaceWriterFence` 把工作区与 journal 私有目录的祖先链以无 delete 共享的句柄钉住，并核对身份、reparse、硬链接、DACL 指纹及 NTFS 变更日志；`RequireWriterFence` 开启后生命周期拒绝无法 Verify 的 guard，违规使监视器终止执行，清理仍完成并返回违规。读取变更日志需要卷设备访问，普通账号须在目标账号上验收；该围栏在生产路径上只随 `agent.policy.windows_appcontainer`（默认关闭）启用，且不随宿主崩溃存活。

## 本机控制通道

内核启动后在状态目录旁开一条只给本机用户的控制通道，供桌面 UI 和 `xmesh control` 使用。设计见 [12 桌面 UI 与本机控制通道](design/12-ui-and-control.md)。它不是 TCP 监听，不挂在 Gateway、入口或 MCP 上，`tools/list` 里没有它。通道起不来时节点照常运行，只在 stderr 打印「控制通道未启动」。

| 平台 | 端点 | 谁能连 |
| --- | --- | --- |
| macOS、Linux | 状态目录下 `control.sock`（`0600`，目录 `0700`）；路径超过 100 字节时落到 `/tmp/xmesh-<uid>/<节点名>.sock`（固定位置，不随 `$TMPDIR` 变化；契约写作 `control.sock`；加上节点名是为了同一用户的多个节点不互相占用）。回退路径仍超过 100 字节时启动失败，错误给出长度与上限，此时缩短节点名 | 对端 uid 必须等于内核运行用户（`SO_PEERCRED` / `LOCAL_PEERCRED`），其他用户的连接直接断开 |
| Windows | 命名管道 `\\.\pipe\xmesh-<SID>-<节点名>`，创建时拒绝远程客户端，名字不可被抢占 | DACL 只含运行用户、`SYSTEM`、`Administrators`；`install --system` 额外加入安装者的 SID（`serve --control-allow-sid`）。AppContainer 不在其中 |

状态目录里的 `control.json` 记录端点、PID、内核版本和 `control_api`，节点退出时删除。UI 与 CLI 按 `<用户配置目录>/xmesh/*/control.json` 找到本机所有节点；自定义了 `--state-dir` 的节点用 `--state-dir` 指定。

L2 保护：L2 工作目录若包含控制套接字（例如把节点状态目录或它的上级设为工作目录），exec 和 PTY 以 `policy_denied`、规则 `sandbox.control_channel` 拒绝。macOS 的 Seatbelt 本来就拒绝连接工作目录之外的套接字，Linux 的 bwrap 只绑定工作目录。

### 接口

接口、字段、错误码和事件格式以 [控制通道契约](contracts/control.md) 为准，内核的路由注册与它的路由表一一对应（`Test_ControlRegistersEveryContractRoute`），每个响应和事件在测试里都过契约模式。这里只写运维上需要知道的。

内核声明并实现的能力：`status`、`events`、`peers`、`sessions`、`drain`、`pair`、`ingress`、`audit`，以及有配置文件时的 `config.read`、`config.write`、`pats`、`gateway`。

- `GET /v2/status`：`sys.info` 的超集，不按调用方 tag 过滤，另含 `control`、`lifecycle`、`drain`、`paths`、`gateway`、`peers`。`warnings` 是 `{code, severity, message}`。
- `GET /v2/events`：SSE，首条 `hello`；`Last-Event-ID`（`<boot_id>.<seq>`）续传，保留最近 1024 条，id 属于上一次启动或已滚出缓存时先发 `reset`。事件由每秒一次的状态采样推导，短于一个采样周期的 exec 可能不会出现，`call` 事件来自审计记录，不受此限。字段是白名单，带 stdout 等内容的事件被拒绝（INV-10）。
- `GET /v2/sessions`：PTY、排队和运行中的 exec（只有可执行文件基名）、后端租约（`token` 是 fencing 序号）。
- `POST /v2/drain`、`POST /v2/resume`：`keep` 重复是 no-op，`keep` 可升级为 `kill`，`kill` 降级返回 `conflict`（`drain_downgrade`）。恢复只让本节点重新接受新调用，`kill` 已结束的终端和租约不会回来；同一进程里的 Gateway 一并恢复，其他节点上的 Gateway 自己的排空标记不由这里清除。
- `GET /v2/config`、`PUT /v2/config`：整文件读写。`PUT` 必须带 `If-Match`（缺少 428，过期 412 并附 `current_etag`），内容经 `config.Parse` 校验（失败 422 `config_invalid`，带逐条诊断），临时文件加重命名原子替换，注释与格式由提交的内容决定。响应的 `diff` 是 JSON Pointer 操作列表，`weakens_security` 只是给 UI 弹确认的提示，内核不拦截，因为同一用户的进程本来就能直接改配置文件。`xmesh control config set --set/--delete` 在客户端读当前文件、改好再整体提交。
- `POST /v2/pair/export`、`POST /v2/pair/import`：使用统一的版本 2 设备身份，与网络模式无关。导入同时核对用户确认的指纹、文档指纹和公钥摘要，任一不符返回 `fingerprint_mismatch`；成功后立即生效（`restart_required: false`）。身份与信任保存在 `<状态目录>/identity-v2/`，与 `xmesh pair` 共用。`DELETE /v2/pair/trust/{device_id}` 撤销该设备的所有路径与会话。
- `GET/POST /v2/pats`、`POST /v2/pats/{name}/rotate`、`DELETE /v2/pats/{name}`：改 `gateway.pats` 与令牌文件，Gateway 逐请求读取，立即生效。控制通道创建的令牌放在 `<状态目录>/pats/<名字>.token`（`0600`），先写文件再写配置条目，删除时先删条目再删文件；配置里指向别处的令牌文件可以轮转，但删除 PAT 时不会被删。节点没有 `gateway` 配置时创建返回 `conflict`（`gateway_not_configured`）。令牌正文只在创建与轮转的响应里出现一次。
- `GET /v2/ingress/clients`、`DELETE /v2/ingress/clients/{client_id}`、`PUT /v2/ingress/password`：没有 `ingress` 角色返回 `role_disabled`。撤销客户端同时清掉它的授权码、access 与 refresh token 并持久化；写在 `ingress.oauth.clients` 里的客户端下次启动会重新登记，但旧 token 不会复活。轮转口令先原子重写口令文件（`0600`）再让运行中的服务切换，已签发的 token 不受影响；口令长度 12 到 256 个字符，首尾不能有空白。
- `GET /v2/audit`：审计元数据，最新在前，`limit` 默认 100、最大 1000。含未允许字段的记录不返回。

经通道发生的非 `GET` 请求写审计，`event` 为 `control_call`，`actor` 为 `control`，`caller` 为 `control:uid-<数字>`（Windows 是管道所属用户的 SID），`tool` 是路由编号，`args_digest` 只含请求体的键名与字节数。`restart_required`：真正写入时是节点配置存储重新加载后的结论；`dry_run` 按需重启的配置路径及角色变化估计。版本 2 的 `mesh.lan` 与 `mesh.tailnet` 由各自的网络组件热应用。

### 命令行

```
xmesh status --control [--node 名 | --state-dir 目录] [--json]
xmesh control status|peers|sessions|events|audit|drain|resume|config get|config set|config reload ...
xmesh control pair password show | set (--password-stdin | --password-file 文件) | clear
xmesh control pair discover | connect (--address 地址 | --fingerprint 指纹) [--path lan|tailnet] (--password-stdin | --password-file 文件)
xmesh control pair export [--out 文件] | pair import --file 文件 --fingerprint 指纹 [--tags ...]
xmesh control pair revoke --device-id 设备ID
xmesh control pats list | create --name 名字 --scope S[,S...] | rotate 名字 | delete 名字
xmesh control ingress clients | revoke 客户端ID | password [--password-file 文件]
xmesh install|pair|uninstall|start|stop|restart ... --json
```

`xmesh control` 的输出都是 JSON；`events` 每行一个事件（首条为 `hello`）。`--count N` 把 `hello` 也算在内；空闲节点除 `hello` 外没有事件（心跳是不计数的 SSE 注释），要在有限时间内退出请加 `--timeout 5s`，到时正常退出。

`pair`、`pats`、`ingress` 三组命令调用上面同名的接口，节点不需要停：

- `pair password set` 设置或修改连接密码，口令只从标准输入或 `--password-file` 读取，与 `ingress password` 相同；`pair password show` 只报告是否设置、最近修改时间与失败计数，不回显口令；`pair password clear` 清除。`pair discover` 列出 LAN 和 Tailnet 中尚未配对的设备。`pair connect` 用对端的连接密码配对，`--address` 与 `--fingerprint` 至少给一个：只给指纹时地址取自发现列表，同时给出时核对对端公钥。`--path` 指定配对路径，缺省为 LAN；配对后的信任可用于两种路径。
- `pair export` 打印本机公开配对文档（`{"pair": {...}}`）；加 `--out 文件` 则把文档写成 `0600` 的文件，stdout 只给出节点名与指纹，该文件可直接交给对端的 `xmesh pair --import` 或 `xmesh control pair import`。`pair import --file 文件 --fingerprint 指纹` 导入对端文件，`--fingerprint` 必须是从对端另行拿到的值（冒号与大小写不限），不能从配对文件里抄；`--tags` 缺省为 `tag:peer,tag:device`。导入立即生效，与网络模式无关；也接受 `pair export` 的完整输出作文件。`pair revoke` 按设备 ID 撤销 xmesh 信任，终止该设备全部会话。
- `pats create` 的 `--scope` 可重复或用逗号分隔（语法见 [CTR-OAUTH-02](contracts/oauth-authz.md#ctr-oauth-02)，如 `devices:read`、`exec:<设备>`、`fs:read:<设备>`）；`pats rotate`、`pats delete` 的名字写在子命令后面或用 `--name`。令牌正文只在 `create` 和 `rotate` 的输出里出现一次，`list` 不含令牌，请在拿到时保存。删除成功输出 `{"deleted": true, "name": …}`。
- `ingress clients` 列出 issuer 和已注册的 OAuth 客户端，`ingress revoke 客户端ID` 撤销并使其令牌失效，输出 `{"revoked": true, "client_id": …}`。`ingress password` 从 `--password-file` 或标准输入读新登录口令（交互终端下不回显），口令不接受命令行参数，以免进入 shell 历史和进程列表；长度 12 到 256 个字符，首尾空白会被去掉。口令不是来自文件时节点返回 `conflict`（`password_source_not_file`）。`uninstall`、`start`、`stop`、`restart` 操作 `install` 注册的服务（`xmesh-<节点名>`；Windows 默认是当前用户的登录任务，`--system` 为 SCM 服务）。所有子命令加 `--json` 时，stdout 只输出一个对象 `{"ok":…, "action":…, "messages":[…]}`，失败时 `ok` 为 false、带 `error`，退出码仍非零。服务或登录任务没安装时，`start`、`stop`、`restart`、`uninstall` 返回 `error: "not_found"`、`reason: "not_installed"` 和 `name`，不转述 `schtasks` 等系统工具的原文（它们按系统语言输出）。

### 尚未提供

- `install --system` 的服务以 SYSTEM 运行。不带 `--state-dir` 时安装命令默认补上 `--state-dir %ProgramData%\xmesh\<节点名>`（`Test_InstallStateDir`），UI 和 `xmesh ... --control` 会枚举这个位置（`Test_ControlDiscoverSystemServices`）；管道名里的 SID 是 SYSTEM 的，以 `control.json` 的 `endpoint` 为准。已有的 SYSTEM 服务若原来用了默认目录（SYSTEM 的用户配置目录），升级后会换到新目录，需要重新配对或重新注册，或在重新安装时显式传原来的 `--state-dir`。
- Windows 管道 DACL 以实际创建的对象检查（`Test_CONSEC12_LivePipeDACL`），并在打开 `windows_appcontainer` 的 `serve` 里验证 AppContainer 内的命令打开该管道被拒、本账户可以打开（`Test_WindowsL2ServeSandboxCannotOpenControlPipe`）。Linux 与 macOS 的 L2 沙箱真实拒绝测试（`Test_L2CannotConnectToControlSocket`）需要 `XMESH_TEST_L2=1`，由 `go.yml` 的 `l2-unix` 任务运行。

## 测试版发布

向仓库推送形如 `v0.2.0-beta.1`（或 `-rc`）的标签，`.github/workflows/release.yml` 会为 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64 和 windows/arm64 六个目标构建二进制（Windows 为 `.zip`，其余为 `.tar.gz`），生成 `SHA256SUMS.txt`，并创建 GitHub 预发布版。`xmesh version` 输出标签名。二进制未签名；该工作流只在标签推送时运行，不属于常规 CI。例如：`git tag v0.2.0-beta.1 && git push origin v0.2.0-beta.1`。

不想在本机打标签时，用 `.github/workflows/cut-release.yml`：在 Actions 里手动运行「打发布标签」并填入版本号（例如 `v0.2.0-beta.2`），它会校验格式、要求版本号高于现有最新标签，然后在 `master` 最新提交上推送该标签，随后 `release.yml` 照常运行。标签必须用仓库密钥 `RELEASE_PUSH_TOKEN` 推送（只限本仓库、Contents 读写的细粒度令牌）：默认 `GITHUB_TOKEN` 推的标签不会触发 CI，发布闸门会因找不到标签上的 CI 运行而一直等待。

## tailnet 管理（xmesh mesh）

桌面「策略」和「DERP 中继」先读取云端策略，共用一份本地会话草稿。
DERP 页显示云端已有地域；新增、编辑、删除及官方中继开关更新这份草稿。
模板也先合并到草稿中。到「策略」查看最终完整正文，点击「预览完整策略差异」，
校验通过并确认后「应用到云端」。应用前保存快照、检查版本，应用后回读。
版本冲突保留草稿；先复制草稿再重新加载云端策略，比较并合并需要的修改。
快照先恢复到草稿，再统一预览和应用。草稿在本次应用会话内保留，尚未持久化到磁盘。

真实边界验收（复用现有依赖与 Go 缓存）：

```bash
go test ./meshadmin -run '^TestPolicyDocument' -count=1
go build -o /tmp/xmesh-policy-check ./cmd/xmesh
# 使用已校验的 Headscale v0.29.5 二进制；测试只写入临时 SQLite 控制面。
XMESH_TEST_HEADSCALE_BIN=/path/to/headscale XMESH_TEST_XMESH_BIN=/tmp/xmesh-policy-check \
  go test -tags policy_live ./meshadmin -run '^TestPolicyDocumentLivePublish$' -count=1
# UI 构建后，用具有两个已有地域的 Tailscale 凭据执行真实只读验收。
# 脚本将凭据复制为只读的临时凭据，结果写入 XMESH_ACCEPT_OUT，结束后移除临时凭据。
cd ui
npm run build
XMESH_ACCEPT_BIN=/tmp/xmesh-policy-check XMESH_ACCEPT_CREDENTIAL_FILE=/path/to/mesh-credential.json \
  XMESH_ACCEPT_OUT=/tmp/xmesh-policy-evidence node scripts/accept-policy-draft.mjs
```

上述真实验收独立于旧版假内核/控制面 E2E 夹具；Windows/Linux 的实际桌面运行仍需各平台验证。

`xmesh mesh` 管理控制面（官方 Tailscale 或 Headscale）：设备与 tag、接入新设备、权限模板、第三方 DERP。管理凭据只存系统钥匙串或 `0600` 文件，不放进 `xmesh.yaml`，也不进内核进程，见 [CON-MESH-02](constraints/meshadmin.md#con-mesh-02)。命令都支持 `--json`，桌面 UI 通过它们调用。契约见 [meshadmin](contracts/meshadmin.md)。

登录（密钥从标准输入读，不放命令行）：

```bash
# Tailscale：OAuth client（--client-id 给出时密钥视为 client secret；不给则视为 API token）。读写各存一份，写凭据只在要改东西时才放
printf '%s' "$SECRET" | xmesh mesh login --profile home --kind tailscale --tailnet example.com --client-id ID --access read
printf '%s' "$SECRET_W" | xmesh mesh login --profile home --kind tailscale --tailnet example.com --client-id ID_W --access write
# Headscale：API key，等同完整权限；--url 必须是 https，本机验收可用环回 http
printf '%s' "$KEY" | xmesh mesh login --profile lab --kind headscale --url https://hs.example.com --access write
xmesh mesh status --profile home --node-config xmesh.yaml
```

桌面面板默认把凭据写进 `<状态根>/mesh/mesh-credential.json`（`0600`），并把路径以 `--credential-file` 交给 `xmesh mesh`；它不使用系统钥匙串，所以无桌面会话的 Linux 也一样可用。手动在终端登录时也可以照这个方式写同一个文件；不给 `--credential-file` 才走系统钥匙串。文件权限必须是 `0600`。

读与写：

```bash
xmesh mesh tags --profile home                                   # 设备与 tag
xmesh mesh tags --profile home --device DEVICE --set tag:device --apply
xmesh mesh authkey --profile home --tags tag:device --out ./authkey            # 预授权 key 写入 0600 文件，不打印
xmesh mesh onboard --profile home --authkey-file ./authkey --config xmesh.yaml # 新设备接入；加 --install 直接安装
xmesh mesh matrix --profile home --node-config xmesh.yaml        # 网络层与 agent.authz 合并后的权限矩阵
xmesh mesh policy --profile home --template team --owner autogroup:admin       # 预览
xmesh mesh policy --profile home --template team --owner autogroup:admin --apply --expect-version VERSION
xmesh mesh policy --profile home --snapshots
xmesh mesh policy --profile home --rollback SNAPSHOT_ID --apply
```

所有写操作默认只预览（含控制面校验结果与 diff）；`tags`、`policy` 加 `--apply` 才写，DERP 用 `derp apply`（`derp plan` 只预览），写前存快照到本机状态目录。`--expect-version` 取预览里的 `base_version`，保证写入的正是看过的那一版。Headscale 以 `policy.mode=file` 运行时策略只读：命令给出文本，由运维写入服务器后 reload。Headscale 数据库模式没有原子的版本检查，见 [RUL-21](contracts/rulings.md#rul-21)。

第三方 DERP：

```bash
xmesh mesh derp plan --profile home --region "code=lab,name=Lab,host=derp.example.com,ipv4=203.0.113.9"   # 预览 derpMap
xmesh mesh derp apply --profile home --region "..."   # 写 derpMap，随后探测，失败自动回滚
xmesh mesh derp probe --region "..." --compare-official
xmesh mesh derp headscale --region "..." --out derp.yaml      # Headscale：生成 derp.paths 文件和 config 片段，重启 Headscale 生效
```

内部使用的准入（只放行已注册设备）：

```bash
derper ... --verify-client-url http://127.0.0.1:8480/admit --verify-client-url-fail-open=false
xmesh mesh derp admit-serve --listen 127.0.0.1:8480 --allowlist allow.json --admin-token-file admin.token
xmesh mesh derp sync --profile home --admission-url http://127.0.0.1:8480 --admission-token-file admin.token
```

`--verify-client-url-fail-open=false` 不能省：上游默认在准入服务不可达时放行。准入只在连接建立时判断，已有连接要重连才受影响。

验证状态：Headscale 一侧用真实 v0.29.5 做过集成测试（`policy.mode=database`，含生成的 DERP 文件被其加载）；Tailscale 一侧只对照官方客户端的请求形状做了夹具测试，没有联调真实 tailnet；第三方 DERP 的探测与准入在本机回环验证过，没有跨公网验证。计量、计费、多租户目录没有实现。
## Computer 桌面接入

macOS 的 computer 控制默认绑定应用窗口并在后台执行；Windows 与 Linux 当前使用目标设备的活跃用户桌面，鼠标和键盘焦点与目标设备用户共享。Agent 的输入发生在被控制的设备。具体能力与平台验收见[设计与验收记录](design/19-computer.md#macos-应用后台执行)。

macOS 后台后端已接入窗口列表、单窗口采集及固定版本的开源输入实现。Agent 先调用 `computer.info`，从 `targets` 选择窗口，再用 `computer.open` 的 `mode: control`、`interaction: background`、`target_id` 建立会话；后续 observe / act 沿用同一目标。macOS 控制的默认 interaction 为 background，缺少目标时报错。显式 foreground 会使用共享桌面输入。单击优先使用目标控件的 AXPress / AXShowMenu，事件路径支持左键单击 / 双击。文本优先使用目标字段的辅助功能接口；其余输入复用固定版本的上游 SkyLight 配方。本机 AppKit 与 Electron 临时窗口已通过完整输入和指针 / 前台应用监测，其他应用兼容性按验收记录确认。

在目标设备的图形用户会话中打开面板“桌面控制”，开启桌面能力，再点击“安装桌面服务并授权”。系统会请求屏幕录制与输入控制权限。桌面 host 独立于面板运行，用户注销后按平台会话规则退出。

CLI 对应步骤：`xmesh computer setup` 安装并启动当前用户的桌面服务并请求系统授权；`xmesh computer info` 查询执行器状态；`xmesh computer stop|start|restart|uninstall` 管理用户服务。需要前台诊断时运行 `xmesh computer host --authorize`。这些命令要求发行包中同时存在 `xmesh` 与原生桌面执行器；macOS 原生执行器位于同目录的 `xmesh-computer.app`。

在节点配置中设置 `agent.computer.enabled: true`，并按用途向节点 tag 授予 `computer.info`、`computer.open`、`computer.observe`、`computer.sessions`、`computer.renew`、`computer.close`；控制还需要 `computer.act` 和 `computer.action_status`。Gateway PAT / Ingress OAuth 使用 `computer:observe:<设备名>` 或 `computer:control:<设备名>`。

面板预览须显式选择具有目标设备观察权限的凭据。默认 Gateway PAT 未包含桌面 scope 时，到“授权”页创建观察凭据，再回到桌面页选择它。页面只接触凭据名称，令牌由主进程读取；控制 scope 也可观察，剪贴板 scope 不授予预览权限。

Windows SYSTEM 内核需要显式配置 `agent.computer.endpoint` 指向登录用户 SID 的 `\\.\pipe\xmesh-<SID>-computer` 管道，桌面 host 由该用户的登录任务启动。Linux 服务绑定 `graphical-session.target`；Wayland 还需要 RemoteDesktop 与 ScreenCast Portal、PipeWire，以及支持对应接口的桌面后端。Linux 二进制运行时依赖 libdbus、libxkbcommon 和 libpipewire。

本机接管可在面板暂停 Agent，或执行 `xmesh control computer --action pause --node <节点名>`；`--action resume` 恢复，Agent 随后重新获取控制会话。紧急快捷键在面板应用运行期间有效。各平台实测进度见 [Computer 方案](design/19-computer.md)，工具参数见 [契约](contracts/computer.md)。

内核不可用时，执行 `xmesh computer emergency-stop` 直接停止用户桌面 Host 的画面和输入共享；自定义用户目录使用 `--state-dir <目录>`。停止状态保留到 Host 重启之后。执行 `xmesh computer resume` 清除本机停止状态，再按需恢复节点控制；面板的恢复操作会依次恢复本机 Host 和内核。新的远程会话须重新截图和获取租约。

原生输入验收会共用目标桌面的系统指针和键盘，须先取得该桌面用户的明确同意，再设置 `XMESH_TEST_ALLOW_DESKTOP_INPUT=1` 运行 `ui/scripts/test-native-desktop.mjs`。普通本机测试保留此变量未设置，输入用例自动跳过，启动器会在创建窗口之前拒绝运行。CI 的一次性 runner 显式启用该开关。

macOS 的真实内核与面板预览验收脚本为 `ui/scripts/accept-computer-macos.mjs`。它要求预先构建 UI、测试配置的 Electron fuses，以及绝对路径的 `XMESH_TEST_COMPUTER_SERVICE_BINARY` 和 `XMESH_TEST_COMPUTER_HELPER`；打开窗口前须取得本机用户同意，并设置 `XMESH_TEST_ALLOW_DESKTOP_UI=1`。脚本使用独立节点、PAT、Host 和用户数据目录，结束后清理。与输入夹具组合运行时还要求输入验收开关；单独运行只观察桌面和验证控制状态。可选 `XMESH_TEST_COMPUTER_UI_EVIDENCE` 仅在清除画面后保存面板截图，原始桌面图像保留在内存。

后台临时窗口验收使用 `scripts/accept_computer_background.py --helper <原生执行器绝对路径> --binary <xmesh绝对路径> --fixture appkit|electron`，须预先取得用户批准并设置 `XMESH_TEST_ALLOW_BACKGROUND_INPUT=1`。该脚本通过真实 Gateway / Agent / Host 执行并验证实际控件变化，监测系统指针和前台应用；AppKit 的 `--occluded` 使用同一测试应用的第二个窗口覆盖目标。桌面验收须串行运行，以遵守每用户单执行器约束。

macOS 升级验收使用 `ui/scripts/accept-computer-upgrade-macos.mjs`，显式设置 `XMESH_TEST_ALLOW_COMPUTER_UPGRADE=1`，并传入绝对路径的 `--old-binary`、`--new-binary`、`--old-helper`、`--new-helper`；两组内核 / 原生执行器的版本须匹配且前后不同。先构建 UI，使脚本运行实际 Updater、macApplier 和 computerHostServices。验收要求本机用户桌面服务未安装，使用临时 bundle、无窗口入口、真实 hdiutil DMG 和原生二进制，校验交接失败后的旧 Host 恢复及成功替换后的新版本 / PID / epoch，最后卸载自己的服务和临时文件。该脚本不请求系统授权；ad-hoc 包升级后报告 permission_required 时，需要本机显式授权，不能据此声明签名发行包的权限延续已通过。
