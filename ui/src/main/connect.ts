import { type CopyKind, type GatewayCopyKind, isGatewayCopyKind } from "../shared/ipc";
import { MESH_LOGIN_COMMANDS } from "../shared/mesh";
import { isSafeToken } from "../shared/gateway";

// The gateway URL comes from the kernel and ends up in text the user pastes
// into a shell or a config file, so anything beyond a plain http(s) URL is refused.
const SAFE_URL = /^https?:\/\/[A-Za-z0-9._~:/?#@!&=+%[\]-]+$/;

export function isSafeGatewayUrl(url: string): boolean {
  return SAFE_URL.test(url);
}

/**
 * Text to put on the clipboard, or undefined when there is nothing safe to copy. Every gateway kind
 * carries the bearer token: the gateway refuses requests without one, so a command without it would
 * only fail later. The token comes from the token file in the main process; it is never passed to the page.
 */
export function clipboardText(kind: CopyKind, gatewayUrl: string | undefined, token?: string): string | undefined {
  // Terminal commands for the tailnet admin login; the secret is typed into the CLI, never into this app.
  if (kind === "meshLoginRead") return MESH_LOGIN_COMMANDS.read;
  if (kind === "meshLoginWrite") return MESH_LOGIN_COMMANDS.write;
  if (kind === "meshLoginReadHeadscale") return MESH_LOGIN_COMMANDS.readHeadscale;
  if (kind === "meshLoginWriteHeadscale") return MESH_LOGIN_COMMANDS.writeHeadscale;
  if (!isGatewayCopyKind(kind)) return undefined;
  if (gatewayUrl === undefined || !isSafeGatewayUrl(gatewayUrl)) return undefined;
  if (token === undefined || !isSafeToken(token)) return undefined;
  return gatewayText(kind, gatewayUrl, token);
}

function gatewayText(kind: GatewayCopyKind, url: string, token: string): string {
  switch (kind) {
    case "gatewayUrl":
      return `${url}\nAuthorization: Bearer ${token}`;
    case "claudeCodeCommand":
      return `claude mcp add --transport http xmesh ${url} --header "Authorization: Bearer ${token}"`;
    case "codexConfig":
      return `[mcp_servers.xmesh]\nurl = "${url}"\nhttp_headers = { Authorization = "Bearer ${token}" }\n`;
  }
}
