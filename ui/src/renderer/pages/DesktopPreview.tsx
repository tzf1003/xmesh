import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Banner } from "../components";
import { useResource } from "../hooks";
import { useT } from "../i18n";
import { observationPats, observeDesktop, type DesktopPreview as Preview } from "../lib/computer-preview";
import { formatTime } from "../lib/format";
import { DeviceSelect } from "./playground/DeviceSelect";

export function DesktopPreview({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const instance = ctx.snapshot.selectedInstance;
  const connection = ctx.snapshot.connection;
  const boot = connection.kind === "connected" ? connection.status.control.boot_id : "";
  const enabled = connection.kind === "connected" && connection.status.roles.includes("gateway") && ctx.can("pats");
  const [device, setDevice] = useState("local");
  const [display, setDisplay] = useState("");
  const [target, setTarget] = useState("");
  const [targets,setTargets] = useState<Preview["targets"]>([]);
  const [pat, setPat] = useState("");
  const { resource: credentials, reload: reloadCredentials } = useResource(() => getApi().listPats(), [instance, boot, enabled]);
  const targetDevice = device === "local" && connection.kind === "connected" ? connection.status.hostname : device;
  const pats = credentials.state === "ready" ? observationPats(credentials.data.pats, targetDevice) : [];
  const selectedPat = pats.find((p) => p.name === pat)?.name;
  const [preview, setPreview] = useState<Preview>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [automatic, setAutomatic] = useState(false);
  const pending = useRef<AbortController>();
  const generation = useRef(0);
  useEffect(() => {setPat("");setTarget("");setTargets([]);}, [instance, device, boot]);
  const refresh = async (): Promise<void> => {
    if (!enabled || !selectedPat || pending.current || document.visibilityState === "hidden") return;
    const seq = generation.current;
    const abort = new AbortController();
    pending.current = abort;
    setBusy(true);
    try {
      const result = await observeDesktop(getApi().playground, instance, device, display, abort.signal, selectedPat,target);
      if (seq === generation.current && !abort.signal.aborted) { setPreview(result); setTargets(result.targets);setError(""); }
    } catch (e) {
      if (seq === generation.current && !abort.signal.aborted) {
        setError(e instanceof Error ? e.message : String(e)); setPreview(undefined); setAutomatic(false);
      }
    } finally {
      if (pending.current === abort) pending.current = undefined;
      if (seq === generation.current) setBusy(false);
    }
  };
  useEffect(() => {
    generation.current++;
    pending.current?.abort(); pending.current = undefined;
    setPreview(undefined); setError(""); setBusy(false); setAutomatic(false);
    return () => { generation.current++; pending.current?.abort(); pending.current = undefined; };
  }, [instance, device, display, boot, enabled, selectedPat,target]);
  useEffect(() => {
    if (!automatic || !enabled) return;
    void refresh();
    const timer = setInterval(() => void refresh(), 2000);
    return () => clearInterval(timer);
  }, [automatic, enabled, instance, device, display, boot, selectedPat,target]);
  return <section class="card stack" data-testid="desktop-preview">
    <h2>{t("computer.preview")}</h2>
    <p class="hint">{t("computer.previewHint")}</p>
    {!enabled ? <Banner>{t("pg.needsGateway")}</Banner> : <>
      <div class="row computer-preview-controls">
        <DeviceSelect ctx={ctx} value={device} onChange={(value) => { setDevice(value); setDisplay(""); }}/>
        <label class="field"><span>{t("computer.previewCredential")}</span><select value={selectedPat ?? ""} onChange={(e) => setPat(e.currentTarget.value)} data-testid="computer-preview-pat"><option value="">{t("computer.previewChooseCredential")}</option>{pats.map((p) => <option value={p.name} key={p.name}>{p.name}</option>)}</select></label>
        {targets.length>0 && <label class="field"><span>{t("computer.targetWindow")}</span><select value={target} onChange={(e)=>setTarget(e.currentTarget.value)} data-testid="computer-preview-target"><option value="">{t("computer.fullDesktop")}</option>{targets.map((window)=><option key={window.id} value={window.id}>{window.name} · {window.id}</option>)}</select></label>}
        {!target && preview && preview.displays.length > 1 && <label class="field"><span>{t("computer.displays")}</span><select value={display || preview.display} onChange={(e) => setDisplay(e.currentTarget.value)}>{preview.displays.map((d) => <option value={d.id} key={d.id}>{d.name}</option>)}</select></label>}
        <button type="button" class="btn" disabled={busy || !selectedPat} onClick={() => void refresh()} data-testid="computer-preview-refresh">{t("computer.previewRefresh")}</button>
        <label><input type="checkbox" checked={automatic} disabled={!selectedPat} onChange={(e) => setAutomatic(e.currentTarget.checked)} data-testid="computer-preview-auto"/> {t("computer.previewAuto")}</label>
        <button type="button" class="btn small" disabled={!preview && !busy} onClick={() => { generation.current++; pending.current?.abort(); pending.current = undefined; setPreview(undefined); setAutomatic(false); setBusy(false); }} data-testid="computer-preview-clear">{t("computer.previewClear")}</button>
      </div>
      {!selectedPat && <Banner>{t("computer.previewCredentialHint", { scope: `computer:observe:${targetDevice}` })} <button class="btn small" type="button" onClick={() => ctx.go("authz")}>{t("computer.previewManageCredentials")}</button> <button class="btn small" type="button" onClick={reloadCredentials}>{t("common.refresh")}</button></Banner>}
      {credentials.state === "error" && <Banner kind="warn">{credentials.error.message}</Banner>}
      {error && <Banner kind="warn">{error}</Banner>}
      {preview && <figure class="computer-preview-frame"><img src={preview.source} width={preview.width} height={preview.height} alt={t("computer.previewAlt", { device })} data-testid="computer-preview-image"/><figcaption class="hint">{device} · {preview.width}×{preview.height} · {formatTime(preview.capturedAt)}</figcaption></figure>}
    </>}
  </section>;
}
