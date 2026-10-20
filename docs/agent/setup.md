# Plan, apply and verify setup

First run the persistent CLI returned by the installer:

```bash
xmesh setup inspect --json
```

Read platform, architecture, suggested node name, installed clients, existing nodes and jobs. Reuse an existing matching node when appropriate. Ask the user for purpose (`local`, `multi-device`, `cloud`), an absolute workspace directory, permission (`development`, `readonly`), and clients (`codex`, `claude-code`, or an empty list to defer client setup).

Save a UTF-8 JSON input file in a private local directory:

```json
{
  "schema_version": 1,
  "purpose": "local",
  "node_name": "my-mac",
  "workspace": "/absolute/project",
  "permission": "development",
  "clients": ["codex"],
  "autostart": true,
  "network": {"mode": "lan"}
}
```

Use a Windows absolute path with JSON escaping on Windows, for example `"workspace": "C:\\Users\\Alice\\work\\project"`. Development permission allows terminal actions and file changes within policy. Readonly permission is for reading the selected workspace. Explain the selected access before asking the user to approve the resulting plan.

```bash
xmesh setup plan --input /absolute/setup-input.json --json
xmesh setup apply --plan PLAN_ID --json
xmesh setup status --job JOB_ID --json
xmesh setup verify --job JOB_ID --json
```

`plan` returns the ID, normalized input, config path, summary, client changes and steps. Its config preview redacts sensitive existing backend fields; apply the saved plan ID so the private original is used. Show the changes and intended access to the user. Apply only their approved plan ID. Keep both plan ID and resulting job ID. Status and verify return individual step states and readiness (`installed`, `service_ready`, `client_verified`). When the job reaches `waiting_user`, read its `verification_tool` and `verification_path`. Ask the user to send this exact request from the intended Codex or Claude Code client, substituting both values from the job:

```text
Call the xmesh tool VERIFICATION_TOOL with {"path":"VERIFICATION_PATH"} and show the result.
```

The tool is normally `local/fs.stat`, or `<node_name>/fs.stat` for a cloud connection. The path contains a challenge unique to this job, for example `/absolute/project/.xmesh-connection-check-JOB_ID`. Preserve the returned path exactly, with appropriate JSON escaping. After the actual client completes this request, run `setup verify --job JOB_ID --json`. The server checks a successful matching audit event; an ordinary health check or another path does not satisfy this challenge. Multi-device verification also checks a real remote-device tool. If a client has not yet been selected, keep the job pending user verification until that client is connected.

For multiple devices choose `purpose: "multi-device"`. The setup input protocol stays at `schema_version: 1`; generated runtime configuration uses `schema_version: 2` and the owner-only Control API 2. Local setup disables both network paths. For LAN and Tailnet together, use `network: {"mode":"coexist","auth_method":"browser"}` and pair the remote device once; both paths share the same xmesh device identity. Tailnet mode uses `network: {"mode":"tailnet","auth_method":"browser"}`; the browser login is completed by the user. `control_url` can select an organization-approved coordination server. Key login uses `auth_method: "key"` and a private local `authkey_file`; never put the key itself in the JSON or prompt. Pairing over LAN is also available and requires the peer certificate/fingerprint exchange shown by the application.

For a cloud agent choose `purpose: "cloud"` and tailnet/browser login. Setup guides the user through Funnel availability and resolves the public issuer after login. After the service is ready, use the wizard's masked field to set the OAuth login password. Agents can use `setup ingress-password --job JOB_ID --input - --json` with a single JSON stdin object containing `password`, supplied through a private user input channel. Use 12–256 Unicode characters with no leading/trailing whitespace or line breaks. The result reports `ingress_password_set`; the value is passed through stdin and the owner-only Control API, then retained by the local ingress password store. Enter the chosen password directly in the browser authorization screen. The intended cloud client must complete OAuth and the exact verification request before readiness can succeed. If the organization cannot provide the required public entry, keep the job's reported next action and resolve that prerequisite with the user.

For an interrupted job:

```bash
xmesh setup status --job JOB_ID --json
xmesh setup resume --job JOB_ID --json
xmesh setup verify --job JOB_ID --json
xmesh doctor --json
```

Use `--root /absolute/state-directory` after each subcommand to keep all operations on the same state root. `setup doctor` is also accepted. Do not infer that a successful service step proves browser login, public reachability or client verification.

`setup resume` publishes `pending` before starting a new worker. If authorization is already running, it returns that worker's current status. Continue polling until a new terminal state is observed. Each worker attempt has a ten-minute limit; an expired authorization attempt reports its recovery action and can be resumed after the prerequisite is resolved.

To stop an authorization wait, use `xmesh setup cancel --job JOB_ID --json`. The request is durable and interrupts the running worker; poll status until `cancelled`. The worker restores an existing service baseline before completing cancellation. Identity state and backups remain available for explicit cleanup or a new setup. A failed recovery reports its next action and remains retryable. Cancelled jobs cannot be resumed.

Setup and local configuration APIs serialize compare-and-replace updates through a shared file lock and recheck the approved bytes before publication. Editors that do not participate in this lock are outside that serialization boundary; close external configuration editors while applying a plan and review backups when resolving a conflict.
