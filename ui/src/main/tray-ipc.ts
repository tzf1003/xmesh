import { ipcMain, type IpcMainInvokeEvent } from "electron";
import { TRAY_CHANNELS, parseTrayCommand } from "../shared/tray";
import type { TrayService } from "./tray-service";
import type { TrayWindow } from "./tray-window";

export function trustedTraySender(frame: { url: string; parent: unknown } | null, registered: boolean): boolean {
  if (!registered || !frame || frame.parent !== null) return false;
  try {
    const u = new URL(frame.url);
    return (
      u.protocol === "app:" &&
      u.hostname === "tray" &&
      !u.username &&
      !u.password &&
      !u.port &&
      u.pathname === "/index.html"
    );
  } catch {
    return false;
  }
}
export function registerTrayIpc(service: TrayService, window: TrayWindow): () => void {
  const trusted = (e: IpcMainInvokeEvent): void => {
    if (!trustedTraySender(e.senderFrame, e.sender === window.contents)) throw new Error("untrusted sender");
  };
  ipcMain.handle(TRAY_CHANNELS.state, (e) => {
    trusted(e);
    window.present();
    return service.state;
  });
  ipcMain.handle(TRAY_CHANNELS.command, (e, value: unknown) => {
    trusted(e);
    const command = parseTrayCommand(value);
    return command
      ? service.execute(command)
      : {
          ok: false,
          error: { code: "invalid_args", message: "Invalid tray request" },
        };
  });
  ipcMain.handle(TRAY_CHANNELS.hide, (e) => {
    trusted(e);
    window.hide();
    return { ok: true };
  });
  return () => {
    for (const channel of Object.values(TRAY_CHANNELS)) ipcMain.removeHandler(channel);
  };
}
