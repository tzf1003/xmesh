// Electron Fuses for xmesh-ui (docs/design/12 "面板": 关闭 RunAsNode 等，启用 ASAR 完整性校验).
//
// release: what ships. Applied by the packaging step (electron-builder afterPack, owned by the
//          platform/release tasks) with `node scripts/flip-fuses.mjs --profile release --binary <app>`.
// test:    the same, except for the three things the end-to-end run needs:
//          Playwright attaches through --inspect (EnableNodeCliInspectArguments), and the app runs
//          from loose files in dist/ instead of an asar (so no ASAR integrity / OnlyLoadAppFromAsar).
//          Everything that guards against code injection stays as in release.

import { FuseV1Options, FuseVersion } from "@electron/fuses";

const release = {
  version: FuseVersion.V1,
  [FuseV1Options.RunAsNode]: false,
  [FuseV1Options.EnableCookieEncryption]: true,
  [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
  [FuseV1Options.EnableNodeCliInspectArguments]: false,
  [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: true,
  [FuseV1Options.OnlyLoadAppFromAsar]: true,
  [FuseV1Options.GrantFileProtocolExtraPrivileges]: false,
};

export const profiles = {
  release,
  test: {
    ...release,
    [FuseV1Options.EnableNodeCliInspectArguments]: true,
    [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: false,
    [FuseV1Options.OnlyLoadAppFromAsar]: false,
  },
};

/** Fuses that must be identical in every profile: they are the injection defences. */
export const ALWAYS = [
  FuseV1Options.RunAsNode,
  FuseV1Options.EnableNodeOptionsEnvironmentVariable,
  FuseV1Options.GrantFileProtocolExtraPrivileges,
];
