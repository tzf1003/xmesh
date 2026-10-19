import { type IpcMainInvokeEvent, type WebContents, ipcMain } from "electron";
import { parseTransfer } from "../shared/transfers";
import type { TransferService } from "./transfers";
import { parsePlayground } from "../shared/playground";
import type { PlaygroundService } from "./playground";
import { parseCiRequest } from "../shared/ci";
import {
  CALLS_EVENT,
  CHANNELS,
  SNAPSHOT_EVENT,
  type ActionResult,
  parseAuditQuery,
  parseBackendEnv,
  parseBoolean,
  parseClientId,
  parseConfigPatch,
  parseConfigSave,
  parseDenyFileCreate,
  parseDenyFileSave,
  parseCopyKind,
  parseGatewayCopy,
  parseGatewayUpdate,
  parseDrainMode,
  parseExternalKey,
  parseInstanceId,
  parseNodeName,
  parsePairConnect,
  parsePairImport,
  parsePairPassword,
  parsePassword,
  parsePatCreate,
  parsePathKind,
  parsePickKind,
} from "../shared/ipc";
import type { CallEventData } from "../shared/control";
import { parseDisplay } from "../shared/display";
import { parseUpdatePrefs } from "../shared/update";
import type { PanelService } from "./panel-service";
import type { SetupPreview, SetupReport } from "../shared/ipc";
import type { DataResult } from "../shared/ipc";
import type { Snapshot } from "../shared/state";
import type { Actions } from "./actions";
import type { UpdateActions } from "./update/service";
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
  playground: PlaygroundService;
  transfers: TransferService;
  controller: ShellController;
  actions: Actions;
  service: PanelService;
  /** Validates first-run setup and returns what it would write; `params` is whatever the renderer sent. */
  previewSetup: (params: unknown) => DataResult<SetupPreview>;
  /** Validates and runs first-run setup; `params` is whatever the renderer sent. */
  setup: (params: unknown) => Promise<DataResult<SetupReport>>;
  /** Validates and runs one tailnet-administration request; `request` is whatever the renderer sent. */
  mesh: (request: unknown) => Promise<DataResult<unknown>>;
  panel: () => WebContents | undefined;
  update: UpdateActions;
}

export function registerIpc({ transfers, playground, controller, actions, service, previewSetup, setup, mesh, panel, update }: IpcDeps): () => void {
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

  handle(CHANNELS.transfers, (request: unknown) => {
    const req = parseTransfer(request);
    return req ? transfers.request(req) : bad("transfer request");
  });
  handle(CHANNELS.playground, (request: unknown) => {
    const req = parsePlayground(request);
    return req ? playground.request(req) : bad("playground request");
  });
  handle(CHANNELS.getSnapshot, (): Snapshot => controller.snapshot);
  handle(CHANNELS.drain, (mode: unknown) => {
    if (mode === undefined) return actions.drain();
    const v = parseDrainMode(mode);
    return v === undefined ? bad("drain mode") : actions.drain(v);
  });
  handle(CHANNELS.resume, () => actions.resume());
  handle(CHANNELS.startKernel, (node: unknown) => {
    const v = parseNodeName(node);
    return v === undefined ? bad("node name") : actions.startKernel(v);
  });
  handle(CHANNELS.restartKernel, () => actions.dispatch({ type: "restart-kernel" }));
  handle(CHANNELS.restartUnresponsive, (node: unknown) => {
    const v = parseNodeName(node);
    return v === undefined ? bad("node name") : actions.restartUnresponsive(v);
  });
  handle(CHANNELS.cleanupStale, (node: unknown) => {
    const v = parseNodeName(node);
    return v === undefined ? bad("node name") : actions.cleanupStale(v);
  });
  handle(CHANNELS.selectInstance, (id: unknown) => {
    const v = parseInstanceId(id);
    return v === undefined ? bad("instance id") : actions.selectInstance(v);
  });
  handle(CHANNELS.copy, (kind: unknown) => {
    const v = parseCopyKind(kind);
    return v === undefined ? bad("copy kind") : actions.copy(v);
  });
  handle(CHANNELS.copyGateway, (req: unknown) => {
    const v = parseGatewayCopy(req);
    return v === undefined ? bad("gateway copy request") : actions.copyGateway(v.kind, v.url);
  });
  handle(CHANNELS.openPath, (kind: unknown) => {
    const v = parsePathKind(kind);
    return v === undefined ? bad("path kind") : actions.openPath(v);
  });
  handle(CHANNELS.setLoginItem, (enabled: unknown) => {
    const v = parseBoolean(enabled);
    return v === undefined ? bad("flag") : actions.setLoginItem(v);
  });
  handle(CHANNELS.setDisplay, (prefs: unknown) => {
    const v = parseDisplay(prefs);
    return v === undefined ? bad("display preferences") : actions.setDisplay(v);
  });
  handle(CHANNELS.openExternal, (key: unknown) => {
    const v = parseExternalKey(key);
    return v === undefined ? bad("link key") : actions.openExternal(v);
  });

  handle(CHANNELS.checkUpdate, () => update.check());
  handle(CHANNELS.installUpdate, () => update.install());
  handle(CHANNELS.cancelUpdate, () => update.cancel());
  handle(CHANNELS.setUpdatePrefs, (prefs: unknown) => {
    const v = parseUpdatePrefs(prefs);
    return v === undefined ? bad("update preferences") : update.setPrefs(v);
  });

  handle(CHANNELS.getSessions, () => service.getSessions());
  handle(CHANNELS.getConfig, () => service.getConfig());
  handle(CHANNELS.putConfig, (req: unknown) => {
    const v = parseConfigSave(req);
    return v === undefined ? bad("config request") : service.putConfig(v);
  });
  handle(CHANNELS.patchConfig, (req: unknown) => {
    const v = parseConfigPatch(req);
    return v === undefined ? bad("config edit request") : service.patchConfig(v);
  });
  handle(CHANNELS.patchBackendEnv, (req: unknown) => {
    const v = parseBackendEnv(req);
    return v === undefined ? bad("backend env request") : service.patchBackendEnv(v);
  });
  handle(CHANNELS.clearBackendEnv, (name: unknown) => {
    const v = parseNodeName(name);
    return v === undefined ? bad("backend name") : service.clearBackendEnv(v);
  });
  handle(CHANNELS.readDenyFile, () => service.readDenyFile());
  handle(CHANNELS.writeDenyFile, (req: unknown) => {
    const v = parseDenyFileSave(req);
    return v === undefined ? bad("deny file request") : service.writeDenyFile(v);
  });
  handle(CHANNELS.createDenyFile, (req: unknown) => {
    const v = parseDenyFileCreate(req);
    return v === undefined ? bad("deny file request") : service.createDenyFile(v);
  });
  handle(CHANNELS.reloadConfig, () => service.reloadConfig());
  handle(CHANNELS.listPats, () => service.listPats());
  handle(CHANNELS.createPat, (args: unknown) => {
    const v = parsePatCreate(args);
    return v === undefined ? bad("PAT request") : service.createPat(v);
  });
  handle(CHANNELS.rotatePat, (name: unknown) => {
    const v = parseNodeName(name);
    return v === undefined ? bad("PAT name") : service.rotatePat(v);
  });
  handle(CHANNELS.rotateGatewayPat, () => service.rotateGatewayPat());
  handle(CHANNELS.updateGateway, (req: unknown) => {
    const v = parseGatewayUpdate(req);
    return v === undefined ? bad("gateway request") : service.updateGateway(v);
  });
  handle(CHANNELS.deletePat, (name: unknown) => {
    const v = parseNodeName(name);
    return v === undefined ? bad("PAT name") : service.deletePat(v);
  });
  handle(CHANNELS.listIngressClients, () => service.listIngressClients());
  handle(CHANNELS.revokeIngressClient, (id: unknown) => {
    const v = parseClientId(id);
    return v === undefined ? bad("client id") : service.revokeIngressClient(v);
  });
  handle(CHANNELS.rotateIngressPassword, (password: unknown) => {
    const v = parsePassword(password);
    return v === undefined ? bad("password") : service.rotateIngressPassword(v);
  });
  handle(CHANNELS.exportPair, () => service.exportPair());
  handle(CHANNELS.importPair, (args: unknown) => {
    const v = parsePairImport(args);
    return v === undefined ? bad("pair request") : service.importPair(v);
  });
  handle(CHANNELS.getPairPassword, () => service.getPairPassword());
  handle(CHANNELS.setPairPassword, (password: unknown) => {
    const v = parsePairPassword(password);
    return v === undefined ? bad("pair password") : service.setPairPassword(v);
  });
  handle(CHANNELS.revokeDevice, (id: unknown) => typeof id === "string" && /^dev-[0-9a-f]{64}$/.test(id) ? service.revokeDevice(id) : bad("device id"));
  handle(CHANNELS.clearPairPassword, () => service.clearPairPassword());
  handle(CHANNELS.discoverPairs, () => service.discoverPairs());
  handle(CHANNELS.connectPair, (args: unknown) => {
    const v = parsePairConnect(args);
    return v === undefined ? bad("pair connect request") : service.connectPair(v);
  });
  handle(CHANNELS.queryAudit, (query: unknown) => {
    const v = parseAuditQuery(query);
    return v === undefined ? bad("audit query") : service.queryAudit(v);
  });
  handle(CHANNELS.getRecentCalls, (): CallEventData[] => service.recentCalls());
  handle(CHANNELS.previewSetup, (params: unknown) => previewSetup(params));
  handle(CHANNELS.runSetup, (params: unknown) => setup(params));
  handle(CHANNELS.pickPath, (kind: unknown) => {
    const v = parsePickKind(kind);
    return v === undefined ? bad("path kind") : actions.pickPath(v);
  });

  handle(CHANNELS.mesh, (request: unknown) => mesh(request));
  handle(CHANNELS.ci, (request: unknown) => {
    const v = parseCiRequest(request);
    return v === undefined ? bad("ci request") : service.ci(v);
  });

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
