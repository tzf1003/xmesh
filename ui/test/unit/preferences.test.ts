import test from "node:test";
import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { PreferencesStore } from "../../src/main/preferences-store";
import { parseAppPreferences } from "../../src/shared/preferences";

test("application preferences survive restart, including the first-run marker", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-preferences-"));
  try {
    const file = path.join(dir, "preferences.json");
    const store = new PreferencesStore(file, "en");
    assert.deepEqual(store.get(), { locale: "en", closeToTray: true, firstRunSeen: false });
    const saved = { locale: "zh" as const, closeToTray: false, firstRunSeen: true };
    assert.equal(store.set(saved), true);
    assert.deepEqual(new PreferencesStore(file, "en").get(), saved);
    store.get().firstRunSeen = false;
    assert.equal(store.get().firstRunSeen, true);
    fs.writeFileSync(file, "invalid");
    assert.equal(new PreferencesStore(file, "en").get().firstRunSeen, false);
  } finally { fs.rmSync(dir, { recursive: true, force: true }); }
});

test("preferences IPC accepts supported values and rejects malformed fields", () => {
  const valid = { locale: "en", closeToTray: true, firstRunSeen: true };
  assert.deepEqual(parseAppPreferences(valid), valid);
  for (const invalid of [null, [], {}, { ...valid, locale: "xx" }, { ...valid, closeToTray: "yes" }, { ...valid, firstRunSeen: 1 }, { ...valid, unknown: true }]) {
    assert.equal(parseAppPreferences(invalid), undefined);
  }
});
