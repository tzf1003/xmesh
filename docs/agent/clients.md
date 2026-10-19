# Connect an AI client

Setup supports Codex CLI (`codex`) and Claude Code (`claude-code`) as user-level HTTP MCP clients. `setup inspect` detects the executable/version and resolved configuration path. Choose clients explicitly in the setup input; an empty list defers configuration and leaves client verification pending.

Codex uses `CODEX_HOME/config.toml`, defaulting to `~/.codex/config.toml`, and adds `[mcp_servers.xmesh]` with the local URL and private Authorization header. Claude Code uses the user `.claude.json` (relocated by `CLAUDE_CONFIG_DIR`) and adds `mcpServers.xmesh` with `type: http`, URL and headers. Existing entries are previewed; a different xmesh entry requires replacement acknowledgement in the wizard. Unrelated client settings are retained. Backups and recovery receipts are private local files.

After apply, reload or restart the selected client so it reads the new configuration. Existing running agent sessions may retain their original MCP server list. Use the client to perform the `verification_tool`/`verification_path` request returned by the job, then call `setup verify`. The client file being readable proves only saved configuration, while a successful job requires the tool call evidence.

Cloud clients connect to `job.ingress_url`, complete OAuth in the browser using the password set in the wizard, and call the cloud-prefixed verification tool. A local client configuration is never written with a public bearer token. For other clients use their supported Streamable HTTP MCP connection mechanism and retain the same authentication and verification requirements.

Reference documentation: Codex MCP at `https://developers.openai.com/codex/mcp`; Claude Code MCP at `https://code.claude.com/docs/en/mcp`. Client configuration changes should be tested against the installed client version before extending this adapter list.
