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

先修改 `xmesh.local.yaml` 的允许目录，再启动。该文件启用 `agent` 与 `gateway` 两个角色，所以同时有 Agent（`127.0.0.1:7433`）和本机 Gateway（`127.0.0.1:8080`）；只写 `roles: [agent]` 时不会启动 Gateway，下面第三条 `8080` 的命令会连不上：

```bash
bin/xmesh serve --local --config xmesh.local.yaml
bin/xmesh status --json
bin/xmesh status --endpoint http://127.0.0.1:8080/mcp --tool local/sys.info --json
```

开发 Agent 默认 `127.0.0.1:7433/mcp`（可用 `--listen` 改，必须在 `127.0.0.1` 上）；Gateway 默认 `127.0.0.1:8080/mcp`（`gateway.listen`），经它调用本机工具时工具名带 `local/` 前缀，所以 `status` 打 Gateway 要加 `--tool local/sys.info`。客户端使用带会话的 Streamable HTTP MCP，协议版本 `2025-11-25` 或 `2025-06-18`。PTY owner 与租约属于对应 MCP 会话。

配置文件顶层的 `node`、`mesh`、`agent` 三段对所有角色都必填：只启用 `gateway` 或 `ingress` 的节点也要写 `agent` 段，且 `agent.cwd_policy.allow` 至少一条（模式校验的要求）；未启用 `agent` 角色时这一段不会被使用。

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

## 临时沙箱节点

短生命周期环境可设置 `mesh.ephemeral: true`，示例见[临时节点配置](contracts/schemas/examples/xmesh.ephemeral.json)。控制面注册 key 应配置 ephemeral 与所需 tag；重复启动的验收需要可复用 key。节点身份仅存内存，复用同一状态目录也会重新注册。正常退出会在最多 5 秒内尝试注销并关闭 tsnet；注销失败返回运行错误。异常退出后的临时节点回收由控制面负责。

在待验收的沙箱内执行：

```bash
python3 scripts/smoke_sandbox.py --binary /opt/xmesh \
  --auth-key-file /run/secrets/xmesh-authkey \
  --peer mac-dev:darwin --peer linux-dev:linux \
  --observer-gateway 127.0.0.1:8080
```

`--observer-gateway` 可选，指向当前环境能访问的现有回环 Gateway，用于反向工具调用和退出离线观察。脚本启动两个生命周期，验证身份变化、远端文件/exec/PTY、身份状态不落盘和临时工作目录清理；key 通过文件路径引用。需要沙箱允许出站访问控制面和 DERP；测试环境中的设备授权应允许临时节点调用所选工具。各云产品的出站限制须在对应环境验收。

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

Linux 与 macOS 均拒绝把凭证目录或其祖先用作 L2 工作目录，检查 `.ssh`、`.config/gcloud`、`.azure` 和 `.aws`，包括已存在的符号链接目标。工作目录中的既有硬链接仍共享主机 inode；部署时应使用独立工作目录。Unix PTY 关闭会清理同一终端会话的进程组；主动创建独立会话的守护进程需要额外的生命周期约束。Windows 当前配置 L2 时拒绝命令和终端启动。

## Windows L2

当前 Windows exec 与 PTY 的 L2 请求返回 `sandbox.unavailable`。`internal/winbox` 提供独立 AppContainer profile、能力为空的启动属性和 profile 删除原语；真实工作目录绑定、exec/ConPTY 接入及异常退出回收仍在实现。其原生核验使用临时 profile，在 Windows 会话中执行：

```powershell
$env:XMESH_TEST_WINDOWS_SANDBOX = "1"
go test -race ./internal/winbox -run WindowsAppContainer -v -timeout=90s
```

AppContainer 启动环境通过 `winbox.Environment` 从 Windows Known Folder 补全 `LOCALAPPDATA`；调用方继续提供经过策略过滤的其他变量。该用例验证父进程环境哨兵未继承、profile 内目录写入、目录外文件读写拒绝、父进程内存读取拒绝、已知可达的环回监听访问拒绝，以及进程 token 的 AppContainer 身份与空 capability 集合。环回网络拒绝核对 helper 的准确 AppContainer SID，通过 Windows 网络隔离 API 确认该 SID 在探测前后均没有环回豁免，并验证宿主在探测前后均能连接、监听端未接受沙箱连接；单独的连接超时不作为通过证据。清理后检查 profile 目录已删除。原语的原生结果见 [Windows 验收记录](validation/2026-10-15-windows.md)。

内部单对象授权原语为准确 profile SID 添加显式、无继承的读写执行 ACE，并通过保留的元数据句柄跟踪对象；回收重读当前 DACL，只删除本次 ACE。原生用例验证普通 Medium 完整性文件与目录的授权写入、标签保持、未授权子目录与目录外拒绝，以及文件和目录改名、原路径替换后的对象回收。最终重解析点、硬链接和 null DACL 会拒绝授权。授权前按对象身份取得并保留独立的 `READ_CONTROL|WRITE_DAC` 句柄，直至准确 ACE 回收成功；对象进入 delete-pending 后可通过该句柄回收。ACL 通过 `NtSetSecurityObject` 直接更新所选对象，并显式保留 DACL 保护与自动继承状态。根路径固定时的更新、控制位、标签和子对象保持已通过 Windows 原生验收；delete-pending 文件与目录用例在 `0f0536a` 的 PR 原生任务通过。同进程 ACL 更新串行，其他 ACL 写入方需与工作目录所有者协调。进程退出并释放授权后才能删除 profile。

工作树检查原语从本地卷根逐组件按句柄打开目录，固定工作目录及祖先的绑定；目录枚举中的 128 位对象 ID 必须与打开后的对象一致。扫描拒绝重解析点、硬链接、重复身份、对象超限及不完整结果，支持取消，关闭时释放全部句柄，扫描本身保持 ACL 不变。枚举缓冲区显式按八字节对齐。保留的子文件句柄会使 Windows 拒绝包含这些文件的目录改名；执行期授权需要独立处理这一生命周期，当前检查原语尚未接入工具启动路径。

已选对象的单对象授权通过非继承句柄复制保留实际对象，授权前重新核对卷号、对象 ID、类型与链接属性。快照关闭与授权释放各自管理所持句柄；调用方应串行完成选择、授权和快照关闭。内部工作树授权覆盖既有与未来新对象，按准确 profile SID 回收；失败保留 profile 和根 guard 供重试。`c827749` 原生验收通过新对象读写、准确内核 DACL/控制位恢复及回收后的访问拒绝。当前授权仍保留子句柄，可编辑目录的执行期寿命、跨进程协调、持久异常退出恢复和工具执行接入继续作为生产门。

工作树内部准入会以当前进程 token 的 profile 与 RoamingAppData 定位默认凭证目录，按实际卷号和对象 ID 排除相互包含及目录别名。已存在前缀以目录数据访问和禁止 delete sharing 的句柄固定，保留至完整回收。调用方须协调外部命名空间、reparse 数据及缺失凭证组件创建；Windows L2 生产启动仍等待继承回收、寿命、跨进程协调及恢复验收。

## 治理与生命周期

exec 与 PTY 只继承程序搜索路径、用户目录、系统目录及语言设置，并过滤凭证名称和 `.ssh`、`gcloud`、`.azure`、`.aws` 路径。Windows 还保留运行时所需的 `WINDIR`、`SYSTEMDRIVE`、`COMSPEC`、`PATHEXT`、`TEMP`、`TMP`、`APPDATA`、`LOCALAPPDATA` 和 `ProgramFiles`，同样经过路径过滤。Windows 保留 `PSModulePath` 中经过拒绝规则的模块目录；缺少该值且 SystemRoot 可用时传入空值，由所启动的 PowerShell 运行时初始化模块目录；拒绝变量名的策略仍然生效。继承的 `PATH` 和 Windows `PSModulePath` 按平台分隔符逐项过滤，保留其余搜索目录；调用方显式传入的搜索路径包含拒绝目录时返回策略错误。调用方的 `env` 经过同一过滤器。Windows 按大小写不敏感的变量名覆盖继承值，调用方同时提供 `Path` 和 `PATH` 等重复键时返回参数错误；Unix 保持大小写敏感。

Agent 与 Gateway 的工具调用预算为每主体每小时 10000 次、并发 64、单次最长 10 分钟。Agent 的 `sys.info` 免计数。Agent 主体优先采用可信 OAuth client，其余按节点 ID 计数，多个 MCP 会话共享预算。PTY 的设备与 caller 限额之外，同一主体还共享 `max_per_caller` 的 active/orphaned 额度；转移计入接收方额度。

入口按 `ingress.max_pty_per_client`（默认 8）聚合各设备的终端占用，串行准入 open/transfer。所有发现设备须支持并授权入口读取 PTY 控制资源；资源不可确认时返回 `quota.unavailable`。保留状态目录中的 `ingress-pty.json`，入口重启后会据此核对仍有资源的设备。设备离线时恢复连接；调用结果丢失且留下待完成标志时，重启对应 Agent 后重新确认。关闭现有终端仍可执行。多入口进程的协调边界见 [CTR-GW-06](contracts/gateway.md#ctr-gw-06)。

同一主机运行多个入口时，各入口可设置相同的 `ingress.pty_budget_file` 绝对路径，例如 `/var/lib/xmesh/ingress-pty.json`。使用独立的入口状态目录、相同的 `max_pty_per_client`、同一 OAuth client 身份域和支持文件锁的版本；预算目录须仅允许受信任的入口进程写入，并位于本地文件系统。运行期间保留该文件及相邻的 `.lock` 文件。升级多个入口时先全部停止，升级完成后再启动。等待预算锁可随请求取消；退出进程自动释放锁，预算记录继续参与恢复核对。

多台主机各运行入口时，在每台入口的配置里设置相同的 `ingress.pty_budget_authority`，值为一台长期在线、所有入口都能连到的 Agent 的设备名（例如 `"pty_budget_authority": "home-server"`）。该 Agent 无需额外配置；入口须是 `tag:ingress` 并获得 `pty.open`/`pty.transfer` 授权。各入口仍须使用相同的 `max_pty_per_client` 和同一 OAuth client 身份域。权威 Agent 离线时所有入口的新增与转移返回 `quota.unavailable`；重启权威 Agent 前先确认没有结果不确定的终端调用，重启后约 30 秒内不会发放租约。续见 [CTR-GW-06](contracts/gateway.md#ctr-gw-06)。

审计写入状态目录的 `audit`，默认保留 720 小时；记录工具、路径、状态、耗时等元数据。PTY 读操作按分钟汇总字节量，审计编码器限制可写字段。

`/control/drain` 只接受可信 `tag:admin` 控制请求。`keep` 拒绝新的 exec、PTY 与后端占用，已有资源按期限结束；`kill` 清理本机 PTY 与租约。`scope:mesh` 使当前进程的 Agent 与 Gateway 进入 drain，控制器须向其他节点分别发送控制消息。重启重置内存 drain 状态。

`install` 使用持久位置的二进制与配置。macOS 使用 LaunchAgent，Linux 使用 systemd 用户服务。Windows 默认注册当前用户的登录计划任务，以 `InteractiveToken` 和 `LeastPrivilege` 配置运行，登录后启动；保持该用户登录以运行 GUI 后端。任务直接执行二进制，保留配置所在工作目录，持续运行并在失败后每分钟重试，最多 999 次。同一用户的同名任务已存在时安装失败，应在任务计划程序中检查后再更新。任务名会打印在安装结果中，可在任务计划程序中立即运行、停止和删除。

Windows 可用 `install --system` 选择管理员 SCM 服务，默认以 LocalSystem 身份运行于 Session 0；配置、状态和工作目录须允许该账户访问。已有 SCM 安装迁移到用户任务时，先停止并卸载原服务，避免同一节点的两个进程同时占用状态与监听端口。用户任务运行于登录会话；SCM 适用于系统服务场景。

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

Windows 普通命令与 ConPTY 进程通过 `PROC_THREAD_ATTRIBUTE_JOB_LIST` 在创建时加入专属 Job Object；shell 自行退出、TERM 宽限结束和 KILL 均清理其后代进程。普通命令只继承显式标准输入、输出和错误句柄，主进程退出后先关闭 Job 再等待输入复制结束。创建期 Job 属性需要 Windows 10 / Server 2016 或更新系统。INT 通过终端输入 Ctrl-C，应用的原始输入模式可能改变其行为。Windows 10 1809 或更新系统上的原生验收：

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

内部 `workspaceExecution` 要求可信调用方在准备开始前取得并持续持有外部 namespace/reparse/ACL writer guard；该 guard 覆盖 journal、profile、授权、USN 追踪、进程与监视器的完整寿命。准备返回所有者及错误时，保留所有者并调用带 context 的关闭接口重试。关闭封闭新准入，确认全部 Job 与持久进程清单回执，停止监视器并释放进程所有者，再按身份回收 ACL、删除 profile、刷盘 complete、关闭 journal，最后释放 guard。取消或期限到达保留清理义务；guard 关闭失败保留重试状态，poisoned journal 保留供恢复。普通进程与 ConPTY 启动检查生命周期取消并串行准入。生产部署仍须完成外部 writer 及异常宿主后 guard 寿命、完整 census、未知导入和工具层 exec/ConPTY 集成；Windows L2 保持 `sandbox.unavailable`。

协调导入由生命周期串行入口调用受信源协调器；协调器接收源、目标路径，在 census/移动前协调外部 namespace/reparse/ACL 写入者，并持续保护移动后的导入对象身份，覆盖完整清理期间直至 guard 成功释放。guard 随准备错误一并返回时仍由生命周期接管；最多保留 256 个。关闭完成进程回收、ACL 恢复及 profile/journal 持久化清理后，逆序释放源 guard，再释放原 workspace guard；失败时保留所有者并重试关闭。宿主故障后的保护须由独立存续的协调机制提供。生产外部协调及工具层 exec/ConPTY L2 集成仍待完成，Windows 生产 L2 保持 `sandbox.unavailable`。


### Windows 独立 guard keeper 与未知导入

内部 `launchGuardKeeper` 在 journal 与 profile 创建前启动 `xmesh __sandbox-guard`，固定工作区根、祖先链与 journal 目录链并在宿主退出后保持；宿主死亡后恢复入口 `recoverJournalDirectoryWithKeepers` 通过仅当前用户与 SYSTEM 可访问的私有命名管道接管，keeper 仅在读到已绑定 journal 的持久 `complete` 记录后同意释放。缺少 keeper 时默认失败关闭。keeper 不冻结树内 ACL/reparse 写入，整机重启或外层 Job 的 kill-on-close 会连同 keeper 一起结束；该机制尚未接入生产启动路径。

活动执行观察到未知导入（未登记且无创建记录的对象，例如被外部移入的目录）时，监视器以 `errWorkspaceUnknownImport` 终止执行，journal 持久记录 `workspace-unknown-import`；关闭仍可完成回收并向调用方返回该错误，之后不再接受协调导入。含该记录的 journal 需要本版本或更新的恢复二进制。该归并不证明子树完整，Windows 生产 L2 保持 `sandbox.unavailable`。

内部 `workspaceWriterFence` 把工作区与 journal 私有目录的祖先链以无 delete 共享的句柄钉住，并核对身份、reparse、硬链接、DACL 指纹及 NTFS 变更日志；`RequireWriterFence` 开启后生命周期拒绝无法 Verify 的 guard，违规使监视器终止执行，清理仍完成并返回违规。读取变更日志需要卷设备访问，普通账号须在目标账号上验收；该围栏默认关闭、尚未接入生产，且不随宿主崩溃存活。

## 测试版发布

向仓库推送形如 `v0.2.0-beta.1`（或 `-rc`）的标签，`.github/workflows/release.yml` 会为 linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64 和 windows/arm64 六个目标构建二进制（Windows 为 `.zip`，其余为 `.tar.gz`），生成 `SHA256SUMS.txt`，并创建 GitHub 预发布版。`xmesh version` 输出标签名。二进制未签名；该工作流只在标签推送时运行，不属于常规 CI。例如：`git tag v0.2.0-beta.1 && git push origin v0.2.0-beta.1`。

## tailnet 管理（xmesh mesh）

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

没有钥匙串的主机（无桌面的 Linux 服务器）用 `--credential-file`，文件权限必须是 `0600`。

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
