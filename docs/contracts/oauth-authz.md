# OAuth 与授权

## CTR-OAUTH-01

仅 `ingress` 角色启用。授权服务器是单主体的。登录页收集这一个主体的口令或本地配置的登录证明。没有用户注册接口。

### 发现

`GET /.well-known/oauth-protected-resource` 返回 RFC 9728 资源元数据，其中 `authorization_servers` 指向本入口的 issuer。

`GET /.well-known/oauth-authorization-server` 返回 RFC 8414 元数据，至少包括：

| 字段 | 值 |
| --- | --- |
| `issuer` | 配置的 `ingress.oauth.issuer` |
| `authorization_endpoint` | `{issuer}/oauth/authorize` |
| `token_endpoint` | `{issuer}/oauth/token` |
| `registration_endpoint` | `{issuer}/oauth/register` |
| `code_challenge_methods_supported` | `["S256"]` |
| `grant_types_supported` | `["authorization_code", "refresh_token"]` |
| `response_types_supported` | `["code"]` |

issuer 的来源必须与客户端实际访问的 HTTPS 源一致。禁止返回另一个域的 issuer。入口在两处强制这一点：

- 启动时核对 issuer 与监听方式。`public:443` 的 issuer 主机必须等于 `ingress.domain`（不接受 443 以外的端口）；`funnel` 的 issuer 必须是该节点的 `ts.net` 源。不一致拒绝启动，错误指向 `/ingress/oauth/issuer`。`tunnel` 没有可核对的域名，由下一条在运行时兜底。
- 运行时每个请求的 `Host` 必须等于 issuer 的主机（忽略大小写和默认的 443 端口），否则返回 HTTP 421，`error` 为 `misdirected_request`。发现文档、授权、令牌、注册和 `/mcp` 都适用，所以元数据不会在另一个域名下返回。`tunnel` 只监听回环地址，外层隧道可能把 `Host` 改写成回环地址：此时回环 `Host` 被接受；隧道带 `X-Forwarded-Host` 时取其最后一项，必须等于 issuer 的主机。核对通过后，入口把转给 MCP 处理器的请求 `Host` 改为回环地址，因为 MCP SDK 的 DNS 重绑定保护会拒绝回环监听上的公网 `Host`（403），隧道因此可以直接转发公网 `Host`。

### 动态客户端注册

`POST /oauth/register` 实现 RFC 7591 的最小子集：

| 请求字段 | 要求 |
| --- | --- |
| `client_name` | 必须 |
| `redirect_uris` | 必须，至少一个绝对 HTTPS URI。环回 `http://127.0.0.1` 允许，供本机 CLI 使用 |
| `grant_types` | 可以。缺省授权码 |
| `token_endpoint_auth_method` | 允许 `none` 或 `client_secret_basic`。公共客户端使用 `none` 并强制 PKCE |

响应包含 `client_id`。若方法不是 `none`，还包含 `client_secret`，只显示这一次。注册不需要事先的管理员点击；单租户下，能访问入口 URL 的人就是这个用户自己。这是已决的单租户模型，不是多租户开放注册。

已在配置里写好的 `clients.codex` 与 `clients.claude` 是预注册客户端，与动态注册并存。

动态注册的客户端不会得到全部 scope。它的 scope 上限是 `ingress.oauth.dynamic_client_scopes`，缺省 `["devices:read"]`；空数组表示只能完成 OAuth。注册响应的 `scope` 字段给出实际授予的 scope，授权或刷新请求超出该上限返回 `invalid_scope`。需要更多权限时，运维把它们写进上限，或者把客户端预注册在 `clients` 里。入口重启时，已持久化的动态客户端按当前上限收窄，只减不增，因此调低上限或从旧版本升级都会生效；配置里写好的客户端不受影响。

### 限速

单口令加上开放的动态注册，公网入口会被拿来爆破。下列限额是契约的一部分，见 [CON-SEC-11](../constraints/security.md#con-sec-11)。计数在入口进程内存中，按来源 IP，重启后清零。超限的响应是 HTTP 429，并且必须带 `Retry-After`（秒）。

「来源 IP」取决于监听方式：

| 监听方式 | 来源 IP |
| --- | --- |
| `public:443` | 连接的对端地址。`X-Forwarded-For` 由客户端自填，不采信 |
| `funnel` | 连接的对端地址。tsnet 把 Funnel 流的源地址（真实客户端，不是 Funnel 节点）作为对端地址，`X-Forwarded-For` 不采信 |
| `tunnel` | 隧道代理追加的 `X-Forwarded-For` 的**最后一项**；更左边的项由客户端自填，忽略。缺失或不是 IP 地址时退回连接对端（回环地址），所有请求共用同一计数 |

只有 `tunnel` 采信代理头，因为只有它前面恰好有一层受信代理，且监听只在回环地址上，外部无法绕过代理直接连入。

| 动作 | 限额 | 锁定如何结束 |
| --- | --- | --- |
| `POST /oauth/register` | 每个 IP 每小时最多 5 次，不论成功失败 | 窗口滑动，满一小时的旧请求不再计数 |
| 登录口令校验失败 | 每个 IP 连续失败 5 次 | 锁定 15 分钟。一次成功把该 IP 的失败计数清零 |
| `GET /oauth/authorize` | 每个 IP 每分钟最多 60 次 | 窗口滑动 |

口令比对必须消耗大致固定的时间，避免用响应快慢判断口令前缀。锁定期间登录提交直接 429，不再做口令比对。

### 授权码与 PKCE

`GET /oauth/authorize` 要求 `response_type=code`、`client_id`、`redirect_uri`、`code_challenge`、`code_challenge_method=S256`、`state`。缺 `code_challenge` 返回 400，不发码。

用户确认后 302 到 `redirect_uri`，带 `code` 与 `state`。`code` 有效期不超过 60 秒，一次性。

`POST /oauth/token` 用 `grant_type=authorization_code` 交换时校验 `code_verifier`。成功返回 `access_token`、`token_type: Bearer`、`expires_in`、`refresh_token`、`scope`。

access token 有效期固定 1 小时。refresh token 旋转：每次使用后旧 refresh token 失效，新的重新计时；有效期默认 30 天，用 `ingress.oauth.refresh_token_ttl` 在 1 小时到 365 天之间调整。

### 保护 MCP

没有 `Authorization: Bearer` 的 `POST /mcp` 返回 HTTP 401，并带 `WWW-Authenticate`，指向上面的资源元数据。token 的 scope 不足以调用即将执行的工具时，不在 HTTP 层拒绝整个连接；连接可以建立，具体工具返回 `unauthorized`。这样 `tools/list` 可以按 scope 过滤，而不是让客户端拿到一堆调用必失败的工具。

`tools/list` 只包含 scope 允许的工具。

## CTR-OAUTH-02

### 规范 scope

```text
scope        = "devices:read"
             | "exec:" device
             | "pty:" device
             | "fs:read:" device
             | "fs:write:" device
             | "sys:" device
             | "backend:" device ":" backend
device       = "*" | node-name
backend      = "*" | backend-name
node-name    = 配置里的 node.name 语法：字母数字、`-`，长度 1 到 63
backend-name = 字母数字、`-`，长度 1 到 63
```

### 简写

| 简写 | 展开 |
| --- | --- |
| `exec:*` | `exec:*` |
| `pty:*` | `pty:*` |
| `fs:*` | `fs:read:*` 与 `fs:write:*` |
| `fs:<device>` | `fs:read:<device>` 与 `fs:write:<device>` |
| `sys:*` | `sys:*` |

比较前先展开。工具与 scope 的对应：

| 工具前缀 | 需要的 scope |
| --- | --- |
| `devices.list`、`tools.search` | `devices:read` |
| `<device>/sys.*` | `sys:<device>` 或 `sys:*` |
| `<device>/exec.*` | `exec:<device>` 或 `exec:*` |
| `<device>/pty.*` | `pty:<device>` 或 `pty:*` |
| `<device>/fs.read`、`fs.download`、`fs.glob`、`fs.grep`、`fs.list`、`fs.stat`、`fs.sha256` | `fs:read:<device>` |
| `<device>/fs.write`、`fs.upload`、`fs.edit`、`fs.mkdir`、`fs.move`、`fs.delete` | `fs:write:<device>` |
| `<device>/<backend>.<tool>` | `backend:<device>:<backend>` 或带 `*` 的相应项 |
| `<device>/backend.acquire` 等 | 与该后端相同的 backend scope |
| `session.set_defaults` | `devices:read` |

`fs.read` 类工具不接受只拥有 `fs:write` 的 token。写工具也不接受只拥有 `fs:read` 的 token。

## CTR-AUTHZ-01

### 顺序

```text
传输身份
  → 入口或 Gateway 的 scope / PAT / 节点允许列表
    → 路由到目标 Agent
      → WhoIs tag
        → agent.authz 的 backends 与 tools
          → cwd 与命令策略
            → 资源配额、租约、owner
              → 执行
```

任一步失败都停住。错误码：

| 步骤失败 | error |
| --- | --- |
| 没有 token 且不是允许的 tailnet 身份 | HTTP 401，或工具 `unauthenticated` |
| scope 不够 | `unauthorized` |
| WhoIs 失败 | `unauthenticated` |
| tag 未列出，或工具/后端不在允许列表 | `unauthorized` |
| 策略 | `policy_denied` 或 `invalid_cwd` |

### tag 允许列表

`agent.authz` 的值：

```yaml
tag:ingress:
  backends: ["ida"]
  tools: ["sys.*", "exec.*", "pty.*", "fs.*"]
```

`agent.authz` 的键是 tag，也可以含 `*`，按 `path.Match` 与调用方的 tag 比较：`tag:*` 匹配任意 `tag:` 开头的 tag，`tag:ingress-*` 匹配 `tag:ingress-eu` 而不匹配 `tag:ingress`。调用方有多个 tag 或多个键同时匹配时，允许列表取并集；没有任何键匹配仍然拒绝。

`tools` 的项支持末尾 `*`，只匹配一个路径段里的前缀，不跨 `/`。`fs.*` 匹配 `fs.read`、`fs.delete`，不匹配 `fsx.read`。`backends: ["*"]` 表示全部后端。后端工具还要同时通过 `tools` 与 `backends`：`ida.decompile_function` 需要 backends 含 `ida` 或 `*`，且 tools 含 `ida.*` 或 `*`。

内置工具不受 `backends` 限制，只受 `tools` 限制。

### 默认拒绝

tag 没有配置项 → 拒绝。`tools` 为空数组 → 该 tag 不能调用任何工具。禁止「缺省允许」。

### PAT

tailnet 模式的 PAT 是入口之外的本地凭据，绑定一组与 OAuth 相同的 scope。PAT 只呈给本机 Gateway。Agent 仍然用 WhoIs 做第二层，不接受调用方把 PAT 转贴到工具参数里。

配置为 `gateway.pats.<name>.token_file` 和 `scopes`。名称区分本机会话身份与调用预算；Gateway 转发时保留本节点身份。每次请求重新认证，文件轮转、删除配置及 scope 收窄立即约束后续请求，scope 改变须重新建立 MCP 会话。具体文件约束见[配置](config.md#密钥)。
