import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

export interface BinaryLookup {
  platform: NodeJS.Platform;
  env: NodeJS.ProcessEnv;
  /** Electron's `process.resourcesPath` of a packaged app; the installer may ship `bin/xmesh` there. */
  resourcesPath?: string | undefined;
  home: string;
  exists: (p: string) => boolean;
}

const isFile = (p: string): boolean => {
  try {
    return fs.statSync(p).isFile();
  } catch {
    return false;
  }
};

export function defaultLookup(): BinaryLookup {
  return {
    platform: process.platform,
    env: process.env,
    resourcesPath: (process as NodeJS.Process & { resourcesPath?: string }).resourcesPath,
    home: os.homedir(),
    exists: isFile,
  };
}

/** Directories an `xmesh` install usually ends up in that a GUI-launched process may not have on its PATH. */
export function wellKnownDirs(l: Pick<BinaryLookup, "platform" | "env" | "home">): string[] {
  const p = l.platform === "win32" ? path.win32 : path.posix;
  switch (l.platform) {
    case "win32": {
      const out: string[] = [];
      const local = l.env.LOCALAPPDATA || p.join(l.home, "AppData", "Local");
      out.push(p.join(local, "Programs", "xmesh"), p.join(local, "xmesh", "bin"));
      for (const v of [l.env.ProgramFiles, l.env.ProgramW6432]) if (v) out.push(p.join(v, "xmesh"));
      out.push(p.join(l.home, "go", "bin"));
      return [...new Set(out)];
    }
    case "darwin":
      // Finder and login items start apps with a minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin).
      return ["/opt/homebrew/bin", "/usr/local/bin", p.join(l.home, ".local", "bin"), p.join(l.home, "go", "bin")];
    default:
      return ["/usr/local/bin", "/usr/bin", p.join(l.home, ".local", "bin"), p.join(l.home, "go", "bin")];
  }
}

/**
 * Finds the `xmesh` executable the tray shells out to (`xmesh restart|stop`).
 * Order: `XMESH_BIN` (taken at its word), the binary shipped inside the app,
 * `PATH`, then the usual install directories. Falls back to the bare name so
 * the OS reports "not found" through the normal error path.
 */
export function resolveXmeshBinary(l: BinaryLookup = defaultLookup()): string {
  const override = l.env.XMESH_BIN;
  if (override) return override;
  const win = l.platform === "win32";
  const p = win ? path.win32 : path.posix;
  const exe = win ? "xmesh.exe" : "xmesh";

  if (l.resourcesPath) {
    const bundled = p.join(l.resourcesPath, "bin", exe);
    if (l.exists(bundled)) return bundled;
  }
  const pathVar = win ? (l.env.Path ?? l.env.PATH) : l.env.PATH;
  const dirs = (pathVar ?? "").split(win ? ";" : ":").filter((d) => d !== "" && p.isAbsolute(d));
  for (const d of [...dirs, ...wellKnownDirs(l)]) {
    const candidate = p.join(d, exe);
    if (l.exists(candidate)) return candidate;
  }
  return "xmesh";
}
