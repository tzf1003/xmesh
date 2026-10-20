// Handing the downloaded installer over. The app cannot replace itself while it runs, so a small detached
// script waits for the app to exit, runs the installer, and starts the new app (or the old one again when the
// install failed, leaving a note the next start reads). Everything the script needs reaches it through its
// environment or arguments, never by editing its text, so no path can change what it does.

import { type SpawnOptions, spawn as nodeSpawn } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import { randomUUID } from "node:crypto";
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
  /** Starts an independent script. On Windows, resolves after it confirms it is ready for the app to quit. */
  start(h: HandOver): Promise<void>;
}

/** The part of a child process the hand-over uses. */
export interface Spawned {
  once(event: "error" | "spawn", listener: (e: Error) => void): unknown;
  once(event: "exit", listener: (code: number | null) => void): unknown;
  unref(): void;
  kill(): boolean;
}

export type SpawnFn = (command: string, args: string[], options: SpawnOptions) => Spawned;

/**
 * Windows: a hidden PowerShell process waits on the app's process handle for at most a minute, runs the
 * NSIS installer, then starts the app. --updated keeps the kernels' logon tasks during the upgrade.
 * Paths come through the environment; the script is ASCII for Windows PowerShell's default encoding.
 */
export const WINDOWS_SCRIPT = `
$ErrorActionPreference = 'Stop'
function Fail-Update([string]$Reason, [switch]$Restart) {
  [IO.File]::WriteAllText($env:XMESH_UPD_RESULT, $Reason)
  if ($Restart -and [IO.File]::Exists($env:XMESH_UPD_APP)) {
    Start-Process -FilePath $env:XMESH_UPD_APP
  }
  exit 1
}
try {
  $oldApp = $null
  try {
    $oldApp = [Diagnostics.Process]::GetProcessById([int]$env:XMESH_UPD_PID)
  } catch [ArgumentException] {
    # The app already exited before the helper opened its process handle.
  }
  if ($null -ne $oldApp) {
    try {
      if ($env:XMESH_UPD_READY) { [IO.File]::WriteAllText($env:XMESH_UPD_READY, 'ready') }
      if (-not $oldApp.WaitForExit(60000)) { Fail-Update 'the app did not exit' }
    } finally {
      $oldApp.Dispose()
    }
  }
  elseif ($env:XMESH_UPD_READY) { [IO.File]::WriteAllText($env:XMESH_UPD_READY, 'ready') }
} catch {
  Fail-Update ('could not wait for the app: ' + $_.Exception.Message)
}
try {
  if ($env:XMESH_UPD_READY -and [IO.File]::ReadAllText($env:XMESH_UPD_READY) -ne 'install') {
    Fail-Update 'the app did not authorize installation'
  }
} catch {
  Fail-Update ('could not confirm installation: ' + $_.Exception.Message)
}
# Allow Electron's helper processes to release the installed files.
Start-Sleep -Seconds 2
try {
  $installer = Start-Process -FilePath $env:XMESH_UPD_INSTALLER -ArgumentList '/S', '--updated' -PassThru
  $installer.WaitForExit()
  $code = $installer.ExitCode
  $installer.Dispose()
  if ($code -ne 0) { Fail-Update ('installer exit code ' + $code) -Restart }
} catch {
  Fail-Update ('could not run the installer: ' + $_.Exception.Message) -Restart
}
try {
  Start-Process -FilePath $env:XMESH_UPD_APP
} catch {
  Fail-Update ('could not start the app: ' + $_.Exception.Message)
}
`;

// A detached console process makes Windows PowerShell exit before reading its script.
// Start-Process creates a separate hidden console instead, which survives Electron's exit.
export const WINDOWS_LAUNCH = `$ErrorActionPreference = 'Stop'; Start-Process -FilePath ($env:SystemRoot + '\\System32\\WindowsPowerShell\\v1.0\\powershell.exe') -WindowStyle Hidden -ArgumentList ('-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' + $env:XMESH_UPD_SCRIPT + '"')`;

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

function launch(spawn: SpawnFn, command: string, args: string[], options: SpawnOptions, readyFile?: string): Promise<void> {
  return new Promise((resolve, reject) => {
    let child: Spawned;
    let poll: NodeJS.Timeout | undefined;
    let timer: NodeJS.Timeout | undefined;
    let settled = false;
    const finish = (error?: Error): void => {
      if (settled) return;
      settled = true;
      clearInterval(poll);
      clearTimeout(timer);
      if (error) {
        child?.kill();
        try {
          if (readyFile) fs.rmSync(readyFile, { force: true });
        } catch {
          // Without the 'install' acknowledgement the helper cannot install, even if cleanup fails.
        }
        reject(new ApplyError("spawn_failed", error.message));
      } else {
        child.unref();
        resolve();
      }
    };
    try {
      child = spawn(command, args, { detached: true, stdio: "ignore", ...options });
    } catch (e) {
      reject(new ApplyError("spawn_failed", e instanceof Error ? e.message : String(e)));
      return;
    }
    child.once("error", (e) => finish(e));
    child.once("spawn", () => {
      if (!readyFile) return finish();
      poll = setInterval(() => {
        try {
          if (!fs.existsSync(readyFile) || fs.readFileSync(readyFile, "utf8") !== "ready") return;
          fs.writeFileSync(readyFile, "install");
          finish();
        } catch (e) { finish(e instanceof Error ? e : new Error(String(e))); }
      }, 50);
      timer = setTimeout(() => finish(new Error("the Windows update helper did not become ready")), 15_000);
    });
    if (readyFile) child.once("exit", (code) => {
      if (code !== 0) finish(new Error(`the Windows update launcher exited (${code})`));
    });
  });
}

export function windowsApplier(spawn: SpawnFn = nodeSpawn): Applier {
  return {
    blocked: () => "",
    async start(h) {
      const script = path.join(h.dir, "apply-update.ps1");
      const ready = path.join(h.dir, `apply-update-${randomUUID()}.ready`);
      fs.writeFileSync(script, WINDOWS_SCRIPT);
      await launch(spawn, "powershell.exe", ["-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-Command", WINDOWS_LAUNCH], {
        detached: false,
        windowsHide: true,
        env: {
          ...process.env,
          XMESH_UPD_PID: String(h.pid),
          XMESH_UPD_INSTALLER: h.installer,
          XMESH_UPD_APP: h.execPath,
          XMESH_UPD_RESULT: h.resultFile,
          XMESH_UPD_SCRIPT: script,
          XMESH_UPD_READY: ready,
        },
      }, ready);
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
