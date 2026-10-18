import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { DEFAULT_DISPLAY, cssFamily, isFontName, parseDisplay, stepZoom, zoomKey } from "../../src/shared/display";
import { DisplayStore } from "../../src/main/display-store";

test("parseDisplay accepts exactly a zoom step and clean font names", () => {
  assert.deepEqual(parseDisplay({ zoom: 1.25, uiFont: "Microsoft YaHei UI", codeFont: "Cascadia Mono" }), { zoom: 1.25, uiFont: "Microsoft YaHei UI", codeFont: "Cascadia Mono" });
  assert.deepEqual(parseDisplay({ zoom: 1 }), DEFAULT_DISPLAY);
  assert.deepEqual(parseDisplay({ zoom: 1, uiFont: "微软雅黑", codeFont: "" }), { zoom: 1, uiFont: "微软雅黑", codeFont: "" });
});

test("parseDisplay refuses anything else, so nothing odd reaches a CSS string", () => {
  for (const bad of [
    null,
    [],
    "x",
    {},
    { zoom: 2 },
    { zoom: "1" },
    { zoom: 1, extra: true },
    { zoom: 1, uiFont: 3 },
    { zoom: 1, uiFont: 'A"; color: red' },
    { zoom: 1, codeFont: "a;b" },
    { zoom: 1, uiFont: "x".repeat(65) },
  ]) {
    assert.equal(parseDisplay(bad), undefined, JSON.stringify(bad));
  }
  assert.equal(isFontName(""), false);
});

test("stepZoom moves one step and stops at the ends", () => {
  assert.equal(stepZoom(1, 1), 1.1);
  assert.equal(stepZoom(1.25, 1), 1.5);
  assert.equal(stepZoom(1.5, 1), 1.5);
  assert.equal(stepZoom(1, -1), 1);
  assert.equal(stepZoom(1.5, -1), 1.25);
  assert.equal(stepZoom(1.3, 1), 1.1, "an unknown value counts from 100%");
});

test("cssFamily puts the chosen family first and ignores a bad or empty name", () => {
  assert.equal(cssFamily("Inter", "sans-serif"), '"Inter", sans-serif');
  assert.equal(cssFamily("", "sans-serif"), undefined);
  assert.equal(cssFamily('a"b', "sans-serif"), undefined);
});

test("DisplayStore keeps what was saved and falls back to the defaults for a missing or damaged file", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-display-"));
  try {
    const file = path.join(dir, "sub", "display.json");
    assert.deepEqual(new DisplayStore(file).get(), DEFAULT_DISPLAY);
    const s = new DisplayStore(file);
    assert.equal(s.set({ zoom: 1.1, uiFont: "Inter", codeFont: "" }), true);
    assert.deepEqual(new DisplayStore(file).get(), { zoom: 1.1, uiFont: "Inter", codeFont: "" });
    fs.writeFileSync(file, "{not json");
    assert.deepEqual(new DisplayStore(file).get(), DEFAULT_DISPLAY);
    fs.writeFileSync(file, JSON.stringify({ zoom: 9 }));
    assert.deepEqual(new DisplayStore(file).get(), DEFAULT_DISPLAY);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("DisplayStore reports a write failure but keeps the value for this run", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-display-"));
  try {
    fs.writeFileSync(path.join(dir, "blocked"), "a file where a directory is needed");
    const s = new DisplayStore(path.join(dir, "blocked", "display.json"));
    assert.equal(s.set({ zoom: 1.5, uiFont: "", codeFont: "" }), false);
    assert.equal(s.get().zoom, 1.5);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test("zoomKey recognises only Ctrl/Cmd with +, -, 0 on key down", () => {
  const k = (key: string, o: Partial<{ type: string; control: boolean; meta: boolean; alt: boolean }> = {}) => ({ type: "keyDown", key, control: true, meta: false, alt: false, ...o });
  assert.equal(zoomKey(k("+")), 1);
  assert.equal(zoomKey(k("=")), 1);
  assert.equal(zoomKey(k("-")), -1);
  assert.equal(zoomKey(k("0", { control: false, meta: true })), 0);
  assert.equal(zoomKey(k("0", { control: false })), undefined);
  assert.equal(zoomKey(k("+", { type: "keyUp" })), undefined);
  assert.equal(zoomKey(k("+", { alt: true })), undefined);
  assert.equal(zoomKey(k("c")), undefined);
});
