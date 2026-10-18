# 契约模式

这些 JSON Schema 是契约的机器可读部分。人类可读的规则仍以对应的 `CTR-*` 文档为准。二者冲突时先改文档，再改模式，然后跑校验脚本。

| 模式 | 契约 | 正例 |
| --- | --- | --- |
| [`error.schema.json`](error.schema.json) | [CTR-ERR-01](../errors.md#ctr-err-01) | `examples/error-*.json` |
| [`lease.schema.json`](lease.schema.json) | [CTR-LEASE-01](../backend-lease.md#ctr-lease-01) | `examples/lease.json` |
| [`audit-event.schema.json`](audit-event.schema.json) | [CTR-AUD-01](../audit.md#ctr-aud-01) | `examples/audit-event.json` |
| [`config.schema.json`](config.schema.json) | [CTR-CFG-01](../config.md#ctr-cfg-01) | `examples/xmesh.config.json`、`examples/xmesh.ingress-public.json`、`examples/xmesh.ingress-multihost.json` |

`config.schema.json` 的顶层必填项是 `node`、`mesh`、`agent`，与 `roles` 无关：只启用 `gateway` 或 `ingress` 角色的配置也必须带 `agent` 段，并且 `agent.cwd_policy.allow` 必须存在（可以为空）。这比 [CTR-CFG-01](../config.md#ctr-cfg-01) 的角色联动表（“`roles` 含 `agent` 才要求 `cwd_policy.allow`”）严格；以模式和实际校验行为为准。

`examples/invalid/` 里的文件必须校验失败。它们防止模式被放宽到能接受「占用中却没有持有者」、`relay` 模式、Funnel 上再写自定义域名、`pty_limit` 使用字段 `active`，或在审计摘要里写入命令输出。

依赖写在 [`requirements.txt`](requirements.txt)。GitHub Actions 工作流 `.github/workflows/contracts.yml` 在推送和拉取请求上跑同一条命令。

```bash
python3 -m pip install -r docs/contracts/schemas/requirements.txt
python3 docs/contracts/schemas/check_examples.py
```

脚本会检查正例通过、反例失败，并检查文档库里的相对链接是否都能落到文件。

控制通道（[control.md](../control.md)）的模式是 `control-*.schema.json`，共用 `$defs` 里的类型，没有根类型。它们由 [`check_control.py`](check_control.py) 检查（`check_examples.py` 会调用它）：夹具放在 `examples/control/<定义名>.<变体>.json`，反例在 `examples/control/invalid/`，路由表 `control-routes.json` 的每个请求与响应定义都必须有正例。Go 类型在 `api/control/v1`，TypeScript 类型由 `scripts/gen-control-ts` 生成到 `api/control/v1/ts/control.ts`，见 [CTR-CTL-12](../control.md#ctr-ctl-12)。
