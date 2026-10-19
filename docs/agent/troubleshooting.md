# Fix installation or configuration

Start with the installer event `phase` or `xmesh doctor --json`. Inspect the durable setup job with the same `--root` used to create it. Retain the selected version, plan ID and job ID when handing recovery to another agent.

| Result | Next action |
| --- | --- |
| Download or checksum failure | Retry the same pinned version; replace corrupt bytes with a verified download or matching complete offline bundle. |
| `trust` installer phase | Use an accepted signed release or the organization's approved system authorization procedure. |
| `plan_stale` | Read the latest files, regenerate the plan and review the new changes. |
| `busy` | Another process owns this node's operation; query its status and wait for its result. |
| `service_conflict` | A same-name native service points to another configuration; choose a new node name. |
| `waiting_user` and `auth_url` | Open the URL only after checking its control-plane origin; complete browser login or administrator approval. |
| Network or Funnel prerequisites | Ask the tailnet administrator to grant device/ingress tags, permit Funnel and enable HTTPS; resume the existing job. |
| Service step failed | Read doctor output, check user-service permissions and port availability, then resume. |
| Client configuration changed | Review the current client file, regenerate the plan and preserve concurrent edits. |
| `client_pending` | Reload the intended client, perform the exact nonce-path request, then run verify. |
| `ingress_password_pending` | Set the login password through the masked desktop field or private stdin input, then complete cloud OAuth. |
| Service recovery pending | Inspect the original configuration and native registration backups before restoring; keep later user edits. |

Diagnostics contain status and stable codes. The desktop Copy diagnostics action removes browser challenge URLs and credential-shaped fields. Share diagnostics only with the user's choice of destination. Recovery semantics and examples are in [recovery.md](recovery.md).
