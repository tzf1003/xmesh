import { type IpcMainInvokeEvent, type WebContents, ipcMain } from "electron";
import {
  CHANNELS,
  SNAPSHOT_EVENT,
  type ActionResult,
  parseBoolean,
  parseCopyKind,
  parseExternalKey,
  parseInstanceId,
  parsePathKind,
} from "../shared/ipc";
import type { Snapshot } from "../shared/state";
import type { Actions } from "./actions";
import type { ShellController } from "./controller";
import { isPanelUrl } from "./policy";

const bad = (field: string): ActionResult => ({
  ok: false,
  error: { code: "invalid_args", message: `invalid ${field}` },
});

/** The frame must be the top frame of the panel window itself, showing the bundled panel origin. */
export function isTrustedSender(
  frame: { url: string; parent: unknown } | null,
  senderIsPanel: boolean,
): boolean {
  return senderIsPanel && frame !== null && frame.parent === null && isPanelUrl(frame.url);
}

export interface IpcDeps {
  controller: ShellController;
  actions: Actions;
  panel: () => WebContents | undefined;
}

export function registerIpc({ controller, actions, panel }: IpcDeps): () => void {
  const trusted = (e: IpcMainInvokeEvent): boolean => {
    const target = panel();
    return isTrustedSender(e.senderFrame, target !== undefined && e.sender === target);
  };

  const handle = <A extends unknown[], R>(channel: string, fn: (...args: A) => Promise<R> | R): void => {
    ipcMain.handle(channel, (e, ...args) => {
      if (!trusted(e)) throw new Error("untrusted sender");
      return fn(...(args as A));
    });
  };

  handle(CHANNELS.getSnapshot, (): Snapshot => controller.snapshot);
  handle(CHANNELS.drain, () => actions.drain());
  handle(CHANNELS.resume, () => actions.resume());
  handle(CHANNELS.selectInstance, (id: unknown) => {
    const v = parseInstanceId(id);
    return v === undefined ? bad("instance id") : actions.selectInstance(v);
  });
  handle(CHANNELS.copy, (kind: unknown) => {
    const v = parseCopyKind(kind);
    return v === undefined ? bad("copy kind") : actions.copy(v);
  });
  handle(CHANNELS.openPath, (kind: unknown) => {
    const v = parsePathKind(kind);
    return v === undefined ? bad("path kind") : actions.openPath(v);
  });
  handle(CHANNELS.setLoginItem, (enabled: unknown) => {
    const v = parseBoolean(enabled);
    return v === undefined ? bad("flag") : actions.setLoginItem(v);
  });
  handle(CHANNELS.openExternal, (key: unknown) => {
    const v = parseExternalKey(key);
    return v === undefined ? bad("link key") : actions.openExternal(v);
  });

  const push = (snap: Snapshot): void => {
    const target = panel();
    if (target && !target.isDestroyed()) target.send(SNAPSHOT_EVENT, snap);
  };
  controller.on("change", push);

  return () => {
    for (const c of Object.values(CHANNELS)) ipcMain.removeHandler(c);
    controller.off("change", push);
  };
}
