import assert from "node:assert/strict";
import { test } from "node:test";
import { deriveFeatureStates, selectedSetupJob } from "../../src/renderer/lib/features";
import { EMPTY_SNAPSHOT, type Snapshot } from "../../src/shared/state";
import type { OnboardingJob } from "../../src/shared/onboarding";
import type { CiBinding } from "../../src/shared/control";
import { fixture, peersFixture, statusFixture } from "../../testserver/fixtures";
const makeSnapshot = (): Snapshot => ({ ...EMPTY_SNAPSHOT, selectedInstance: "node-folder", connection: { kind: "connected", status: { ...statusFixture(), backends: [], network: { mode: "lan", node_id: "local", tags: [] } }, peers: peersFixture().filter((peer) => peer.self) } });
const makeJob = (name = "win-dev-01"): OnboardingJob => ({ schema_version: 1, id: "job-1", plan_id: "plan-1", node_name: name, purpose: "local", status: "waiting_user", steps: [], readiness: { installed: true, service_ready: true, client_verified: false }, created_at: "", updated_at: "2026-10-19T12:00:00Z", next_action: "" });
test("feature center excludes self and a gateway alone does not verify MCP", () => {
  const states = deriveFeatureStates(makeSnapshot(), []);
  assert.equal(states.devices, "unconfigured");
  assert.equal(states.clients, "unconfigured");
  assert.equal(states.ingress, "unconfigured");
  assert.equal(states.permissions, "ready");
});
test("device readiness needs an online nonself peer and successful info when available", () => {
  const s = makeSnapshot(); if (s.connection.kind !== "connected") return;
  const peer = peersFixture().find((p) => !p.self && p.info?.refreshed_at)!;
  s.connection.peers = [peer]; assert.equal(deriveFeatureStates(s, []).devices, "ready");
  s.connection.peers = [{ ...peer, info: { refreshed_at: null, error: null } }]; assert.equal(deriveFeatureStates(s, []).devices, "configuring");
  s.connection.peers = [{ ...peer, online: false }]; assert.equal(deriveFeatureStates(s, []).devices, "attention");
  s.connection.peers = [{ ...peer, info: { refreshed_at: peer.info!.refreshed_at, error: "probe failed" } }]; assert.equal(deriveFeatureStates(s, []).devices, "attention");
  s.connection.peers = [{ ...peer, info: undefined }]; assert.equal(deriveFeatureStates(s, []).devices, "ready");
});
test("selected hostname owns task state and unrelated newest task is excluded", () => {
  const s = makeSnapshot();
  const unrelated = { ...makeJob("different-device"), status: "failed" as const, updated_at: "2026-10-20T00:00:00Z" };
  assert.equal(selectedSetupJob(s, [unrelated]), undefined);
  assert.equal(selectedSetupJob(s, [unrelated, makeJob()])?.node_name, "win-dev-01");
  assert.equal(deriveFeatureStates(s, [unrelated]).clients, "unconfigured");
  assert.equal(deriveFeatureStates(s, [makeJob()]).clients, "configuring");
});
test("MCP running backends and verified selected clients determine readiness", () => {
  const s = makeSnapshot(); if (s.connection.kind !== "connected") return;
  s.connection.status.backends = [{ name: "server", policy: "shared", state: "running", favorite: false }];
  assert.equal(deriveFeatureStates(s, []).clients, "ready");
  s.connection.status.backends[0]!.state = "crashed"; assert.equal(deriveFeatureStates(s, []).clients, "attention");
  s.connection.status.backends = [];
  const job = makeJob(); job.status = "succeeded"; job.readiness.client_verified = true;
  assert.equal(deriveFeatureStates(s, [job]).clients, "ready");
});
test("runtime lifecycle and warnings change configured feature states", () => {
  const s = makeSnapshot(); if (s.connection.kind !== "connected") return;
  s.connection.status.network.mode = "tailnet"; s.connection.status.roles = ["agent", "gateway", "ingress"]; s.connection.status.ingress = { listen: "tunnel", issuer: "https://entry.example" };
  assert.equal(deriveFeatureStates(s, []).ingress, "ready");
  s.connection.status.warnings = [{ code: "tailnet_not_ready", severity: "warning", message: "network waiting" }];
  assert.equal(deriveFeatureStates(s, []).ingress, "attention"); assert.equal(deriveFeatureStates(s, []).mesh, "attention");
  s.connection.status.warnings = [{ code: "policy_reload_failed", severity: "error", message: "policy failed" }];
  assert.equal(deriveFeatureStates(s, []).permissions, "attention");
  s.connection.status.warnings = []; s.connection.status.lifecycle = "starting";
  assert.equal(deriveFeatureStates(s, []).mesh, "configuring");
  const cloud = { ...makeJob(), purpose: "cloud", ingress_password_set: false };
  s.connection.status.lifecycle = "running"; assert.equal(deriveFeatureStates(s, [cloud]).ingress, "configuring");
});
test("failed selected jobs and offline service state require attention", () => {
  const s = makeSnapshot(); const failed = { ...makeJob(), status: "failed" as const, steps: [{ id: "clients", status: "failed" as const, message: "client edit failed" }] };
  assert.equal(deriveFeatureStates(s, [failed]).clients, "attention");
  s.connection = { kind: "offline" }; s.selectedInstance = "win-dev-01"; s.stoppedNodes = ["win-dev-01"];
  assert.equal(deriveFeatureStates(s, []).permissions, "attention");
  const job = makeJob(); job.status = "running"; job.readiness.service_ready = false;
  assert.equal(deriveFeatureStates(s, [job]).permissions, "configuring");
});

test("CI state reflects running, installing, paused and failed runner instances", () => {
  const s = makeSnapshot(); if (s.connection.kind !== "connected") return;
  const binding = fixture<CiBinding>("CiBinding.busy");
  s.connection.status.ci = { bindings: [binding] };
  assert.equal(deriveFeatureStates(s, []).ci, "ready");
  binding.instances[0]!.state = "installing"; assert.equal(deriveFeatureStates(s, []).ci, "configuring");
  binding.instances[0]!.state = "broken"; assert.equal(deriveFeatureStates(s, []).ci, "attention");
  binding.instances[0]!.state = "idle"; binding.paused = true; assert.equal(deriveFeatureStates(s, []).ci, "attention");
  binding.paused = false; binding.removing = true; assert.equal(deriveFeatureStates(s, []).ci, "configuring");
});
