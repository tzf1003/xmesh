import { expect, test } from "@playwright/test";
import * as fs from "node:fs";
import * as path from "node:path";
import { type Harness, launch, navigate } from "./helpers";

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});
async function open() {
  h = await launch({
    kernel: { playground: true, capabilities: ["status", "events", "peers", "pats", "sessions", "drain"] },
  });
  h.kernel.playground!.enableWorkspace();
  return h.openPanel();
}
async function pick(file: string) {
  await h.app.evaluate((_e, p) => {
    (globalThis as unknown as { __xmeshUiTest: { answers: { picked: string } } }).__xmeshUiTest.answers.picked = p;
  }, file);
}

test("terminal opens, accepts keyboard input, resizes, sends a signal and closes", async () => {
  const page = await open();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error") errors.push(m.text());
  });
  await navigate(page, "terminal");
  await page.getByTestId("terminal-new").click();
  await page.getByTestId("terminal-connect").click();
  await expect(page.getByTestId("terminal-screen")).toBeVisible();
  await expect(page.getByText("可输入", { exact: true })).toBeVisible();
  await page.locator(".xterm-helper-textarea").fill("pwd\r");
  await expect
    .poll(() =>
      h.kernel.playground!.calls.some((c) => c.name === "local/pty.write" && String(c.args.data).includes("pwd")),
    )
    .toBe(true);
  await page.setViewportSize({ width: 1400, height: 920 });
  await expect.poll(() => h.kernel.playground!.calls.some((c) => c.name === "local/pty.resize")).toBe(true);
  await page.getByRole("button", { name: "INT", exact: true }).click();
  await page.getByTestId("confirm-ok").click();
  await expect
    .poll(() => h.kernel.playground!.calls.some((c) => c.name === "local/pty.signal" && c.args.signal === "INT"))
    .toBe(true);
  await page.screenshot({ path: "/tmp/xmesh-terminal.png" });
  const nonce = await page.locator('meta[name="xmesh-style-nonce"]').getAttribute("content");
  expect(nonce).toBeTruthy();
  expect(nonce).not.toContain("__XMESH");
  const styles = await page
    .locator(".terminal-screen style")
    .evaluateAll((els) => els.map((e) => (e as HTMLStyleElement).nonce));
  expect(styles.length).toBeGreaterThan(0);
  expect(styles.every((n) => n === nonce)).toBe(true);
  const session = [...h.kernel.playground!.workspace!.terminals.values()][0]!;
  session.owner = "other-client";
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(page.getByText("只读 · 此会话由其他调用方持有")).toBeVisible();
  await expect(page.getByTestId("terminal-close")).toBeDisabled();
  session.state = "orphaned";
  session.owner = "";
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await page.getByRole("button", { name: "接管会话", exact: true }).click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByText("可输入", { exact: true })).toBeVisible();
  await page.getByTestId("terminal-close").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByText("会话已关闭", { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test("file UI uploads and downloads binary bytes and shows conflict retry", async () => {
  const page = await open();
  await navigate(page, "transfers");
  const input = path.join(h.root, "source.bin"),
    output = path.join(h.root, "download.bin");
  const bytes = Buffer.alloc(1024 * 1024 + 31, 0xa3);
  fs.writeFileSync(input, bytes);
  await pick(input);
  await page.getByTestId("transfer-pick").click();
  await page.getByTestId("transfer-path").fill("/work/target.bin");
  await page.getByTestId("transfer-start").click();
  await expect(page.getByTestId("transfer-job").first()).toContainText("完成 · 校验一致");
  expect(h.kernel.playground!.workspace!.files.get("/work/target.bin")).toEqual(bytes);
  await page.getByTestId("transfer-start").click();
  await expect(page.getByRole("button", { name: "确认后覆盖并重试" })).toBeVisible();
  await page.getByRole("button", { name: "确认后覆盖并重试" }).click();
  await expect(page.getByTestId("transfer-job").first()).toContainText("完成 · 校验一致");
  await page.getByTestId("transfer-download").click();
  await pick(output);
  await page.getByTestId("transfer-start").click();
  await expect.poll(() => fs.existsSync(output)).toBe(true);
  expect(fs.readFileSync(output)).toEqual(bytes);
  await page.setViewportSize({ width: 1400, height: 920 });
  await page.screenshot({ path: "/tmp/xmesh-transfers.png" });
  expect(await page.content()).not.toContain(bytes.toString("base64").slice(0, 200));
});
