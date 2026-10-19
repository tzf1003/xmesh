import assert from "node:assert/strict";
import { test } from "node:test";
import { CHROME_BG, TOPBAR_HEIGHT, overlayColors, windowChrome } from "../../src/main/chrome";

test("macOS hides the title bar and keeps the traffic lights over the page's top bar", () => {
  const o = windowChrome("darwin", false);
  assert.equal(o.titleBarStyle, "hiddenInset");
  assert.deepEqual(o.trafficLightPosition, { x: 16, y: 15 });
  assert.equal(o.titleBarOverlay, undefined);
});

test("Windows paints the caption area in the page colour and follows the theme", () => {
  const light = windowChrome("win32", false);
  assert.equal(light.titleBarStyle, "hidden");
  assert.deepEqual(light.titleBarOverlay, { color: CHROME_BG.light, symbolColor: "#161b18", height: TOPBAR_HEIGHT });
  const dark = windowChrome("win32", true);
  assert.equal((dark.titleBarOverlay as { color: string }).color, CHROME_BG.dark);
  assert.deepEqual(overlayColors(true), { color: CHROME_BG.dark, symbolColor: "#e8ece9", height: TOPBAR_HEIGHT });
});

test("Linux keeps the system frame", () => {
  const o = windowChrome("linux", true);
  assert.equal(o.titleBarStyle, undefined);
  assert.equal(o.titleBarOverlay, undefined);
  assert.equal(o.backgroundColor, CHROME_BG.dark);
});
