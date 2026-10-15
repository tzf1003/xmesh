# Gateway

Gateway 与入口不保存设备资源，见 [INV-03](../constraints/invariants.md#inv-03)。它们保存发现结果、到对端的 MCP 客户端，以及每条上游连接上的会话默认值。

## CTR-GW-01

### 名字

对端工具在 Gateway 上的名字是：

```text
<node.name> + "/" + <agent 上的工具名>
```

本机 Agent 的工具在本机 Gateway 上使用前缀 `local/`。入口对外发布时，把本机会被叫成 `local/` 的那一组改成 `<node.name>/`。

工具名的字母、数字、`_`、`-`、`.` 来自 Agent。前缀只用一个 `/` 分隔。Agent 工具名本身不得包含 `/`。

### 转发

1. 若本连接设置过默认 device，且工具名没有 `/`，则补上默认 device 的前缀。`session.set_defaults`、`devices.list`、`tools.search` 不加前缀。
2. 鉴权，见 [CTR-AUTHZ-01](oauth-authz.md#ctr-authz-01)。
3. 补全默认 `cwd` 与 `env`：仅当参数里没有这两个键。调用方显式传入的值，包括空字符串，都不覆盖。
4. 写审计开始记录。
5. 把请求发给目标 Agent。
6. 把 Agent 的结果原样返回，包括 `isError` 与 `structuredContent`。Gateway 不把 `resource_busy` 翻译成别的码。

目标不在线：`not_found`，`name` 为节点名。

## CTR-GW-02

### devices.list

无参数。结果：

```json
{
  "devices": [
    {
      "id": "win-dev-01",
      "os": "windows",
      "online": true,
      "tags": ["tag:device"],
      "backends": ["ida", "frida"],
      "load": {"pty_in_use": 2, "pty_max": 20, "exec_shared_in_use": 1}
    }
  ]
}
```

`id` 是 `node.name`。在线数据来自最近一次成功的 `sys.info`。从未成功过的新节点 `online: true` 但 `os` 可为空，并带 `stale: true`。离线节点保留在列表里最多 24 小时，`online: false`，方便模型解释「刚才那台没了」。超过 24 小时删除。

scope 不含 `devices:read` 的客户端得到 `unauthorized`。

### tools.search

参数：`device`（节点名或 `local`）、`pattern`（大小写不敏感的子串，不是正则）。

在该设备的完整工具表里查找，包括当前 `expose_backends: favorite` 下没有放进默认 `tools/list` 的工具。返回 `tools`：`name`（已带前缀）、`description`。上限 100 条，带 `truncated`。

`pattern` 为空 → `invalid_args`。设备不存在 → `not_found`。

## CTR-GW-03

工具名：`session.set_defaults`。

参数：`device`、`cwd`、`env`，至少提供一个。`device` 必须出现在当前 `devices.list` 的在线集合，或为 `local`。`cwd` 在 Gateway 侧只做字符串保存，真正的路径合法性由目标 Agent 在调用时判定。Gateway 不因为 cwd 看起来不存在而拒绝默认值，它没有那台机器的磁盘权威。

成功：`{"ok": true}`。只影响当前 MCP 连接。同一连接再次调用则整体替换先前提交的键；未提交的键保持不变。传入 JSON `null` 表示清除该键。

Agent 上不存在这个工具。若客户端直连 Agent 并调用它，得到 `not_found`。

## CTR-GW-04

### tailnet

每隔不超过 15 秒调用一次 `LocalClient().Status()`。对 `tag:device` 的新节点建立 MCP 客户端；对消失的节点关闭客户端并触发 [CTR-MUX-04](mcpmux.md#ctr-mux-04)。

对每个在线节点，`sys.info` 的缓存时间不超过 30 秒。调用失败时保留上一份缓存并标记 `stale: true`，连续失败超过 30 秒则把工具表视为空并通知上游，但节点仍显示在线直到 Status 说它不在。

各设备独立刷新连接与能力表。首次 `tools/list` 等待本机 Agent 的能力就绪，远端设备就绪后通过 `tools/list_changed` 加入；对指定设备的首次调用或 `tools.search` 仅等待该设备。已有能力缓存的调用可在后台刷新时继续执行。单次刷新最长 10 秒，失败后至少间隔 5 秒重试；设备身份或地址变化时重新连接。上游会话结束时取消其待完成刷新并关闭下游连接。

### lan

用 mDNS 的出现与消失代替 Status。证书不是 `tag:device` 的节点不连接。

### 路由失效

一旦到某节点的 MCP 客户端断开，立即停止路由，不等待 30 秒缓存。这满足 [INV-09](../constraints/invariants.md#inv-09)。

## CTR-GW-05

控制面下线不是云端 MCP 工具。它是 tailnet 内的控制消息，阶段 5 实现。最小载荷：

| 字段 | 含义 |
| --- | --- |
| `scope` | `device` 或 `mesh` |
| `device` | `scope=device` 时的节点名 |
| `drain` | `keep`（默认）或 `kill` |

Agent 收到后拒绝新的 `exec.run`、`pty.open`、`backend.acquire` 和后端工具，错误码 `policy_denied`，规则名 `mesh.draining`。`drain: keep` 时已有 PTY 与租约按原超时结束。`drain: kill` 时按 `pty.close` 的顺序终止本机全部 PTY，并 release 全部租约。

Gateway 收到后对该设备（或全网）停止路由，并发送 `tools/list_changed`。

## CTR-GW-06

### 入口 PTY 总预算

`ingress.max_pty_per_client` 默认 8。入口把各 Agent 的同一 OAuth client 的 `active` 与 `orphaned` 终端相加；`closed` 释放额度。不同 MCP 会话共享额度。`pty.transfer` 使用目标 Agent 记录的接收方身份，向另一主体转移时检查接收方总额；同主体接管保持原有额度。

入口在整个查询、准入与调用完成期间串行化 `pty.open` 和 `pty.transfer`。查询使用 Agent 的 MCP 控制资源 `xmesh://control/pty-budget`，由已认证且获 PTY 授权的 `tag:ingress` 读取。该资源包含当前终端 reservation、对应预算主体、在线 caller 的主体及进程 epoch，不含终端正文；Gateway 不向公网转发此资源。Agent 的启动 reservation 和断线孤儿均计数。

入口状态目录的 `ingress-pty.json` 保存需要重新查询的设备 ID、Agent epoch 和待完成调用标志。发送有副作用的调用前原子落盘。该记录用于准入治理，终端归属与资源状态始终由 Agent 决定。入口重启后重新查询 Agent。已记载资源的设备离线、旧版 Agent 缺少控制资源或快照无效时，新增与转移返回 `policy_denied`、`rule: quota.unavailable`。读写、关闭与信号仍可作用于在线资源。

调用结果丢失时，待完成标志保留；同一 Agent epoch 的快照无法证明旧调用已经完成，因此继续拒绝新增。重启对应 Agent 并重新查询到新 epoch 后可恢复准入。状态写入失败时停止新增；保留状态目录，修复存储后重启入口。总额已满返回 `policy_denied`、`rule: quota.exceeded`。

入口使用独立的持久化 `.lock` 文件锁住预算文件，在锁内重读记录、查询 Agent、准入和完成调用。同一主机的多个入口通过 `ingress.pty_budget_file` 指向同一本地文件时共享原子准入；进程退出后由操作系统释放锁，待完成标志仍保留。共享文件记录配额值，各入口配置不一致时拒绝启动或新增。版本 1 的既有记录会在下一次写入时补充配额值。

共享入口必须使用支持文件锁的版本、同一 OAuth client 身份域和本地预算文件，并保留预算文件及其 `.lock` 文件。网络文件系统与跨主机入口需要独立的协调服务，当前不承诺跨主机原子总额。直接调用 Agent 使用 Agent 上的设备与主体配额。
