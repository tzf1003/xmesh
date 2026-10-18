# 安全约束

## CON-SEC-01

一次工具调用在产生副作用之前要经过两层允许：

1. 入口或 Gateway：入口上是 OAuth scope；Gateway 上是 PAT，每个请求都要（[CTR-GW-07](../contracts/gateway.md#ctr-gw-07)）。
2. Agent：`WhoIs` 得到的 tag 命中 `agent.authz`，且工具名与后端名都在允许列表内。

本机 Gateway 把请求转到本机 Agent 时，第二层仍然执行。禁止因为「来自 localhost」跳过 Agent 授权。localhost 在 `lan`/`tailnet` 建成之前的阶段 1，使用显式的开发 tag `tag:peer`，并且该例外只允许绑定在 `127.0.0.1`。

任一层拒绝：返回 [CTR-AUTHZ-01](../contracts/oauth-authz.md#ctr-authz-01) 规定的错误，且满足 [INV-05](invariants.md#inv-05)。

## CON-SEC-02

公网入口没有「关闭认证」的配置。每个已注册 client 的 scope 列表是它的全部权限，空 scope 表示只能完成 OAuth，不能调用工具。

动态注册不需要管理员点击，所以它不能默认拿到全部权限：动态注册客户端的 scope 上限是 `ingress.oauth.dynamic_client_scopes`，缺省只有 `devices:read`。更多权限须由运维显式写进该上限，或预注册客户端。

Gateway 用 PAT 代替 OAuth，与 `mesh.mode` 无关，不论绑定在哪个地址；入口不接受 PAT。PAT 绑定的 scope 语法与 OAuth scope 相同。

## CON-SEC-03

Agent 从 `LocalClient().WhoIs(remoteAddr)` 取对端节点与 tag。取不到身份时拒绝，错误码 `unauthenticated`。禁止信任客户端在工具参数里自报的 tag 或节点名。

`agent.authz` 的键是 tag，可含 `*`（按 `path.Match`，如 `tag:*`）。没有任何键匹配的 tag 默认拒绝；多个键同时匹配时允许列表取并集。`backends` 与 `tools` 都是允许列表，`*` 只在该字段显式写出时才表示全部。

## CON-SEC-04

子进程环境按下面的顺序构造：

1. 从一份最小环境开始（`PATH`、`HOME`、`USERPROFILE`、`SystemRoot`、语言类变量）。Windows 另保留运行时目录与命令查找变量：`WINDIR`、`SYSTEMDRIVE`、`COMSPEC`、`PATHEXT`、`TEMP`、`TMP`、`APPDATA`、`LOCALAPPDATA`、`ProgramFiles`。
2. 应用 `agent.policy.env` 的拒绝规则。
3. 叠加调用方 `env`，叠加时再次应用拒绝规则。

Windows 保留 `PSModulePath` 中经过拒绝规则的模块搜索目录；缺少该值且 `SystemRoot` 可用时，传入空值，由所启动的 PowerShell 运行时初始化默认模块目录。调用方可以显式覆盖该值，拒绝变量名的策略仍然生效。

继承的 `PATH` 和 Windows `PSModulePath` 按平台的搜索路径分隔符逐项应用拒绝规则，保留其余目录。调用方显式传入的搜索路径含拒绝目录时，按被拒绝的环境变量处理。

禁止把 Agent 进程里的 auth key、OAuth 客户端密钥、云凭证文件路径、`SSH_AUTH_SOCK` 继承给 `exec` 或 PTY。调用方显式传入被拒绝的键时，返回 `policy_denied`，规则名 `env.denied`，命令不启动。

## CON-SEC-05

`cwd_policy` 只在解析工具参数里的路径时执行。PTY 会话启动时的 `cwd` 要检查；会话启动之后，用户或模型在 shell 里执行的 `cd` 不再次经过策略引擎。这是已知边界：交互式终端的后续路径由命令策略和隔离级别约束，不由 cwd 前缀约束。

文档和 `sys.info` 不把 cwd 策略描述成「终端内的沙箱」。

## CON-SEC-06

`agent.policy` 的变更在下一次工具调用生效，不要求重启进程，也不丢弃已有 PTY 或租约。热加载解析失败时继续使用上一份成功的策略，向日志写入 `policy_reload_failed`，并拒绝「没有上一份策略」的启动。进程第一次启动时策略非法则拒绝启动。

## CON-SEC-07

禁止把 PTY 环形缓冲、`pty.read` 的返回或 `pty.write` 的 data 写入审计日志、追踪系统或崩溃转储中的业务日志。崩溃转储若可能含终端内容，默认关闭。

允许记录的 PTY 事件：`open`、`close`、`transfer`、`signal`、`write` 的发生时间与 caller、字节长度。不记录字节本身。

## CON-SEC-08

审计记录的参数摘要是键名、标量长度、路径的 `resolved_path`、以及正文的 SHA-256。禁止记录正文。`idempotency_key` 可以记录，它不是输出。保留期由 `audit.retention` 配置，过期必须删除，而不是只在查询时隐藏。

## CON-SEC-09

只要角色包含 `ingress`，下列端点必须可用，且 MCP 端点在没有合法 access token 时返回 401 与资源元数据提示：

- `/.well-known/oauth-protected-resource`
- `/.well-known/oauth-authorization-server`
- `POST /oauth/register`
- `GET /oauth/authorize`
- `POST /oauth/token`

授权码流程必须要求 PKCE（`S256`）。禁止接受没有 `code_challenge` 的授权请求。动态客户端注册必须实现，否则 claude.ai 与 Codex 无法完成连接器安装。

## CON-SEC-10

默认策略在用户没有写任何 `deny_patterns` 时仍然拒绝：

| 规则名 | 行为 |
| --- | --- |
| `fs.delete.recursive_default_deny` | 目录删除缺少 `recursive: true` |
| `exec.deny.rm_rf_root` | 命令序列匹配删除根或盘符根 |
| `exec.deny.curl_pipe_sh` | 命令序列把下载内容送进 shell |
| `exec.deny.format` | 命令基名为磁盘格式化工具 |
| `quota.exceeded` | 超过入口或设备配额 |

配额数字来自 `agent.quota` 与 `gateway.quota`（`calls_per_hour`、`max_concurrent`、`max_call_duration`），缺省每小时 10000 次、并发 64、单次 10 分钟。`exec.run` 的 `timeout`（缺省时取 `agent.exec.default_timeout`）不得超过 `max_call_duration`：超过的调用以 `invalid_args`（`field: timeout`，`reason: quota.max_call_duration`）拒绝，不消耗预算，而不是被调用时限静默截断。配置里 `agent.exec.default_timeout` 超过 `agent.quota.max_call_duration` 时拒绝启动。

写入或移动的目标不在 allow、或命中 deny 时，由路径解析在策略之前拒绝，返回 `invalid_cwd`（`reason: denied`），没有对应的 `policy_denied` 规则名；分工见 [错误契约](../contracts/errors.md)。

上表里的 exec 规则和 `agent.policy.exec.deny_patterns` 使用同一种匹配，见 [RUL-11](../contracts/rulings.md#rul-11)：对规范化命令行做 RE2 搜索，不对每个 `args[i]` 单独匹配。因此 `["rm", "-rf", "/"]` 会变成 `rm -rf /` 并命中 `rm\s+-rf\s+/`。`sh -c`、`pwsh -Command` 和 `cmd /c` 的脚本字符串会再搜一次，所以写在这些脚本里的 `curl | sh` 也能命中。`python -c` 与 `node -e` 不在这次搜索里。

用户配置可以加严，不可以删除上表中的格式化与 `curl \| sh` 两类规则。若部署确实要关掉它们，必须在配置里显式 `allow_dangerous: true`，并且 `status` 输出警告。默认 `allow_dangerous` 为 false。

## CON-SEC-11

入口必须按 [CTR-OAUTH-01](../contracts/oauth-authz.md#ctr-oauth-01) 的限速表限制 `POST /oauth/register`、登录口令校验和 `GET /oauth/authorize`。超限返回 HTTP 429，并带 `Retry-After`。

授权服务器只有一个口令，动态注册又不需要管理员点击。没有这道限速时，公网入口就是口令爆破面。计数放在入口进程内存里，重启后清零；它用来拖慢爆破，不是跨节点的配额。

计数的来源 IP 在 `tunnel` 下取代理追加的 `X-Forwarded-For` 最后一项，在 `public:443` 和 `funnel` 下取连接对端，见 [CTR-OAUTH-01](../contracts/oauth-authz.md#ctr-oauth-01)。客户端自填的转发头不得用来换取新的计数桶。

## CON-SEC-12

控制通道只接受内核运行用户的连接（Windows 另含 DACL 授权者），不使用令牌。L2 沙箱内的进程必须不能连接它：Seatbelt 规则拒绝该套接字，bwrap 不绑定状态目录，Windows AppContainer 不在管道 DACL 中。

经控制通道发生的变更写审计，`actor` 为 `control`，只含元数据（[CTR-CTL-02](../contracts/control.md#ctr-ctl-02)）。通道内的事件流、会话视图与审计视图遵守 [INV-10](invariants.md#inv-10)；令牌正文只出现在创建或轮转 PAT 的那一次响应里。

`weakens_security` 是给 UI 弹确认的提示，不是内核的授权边界；同一用户下的进程本来就能改配置文件，通道没有增加权限（[CTR-CTL-07](../contracts/control.md#ctr-ctl-07)）。

验收：另一个 OS 用户连接被拒；Windows 原生测试验证管道 DACL 与远程客户端拒绝；L2 的 exec 子进程连接控制端点失败。
