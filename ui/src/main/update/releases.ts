// Reading the GitHub releases of the project: which one is newest for this machine, which installer file to
// fetch, and what hash it must have. Pure functions; the network lives in source.ts.

import type { UpdateRelease } from "../../shared/update";
import { type Version, compareVersions, formatVersion, parseVersion } from "../../shared/version";

/** The part of a GitHub release this module uses. */
export interface RawRelease {
  tag_name: string;
  draft: boolean;
  prerelease: boolean;
  published_at: string | null;
  body: string | null;
  assets: RawAsset[];
}

export interface RawAsset {
  name: string;
  size: number;
  /** `sha256:<hex>`; GitHub fills it in for assets uploaded recently. */
  digest?: string | null;
}

export type InstallerPlatform = "win" | "mac" | "linux";

export interface Target {
  platform: InstallerPlatform;
  arch: "x64" | "arm64";
}

/** Maps Node's platform and arch to the installer naming; undefined where no installer is published. */
export function targetFor(platform: NodeJS.Platform, arch: string): Target | undefined {
  const p = platform === "win32" ? "win" : platform === "darwin" ? "mac" : platform === "linux" ? "linux" : undefined;
  const a = arch === "x64" || arch === "arm64" ? arch : undefined;
  return p !== undefined && a !== undefined ? { platform: p, arch: a } : undefined;
}

/** Linux has no UI installer: the release's `install-linux.sh` (in SHA256SUMS.txt) updates the kernel and the web panel in place. */
export const LINUX_INSTALLER = "install-linux.sh";

/** `xmesh-ui-0.1.0-beta.3-win-x64.exe`, the artifactName in electron-builder.config.mjs. */
export function installerName(version: Version, t: Target): string {
  if (t.platform === "linux") return LINUX_INSTALLER;
  return `xmesh-ui-${formatVersion(version)}-${t.platform}-${t.arch}.${t.platform === "win" ? "exe" : "dmg"}`;
}

export const CHECKSUM_FILE = "SHA256SUMS.txt";

export interface Candidate {
  release: UpdateRelease;
  tag: string;
  /** Installer file name in the release. */
  asset: string;
  /** Hash GitHub reports for the installer, when it does. */
  digest?: string;
}

const NOTES_MAX = 2000;

/** Release notes become plain text for a card: markdown stays as typed, control characters go, the length is capped. */
export function cleanNotes(body: string | null): string {
  const text = (body ?? "").replace(/\r\n?/g, "\n").replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, "").trim();
  return text.length > NOTES_MAX ? `${text.slice(0, NOTES_MAX).trimEnd()}…` : text;
}

/**
 * The newest release that is newer than `current`, allowed by `prerelease`, and has an installer plus a
 * checksum for `target`. Order in the API answer does not matter: versions are compared. Drafts and tags
 * that are not versions are skipped.
 */
export function pickCandidate(releases: RawRelease[], current: string, prerelease: boolean, target: Target): Candidate | undefined {
  const now = parseVersion(current);
  let best: { v: Version; c: Candidate } | undefined;
  for (const r of releases) {
    if (r.draft || (!prerelease && r.prerelease)) continue;
    const v = parseVersion(r.tag_name);
    if (v === undefined || r.tag_name !== `v${formatVersion(v)}`) continue;
    // A pre-release tag with no "prerelease" mark is still a pre-release for the stable channel.
    if (!prerelease && v.pre.length > 0) continue;
    if (now !== undefined && compareVersions(v, now) <= 0) continue;
    if (best !== undefined && compareVersions(v, best.v) <= 0) continue;
    const asset = installerName(v, target);
    const found = r.assets.find((a) => a.name === asset);
    if (found === undefined || !r.assets.some((a) => a.name === CHECKSUM_FILE)) continue;
    best = {
      v,
      c: {
        tag: r.tag_name,
        asset,
        ...(typeof found.digest === "string" && found.digest !== "" && { digest: found.digest }),
        release: {
          version: formatVersion(v),
          tag: r.tag_name,
          prerelease: r.prerelease || v.pre.length > 0,
          publishedAt: r.published_at ?? "",
          size: found.size,
          notes: cleanNotes(r.body),
        },
      },
    };
  }
  return best?.c;
}

/** Parses `sha256sum` output (`<hex>  <name>` or `<hex> *<name>`) into name -> lowercase hex. */
export function parseChecksums(text: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const line of text.split(/\r?\n/)) {
    const m = /^([0-9a-fA-F]{64})\s+\*?(\S.*?)\s*$/.exec(line);
    if (m) out.set(m[2]!, m[1]!.toLowerCase());
  }
  return out;
}

/** `sha256:<hex>` as GitHub writes it -> lowercase hex. */
export function parseDigest(digest: string | undefined): string | undefined {
  const m = /^sha256:([0-9a-fA-F]{64})$/.exec(digest ?? "");
  return m ? m[1]!.toLowerCase() : undefined;
}

export type HashDecision = { ok: true; sha256: string } | { ok: false; reason: "missing" | "conflict" };

/**
 * The hash the download must have. The API digest and SHA256SUMS.txt both come from the release; when both are
 * there they must agree, and when neither is there the installer is not fetched.
 */
export function expectedHash(digest: string | undefined, sums: Map<string, string>, asset: string): HashDecision {
  const a = parseDigest(digest);
  const b = sums.get(asset);
  if (a === undefined && b === undefined) return { ok: false, reason: "missing" };
  if (a !== undefined && b !== undefined && a !== b) return { ok: false, reason: "conflict" };
  return { ok: true, sha256: (a ?? b)! };
}
