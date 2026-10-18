// The panel's look: one top bar across the window, platform class for the frame, theme tokens.

import { expect, test } from "@playwright/test";
import { type Harness, launch } from "./helpers";

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
  expect(bar!.height).toBe(46);
  expect(bar!.x).toBe(0);
  expect(sidebar!.y).toBeGreaterThanOrEqual(bar!.height);

  const bg = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  await page.emulateMedia({ colorScheme: "light" });
  expect(await bg()).toBe("rgb(244, 244, 246)");
  await page.emulateMedia({ colorScheme: "dark" });
  expect(await bg()).toBe("rgb(26, 26, 30)");
});
