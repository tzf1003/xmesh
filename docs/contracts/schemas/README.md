# 契约模式

这些 JSON Schema 是契约的机器可读部分。人类可读的规则仍以对应的 `CTR-*` 文档为准。二者冲突时先改文档，再改模式，然后跑校验脚本。

| 模式 | 契约 | 正例 |
| --- | --- | --- |
| [`error.schema.json`](error.schema.json) | [CTR-ERR-01](../errors.md#ctr-err-01) | `examples/error-*.json` |
| [`lease.schema.json`](lease.schema.json) | [CTR-LEASE-01](../backend-lease.md#ctr-lease-01) | `examples/lease.json` |
| [`audit-event.schema.json`](audit-event.schema.json) | [CTR-AUD-01](../audit.md#ctr-aud-01) | `examples/audit-event.json` |
| [`config.schema.json`](config.schema.json) | [CTR-CFG-01](../config.md#ctr-cfg-01) | `examples/xmesh.config.json`、`examples/xmesh.ingress-public.json` |

`examples/invalid/` 里的文件必须校验失败。它们防止模式被放宽到能接受「占用中却没有持有者」、`relay` 模式、Funnel 上再写自定义域名、`pty_limit` 使用字段 `active`，或在审计摘要里写入命令输出。

依赖写在 [`requirements.txt`](requirements.txt)。GitHub Actions 工作流 `.github/workflows/contracts.yml` 在推送和拉取请求上跑同一条命令。

```bash
python3 -m pip install -r docs/contracts/schemas/requirements.txt
python3 docs/contracts/schemas/check_examples.py
```

脚本会检查正例通过、反例失败，并检查文档库里的相对链接是否都能落到文件。
