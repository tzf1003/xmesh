import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { Terminal as Xterm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Dialog, Empty, ErrorBanner, Field, PageHead } from "../components";
import { useT } from "../i18n";
import { DeviceSelect } from "./playground/DeviceSelect";
import type { ActionError } from "../../shared/ipc";

interface Session {
  id: string;
  name?: string;
  owner: string;
  state: string;
  cols: number;
  rows: number;
  cwd: string;
  idle_s: number;
}

export function Terminal({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const instance = ctx.snapshot.selectedInstance;
  const [device, setDevice] = useState("local");
  const [sessions, setSessions] = useState<Session[]>([]);
  const [caller, setCaller] = useState("");
  const [selected, setSelected] = useState("");
  const [error, setError] = useState<ActionError>();
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [shell, setShell] = useState("");
  const [cwd, setCwd] = useState("");
  const [env, setEnv] = useState("{}");
  const [target, setTarget] = useState("");
  const [transfer, setTransfer] = useState(false);
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<"close" | "take" | "INT" | "TERM" | "KILL">();
  const alive = useRef(true);
  useEffect(
    () => () => {
      alive.current = false;
    },
    [],
  );
  const generation = useRef(0);
  const owned = useRef(new Map<string, string>());
  const enabled =
    ctx.snapshot.connection.kind === "connected" &&
    ctx.snapshot.connection.status.roles.includes("gateway") &&
    ctx.can("pats");
  const row = sessions.find((s) => s.id === selected);
  const writable = row?.state === "active" && row.owner === (caller || owned.current.get(`${device}/${selected}`));
  const call = async (tool: string, args: Record<string, unknown>): Promise<Record<string, unknown>> => {
    const result = await getApi().playground({
      op: "call",
      instance,
      id: crypto.randomUUID(),
      name: `${device}/${tool}`,
      arguments: args,
      timeoutMs: 40_000,
    });
    if (!result.ok) throw new Error(result.error.message);
    const body =
      result.data.result.structuredContent ??
      (JSON.parse(
        result.data.result.content
          .filter((c) => c.type === "text")
          .map((c) => c.text)
          .join(""),
      ) as Record<string, unknown>);
    if (result.data.result.isError) throw new Error(JSON.stringify(body));
    return body;
  };
  const refresh = async (): Promise<void> => {
    const g = generation.current;
    try {
      const body = await call("pty.list", {});
      if (g !== generation.current || !alive.current) return;
      setSessions(Array.isArray(body.sessions) ? (body.sessions as Session[]) : []);
      if (typeof body.caller_id === "string") setCaller(body.caller_id);
    } catch (e) {
      if (g === generation.current && alive.current) setError({ code: "tool_error", message: String(e) });
    }
  };
  useEffect(() => {
    generation.current++;
    setSessions([]);
    setCaller("");
    setSelected("");
    setError(undefined);
    if (!enabled) return;
    let pending = false;
    const load = (): void => {
      if (pending) return;
      pending = true;
      void refresh().finally(() => {
        pending = false;
      });
    };
    load();
    const timer = setInterval(load, 3000);
    return () => {
      generation.current++;
      clearInterval(timer);
    };
  }, [device, enabled]);
  const operation = async (fn: () => Promise<unknown>): Promise<void> => {
    setBusy(true);
    setError(undefined);
    try {
      await fn();
      await refresh();
    } catch (e) {
      setError({ code: "tool_error", message: e instanceof Error ? e.message : String(e) });
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const open = async (): Promise<void> => {
    let parsed: unknown;
    try {
      parsed = JSON.parse(env);
      if (
        !parsed ||
        typeof parsed !== "object" ||
        Array.isArray(parsed) ||
        Object.values(parsed).some((x) => typeof x !== "string")
      )
        throw new Error("env must be an object of strings");
    } catch (e) {
      setError({ code: "invalid_args", message: String(e) });
      return;
    }
    await operation(async () => {
      const body = await call("pty.open", {
        cols: 100,
        rows: 30,
        ...(name && { name }),
        ...(shell && { shell }),
        ...(cwd && { cwd }),
        env: parsed,
      });
      if (typeof body.session_id !== "string" || typeof body.owner !== "string")
        throw new Error("Invalid terminal response");
      owned.current.set(`${device}/${body.session_id}`, body.owner);
      setCaller(body.owner);
      setSelected(body.session_id);
      setCreating(false);
      setEnv("{}");
    });
  };
  const confirmed = (): void => {
    const action = confirm;
    setConfirm(undefined);
    if (!action) return;
    void operation(async () => {
      if (action === "take") {
        if (!caller) throw new Error("Current caller is unavailable; refresh the target node");
        const body = await call("pty.transfer", { session_id: selected, to_caller: caller });
        owned.current.set(`${device}/${selected}`, String(body.owner));
      } else if (action === "close") await call("pty.close", { session_id: selected });
      else await call("pty.signal", { session_id: selected, signal: action });
    });
  };
  return (
    <div class="page terminal-page" data-testid="page-terminal">
      <PageHead title={t("ws.terminal")} sub={t("ws.terminalSub")} />
      {!enabled ? (
        <Banner>{t("pg.needsGateway")}</Banner>
      ) : (
        <>
          <section class="card row">
            <DeviceSelect ctx={ctx} value={device} disabled={busy} onChange={setDevice} />
            <button
              type="button"
              class="btn primary"
              disabled={busy}
              data-testid="terminal-new"
              onClick={() => setCreating(true)}
            >
              {t("ws.new")}
            </button>
            <button type="button" class="btn" onClick={() => void refresh()}>
              {t("ws.refresh")}
            </button>
            <label class="field grow">
              <span>{t("ws.sessions")}</span>
              <select
                value={selected}
                onChange={(e) => setSelected(e.currentTarget.value)}
                data-testid="terminal-session"
              >
                <option value="">—</option>
                {sessions.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name || s.id} · {s.state} · {s.owner}
                  </option>
                ))}
              </select>
            </label>
          </section>
          {error && <ErrorBanner error={error} />}
          {row ? (
            <>
              <div class="row">
                <Badge kind={writable ? "ok" : ""}>
                  {t(writable ? "ws.active" : row.state === "closed" ? "ws.closed" : "ws.readonly")}
                </Badge>
                <span class="hint mono">
                  {row.cols}×{row.rows} · {row.cwd} · {t("ws.owner")}: {row.owner || "—"}
                </span>
              </div>
              <TerminalScreen
                key={`${device}/${selected}`}
                instance={instance}
                device={device}
                session={selected}
                writable={!!writable}
                onError={(message) => setError({ code: "tool_error", message })}
                onReconnect={() => void refresh()}
              />
              <div class="row">
                {(["INT", "TERM", "KILL"] as const).map((signal) => (
                  <button
                    type="button"
                    class={`btn ${signal === "KILL" ? "danger" : ""}`}
                    disabled={!writable || busy}
                    onClick={() => setConfirm(signal)}
                  >
                    {signal}
                  </button>
                ))}
                <button
                  type="button"
                  class="btn danger"
                  data-testid="terminal-close"
                  disabled={!writable || busy}
                  onClick={() => setConfirm("close")}
                >
                  {t("ws.close")}
                </button>
                <button type="button" class="btn" disabled={!writable || busy} onClick={() => setTransfer(true)}>
                  {t("ws.transfer")}
                </button>
                {row.state === "orphaned" && caller && (
                  <button type="button" class="btn" onClick={() => setConfirm("take")}>
                    {t("ws.take")}
                  </button>
                )}
              </div>
            </>
          ) : (
            <section class="card">
              <Empty>{t("ws.none")}</Empty>
            </section>
          )}
          {creating && (
            <Dialog title={t("ws.new")} onClose={() => setCreating(false)}>
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  void open();
                }}
              >
                <Field label={t("ws.name")}>
                  <input type="text" maxLength={64} value={name} onInput={(e) => setName(e.currentTarget.value)} />
                </Field>
                <Field label={t("ws.shell")}>
                  <input type="text" value={shell} onInput={(e) => setShell(e.currentTarget.value)} />
                </Field>
                <Field label={t("ws.cwd")}>
                  <input type="text" value={cwd} onInput={(e) => setCwd(e.currentTarget.value)} />
                </Field>
                <Field label="env (JSON)">
                  <textarea class="mono" value={env} onInput={(e) => setEnv(e.currentTarget.value)} />
                </Field>
                <button type="submit" class="btn primary" disabled={busy} data-testid="terminal-connect">
                  {t("ws.connect")}
                </button>
              </form>
            </Dialog>
          )}
          {transfer && (
            <Dialog title={t("ws.transfer")} onClose={() => setTransfer(false)}>
              <Field label={t("ws.target")}>
                <input type="text" value={target} onInput={(e) => setTarget(e.currentTarget.value)} />
              </Field>
              <button
                type="button"
                class="btn primary"
                disabled={!target || busy}
                onClick={() =>
                  void operation(async () => {
                    await call("pty.transfer", { session_id: selected, to_caller: target });
                    owned.current.delete(`${device}/${selected}`);
                    setTransfer(false);
                  })
                }
              >
                {t("ws.confirm")}
              </button>
            </Dialog>
          )}
          {confirm && (
            <Confirm
              title={t(confirm === "close" ? "ws.closeTitle" : confirm === "take" ? "ws.takeTitle" : "ws.signalTitle", {
                signal: confirm,
              })}
              body={t(confirm === "close" ? "ws.closeBody" : confirm === "take" ? "ws.takeBody" : "ws.signalBody", {
                device,
                session: selected,
              })}
              confirmLabel={t("ws.confirm")}
              onCancel={() => setConfirm(undefined)}
              onConfirm={confirmed}
            />
          )}
        </>
      )}
    </div>
  );
}

function TerminalScreen({
  instance,
  device,
  session,
  writable,
  onError,
  onReconnect,
}: {
  instance: string;
  device: string;
  session: string;
  writable: boolean;
  onError(s: string): void;
  onReconnect(): void;
}): JSX.Element {
  const t = useT();
  const host = useRef<HTMLDivElement>(null);
  const canWrite = useRef(writable);
  canWrite.current = writable;
  const callbacks = useRef({ onError, onReconnect });
  callbacks.current = { onError, onReconnect };
  const [disconnected, setDisconnected] = useState(false);
  const [gap, setGap] = useState(false);
  useEffect(() => {
    if (!host.current) return;
    const createElement = document.createElement;
    const terminalElement = (tag: string, options?: ElementCreationOptions): HTMLElement => {
      const element = Reflect.apply(createElement, document, [tag, options]) as HTMLElement;
      if (tag.toLowerCase() === "style")
        element.setAttribute(
          "nonce",
          document.querySelector<HTMLMetaElement>('meta[name="xmesh-style-nonce"]')?.content ?? "",
        );
      return element;
    };
    const doc = new Proxy(document, {
      get(target, key) {
        if (key === "createElement") return terminalElement;
        const value = Reflect.get(target, key, target) as unknown;
        return typeof value === "function" ? value.bind(target) : value;
      },
    });
    const term = new Xterm({
      documentOverride: doc,
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "ui-monospace, Menlo, Consolas, monospace",
      scrollback: 5000,
      allowProposedApi: false,
      theme: { background: "#101914", foreground: "#e6f0e9" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    // xterm's viewport also creates styles through window.document during open().
    // Scope the nonce-aware factory to that synchronous initialization, then restore it.
    document.createElement = terminalElement as typeof document.createElement;
    try {
      term.open(host.current);
    } finally {
      document.createElement = createElement;
    }
    fit.fit();
    term.focus();
    let stopped = false,
      readId: string | undefined,
      cursor = 0,
      input = "",
      writeTimer: ReturnType<typeof setTimeout> | undefined,
      resizeTimer: ReturnType<typeof setTimeout> | undefined;
    let writeChain = Promise.resolve();
    const invoke = async (
      tool: string,
      args: Record<string, unknown>,
      id: string = crypto.randomUUID(),
    ): Promise<Record<string, unknown>> => {
      const r = await getApi().playground({
        op: "call",
        instance,
        id,
        name: `${device}/${tool}`,
        arguments: { session_id: session, ...args },
        timeoutMs: 40_000,
      });
      if (!r.ok) throw new Error(r.error.message);
      const body = r.data.result.structuredContent ?? {};
      if (r.data.result.isError) throw new Error(JSON.stringify(body));
      return body;
    };
    const flush = (): void => {
      writeTimer = undefined;
      if (!input) return;
      const data = input;
      input = "";
      writeChain = writeChain
        .then(async () => {
          if (!stopped && canWrite.current) await invoke("pty.write", { data });
        })
        .catch((e) => {
          if (!stopped) callbacks.current.onError(String(e));
        });
    };
    const keyboard = term.onData((data) => {
      if (!canWrite.current) return;
      if (new TextEncoder().encode(input + data).length > 60000) {
        callbacks.current.onError("Terminal input exceeds 60 KiB");
        return;
      }
      input += data;
      if (!writeTimer) writeTimer = setTimeout(flush, 24);
    });
    const observer = new ResizeObserver(() => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => {
        if (stopped) return;
        fit.fit();
        if (canWrite.current)
          void invoke("pty.resize", { cols: Math.min(512, term.cols), rows: Math.min(512, term.rows) }).catch((e) =>
            callbacks.current.onError(String(e)),
          );
      }, 150);
    });
    observer.observe(host.current);
    void (async () => {
      while (!stopped) {
        try {
          readId = crypto.randomUUID();
          const body = await invoke("pty.read", { cursor, max_bytes: 65536, wait_ms: 25_000 }, readId);
          if (stopped) break;
          readId = undefined;
          setDisconnected(false);
          if (body.truncated) setGap(true);
          if (typeof body.data !== "string" || !Number.isSafeInteger(body.next_cursor))
            throw new Error("Invalid terminal output");
          if (body.data) await new Promise<void>((resolve) => term.write(body.data as string, resolve));
          cursor = body.next_cursor as number;
          if (body.eof) break;
        } catch {
          if (stopped) break;
          setDisconnected(true);
          callbacks.current.onReconnect();
          await new Promise((resolve) => setTimeout(resolve, 1500));
        }
      }
    })();
    return () => {
      stopped = true;
      clearTimeout(writeTimer);
      clearTimeout(resizeTimer);
      observer.disconnect();
      keyboard.dispose();
      term.dispose();
      if (readId) void getApi().playground({ op: "cancel", instance, id: readId });
    };
  }, [instance, device, session]);
  return (
    <div>
      {disconnected && <Banner kind="warn">{t("ws.disconnected")}</Banner>}
      {gap && <Banner kind="warn">{t("ws.gap")}</Banner>}
      <div class="terminal-screen" data-testid="terminal-screen" ref={host} aria-label={t("ws.terminal")} />
    </div>
  );
}
