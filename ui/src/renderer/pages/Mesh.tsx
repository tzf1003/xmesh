import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Dialog, Empty, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { useT } from "../i18n";
import { formatTime } from "../lib/format";
import { parseTagText } from "../lib/mesh";
import { usePolicyDraft, type PolicyDraft, type SavedPolicyDraft } from "../lib/policy-draft";
import type { ActionError } from "../../shared/ipc";
import type { MeshDeviceReport, MeshStatus, MeshTagPlan } from "../../shared/mesh";
import type { Key } from "../messages";
import { MeshError } from "./mesh/common";
import { MeshDerp } from "./mesh/Derp";
import { MeshLogin } from "./mesh/Login";
import { MeshMatrix } from "./mesh/Matrix";
import { MeshOnboard } from "./mesh/Onboard";
import { MeshPolicy } from "./mesh/Policy";

const TABS = [
  { id: "devices", label: "mesh.tab.devices" },
  { id: "onboard", label: "mesh.tab.onboard" },
  { id: "policy", label: "mesh.tab.policy" },
  { id: "matrix", label: "mesh.tab.matrix" },
  { id: "derp", label: "mesh.tab.derp" },
] as const satisfies readonly { id: string; label: Key }[];
type TabId = (typeof TABS)[number]["id"];

/** Props every tab receives. `status` is the loaded device list; it also tells which control plane this is. */
export interface MeshTabProps {
  ctx: PageContext;
  status: MeshStatus;
  reloadStatus(): void;
}

export interface MeshPolicyProps extends MeshTabProps {
  draft: PolicyDraft;
  showPolicy(): void;
}

/**
 * The tailnet as seen by the control plane (Tailscale or Headscale), reached
 * through `xmesh mesh --json`. It works with or without a running kernel.
 */
export function Mesh({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const [tab, setTab] = useState<TabId>(ctx.focus?.meshTab ?? "devices");
  const [credentialRevision, setCredentialRevision] = useState(0);
  const savedDrafts = useRef(new Map<string, SavedPolicyDraft>());
  const [draftDirty, setDraftDirty] = useState(false);
  useEffect(() => { if (ctx.focus?.meshTab) setTab(ctx.focus.meshTab); }, [ctx.focus?.meshTab]);
  const { resource, reload } = useResource(() => getApi().mesh({ op: "status" }), [ctx.snapshot.selectedInstance]);
  // After saving a credential, ask for status once more: the answer says whether
  // the key works and which access level it carries.
  const verify = async (): Promise<ActionError | undefined> => {
    const r = await getApi().mesh({ op: "status" });
    return r.ok ? undefined : r.error;
  };

  return (
    <div class="page" data-testid="page-mesh">
      <PageHead title={t("nav.mesh")} sub={t("mesh.sub")}>
        <button type="button" class="btn" onClick={reload} data-testid="mesh-refresh">
          {t("common.refresh")}
        </button>
      </PageHead>
      <MeshLogin
        ctx={ctx}
        open={resource.state === "error" && (resource.error.code === "no_credential" || resource.error.code === "insufficient_scope")}
        onSaved={() => { setCredentialRevision((v) => v + 1); reload(); }}
        verify={verify}
        draftDirty={draftDirty}
      />
      {resource.state === "error" && resource.stale === undefined ? (
        <MeshError error={resource.error} toast={ctx.toast} />
      ) : (
        <ResourceView resource={resource}>
          {(status, stale) => (
            <>
              {stale && resource.state === "error" && <MeshError error={resource.error} toast={ctx.toast} />}
              <p data-testid="mesh-provider">
                {t("mesh.provider")}: <Badge kind="info">{status.provider}</Badge>{" "}
                {!status.capabilities.policyWrite && <Badge kind="warn">{t("mesh.policyFileMode")}</Badge>}{" "}
                {status.credential?.access === "read" && (
                  <span data-testid="mesh-readonly-credential">
                    <Badge kind="warn">{t("mesh.readOnlyCredential")}</Badge>
                  </span>
                )}
              </p>
              {status.credential?.source === "file" && status.credential.file !== undefined && (
                <p class="hint" data-testid="mesh-credential-file">
                  {t("mesh.credentialFile", { path: status.credential.file })}
                </p>
              )}
              <div class="tabs" role="tablist" aria-label={t("nav.mesh")}>
                {TABS.map((x) => (
                  <button
                    key={x.id}
                    type="button"
                    role="tab"
                    class="tab"
                    aria-selected={tab === x.id}
                    data-testid={`mesh-tab-${x.id}`}
                    onClick={() => setTab(x.id)}
                  >
                    {t(x.label)}
                  </button>
                ))}
              </div>
              <div role="tabpanel" data-testid={`mesh-panel-${tab}`}>
                {tab === "devices" && <MeshDevices ctx={ctx} status={status} reloadStatus={reload} />}
                {tab === "onboard" && <MeshOnboard ctx={ctx} status={status} reloadStatus={reload} />}
                {tab === "matrix" && <MeshMatrix ctx={ctx} status={status} reloadStatus={reload} />}
                <PolicyWorkspace key={credentialRevision} revision={credentialRevision} savedDrafts={savedDrafts.current} onDirty={setDraftDirty} ctx={ctx} status={status} reloadStatus={reload} tab={tab} showPolicy={() => setTab("policy")} />
              </div>
            </>
          )}
        </ResourceView>
      )}
    </div>
  );
}

function PolicyWorkspace(props: MeshTabProps & { tab: TabId; revision: number; savedDrafts: Map<string, SavedPolicyDraft>; onDirty(dirty: boolean): void; showPolicy(): void }): JSX.Element {
  const { ctx, status, tab } = props;
  // A local node selection does not change the control-plane credential.
  const draft = usePolicyDraft(`${props.revision}:${status.provider}:${status.credential?.file ?? "keychain"}`, props.savedDrafts);
  useEffect(() => props.onDirty(draft.dirty), [draft.dirty]);
  const t = useT();
  const [refreshing, setRefreshing] = useState(false);
  useEffect(() => { if (tab === "derp" && draft.document && draft.document.text !== draft.text) void draft.edit({}); }, [tab]);
  return <div hidden={tab !== "policy" && tab !== "derp"}>
    <section class="card" data-testid="mesh-policy-workspace">
      <p>{t("mesh.draft.intro")}</p>
      <div class="row">
        <Badge kind={draft.dirty ? "warn" : "info"}>{t(draft.dirty ? "mesh.draft.dirty" : "mesh.draft.synced")}</Badge>
        <button type="button" class="btn" disabled={draft.busy} onClick={() => draft.dirty ? setRefreshing(true) : void draft.load()} data-testid="mesh-policy-cloud-refresh">{t("mesh.draft.refresh")}</button>
        {tab === "derp" && <button type="button" class="btn primary" onClick={props.showPolicy} data-testid="mesh-derp-open-policy">{t("mesh.draft.review")}</button>}
      </div>
      {draft.busy && <p role="status">{t("common.loading")}</p>}
      {draft.error && <MeshError error={draft.error} toast={ctx.toast} />}
    </section>
    {draft.base && draft.document && <>
      <div hidden={tab !== "policy"}><MeshPolicy {...props} draft={draft} /></div>
      <div hidden={tab !== "derp"}><MeshDerp {...props} draft={draft} /></div>
    </>}
    {refreshing && <Confirm title={t("mesh.draft.refresh")} body={<p>{t("mesh.draft.discard")}</p>} confirmLabel={t("mesh.draft.refresh")} onCancel={() => setRefreshing(false)} onConfirm={() => { setRefreshing(false); void draft.load(); }} />}
  </div>;
}

function MeshDevices({ ctx, status, reloadStatus }: MeshTabProps): JSX.Element {
  const t = useT();
  const [editing, setEditing] = useState<MeshDeviceReport | undefined>();
  if (status.devices.length === 0) return <Empty>{t("mesh.devices.none")}</Empty>;
  return (
    <section class="card">
      <div class="table-wrap">
        <table data-testid="mesh-devices">
          <thead>
            <tr>
              <th scope="col">{t("col.name")}</th>
              <th scope="col">{t("col.status")}</th>
              <th scope="col">{t("col.tags")}</th>
              <th scope="col">{t("mesh.col.roles")}</th>
              <th scope="col">{t("mesh.col.issues")}</th>
              <th scope="col" />
            </tr>
          </thead>
          <tbody>
            {status.devices.map((r) => (
              <tr key={r.device.id} data-testid={`mesh-device-${r.device.id}`}>
                <td>
                  <strong>{r.device.name}</strong>
                  <br />
                  <span class="hint mono">{r.device.addresses.join(" ") || r.device.id}</span>
                  {r.device.lastSeen && !r.device.online && <div class="hint">{t("mesh.lastSeen", { time: formatTime(r.device.lastSeen) })}</div>}
                </td>
                <td>
                  <Badge kind={r.device.online ? "ok" : ""}>{r.device.online ? t("mesh.online") : t("mesh.offline")}</Badge>
                  {!r.device.authorized && <Badge kind="warn">{t("mesh.unauthorized")}</Badge>}
                </td>
                <td>
                  <div class="row">
                    {r.device.tags.length === 0 ? <span class="hint">{t("mesh.noTags")}</span> : r.device.tags.map((x) => <Badge key={x}>{x}</Badge>)}
                  </div>
                </td>
                <td>{r.roles.join(", ") || "—"}</td>
                <td>
                  {r.issues.length === 0 ? (
                    "—"
                  ) : (
                    <ul class="plain">
                      {r.issues.map((i, n) => (
                        <li key={n}>
                          <Badge kind={i.severity === "error" ? "danger" : i.severity === "warn" ? "warn" : ""}>{i.severity}</Badge> {i.message}
                        </li>
                      ))}
                    </ul>
                  )}
                </td>
                <td>
                  <button type="button" class="btn small" onClick={() => setEditing(r)} data-testid={`mesh-edit-tags-${r.device.id}`}>
                    {t("mesh.editTags")}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editing && (
        <TagEditor
          ctx={ctx}
          device={editing}
          onClose={() => setEditing(undefined)}
          onApplied={() => {
            setEditing(undefined);
            reloadStatus();
          }}
        />
      )}
    </section>
  );
}

/** Preview, then apply: the CLI compares the new tags with the policy's `tagOwners` before anything is written. */
function TagEditor({ ctx, device, onClose, onApplied }: { ctx: PageContext; device: MeshDeviceReport; onClose: () => void; onApplied: () => void }): JSX.Element {
  const t = useT();
  const [text, setText] = useState(device.device.tags.join(", "));
  const [plan, setPlan] = useState<MeshTagPlan | undefined>();
  const [previewOf, setPreviewOf] = useState("");
  const [err, setErr] = useState<ActionError | undefined>();
  const [busy, setBusy] = useState(false);
  const parsed = parseTagText(text, true);

  const run = async (apply: boolean): Promise<void> => {
    if (!parsed.ok) return;
    setBusy(true);
    setErr(undefined);
    const r = await getApi().mesh({ op: "tags", device: device.device.id, tags: parsed.tags, apply });
    setBusy(false);
    if (!r.ok) return setErr(r.error);
    if (apply) {
      ctx.toast(t("mesh.tags.saved", { name: device.device.name }));
      return onApplied();
    }
    setPlan(r.data);
    setPreviewOf(text);
  };

  const fresh = plan !== undefined && previewOf === text;
  const blocked = plan !== undefined && plan.missingTagOwners.length > 0;
  const unchanged = plan !== undefined && plan.from.join(",") === plan.to.join(",");
  return (
    <Dialog title={t("mesh.tags.title", { name: device.device.name })} onClose={onClose} testid="mesh-tags-dialog">
      <label class="field">
        <span>{t("mesh.tags.label")}</span>
        <input
          type="text"
          value={text}
          onInput={(e) => {
            setText((e.currentTarget as HTMLInputElement).value);
            setPlan(undefined);
          }}
          aria-invalid={!parsed.ok}
          autocomplete="off"
          data-testid="mesh-tags-input"
        />
        <span class="hint">{t("mesh.tags.hint")}</span>
      </label>
      {!parsed.ok && <Banner kind="danger" testid="mesh-tags-invalid">{t("mesh.tags.invalid")}</Banner>}
      {err && <MeshError error={err} toast={ctx.toast} />}
      {fresh && (
        <div data-testid="mesh-tags-plan">
          <p>
            {plan.from.length === 0 ? t("mesh.noTags") : plan.from.join(", ")} → <strong>{plan.to.length === 0 ? t("mesh.noTags") : plan.to.join(", ")}</strong>
          </p>
          {blocked && <Banner kind="danger" testid="mesh-tags-missing">{t("mesh.tags.missingOwners", { tags: plan.missingTagOwners.join(", ") })}</Banner>}
          {unchanged && <Banner testid="mesh-tags-unchanged">{t("mesh.tags.unchanged")}</Banner>}
        </div>
      )}
      <div class="row end" style={{ marginTop: "12px" }}>
        <button type="button" class="btn" onClick={onClose} data-testid="mesh-tags-cancel">
          {t("common.cancel")}
        </button>
        <button type="button" class="btn" disabled={!parsed.ok || busy} onClick={() => void run(false)} data-testid="mesh-tags-preview">
          {t("mesh.preview")}
        </button>
        <button type="button" class="btn primary" disabled={!fresh || blocked || unchanged || busy} onClick={() => void run(true)} data-testid="mesh-tags-apply">
          {t("mesh.tags.apply")}
        </button>
      </div>
    </Dialog>
  );
}
