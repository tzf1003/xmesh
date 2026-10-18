import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { type ControlEndpointFile, parseManifest } from "../shared/control";
import { parseNodeName } from "../shared/ipc";

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

/**
 * Node directories that have a config file, running or not. A node whose kernel was stopped has no
 * `control.json` any more, but its config and service are still there to start it again. Only names
 * the CLI accepts with `--name` are listed: anything else could not be started by name.
 */
export function discoverConfigured(roots: string | string[] = stateRoot()): string[] {
  const seen = new Set<string>();
  for (const root of Array.isArray(roots) ? roots : [roots]) {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(root, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const e of entries) {
      if (parseNodeName(e.name) === undefined || !e.isDirectory()) continue;
      if (fs.existsSync(path.join(root, e.name, "xmesh.yaml"))) seen.add(e.name);
    }
  }
  return [...seen].sort();
}

/** The configured nodes whose kernel is not running. A stale manifest's process is gone too. */
export function stoppedNodes(configured: string[], found: Instance[]): string[] {
  const running = new Set(found.map((i) => i.id));
  return configured.filter((id) => !running.has(id));
}

/** A missing PID proves exit; access errors and invalid PIDs leave the process state unknown. */
export function kernelGone(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return false;
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === "ESRCH";
  }
}

/**
 * Removes `<root>/<id>/control.json` for an instance whose kernel is gone. It re-reads the manifest and
 * proves the process is absent again, so a kernel that started between discovery and this call keeps its
 * file; the node name must be one the CLI accepts, so no path outside the roots can be named.
 */
export function removeStaleControl(
  roots: string | string[],
  id: string,
  gone: (pid: number) => boolean = kernelGone,
): boolean {
  if (parseNodeName(id) === undefined) return false;
  for (const root of Array.isArray(roots) ? roots : [roots]) {
    const file = path.join(root, id, "control.json");
    let manifest: ReturnType<typeof parseManifest>;
    let raw: string;
    try {
      raw = fs.readFileSync(file, "utf8");
      manifest = parseManifest(JSON.parse(raw));
    } catch {
      continue;
    }
    if (!manifest) continue;
    // Match discovery precedence: a same-name node in a later root is a separate installation.
    if (!gone(manifest.pid)) return false;
    try {
      // A restarting kernel may publish a replacement while the process is being checked.
      if (fs.readFileSync(file, "utf8") !== raw) return false;
      fs.unlinkSync(file);
      return true;
    } catch {
      return false;
    }
  }
  return false;
}
