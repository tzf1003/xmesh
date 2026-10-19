import { expect, test, type Page } from "@playwright/test";
import type { Capability } from "../../src/shared/control";
import { launch, navigate, type Harness } from "./helpers";

const ALL: Capability[] = ["status", "events", "peers", "sessions", "config.read", "config.write", "pats", "ingress", "gateway"];
const CONFIG = {
  node: { name: "role-test", roles: ["agent", "gateway"] },
  mesh: { mode: "lan" },
  gateway: { listen: ":8080", access: ["localhost", "lan", "tailnet"], hosts: ["xmesh.example.com"] },
  agent: { default_cwd: "/tmp", cwd_policy: { allow: ["/tmp"] } },
};
let h: Harness;
test.afterEach(async () => { await h?.close(); });
async function open(capabilities = ALL, config: object = CONFIG): Promise<Page> {
  h = await launch({ kernel: { capabilities, status: { roles: ["agent", "gateway"] } } });
  h.kernel.panel.setContent(JSON.stringify(config));
  const page = await h.openPanel();
  await expect(page.getByTestId("role-agent")).toHaveAttribute("aria-checked", "true");
  return page;
}

test("role capsules preview and save only role edits, then reflect the running kernel", async () => {
  const page = await open();
  await expect(page.getByTestId("role-ingress")).toHaveAttribute("aria-checked", "false");
  await page.getByTestId("role-gateway").click();
  await expect(page.getByTestId("confirm")).toContainText("agent");
  expect(h.kernel.panel.writes).toEqual([]);
  await page.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("role-gateway")).toHaveAttribute("aria-checked", "true");
  await page.getByTestId("role-gateway").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("role-gateway")).toHaveAttribute("aria-checked", "false");
  expect(JSON.parse(h.kernel.panel.content)).toEqual({ ...CONFIG, node: { ...CONFIG.node, roles: ["agent"] } });
  const writes = h.kernel.requests.filter((r) => r.method === "PATCH" && !(r.body as { dry_run?: boolean }).dry_run);
  expect(writes).toHaveLength(1);
  expect(writes[0]!.body).toMatchObject({ edits: [{ path: "/node/roles", value: ["agent"] }] });
  await expect(page.getByTestId("role-gateway")).toHaveAttribute("data-pending", "true");
  h.kernel.setStatus({ roles: ["agent"] });
  await expect(page.getByTestId("role-gateway")).toHaveAttribute("data-pending", "false");
  await expect(page.getByTestId("role-agent")).toBeDisabled();
});

test("enabling a gateway supplies its initial listen address", async () => {
  const { gateway: _gateway, ...config } = CONFIG;
  const page = await open(ALL, { ...config, node: { ...config.node, roles: ["agent"] } });
  await page.getByTestId("role-gateway").focus();
  await page.keyboard.press("Space");
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("role-gateway")).toHaveAttribute("aria-checked", "true");
  expect(JSON.parse(h.kernel.panel.content).gateway.listen).toBe("127.0.0.1:8080");
});

test("a configured ingress can be enabled from its capsule", async () => {
  const page = await open(ALL, { ...CONFIG, mesh: { mode: "tailnet" }, ingress: { listen: "tunnel", oauth: { issuer: "https://xmesh.example.com" } } });
  await page.getByTestId("role-ingress").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("role-ingress")).toHaveAttribute("aria-checked", "true");
  expect(JSON.parse(h.kernel.panel.content).node.roles).toEqual(["agent", "gateway", "ingress"]);
});

test("agent changes retain the kernel restart outcome", async () => {
  const page = await open();
  h.kernel.panel.restartPaths = ["/node/roles"];
  await page.getByTestId("role-agent").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("apply-restart-needed")).toBeVisible();
  await expect(page.getByTestId("role-agent")).toHaveAttribute("data-pending", "true");
  h.kernel.setStatus({ restart_required: true });
  await navigate(page, "runtime");
  await expect(page.getByTestId("apply-restart-needed")).toBeVisible();
  await expect(page.getByTestId("role-agent")).toBeVisible();
});

test("a pending role change in the source config refreshes the capsules", async () => {
  const page = await open();
  h.kernel.panel.setContent(JSON.stringify({ ...CONFIG, node: { ...CONFIG.node, roles: ["gateway"] } }));
  h.kernel.setStatus({ restart_required: true });
  await expect(page.getByTestId("role-agent")).toHaveAttribute("aria-checked", "false");
  await expect(page.getByTestId("role-agent")).toHaveAttribute("data-pending", "true");
});

test("a role the kernel could not apply carries its failure reason", async () => {
  const page = await open();
  h.kernel.panel.applyErrors = [{ section: "gateway", message: "address already in use" }];
  await page.getByTestId("role-gateway").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("apply-error")).toContainText("address already in use");
  await expect(page.getByTestId("apply-applied")).toHaveCount(0);
  await expect(page.getByTestId("role-gateway")).toHaveAttribute("data-pending", "true");
});

test("ingress setup and unsaved configuration are respected by the top-bar switches", async () => {
  const page = await open();
  await page.getByTestId("role-ingress").click();
  await expect(page.getByTestId("settings-ingress")).toBeVisible();
  await expect(page.getByTestId("settings-needs-tailnet")).toBeVisible();
  expect(h.kernel.panel.writes).toEqual([]);
  await expect(page.getByTestId("role-gateway")).toBeDisabled();
  await navigate(page, "config");
  await page.getByTestId("settings-name").fill("draft-node");
  await expect(page.getByTestId("role-agent")).toBeDisabled();
  await expect(page.getByTestId("settings-name")).toHaveValue("draft-node");
  await page.getByTestId("config-source-tab").click();
  await page.getByTestId("confirm-ok").click();
  await page.getByTestId("config-editor").fill("# draft\n");
  await expect(page.getByTestId("role-gateway")).toBeDisabled();
});

test("a concurrent config change rejects the stale role confirmation", async () => {
  const page = await open();
  await page.getByTestId("role-gateway").click();
  await expect(page.getByTestId("confirm")).toBeVisible();
  h.kernel.panel.setContent(JSON.stringify({ ...CONFIG, other: "preserved" }));
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("toast").first()).toHaveClass(/error/);
  expect(h.kernel.panel.writes).toEqual([]);
  expect(h.kernel.panel.content).toContain("preserved");
  await expect(page.getByTestId("role-gateway")).toHaveAttribute("aria-checked", "true");
});

test("read-only and disconnected nodes cannot toggle roles", async () => {
  const page = await open(ALL.filter((c) => c !== "config.write"));
  for (const role of ["agent", "gateway", "ingress"]) await expect(page.getByTestId(`role-${role}`)).toBeDisabled();
  expect(h.kernel.requests.filter((r) => r.method === "PATCH")).toEqual([]);
  await h.kernel.stop();
  await expect(page.getByTestId("role-switches")).toHaveCount(0);
});
