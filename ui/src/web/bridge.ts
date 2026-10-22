// What the desktop shell does with native APIs (clipboard, dialogs, the browser),
// the web panel does in the page that made the call. Each call runs inside a
// request context: effects are returned with the reply, and questions go to the
// calling session's event stream and wait for its answer.

import { AsyncLocalStorage } from "node:async_hooks";
import { randomBytes } from "node:crypto";
import type { Platform } from "../main/actions";
import { runXmesh } from "../main/cli";
import { removeStaleControl, stateRoots } from "../main/discovery";
import type { PickKind } from "../shared/ipc";
import type { WebAnswer, WebAsk, WebEffect } from "../shared/web";

export interface CallContext {
  session: string;
  effects: WebEffect[];
}

type AskBody = WebAsk extends infer A ? A extends WebAsk ? Omit<A, "id"> : never : never;

interface Pending {
  session: string;
  kind: WebAsk["kind"];
  resolve: (value: boolean | string | null) => void;
  timer: NodeJS.Timeout;
}

export class WebBridge {
  private readonly context = new AsyncLocalStorage<CallContext>();
  private readonly pending = new Map<string, Pending>();

  /**
   * @param send delivers an ask to the session's open event streams; false when it has none.
   * @param askTimeoutMs an unanswered question counts as cancelled after this long.
   */
  constructor(private readonly send: (session: string, ask: WebAsk) => boolean, private readonly askTimeoutMs = 5 * 60_000) {}

  /** Runs one call for `session`; the effects are those the call produced up to its result. */
  async run<T>(session: string, fn: () => T | Promise<T>): Promise<{ value: T; effects: WebEffect[] }> {
    const ctx: CallContext = { session, effects: [] };
    const value = await this.context.run(ctx, fn);
    return { value, effects: ctx.effects };
  }

  get current(): CallContext | undefined {
    return this.context.getStore();
  }

  effect(e: WebEffect): void {
    this.current?.effects.push(e);
  }

  /** Asks the calling session; resolves to null (or false for confirm) when it cannot or does not answer. */
  ask(body: AskBody): Promise<boolean | string | null> {
    const ctx = this.current;
    const cancelled = body.kind === "confirm" ? false : null;
    if (!ctx) return Promise.resolve(cancelled);
    const id = randomBytes(12).toString("base64url");
    return new Promise((resolve) => {
      const timer = setTimeout(() => this.settle(id, cancelled), this.askTimeoutMs);
      this.pending.set(id, { session: ctx.session, kind: body.kind, resolve, timer });
      if (!this.send(ctx.session, { ...body, id } as WebAsk)) this.settle(id, cancelled);
    });
  }

  /** An answer counts only from the session that was asked, and only with the type the question expects. */
  answer(session: string, a: WebAnswer): boolean {
    const p = this.pending.get(a.id);
    if (!p || p.session !== session) return false;
    if (p.kind === "confirm" && typeof a.value !== "boolean") return false;
    if (p.kind === "path" && a.value !== null && (typeof a.value !== "string" || !a.value.startsWith("/"))) return false;
    this.settle(a.id, a.value);
    return true;
  }

  /** Cancels every question still open for `session` (it logged out or its last stream closed). */
  cancel(session: string): void {
    for (const [id, p] of this.pending) if (p.session === session) this.settle(id, p.kind === "confirm" ? false : null);
  }

  private settle(id: string, value: boolean | string | null): void {
    const p = this.pending.get(id);
    if (!p) return;
    this.pending.delete(id);
    clearTimeout(p.timer);
    p.resolve(value);
  }

  async pickPath(pick: PickKind | "upload" | "download", suggested?: string): Promise<string | undefined> {
    const v = await this.ask({ kind: "path", pick, ...(suggested !== undefined && { suggested }) });
    return typeof v === "string" ? v : undefined;
  }
}

export function webPlatform(bridge: WebBridge, notifyError: (message: string) => void, manageKernel?: Platform["manageKernel"]): Platform {
  return {
    writeClipboard: (text) => bridge.effect({ type: "clipboard", text }),
    openPath: async (p) => {
      // The files are on the server; the page gets the path to use in a shell there.
      bridge.effect({ type: "clipboard", text: p });
      return `网页管理界面无法打开服务器上的文件，路径已复制：${p}`;
    },
    openExternal: async (url) => bridge.effect({ type: "open", url }),
    confirm: async (o) => (await bridge.ask({ kind: "confirm", ...o })) === true,
    showPanel: () => {},
    quit: () => {},
    runCli: (command, node) => runXmesh(command, node),
    manageKernel,
    notifyError,
    removeStaleState: async (node) => {
      if (!removeStaleControl(stateRoots(), node)) throw new Error("状态文件已更新或仍被进程使用，请刷新后重试");
    },
    pickPath: (kind) => bridge.pickPath(kind),
  };
}
