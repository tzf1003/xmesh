# 运行手册

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

先修改 `xmesh.local.yaml` 的允许目录，再启动：

```bash
bin/xmesh serve --local --config xmesh.local.yaml
bin/xmesh status --json
bin/xmesh status --endpoint http://127.0.0.1:8080/mcp --tool local/sys.info --json
```

开发 Agent 默认 `127.0.0.1:7433/mcp`；Gateway 默认 `127.0.0.1:8080/mcp`。客户端使用带会话的 Streamable HTTP MCP，协议版本 `2025-11-25` 或 `2025-06-18`。PTY owner 与租约属于对应 MCP 会话。

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
    tag:device:
      tools: ['*']
      backends: ['*']
```

注册 key 文件设为 `0600`，状态目录由程序设为 `0700`。当前启动流程每次读取 key 文件，已注册后仍需保留其可读路径。控制面须授予节点 `tag:device`；Gateway 仅发现在线、未过期、带该 tag 的设备。网络策略须允许调用节点访问目标 TCP 7433；Agent 再按 `agent.authz` 检查 WhoIs 的可信 tag。生产环境应按设备用途收窄工具和后端授权。

```bash
bin/xmesh serve --config /secure/xmesh.yaml --state-dir /secure/xmesh-state
bin/xmesh status --endpoint http://127.0.0.1:8080/mcp --tool devices.list --json
```

远端工具名为 `<node.name>/exec.run` 等；本机 Gateway 上为 `local/exec.run`。`sys.info` 返回实际网络身份、角色、重载失败及待重启提示。

设备能力表独立后台刷新，已连接设备通过缓存提供工具。远端首次就绪后会通知客户端更新工具列表；不可达设备的连接尝试具有独立超时和重试间隔。`devices.list` 中的 `stale` 表示元数据仍待刷新，针对某设备的首次工具调用会等待该设备的能力查询。

两端运行后，可在各自 Gateway 验证对端：

```bash
python3 scripts/smoke_peer.py --gateway 127.0.0.1:8080 --device linux-dev --expected-os linux
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

## 本机 Gateway PAT

在 `gateway.pats` 中按凭据名称配置文件和 scope：

```yaml
gateway:
  listen: 127.0.0.1:8080
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

## LAN 配对

LAN 模式通过 mDNS 发现，配对记录中的公钥用于 mTLS 验证。双方交换公开导出文件，并通过独立可信渠道核对指纹：

```bash
bin/xmesh pair --config xmesh.yaml --state-dir /secure/state --export /tmp/node-public.json
bin/xmesh pair --config xmesh.yaml --state-dir /secure/state --import /tmp/peer-public.json --fingerprint VERIFIED_SHA256 --tags tag:peer,tag:device
```

两端分别导入对方，重启使配对生效。`tag:device` 用于发现，调用权限由接收方 `authz` 决定。LAN 模式的 ingress 配置会被拒绝。

## 后端与入口

后端启动命令、工作目录和策略配置在 `agent.backends`。exclusive 后端先 `backend.acquire`，后续请求以 `_lease_token` 携带返回的 fencing token；Agent 转发业务参数前剥离此字段。普通业务 `token` 字段照常转发。续期必须显式调用 `backend.renew`。只改策略时有效后端进程和租约保留，后端配置变更或故障会使旧租约失效。

公网入口节点需要控制面授予 `tag:ingress`，并配置对应 Agent 权限。登录口令来自 `--oauth-password-file`，默认状态目录内的 `oauth-password`，建议权限 `0600`。`ingress.oauth.issuer` 必须匹配客户端访问的 HTTPS 外部地址。Funnel 需要官方控制面；反向隧道监听回环地址，外层隧道负责 HTTPS；public:443 使用证书文件或 ACME。

动态注册客户端提供允许的回调 URI；配置文件中的客户端只定义 scope，其首次通过口令与 CSRF 校验的授权会绑定并持久化回调 URI。授权码采用 PKCE S256、一次性、最长 60 秒；访问令牌有效 1 小时，刷新令牌 30 天且轮转。令牌保存在内存，入口重启后须重新授权。scope 收窄后建立新的 MCP 会话，使工具表和 owner 使用新的权限上下文。

## Linux L2

安装发行版 `bubblewrap`，设置 `agent.policy.isolation: L2`。bwrap 隔离网络、提供临时 HOME，并绑定本次调用的工作目录。主机必须允许相应 user namespace。受 AppArmor 限制的主机可为实际 bwrap 路径配置仅允许 `userns` 的执行 profile；管理员应按主机策略审核并加载。环境不满足时工具返回隔离失败。

```bash
XMESH_TEST_L2=1 go test ./internal/policy -run L2 -v
python3 scripts/smoke_local.py --binary bin/xmesh --isolation L2
```

## macOS L2

设置 `agent.policy.isolation: L2` 后，exec 与 PTY 使用系统 `/usr/bin/sandbox-exec`。配置按本次工作目录生成：目录可读写，系统程序和运行库目录只读，网络、Mach 服务查询和其他进程信息默认受限。`HOME`、`USERPROFILE`、`TMPDIR` 均指向工作目录，目录参数独立传递给沙箱启动器。需要访问主目录配置或系统服务的程序可能无法在此级别运行。

```bash
XMESH_TEST_L2=1 go test -race ./internal/policy -run L2 -v
python3 scripts/smoke_local.py --binary bin/xmesh --isolation L2 --pat --shell /bin/zsh
```

`sandbox-exec` 已被 Apple 标记为废弃，系统升级后应重新执行上述验收。系统缺少启动器时拒绝 L2 启动。当前实现允许读取 `/bin`、`/sbin`、`/usr`、`/System`、`/opt/homebrew`，以及必要的设备与目录元数据。

Linux 与 macOS 均拒绝把凭证目录或其祖先用作 L2 工作目录，检查 `.ssh`、`.config/gcloud`、`.azure` 和 `.aws`，包括已存在的符号链接目标。工作目录中的既有硬链接仍共享主机 inode；部署时应使用独立工作目录。Unix PTY 关闭会清理同一终端会话的进程组；主动创建独立会话的守护进程需要额外的生命周期约束。Windows 当前配置 L2 时拒绝命令和终端启动。

## 治理与生命周期

Agent 与 Gateway 的工具调用预算为每主体每小时 10000 次、并发 64、单次最长 10 分钟。Agent 的 `sys.info` 免计数。Agent 主体优先采用可信 OAuth client，其余按节点 ID 计数，多个 MCP 会话共享预算。PTY 的设备与 caller 限额之外，同一主体还共享 `max_per_caller` 的 active/orphaned 额度；转移计入接收方额度。入口跨设备 PTY 聚合预算待实现。

审计写入状态目录的 `audit`，默认保留 720 小时；记录工具、路径、状态、耗时等元数据。PTY 读操作按分钟汇总字节量，审计编码器限制可写字段。

`/control/drain` 只接受可信 `tag:admin` 控制请求。`keep` 拒绝新的 exec、PTY 与后端占用，已有资源按期限结束；`kill` 清理本机 PTY 与租约。`scope:mesh` 使当前进程的 Agent 与 Gateway 进入 drain，控制器须向其他节点分别发送控制消息。重启重置内存 drain 状态。

`install` 使用持久位置的二进制与配置。macOS 使用 LaunchAgent，Linux 使用 systemd 用户服务；Windows 当前使用需要管理员权限的 SCM。服务运行异常会记录错误并以非零状态退出，供管理器处理。各平台原生安装的验收状态见[实现记录](implementation-status.md)。
