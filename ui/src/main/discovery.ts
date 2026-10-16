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

/**
 * Every directory searched for `*\/control.json`, most specific first.
 * `XMESH_UI_STATE_ROOT` replaces the list (tests, fake kernel). On Windows a
 * kernel installed with `install --system` runs as SYSTEM in Session 0 and
 * its own `UserConfigDir` is not readable by the signed-in user, so it has to
 * be given a `--state-dir` somewhere shared; `%ProgramData%\xmesh` is where the
 * UI looks for those (contract: the manifest's `endpoint` is authoritative,
 * only the discovery of `control.json` is widened).
 */
export function stateRoots(
  env: NodeJS.ProcessEnv = process.env,
  platform: NodeJS.Platform = process.platform,
  home: string = os.homedir(),
): string[] {
  if (env.XMESH_UI_STATE_ROOT) return [env.XMESH_UI_STATE_ROOT];
  const roots = [path.join(userConfigDir(platform, env, home), "xmesh")];
  if (platform === "win32") {
    roots.push(path.win32.join(env.ProgramData || "C:\\ProgramData", "xmesh"));
  }
  return roots;
}

/**
 * Enumerates `<root>/*\/control.json` over one or more roots. Unreadable or
 * malformed manifests are skipped. A node name found under several roots is
 * listed once, from the first root.
 */
export function discoverInstances(roots: string | string[] = stateRoot()): Instance[] {
  const out: Instance[] = [];
  const seen = new Set<string>();
  for (const root of Array.isArray(roots) ? roots : [roots]) {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(root, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const e of entries) {
      if (!e.isDirectory() || seen.has(e.name)) continue;
      try {
        const raw: unknown = JSON.parse(fs.readFileSync(path.join(root, e.name, "control.json"), "utf8"));
        const manifest = parseManifest(raw);
        if (manifest) {
          out.push({ id: e.name, manifest });
          seen.add(e.name);
        }
      } catch {
        // no control.json, or half-written: not an instance right now
      }
    }
  }
  out.sort((a, b) => a.id.localeCompare(b.id));
  return out;
}
