// Semantic version comparison for release tags (`v0.1.0-beta.3`) and the running app's version (`0.1.0-beta.3`).

export interface Version {
  major: number;
  minor: number;
  patch: number;
  /** Dot-separated pre-release identifiers; empty for a stable release. */
  pre: (number | string)[];
}

const SEMVER = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z.-]+)?$/;

/** Parses `1.2.3`, `v1.2.3-beta.4` and the like; undefined for anything else. */
export function parseVersion(text: string): Version | undefined {
  const m = SEMVER.exec(text.trim());
  if (!m) return undefined;
  const pre = (m[4] ?? "").split(".").filter((x) => x !== "").map((x) => (/^\d+$/.test(x) ? Number(x) : x));
  return { major: Number(m[1]), minor: Number(m[2]), patch: Number(m[3]), pre };
}

/** The version as it is written in an installer file name: no `v`, no build metadata. */
export function formatVersion(v: Version): string {
  return `${v.major}.${v.minor}.${v.patch}${v.pre.length > 0 ? `-${v.pre.join(".")}` : ""}`;
}

/** Negative when `a` is older than `b`, positive when newer, 0 when equal (semver 2.0 precedence). */
export function compareVersions(a: Version, b: Version): number {
  for (const k of ["major", "minor", "patch"] as const) {
    if (a[k] !== b[k]) return a[k] < b[k] ? -1 : 1;
  }
  // A version without a pre-release is newer than one with.
  if (a.pre.length === 0 || b.pre.length === 0) return a.pre.length === b.pre.length ? 0 : a.pre.length === 0 ? 1 : -1;
  for (let i = 0; i < Math.max(a.pre.length, b.pre.length); i++) {
    const x = a.pre[i];
    const y = b.pre[i];
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    if (x === y) continue;
    // Numeric identifiers sort below alphanumeric ones.
    if (typeof x === "number" && typeof y === "number") return x < y ? -1 : 1;
    if (typeof x === "number") return -1;
    if (typeof y === "number") return 1;
    return x < y ? -1 : 1;
  }
  return 0;
}

/** True when both parse and `a` is strictly newer than `b`. */
export function isNewer(a: string, b: string): boolean {
  const x = parseVersion(a);
  const y = parseVersion(b);
  return x !== undefined && y !== undefined && compareVersions(x, y) > 0;
}

/** True when both parse and name the same version, ignoring a leading `v`. */
export function sameVersion(a: string, b: string): boolean {
  const x = parseVersion(a);
  const y = parseVersion(b);
  return x !== undefined && y !== undefined && compareVersions(x, y) === 0;
}
