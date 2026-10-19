import type { Snapshot } from "../../shared/state";
import type { OnboardingJob } from "../../shared/onboarding";
export type FeatureState = "unconfigured" | "configuring" | "ready" | "attention";
export type FeatureId = "devices" | "ingress" | "clients" | "ci" | "permissions" | "mesh";
export function selectedSetupJob(snapshot: Snapshot, jobs: readonly OnboardingJob[]): OnboardingJob | undefined {
  const name = snapshot.connection.kind === "connected" ? snapshot.connection.status.hostname : snapshot.selectedInstance;
  if (!name) return;
  return jobs.filter((job) => job.node_name === name).slice().sort((a, b) => b.updated_at.localeCompare(a.updated_at))[0];
}
export function deriveFeatureStates(snapshot: Snapshot, jobs: readonly OnboardingJob[]): Record<FeatureId, FeatureState> {
  const result: Record<FeatureId, FeatureState> = { devices: "unconfigured", ingress: "unconfigured", clients: "unconfigured", ci: "unconfigured", permissions: "unconfigured", mesh: "unconfigured" };
  const job = selectedSetupJob(snapshot, jobs);
  const c = snapshot.connection;
  const active = job && ["pending", "running", "waiting_user"].includes(job.status);
  const jobFeature = (id: FeatureId): boolean => id === "permissions" || id === "clients" || id === "devices" && job?.purpose === "multi-device" || (id === "mesh" || id === "ingress") && job?.purpose === "cloud";
  if (c.kind !== "connected") {
    const unavailable = c.kind === "mismatch" || snapshot.stoppedNodes.includes(snapshot.selectedInstance) || snapshot.unresponsiveNodes.includes(snapshot.selectedInstance);
    for (const id of Object.keys(result) as FeatureId[]) {
      if (unavailable) result[id] = "attention";
      if (jobFeature(id) && active) result[id] = "configuring";
      if (jobFeature(id) && job?.status === "failed") result[id] = "attention";
    }
    return result;
  }
  const s = c.status;
  const warnings = (codes: string[]): boolean => s.warnings.some((warning) => warning.severity !== "info" && codes.includes(warning.code));
  const peers = c.peers.filter((peer) => !peer.self);
  const usablePeers = peers.filter((peer) => peer.online && (!peer.info || !!peer.info.refreshed_at && !peer.info.error));
  result.devices = usablePeers.length ? "ready" : peers.some((peer) => !peer.online || !!peer.info?.error) ? "attention" : peers.length ? "configuring" : "unconfigured";
  if (warnings(["peer_offline", "firewall_blocked"])) result.devices = "attention";
  const cloudPending = job?.purpose === "cloud" && !job.readiness.client_verified;
  result.ingress = !s.roles.includes("ingress") ? "unconfigured" : warnings(["tailnet_not_ready", "authkey_unreadable", "firewall_blocked"]) ? "attention" : s.ingress?.issuer && !cloudPending ? "ready" : "configuring";
  result.clients = s.backends.some((backend) => backend.state === "crashed") ? "attention" : s.backends.some((backend) => backend.state === "running") || job?.readiness.client_verified ? "ready" : s.backends.length ? "configuring" : "unconfigured";
  const bindings = s.ci?.bindings ?? [];
  const instances = bindings.flatMap((binding) => binding.instances);
  result.ci = !bindings.length ? "unconfigured" : bindings.some((binding) => binding.paused) || instances.some((instance) => instance.state === "broken" || instance.state === "backoff" || instance.state === "stopped" || !!instance.problem) ? "attention" : !bindings.some((binding) => binding.removing) && instances.length > 0 && instances.every((instance) => ["idle", "busy"].includes(instance.state)) ? "ready" : "configuring";
  result.permissions = warnings(["policy_reload_failed", "sandbox_unavailable"]) || s.policy_reload_failures > 0 ? "attention" : "ready";
  result.mesh = (s.network.mode !== "tailnet" && s.network.mode !== "coexist") ? "unconfigured" : warnings(["tailnet_not_ready", "authkey_unreadable", "firewall_blocked"]) ? "attention" : "ready";
  if (active) {
    if (!job.readiness.service_ready) for (const id of Object.keys(result) as FeatureId[]) { if (jobFeature(id) && result[id] !== "attention") result[id] = "configuring"; }
    if (!job.readiness.client_verified && result.clients === "unconfigured") result.clients = "configuring";
  }
  if (job?.status === "failed") {
    const failed = job.steps.find((step) => step.status === "failed")?.id;
    if (failed === "network") { result.mesh = "attention"; if (job.purpose === "cloud") result.ingress = "attention"; }
    else if (failed === "clients" || failed === "verify") result.clients = "attention";
    else for (const id of Object.keys(result) as FeatureId[]) if (jobFeature(id)) result[id] = "attention";
  }
  if (s.lifecycle !== "running") for (const id of Object.keys(result) as FeatureId[]) if (result[id] === "ready") result[id] = "configuring";
  if (s.drain.draining || s.restart_required || s.warnings.some((warning) => warning.severity === "error" && warning.code === "unclassified")) for (const id of Object.keys(result) as FeatureId[]) if (result[id] === "ready") result[id] = "attention";
  return result;
}
