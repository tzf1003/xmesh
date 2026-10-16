import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";
import { MESSAGE_KEYS, TABLES, resolveLocale, translator } from "../../src/shared/i18n";

const UI = path.join(__dirname, "..", "..", "..");

test("zh is the default locale; en is selectable", () => {
  assert.equal(resolveLocale(undefined), "zh");
  assert.equal(resolveLocale("en"), "en");
  assert.equal(resolveLocale("EN-us"), "en");
  assert.equal(resolveLocale("fr"), "zh");
});

test("every key exists in both tables with the same placeholders", () => {
  const holders = (s: string): string[] => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]!).sort();
  for (const k of MESSAGE_KEYS) {
    assert.ok(TABLES.en[k] && TABLES.zh[k], k);
    assert.deepEqual(holders(TABLES.en[k]), holders(TABLES.zh[k]), k);
  }
  assert.equal(translator("en")("menu.peers", { count: 3 }), "Peers online: 3");
});

test("tray icon assets exist for every state, in plain and macOS template variants, at 1x and 2x", () => {
  const states = ["offline", "ok", "degraded", "draining", "restart", "mismatch"];
  for (const s of states) {
    for (const f of [`tray-${s}.png`, `tray-${s}@2x.png`, `tray-${s}Template.png`, `tray-${s}Template@2x.png`]) {
      const file = path.join(UI, "assets", "tray", f);
      const head = fs.readFileSync(file).subarray(0, 8);
      assert.deepEqual([...head], [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a], f);
    }
  }
});

test("fuse profiles: injection defences are off in every profile; only test relaxes inspect and asar", async () => {
  const mod = (await import(pathToFileURL(path.join(UI, "scripts", "fuses.config.mjs")).href)) as {
    profiles: Record<string, Record<string, unknown>>;
    ALWAYS: number[];
  };
  const fuses = await import("@electron/fuses");
  const { release, test: testProfile } = mod.profiles as Record<string, Record<number, unknown>>;
  const O = fuses.FuseV1Options;

  for (const profile of [release!, testProfile!]) {
    for (const id of mod.ALWAYS) assert.equal(profile[id], false, `${O[id]} must be disabled`);
    assert.equal(profile[O.EnableCookieEncryption], true);
  }
  assert.equal(release![O.EnableNodeCliInspectArguments], false);
  assert.equal(release![O.EnableEmbeddedAsarIntegrityValidation], true);
  assert.equal(release![O.OnlyLoadAppFromAsar], true);
  // The test profile differs from release in exactly these three.
  const differing = Object.keys(release!).filter((k) => release![Number(k)] !== testProfile![Number(k)]).map((k) => O[Number(k)]);
  assert.deepEqual(differing.sort(), ["EnableEmbeddedAsarIntegrityValidation", "EnableNodeCliInspectArguments", "OnlyLoadAppFromAsar"].sort());
});
