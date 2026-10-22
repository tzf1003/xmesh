import { type IpcMainInvokeEvent, type WebContents, ipcMain } from "electron";
import { CALLS_EVENT, CHANNELS, SNAPSHOT_EVENT } from "../shared/ipc";
import type { CallEventData } from "../shared/control";
import type { Snapshot } from "../shared/state";
import { createHandlers, type HandlerDeps } from "./handlers";
import { isPanelUrl } from "./policy";

/** The frame must be the top frame of the panel window itself, showing the bundled panel origin. */
export function isTrustedSender(
  frame: { url: string; parent: unknown } | null,
  senderIsPanel: boolean,
): boolean {
  return senderIsPanel && frame !== null && frame.parent === null && isPanelUrl(frame.url);
}

export interface IpcDeps extends HandlerDeps {
  panel: () => WebContents | undefined;
}

export function registerIpc(deps: IpcDeps): () => void {
  const { controller, panel } = deps;
  const trusted = (e: IpcMainInvokeEvent): boolean => {
    const target = panel();
    return isTrustedSender(e.senderFrame, target !== undefined && e.sender === target);
  };

  for (const [channel, fn] of createHandlers(deps)) {
    ipcMain.handle(channel, (e, ...args) => {
      if (!trusted(e)) throw new Error("untrusted sender");
      return fn(...args);
    });
  }

  const push = (snap: Snapshot): void => {
    const target = panel();
    if (target && !target.isDestroyed()) target.send(SNAPSHOT_EVENT, snap);
  };
  controller.on("change", push);

  // Bursts of calls are coalesced so a busy kernel cannot flood the page.
  let pending: CallEventData[] | undefined;
  let timer: NodeJS.Timeout | undefined;
  const pushCalls = (calls: CallEventData[]): void => {
    pending = calls;
    if (timer) return;
    timer = setTimeout(() => {
      timer = undefined;
      const target = panel();
      if (pending && target && !target.isDestroyed()) target.send(CALLS_EVENT, pending);
      pending = undefined;
    }, 250);
  };
  controller.on("calls", pushCalls);

  return () => {
    for (const c of Object.values(CHANNELS)) ipcMain.removeHandler(c);
    controller.off("change", push);
    controller.off("calls", pushCalls);
    clearTimeout(timer);
  };
}
