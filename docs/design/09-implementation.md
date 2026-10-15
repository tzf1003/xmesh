# 9. Go 实现

单一 Go 仓库、单一二进制 `xmesh`，三平台交叉编译。三个角色是同一进程内的三个子包，由配置开关组合。`mcpmux` 包被 Gateway 角色、Ingress 角色和 Agent 的后端转发共用，是最先要写并配齐测试的核心。

开发顺序受 [CON-IMPL-01](../constraints/implementation.md#con-impl-01) 约束：`cwd` → `fs` → `pty` → `exec` → `mcpmux` + `backend` + `lease` → `mesh` → `gateway`。前四个包没有 tailnet 依赖，先在本机跑通测试。

## 仓库骨架

```text
xmesh/
├── cmd/xmesh/            # 单二进制：serve / install / status / pair 子命令
├── internal/
│   ├── role/
│   │   ├── agent/        # MCP server：内置工具 + 后端挂载，监听 tailnet
│   │   ├── gateway/      # 本机 localhost:8080/mcp，聚合 local/* 与 <peer>/*
│   │   └── ingress/      # 公网或 Funnel 监听 + OAuth 授权服务器 + 前缀换名
│   ├── mesh/             # tsnet 封装、WhoIs、tag → 权限；lan 的 mDNS + mTLS 配对
│   ├── mcpmux/           # id / progressToken 重映射、前缀、list_changed 聚合
│   ├── backend/          # stdio/http 后端生命周期、actor 串行、policy
│   ├── lease/            # 租约 + fencing token
│   ├── cwd/              # 路径解析、别名、allow/deny
│   ├── exec/             # 并发槽、幂等缓存、输出分页
│   ├── pty/              # Manager、ring buffer；pty_unix.go / pty_windows.go
│   ├── fs/               # read/write/edit/glob/grep/list/stat
│   ├── oauth/            # OAuth 2.1 + 动态注册 + PKCE，仅 ingress 使用
│   ├── policy/           # 命令策略、自动拦截规则、热加载
│   └── audit/
├── api/xmesh/v1/         # 内部 RPC。仅当 MCP 不够表达控制通道时使用
└── xmesh.yaml
```

`api/xmesh/v1` 不是云端协议。云端只看见 MCP。控制面下线一类节点间控制若不适配 MCP 工具，才放进这个包，并且只在 tailnet 内暴露。

## 关键依赖

| 包 | 用途 |
| --- | --- |
| `tailscale.com/tsnet` | 用户态加入 tailnet，监听 / 拨号 / WhoIs / Funnel |
| `github.com/modelcontextprotocol/go-sdk` | MCP server（Agent）与 client（Gateway、后端连接） |
| `github.com/creack/pty` | Unix PTY |
| `golang.org/x/sys/windows` | ConPTY（`CreatePseudoConsole`）、`GenerateConsoleCtrlEvent` |
| `github.com/kardianos/service` | Windows Service / launchd / systemd 安装 |
| `github.com/bmatcuk/doublestar/v4` | glob `**` |
| `github.com/sabhiram/go-gitignore` | `.gitignore` 过滤 |
| `github.com/ory/fosite` 或自写最小授权服务器 | OAuth 2.1 + 动态客户端注册 + PKCE |

选 fosite 还是自写，不影响 [OAuth 契约](../contracts/oauth-authz.md)。对外端点和错误必须一致。

## PTY 核心结构

`Manager.mu` 只保护 map 与计数。每个 Session 自己的 `cond` 锁保护 ring。读写不互斥。见 [CON-IMPL-03](../constraints/implementation.md#con-impl-03)。

```go
// Manager 是一台设备上 PTY 会话池的唯一权威。
// mu 只保护 sessions、byCaller 和配额计数，不保护环形缓冲的字节。
type Manager struct {
    mu       sync.Mutex
    sessions map[SessionID]*Session
    byCaller map[CallerID]int
    cfg      Config
}

// Session 是一条交互式终端。
// Owner 可变：transfer 改写它，Orphan 清空它。
// cursor 是写入总字节数，只增不减；读者用自己的 cursor 追这个值。
type Session struct {
    ID     SessionID
    Owner  CallerID
    pty    Pty             // Unix 上是 creack/pty，Windows 上是 ConPTY
    cmd    *exec.Cmd
    ring   *ringbuf.Buffer // 默认 1 MiB，覆写最旧字节
    cursor uint64
    cond   *sync.Cond      // 绑定 Session 自己的锁，不复用 Manager.mu
    state  State           // active / orphaned / closed / released
    lastIO atomic.Int64    // unix 纳秒，供 reaper 计算 idle
    closed chan struct{}   // 关闭时 close，唤醒阻塞中的 reader
}

// Open 在持有 mu 时检查两级配额并递增计数，然后才启动 PTY。
// 任一配额不满足则返回 pty_limit，不留下半初始化会话。
func (m *Manager) Open(ctx context.Context, caller CallerID, o OpenOpts) (*Session, error)

// Read 是 long-poll。cursor 落在已被覆写的区间时 truncated=true，
// 数据从当前缓冲起点开始，而不是报错。
func (s *Session) Read(ctx context.Context, cursor uint64, max int, wait time.Duration) (ReadResult, error)

// Transfer 把 owner 从 from 改成 to。from 必须是当前 owner；
// orphaned 会话的 from 为空，只校验调用方有权接管。
func (m *Manager) Transfer(id SessionID, from, to CallerID) error

// Orphan 在某个 MCP 会话断开时调用，将该 caller 名下全部会话标为 orphaned。
func (m *Manager) Orphan(caller CallerID)
```

## 租约

```go
// Lease 是 exclusive 后端的排他权。
// Token 在该资源上单调递增。崩溃重拉、释放后再获取，都会用新 Token，旧值永久失效。
type Lease struct {
    Resource string    // 形如 "backend:ida"
    Owner    CallerID
    Token    uint64
    Expires  time.Time
}
```

同一 `Resource` 在任意时刻至多一条未过期租约，见 [INV-01](../constraints/invariants.md#inv-01)。结构的 JSON 形状见 [`lease.schema.json`](../contracts/schemas/lease.schema.json)。

## 跨平台要点

| 平台 | PTY | 常驻 | 默认 shell | 注意 |
| --- | --- | --- | --- | --- |
| Windows | ConPTY，build tag `windows` | Windows Service | `pwsh` | 不用 cmd。WSL 作为独立设备注册 |
| macOS | `forkpty` | launchd LaunchAgent（需用户会话时） | `$SHELL` | GUI 类后端（IDA）需用户级服务 |
| Linux | `forkpty` | systemd user service | `$SHELL` | 沙箱用 bwrap / nsjail |

`grep` 实现：优先调用本机 `rg --json`。无 `rg` 时用纯 Go（`WalkDir` + `regexp` + gitignore）兜底。可选在安装包内嵌各平台 `rg` 二进制。两条路径的结果字段必须相同，见 [CTR-FS-05](../contracts/fs.md#ctr-fs-05)。

## 子命令

| 子命令 | 作用 |
| --- | --- |
| `xmesh serve` | 按配置启动已启用的角色 |
| `xmesh install` | 安装为本平台的用户级服务 |
| `xmesh status` | 打印本节点角色、tailnet 身份、后端与配额 |
| `xmesh pair` | `lan` 模式下首次配对，交换节点证书 |

`status` 的人类可读输出使用中文标签，并同时给出机器可解析的 `--json`。
