import type { CopyKind } from "../shared/ipc";

// The gateway URL comes from the kernel and ends up in text the user pastes
// into a shell or a config file, so anything beyond a plain http(s) URL is refused.
const SAFE_URL = /^https?:\/\/[A-Za-z0-9._~:/?#@!&=+%[\]-]+$/;

export function isSafeGatewayUrl(url: string): boolean {
  return SAFE_URL.test(url);
}

/** Text to put on the clipboard, or undefined when there is nothing safe to copy. */
export function clipboardText(kind: CopyKind, gatewayUrl: string | undefined): string | undefined {
  if (gatewayUrl === undefined || !isSafeGatewayUrl(gatewayUrl)) return undefined;
  switch (kind) {
    case "gatewayUrl":
      return gatewayUrl;
    case "claudeCodeCommand":
      return `claude mcp add --transport http xmesh ${gatewayUrl}`;
    case "codexConfig":
      return `[mcp_servers.xmesh]\nurl = "${gatewayUrl}"\n`;
  }
}
