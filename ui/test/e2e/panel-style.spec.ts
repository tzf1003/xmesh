// The panel's look: one top bar across the window, platform class for the frame, theme tokens.

import { expect, test } from "@playwright/test";
import * as fs from "node:fs";
import * as path from "node:path";
import { type Harness, launch, navigate } from "./helpers";

let h: Harness;
test.afterEach(async () => {
  await h?.close();
});

test("the top bar spans the window and the page takes its colours from the theme tokens", async () => {
  h = await launch();
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);

  await expect(page.locator("html")).toHaveClass(/\b(mac|win|linux)\b/);
  const bar = await page.locator("header.topbar").boundingBox();
  const sidebar = await page.locator("nav.sidebar").boundingBox();
  expect(bar!.height).toBe(54);
  expect(bar!.x).toBe(0);
  expect(sidebar!.y).toBeGreaterThanOrEqual(bar!.height);

  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  await page.emulateMedia({ colorScheme: "light" });
  expect(await bg()).toBe("rgb(247, 248, 247)");
  await page.emulateMedia({ colorScheme: "dark" });
  expect(await bg()).toBe("rgb(16, 21, 19)");
});

async function zoomFactor(h: Harness): Promise<number> {
  return h.app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0]!.webContents.getZoomFactor());
}

test("the display dialog sets text size and typefaces, and saves them to the user-data directory", async () => {
  h = await launch();
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);

  await navigate(page, "settings");
  await page.getByTestId("open-display").click();
  await page.getByTestId("zoom-125").click();
  await expect.poll(() => zoomFactor(h)).toBe(1.25);
  await expect(page.getByTestId("zoom-125")).toHaveAttribute("aria-pressed", "true");

  await page.getByTestId("ui-font").fill('Bad"; x');
  await expect(page.getByTestId("display-invalid")).toBeVisible();
  await expect(page.getByTestId("display-apply")).toBeDisabled();

  await page.getByTestId("ui-font").fill("Inter");
  await page.getByTestId("code-font").fill("Cascadia Mono");
  await page.getByTestId("display-apply").click();
  const vars = () => page.evaluate(() => ({ ui: document.documentElement.style.getPropertyValue("--font-ui"), mono: document.documentElement.style.getPropertyValue("--mono") }));
  await expect.poll(vars).toEqual({ ui: '"Inter", var(--font-sys)', mono: '"Cascadia Mono", var(--mono-sys)' });

  const saved = JSON.parse(fs.readFileSync(path.join(h.userData, "display.json"), "utf8"));
  expect(saved).toEqual({ zoom: 1.25, uiFont: "Inter", codeFont: "Cascadia Mono" });
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("settings-ui-font")).toHaveValue("Inter");
  await expect(page.getByTestId("settings-code-font")).toHaveValue("Cascadia Mono");
  await page.getByTestId("open-display").click();

  await page.getByTestId("display-reset").click();
  await expect.poll(() => zoomFactor(h)).toBe(1);
  await expect.poll(vars).toEqual({ ui: "", mono: "" });
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("settings-ui-font")).toHaveValue("");
  await expect(page.getByTestId("settings-code-font")).toHaveValue("");
});

test("Ctrl+plus, Ctrl+minus and Ctrl+0 step the text size from the keyboard", async () => {
  h = await launch();
  const page = await h.openPanel();
  await expect(page.getByTestId("state")).toHaveAttribute("data-state", /ok|degraded/);
  const press = (keyCode: string): Promise<void> =>
    h.app.evaluate(({ BrowserWindow }, k) => {
      const wc = BrowserWindow.getAllWindows()[0]!.webContents;
      wc.sendInputEvent({ type: "keyDown", keyCode: k, modifiers: ["control"] });
    }, keyCode);
  await press("=");
  await expect.poll(() => zoomFactor(h)).toBe(1.1);
  await press("=");
  await expect.poll(() => zoomFactor(h)).toBe(1.25);
  await press("-");
  await expect.poll(() => zoomFactor(h)).toBe(1.1);
  await press("0");
  await expect.poll(() => zoomFactor(h)).toBe(1);
});
