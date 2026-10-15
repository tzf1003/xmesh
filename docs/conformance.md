# 符合性矩阵

阶段验收时按本表勾选。一行的「首次」是这条规则最早必须被测试抓住的阶段；后面的阶段不得把已经绿的测试改成跳过。

建议测试名用 `Test_<编号去横线>`，例如 `Test_CONCONC03_并发打开不超过上限`。表里只写编号和观察点，避免与契约正文漂移。

## 门 1 · 阶段 1 工具基础

本机 MCP，不依赖 tailnet。节点还没有 tailnet 身份时，`caller_id` 使用 `local/<session>`。已有身份后的环回改用本节点 ID，见 RUL-02。

| 编号 | 观察点 |
| --- | --- |
| CON-ARCH-02 CON-ARCH-03 CON-ARCH-06 | 一个进程提供 Agent；没有 worktree 工具 |
| CON-ARCH-05 | 拒绝是同一次调用里的结构化错误 |
| CON-TOOL-01 CON-TOOL-02 CON-TOOL-03 | `~`、`@别名`、软链逃逸；响应带 `resolved_path` |
| CON-TOOL-04 CON-TOOL-05 CON-TOOL-06 CON-TOOL-07 CON-TOOL-08 CON-TOOL-09 | 换行与 BOM、上限、递归删除、原子写、唯一编辑 |
| CON-CONC-01 CON-CONC-03 CON-CONC-04 CON-CONC-05 | 20 路并发 open；第 21 路 `pty_limit`；非 owner 不能写 |
| CON-CONC-09 CON-CONC-10 | 幂等缓存按 caller 隔离；exclusive 等 shared 排空 |
| CON-SEC-04 CON-SEC-05 CON-SEC-10 | 凭证不进 env；不拦 PTY 内的 cd；`["rm","-rf","/"]` 命中删除根的规则 |
| CON-IMPL-01 CON-IMPL-03 CON-IMPL-04 CON-IMPL-05 | 包顺序可在本机测；锁分离；Windows shell；grep 两路同构 |
| INV-02 INV-05 INV-06 INV-07 | `closed` 不占名额；`pty_limit.in_use` 不含 closed；拒绝无副作用；原子写；cursor 覆写 |
| CTR-ERR-01 CTR-ID-01 CTR-CWD-01 | 错误模式、身份、路径 |
| CTR-SYS-01 | `sys.info` 字段与配置一致 |
| CTR-EXEC-01 CTR-EXEC-02 | 不经 shell、截断与分页、超时不留下失控进程 |
| CTR-PTY-01 CTR-PTY-02 | 四状态、long-poll、关闭顺序、列表不含缓冲文本 |
| CTR-FS-01 CTR-FS-02 CTR-FS-03 CTR-FS-04 CTR-FS-05 CTR-FS-06 CTR-FS-07 | 各文件工具的错误码与成功字段 |
| CTR-CFG-01 | `examples/xmesh.config.json` 通过模式；Funnel 配了 domain 的反例失败；非法配置拒绝启动 |
| RUL-01 RUL-04 RUL-05 RUL-07 RUL-08 RUL-10 RUL-11 RUL-12 | 排队含 `retry_after_ms`；缓存；内联阈值；状态名；MCP 承载；编辑错误码；规范化命令行；closed 配额 |

Claude Code 连上本机 URL，完成一次 `fs.read`、一次 `fs.write`、一次 `pty.open` + `pty.write` + `pty.read`。这是门 1 的人工观察，不代替上表的单元测试。

## 门 2 · 阶段 2 后端与并发

| 编号 | 观察点 |
| --- | --- |
| CON-TOOL-10 CON-TOOL-11 | 透传；`_lease_token` 不进入后端 |
| CON-CONC-02 CON-CONC-06 CON-CONC-07 CON-CONC-08 CON-CONC-11 CON-CONC-12 | 身份、actor 串行、token、重启清空、断线 Orphan、id 重映射 |
| CON-IMPL-02 | 映射测试不依赖真实后端二进制与 tsnet |
| INV-01 INV-04 | 一个持有者；剥离 token |
| CTR-BE-01 CTR-BE-02 CTR-LEASE-01 | 两个客户端同时调 exclusive 工具，无租约方得到 `resource_busy` |
| CTR-MUX-01 CTR-MUX-02 CTR-MUX-03 CTR-MUX-04 | 相同上游 id 不串线；进度不广播；无持有者拒绝 sampling；崩溃后旧 token 失效且两端都收到 `backend_unavailable` |
| RUL-03 RUL-06 | acquire 不自动续期；renew 与 release 的参数名也是 `_lease_token` |

门 2 的场景固定为：后端用一个脚本模拟 IDA，记录收到的参数里没有 `_lease_token`。杀掉脚本后，旧 token 的下一次调用失败，新 acquire 得到更大的 token。

## 门 3 · 阶段 3 互联

| 编号 | 观察点 |
| --- | --- |
| CON-ARCH-08 | 关掉入口后，两台设备仍能互调 |
| CON-NET-01 CON-NET-02 CON-NET-03 CON-NET-04 CON-NET-05 CON-NET-06 CON-NET-07 CON-NET-09 | Agent 无公网监听；tsnet 无 TUN；两种控制面；DERP 兜底；tag 二次授权；lan 配对 mTLS |
| CON-NET-08 | 配置 `relay` 时模式校验失败，进程不启动 |
| CON-SEC-01 CON-SEC-03 | 缺 WhoIs 即拒绝；未配置的 tag 默认拒绝 |
| INV-09 | 节点断开后不再路由，不改派 |
| CTR-GW-04 | 发现与 30 秒能力缓存；连接一断立即停路由 |
| CTR-AUTHZ-01 | 入口 scope 通过后，Agent 仍可按 `tag:ingress` 拒绝后端 |

官方 Tailscale 与 Headscale 各跑通一次三台设备。至少一台路径是 DERP 而不是直接打洞，用控制面的连接状态确认，而不是只看 ping 成功。

## 门 4 · 阶段 4 Gateway 与 OAuth

| 编号 | 观察点 |
| --- | --- |
| CON-ARCH-01 CON-ARCH-04 CON-ARCH-07 | 云端只有 MCP + OAuth；Gateway 重启不丢 Agent 上的租约；单主体登录 |
| CON-SEC-02 CON-SEC-09 CON-SEC-11 | 入口不能关认证；PKCE 与动态注册可用；注册和登录口令超限返回 429 |
| INV-03 INV-08 | Gateway 无设备状态；云端工具名没有 `local/` |
| CTR-GW-01 CTR-GW-02 CTR-GW-03 | 前缀、搜索、会话默认值只活在当前连接 |
| CTR-OAUTH-01 CTR-OAUTH-02 | 发现文档、授权码、scope 展开与 `tools/list` 过滤 |
| RUL-02 RUL-09 RUL-13 | 入网后本机环回用本节点 ID；入口会话带 oauth client 头；`fs:*` 展开；orphaned 接管要求同一个 oauth client |

claude.ai 自定义连接器与 Codex 都完成 OAuth。模型实际看到 `win-dev-01/exec.run` 并成功调用一次。连接器只显示已连接、对话里没有工具，视为门 4 失败。

## 阶段 5 · 治理与硬化

没有下一道门。下列条目完成本身就是阶段 5 的验收。

| 编号 | 观察点 |
| --- | --- |
| CON-SEC-06 CON-SEC-07 CON-SEC-08 | 改策略不重启；不录像；审计无正文 |
| INV-10 | 编码器拒绝把 stdout 写进审计 |
| CTR-AUD-01 | 正例通过模式；含 stdout 的反例失败 |
| CTR-GW-05 | `drain: keep` 拒绝新调用并保留旧 PTY；`drain: kill` 终止会话 |
| CON-SEC-10 | L2 沙箱在 Linux 上用 bwrap 或 nsjail 跑一条被允许的命令，并挡住网络与凭证目录 |

云沙箱加入 tailnet 是实验：Codex Cloud 与 Claude Code 云沙箱至少一家用 ephemeral pre-auth key 连上并调用一次对端工具。失败时写下出站限制，主路径（公网入口）的门 4 结果仍然有效。

## 模式夹具

每次改契约或模式，都要执行：

```bash
python3 docs/contracts/schemas/check_examples.py
```

正例在 `docs/contracts/schemas/examples/`，反例在同目录的 `invalid/`。这一步不代替上表的行为测试，它只保证错误、租约、审计和配置的形状没有被改松。
