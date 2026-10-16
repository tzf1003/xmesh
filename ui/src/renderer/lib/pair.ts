import type { PairDocument } from "../../shared/control";
import { parsePairImport } from "../../shared/ipc";

export const PAIR_TAG_CHOICES = ["tag:peer", "tag:device", "tag:ingress"] as const;

/** Lowercase hex without colons or spaces, for comparing fingerprints the way the kernel does. */
export function normalizeFingerprint(s: string): string {
  return s.replace(/[:\s]/g, "").toLowerCase();
}

/** `ab:cd:…` grouping for display. */
export function groupFingerprint(s: string): string {
  const n = normalizeFingerprint(s);
  return (n.match(/.{1,2}/g) ?? []).join(":");
}

export type PairParse = { ok: true; pair: PairDocument } | { ok: false };

/** Reads a pasted pair document. Only the shape the kernel accepts gets through. */
export function parsePairText(text: string): PairParse {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { ok: false };
  }
  // Accept both the bare document and the `{ "pair": … }` export response.
  const doc = typeof raw === "object" && raw !== null && "pair" in raw ? (raw as { pair: unknown }).pair : raw;
  // The fingerprint field is only checked for shape here; the user retypes it separately.
  const probe = parsePairImport({ pair: doc, fingerprint: "00".repeat(32) });
  return probe ? { ok: true, pair: probe.pair } : { ok: false };
}

/** True when what the user typed is a well-formed fingerprint equal to the one the document declares. */
export function fingerprintMatches(typed: string, declared: string): boolean {
  const a = normalizeFingerprint(typed);
  return /^[0-9a-f]{64}$/.test(a) && a === normalizeFingerprint(declared);
}
