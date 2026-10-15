# 4. 节点设计（单二进制，Agent 角色）

每台机器只跑一个 `xmesh` 进程，内部分三个角色，用配置开关：

- **Agent**（本节）把本机内置工具和 MCP 后端以 Streamable HTTP MCP 暴露在 tailnet 地址。
- **Gateway**（[第 7 节](07-gateway.md)）在 `localhost:8080/mcp` 聚合本机与对等节点的工具。
- **Ingress**（[第 3 节](03-mesh.md)、[第 7 节](07-gateway.md)）可选，给云端提供公网 URL。

Agent 角色不管理工作区，不建 worktree。路径只以 `cwd` 参数出现。解析规则见 [CTR-CWD-01](../contracts/cwd.md)。

## 工具来源

| 来源 | 工具 | 说明 |
| --- | --- | --- |
| 内置 | `sys.info`、`exec.run`、`exec.output`、`pty.*`、`fs.*` | 对齐 Claude Code 默认工具语义，见 [工具面](05-tools.md) |
| 后端（stdio） | `<name>.<tool>`，如 `ida.decompile_function` | Agent 拉起子进程，维持单条 MCP 连接 |
| 后端（http） | `<name>.<tool>`，如 `frida.attach` | 本机已在运行的 MCP server |

另有租约控制工具 `backend.acquire` / `backend.renew` / `backend.release`，只服务于 `policy: exclusive` 的后端。它们不是后端进程自己的工具。这是 [RUL-03](../contracts/rulings.md#rul-03) 的裁定，契约在 [CTR-BE-02](../contracts/backend-lease.md#ctr-be-02)。

Agent 启动时对每个后端做 `initialize` + `tools/list`，加前缀后合并。后端的 `notifications/tools/list_changed` 向上透传。后端崩溃自动拉起，并通知所有上游。见 [CTR-MUX-04](../contracts/mcpmux.md#ctr-mux-04)。

## 配置文件 `xmesh.yaml`

下面是方案中的完整示例。字段的类型、必填和互斥规则以 [配置契约](../contracts/config.md) 和 [`config.schema.json`](../contracts/schemas/config.schema.json) 为准。`agent.policy` 在 [安全](08-security.md) 中要求存在，示例里补上了与正文一致的最小形状。

```yaml
node:
  name: win-dev-01           # tailnet 节点名，同时是工具名前缀
  roles: [agent, gateway]    # 可选再加 ingress

mesh:
  mode: tailnet              # tailnet | lan
  control_url: https://controlplane.tailscale.com   # 或 Headscale: https://hs.example.com
  authkey_file: /etc/xmesh/authkey
  lan:
    mdns: true

gateway:
  listen: 127.0.0.1:8080     # 本机 Claude Code / Codex CLI 连这里
  expose_backends: all       # all | favorite

ingress:                     # 仅入口节点配置；roles 不含 ingress 时忽略
  listen: funnel             # funnel | public:443
  domain: mcp.example.com
  tls: autocert
  oauth:
    issuer: https://mcp.example.com
    clients:
      codex:  { scopes: ["devices:read", "exec:*", "pty:*", "fs:*"] }
      claude: { scopes: ["devices:read", "exec:*", "pty:*", "fs:*", "backend:win-dev-01:ida"] }

agent:
  default_cwd: ~
  workspaces:                # 别名，供 cwd 参数使用（cwd: "@foo"）
    foo: { path: ~/repos/foo, desc: "主项目 (Rust)" }
    ida: { path: ~/ida/work,  desc: "IDA 工作目录" }
  cwd_policy:
    allow: ["~/repos", "~/ida", "/tmp"]
    deny:  ["~/.ssh", "~/.config/gcloud"]
  exec:
    max_concurrent: 4        # shared 槽位数
    exclusive_slots: 1       # 正文约定 exclusive 全局 1
    default_timeout: 120s
    idempotency_cache: 200
    inline_output_bytes: 256KiB   # 裁定 RUL-05：超过则截断并给 output_ref
  pty:
    max_sessions: 20
    max_per_caller: 8
    idle_timeout: 30m
    orphan_grace: 10m
    scrollback_bytes: 1MiB
    default_shell: auto
    closed_retain: 5m
  backends:
    ida:
      transport: stdio
      command: ["python", "-m", "ida_pro_mcp"]
      policy: exclusive
      idle_timeout: 30m
      favorite: true
    frida:
      transport: http
      url: http://127.0.0.1:9000/mcp
      policy: shared
    ghidra:
      transport: stdio
      command: ["ghidra-mcp"]
      policy: per-caller
  authz:
    tag:peer:    { backends: ["*"], tools: ["*"] }
    tag:ingress: { backends: ["ida"], tools: ["sys.*", "exec.*", "pty.*", "fs.*"] }
  policy:                    # 改这个文件后热加载，无需重启进程
    isolation: L1            # L0 | L1 | L2
    exec:
      deny_patterns:
        - 'rm\s+-rf\s+/'
        - 'curl\s+.*\|\s*sh'
        - '\bformat\b'
    env:
      deny_names: ["AWS_*", "GOOGLE_APPLICATION_CREDENTIALS", "SSH_AUTH_SOCK"]
      deny_path_substrings: [".ssh", "gcloud", ".azure"]
```

`exclusive_slots`、`inline_output_bytes`、`closed_retain` 和 `agent.policy` 的字段形状是契约补全，分别对应 [RUL-01](../contracts/rulings.md#rul-01)、[RUL-05](../contracts/rulings.md#rul-05)、正文里的「退出后缓冲保留 5 分钟」，以及安全一节的 `agent.policy`。未出现在上例中的行为，仍以契约为准。

## 常驻方式

| 平台 | 常驻 | 说明 |
| --- | --- | --- |
| Windows | Windows Service（`kardianos/service`） | 需要用户会话的后端以用户级服务运行 |
| macOS | launchd LaunchAgent | GUI 类后端（IDA）需要用户级服务，设备保持登录 |
| Linux | systemd user service | 沙箱用 bwrap / nsjail，见安全一节的 L2 |

安装入口是同一个二进制的 `install` 子命令，见 [实现](09-implementation.md)。
