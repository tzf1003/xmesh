// Pure helpers behind the tailnet pages: turning form text into the validated
// request shapes, and small decisions the views share.

import { parseDerpRegion, parseMeshTags, type DerpRegionInput, type MeshStatus } from "../../shared/mesh";

/** Splits `tag:a, tag:b tag:c` on commas and whitespace; empty input is the empty list. */
export function splitList(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

export type TagsParse = { ok: true; tags: string[] } | { ok: false };

/** Empty tags clear a device's network labels or create an untagged onboarding key. */
export function parseTagText(text: string, allowEmpty: boolean): TagsParse {
  const tags = parseMeshTags(splitList(text), allowEmpty);
  return tags === undefined ? { ok: false } : { ok: true, tags };
}

export interface RegionForm {
  code: string;
  name: string;
  host: string;
  ipv4: string;
  ipv6: string;
  derpPort: string;
  stunPort: string;
  certName: string;
}

export const EMPTY_REGION: RegionForm = { code: "", name: "", host: "", ipv4: "", ipv6: "", derpPort: "", stunPort: "", certName: "" };

export type RegionProblem = "empty" | "invalid" | "duplicate";

export type RegionsParse = { ok: true; regions: DerpRegionInput[] } | { ok: false; problem: RegionProblem; row?: number };

const blank = (s: string): boolean => s.trim() === "";

/** Blank optional fields are left out; the CLI picks the id and the default ports. */
export function regionsFromForms(forms: RegionForm[]): RegionsParse {
  if (forms.length === 0) return { ok: false, problem: "empty" };
  const out: DerpRegionInput[] = [];
  for (let i = 0; i < forms.length; i++) {
    const f = forms[i]!;
    const raw: Record<string, unknown> = { code: f.code.trim(), name: f.name.trim(), host: f.host.trim() };
    if (!blank(f.ipv4)) raw.ipv4 = f.ipv4.trim();
    if (!blank(f.ipv6)) raw.ipv6 = f.ipv6.trim();
    if (!blank(f.certName)) raw.certName = f.certName.trim();
    for (const [key, text] of [
      ["derpPort", f.derpPort],
      ["stunPort", f.stunPort],
    ] as const) {
      if (blank(text)) continue;
      if (!/^\d{1,5}$/.test(text.trim())) return { ok: false, problem: "invalid", row: i };
      raw[key] = Number(text.trim());
    }
    const r = parseDerpRegion(raw);
    if (r === undefined) return { ok: false, problem: "invalid", row: i };
    if (out.some((x) => x.code === r.code)) return { ok: false, problem: "duplicate", row: i };
    out.push(r);
  }
  return { ok: true, regions: out };
}

/** Headscale keeps its policy in a file or only through the API; `policyWrite` says which. */
export function canWritePolicy(status: MeshStatus | undefined): boolean {
  return status?.capabilities.policyWrite === true;
}

export function ownerPlaceholder(provider: string | undefined): string {
  return provider === "headscale" ? "alice@" : "autogroup:admin";
}

export const TTL_CHOICES = ["30m", "1h", "6h", "24h"] as const;
export const POLICY_TEMPLATE_PORT_MAX = 65535;

/** A fixed set of lines for the first lines of a diff, so a huge diff does not freeze the page. */
export function clipLines(text: string, max: number): { text: string; clipped: number } {
  const lines = text.split("\n");
  if (lines.length <= max) return { text, clipped: 0 };
  return { text: lines.slice(0, max).join("\n"), clipped: lines.length - max };
}
