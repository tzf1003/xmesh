import {execFileSync} from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { type ControlEndpointFile, parseManifest } from "../shared/control";
import { parseNodeName } from "../shared/ipc";

export interface Instance {
  /** Directory name under `<UserConfigDir>/xmesh`. */
  id: string;
  manifest: ControlEndpointFile;
  /** System service state is managed by an administrator. */
  protected?: boolean;
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

/** Windows system service state has read-only discovery grants for the signed-in user. */
export function protectedStateRoots(): string[] {
  return process.platform === "win32" ? stateRoots().slice(1) : [];
}

/**
 * Enumerates `<root>/*\/control.json` over one or more roots. Unreadable or
 * malformed manifests are skipped. A node name found under several roots is
 * listed once, from the first root.
 */
export function discoverInstances(roots: string | string[] = stateRoot(), protectedRoots: readonly string[] = protectedStateRoots()): Instance[] {
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
          out.push({ id: e.name, manifest, protected: protectedRoots.includes(root) });
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
export function kernelGone(pid: number, startedAt?: string, birth: (pid:number)=>number|undefined = processBirth): boolean {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    const published=startedAt ? Date.parse(startedAt) : NaN;
    const created=Number.isFinite(published)?birth(pid):undefined;
    return created!==undefined && created>published+2000;
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === "ESRCH";
  }
}

/** Process creation time proves PID reuse; lookup failures keep identity unknown. */
function processBirth(pid:number): number|undefined {
  try {
    const output=process.platform==="win32"
      ? execFileSync("powershell.exe",["-NoProfile","-NonInteractive","-Command",`(Get-Process -Id ${pid} -ErrorAction Stop).StartTime.ToUniversalTime().ToString('o')`],{encoding:"utf8",timeout:2000,windowsHide:true})
      : execFileSync("ps",["-p",String(pid),"-o","lstart="],{encoding:"utf8",timeout:2000,env:{...process.env,LC_ALL:"C"}});
    const result=Date.parse(output.trim());
    return Number.isFinite(result)?result:undefined;
  }catch{return undefined;}
}

/**
 * Captures the directory entry under a private name before checking and deleting it. New kernel
 * publications at control.json are independent of the captured file. A captured replacement is restored
 * with an exclusive hard link, so restoration also leaves any later publication in place.
 */
export function removeStaleControl(
  roots: string | string[],
  id: string,
  gone: (pid: number) => boolean = kernelGone,
  protectedRoots: readonly string[] = protectedStateRoots(),
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
    // The first root determines the selected node; later roots can contain other installations.
    if (protectedRoots.includes(root) || !gone(manifest.pid)) return false;
    const pending = fs.mkdtempSync(path.join(root, id, ".control-cleanup-"));
    const captured = path.join(pending, "control.json");
    let moved = false;
    let removed = false;
    let retain = false;
    try {
      // Verify hard-link support before moving a directory entry that might need restoration.
      fs.linkSync(file, path.join(pending, "backup.json"));
      fs.renameSync(file, captured);
      moved = true;
      if (fs.readFileSync(captured, "utf8") !== raw || !gone(manifest.pid)) return false;
      fs.unlinkSync(captured);
      removed = true;
      return true;
    } finally {
      if (moved && !removed) {
        try {
          fs.linkSync(captured, file);
        } catch (e) {
          if ((e as NodeJS.ErrnoException).code !== "EEXIST") {
            retain = true;
            throw new Error(`无法恢复内核状态文件，请从 ${captured} 恢复`, { cause: e });
          }
        }
      }
      if (!retain) fs.rmSync(pending, { recursive: true, force: true });
    }
  }
  return false;
}
