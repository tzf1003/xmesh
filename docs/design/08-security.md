# 8. 安全与授权

授权分两层、零信任：Gateway 按 OAuth scope 过滤，Agent 再按 tailnet 节点身份过滤。两层都通过才执行。Agent 永远不监听公网。

## 双层授权

| 层 | 身份来源 | 决定 |
| --- | --- | --- |
| Gateway / 入口 | OAuth client + scope（公网模式），或节点身份（tailnet 模式） | 该客户端可见哪些设备、哪些工具前缀 |
| Agent | `LocalClient().WhoIs(remoteAddr)` 返回的节点与 tag | 该 tag 可见哪些后端、可用哪些工具。配置在 `agent.authz` |

任一层次拒绝都不会把请求送到下一层的副作用代码。策略拒绝也一样，见 [INV-05](../constraints/invariants.md#inv-05)。

Tailscale ACL 同时限制网络层：`tag:gw-*` 和 `tag:cloud` 只能访问 `tag:device` 的 Agent 端口。ACL 是网络前提，不能代替上表的两层检查。

判定顺序、scope 与 tag 的对应关系见 [CTR-AUTHZ-01](../contracts/oauth-authz.md#ctr-authz-01)。

## 策略引擎

Agent 侧，`exec` / `fs` / `pty` 共用同一策略引擎。

- **`cwd_policy`：** allow / deny 前缀列表。解析成绝对路径并 `EvalSymlinks` 后比对，防止 `..` 与软链逃逸。只限制 `cwd` 与 `fs` 路径，不拦 PTY 内的 `cd`。
- **命令策略：** `exec` 的 `cmd` 与参数序列的黑名单（如拒绝 `rm -rf /`、`curl | sh`、`format`）。可选白名单；白名单非空时，未命中即拒绝。
- **环境变量：** 不透传 Agent 自身凭证。设备上的 SSH key、云凭证不进入子进程 env。调用方传入的 `env` 也要过同一过滤器。
- **执行隔离分级：**
  - L0：配置上可选，目前没有独立行为，运行时与 L1 完全相同（cwd 白名单与命令黑名单照常执行）。
  - L1：cwd 白名单 + 命令黑名单。默认级别。
  - L2：Agent 管理的沙箱。Linux 用 bubblewrap，macOS 用 `sandbox-exec`，均已实现。Windows 的目标形态是 AppContainer，但生产启动链尚未接入：Windows 上配置 L2 时，exec 与 PTY 返回 `policy_denied`，规则 `sandbox.unavailable`。进展见[实现记录](../implementation-status.md)。

## 自动策略拦截

所有请求在 Agent 侧由策略引擎同步判定：命中 allow 即执行，命中 deny 即拒绝。没有挂起等人的状态。被拦截时返回结构化的 `policy_denied`（含规则名与原因），模型据此换路径或换方式。

默认拦截：

- `fs.delete` 递归（未显式 `recursive: true`，以及 `recursive: true` 但路径落在 deny 或 allow 之外）。
- `fs.write` / `fs.move` 到 allow 之外。
- `exec` 命中黑名单模式，例如 `rm -rf /`、`curl | sh`、`format`。
- 超配额调用（与 `pty_limit`、`exec_saturated` 等专用错误码并存；策略引擎负责识别「这是配额拒绝」，专用错误码负责让模型能分支）。

规则在 `xmesh.yaml` 的 `agent.policy` 段维护。改规则无需重启：Agent 不做文件监听，而是每次工具调用都重读配置文件并比较内容哈希，内容有变化且校验通过才换用新规则，所以下一次工具调用即使文件刚被写入也会使用新规则。热加载失败时保持上一份有效规则，并在日志里记录失败原因，不静默放行。

`policy_denied` 的字段见 [CTR-ERR-01](../contracts/errors.md#ctr-err-01)。

## 配额与熔断

- 每 client：每小时调用数、并发 PTY、单任务时长上限。入口节点按 OAuth client 计数；tailnet 模式按 `caller_id` 的节点部分计数。
- 调用预算（实现中的默认值）：每主体每小时 10000 次调用、并发 64、单次调用最长 10 分钟，Agent 与 Gateway 各自计数；Agent 的 `sys.info` 免计数。主体在 Agent 上优先取可信的 OAuth client，其次取节点 ID，同一主体的多个 MCP 会话共享预算；本机 Gateway 转发的会话共用一个节点预算。
- 终端预算：同一主体（口径同上）名下 `active` 与 `orphaned` 终端合计不得超过 `pty.max_per_caller`，超出返回 `policy_denied`、规则 `quota.exceeded`；这条与按 `caller_id`（单个 MCP 会话）计数的 `pty_limit` 并存，见 [INV-02](../constraints/invariants.md#inv-02)。
- 每设备：exec 并发槽、PTY 20、后端租约 TTL。这些计数只在 Agent 上，Gateway 的配额是额外一层，不能代替设备配额。
- 控制面一键下线单设备或全网。Agent 收到后拒绝新请求，保留现有会话直到它们按自身超时结束。已有 PTY 不强制立刻 Kill，除非下线指令显式带 `drain: kill`。该指令的最小字段见 [CTR-GW-05](../contracts/gateway.md#ctr-gw-05)。

## 审计

每次 tool 调用落盘为日志。PTY 不录像。保留期可配。审计日志不记录文件内容与命令输出正文，只记摘要与哈希。字段表见 [CTR-AUD-01](../contracts/audit.md)。
