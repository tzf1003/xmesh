import { expect, test } from "@playwright/test";
import type { Capability } from "../../src/shared/control";
import { launch, navigate } from "./helpers";

const PEER = `dev-${"a".repeat(64)}`;
const CAPS: Capability[] = ["status", "events", "peers", "sessions", "config.read", "config.write"];

const config = (preferred?: object): string => JSON.stringify({
  schema_version: 2,
  node: { name: "pip-node", roles: ["agent"] },
  mesh: { cloudflare: { enabled: true, hostname: "mac.link.example.com", peers: { [PEER]: "win.link.example.com" }, preferred_ips: ["104.16.0.1"], ...(preferred && { preferred_ip: preferred }) } },
});

test("the preferred IP page saves sources, a scan schedule and a per-device override, and the pool follows", async () => {
  const h = await launch({ kernel: { capabilities: CAPS } });
  try {
    h.kernel.panel.setContent(config());
    const page = await h.openPanel();
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await navigate(page, "preferredip");
    await expect(page.getByTestId("page-preferred-ip")).toBeVisible();
    await expect(page.getByTestId("pip-count")).toHaveText("1");
    await expect(page.getByTestId("pip-row-104.16.0.1")).toContainText("手动");
    await expect(page.getByTestId("pip-savebar")).toHaveCount(0);

    // Scheduled selection.
    await page.getByTestId("pip-scan-enabled").check();
    await expect(page.getByTestId("pip-savebar")).toBeVisible();
    await page.getByTestId("pip-scan-interval").selectOption("12h");

    // A manual source and a subscription, from the pool tab.
    await page.getByTestId("pip-tab-pool").click();
    await page.getByTestId("pip-add-manual").click();
    await page.getByRole("textbox", { name: "地址" }).fill("172.64.153.21\n8.8.4.4");
    await page.getByTestId("pip-add-url").click();
    await expect(page.getByTestId("pip-url-warning")).toContainText("第三方");
    await page.getByTestId("pip-source-url-1").fill("http://example.com/ips");
    await expect(page.getByTestId("pip-problem")).toContainText("https://");
    await expect(page.getByTestId("pip-save")).toBeDisabled();
    await page.getByTestId("pip-source-url-1").fill("https://example.com/ips.txt");
    await expect(page.getByTestId("pip-problem")).toHaveCount(0);

    // One device never uses preferred addresses.
    await page.getByTestId("pip-tab-access").click();
    await page.getByTestId(`pip-peer-mode-${PEER.slice(0, 12)}`).selectOption("off");
    await page.getByTestId("pip-cooldown").fill("10");

    await page.screenshot({ path: test.info().outputPath("preferred-ip-access.png"), fullPage: true });
    await page.getByTestId("pip-save").click();
    await expect(page.getByTestId("pip-savebar")).toHaveCount(0);
    const saved = JSON.parse(h.kernel.panel.content) as { mesh: { cloudflare: { preferred_ip: Record<string, unknown> } } };
    expect(saved.mesh.cloudflare.preferred_ip).toEqual({
      cooldown: "10m",
      sources: [{ type: "manual", ips: ["172.64.153.21", "8.8.4.4"] }, { type: "url", url: "https://example.com/ips.txt" }],
      scan: { enabled: true, interval: "12h" },
      peers: { [PEER]: { mode: "off" } },
    });

    // The pool only takes addresses inside Cloudflare's ranges; the scan and the refresh run on request.
    await page.getByTestId("pip-tab-overview").click();
    await expect(page.getByTestId("pip-count")).toHaveText("2");
    await expect(page.getByTestId("pip-row-8.8.4.4")).toHaveCount(0);
    await page.getByTestId("pip-scan-now").click();
    await expect(page.getByTestId("pip-pool")).toContainText("测速");
    await expect(page.getByTestId("pip-count")).toHaveText("4");
    await page.screenshot({ path: test.info().outputPath("preferred-ip-overview.png"), fullPage: true });
    expect(errors).toEqual([]);
  } finally { await h.close(); }
});

test("the speed test region defaults to automatic, can be limited to picked regions and colo codes, and the pool shows colos", async () => {
  const h = await launch({ kernel: { capabilities: CAPS } });
  try {
    h.kernel.panel.setContent(config());
    const page = await h.openPanel();
    await navigate(page, "preferredip");
    await page.getByTestId("pip-scan-enabled").check();
    await page.getByTestId("pip-tab-pool").click();
    await expect(page.getByTestId("pip-region-mode-auto")).toBeChecked();
    await expect(page.getByTestId("pip-region-count")).toHaveValue("2");

    await page.getByTestId("pip-region-mode-pick").check();
    await expect(page.getByTestId("pip-problem")).toContainText("至少");
    await expect(page.getByTestId("pip-save")).toBeDisabled();
    await page.getByTestId("pip-region-hk").click();
    await page.getByTestId("pip-region-codes").fill("nrt, x1 hongkong");
    await page.getByTestId("pip-region-codes").blur();
    await expect(page.getByTestId("pip-region-NRT")).toBeVisible();
    await expect(page.getByTestId("pip-problem")).toHaveCount(0);
    await page.getByTestId("pip-save").click();
    await expect(page.getByTestId("pip-savebar")).toHaveCount(0);
    const saved = JSON.parse(h.kernel.panel.content) as { mesh: { cloudflare: { preferred_ip: Record<string, unknown> } } };
    expect(saved.mesh.cloudflare.preferred_ip.scan).toEqual({ enabled: true, region_mode: "pick", regions: ["hk", "NRT"] });

    await page.getByTestId("pip-tab-overview").click();
    await page.getByTestId("pip-scan-now").click();
    await expect(page.getByTestId("pip-colo-104.18.32.77")).toContainText("HKG");
    await page.getByTestId("pip-tab-pool").click();
    await expect(page.getByTestId("pip-region-row-hk")).toContainText("已保留");
    await expect(page.getByTestId("pip-region-row-us-w")).not.toContainText("已保留");
  } finally { await h.close(); }
});

test("importing a list checks each line first and adds the new addresses as a manual source", async () => {
  const h = await launch({ kernel: { capabilities: CAPS } });
  try {
    h.kernel.panel.setContent(config());
    const page = await h.openPanel();
    await navigate(page, "preferredip");
    await page.getByTestId("pip-tab-pool").click();
    await page.getByTestId("pip-import").click();
    await page.getByTestId("pip-import-text").fill("IP 地址,已发送\n104.16.0.1,4\n104.18.32.77,4\n8.8.8.8,4\n");
    await expect(page.getByTestId("pip-import-summary")).toContainText("1 个可新增");
    await expect(page.getByTestId("pip-import-summary")).toContainText("1 个已存在");
    await expect(page.getByTestId("pip-import-summary")).toContainText("1 个被拒绝");
    await page.screenshot({ path: test.info().outputPath("preferred-ip-import.png") });
    await page.getByTestId("pip-import-confirm").click();
    await expect(page.getByTestId("pip-source-0").getByRole("textbox", { name: "地址" })).toHaveValue("104.18.32.77");
    await page.getByTestId("pip-save").click();
    await expect(page.getByTestId("pip-savebar")).toHaveCount(0);
    expect(JSON.parse(h.kernel.panel.content).mesh.cloudflare.preferred_ip.sources).toEqual([{ type: "manual", name: "导入", ips: ["104.18.32.77"] }]);
  } finally { await h.close(); }
});

test("a read-only session can look but not change, and a kernel without the capability says so", async () => {
  const h = await launch({ kernel: { capabilities: CAPS.filter((c) => c !== "config.write") } });
  try {
    h.kernel.panel.setContent(config());
    const page = await h.openPanel();
    await navigate(page, "preferredip");
    await expect(page.getByTestId("pip-enabled")).toBeDisabled();
    await page.getByTestId("pip-tab-pool").click();
    await expect(page.getByTestId("pip-add-manual")).toBeDisabled();
  } finally { await h.close(); }
  const old = await launch({ kernel: { capabilities: ["status", "events"] satisfies Capability[] } });
  try {
    const page = await old.openPanel();
    await navigate(page, "preferredip");
    await expect(page.getByTestId("page-preferred-ip")).toHaveCount(0);
    await expect(page.locator("main")).toContainText("优选 IP");
  } finally { await old.close(); }
});
