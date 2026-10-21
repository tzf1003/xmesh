import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Dialog, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { useLocale, useT } from "../i18n";
import { Icon } from "../icons";
import { formatTime } from "../lib/format";
import { parseTagText } from "../lib/mesh";
import { meshIssueText } from "../lib/mesh-issues";
import { usePolicyDraft, type PolicyDraft, type SavedPolicyDraft } from "../lib/policy-draft";
import type { ActionError } from "../../shared/ipc";
import type { MeshDeviceReport, MeshStatus, MeshTagPlan } from "../../shared/mesh";
import type { Key } from "../messages";
import { MeshError } from "./mesh/common";
import { MeshDerp } from "./mesh/Derp";
import { MeshLogin } from "./mesh/Login";
import { MeshMatrix } from "./mesh/Matrix";
import { MeshOnboard } from "./mesh/Onboard";
import { MeshPolicy, MeshPolicyPublish } from "./mesh/Policy";
import { deviceNeedsAttention, filterMeshDevices, type MeshFilter } from "./module-model";
import { useModuleT } from "./module-messages";
import { ModuleBox, ModuleBreadcrumb, ModuleFacts, ModuleStat, ModuleSummary } from "./module-ui";
import "./module-redesign.css";

const TABS = [
  { id: "devices", label: "mesh.tab.devices" },
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
  const m = useModuleT();
  const initialTab = ctx.focus?.meshTab;
  const [tab, setTab] = useState<TabId>(initialTab === "onboard" ? "devices" : initialTab ?? "devices");
  const [onboarding, setOnboarding] = useState(initialTab === "onboard");
  const [loginOpen, setLoginOpen] = useState(false);
  const [deviceFilter, setDeviceFilter] = useState<MeshFilter>("all");
  const [deviceQuery, setDeviceQuery] = useState("");
  const [policyInfo, setPolicyInfo] = useState<{ version: string; regions: number }>();
  const [credentialRevision, setCredentialRevision] = useState(0);
  const savedDrafts = useRef(new Map<string, SavedPolicyDraft>());
  const [draftDirty, setDraftDirty] = useState(false);
  useEffect(() => { const next = ctx.focus?.meshTab; if (next === "onboard") setOnboarding(true); else if (next) setTab(next); }, [ctx.focus?.meshTab]);
  const instance=ctx.snapshot.selectedInstance;
  const currentInstance=useRef(instance);currentInstance.current=instance;
  const [verified,setVerified]=useState<{instance:string;status:MeshStatus}>();
  useEffect(()=>setVerified(undefined),[instance]);
  const { resource:loadedResource, reload:requestReload } = useResource(() => getApi().mesh({ op: "status" }), [ctx.snapshot.selectedInstance]);
  const resource=verified?.instance===instance ? {state:"ready" as const,data:verified.status} : loadedResource;
  const reload=():void=>{setVerified(undefined);requestReload();};
  // After saving a credential, ask for status once more: the answer says whether
  // the key works and which access level it carries.
  const verify = async (): Promise<ActionError | undefined> => {
    const r = await getApi().mesh({ op: "status" });
    if(currentInstance.current!==instance) return {code:"cancelled",message:t("mesh.verifyNodeChanged")};
    if(r.ok) {setVerified({instance,status:r.data});return undefined;}
    return r.error;
  };

  return (
    <div class="page module-page" data-testid="page-mesh">
      <ModuleBreadcrumb title={t("nav.mesh")}/>
      <PageHead title={t("nav.mesh")} sub={m("meshSub")}>
        <div class="module-heading-actions"><button type="button" class="btn" onClick={reload} data-testid="mesh-refresh">{t("common.refresh")}</button><button type="button" class="btn primary" onClick={() => setOnboarding(true)} disabled={resource.state !== "ready" && !(resource.state === "error" && resource.stale)} data-testid="mesh-tab-onboard"><Icon name="plus"/>{m("onboard")}</button></div>
      </PageHead>
      {loginOpen && <Dialog title={m("credentialTitle")} onClose={() => setLoginOpen(false)} testid="mesh-credentials-drawer" drawer><div class="module-drawer-content"><MeshLogin
        ctx={ctx}
        open
        onSaved={() => { setCredentialRevision((v) => v + 1); setVerified(undefined); }}
        verify={verify}
        draftDirty={draftDirty}
        onVerified={() => setLoginOpen(false)}
      /></div></Dialog>}
      {resource.state === "error" && resource.stale === undefined ? (
        <><MeshError error={resource.error} toast={ctx.toast}/><button type="button" class="btn primary" onClick={() => setLoginOpen(true)} data-testid="mesh-connect-credentials">{m("credentials")}</button></>
      ) : (
        <ResourceView resource={resource}>
          {(status, stale) => (
            <>
              {stale && resource.state === "error" && <MeshError error={resource.error} toast={ctx.toast} />}
              <ModuleSummary icon="network" title={<>{status.credential?.profile || status.provider}<span data-testid="mesh-provider"><Badge>{status.provider}</Badge>{!status.capabilities.policyWrite && <Badge kind="warn">{t("mesh.policyFileMode")}</Badge>}</span></>} detail={<><span class={`module-state ${stale ? "warn" : "ok"}`}>{m(stale ? "staleStatus" : "connected")}</span><span>·</span><span data-testid={status.credential?.access === "read" ? "mesh-readonly-credential" : undefined}>{m(status.credential?.access === "write" ? "write" : status.credential?.access === "read" ? "read" : "unknown")}</span><button type="button" class="module-link" onClick={() => setLoginOpen(true)}>{m("manage")}<Icon name="chevron"/></button></>}><ModuleStat value={status.devices.length} label={m("networkDevices")}/><ModuleStat value={status.devices.filter((report) => report.device.online).length} label={m("onlineDevices")} tone="ok"/><ModuleStat value={policyInfo?.regions ?? "—"} label={t("mesh.tab.derp")}/><ModuleStat value={status.devices.filter(deviceNeedsAttention).length} label={m("issueDevices")} tone={status.devices.some(deviceNeedsAttention) ? "warn" : ""}/></ModuleSummary>
              <div class="module-tabs" role="tablist" aria-label={t("nav.mesh")}>
                {TABS.map((x) => (
                  <button
                    key={x.id}
                    type="button"
                    role="tab"
                    class="tab"
                    aria-selected={tab === x.id}
                    aria-controls={`mesh-panel-${x.id}`}
                    id={`mesh-tab-${x.id}`}
                    data-testid={`mesh-tab-${x.id}`}
                    onClick={() => setTab(x.id)}
                  >
                    {x.id === "devices" ? m("meshDevices") : t(x.label)}{x.id === "devices" && <span>{status.devices.length}</span>}
                  </button>
                ))}
              </div>
              <div class="module-layout"><div class="module-main"><div class="section-stack" role="tabpanel" id={`mesh-panel-${tab}`} aria-labelledby={`mesh-tab-${tab}`} data-testid={`mesh-panel-${tab}`}>
                {tab === "devices" && <MeshDevices ctx={ctx} status={status} reloadStatus={reload} filter={deviceFilter} setFilter={setDeviceFilter} query={deviceQuery} setQuery={setDeviceQuery} />}
                {tab === "matrix" && <MeshMatrix ctx={ctx} status={status} reloadStatus={reload} />}
                <PolicyWorkspace key={credentialRevision} revision={credentialRevision} savedDrafts={savedDrafts.current} onDirty={setDraftDirty} onInfo={setPolicyInfo} ctx={ctx} status={status} reloadStatus={reload} tab={tab} showPolicy={() => setTab("policy")} />
              </div></div><aside class="module-side">
                <ModuleBox title={m("controlPlane")} icon="network"><div class="module-health"><Icon name="shield"/><div><strong>{status.provider === "tailscale" ? "Tailscale" : status.provider === "headscale" ? "Headscale" : status.provider}</strong><small>{status.credential?.profile || m("connected")}</small></div></div><ModuleFacts items={[[m("access"), <Badge kind={status.credential?.access === "write" ? "ok" : "warn"}>{m(status.credential?.access === "write" ? "write" : status.credential?.access === "read" ? "read" : "unknown")}</Badge>], [m("policyVersion"), policyInfo?.version || "—"], [m("authorizedDevices"), status.devices.filter((report) => report.device.authorized).length], [m("source"), m(status.credential?.source === "keychain" ? "keychain" : status.credential?.source === "file" ? "file" : "unknown")]]}/>{!status.capabilities.policyWrite && <p><Badge kind="warn">{t("mesh.policyFileMode")}</Badge></p>}{status.credential?.source === "file" && status.credential.file && <p class="hint" data-testid="mesh-credential-file">{t("mesh.credentialFile", { path: status.credential.file })}</p>}<button type="button" class="module-link" onClick={() => setLoginOpen(true)} data-testid="mesh-manage-credentials">{m("credentials")}<Icon name="arrow"/></button></ModuleBox>
                <ModuleBox title={m("checks")} icon="info"><p>{status.devices.some(deviceNeedsAttention) ? m("checksHint", { n: status.devices.filter(deviceNeedsAttention).length }) : m("checksClear")}</p>{status.devices.some(deviceNeedsAttention) && <button type="button" class="module-link" onClick={() => { setTab("devices"); setDeviceQuery(""); setDeviceFilter("attention"); }}>{m("showIssues")}<Icon name="arrow"/></button>}<div class="module-side-divider"><h3>{m("accessLayers")}</h3><p>{m("accessLayersHint")}</p><button type="button" class="module-link" onClick={() => setTab("matrix")}>{m("checkMatrix")}<Icon name="arrow"/></button></div></ModuleBox>
                <div class="module-note"><Icon name="info"/><div>{m("tailnetScope")}<br/><button type="button" class="module-link" onClick={() => ctx.go("devices")}>{m("devicesPage")}<Icon name="arrow"/></button></div></div>
              </aside></div>
              {onboarding && <Dialog title={m("onboard")} onClose={() => setOnboarding(false)} testid="mesh-onboard-drawer" drawer><div class="module-drawer-content"><MeshOnboard ctx={ctx} status={status} reloadStatus={reload}/></div></Dialog>}
            </>
          )}
        </ResourceView>
      )}
    </div>
  );
}

function PolicyWorkspace(props: MeshTabProps & { tab: TabId; revision: number; savedDrafts: Map<string, SavedPolicyDraft>; onDirty(dirty: boolean): void; onInfo(info: { version: string; regions: number } | undefined): void; showPolicy(): void }): JSX.Element {
  const { ctx, status, tab } = props;
  // A local node selection does not change the control-plane credential.
  const draft = usePolicyDraft(`${props.revision}:${status.provider}:${status.credential?.file ?? "keychain"}`, props.savedDrafts);
  useEffect(() => props.onDirty(draft.dirty), [draft.dirty]);
  useEffect(() => { if (!draft.dirty) props.onInfo(draft.base && draft.document ? { version: draft.base.exists ? draft.base.version : "—", regions: draft.document.regions.length } : undefined); }, [draft.base?.version, draft.document?.regions.length, draft.dirty]);
  const t = useT();
  const [refreshing, setRefreshing] = useState(false);
  useEffect(() => { if (tab === "derp" && draft.document && draft.document.text !== draft.text) void draft.edit({}); }, [tab]);
  return <div class="section-stack" hidden={tab !== "policy" && tab !== "derp"}>
    <MeshPolicyPublish {...props} draft={draft} statusLabel={<Badge kind={draft.dirty ? "warn" : "info"}>{t(draft.dirty ? "mesh.draft.dirty" : "mesh.draft.synced")}</Badge>}>
      <>
        {tab === "derp" && <button type="button" class="btn" onClick={props.showPolicy} data-testid="mesh-derp-open-policy">{t("mesh.draft.review")}</button>}
        <button type="button" class="btn" disabled={draft.busy} onClick={() => draft.dirty ? setRefreshing(true) : void draft.load()} data-testid="mesh-policy-cloud-refresh">{t("mesh.draft.refresh")}</button>
      </>
    </MeshPolicyPublish>
    {draft.base && draft.document && <>
      <div hidden={tab !== "policy"}><MeshPolicy {...props} draft={draft} /></div>
      <div hidden={tab !== "derp"}><MeshDerp {...props} draft={draft} /></div>
    </>}
    {refreshing && <Confirm title={t("mesh.draft.refresh")} body={<p>{t("mesh.draft.discard")}</p>} confirmLabel={t("mesh.draft.refresh")} onCancel={() => setRefreshing(false)} onConfirm={() => { setRefreshing(false); void draft.load(); }} />}
  </div>;
}

function MeshDevices({ ctx, status, reloadStatus, filter, setFilter, query, setQuery }: MeshTabProps & { filter: MeshFilter; setFilter(filter: MeshFilter): void; query: string; setQuery(query: string): void }): JSX.Element {
  const t = useT();
  const m = useModuleT();
  const locale = useLocale();
  const [editing, setEditing] = useState<MeshDeviceReport | undefined>();
  const filtered = filterMeshDevices(status.devices, query, filter);
  const issues = status.devices.filter(deviceNeedsAttention).length;
  return (
    <>
      <p class="hint" data-testid="mesh-network-tags-scope">{t("mesh.tags.scope")}</p>
      {issues > 0 && <div class="module-alert" data-testid="mesh-attention"><Icon name="info"/><span>{m("checksHint", { n: issues })}</span><button type="button" class="module-link" onClick={() => { setQuery(""); setFilter(filter === "attention" ? "all" : "attention"); }}>{m(filter === "attention" ? "allDevices" : "showIssues")}<Icon name="arrow"/></button></div>}
      <div class="module-toolbar"><label class="module-search"><Icon name="search"/><input type="search" value={query} onInput={(event) => setQuery(event.currentTarget.value)} placeholder={m("searchDevices")} aria-label={m("searchDevices")} data-testid="mesh-search"/></label><select value={filter} onChange={(event) => setFilter(event.currentTarget.value as MeshFilter)} aria-label={m("allDevices")} data-testid="mesh-device-filter"><option value="all">{m("allDevices")}</option><option value="online">{t("mesh.online")}</option><option value="offline">{t("mesh.offline")}</option><option value="attention">{m("issueFilter")}</option></select></div>
      {status.devices.length === 0 ? <div class="module-empty"><span class="module-avatar"><Icon name="device"/></span><h2>{t("mesh.devices.none")}</h2><p>{m("emptyDevicesHint")}</p></div> : filtered.length === 0 ? <div class="module-empty"><h2>{m("noMatches")}</h2><button type="button" class="btn" onClick={() => { setQuery(""); setFilter("all"); }}>{m("reset")}</button></div> : <section class="card module-device-card">
      <div class="table-wrap">
        <table data-testid="mesh-devices">
          <colgroup><col class="module-device-col-name"/><col class="module-device-col-status"/><col class="module-device-col-tags"/><col/><col class="module-device-col-action"/></colgroup>
          <thead>
            <tr>
              <th scope="col">{m("deviceAddress")}</th>
              <th scope="col">{t("col.status")}</th>
              <th scope="col">{t("mesh.col.networkTags")}</th>
              <th scope="col">{t("mesh.col.roles")}</th>
              <th scope="col" />
            </tr>
          </thead>
            {filtered.map((r) => (
              <tbody key={r.device.id} data-testid={`mesh-device-${r.device.id}`}>
              <tr>
                <td>
                  <div class="module-device-name"><span class="module-repo-icon"><Icon name="device"/></span><div><strong>{r.device.name}</strong>
                  <span class="hint mono">{r.device.addresses.join(" ") || r.device.id}</span>
                  {r.device.lastSeen && !r.device.online && <div class="hint">{t("mesh.lastSeen", { time: formatTime(r.device.lastSeen) })}</div>}</div></div>
                </td>
                <td>
                  <span class={`module-state ${r.device.online ? "ok" : ""}`}>{r.device.online ? t("mesh.online") : t("mesh.offline")}</span>
                  {!r.device.authorized && <Badge kind="warn">{t("mesh.unauthorized")}</Badge>}
                </td>
                <td>
                  <div class="row">
                    {r.device.tags.length === 0 ? <span class="hint">{t("mesh.noTags")}</span> : r.device.tags.map((x) => <Badge key={x}>{x}</Badge>)}
                  </div>
                </td>
                <td class="module-device-roles">{r.roles.join(", ") || "—"}</td>
                <td>
                  <button type="button" class="module-icon-button" aria-label={`${t("mesh.editTags")} · ${r.device.name}`} onClick={() => setEditing(r)} data-testid={`mesh-edit-tags-${r.device.id}`}>
                    <Icon name="chevron"/>
                  </button>
                </td>
              </tr>
              {r.issues.length > 0 && <tr class="module-device-diagnostics"><td colSpan={5}>
                <ul class="module-device-issues">{r.issues.map((issue, n) => <li key={n} class={`module-device-issue ${issue.severity}`}><Badge kind={issue.severity === "error" ? "danger" : issue.severity === "warn" ? "warn" : "info"}>{m(issue.severity === "error" ? "issueError" : issue.severity === "warn" ? "issueWarning" : "issueInfo")}</Badge><span title={issue.message}>{meshIssueText(issue, locale)}</span></li>)}</ul>
              </td></tr>}
              </tbody>
            ))}
        </table>
      </div>
      <div class="module-device-foot">{m("deviceCount", { n: filtered.length })}</div>
      </section>}
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
    </>
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
    <Dialog title={t("mesh.tags.title", { name: device.device.name })} onClose={onClose} testid="mesh-tags-dialog" drawer>
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
