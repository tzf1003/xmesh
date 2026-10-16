/** The slice of Electron's `app` the login item logic needs. */
export interface LoginItemApi {
  getLoginItemSettings(options?: { path?: string; args?: string[] }): {
    openAtLogin: boolean;
    /** Windows: false when the user switched the entry off in Task Manager > Startup apps. */
    executableWillLaunchAtLogin?: boolean;
    /** macOS 13+ (`SMAppService`). */
    status?: string;
  };
  setLoginItemSettings(settings: { openAtLogin: boolean; path?: string; args?: string[] }): void;
}

export type LoginItemHint = "unsupported" | "unpackaged" | "requires-approval" | "disabled-by-user";

export interface LoginItemState {
  enabled: boolean;
  /** Why `enabled` may not match what the user asked for; absent when it does. */
  hint?: LoginItemHint;
}

export interface AutostartEnv {
  platform: NodeJS.Platform;
  /** `app.isPackaged`. An unpackaged run would register `electron.exe` / the Electron helper, not xmesh. */
  packaged: boolean;
  execPath: string;
}

/**
 * Login-time start of the UI (the kernel has its own user task / LaunchAgent).
 * Windows writes an `HKCU\...\Run` entry for the exe and honours the Task
 * Manager switch; macOS registers a login item and can be left waiting for the
 * user's approval in System Settings. Anything else, and development runs,
 * report a hint instead of touching the system.
 */
export class Autostart {
  constructor(
    private readonly api: LoginItemApi,
    private readonly env: AutostartEnv,
  ) {}

  private options(): { path?: string; args?: string[] } {
    // Windows matches the entry by exe path and arguments, so reads and writes must pass the same ones.
    return this.env.platform === "win32" ? { path: this.env.execPath, args: [] } : {};
  }

  private unsupported(): LoginItemHint | undefined {
    if (this.env.platform !== "win32" && this.env.platform !== "darwin") return "unsupported";
    if (!this.env.packaged) return "unpackaged";
    return undefined;
  }

  get(): LoginItemState {
    const hint = this.unsupported();
    if (hint) return { enabled: false, hint };
    const s = this.api.getLoginItemSettings(this.options());
    if (this.env.platform === "win32") {
      if (s.openAtLogin && s.executableWillLaunchAtLogin === false) return { enabled: false, hint: "disabled-by-user" };
      return { enabled: s.openAtLogin };
    }
    switch (s.status) {
      case "enabled":
        return { enabled: true };
      case "requires-approval":
        return { enabled: false, hint: "requires-approval" };
      case "not-registered":
      case "not-found":
        return { enabled: false };
      default:
        return { enabled: s.openAtLogin };
    }
  }

  /** Applies the request and returns the state that actually results. */
  set(enabled: boolean): LoginItemState {
    const hint = this.unsupported();
    if (hint) return { enabled: false, hint };
    this.api.setLoginItemSettings({ openAtLogin: enabled, ...this.options() });
    return this.get();
  }
}
