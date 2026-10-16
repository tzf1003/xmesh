import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { getApi } from "../../api";
import { Badge, Banner, Confirm, Empty, Field, ResourceView } from "../../components";
import { useResource } from "../../hooks";
import { useT } from "../../i18n";
import { formatTime } from "../../lib/format";
import { ownerPlaceholder } from "../../lib/mesh";
import type { ActionError } from "../../../shared/ipc";
import { MESH_TEMPLATES, type MeshPlanResult, type MeshSnapshot, type MeshTemplate } from "../../../shared/mesh";
import type { MeshTabProps } from "../Mesh";
import { MeshError, PlanView } from "./common";

type Source = { kind: "template"; template: MeshTemplate; owner: string; port?: number } | { kind: "rollback"; snapshot: string };

/**
 * Policy changes always go preview -> confirm -> write. The write carries the
 * version the preview was made against, so a policy edited elsewhere in the
 * meantime is refused instead of overwritten; every write keeps a snapshot
 * that can be restored from this page.
 */
export function MeshPolicy({ ctx, status, reloadStatus }: MeshTabProps): JSX.Element {
  const t = useT();
  const writable = status.capabilities.policyWrite;
  const policy = useResource(() => getApi().mesh({ op: "policy.show" }), [ctx.snapshot.selectedInstance]);
  const snaps = useResource(() => getApi().mesh({ op: "policy.snapshots" }), [ctx.snapshot.selectedInstance]);
  const [template, setTemplate] = useState<MeshTemplate>("personal");
  const [owner, setOwner] = useState("");
  const [port, setPort] = useState("");
  const [source, setSource] = useState<Source | undefined>();
  const [plan, setPlan] = useState<MeshPlanResult | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();
  const [busy, setBusy] = useState(false);
  const [asking, setAsking] = useState(false);

  const effectiveOwner = owner.trim() === "" ? ownerPlaceholder(status.provider) : owner.trim();
  const portNum = port.trim() === "" ? undefined : Number(port.trim());
  const portOk = port.trim() === "" || (/^\d{1,5}$/.test(port.trim()) && portNum! >= 1 && portNum! <= 65535);

  const request = (src: Source, apply: boolean, expectVersion?: string) =>
    src.kind === "template"
      ? getApi().mesh({
          op: "policy.template",
          template: src.template,
          owner: src.owner,
          ...(src.port !== undefined && { port: src.port }),
          apply,
          ...(expectVersion !== undefined && { expectVersion }),
        })
      : getApi().mesh({ op: "policy.rollback", snapshot: src.snapshot, apply, ...(expectVersion !== undefined && { expectVersion }) });

  const preview = async (src: Source): Promise<void> => {
    setBusy(true);
    setErr(undefined);
    setPlan(undefined);
    const r = await request(src, false);
    setBusy(false);
    if (r.ok) {
      setSource(src);
      setPlan(r.data);
    } else setErr(r.error);
  };

  const apply = async (): Promise<void> => {
    if (!source || !plan) return;
    setBusy(true);
    const r = await request(source, true, plan.plan.baseVersion);
    setBusy(false);
    setAsking(false);
    if (!r.ok) {
      setErr(r.error);
      // A refused write means the policy moved on; the old preview must not be applied again.
      if (r.error.code === "conflict") setPlan(undefined);
      return;
    }
    ctx.toast(t("mesh.policy.applied"));
    setPlan(undefined);
    setSource(undefined);
    setErr(undefined);
    policy.reload();
    snaps.reload();
    reloadStatus();
  };

  return (
    <div data-testid="mesh-policy">
      <section class="card">
        <h2>{t("mesh.policy.current")}</h2>
        <ResourceView resource={policy.resource}>
          {(p) => (
            <>
              <p class="hint">
                {t("mesh.policy.version", { version: p.version || "—" })}
                {p.managedSections.length > 0 && <> · {t("mesh.policy.managed", { sections: p.managedSections.join(", ") })}</>}
              </p>
              {p.exists ? (
                <textarea class="editor" readOnly value={p.text} aria-label={t("mesh.policy.current")} data-testid="mesh-policy-text" />
              ) : (
                <Empty>{t("mesh.policy.none")}</Empty>
              )}
            </>
          )}
        </ResourceView>
        {!writable && <Banner kind="warn" testid="mesh-policy-readonly">{t("mesh.policy.fileMode")}</Banner>}
      </section>

      <section class="card">
        <h2>{t("mesh.policy.template.title")}</h2>
        <p class="hint">{t("mesh.policy.template.intro")}</p>
        <Field label={t("mesh.policy.template.pick")}>
          <select value={template} onChange={(e) => setTemplate((e.currentTarget as HTMLSelectElement).value as MeshTemplate)} data-testid="mesh-template">
            {MESH_TEMPLATES.map((x) => (
              <option key={x} value={x}>
                {t(`mesh.template.${x}` as const)}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t("mesh.policy.owner")} hint={t("mesh.policy.ownerHint")}>
          <input type="text" value={owner} placeholder={ownerPlaceholder(status.provider)} onInput={(e) => setOwner((e.currentTarget as HTMLInputElement).value)} autocomplete="off" data-testid="mesh-owner" />
        </Field>
        <Field label={t("mesh.policy.port")} hint={t("mesh.policy.portHint")}>
          <input type="text" value={port} placeholder="7433" onInput={(e) => setPort((e.currentTarget as HTMLInputElement).value)} aria-invalid={!portOk} autocomplete="off" data-testid="mesh-port" />
        </Field>
        <div class="row">
          <button
            type="button"
            class="btn primary"
            disabled={busy || !portOk}
            onClick={() => void preview({ kind: "template", template, owner: effectiveOwner, ...(portNum !== undefined && { port: portNum }) })}
            data-testid="mesh-template-preview"
          >
            {t("mesh.preview")}
          </button>
        </div>
      </section>

      {err && <MeshError error={err.code === "conflict" ? { ...err, message: t("mesh.policy.stale") } : err} toast={ctx.toast} />}
      {plan && (
        <section class="card" data-testid="mesh-policy-plan">
          <h2>{t("mesh.policy.plan", { label: plan.plan.label })}</h2>
          <PlanView plan={plan.plan} />
          <div class="row end">
            <button type="button" class="btn" onClick={() => setPlan(undefined)} data-testid="mesh-plan-discard">
              {t("mesh.plan.discard")}
            </button>
            <button type="button" class="btn danger" disabled={!plan.plan.changed || !plan.plan.writable || busy} onClick={() => setAsking(true)} data-testid="mesh-plan-apply">
              {t("mesh.plan.apply")}
            </button>
          </div>
        </section>
      )}

      <section class="card" data-testid="mesh-snapshots">
        <h2>{t("mesh.snapshots.title")}</h2>
        <p class="hint">{t("mesh.snapshots.hint")}</p>
        <ResourceView resource={snaps.resource}>
          {(d) =>
            d.snapshots.length === 0 ? (
              <Empty>{t("mesh.snapshots.none")}</Empty>
            ) : (
              <div class="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th scope="col">{t("mesh.snapshots.time")}</th>
                      <th scope="col">{t("mesh.snapshots.label")}</th>
                      <th scope="col">{t("mesh.snapshots.id")}</th>
                      <th scope="col" />
                    </tr>
                  </thead>
                  <tbody>
                    {[...d.snapshots].reverse().map((s: MeshSnapshot) => (
                      <tr key={s.id} data-testid={`mesh-snapshot-${s.id}`}>
                        <td>{formatTime(s.time)}</td>
                        <td>{s.label}</td>
                        <td class="mono">
                          <Badge>{s.id}</Badge>
                        </td>
                        <td>
                          <button type="button" class="btn small" disabled={busy || !writable} onClick={() => void preview({ kind: "rollback", snapshot: s.id })} data-testid={`mesh-rollback-${s.id}`}>
                            {t("mesh.snapshots.rollback")}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </ResourceView>
      </section>

      {asking && plan && (
        <Confirm
          title={t("mesh.policy.confirm.title")}
          body={<p>{t("mesh.policy.confirm.body", { label: plan.plan.label })}</p>}
          confirmLabel={t("mesh.plan.apply")}
          danger
          busy={busy}
          onConfirm={() => void apply()}
          onCancel={() => setAsking(false)}
        />
      )}
    </div>
  );
}
