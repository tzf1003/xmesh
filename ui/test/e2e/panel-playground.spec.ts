import { expect, test } from "@playwright/test";
import { type Harness, launch, navigate } from "./helpers";

let h: Harness;
test.afterEach(async () => { await h?.close(); });

async function open(lang: "en" | "zh" = "zh") {
  h = await launch({ lang, kernel: { playground: true, capabilities: ["status", "events", "peers", "sessions", "drain", "pats", "audit"] } });
  const page = await h.openPanel();
  await navigate(page, "playground");
  await expect(page.getByTestId("tool-local/sys.info")).toBeVisible();
  return page;
}

test("schema form, raw JSON, result, history and tool-change refresh use the real client", async () => {
  const page = await open();
  const errors: string[] = []; page.on("pageerror", (e) => errors.push(e.message));
  await page.getByTestId("tool-local/exec.run").click();
  await expect(page.getByTestId("tool-run")).toBeDisabled();
  await page.locator("#tool-arg-cmd").fill("pwd");
  await page.locator("#tool-arg-args").fill('["-L"]');
  await page.getByTestId("tool-run").click();
  await expect(page.getByTestId("tool-result")).toContainText("executed: pwd");
  expect(h.kernel.playground!.calls.at(-1)).toMatchObject({ name: "local/exec.run", args: { cmd: "pwd", args: ["-L"] }, origin: "panel" });
  await page.getByRole("tab", { name: "原始 JSON" }).click();
  await page.getByTestId("tool-json").fill('{"cmd":"echo","stdin":"private-value","env":{"SECRET":"secret"}}');
  await page.getByTestId("tool-run").click();
  await expect(page.getByTestId("tool-history-entry")).toHaveCount(2);
  await page.getByTestId("tool-history-entry").first().click();
  await expect(page.getByTestId("tool-json")).not.toHaveValue(/private-value|secret/);
  await expect(page.getByTestId("tool-run")).toBeDisabled();
  await page.getByTestId("tool-json").fill('{"cmd":"pwd"}');
  await h.kernel.playground!.changed();
  await expect(page.getByText("工具列表已更新。", { exact: false })).toBeVisible();
  await page.getByTestId("tools-refresh").click();
  await expect(page.getByTestId("tool-json")).toHaveValue('{"cmd":"pwd"}');
  expect(await page.content()).not.toContain(h.kernel.panel.tokenOf("default"));
  await page.setViewportSize({ width: 1440, height: 960 });
  await page.screenshot({ path: "/tmp/xmesh-playground-tools.png" });
  expect(errors).toEqual([]);
});

test("tool error, cancellation and local JSON error remain distinct", async () => {
  const page = await open("en");
  await page.getByTestId("tool-local/demo.denied").click();
  await page.getByTestId("tool-run").click();
  await expect(page.getByTestId("tool-result")).toContainText("Tool execution failed");
  await expect(page.getByTestId("tool-result")).toContainText("policy_denied");
  await page.getByTestId("tool-local/demo.slow").click();
  await page.getByTestId("tool-run").click();
  await expect(page.getByTestId("tool-cancel")).toBeVisible();
  await page.getByTestId("tool-cancel").click();
  await expect(page.getByTestId("error")).toContainText("cancelled");
  await page.getByRole("tab", { name: "Raw JSON" }).click();
  await page.getByTestId("tool-json").fill("{broken");
  await expect(page.getByTestId("tool-run")).toBeDisabled();
  await page.setViewportSize({ width: 850, height: 800 });
  await page.screenshot({ path: "/tmp/xmesh-playground-narrow.png" });
});
