# 符合性矩阵

阶段验收时按本表勾选。一行的「首次」是这条规则最早必须被测试抓住的阶段；后面的阶段不得把已经绿的测试改成跳过。

建议测试名用 `Test_<编号去横线>`，例如 `Test_CONCONC03_并发打开不超过上限`。表里只写编号和观察点，避免与契约正文漂移。

### 编号引用与机械核对

每个编号至少要有一处测试声明覆盖它，声明方式二选一：

- 测试函数名以 `Test_<编号去横线>` 开头；`Test_CTRFS02_03_...` 同时声明 CTR-FS-02 与 CTR-FS-03。
- `_test.go` 里写一行注释 `// conformance: CON-TOOL-04 CON-TOOL-05`（脚本里用 `# conformance: ...`），放在测试函数上方或函数体内。

`python3 scripts/conformance_check.py` 对照本表与测试代码：表里有编号却没有声明、或注释里引用了表里没有的编号，都会失败（契约夹具工作流每次都跑；`--list` 列出每个编号对应的位置）。它只证明“有人声明了”，不证明断言足够，所以声明只能写在真正断言了观察点的测试上。环境变量才开启的测试（如 `XMESH_TEST_TAILNET_*`）同样要声明，并在下面注明由哪条工作流运行。

自动化与环境覆盖：

| 内容 | 位置 |
| --- | --- |
| CON-ARCH-02：六个发布目标交叉编译 | `Test_CONARCH02_*`，`XMESH_TEST_CROSSBUILD=1`，在 `go.yml` 的 ubuntu 步骤运行；`release.yml` 打包时再构建一次 |
| 门 1、门 2 的进程级冒烟 | `scripts/smoke_local.py`（L1 与 `--pat`）、`scripts/smoke_backend.py`，在 `go.yml` 三个系统的矩阵里运行 |
| 门 3 的 tailnet 互联（仅直连路径） | `headscale.yml`：一次性 Headscale、两个 tsnet 节点互调、`Test_TailnetPeerConnectivity` |
| 门 4 的 OAuth 协议路径 | `headscale.yml` 里的 `scripts/oauth_client.py`：动态注册、PKCE、登录、`tools/list`、`exec.run`；证明协议，不证明 claude.ai 的展示 |
| 发布门禁 | `release.yml` 先跑 vet、竞态测试、矩阵检查，并要求同一提交上 `go.yml`、`contracts.yml`、`headscale.yml` 都已成功 |
| 控制面管理 | `meshadmin.yml`：`go test -race ./meshadmin ./cmd/xmesh`，带校验过的 Headscale 0.29.5 二进制运行集成场景 |

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
| CTR-CFG-02 | 对应 [CTR-CFG-01](contracts/config.md#ctr-cfg-01) 的“热加载与冷加载”一节（该节尚未单独编号，测试以 `Test_CTRCFG02_*` 命名）：热加载失败保持上一份有效配置，写入 `policy_reload_failed` 审计事件并累计到 `sys.info` 的 `policy_reload_failures`；`node`、`mesh`、`ingress` 变更只置 `restart_required`、不改变运行中的值；后端监督循环随配置增删后端 |
| RUL-01 RUL-04 RUL-05 RUL-07 RUL-08 RUL-10 RUL-11 RUL-12 | 排队含 `retry_after_ms`；缓存；内联阈值；状态名；MCP 承载；编辑错误码；规范化命令行；closed 配额 |

Claude Code 连上本机 URL，完成一次 `fs.read`、一次 `fs.write`、一次 `pty.open` + `pty.write` + `pty.read`。这是门 1 的人工观察，不代替上表的单元测试。

Windows Server 2025 的原生进程、ConPTY、文件及 HTTP MCP 测试已通过，证据见 [Windows 验收记录](validation/2026-10-15-windows.md)。Claude Code 连接与目标用户登录会话仍按各自场景验收。

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

门 2 的场景固定为：后端用一个脚本模拟 IDA，记录收到的参数里没有 `_lease_token`。杀掉脚本后，旧 token 的下一次调用失败，新 acquire 得到更大的 token。这个场景由 `scripts/fake_ida.py` 与 `scripts/smoke_backend.py` 实现：两个 MCP 会话、从外部杀掉后端进程、旧 token 失败、新 token 更大。

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

`headscale.yml` 只在单台 runner 上证明加入、互调和直连连通；DERP 兜底（`XMESH_TEST_TAILNET_REQUIRE_DERP=1`）和三台物理设备仍按[验收记录](validation/2026-10-15-headscale.md)多机进行。

## 门 4 · 阶段 4 Gateway 与 OAuth

| 编号 | 观察点 |
| --- | --- |
| CON-ARCH-01 CON-ARCH-04 CON-ARCH-07 | 云端只有 MCP + OAuth；Gateway 重启不丢 Agent 上的租约；单主体登录 |
| CON-SEC-02 CON-SEC-09 CON-SEC-11 | 入口不能关认证；PKCE 与动态注册可用；注册和登录口令超限返回 429 |
| INV-03 INV-08 | Gateway 无设备状态；云端工具名没有 `local/` |
| CTR-GW-01 CTR-GW-02 CTR-GW-03 | 前缀、搜索、会话默认值只活在当前连接 |
| CTR-OAUTH-01 CTR-OAUTH-02 | 发现文档、授权码、scope 展开与 `tools/list` 过滤 |
| RUL-02 RUL-09 RUL-13 | 入网后本机环回用本节点 ID；入口会话带 oauth client 头；`fs:*` 展开；orphaned 接管要求同一个 oauth client；active 的 transfer 是 owner 自愿交接 |

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

## 控制面管理（meshadmin）

不依赖设备门，随 `meshadmin` 与 `xmesh mesh` 交付。Tailscale 一侧只用夹具（请求形状对照官方客户端），Headscale 一侧用真实 v0.29.5 二进制：本机用 `XMESH_TEST_HEADSCALE_BIN` 指向二进制即可运行，`meshadmin.yml` 在 CI 里下载并校验后运行。DERP 的真实公网探测与官方控制面联调不在自动化里，见 [运行手册](operations.md) 的“tailnet 管理”一节。

| 编号 | 观察点 |
| --- | --- |
| CON-MESH-01 | `meshadmin` 不引用 `internal/`；内核、Gateway、Agent 不引用 `meshadmin` |
| CON-MESH-02 | 登录与所有 CLI 输出里搜不到凭据和新建的 auth key；只读凭据发起写请求被拒 |
| CON-MESH-03 | 默认只预览；`--apply` 才写；etag 冲突不覆盖；快照可回滚 |
| CON-MESH-04 | 含用户注释、规则与同名 tagOwners 的策略往返后只有标记片段变化 |
| CON-MESH-05 | region id 在 900 到 999；探测失败后策略恢复为写前文本 |
| CON-MESH-06 | 放行文件损坏时拒绝；真实 `derpserver` 拒绝未放行节点；管理端点需要 token |
| CTR-MESH-01 | 两个 Provider 的设备、tag、key 请求形状；明文 HTTP 只许环回 |
| CTR-MESH-02 | 模板、托管片段幂等与回滚、凭据文件格式 |
| CTR-MESH-03 | 设备检查、权限矩阵合并网络层与工具层 |
| CTR-MESH-04 | DERP 片段写入、Headscale 生成的 YAML 被真实 Headscale 加载、探测的每项原因 |
| CTR-MESH-05 | 准入接口契约、控制面设备同步到放行名单 |
| RUL-20 RUL-21 RUL-22 | 只经 `Capabilities` 区分控制面；Headscale 版本检查为客户端侧；读写凭据分离 |

## 阶段 6 · 桌面 UI 与本机控制通道（U0 契约）

契约先行：模式、夹具、Go 类型、TS 生成物。内核实现（U1、U2）与 UI（U3 起）各自追加自己的行，不改下表。

| 编号 | 观察点 |
| --- | --- |
| CTR-CTL-02 CTR-CTL-03 | 状态与错误体：每个夹具通过模式，解码进 Go 类型再编码后语义不变；反例（未知字段、字符串形式的警告、缺毫秒的时间戳）失败；Go 枚举与错误码到 HTTP 状态的映射和模式相同 |
| CTR-CTL-04 | 事件：九种类型的夹具各有一个；载荷解码进类型化结构；SSE 帧是 `id`、`event`、`data`；含 stdout 的调用事件、含终端数据的会话事件被拒绝 |
| CTR-CTL-05 CTR-CTL-06 CTR-CTL-07 | 对端、会话、drain、配置读写的请求与响应夹具；终端缓冲字段被拒绝；`replace` 缺 `before` 被拒绝 |
| CTR-CTL-08 CTR-CTL-09 CTR-CTL-10 CTR-CTL-11 | 配对、PAT、入口、审计夹具；令牌与 `secret_hash` 泄漏的反例失败；`PatSecret` 之外的类型没有令牌字段；审计元素含 stdout 的反例失败 |
| CTR-CTL-12 | 路由表通过模式，路由编号与「方法加路径」唯一，每个能力至少一条路由，请求与响应类型都有 Go 类型与正例；TS 生成物与模式一致（`contracts.yml`） |

## 模式夹具

每次改契约或模式，都要执行：

```bash
python3 docs/contracts/schemas/check_examples.py
```

正例在 `docs/contracts/schemas/examples/`，反例在同目录的 `invalid/`。这一步不代替上表的行为测试，它只保证错误、租约、审计和配置的形状没有被改松。
