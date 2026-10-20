import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Dialog, Empty, ErrorBanner, Field, Meter, PageHead } from "../components";
import { useT } from "../i18n";
import { DeviceSelect } from "./playground/DeviceSelect";
import type { ActionError } from "../../shared/ipc";
import type { ToolDescriptor } from "../../shared/playground";
import type { TransferState } from "../../shared/transfers";

export function Transfers({ ctx }: { ctx: PageContext }): JSX.Element {
  return <InstanceTransfers key={ctx.snapshot.selectedInstance} ctx={ctx}/>;
}

function InstanceTransfers({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const instance = ctx.snapshot.selectedInstance;
  const [device, setDevice] = useState("local");
  const [direction, setDirection] = useState<"upload" | "download">("upload");
  const [tools, setTools] = useState<ToolDescriptor[]>([]);
  const [file, setFile] = useState<{ ticket: string; name: string; bytes: number }>();
  const [remote, setRemote] = useState("");
  const [cwd, setCwd] = useState("");
  const [states, setStates] = useState<TransferState[]>([]);
  const [error, setError] = useState<ActionError>();
  const [pending, setPending] = useState(false);
  const [browse, setBrowse] = useState(false);
  const alive = useRef(true);
  useEffect(
    () => () => {
      alive.current = false;
    },
    [],
  );
  const enabled =
    ctx.snapshot.connection.kind === "connected" &&
    ctx.snapshot.connection.status.roles.includes("gateway") &&
    ctx.can("pats");
  useEffect(() => {
    if (!enabled) return;
    let valid = true;
    void getApi()
      .playground({ op: "list", instance, device, pattern: "fs." })
      .then((r) => {
        if (!valid) return;
        if (r.ok) {
          setTools(r.data.tools);
          setError(undefined);
        } else setError(r.error);
      });
    return () => {
      valid = false;
    };
  }, [device, enabled]);
  useEffect(() => {
    let pending = false;
    const poll = (): void => {
      if (pending || !enabled) return;
      pending = true;
      void getApi()
        .transfers({ op: "status", instance })
        .then((r) => {
          if (alive.current && r.ok) setStates(r.data.states ?? []);
        })
        .finally(() => {
          pending = false;
        });
    };
    poll();
    const timer = setInterval(poll, 500);
    return () => clearInterval(timer);
  }, [enabled]);
  const tool = tools.find((x) => x.name === `${device}/fs.${direction}`);
  const chunked = !!(tool?.inputSchema.properties as Record<string, unknown> | undefined)?.transfer_id;
  const active = states.some((s) => ["preparing", "sending", "checking"].includes(s.phase));
  const start = async (force = false): Promise<void> => {
    setPending(true);
    setError(undefined);
    const r = await getApi().transfers({
      op: "start",
      instance,
      direction,
      device,
      path: remote,
      ...(cwd && { cwd }),
      ...(file && { ticket: file.ticket }),
      force,
    });
    if (!alive.current) return;
    setPending(false);
    if (!r.ok) setError(r.error);
  };
  return (
    <div class="page" data-testid="page-transfers">
      <PageHead title={t("ws.files")} sub={t("ws.fileSub")} />
      {!enabled ? (
        <Banner>{t("pg.needsGateway")}</Banner>
      ) : (
        <>
          <section class="card">
            <div class="row">
              <DeviceSelect
                ctx={ctx}
                value={device}
                disabled={active || pending}
                onChange={(v) => {
                  setDevice(v);
                  setTools([]);
                }}
              />
              <div class="segs">
                {(["upload", "download"] as const).map((d) => (
                  <button
                    type="button"
                    disabled={active || pending}
                    aria-pressed={d === direction}
                    onClick={() => setDirection(d)}
                    data-testid={`transfer-${d}`}
                  >
                    {t(`ws.${d}`)}
                  </button>
                ))}
              </div>
              {tool && <Badge kind="info">{t(chunked ? "ws.chunked" : "ws.legacy")}</Badge>}
            </div>
            <fieldset class="transfer-fields" disabled={active || pending}>
              {direction === "upload" && (
                <div class="row">
                  <button
                    type="button"
                    class="btn"
                    data-testid="transfer-pick"
                    onClick={() =>
                      void getApi()
                        .transfers({ op: "pick", instance })
                        .then((r) => {
                          if (r.ok && r.data.ticket)
                            setFile({ ticket: r.data.ticket, name: r.data.name!, bytes: r.data.bytes! });
                          else if (!r.ok) setError(r.error);
                        })
                    }
                  >
                    {t("ws.pick")}
                  </button>
                  {file && (
                    <span class="mono" data-testid="transfer-file">
                      {file.name} · {file.bytes.toLocaleString()} B
                    </span>
                  )}
                </div>
              )}
              <Field label={t("ws.path")}>
                <div class="row">
                  <input
                    class="grow"
                    type="text"
                    value={remote}
                    data-testid="transfer-path"
                    onInput={(e) => setRemote(e.currentTarget.value)}
                  />
                  <button
                    type="button"
                    class="btn"
                    disabled={!tools.some((x) => x.name.endsWith("/fs.list"))}
                    onClick={() => setBrowse(true)}
                  >
                    {t("ws.browse")}
                  </button>
                </div>
              </Field>
              <Field label={t("ws.cwd")}>
                <input type="text" value={cwd} onInput={(e) => setCwd(e.currentTarget.value)} />
              </Field>
              {!tool && <Banner>{t("ws.unsupported")}</Banner>}
              <p class="hint">{t("ws.fileOnly")}</p>
              <button
                type="button"
                class="btn primary"
                data-testid="transfer-start"
                disabled={!remote || !tool || (direction === "upload" && !file)}
                onClick={() => void start()}
              >
                {t("ws.start")}
              </button>
            </fieldset>
          </section>
          {error && <ErrorBanner error={error} />}
          <section class="card">
            <h2>{t("ws.progress")}</h2>
            {states.length === 0 ? (
              <Empty>{t("ws.transferNone")}</Empty>
            ) : (
              [...states].reverse().map((s) => (
                <div class="transfer-job" key={s.id} data-testid="transfer-job">
                  <div class="row">
                    <strong>{s.name}</strong>
                    <Badge kind={s.phase === "complete" ? "ok" : s.phase === "failed" ? "danger" : ""}>
                      {t(`ws.${s.phase}`)}
                    </Badge>
                    <span class="hint">
                      {t(`ws.${s.direction}`)} · {s.device}: {s.remote}
                    </span>
                  </div>
                  <div class="row">
                    <div class="grow">
                      <Meter
                        used={s.bytes}
                        max={s.total ?? 0}
                        label={`${s.bytes.toLocaleString()} / ${(s.total ?? 0).toLocaleString()} B`}
                      />
                    </div>
                    {["preparing", "sending", "checking"].includes(s.phase) && (
                      <button
                        type="button"
                        class="btn"
                        onClick={() => void getApi().transfers({ op: "cancel", instance, id: s.id })}
                      >
                        {t("ws.cancel")}
                      </button>
                    )}
                  </div>
                  {s.sha256 && (
                    <p class="hint mono">
                      {t("ws.verify")}: {s.sha256}
                    </p>
                  )}
                  {s.error && <pre>{s.error}</pre>}
                  {s.code === "conflict" && s.direction === "upload" && (
                    <button
                      type="button"
                      class="btn danger"
                      disabled={active || pending}
                      onClick={() => void getApi().transfers({ op: "retry", instance, id: s.id }).then((r) => { if (!r.ok) setError(r.error); })}
                    >
                      {t("ws.overwrite")}
                    </button>
                  )}
                </div>
              ))
            )}
            <p class="hint">{t("ws.cancelHint")}</p>
          </section>
          {browse && (
            <RemoteBrowser
              instance={instance}
              device={device}
              cwd={cwd}
              onClose={() => setBrowse(false)}
              onChoose={(p) => {
                setRemote(p);
                setBrowse(false);
              }}
            />
          )}
        </>
      )}
    </div>
  );
}

function RemoteBrowser({
  instance,
  device,
  cwd,
  onClose,
  onChoose,
}: {
  instance: string;
  device: string;
  cwd: string;
  onClose(): void;
  onChoose(p: string): void;
}): JSX.Element {
  const t = useT();
  const [path, setPath] = useState(".");
  const [resolved, setResolved] = useState(".");
  const [entries, setEntries] = useState<{ name: string; type: string; size: number }[]>([]);
  const [error, setError] = useState<ActionError>();
  const seq = useRef(0);
  useEffect(
    () => () => {
      seq.current++;
    },
    [],
  );
  const load = async (p: string): Promise<void> => {
    const ticket = ++seq.current;
    const r = await getApi().playground({
      op: "call",
      instance,
      id: crypto.randomUUID(),
      name: `${device}/fs.list`,
      arguments: { path: p, depth: 1, ...(cwd && { cwd }) },
      timeoutMs: 20_000,
    });
    if (ticket !== seq.current) return;
    if (!r.ok) return setError(r.error);
    const body = r.data.result.structuredContent;
    if (r.data.result.isError || !body || !Array.isArray(body.entries))
      return setError({ code: "tool_error", message: JSON.stringify(body ?? r.data.result.content) });
    setError(undefined);
    setEntries(body.entries as typeof entries);
    setResolved(String(body.resolved_path ?? p));
    setPath(String(body.resolved_path ?? p));
  };
  useEffect(() => {
    void load(".");
  }, []);
  return (
    <Dialog title={t("ws.browse")} onClose={onClose}>
      <form
        class="row"
        onSubmit={(e) => {
          e.preventDefault();
          void load(path);
        }}
      >
        <input
          type="text"
          value={path}
          aria-label={t("ws.directory")}
          onInput={(e) => setPath(e.currentTarget.value)}
        />
        <button type="submit" class="btn">
          {t("ws.show")}
        </button>
        <button type="button" class="btn" onClick={() => void load(`${resolved}/..`)}>
          ..
        </button>
      </form>
      {error && <ErrorBanner error={error} />}
      <div class="remote-files">
        <table>
          <thead>
            <tr>
              <th>{t("ws.fileName")}</th>
              <th>{t("ws.size")}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.name}>
                <td class="mono">
                  {e.name}
                  {e.type === "dir" ? "/" : ""}
                </td>
                <td>{e.size.toLocaleString()} B</td>
                <td>
                  <button
                    type="button"
                    class="btn small"
                    onClick={() =>
                      e.type === "dir" ? void load(`${resolved}/${e.name}`) : onChoose(`${resolved}/${e.name}`)
                    }
                  >
                    {t(e.type === "dir" ? "ws.show" : "ws.choose")}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Dialog>
  );
}
