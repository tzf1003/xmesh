import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import { getApi } from "../../api";
import { Banner, Confirm, Dialog, Empty, Field, ResourceView } from "../../components";
import { useResource } from "../../hooks";
import { useT } from "../../i18n";
import { formatTime } from "../../lib/format";
import { ownerPlaceholder } from "../../lib/mesh";
import type { ActionError } from "../../../shared/ipc";
import { MESH_TEMPLATES, type MeshPlanResult, type MeshTemplate } from "../../../shared/mesh";
import type { MeshPolicyProps } from "../Mesh";
import { MeshError, PlanView } from "./common";

/** Only the reviewed full document is published; every editor shares draft.text. */
export function MeshPolicyPublish({ ctx, status, reloadStatus, draft, statusLabel, children }: MeshPolicyProps & { statusLabel: JSX.Element; children: JSX.Element }): JSX.Element {
  const t = useT();
  const [plan, setPlan] = useState<MeshPlanResult>();
  const [error, setError] = useState<ActionError>();
  const [busy, setBusy] = useState(false);
  useEffect(() => { setPlan(undefined); }, [draft.text, draft.base?.version]);
  const blocked = busy || draft.busy || draft.error !== undefined;

  const preview = async (): Promise<void> => {
    if (!draft.base || !draft.dirty) return;
    setBusy(true); setError(undefined); setPlan(undefined); draft.lock(true);
    const text = draft.text, version = draft.base.version;
    const r = await getApi().mesh({ op: "policy.publish", text, expectVersion: version, apply: false });
    draft.lock(false); setBusy(false);
    if (!draft.isCurrent(text, version)) return;
    if (r.ok) setPlan(r.data); else setError(r.error);
  };
  const apply = async (): Promise<void> => {
    if (!plan || plan.plan.after !== draft.text) return;
    const version = plan.plan.baseExists ? plan.plan.baseVersion : "absent";
    if (draft.base?.version !== version) return;
    setBusy(true); setError(undefined); draft.lock(true);
    const r = await getApi().mesh({ op: "policy.publish", text: plan.plan.after, expectVersion: version, apply: true });
    setPlan(undefined);
    if (!r.ok) { draft.lock(false); setBusy(false); setError(r.error); return; }
    const loaded = await draft.load();
    draft.lock(false); setBusy(false); reloadStatus();
    if (loaded) ctx.toast(t("mesh.policy.applied"));
  };
  return <>
    <section class="card module-policy-workspace" data-testid="mesh-policy-workspace">
      <p>{t(status.capabilities.policyWrite ? "mesh.draft.intro" : "mesh.draft.fileIntro")}</p>
      <div class="row module-policy-toolbar">
        {statusLabel}
        <div class="row module-policy-actions">
          {children}
          <button type="button" class="btn primary" disabled={blocked || !draft.dirty} onClick={() => void preview()} data-testid="mesh-draft-preview">{t(status.capabilities.policyWrite ? "mesh.draft.apply" : "mesh.draft.preview")}</button>
        </div>
      </div>
      {draft.busy && <p role="status">{t("common.loading")}</p>}
      {draft.error && <MeshError error={draft.error} toast={ctx.toast} />}
      {error && <MeshError error={error.code === "conflict" ? { ...error, message: t("mesh.draft.conflict") } : error} toast={ctx.toast} />}
    </section>
    {plan && <div class="module-policy-preview"><Dialog title={t("mesh.draft.preview")} testid="mesh-policy-plan" onClose={() => { if (!busy) setPlan(undefined); }}>
      <div class="module-policy-preview-body">
        <PlanView plan={plan.plan} />
        {plan.plan.writable && <p class="hint">{t("mesh.draft.saveHint")}</p>}
      </div>
      <div class="row end module-policy-preview-actions">
        <button type="button" class="btn" disabled={busy} onClick={() => setPlan(undefined)} data-testid="mesh-plan-cancel">{t("common.cancel")}</button>
        <button type="button" class="btn primary" disabled={blocked || !plan.plan.changed || !plan.plan.writable || status.credential?.access === "read"} onClick={() => void apply()} data-testid="mesh-plan-apply">{t("mesh.draft.save")}</button>
      </div>
    </Dialog></div>}
  </>;
}

export function MeshPolicy({ ctx, status, draft }: MeshPolicyProps): JSX.Element {
  const t = useT();
  const snaps = useResource(() => getApi().mesh({ op: "policy.snapshots" }), [ctx.snapshot.selectedInstance, draft.base?.version]);
  const [template, setTemplate] = useState<MeshTemplate>("personal");
  const [owner, setOwner] = useState("");
  const [port, setPort] = useState("");
  const [error, setError] = useState<ActionError>();
  const [busy, setBusy] = useState(false);
  const [rollback, setRollback] = useState<string>();
  const blocked = busy || draft.busy || draft.error !== undefined;
  const portNum = port.trim() === "" ? undefined : Number(port.trim());
  const portOk = port.trim() === "" || (/^\d{1,5}$/.test(port.trim()) && portNum! >= 1 && portNum! <= 65535);
  const restore = async (snapshot: string): Promise<void> => {
    setRollback(undefined); setBusy(true); setError(undefined); draft.lock(true);
    const r = await getApi().mesh({ op: "policy.rollback", snapshot, apply: false });
    draft.lock(false);
    if (!r.ok) { setBusy(false); setError(r.error); return; }
    await draft.edit({ text: r.data.plan.after });
    setBusy(false);
  };
  return <div class="section-stack" data-testid="mesh-policy">
    <section class="card">
      <h2>{t("mesh.draft.title")}</h2>
      <p class="hint">{t("mesh.policy.version", { version: draft.base?.exists ? draft.base.version : "—" })}</p>
      <textarea class="editor" value={draft.text} disabled={busy || draft.busy} onInput={(e) => draft.writeText((e.currentTarget as HTMLTextAreaElement).value)} aria-label={t("mesh.draft.title")} data-testid="mesh-policy-text" />
      <div class="row">
        <button type="button" class="btn" disabled={busy || draft.busy} onClick={() => void draft.edit({})} data-testid="mesh-draft-check">{t("mesh.draft.check")}</button>
      </div>
      {!status.capabilities.policyWrite && <Banner kind="warn" testid="mesh-policy-readonly">{t("mesh.policy.fileMode")}</Banner>}
    </section>
    <section class="card">
      <h2>{t("mesh.policy.template.title")}</h2>
      <p class="hint">{t("mesh.draft.templateHint")}</p>
      <Field label={t("mesh.policy.template.pick")}><select value={template} onChange={(e) => setTemplate((e.currentTarget as HTMLSelectElement).value as MeshTemplate)} data-testid="mesh-template">{MESH_TEMPLATES.map((v) => <option key={v} value={v}>{t(`mesh.template.${v}`)}</option>)}</select></Field>
      <Field label={t("mesh.policy.owner")} hint={t("mesh.policy.ownerHint")}><input type="text" value={owner} placeholder={ownerPlaceholder(status.provider)} onInput={(e) => setOwner((e.currentTarget as HTMLInputElement).value)} data-testid="mesh-owner" /></Field>
      <Field label={t("mesh.policy.port")} hint={t("mesh.policy.portHint")}><input type="text" value={port} placeholder="7433" onInput={(e) => setPort((e.currentTarget as HTMLInputElement).value)} aria-invalid={!portOk} data-testid="mesh-port" /></Field>
      <button type="button" class="btn" disabled={blocked || !portOk} onClick={() => void draft.edit({ template, owner: owner.trim() || ownerPlaceholder(status.provider), ...(portNum !== undefined && { port: portNum }) })} data-testid="mesh-template-preview">{t("mesh.draft.update")}</button>
    </section>
    {error && <MeshError error={error.code === "conflict" ? { ...error, message: t("mesh.draft.conflict") } : error} toast={ctx.toast} />}
    <section class="card" data-testid="mesh-snapshots">
      <h2>{t("mesh.snapshots.title")}</h2><p class="hint">{t("mesh.draft.snapshotHint")}</p>
      <ResourceView resource={snaps.resource}>{(d) => d.snapshots.length === 0 ? <Empty>{t("mesh.snapshots.none")}</Empty> : <div class="table-wrap"><table><thead><tr><th>{t("mesh.snapshots.time")}</th><th>{t("mesh.snapshots.label")}</th><th>{t("mesh.snapshots.id")}</th><th /></tr></thead><tbody>{[...d.snapshots].reverse().map((s) => <tr key={s.id} data-testid={`mesh-snapshot-${s.id}`}><td>{s.time ? formatTime(s.time) : "—"}</td><td>{s.label}</td><td class="mono">{s.id}</td><td><button type="button" class="btn small" disabled={blocked} onClick={() => setRollback(s.id)} data-testid={`mesh-rollback-${s.id}`}>{t("mesh.draft.restore")}</button></td></tr>)}</tbody></table></div>}</ResourceView>
    </section>
    {rollback && <Confirm title={t("mesh.draft.restore")} body={<p>{t("mesh.draft.restoreHint")}</p>} confirmLabel={t("mesh.draft.update")} onConfirm={() => void restore(rollback)} onCancel={() => setRollback(undefined)} />}
  </div>;
}
