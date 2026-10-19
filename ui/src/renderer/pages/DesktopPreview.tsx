import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Banner } from "../components";
import { useT } from "../i18n";
import { observeDesktop, type DesktopPreview as Preview } from "../lib/computer-preview";
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
  const [preview, setPreview] = useState<Preview>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [automatic, setAutomatic] = useState(false);
  const pending = useRef<AbortController>();
  const generation = useRef(0);
  const refresh = async (): Promise<void> => {
    if (!enabled || pending.current || document.visibilityState === "hidden") return;
    const seq = generation.current;
    const abort = new AbortController();
    pending.current = abort;
    setBusy(true);
    try {
      const result = await observeDesktop(getApi().playground, instance, device, display, abort.signal);
      if (seq === generation.current && !abort.signal.aborted) { setPreview(result); setError(""); }
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
  }, [instance, device, display, boot, enabled]);
  useEffect(() => {
    if (!automatic || !enabled) return;
    void refresh();
    const timer = setInterval(() => void refresh(), 2000);
    return () => clearInterval(timer);
  }, [automatic, enabled, instance, device, display, boot]);
  return <section class="card stack" data-testid="desktop-preview">
    <h2>{t("computer.preview")}</h2>
    <p class="hint">{t("computer.previewHint")}</p>
    {!enabled ? <Banner>{t("pg.needsGateway")}</Banner> : <>
      <div class="row computer-preview-controls">
        <DeviceSelect ctx={ctx} value={device} onChange={(value) => { setDevice(value); setDisplay(""); }}/>
        {preview && preview.displays.length > 1 && <label class="field"><span>{t("computer.displays")}</span><select value={display || preview.display} onChange={(e) => setDisplay(e.currentTarget.value)}>{preview.displays.map((d) => <option value={d.id} key={d.id}>{d.name}</option>)}</select></label>}
        <button type="button" class="btn" disabled={busy} onClick={() => void refresh()} data-testid="computer-preview-refresh">{t("computer.previewRefresh")}</button>
        <label><input type="checkbox" checked={automatic} onChange={(e) => setAutomatic(e.currentTarget.checked)} data-testid="computer-preview-auto"/> {t("computer.previewAuto")}</label>
        <button type="button" class="btn small" disabled={!preview && !busy} onClick={() => { generation.current++; pending.current?.abort(); pending.current = undefined; setPreview(undefined); setAutomatic(false); setBusy(false); }} data-testid="computer-preview-clear">{t("computer.previewClear")}</button>
      </div>
      {error && <Banner kind="warn">{error}</Banner>}
      {preview && <figure class="computer-preview-frame"><img src={preview.source} width={preview.width} height={preview.height} alt={t("computer.previewAlt", { device })} data-testid="computer-preview-image"/><figcaption class="hint">{device} · {preview.width}×{preview.height} · {formatTime(preview.capturedAt)}</figcaption></figure>}
    </>}
  </section>;
}
