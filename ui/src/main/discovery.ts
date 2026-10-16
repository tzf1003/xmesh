import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { type ControlEndpointFile, parseManifest } from "../shared/control";

export interface Instance {
  /** Directory name under `<UserConfigDir>/xmesh`. */
  id: string;
  manifest: ControlEndpointFile;
}

/** Go's os.UserConfigDir, so the UI finds what `xmesh` wrote. */
export function userConfigDir(
  platform: NodeJS.Platform = process.platform,
  env: NodeJS.ProcessEnv = process.env,
  home: string = os.homedir(),
): string {
  switch (platform) {
    case "win32":
      return env.APPDATA || path.join(home, "AppData", "Roaming");
    case "darwin":
      return path.join(home, "Library", "Application Support");
    default:
      return env.XDG_CONFIG_HOME || path.join(home, ".config");
  }
}

/** Directory whose children are the node state directories. `XMESH_UI_STATE_ROOT` overrides it (tests, fake kernel). */
export function stateRoot(env: NodeJS.ProcessEnv = process.env): string {
  return env.XMESH_UI_STATE_ROOT || path.join(userConfigDir(process.platform, env), "xmesh");
}

/** Enumerates `<root>/*\/control.json`. Unreadable or malformed manifests are skipped. */
export function discoverInstances(root: string = stateRoot()): Instance[] {
  let entries: fs.Dirent[];
  try {
    entries = fs.readdirSync(root, { withFileTypes: true });
  } catch {
    return [];
  }
  const out: Instance[] = [];
  for (const e of entries) {
    if (!e.isDirectory()) continue;
    try {
      const raw: unknown = JSON.parse(fs.readFileSync(path.join(root, e.name, "control.json"), "utf8"));
      const manifest = parseManifest(raw);
      if (manifest) out.push({ id: e.name, manifest });
    } catch {
      // no control.json, or half-written: not an instance right now
    }
  }
  out.sort((a, b) => a.id.localeCompare(b.id));
  return out;
}
