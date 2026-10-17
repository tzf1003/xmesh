import { app, clipboard, dialog, shell } from "electron";
import { runXmesh } from "./cli";
import type { Platform } from "./actions";

export function electronPlatform(showPanel: () => void, notifyError: (message: string) => void): Platform {
  return {
    writeClipboard: (text) => clipboard.writeText(text),
    openPath: (p) => shell.openPath(p),
    openExternal: (url) => shell.openExternal(url),
    confirm: async ({ title, detail, confirmLabel, cancelLabel }) => {
      const r = await dialog.showMessageBox({
        type: "warning",
        title,
        message: title,
        detail,
        buttons: [cancelLabel, confirmLabel],
        defaultId: 0,
        cancelId: 0,
        noLink: true,
      });
      return r.response === 1;
    },
    showPanel,
    quit: () => app.quit(),
    runCli: (command, node) => runXmesh(command, node),
    notifyError,
    pickPath: async (kind) => {
      if (kind === "authkeySave") {
        // A file to be created: the pre-auth key is written there (0600) by the CLI, never shown or kept here.
        const r = await dialog.showSaveDialog({ defaultPath: "xmesh-authkey.txt", properties: ["createDirectory", "showOverwriteConfirmation"] });
        return r.canceled ? undefined : r.filePath;
      }
      const r = await dialog.showOpenDialog({
        properties: kind === "workspace" ? ["openDirectory", "createDirectory"] : ["openFile"],
      });
      return r.canceled ? undefined : r.filePaths[0];
    },
  };
}

export interface RecordedCall {
  op: string;
  arg?: unknown;
}

/** Test double used when XMESH_UI_TEST=1: records side effects instead of performing them. */
export function recordingPlatform(
  showPanel: () => void,
  calls: RecordedCall[],
  answers: { confirm: boolean; cliFails: boolean; picked?: string },
): Platform {
  return {
    writeClipboard: (text) => void calls.push({ op: "clipboard", arg: text }),
    openPath: async (p) => {
      calls.push({ op: "openPath", arg: p });
      return "";
    },
    openExternal: async (url) => void calls.push({ op: "openExternal", arg: url }),
    confirm: async (o) => {
      calls.push({ op: "confirm", arg: o.title });
      return answers.confirm;
    },
    showPanel,
    quit: () => void calls.push({ op: "quit" }),
    runCli: async (command) => {
      calls.push({ op: "cli", arg: command });
      if (answers.cliFails) throw new Error("simulated cli failure");
    },
    notifyError: (m) => void calls.push({ op: "error", arg: m }),
    pickPath: async (kind) => {
      calls.push({ op: "pickPath", arg: kind });
      return answers.picked;
    },
  };
}
