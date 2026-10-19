#!/usr/bin/env node
// Local release credentials stay in the macOS keychain; this file reads references only.
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ui = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const referenceFile = path.join(homedir(), "Library", "Application Support", "xmesh", "release.json");
function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: ui, stdio: "inherit", ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} failed (${result.status ?? result.signal})`);
  return result;
}

try {
  if (process.platform !== "darwin") throw new Error("Local Mac packaging requires macOS.");
  const version = process.argv[2];
  if (process.argv.length !== 3 || (version !== "--check" && !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version ?? ""))) {
    throw new Error("Usage: node scripts/package-macos-local.mjs <version|--check>");
  }
  const references = existsSync(referenceFile) ? JSON.parse(readFileSync(referenceFile, "utf8")) : {};
  const identity = (process.env.CSC_NAME ?? references.identity ?? "").trim();
  const profile = (process.env.APPLE_KEYCHAIN_PROFILE ?? references.notary_profile ?? "").trim();
  const keychainArgs = process.env.APPLE_KEYCHAIN?.trim() ? ["--keychain", process.env.APPLE_KEYCHAIN.trim()] : [];
  if (!/^[A-Fa-f0-9]{40}$/.test(identity)) throw new Error(`Select a Developer ID Application SHA-1 in CSC_NAME or ${referenceFile}.`);
  if (!profile) throw new Error(`Set APPLE_KEYCHAIN_PROFILE or notary_profile in ${referenceFile}.`);
  const identities = run("security", ["find-identity", "-v", "-p", "codesigning"], { encoding: "utf8", stdio: "pipe" }).stdout;
  const selected = identities.split("\n").find(line => line.includes(identity.toUpperCase()) && line.includes('"Developer ID Application:'));
  if (!selected) throw new Error("The selected Developer ID Application identity and private key are unavailable.");
  // Validate authentication before spending time building. Passwords are read by notarytool from Keychain.
  run("xcrun", ["notarytool", "history", "--keychain-profile", profile, ...keychainArgs, "--output-format", "json"], { stdio: ["ignore", "ignore", "inherit"] });
  console.log("Developer ID identity and notarization profile are ready.");
  if (version !== "--check") {
    if (!existsSync(path.join(ui, "node_modules"))) throw new Error("Run npm ci in ui/ first.");
    const kernel = path.join(ui, "build", "kernel", "xmesh");
    mkdirSync(path.dirname(kernel), { recursive: true });
    run("go", ["build", "-trimpath", "-ldflags", `-s -w -X main.buildVersion=v${version}`, "-o", kernel, "./cmd/xmesh"], {
      cwd: path.dirname(ui), env: { ...process.env, CGO_ENABLED: "0" },
    });
    run("codesign", ["--force", "--options", "runtime", "--timestamp", "--sign", identity, kernel]);
    run("npm", ["run", "build"]);
    const env = { ...process.env, CSC_NAME: identity, APPLE_KEYCHAIN_PROFILE: profile, XMESH_KERNEL_BIN: kernel };
    // This path intentionally uses the selected keychain profile, even if CI credentials are inherited.
    for (const key of ["CSC_LINK", "CSC_KEY_PASSWORD", "APPLE_ID", "APPLE_APP_SPECIFIC_PASSWORD", "APPLE_TEAM_ID", "APPLE_API_KEY", "APPLE_API_KEY_ID", "APPLE_API_ISSUER"]) delete env[key];
    run(path.join(ui, "node_modules", ".bin", "electron-builder"), ["--config", "electron-builder.config.mjs", "--mac", `--${process.arch}`, "--publish", "never", "-c.forceCodeSigning=true", "-c.dmg.sign=true", `-c.extraMetadata.version=${version}`], { env });
    const app = path.join(ui, "release", process.arch === "arm64" ? "mac-arm64" : "mac", "xmesh.app");
    const dmg = path.join(ui, "release", `xmesh-ui-${version}-mac-${process.arch}.dmg`);
    run("codesign", ["--verify", "--deep", "--strict", app]);
    run("codesign", ["--verify", "--strict", dmg]);
    run("xcrun", ["stapler", "validate", app]);
    run("xcrun", ["notarytool", "submit", dmg, "--keychain-profile", profile, ...keychainArgs, "--wait"]);
    run("xcrun", ["stapler", "staple", dmg]);
    run("xcrun", ["stapler", "validate", dmg]);
    run("spctl", ["--assess", "--type", "execute", "--verbose=2", app]);
    console.log(`Signed and notarized package: ${dmg}`);
  }
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
