# 契约索引

契约描述跨边界的行为：调用方把什么送进来，成功时拿回什么，失败时拿回哪一个错误对象，以及哪些状态会变。实现与测试以编号为准。方案正文与契约冲突时，以契约为准，并应回头修正方案正文。

原文没有给出唯一行为的地方，先看 [裁定](rulings.md)。契约引用裁定编号，不在每份文档里重新争论。

错误的公共形状在 [CTR-ERR-01](errors.md#ctr-err-01)。下文每个工具只列自己会返回的 `error` 码。

## 编号

| 编号 | 文档 | 内容 |
| --- | --- | --- |
| [CTR-ERR-01](errors.md#ctr-err-01) | 错误 | 信封、MCP 承载、错误码 |
| [CTR-ID-01](identity.md#ctr-id-01) | 身份 | `caller_id` |
| [CTR-CWD-01](cwd.md#ctr-cwd-01) | 路径 | Resolver |
| [CTR-SYS-01](sys.md#ctr-sys-01) | sys | `sys.info` |
| [CTR-EXEC-01](exec.md#ctr-exec-01) | exec | `exec.run` |
| [CTR-EXEC-02](exec.md#ctr-exec-02) | exec | `exec.output` |
| [CTR-PTY-01](pty.md#ctr-pty-01) | pty | 状态机 |
| [CTR-PTY-02](pty.md#ctr-pty-02) | pty | 八个工具 |
| [CTR-FS-01](fs.md#ctr-fs-01) | fs | `fs.read` |
| [CTR-FS-02](fs.md#ctr-fs-02) | fs | `fs.write` |
| [CTR-FS-03](fs.md#ctr-fs-03) | fs | `fs.edit` |
| [CTR-FS-04](fs.md#ctr-fs-04) | fs | `fs.glob` |
| [CTR-FS-05](fs.md#ctr-fs-05) | fs | `fs.grep` |
| [CTR-FS-06](fs.md#ctr-fs-06) | fs | `fs.list` / `fs.stat` / `fs.sha256` |
| [CTR-FS-07](fs.md#ctr-fs-07) | fs | `fs.mkdir` / `fs.move` / `fs.delete` |
| [CTR-BE-01](backend-lease.md#ctr-be-01) | 后端 | 透传与 token 剥离 |
| [CTR-BE-02](backend-lease.md#ctr-be-02) | 后端 | acquire / renew / release |
| [CTR-LEASE-01](backend-lease.md#ctr-lease-01) | 租约 | 内存表与过期 |
| [CTR-MUX-01](mcpmux.md#ctr-mux-01) | 多路复用 | id 重映射 |
| [CTR-MUX-02](mcpmux.md#ctr-mux-02) | 多路复用 | progressToken |
| [CTR-MUX-03](mcpmux.md#ctr-mux-03) | 多路复用 | sampling / elicitation |
| [CTR-MUX-04](mcpmux.md#ctr-mux-04) | 多路复用 | 崩溃与 list_changed |
| [CTR-GW-01](gateway.md#ctr-gw-01) | Gateway | 工具名 |
| [CTR-GW-02](gateway.md#ctr-gw-02) | Gateway | `devices.list` / `tools.search` |
| [CTR-GW-03](gateway.md#ctr-gw-03) | Gateway | `session.set_defaults` |
| [CTR-GW-04](gateway.md#ctr-gw-04) | Gateway | 发现 |
| [CTR-GW-05](gateway.md#ctr-gw-05) | Gateway | 下线 |
| [CTR-OAUTH-01](oauth-authz.md#ctr-oauth-01) | OAuth | 端点与 PKCE |
| [CTR-OAUTH-02](oauth-authz.md#ctr-oauth-02) | OAuth | scope 语法 |
| [CTR-AUTHZ-01](oauth-authz.md#ctr-authz-01) | 授权 | 双层判定 |
| [CTR-AUD-01](audit.md#ctr-aud-01) | 审计 | 事件字段 |
| [CTR-CFG-01](config.md#ctr-cfg-01) | 配置 | `xmesh.yaml` |
| [CTR-MESH-01](meshadmin.md#ctr-mesh-01) | meshadmin | Provider 与两个控制面的差异 |
| [CTR-MESH-02](meshadmin.md#ctr-mesh-02) | meshadmin | 凭据、策略写入协议、模板 |
| [CTR-MESH-03](meshadmin.md#ctr-mesh-03) | meshadmin | 设备与 tag、接入、权限矩阵 |
| [CTR-MESH-04](meshadmin.md#ctr-mesh-04) | meshadmin | 第三方 DERP 与探测 |
| [CTR-MESH-05](meshadmin.md#ctr-mesh-05) | meshadmin | DERP 准入与放行名单同步 |
| [CTR-CTL-01](control.md#ctr-ctl-01) | 控制通道 | 传输、发现、身份 |
| [CTR-CTL-02](control.md#ctr-ctl-02) | 控制通道 | 约定、版本兼容、ETag、审计 |
| [CTR-CTL-03](control.md#ctr-ctl-03) | 控制通道 | `GET /v1/status`、警告、托盘状态判定 |
| [CTR-CTL-04](control.md#ctr-ctl-04) | 控制通道 | `GET /v1/events` 事件流 |
| [CTR-CTL-05](control.md#ctr-ctl-05) | 控制通道 | 对端与会话视图 |
| [CTR-CTL-06](control.md#ctr-ctl-06) | 控制通道 | drain / resume |
| [CTR-CTL-07](control.md#ctr-ctl-07) | 控制通道 | 配置读写 |
| [CTR-CTL-08](control.md#ctr-ctl-08) | 控制通道 | LAN 配对 |
| [CTR-CTL-09](control.md#ctr-ctl-09) | 控制通道 | PAT |
| [CTR-CTL-10](control.md#ctr-ctl-10) | 控制通道 | 入口客户端与口令 |
| [CTR-CTL-11](control.md#ctr-ctl-11) | 控制通道 | 审计视图 |
| [CTR-CTL-12](control.md#ctr-ctl-12) | 控制通道 | 路由表、Go 与 TypeScript 生成物 |
| [CTR-CTL-14](control.md#ctr-ctl-14) | 控制通道 | stdio 后端的环境变量 |
| [CTR-CTL-15](control.md#ctr-ctl-15) | 控制通道 | GitHub Actions 运行器托管 |

Computer 的开发中契约见 [computer.md](computer.md)，验收进度见 [三端方案](../design/17-computer.md)。

## 模式文件

| 文件 | 对应契约 |
| --- | --- |
| [schemas/error.schema.json](schemas/error.schema.json) | CTR-ERR-01 |
| [schemas/lease.schema.json](schemas/lease.schema.json) | CTR-LEASE-01 |
| [schemas/audit-event.schema.json](schemas/audit-event.schema.json) | CTR-AUD-01 |
| [schemas/config.schema.json](schemas/config.schema.json) | CTR-CFG-01 |
| [schemas/control-*.schema.json](schemas/) 与 [schemas/control-routes.json](schemas/control-routes.json) | CTR-CTL-01 至 CTR-CTL-15，列表见 [control.md](control.md) |
| [schemas/examples/](schemas/examples/) | 错误与配置的正例、反例 |
| [schemas/examples/control/](schemas/examples/control/) | 控制通道的正例，`invalid/` 下是反例，文件名 `<定义名>.<变体>.json` |

模式使用 JSON Schema draft 2020-12。`$id` 使用 `urn:xmesh:schema:*`，不依赖尚未决定的公网域名。配置文件是 YAML，校验前先按 YAML 1.2 解析成 JSON 数据模型，再套 `config.schema.json`。
