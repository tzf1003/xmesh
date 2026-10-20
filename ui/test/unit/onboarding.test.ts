import assert from "node:assert/strict";
import { test } from "node:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { createOnboardingService, onboardingArgs, runOnboarding } from "../../src/main/onboarding";
import { onboardingDiagnostics, validIngressPassword, parseOnboarding, parseOnboardingInput, redactOnboarding, redactOnboardingResponse, setupNeedsAttention, verificationPrompt, type OnboardingInput, type OnboardingJob } from "../../src/shared/onboarding";
import { quickZh, quickEn } from "../../src/renderer/onboarding-copy";

const input: OnboardingInput = { schema_version: 1, purpose: "local", node_name: "my-node", workspace: "/work", permission: "development", clients: ["codex"], autostart: true, network: { mode: "lan" } };
const job: OnboardingJob = { schema_version: 1, id: "job-1", plan_id: "plan-1", node_name: "my-node", purpose: "local", status: "waiting_user", steps: [], readiness: { installed: true, service_ready: true, client_verified: false }, created_at: "", updated_at: "", next_action: "", verification_tool: "local/fs.stat", verification_path: "/work/.xmesh-verify-1" };
test("setup input validates purpose, paths, clients and network at the IPC boundary", () => {
  assert.deepEqual(parseOnboardingInput(input), input);
  for (const patch of [{ node_name: "../x" }, { workspace: "relative" }, { workspace: "/work\nnext" }, { clients: ["codex", "codex"] }, { clients: ["other"] }, { purpose: "cloud" }, { network: { mode: "tailnet", auth_method: "key" } }, { network: { mode: "lan", auth_method: "browser", shell: true } }, { schema_version: 2 }]) assert.equal(parseOnboardingInput({ ...input, ...patch }), undefined);
  assert.ok(parseOnboardingInput({ ...input, clients: [] }));
  assert.ok(parseOnboardingInput({ ...input, purpose: "multi-device", network: { mode: "coexist", auth_method: "browser" } }));
  assert.equal(parseOnboardingInput({ ...input, network: { mode: "coexist", auth_method: "browser" } }), undefined);
  assert.equal(parseOnboardingInput({ ...input, network: { mode: "lan", auth_method: "browser" } }), undefined);
});
test("setup operations accept fixed fields and bounded durable IDs", () => {
  assert.deepEqual(parseOnboarding({ op: "plan", input }), { op: "plan", input });
  for (const request of [{ op: "apply", plan: "../x" }, { op: "resume", job: "job-1", command: "rm" }, { op: "status", job: "job-1", open_auth: true }, { op: "verify", job: "job-1", verified: true }, { op: "doctor", root: "/another" }]) assert.equal(parseOnboarding(request), undefined);
  assert.equal(parseOnboarding({ op: "set_password", job: "job-1", password: "short" }), undefined);
  assert.ok(parseOnboarding({ op: "set_password", job: "job-1", password: "long-secure-password" }));
  assert.ok(parseOnboarding({ op: "status", job: "job-1", copy_prompt: true }));
});
test("setup CLI uses fixed argv and transfers plan content through stdin", () => {
  assert.deepEqual(onboardingArgs({ op: "plan", input }, "/state with spaces"), ["setup", "plan", "--input", "-", "--root", "/state with spaces", "--json"]);
  assert.deepEqual(onboardingArgs({ op: "apply", plan: "plan-1" }, "/state"), ["setup", "apply", "--plan", "plan-1", "--root", "/state", "--json"]);
  assert.deepEqual(onboardingArgs({ op: "set_password", job: "job-1", password: "long-secure-password" }, "/state"), ["setup", "ingress-password", "--input", "-", "--job", "job-1", "--root", "/state", "--json"]);
  assert.deepEqual(onboardingArgs({ op: "doctor", copy: true }, "/state"), ["doctor", "--root", "/state", "--json"]);
});
test("browser authorization is checked against the durable job control plane", async () => {
  const opened: string[] = [];
  const seen: unknown[] = [];
  const valid = { ...job, auth_url: "https://login.tailscale.com/a/test" };
  const service = createOnboardingService("/root", async (url) => { opened.push(url); }, async (request) => { seen.push(request); return { ok: true, data: valid }; });
  assert.equal((await service({ op: "resume", job: job.id, open_auth: true })).ok, true);
  assert.deepEqual(opened, [valid.auth_url]);
  assert.deepEqual(seen, [{ op: "status", job: job.id }, { op: "resume", job: job.id, open_auth: true }]);
  const rejected = createOnboardingService("/root", async (url) => { opened.push(url); }, async () => ({ ok: true, data: { ...valid, auth_url: "https://example.org/a/test" } }));
  assert.equal((await rejected({ op: "resume", job: job.id, open_auth: true })).ok, false);
  assert.equal(opened.length, 1);
});
test("verification prompt is derived from job evidence and copied in the main process", async () => {
  assert.equal(verificationPrompt(job), 'Call xmesh tool local/fs.stat with {"path":"/work/.xmesh-verify-1"}. Setup job: job-1.');
  let copied = "";
  const service = createOnboardingService("/root", async () => undefined, async () => ({ ok: true, data: job }), (text) => { copied = text; });
  await service({ op: "status", job: job.id, copy_prompt: true });
  assert.equal(copied, verificationPrompt(job));
});
test("ingress password length counts Unicode characters", () => {
  assert.equal(validIngressPassword("🔐".repeat(12)), true);
  assert.equal(validIngressPassword("🔐".repeat(11)), false);
  assert.equal(validIngressPassword("a".repeat(257)), false);
  assert.equal(validIngressPassword(" " + "a".repeat(12)), false);
  assert.equal(validIngressPassword("a".repeat(12) + "\u0085"), false);
  assert.equal(validIngressPassword("a".repeat(12) + "\n"), false);
});
test("diagnostics redact token-shaped credentials", () => {
  const text = redactOnboarding('authkey=tskey-auth-test token=private Bearer abc');
  assert.doesNotMatch(text, /tskey-auth-test|private|Bearer abc/);
  assert.equal(JSON.parse(redactOnboarding(JSON.stringify({ token: "private", password: "sample" }))).token, "[redacted]");
});
test("diagnostics preserve job IDs and redact browser challenge URLs", () => {
  const parsed = JSON.parse(onboardingDiagnostics({ job: { ...job, auth_url: "https://login.tailscale.com/a/private" } }));
  assert.equal(parsed.job.id, job.id);
  assert.equal(parsed.job.auth_url, "[redacted]");
});

test("structured responses and copied challenges preserve credential-shaped paths", async () => {
  for (const path of ["/work/token=project/tskey-project", "/work/password=文档/hskey-project", "C:\\work space\\secret=project"]) {
    const original = { ...job, verification_path: path };
    const response = redactOnboardingResponse({ data: original, token: "actual-secret", nested: { password: "private" } });
    assert.equal(response.data.verification_path, path);
    assert.equal(response.token, "[redacted]");
    assert.equal(response.nested.password, "[redacted]");
    let copied = "";
    const service = createOnboardingService("/root", async () => undefined, async () => ({ ok: true, data: response.data }), (text) => { copied = text; });
    await service({ op: "status", job: job.id, copy_prompt: true });
    assert.equal(copied, verificationPrompt(original));
  }
});

test("cancelled tasks do not require setup reminders", () => {
  assert.equal(setupNeedsAttention({ ...job, status: "cancelled" }), false);
  assert.equal(setupNeedsAttention({ ...job, status: "failed" }), true);
  assert.equal(setupNeedsAttention({ ...job, status: "succeeded", readiness: { ...job.readiness, client_verified: true } }), false);
  assert.ok(parseOnboarding({ op: "cancel", job: job.id }));
});

test("CLI response parsing retains exact verification paths", { skip: process.platform === "win32" }, async () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "response-"));
  try {
    const binary = path.join(root, "xmesh-fixture");
    const original = { ...job, verification_path: "/work/token=文档/tskey-project/password=notes" };
    fs.writeFileSync(binary, `#!${process.execPath}\nprocess.stdout.write(${JSON.stringify(JSON.stringify({ ok: true, data: original }))});\n`, { mode: 0o700 });
    const result = await runOnboarding({ op: "status", job: job.id }, root, binary);
    assert.equal(result.ok, true);
    if (result.ok) assert.deepEqual(result.data, original);
  } finally { fs.rmSync(root, { recursive: true, force: true }); }
});
test("missing setup binary returns a bounded actionable result", async () => {
  const result = await runOnboarding({ op: "inspect" }, "/root", "/missing-xmesh-binary");
  assert.equal(result.ok, false);
  if (!result.ok) assert.equal(result.error.code, "binary_missing");
});
test("onboarding English copy covers every label", () => {
  assert.deepEqual(Object.keys(quickEn), Object.keys(quickZh));
  for (const value of Object.values(quickEn)) { assert.ok(value.length > 0); assert.doesNotMatch(value, /[一-鿿]/); }
});
