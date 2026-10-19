# xmesh setup guide for agents

Use the documentation shipped with the installed release. The install page generates a prompt with an exact version and documentation URL; keep that version throughout the session. Read [installation](install.md), [setup](setup.md), and [recovery](recovery.md) before changing the host.

[Client configuration](clients.md), [purpose examples](examples.md), and [troubleshooting](troubleshooting.md) provide focused references for each step.

xmesh connects a chosen workspace to local clients, other devices, or a cloud agent through Remote MCP. Windows and macOS desktop packages contain a persistent CLI. The setup wizard collects purpose, workspace, permissions and clients, creates a reviewable plan, and applies that plan as a resumable job.

The user chooses the workspace and permission level. Ask for those choices when absent. Prefer `readonly` when only browsing is needed. Browser login belongs to the user; never request browser credentials, copy browser session tokens, or store an auth key in a prompt.

## State and readiness

Commands return `{ "ok": true, "data": ... }` with `--json`. Failures return `{ "ok": false, "error": { "code": "...", "message": "...", "next_action": "..." } }`. Read `next_action`, retain plan/job identifiers, and report the failing step. A running service and a verified client are separate readiness states. Ask the user to make a real MCP request from the selected client before reporting the connection verified.

Use `--root /absolute/state-directory` after each setup subcommand when isolating a test or managing a nondefault state directory. The default is the operating system user config directory under `xmesh`. The state root differs from the workspace the user grants access to.
