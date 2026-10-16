import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { getApi } from "../../api";
import { Badge, Banner, Confirm, Field } from "../../components";
import { useT } from "../../i18n";
import { EMPTY_REGION, regionsFromForms, type RegionForm } from "../../lib/mesh";
import type { ActionError, PickKind } from "../../../shared/ipc";
import { MESH_REGIONS_MAX, type MeshDerpProbe, type MeshDerpResult, type MeshHeadscaleDerp, type MeshProbe, type MeshSync } from "../../../shared/mesh";
import type { MeshTabProps } from "../Mesh";
import { MeshError, PlanView } from "./common";

/**
 * Self-hosted DERP relays. Tailscale takes them through the policy (preview,
 * write, probe, automatic rollback when a probe fails); Headscale reads them
 * from its own server config, so the page produces the file and the snippet
 * to put there instead.
 */
export function MeshDerp({ ctx, status }: MeshTabProps): JSX.Element {
  const t = useT();
  const viaPolicy = status.capabilities.derpViaPolicy;
  const [forms, setForms] = useState<RegionForm[]>([{ ...EMPTY_REGION }]);
  const [omitDefault, setOmitDefault] = useState(false);
  const [skipStun, setSkipStun] = useState(false);
  const [compare, setCompare] = useState(false);
  const [busy, setBusy] = useState<"plan" | "apply" | "probe" | "headscale" | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();
  const [plan, setPlan] = useState<MeshDerpResult | undefined>();
  const [probe, setProbe] = useState<MeshDerpProbe | undefined>();
  const [hs, setHs] = useState<MeshHeadscaleDerp | undefined>();
  const [asking, setAsking] = useState(false);
  const parsed = regionsFromForms(forms);

  const edit = (i: number, patch: Partial<RegionForm>): void => {
    setForms((l) => l.map((f, n) => (n === i ? { ...f, ...patch } : f)));
    setPlan(undefined);
    setHs(undefined);
  };

  const clear = (): void => {
    setErr(undefined);
    setPlan(undefined);
    setProbe(undefined);
    setHs(undefined);
  };

  const doProbe = async (): Promise<void> => {
    if (!parsed.ok) return;
    clear();
    setBusy("probe");
    const r = await getApi().mesh({ op: "derp.probe", regions: parsed.regions, skipStun, compareOfficial: compare });
    setBusy(undefined);
    if (r.ok) setProbe(r.data);
    else setErr(r.error);
  };

  const doPreview = async (): Promise<void> => {
    if (!parsed.ok) return;
    clear();
    if (!viaPolicy) {
      setBusy("headscale");
      const r = await getApi().mesh({ op: "derp.headscale", regions: parsed.regions, omitDefault });
      setBusy(undefined);
      if (r.ok) setHs(r.data);
      else setErr(r.error);
      return;
    }
    setBusy("plan");
    const r = await getApi().mesh({ op: "derp.plan", regions: parsed.regions, omitDefault, apply: false, skipStun });
    setBusy(undefined);
    if (r.ok) setPlan(r.data);
    else setErr(r.error);
  };

  const doApply = async (): Promise<void> => {
    if (!parsed.ok || !plan) return;
    setBusy("apply");
    setErr(undefined);
    const r = await getApi().mesh({ op: "derp.plan", regions: parsed.regions, omitDefault, apply: true, skipStun, expectVersion: plan.plan.baseVersion });
    setBusy(undefined);
    setAsking(false);
    if (!r.ok) {
      setErr(r.error.code === "conflict" ? { ...r.error, message: t("mesh.policy.stale") } : r.error);
      if (r.error.code === "conflict") setPlan(undefined);
      return;
    }
    setPlan(r.data);
    ctx.toast(r.data.rolledBack ? t("mesh.derp.rolledBack") : t("mesh.derp.applied"), r.data.rolledBack);
  };

  const bad = !parsed.ok ? parsed : undefined;
  return (
    <div data-testid="mesh-derp">
      <section class="card">
        <h2>{t("mesh.derp.title")}</h2>
        <p class="hint">{t("mesh.derp.intro")}</p>
        {!viaPolicy && <Banner kind="warn" testid="mesh-derp-file-mode">{t("mesh.derp.fileMode")}</Banner>}
        {forms.map((f, i) => (
          <fieldset key={i} data-testid={`mesh-region-${i}`}>
            <legend>{t("mesh.derp.region", { n: i + 1 })}</legend>
            <div class="card-grid">
              <Field label={t("mesh.derp.code")}>
                <input type="text" value={f.code} onInput={(e) => edit(i, { code: (e.currentTarget as HTMLInputElement).value })} placeholder="lab" autocomplete="off" data-testid={`mesh-region-${i}-code`} />
              </Field>
              <Field label={t("mesh.derp.name")}>
                <input type="text" value={f.name} onInput={(e) => edit(i, { name: (e.currentTarget as HTMLInputElement).value })} placeholder="Lab" autocomplete="off" data-testid={`mesh-region-${i}-name`} />
              </Field>
              <Field label={t("mesh.derp.host")}>
                <input type="text" value={f.host} onInput={(e) => edit(i, { host: (e.currentTarget as HTMLInputElement).value })} placeholder="derp.example.com" autocomplete="off" data-testid={`mesh-region-${i}-host`} />
              </Field>
              <Field label="IPv4">
                <input type="text" value={f.ipv4} onInput={(e) => edit(i, { ipv4: (e.currentTarget as HTMLInputElement).value })} autocomplete="off" data-testid={`mesh-region-${i}-ipv4`} />
              </Field>
              <Field label="IPv6">
                <input type="text" value={f.ipv6} onInput={(e) => edit(i, { ipv6: (e.currentTarget as HTMLInputElement).value })} autocomplete="off" />
              </Field>
              <Field label={t("mesh.derp.derpPort")}>
                <input type="text" value={f.derpPort} onInput={(e) => edit(i, { derpPort: (e.currentTarget as HTMLInputElement).value })} placeholder="443" autocomplete="off" />
              </Field>
              <Field label={t("mesh.derp.stunPort")}>
                <input type="text" value={f.stunPort} onInput={(e) => edit(i, { stunPort: (e.currentTarget as HTMLInputElement).value })} placeholder="3478" autocomplete="off" />
              </Field>
              <Field label={t("mesh.derp.certName")}>
                <input type="text" value={f.certName} onInput={(e) => edit(i, { certName: (e.currentTarget as HTMLInputElement).value })} autocomplete="off" />
              </Field>
            </div>
            {forms.length > 1 && (
              <button type="button" class="btn small" onClick={() => setForms((l) => l.filter((_, n) => n !== i))}>
                {t("mesh.derp.removeRegion")}
              </button>
            )}
          </fieldset>
        ))}
        <div class="row">
          <button type="button" class="btn" disabled={forms.length >= MESH_REGIONS_MAX} onClick={() => setForms((l) => [...l, { ...EMPTY_REGION }])} data-testid="mesh-region-add">
            {t("mesh.derp.addRegion")}
          </button>
        </div>
        {bad && forms.some((f) => f.code !== "" || f.host !== "") && (
          <Banner kind="danger" testid="mesh-derp-invalid">
            {bad.problem === "duplicate" ? t("mesh.derp.duplicate") : t("mesh.derp.invalid", { n: (bad.row ?? 0) + 1 })}
          </Banner>
        )}
        <label class="check">
          <input type="checkbox" checked={omitDefault} onChange={(e) => (setOmitDefault((e.currentTarget as HTMLInputElement).checked), setPlan(undefined), setHs(undefined))} data-testid="mesh-derp-omit" />
          <span>
            <strong>{t("mesh.derp.omitDefault")}</strong>
            <br />
            <span class="hint">{t("mesh.derp.omitDefaultHint")}</span>
          </span>
        </label>
        <label class="check">
          <input type="checkbox" checked={skipStun} onChange={(e) => setSkipStun((e.currentTarget as HTMLInputElement).checked)} />
          <span>{t("mesh.derp.skipStun")}</span>
        </label>
        <label class="check">
          <input type="checkbox" checked={compare} onChange={(e) => setCompare((e.currentTarget as HTMLInputElement).checked)} data-testid="mesh-derp-compare" />
          <span>{t("mesh.derp.compare")}</span>
        </label>
        <div class="row">
          <button type="button" class="btn" disabled={!parsed.ok || busy !== undefined} onClick={() => void doProbe()} data-testid="mesh-derp-probe">
            {t("mesh.derp.probe")}
          </button>
          <button type="button" class="btn primary" disabled={!parsed.ok || busy !== undefined} onClick={() => void doPreview()} data-testid="mesh-derp-preview">
            {viaPolicy ? t("mesh.preview") : t("mesh.derp.generate")}
          </button>
        </div>
      </section>

      {err && <MeshError error={err} toast={ctx.toast} />}
      {probe && <ProbeView probe={probe} />}
      {hs && <HeadscaleView hs={hs} />}
      {plan && (
        <section class="card" data-testid="mesh-derp-plan">
          <h2>{t("mesh.derp.plan")}</h2>
          <PlanView plan={plan.plan} />
          {plan.probes.length > 0 && <Probes probes={plan.probes} />}
          {plan.rolledBack && <Banner kind="danger" testid="mesh-derp-rolledback">{t("mesh.derp.rolledBackBody")}</Banner>}
          {plan.applied && !plan.rolledBack && <Banner kind="ok" testid="mesh-derp-applied">{t("mesh.derp.appliedBody")}</Banner>}
          {!plan.applied && (
            <div class="row end">
              <button type="button" class="btn danger" disabled={!plan.plan.changed || !plan.plan.writable || busy !== undefined} onClick={() => setAsking(true)} data-testid="mesh-derp-apply">
                {t("mesh.plan.apply")}
              </button>
            </div>
          )}
        </section>
      )}
      <Admission ctx={ctx} />
      {asking && (
        <Confirm title={t("mesh.derp.confirm.title")} body={<p>{t("mesh.derp.confirm.body")}</p>} confirmLabel={t("mesh.plan.apply")} danger busy={busy === "apply"} onConfirm={() => void doApply()} onCancel={() => setAsking(false)} />
      )}
    </div>
  );
}

function Probes({ probes }: { probes: MeshProbe[] }): JSX.Element {
  const t = useT();
  return (
    <div data-testid="mesh-probes">
      {probes.map((p) => (
        <div key={p.host} data-testid={`mesh-probe-${p.host}`}>
          <p>
            <strong>{p.host}</strong> <Badge kind={p.ok ? "ok" : "danger"}>{p.ok ? t("mesh.derp.pass") : t("mesh.derp.fail")}</Badge>
          </p>
          <ul class="plain">
            {p.checks.map((c) => (
              <li key={c.name}>
                <Badge kind={c.ok ? "ok" : "danger"}>{c.name}</Badge> {c.detail}
                {c.latencyMs !== undefined && <span class="hint"> ({c.latencyMs} ms)</span>}
              </li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}

function ProbeView({ probe }: { probe: MeshDerpProbe }): JSX.Element {
  const t = useT();
  return (
    <section class="card" data-testid="mesh-derp-probe-result">
      <h2>{t("mesh.derp.probeResult")}</h2>
      {probe.warnings.map((w, i) => (
        <Banner key={i} kind="warn">
          {w}
        </Banner>
      ))}
      <Probes probes={probe.probes} />
      {probe.official.length > 0 && (
        <>
          <h3>{t("mesh.derp.official")}</h3>
          <ul class="plain" data-testid="mesh-derp-official">
            {probe.official.slice(0, 8).map((o) => (
              <li key={o.regionId}>
                {o.code} {o.host}: {o.error ? <Badge kind="warn">{o.error}</Badge> : `${o.latencyMs ?? "—"} ms`}
              </li>
            ))}
          </ul>
        </>
      )}
      {probe.compareError && <Banner kind="warn">{probe.compareError}</Banner>}
    </section>
  );
}

function HeadscaleView({ hs }: { hs: MeshHeadscaleDerp }): JSX.Element {
  const t = useT();
  return (
    <section class="card" data-testid="mesh-derp-headscale">
      <h2>{t("mesh.derp.headscale.title")}</h2>
      {hs.warnings.map((w, i) => (
        <Banner key={i} kind="warn">
          {w}
        </Banner>
      ))}
      <Field label={t("mesh.derp.headscale.file")}>
        <textarea class="editor" readOnly value={hs.file} data-testid="mesh-derp-hs-file" />
      </Field>
      <Field label={t("mesh.derp.headscale.snippet")}>
        <textarea class="editor" readOnly value={hs.snippet} data-testid="mesh-derp-hs-snippet" />
      </Field>
      <p class="hint">{t("mesh.derp.headscale.restart")}</p>
    </section>
  );
}

type SyncMode = "allowlist" | "remote";

/** Keeps the relay's admission list in step with the devices the control plane has authorised. */
function Admission({ ctx }: { ctx: MeshTabProps["ctx"] }): JSX.Element {
  const t = useT();
  const [mode, setMode] = useState<SyncMode>("allowlist");
  const [allowlist, setAllowlist] = useState("");
  const [url, setUrl] = useState("");
  const [tokenFile, setTokenFile] = useState("");
  const [allDevices, setAllDevices] = useState(false);
  const [busy, setBusy] = useState(false);
  const [asking, setAsking] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const [done, setDone] = useState<MeshSync | undefined>();

  const pick = async (kind: PickKind, set: (p: string) => void): Promise<void> => {
    const r = await getApi().pickPath(kind);
    if (r.ok && r.data.path) set(r.data.path);
  };
  const ready = mode === "allowlist" ? allowlist !== "" : /^https?:\/\//.test(url) && tokenFile !== "";

  const run = async (): Promise<void> => {
    setBusy(true);
    setErr(undefined);
    setDone(undefined);
    const r = await getApi().mesh({ op: "derp.sync", target: mode === "allowlist" ? { allowlist } : { url, tokenFile }, allDevices });
    setBusy(false);
    setAsking(false);
    if (r.ok) {
      setDone(r.data);
      ctx.toast(t("mesh.admission.done", { total: r.data.total }));
    } else setErr(r.error);
  };

  return (
    <section class="card" data-testid="mesh-admission">
      <h2>{t("mesh.admission.title")}</h2>
      <p class="hint">{t("mesh.admission.intro")}</p>
      <fieldset>
        <label class="check">
          <input type="radio" name="admission" checked={mode === "allowlist"} onChange={() => setMode("allowlist")} data-testid="mesh-admission-file" />
          <span>{t("mesh.admission.modeFile")}</span>
        </label>
        <label class="check">
          <input type="radio" name="admission" checked={mode === "remote"} onChange={() => setMode("remote")} data-testid="mesh-admission-remote" />
          <span>{t("mesh.admission.modeRemote")}</span>
        </label>
      </fieldset>
      {mode === "allowlist" ? (
        <Field label={t("mesh.admission.allowlist")}>
          <div class="row">
            <input type="text" readOnly value={allowlist} placeholder="—" aria-label={t("mesh.admission.allowlist")} data-testid="mesh-admission-allowlist" />
            <button type="button" class="btn" onClick={() => void pick("allowlistFile", setAllowlist)} data-testid="mesh-admission-pick-allowlist">
              {t("wizard.choose")}
            </button>
          </div>
        </Field>
      ) : (
        <>
          <Field label={t("mesh.admission.url")} hint={t("mesh.admission.urlHint")}>
            <input type="text" value={url} onInput={(e) => setUrl((e.currentTarget as HTMLInputElement).value)} placeholder="https://derp.example.com:8480" autocomplete="off" data-testid="mesh-admission-url" />
          </Field>
          <Field label={t("mesh.admission.token")} hint={t("mesh.admission.tokenHint")}>
            <div class="row">
              <input type="text" readOnly value={tokenFile} placeholder="—" aria-label={t("mesh.admission.token")} data-testid="mesh-admission-tokenfile" />
              <button type="button" class="btn" onClick={() => void pick("tokenFile", setTokenFile)} data-testid="mesh-admission-pick-token">
                {t("wizard.choose")}
              </button>
            </div>
          </Field>
        </>
      )}
      <label class="check">
        <input type="checkbox" checked={allDevices} onChange={(e) => setAllDevices((e.currentTarget as HTMLInputElement).checked)} data-testid="mesh-admission-all" />
        <span>
          <strong>{t("mesh.admission.all")}</strong>
          <br />
          <span class="hint">{t("mesh.admission.allHint")}</span>
        </span>
      </label>
      {err && <MeshError error={err} toast={ctx.toast} />}
      <div class="row">
        <button type="button" class="btn primary" disabled={!ready || busy} onClick={() => setAsking(true)} data-testid="mesh-admission-sync">
          {t("mesh.admission.sync")}
        </button>
      </div>
      {done && (
        <Banner kind="ok" testid="mesh-admission-result">
          {t("mesh.admission.result", { total: done.total, added: done.added, removed: done.removed })}
        </Banner>
      )}
      {asking && (
        <Confirm title={t("mesh.admission.confirm.title")} body={<p>{t("mesh.admission.confirm.body")}</p>} confirmLabel={t("mesh.admission.sync")} busy={busy} onConfirm={() => void run()} onCancel={() => setAsking(false)} />
      )}
    </section>
  );
}
