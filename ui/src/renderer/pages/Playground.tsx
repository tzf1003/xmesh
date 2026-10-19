import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Empty, ErrorBanner, PageHead } from "../components";
import { useT } from "../i18n";
import type { ActionError } from "../../shared/ipc";
import { historyArgs, type CallOutcome, type ToolDescriptor } from "../../shared/playground";
import { argumentError, initialArgs, objectArgs, toolGroup } from "../lib/playground";
import { ToolForm } from "./playground/ToolForm";
import { ResultView } from "./playground/ResultView";

export function Playground({ ctx }: { ctx: PageContext }): JSX.Element {
  return <InstancePlayground key={ctx.snapshot.selectedInstance} ctx={ctx}/>;
}

function InstancePlayground({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const instance = ctx.snapshot.selectedInstance;
  const c = ctx.snapshot.connection;
  const enabled = c.kind === "connected" && c.status.roles.includes("gateway") && ctx.can("pats");
  const [device, setDevice] = useState("local");
  const [query, setQuery] = useState("");
  const [tools, setTools] = useState<ToolDescriptor[]>([]);
  const [selected, setSelected] = useState<ToolDescriptor>();
  const [raw, setRaw] = useState("{}");
  const [mode, setMode] = useState("form");
  const [invalidForm, setInvalidForm] = useState(false);
  const [revision, setRevision] = useState(0);
  const [changed, setChanged] = useState(false);
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ActionError>();
  const [outcome, setOutcome] = useState<CallOutcome>();
  const [running, setRunning] = useState<{ id: string; started: number }>();
  const [seconds, setSeconds] = useState(0);
  const [progress, setProgress] = useState<string>();
  const [timeout, setTimeoutSeconds] = useState(60);
  const [confirm, setConfirm] = useState(false);
  const [history, setHistory] = useState<{ name: string; device: string; args: unknown; ok: boolean }[]>([]);
  const loadSeq = useRef(0);
  const alive = useRef(true);
  const activeCall = useRef<string>();
  useEffect(() => () => { alive.current = false; loadSeq.current++; if (activeCall.current) void getApi().playground({ op: "cancel", instance, id: activeCall.current }); }, []);
  const select = (tool: ToolDescriptor): void => { setSelected(tool); setRaw(JSON.stringify(initialArgs(tool), null, 2)); setInvalidForm(false); setOutcome(undefined); setError(undefined); };
  const load = async (target = device): Promise<void> => {
    const seq = ++loadSeq.current;
    setLoading(true);
    const r = await getApi().playground({ op: "list", instance, device: target, pattern: query.trim() || "*" });
    if (!alive.current || seq !== loadSeq.current) return;
    setLoading(false);
    if (!r.ok) { setTools([]); setError(r.error); return; }
    setError(undefined); setTools(r.data.tools); setTruncated(r.data.truncated); setRevision(r.data.revision); setChanged(false);
    setSelected((cur) => r.data.tools.find((tool) => tool.name === cur?.name) ?? cur);
  };
  useEffect(() => { if (!enabled) return; const timer = setTimeout(() => void load(), 250); return () => clearTimeout(timer); }, [device, enabled, query]);
  useEffect(() => {
    if (!enabled) return;
    let pending = false;
    const timer = setInterval(() => {
      if (pending) return;
      pending = true;
      void getApi().playground({ op: "status", instance }).then((r) => {
        if (!alive.current || !r.ok) return;
        setChanged(r.data.revision > revision && revision > 0);
        if (running) { setSeconds(Math.floor((Date.now() - running.started) / 1000)); const p = r.data.progress[running.id]; setProgress(p ? `${p.message ?? ""} ${p.progress}${p.total ? ` / ${p.total}` : ""}` : undefined); }
      }).finally(() => { pending = false; });
    }, 700);
    return () => clearInterval(timer);
  }, [enabled, revision, running]);
  const validation = selected ? argumentError(selected, raw) : undefined;
  const execute = async (): Promise<void> => {
    if (!selected || running || validation || invalidForm) return;
    const id = crypto.randomUUID();
    activeCall.current = id;
    const name = selected.name, args = objectArgs(raw), target = device;
    setRunning({ id, started: Date.now() }); setOutcome(undefined); setError(undefined); setSeconds(0);
    const r = await getApi().playground({ op: "call", instance, id, name, arguments: args, timeoutMs: timeout * 1000 });
    activeCall.current = undefined;
    if (!alive.current) return;
    setRunning(undefined); setProgress(undefined);
    setHistory((h) => [{ name, device: target, args: historyArgs(args), ok: r.ok && !r.data.result.isError }, ...h].slice(0, 30));
    if (r.ok) setOutcome(r.data); else setError(r.error);
  };
  const run = (): void => { if (selected?.annotations?.destructiveHint === true || selected?.name.endsWith("/fs.delete")) setConfirm(true); else void execute(); };
  const filtered = tools.filter((tool) => `${tool.name} ${tool.description ?? ""}`.toLowerCase().includes(query.toLowerCase()));
  const groups = [...new Set(filtered.map((tool) => toolGroup(tool.name)))].sort();
  const peers = c.kind === "connected" ? c.peers.filter((p) => !p.self) : [];
  let args: Record<string, unknown> = {};
  try { args = objectArgs(raw); } catch { /* Raw editor retains incomplete input. */ }
  return <div class="page playground" data-testid="page-playground"><PageHead title={t("pg.title")} sub={t("pg.sub")}/>{!enabled ? <Banner>{t("pg.needsGateway")}</Banner> : <>
    <div class="card row tool-toolbar"><label>{t("pg.device")}<select data-testid="tool-device" disabled={!!running} value={device} onChange={(e) => { setDevice(e.currentTarget.value); setSelected(undefined); setOutcome(undefined); setRaw("{}"); }}><option value="local">{t("pg.local")}</option>{peers.map((p) => <option key={p.name} value={p.name} disabled={!p.online}>{p.name}{p.online ? "" : ` · ${t("pg.offline")}`}</option>)}<option value="gateway">{t("pg.gateway")}</option></select></label><input type="search" class="grow" placeholder={t("pg.search")} aria-label={t("pg.search")} value={query} onInput={(e) => setQuery(e.currentTarget.value)}/><button type="button" class="btn" disabled={loading || !!running} onClick={() => void load()} data-testid="tools-refresh">{t("pg.refresh")}</button><span class="hint">{t("pg.toolCount", { n: tools.length })}</span></div>
    {changed && <Banner>{t("pg.changed")}</Banner>}{truncated && <Banner kind="warn">{t("pg.truncated")}</Banner>}
    <div class="tool-workspace"><aside class="card tool-list" aria-label={t("pg.tools")}>{filtered.length === 0 ? <Empty>{loading ? "…" : t("pg.empty")}</Empty> : groups.map((group) => <details key={group} open><summary>{group}<span class="hint">{filtered.filter((x) => toolGroup(x.name) === group).length}</span></summary>{filtered.filter((tool) => toolGroup(tool.name) === group).map((tool) => <button type="button" disabled={!!running} class={`tool-item ${selected?.name === tool.name ? "active" : ""}`} key={tool.name} data-testid={`tool-${tool.name}`} aria-pressed={selected?.name === tool.name} onClick={() => select(tool)}><span class="mono">{tool.name.split("/").at(-1)}</span><small>{tool.description?.split("\n")[0]}</small></button>)}</details>)}</aside>
      <div class="tool-detail">{selected ? <section class="card"><div class="row"><h2 class="mono" data-testid="tool-name">{selected.name}</h2>{selected.annotations?.readOnlyHint && <Badge kind="ok">{t("pg.readonly")}</Badge>}</div><p class="tool-description">{selected.description}</p><div class="tabs" role="tablist">{["form", "json"].map((v) => <button type="button" role="tab" class={mode === v ? "active" : ""} aria-selected={mode === v} onClick={() => { setMode(v); setInvalidForm(false); }}>{t(v === "form" ? "pg.form" : "pg.json")}</button>)}</div>
      <form onSubmit={(e) => { e.preventDefault(); run(); }} onKeyDown={(e) => { if ((e.ctrlKey || e.metaKey) && e.key === "Enter") { e.preventDefault(); run(); } }}><fieldset disabled={!!running} class="tool-argument-set">{mode === "json" ? <textarea data-testid="tool-json" aria-label={t("pg.json")} class="mono tool-json" rows={10} value={raw} onInput={(e) => setRaw(e.currentTarget.value)}/> : <ToolForm key={`${selected.name}:${mode}`} tool={selected} args={args} onChange={(v) => setRaw(JSON.stringify(v, null, 2))} onInvalid={setInvalidForm}/>}
      {(validation || invalidForm) && <p class="hint tool-required" data-testid="tool-validation">{t("pg.invalid", { message: validation === "[redacted]" ? t("pg.redacted") : validation ?? "JSON" })}</p>}<div class="row tool-runbar"><label>{t("pg.timeout")}<input type="number" min={1} max={1800} value={timeout} onInput={(e) => setTimeoutSeconds(Number(e.currentTarget.value))}/></label><button type="submit" class="btn primary" data-testid="tool-run" disabled={!!validation || invalidForm || !Number.isInteger(timeout) || timeout < 1 || timeout > 1800}>{t("pg.run")} <kbd>⌘/Ctrl ↵</kbd></button></div></fieldset></form>
      {running && <div class="row" aria-live="polite"><span>{t("pg.wait", { s: seconds })} {progress}</span><button type="button" class="btn" data-testid="tool-cancel" onClick={() => void getApi().playground({ op: "cancel", instance, id: running.id }).then((r) => { if (r.ok) ctx.toast(t("pg.notSent")); else ctx.toast(r.error.message, true); })}>{t("pg.cancel")}</button></div>}
      <details><summary>{t("pg.schema")}</summary><pre>{JSON.stringify(selected, null, 2)}</pre></details><details><summary>{t("pg.example")}</summary><button type="button" class="btn small" onClick={() => void getApi().playground({ op: "copy", instance, text: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: selected.name, arguments: args } }, null, 2) })}>{t("pg.copy")}</button><pre>{JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: selected.name, arguments: args } }, null, 2)}</pre></details></section> : <section class="card"><Empty>{t("pg.pick")}</Empty></section>}
      {error && <ErrorBanner error={{ ...error, message: error.code === "cancelled" ? t("pg.cancelled") : error.code === "timeout" ? t("pg.timedOut") : error.message }}/>} {outcome && <ResultView instance={instance} key={outcome.durationMs + JSON.stringify(outcome.result).slice(0, 40)} outcome={outcome}/>}</div></div>
      <section class="card tool-history"><div class="row"><h2>{t("pg.history")}</h2><div class="spacer"/><button type="button" class="btn small" onClick={() => setHistory([])}>{t("pg.clear")}</button></div>{history.length === 0 ? <p class="hint">{t("pg.noHistory")}</p> : <div class="row">{history.map((h, i) => <button type="button" class="btn small mono" key={i} disabled={!!running || h.device !== device || !tools.some((x) => x.name === h.name)} onClick={() => { setSelected(tools.find((x) => x.name === h.name)); setMode("json"); setRaw(JSON.stringify(h.args, null, 2)); setInvalidForm(false); setOutcome(undefined); }} data-testid="tool-history-entry">{h.ok ? "✓" : "!"} {h.name}</button>)}</div>}</section>
    </>}{confirm && <Confirm title={t("pg.confirmTitle")} body={t("pg.confirmBody", { device, tool: selected?.name ?? "" })} confirmLabel={t("pg.confirm")} onConfirm={() => { setConfirm(false); void execute(); }} onCancel={() => setConfirm(false)}/>}</div>;
}
