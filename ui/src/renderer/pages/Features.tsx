import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext, PageId, PageFocus } from "../app";
import { getApi } from "../api";
import { Badge, Banner, PageHead } from "../components";
import { deriveFeatureStates, selectedSetupJob, type FeatureId } from "../lib/features";
import { useQuickCopy } from "../onboarding-copy";
import type { OnboardingJob } from "../../shared/onboarding";

export function Features({ ctx }: { ctx: PageContext }): JSX.Element {
  const copy = useQuickCopy();
  const c = ctx.snapshot.connection;
  const [jobs, setJobs] = useState<OnboardingJob[]>([]);
  const job = selectedSetupJob(ctx.snapshot, jobs);
  const states = deriveFeatureStates(ctx.snapshot, jobs);
  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async (): Promise<void> => { try { const r = await getApi().onboarding({ op: "inspect" }); if (active && r.ok) setJobs(r.data.jobs); } catch { /* Runtime state still describes the selected node. */ } if (active) timer = setTimeout(() => void refresh(), 5000); };
    setJobs([]); void refresh();
    return () => { active = false; clearTimeout(timer); };
  }, [ctx.snapshot.selectedInstance, c.kind === "connected" ? c.status.hostname : ""]);
  const connected = c.kind === "connected";
  const features: { id: FeatureId; title: string; sub: string; page: PageId; focus?: PageFocus }[] = [
    { id: "devices", title: copy.deviceFeature, sub: copy.deviceFeatureSub, page: "devices" },
    { id: "ingress", title: copy.ingressFeature, sub: copy.ingressFeatureSub, page: "ingress" },
    { id: "clients", title: copy.clientFeature, sub: copy.clientFeatureSub, page: "authz", focus: { section: "mcp" } },
    { id: "ci", title: copy.ciFeature, sub: copy.ciFeatureSub, page: "ci" },
    { id: "permissions", title: copy.permissionsFeature, sub: copy.permissionsFeatureSub, page: "settings", focus: { section: "workdirs" } },
    { id: "mesh", title: copy.meshFeature, sub: copy.meshFeatureSub, page: "mesh" },
  ];
  return <div class="page" data-testid="page-features"><PageHead title={copy.features} sub={copy.featuresSub}><button class="btn" onClick={() => ctx.go("setup")} data-testid="feature-new-setup">{copy.newSetup}</button></PageHead>{job && <Banner kind={job.readiness.service_ready ? "ok" : "warn"}><div class="row"><span class="grow">{job.node_name} · {job.readiness.service_ready ? copy.ready : copy.servicePending} · {job.readiness.client_verified ? copy.verified : copy.pending}</span><button class="btn small" onClick={() => ctx.go("setup")}>{copy.resume}</button></div></Banner>}{!connected && <Banner>{copy.disconnected}</Banner>}<div class="feature-grid">{features.map((feature) => <button class="feature-card card" key={feature.id} data-state={states[feature.id]} data-testid={`feature-${feature.id}`} onClick={() => ctx.go(feature.page, feature.focus)} disabled={!connected && feature.page !== "mesh"}><span class="row"><strong class="grow">{feature.title}</strong><Badge kind={states[feature.id] === "ready" ? "ok" : states[feature.id] === "attention" ? "warn" : ""}>{({ unconfigured: copy.featureUnconfigured, configuring: copy.featureConfiguring, ready: copy.featureReady, attention: copy.featureAttention })[states[feature.id]]}</Badge></span><span class="hint">{feature.sub}</span><span class="feature-action">{copy.configure} →</span></button>)}</div></div>;
}
