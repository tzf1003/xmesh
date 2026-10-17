import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";
import { bundledXmesh, xmeshBinary } from "../../src/main/cli";

const UI = path.resolve(__dirname, "..", "..", "..");

interface BuilderConfig {
  appId: string;
  asar: boolean;
  files: string[];
  electronFuses: Record<string, boolean>;
  win: { extraResources: { from: string; to: string }[] };
  mac: { extraResources: { from: string; to: string }[]; identity?: null; notarize: boolean; hardenedRuntime: boolean };
}

/** The config reads signing and kernel settings from the environment at import time, so each variant gets its own module URL. */
async function loadConfig(env: Record<string, string | undefined>): Promise<{ default: BuilderConfig; builderFuses: (p: Record<number, boolean>) => Record<string, boolean> }> {
  const keys = ["XMESH_KERNEL_BIN", "CSC_LINK", "CSC_KEY_PASSWORD", "APPLE_ID", "APPLE_APP_SPECIFIC_PASSWORD", "APPLE_TEAM_ID"];
  const saved = keys.map((k) => [k, process.env[k]] as const);
  for (const k of keys) delete process.env[k];
  Object.assign(process.env, Object.fromEntries(Object.entries(env).filter(([, v]) => v !== undefined)));
  try {
    const url = pathToFileURL(path.join(UI, "electron-builder.config.mjs")).href;
    return (await import(`${url}?${Math.random()}`)) as never;
  } finally {
    for (const [k, v] of saved) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  }
}

test("package.json names the repository, or electron-builder fails after packaging when the git root is not ui/", () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(UI, "package.json"), "utf8")) as { repository?: unknown };
  assert.equal(typeof pkg.repository === "string" && pkg.repository.startsWith("https://github.com/"), true);
});

test("package config: release fuses come from scripts/fuses.config.mjs", async () => {
  const { default: cfg } = await loadConfig({});
  const profiles = (await import(pathToFileURL(path.join(UI, "scripts", "fuses.config.mjs")).href)) as {
    profiles: { release: Record<number, boolean> };
  };
  const { FuseV1Options: O } = await import("@electron/fuses");
  const f = cfg.electronFuses;
  assert.equal(f.runAsNode, false);
  assert.equal(f.enableNodeOptionsEnvironmentVariable, false);
  assert.equal(f.enableNodeCliInspectArguments, false);
  assert.equal(f.grantFileProtocolExtraPrivileges, false);
  assert.equal(f.enableEmbeddedAsarIntegrityValidation, true);
  assert.equal(f.onlyLoadAppFromAsar, true);
  assert.equal(f.enableCookieEncryption, true);
  for (const [id, want] of Object.entries(profiles.profiles.release)) {
    const name = O[Number(id)];
    if (name === undefined) continue;
    assert.equal(f[name[0]!.toLowerCase() + name.slice(1)], want, name);
  }
  assert.equal(cfg.asar, true);
});

test("package config: appId is the AppUserModelID the main process registers (notifications, Start menu shortcut)", async () => {
  const { default: cfg } = await loadConfig({});
  const main = fs.readFileSync(path.join(UI, "src", "main", "main.ts"), "utf8");
  assert.ok(main.includes(`app.setAppUserModelId("${cfg.appId}")`), "main.ts must call setAppUserModelId with the packaged appId");
  assert.equal(cfg.appId, "app.xmesh.ui");
});

test("package config: only the compiled app is packaged, never tests or the fake kernel", async () => {
  const { default: cfg } = await loadConfig({});
  assert.deepEqual(cfg.files.filter((p) => !p.startsWith("!")).sort(), ["assets/**", "dist/renderer/**", "dist/src/**", "package.json", "static/**"]);
  for (const forbidden of ["dist/test", "dist/testserver", "testserver", "test/"]) {
    assert.ok(!cfg.files.some((p) => p.includes(forbidden) && !p.startsWith("!")), forbidden);
  }
  // main must be inside what is packaged
  const pkg = JSON.parse(fs.readFileSync(path.join(UI, "package.json"), "utf8")) as { main: string };
  assert.ok(pkg.main.startsWith("dist/src/"));
});

test("package config: kernel is bundled per platform when XMESH_KERNEL_BIN is set", async () => {
  const { default: cfg } = await loadConfig({ XMESH_KERNEL_BIN: "/k/xmesh" });
  assert.deepEqual(cfg.win.extraResources, [{ from: "/k/xmesh", to: "bin/xmesh.exe" }]);
  assert.deepEqual(cfg.mac.extraResources, [{ from: "/k/xmesh", to: "bin/xmesh" }]);
  const none = await loadConfig({});
  assert.deepEqual(none.default.win.extraResources, []);
});

test("package config: macOS signing and notarization are enabled only by secrets, never by default", async () => {
  const unsigned = (await loadConfig({})).default.mac;
  assert.equal(unsigned.identity, null);
  assert.equal(unsigned.notarize, false);
  assert.equal(unsigned.hardenedRuntime, true);

  // a certificate alone signs but does not notarize
  const signed = (await loadConfig({ CSC_LINK: "x", CSC_KEY_PASSWORD: "y" })).default.mac;
  assert.equal("identity" in signed, false);
  assert.equal(signed.notarize, false);

  const notarized = (await loadConfig({ CSC_LINK: "x", CSC_KEY_PASSWORD: "y", APPLE_ID: "a", APPLE_APP_SPECIFIC_PASSWORD: "b", APPLE_TEAM_ID: "c" })).default.mac;
  assert.equal(notarized.notarize, true);

  // whitespace-only secrets (an unset repository secret expands to "") do not count
  const blank = (await loadConfig({ CSC_LINK: " ", CSC_KEY_PASSWORD: "" })).default.mac;
  assert.equal(blank.identity, null);
});

test("xmeshBinary prefers XMESH_BIN, then the bundled kernel, then PATH", () => {
  const res = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-res-"));
  try {
    assert.equal(xmeshBinary({}, res, "linux"), "xmesh");
    assert.equal(bundledXmesh(undefined), undefined);
    fs.mkdirSync(path.join(res, "bin"));
    fs.writeFileSync(path.join(res, "bin", "xmesh"), "");
    assert.equal(xmeshBinary({}, res, "darwin"), path.join(res, "bin", "xmesh"));
    assert.equal(xmeshBinary({ XMESH_BIN: "/custom/xmesh" }, res, "darwin"), "/custom/xmesh");
    // Windows looks for the .exe, which is absent here
    assert.equal(xmeshBinary({}, res, "win32"), "xmesh");
    fs.writeFileSync(path.join(res, "bin", "xmesh.exe"), "");
    assert.equal(xmeshBinary({}, res, "win32"), path.join(res, "bin", "xmesh.exe"));
  } finally {
    fs.rmSync(res, { recursive: true, force: true });
  }
});

test("no runtime dependencies: the renderer is bundled, so app.asar must not carry node_modules", () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(UI, "package.json"), "utf8")) as { dependencies?: Record<string, string> };
  assert.deepEqual(Object.keys(pkg.dependencies ?? {}), []);
});
