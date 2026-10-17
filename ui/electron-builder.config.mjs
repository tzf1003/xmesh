// electron-builder configuration for the xmesh desktop shell (docs/design/12 "签名分发", roadmap U5).
//
//   XMESH_KERNEL_BIN=<path to the xmesh kernel binary for the target>  npm run package -- --win --x64
//
// Bundled kernel: the Go binary for the same OS/arch is shipped as resources/bin/xmesh[.exe] and the
// tray finds it there (src/main/cli.ts). ui-package.yml builds it with the same flags as release.yml.
//
// Signing is opt-in and only ever driven by environment variables that CI maps from repository secrets:
//   Windows  CSC_LINK + CSC_KEY_PASSWORD            (Authenticode certificate, .pfx / base64)
//   macOS    CSC_LINK + CSC_KEY_PASSWORD            (Developer ID Application certificate)
//            APPLE_ID + APPLE_APP_SPECIFIC_PASSWORD + APPLE_TEAM_ID   (notarization)
// Without them the build is unsigned and still succeeds.

import { FuseV1Options } from "@electron/fuses";
import { profiles } from "./scripts/fuses.config.mjs";

const kernel = process.env.XMESH_KERNEL_BIN;
if (!kernel && /electron-builder/.test(process.argv[1] ?? "")) {
  console.warn("XMESH_KERNEL_BIN is not set: the package will not bundle the xmesh kernel and falls back to PATH");
}

const hasValue = (name) => (process.env[name] ?? "").trim() !== "";
const macSigning = hasValue("CSC_LINK") && hasValue("CSC_KEY_PASSWORD");
const macNotarize = macSigning && hasValue("APPLE_ID") && hasValue("APPLE_APP_SPECIFIC_PASSWORD") && hasValue("APPLE_TEAM_ID");

/** scripts/fuses.config.mjs is the single source of truth; electron-builder wants camelCase names. */
export function builderFuses(profile) {
  const out = {};
  for (const [id, enabled] of Object.entries(profile)) {
    const name = FuseV1Options[id];
    if (name === undefined) continue; // "version"
    out[name[0].toLowerCase() + name.slice(1)] = enabled;
  }
  return out;
}

const bundledKernel = (to) => (kernel ? [{ from: kernel, to }] : []);

export default {
  appId: "app.xmesh.ui",
  productName: "xmesh",
  copyright: "xmesh authors",
  artifactName: "xmesh-ui-${version}-${os}-${arch}.${ext}",
  directories: { output: "release", buildResources: "build" },

  // Only the compiled app: no tests, fake kernel, sources, or tooling.
  files: ["package.json", "dist/src/**", "dist/renderer/**", "static/**", "assets/**", "!**/*.map"],
  asar: true,
  // The Fuses flip happens after the asar digest is embedded and right before signing.
  electronFuses: {
    ...builderFuses(profiles.release),
    resetAdHocDarwinSignature: process.platform === "darwin",
  },
  // The app has no native modules; skip the rebuild step.
  npmRebuild: false,
  nodeGypRebuild: false,

  win: {
    target: [{ target: "nsis" }],
    extraResources: bundledKernel("bin/xmesh.exe"),
  },
  nsis: {
    // build/installer.nsh replaces the image-name kill of running xmesh.exe with a check limited to this install's folder.
    include: "installer.nsh",
    oneClick: false,
    perMachine: false,
    allowToChangeInstallationDirectory: true,
    deleteAppDataOnUninstall: false,
    shortcutName: "xmesh",
  },

  mac: {
    target: [{ target: "dmg" }],
    category: "public.app-category.developer-tools",
    // Menu bar app: no Dock icon (the main process also hides it).
    extendInfo: { LSUIElement: true },
    hardenedRuntime: true,
    gatekeeperAssess: false,
    // identity: null keeps electron-builder from picking a keychain identity or failing on a runner without one.
    ...(macSigning ? {} : { identity: null }),
    notarize: macNotarize,
    extraResources: bundledKernel("bin/xmesh"),
  },
  dmg: { sign: false },
};
