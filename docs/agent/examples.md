# Versioned setup examples

These examples are distributed with the selected release. Replace workspace paths with an existing absolute directory on the target computer. On Windows use escaped drive paths such as `C:\\Users\\Alice\\project`.

## Local AI

```json
{"schema_version":1,"purpose":"local","node_name":"my-computer","workspace":"/absolute/project","permission":"development","clients":["codex"],"autostart":true,"network":{"mode":"lan"}}
```

Expected apply response: `ok: true`, `data.id` holds the durable job ID. Status proceeds through config, service and client steps to `waiting_user` with service readiness true and client verification false. Send the returned nonce request from Codex and run verify. Expected final readiness: all three fields true and `status: succeeded`.

## Multiple computers

```json
{"schema_version":1,"purpose":"multi-device","node_name":"my-laptop","workspace":"/absolute/project","permission":"readonly","clients":["claude-code"],"autostart":true,"network":{"mode":"lan"}}
```

After service startup, open the Devices page and pair another xmesh computer using its connection password or certificate fingerprint. Perform the returned nonce request from Claude Code, then verify. Expected final success additionally requires the gateway to discover an online paired device and complete its `sys.info` call. For different networks use `network: {"mode":"tailnet","auth_method":"browser"}` and finish browser authorization on both devices.

## Cloud AI

```json
{"schema_version":1,"purpose":"cloud","node_name":"my-cloud-device","workspace":"/absolute/project","permission":"readonly","clients":[],"autostart":true,"network":{"mode":"tailnet","auth_method":"browser"}}
```

Status may report `waiting_user` with a browser challenge or administrator approval. After tags/Funnel/HTTPS are ready, the job exposes `ingress_url` and asks for an OAuth login password. Set it privately, add that URL to the cloud client's connector settings, complete OAuth, and call the returned `<node>/fs.stat` nonce request. Verify succeeds only with the matching OAuth-authenticated audit evidence. Local health checks leave this flow pending.
