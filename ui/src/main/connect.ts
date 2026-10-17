import { CODEX_PAT_ENV, type CopyKind } from "../shared/ipc";
import { meshLoginCommands } from "../shared/mesh";

// The gateway URL comes from the kernel and ends up in text the user pastes
// into a shell or a config file, so anything beyond a plain http(s) URL is refused.
const SAFE_URL = /^https?:\/\/[A-Za-z0-9._~:/?#@!&=+%[\]-]+$/;

export function isSafeGatewayUrl(url: string): boolean {
  return SAFE_URL.test(url);
}

/**
 * Text to put on the clipboard, or undefined when there is nothing safe to copy. With `auth` "pat" the
 * gateway rejects a request without a bearer token, so the text carries a placeholder where the token
 * goes: the token files are the user's to read, and the UI never holds the secret. `cli` is the CLI as typed
 * in a terminal, used by the login commands.
 */
export function clipboardText(kind: CopyKind, gatewayUrl: string | undefined, auth?: string, cli = "xmesh"): string | undefined {
  // Terminal commands for the tailnet admin login; the secret is typed into the CLI, never into this app.
  const login = meshLoginCommands(cli);
  if (kind === "meshLoginRead") return login.read;
  if (kind === "meshLoginWrite") return login.write;
  if (kind === "meshLoginReadHeadscale") return login.readHeadscale;
  if (kind === "meshLoginWriteHeadscale") return login.writeHeadscale;
  if (gatewayUrl === undefined || !isSafeGatewayUrl(gatewayUrl)) return undefined;
  switch (kind) {
    case "gatewayUrl":
      return gatewayUrl;
    case "claudeCodeCommand":
      return `claude mcp add --transport http xmesh ${gatewayUrl}` + (auth === "pat" ? ` --header "Authorization: Bearer <PAT>"` : "");
    case "codexConfig":
      return `[mcp_servers.xmesh]\nurl = "${gatewayUrl}"\n` + (auth === "pat" ? `bearer_token_env_var = "${CODEX_PAT_ENV}"\n` : "");
    default:
      return undefined;
  }
}
