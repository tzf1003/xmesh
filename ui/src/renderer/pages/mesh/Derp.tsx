import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { getApi } from "../../api";
import { Badge, Banner, Confirm, Field } from "../../components";
import { useT } from "../../i18n";
import { EMPTY_REGION, regionsFromForms, type RegionForm } from "../../lib/mesh";
import type { ActionError, PickKind } from "../../../shared/ipc";
import { MESH_REGIONS_MAX, type MeshDerpProbe, type MeshHeadscaleDerp, type MeshProbe, type MeshSync } from "../../../shared/mesh";
import type { MeshPolicyProps } from "../Mesh";
import { MeshError } from "./common";

/**
 * DERP changes edit the shared policy document. Headscale takes its relay map
 * from server configuration, for which this page renders the file and snippet.
 */
export function MeshDerp({ ctx, status, draft }: MeshPolicyProps): JSX.Element {
  const t = useT();
  const viaPolicy = status.capabilities.derpViaPolicy;
  const [forms, setForms] = useState<RegionForm[]>([{ ...EMPTY_REGION }]);
  const [omitDefault, setOmitDefault] = useState(draft.document?.omitDefault ?? false);
  const [regionEdit, setRegionEdit] = useState<{ id: number; text: string }>();
  const [removeID, setRemoveID] = useState<number>();
  useEffect(() => setOmitDefault(draft.document?.omitDefault ?? false), [draft.document]);
  const [skipStun, setSkipStun] = useState(false);
  const [compare, setCompare] = useState(false);
  const [busy, setBusy] = useState<"plan" | "probe" | "headscale" | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();

  const [probe, setProbe] = useState<MeshDerpProbe | undefined>();
  const [hs, setHs] = useState<MeshHeadscaleDerp | undefined>();

  const parsed = regionsFromForms(forms);

  const edit = (i: number, patch: Partial<RegionForm>): void => {
    setForms((l) => l.map((f, n) => (n === i ? { ...f, ...patch } : f)));
    setHs(undefined);
  };

  const clear = (): void => {
    setErr(undefined);
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
    const saved = await draft.edit({ regions: parsed.regions });
    setBusy(undefined);
    if (saved) { setForms([{ ...EMPTY_REGION }]); ctx.toast(t("mesh.draft.updated")); }
  };

  const bad = !parsed.ok ? parsed : undefined;
  return (
    <div class="section-stack" data-testid="mesh-derp">
      <section class="card">
        <h2>{t("mesh.derp.title")}</h2>
        <p class="hint">{t(viaPolicy ? "mesh.draft.derpHint" : "mesh.derp.intro")}</p>
        {viaPolicy && <section class="section-stack module-relay-grid" data-testid="mesh-derp-current">
          <h3>{t("mesh.draft.relays")}</h3>
          {draft.document?.regions.length === 0 && <p>{t("mesh.draft.noRelays")}</p>}
          {draft.document?.regions.map((r) => <div class="card" key={r.id} data-testid={`mesh-cloud-region-${r.id}`}>
            <strong>{r.id} · {r.name} ({r.code})</strong><p class="mono">{r.hosts.join(", ")}</p>
            <div class="row">
              <button type="button" class="btn" disabled={draft.busy} onClick={() => setRegionEdit({ id: r.id, text: r.text })} data-testid={`mesh-cloud-region-${r.id}-edit`}>{t("mesh.draft.editRegion")}</button>
              <button type="button" class="btn danger" disabled={draft.busy} onClick={() => setRemoveID(r.id)} data-testid={`mesh-cloud-region-${r.id}-remove`}>{t("mesh.derp.removeRegion")}</button>
            </div>
          </div>)}
          {regionEdit && <div class="card" data-testid="mesh-cloud-region-editor">
            <p class="hint">{t("mesh.draft.regionHint")}</p>
            <textarea class="editor" value={regionEdit.text} disabled={draft.busy} onInput={(e) => setRegionEdit({ ...regionEdit, text: (e.currentTarget as HTMLTextAreaElement).value })} data-testid="mesh-cloud-region-text" />
            <div class="row">
              <button type="button" class="btn primary" disabled={draft.busy} onClick={() => void draft.edit({ region: regionEdit }).then((ok) => { if (ok) setRegionEdit(undefined); })} data-testid="mesh-cloud-region-save">{t("mesh.draft.update")}</button>
              <button type="button" class="btn" disabled={draft.busy} onClick={() => setRegionEdit(undefined)}>{t("common.cancel")}</button>
            </div>
          </div>}
        </section>}
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
          <input type="checkbox" checked={omitDefault} disabled={draft.busy} onChange={(e) => { const checked = (e.currentTarget as HTMLInputElement).checked; if (viaPolicy) void draft.edit({ omitDefault: checked }); else setOmitDefault(checked); setHs(undefined); }} data-testid="mesh-derp-omit" />
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
          <button type="button" class="btn" disabled={!parsed.ok || busy !== undefined || draft.busy || (viaPolicy && draft.error !== undefined)} onClick={() => void doProbe()} data-testid="mesh-derp-probe">
            {t("mesh.derp.probe")}
          </button>
          <button type="button" class="btn primary" disabled={!parsed.ok || busy !== undefined || draft.busy || (viaPolicy && draft.error !== undefined)} onClick={() => void doPreview()} data-testid="mesh-derp-preview">
            {viaPolicy ? t("mesh.draft.addRelays") : t("mesh.derp.generate")}
          </button>
        </div>
      </section>

      {err && <MeshError error={err} toast={ctx.toast} />}
      {probe && <ProbeView probe={probe} />}
      {hs && <HeadscaleView hs={hs} />}
      {removeID !== undefined && <Confirm title={t("mesh.derp.removeRegion")} body={<p>{t("mesh.draft.removeHint")}</p>} confirmLabel={t("mesh.draft.update")} danger busy={draft.busy} onCancel={() => setRemoveID(undefined)} onConfirm={() => void draft.edit({ removeID }).then((ok) => { if (ok) setRemoveID(undefined); })} />}
      <Admission ctx={ctx} />
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
function Admission({ ctx }: { ctx: MeshPolicyProps["ctx"] }): JSX.Element {
  const t = useT();
  const [asking, setAsking] = useState(false);
  const [mode, setMode] = useState<SyncMode>("allowlist");
  const [allowlist, setAllowlist] = useState("");
  const [url, setUrl] = useState("");
  // Either a pasted token (kept only until submit) or the path of a file that holds it.
  const [token, setToken] = useState("");
  const [tokenFile, setTokenFile] = useState("");
  const [allDevices, setAllDevices] = useState(false);
  const [busy, setBusy] = useState(false);

  const [err, setErr] = useState<ActionError | undefined>();
  const [done, setDone] = useState<MeshSync | undefined>();

  const pick = async (kind: PickKind, set: (p: string) => void): Promise<void> => {
    const r = await getApi().pickPath(kind);
    if (r.ok && r.data.path) set(r.data.path);
  };
  const ready = mode === "allowlist" ? allowlist !== "" : /^https?:\/\//.test(url) && (token !== "" || tokenFile !== "");

  const run = async (): Promise<void> => {
    setBusy(true);
    setErr(undefined);
    setDone(undefined);
    const r = await getApi().mesh({
      op: "derp.sync",
      target: mode === "allowlist" ? { allowlist } : token !== "" ? { url, token } : { url, tokenFile },
      allDevices,
    });
    setToken("");
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
        <Field label={t("mesh.admission.allowlist")} hint={t("mesh.admission.allowlistHint")}>
          <div class="row">
            <input
              type="text"
              value={allowlist}
              onInput={(e) => setAllowlist((e.currentTarget as HTMLInputElement).value)}
              placeholder="/etc/derp/allow.json"
              aria-label={t("mesh.admission.allowlist")}
              autocomplete="off"
              spellcheck={false}
              data-testid="mesh-admission-allowlist"
            />
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
            <input
              type="password"
              value={token}
              onInput={(e) => setToken((e.currentTarget as HTMLInputElement).value)}
              placeholder={t("mesh.admission.tokenPlaceholder")}
              autocomplete="off"
              spellcheck={false}
              data-testid="mesh-admission-token"
            />
          </Field>
          <Field label={t("mesh.admission.tokenFile")} hint={t("mesh.admission.tokenFileHint")}>
            <div class="row">
              <input
                type="text"
                value={tokenFile}
                onInput={(e) => setTokenFile((e.currentTarget as HTMLInputElement).value)}
                placeholder="—"
                aria-label={t("mesh.admission.tokenFile")}
                autocomplete="off"
                spellcheck={false}
                data-testid="mesh-admission-tokenfile"
              />
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
