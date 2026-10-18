// Handing the downloaded installer over. The app cannot replace itself while it runs, so a small detached
// script waits for the app to exit, runs the installer, and starts the new app (or the old one again when the
// install failed, leaving a note the next start reads). Everything the script needs reaches it through its
// environment or arguments, never by editing its text, so no path can change what it does.

import { type SpawnOptions, spawn as nodeSpawn } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import type { UpdateBlocked } from "../../shared/update";

export interface HandOver {
  installer: string;
  /** The running app's executable (`process.execPath`). */
  execPath: string;
  pid: number;
  /** A directory the update owns (scripts and the result note live here). */
  dir: string;
  /** The script writes a one-line reason here when it could not install. */
  resultFile: string;
}

export class ApplyError extends Error {
  constructor(
    readonly code: "not_writable" | "spawn_failed",
    message: string,
  ) {
    super(message);
    this.name = "ApplyError";
  }
}

export interface Applier {
  /** Whether this install can be replaced by the hand-over below. */
  blocked(): UpdateBlocked;
  /** Starts the detached script. Resolves once it runs; the caller quits the app right after. */
  start(h: HandOver): Promise<void>;
}

/** The part of a child process the hand-over uses. */
export interface Spawned {
  once(event: "error" | "spawn", listener: (e: Error) => void): unknown;
  unref(): void;
}

export type SpawnFn = (command: string, args: string[], options: SpawnOptions) => Spawned;

/**
 * Windows: `cmd` runs this file. Waits up to a minute for the app's process to end, runs the NSIS installer
 * silently (it reuses the folder of the existing install), then starts the app. `--updated` is what tells the
 * installer, and the old uninstaller it runs, that this is an upgrade: build/installer.nsh then keeps the logon
 * tasks of the kernels, which the update restarts afterwards. cmd runs a batch file's
 * program to its end even for a GUI installer, so `%errorlevel%` is the installer's exit code. ASCII only:
 * cmd reads the file in the console code page.
 */
export const WINDOWS_SCRIPT = [
  "@echo off",
  "setlocal",
  "set /a tries=0",
  ":wait",
  'tasklist /NH /FO CSV /FI "PID eq %XMESH_UPD_PID%" 2>nul | find """%XMESH_UPD_PID%""" >nul',
  "if errorlevel 1 goto gone",
  "set /a tries+=1",
  "if %tries% geq 60 goto stuck",
  "ping -n 2 127.0.0.1 >nul",
  "goto wait",
  ":gone",
  "rem The helper processes of the app let go of its files a moment after the main process ends.",
  "ping -n 3 127.0.0.1 >nul",
  '"%XMESH_UPD_INSTALLER%" /S --updated',
  "set code=%errorlevel%",
  'if not "%code%"=="0" goto failed',
  'start "" "%XMESH_UPD_APP%"',
  "exit /b 0",
  ":failed",
  '>"%XMESH_UPD_RESULT%" echo installer exit code %code%',
  'if exist "%XMESH_UPD_APP%" start "" "%XMESH_UPD_APP%"',
  "exit /b 1",
  ":stuck",
  '>"%XMESH_UPD_RESULT%" echo the app did not exit',
  "exit /b 1",
  "",
].join("\r\n");

/**
 * macOS: `/bin/sh script <pid> <dmg> <app bundle> <result file>`. Mounts the disk image read-only, swaps the
 * bundle (the old one is kept until the new one is in place and put back when the copy fails), clears the
 * quarantine flag and opens the app. The app is unsigned, so there is no signature to keep intact beyond the
 * ad-hoc one `ditto` carries over with the bundle.
 */
export const MAC_SCRIPT = `#!/bin/sh
pid="$1"; dmg="$2"; app="$3"; result="$4"
fail() {
  echo "$1" > "$result"
  [ -n "$mnt" ] && hdiutil detach "$mnt" -quiet -force >/dev/null 2>&1
  [ -n "$mnt" ] && rmdir "$mnt" >/dev/null 2>&1
  [ -d "$app" ] && open "$app"
  exit 1
}
i=0
while kill -0 "$pid" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -ge 60 ] && fail "the app did not exit"
  sleep 1
done
mnt=$(mktemp -d "\${TMPDIR:-/tmp}/xmesh-update.XXXXXX") || fail "no temporary folder"
hdiutil attach -nobrowse -readonly -noverify -mountpoint "$mnt" "$dmg" >/dev/null 2>&1 || fail "the disk image could not be opened"
src=""
for a in "$mnt"/*.app; do [ -d "$a" ] && src="$a"; done
[ -n "$src" ] || fail "the disk image has no app"
old="$app.update-old"
rm -rf "$old"
mv "$app" "$old" || fail "the old app could not be moved"
if ! ditto "$src" "$app"; then
  rm -rf "$app"
  mv "$old" "$app"
  fail "the new app could not be copied"
fi
rm -rf "$old"
xattr -dr com.apple.quarantine "$app" >/dev/null 2>&1
hdiutil detach "$mnt" -quiet >/dev/null 2>&1 || hdiutil detach "$mnt" -quiet -force >/dev/null 2>&1
rmdir "$mnt" >/dev/null 2>&1
open "$app"
`;

function launch(spawn: SpawnFn, command: string, args: string[], options: SpawnOptions): Promise<void> {
  return new Promise((resolve, reject) => {
    let child: Spawned;
    try {
      child = spawn(command, args, { ...options, detached: true, stdio: "ignore" });
    } catch (e) {
      reject(new ApplyError("spawn_failed", e instanceof Error ? e.message : String(e)));
      return;
    }
    child.once("error", (e) => reject(new ApplyError("spawn_failed", e.message)));
    child.once("spawn", () => {
      child.unref();
      resolve();
    });
  });
}

export function windowsApplier(spawn: SpawnFn = nodeSpawn): Applier {
  return {
    blocked: () => "",
    async start(h) {
      const script = path.join(h.dir, "apply-update.cmd");
      fs.writeFileSync(script, WINDOWS_SCRIPT);
      await launch(spawn, "cmd.exe", ["/d", "/s", "/c", `"${script}"`], {
        windowsHide: true,
        windowsVerbatimArguments: true,
        env: {
          ...process.env,
          XMESH_UPD_PID: String(h.pid),
          XMESH_UPD_INSTALLER: h.installer,
          XMESH_UPD_APP: h.execPath,
          XMESH_UPD_RESULT: h.resultFile,
        },
      });
    },
  };
}

/** `/Applications/xmesh.app/Contents/MacOS/xmesh` -> `/Applications/xmesh.app`; undefined when not inside a bundle. */
export function macBundle(execPath: string): string | undefined {
  const dir = path.posix.dirname(path.posix.dirname(path.posix.dirname(execPath)));
  return path.posix.basename(dir).endsWith(".app") && path.posix.basename(path.posix.dirname(execPath)) === "MacOS" ? dir : undefined;
}

export function macApplier(execPath: string, spawn: SpawnFn = nodeSpawn, writable: (dir: string) => boolean = canWrite): Applier {
  const bundle = macBundle(execPath);
  return {
    blocked: () => {
      // Gatekeeper's AppTranslocation copy and a disk image are read-only: replacing them changes nothing.
      if (bundle === undefined || bundle.includes("/AppTranslocation/") || bundle.startsWith("/Volumes/")) return "translocated";
      return "";
    },
    async start(h) {
      if (bundle === undefined) throw new ApplyError("spawn_failed", "not running from an app bundle");
      if (!writable(path.posix.dirname(bundle)) || !writable(bundle)) {
        throw new ApplyError("not_writable", `${path.posix.dirname(bundle)} is not writable`);
      }
      const script = path.join(h.dir, "apply-update.sh");
      fs.writeFileSync(script, MAC_SCRIPT, { mode: 0o700 });
      await launch(spawn, "/bin/sh", [script, String(h.pid), h.installer, bundle, h.resultFile], {});
    },
  };
}

function canWrite(dir: string): boolean {
  try {
    fs.accessSync(dir, fs.constants.W_OK);
    return true;
  } catch {
    return false;
  }
}
