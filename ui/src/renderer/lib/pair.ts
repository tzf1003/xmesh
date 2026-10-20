import type { PairDocument } from "../../shared/control";
import { type ActionError, parsePairImport, parsePairPassword } from "../../shared/ipc";
import type { Key } from "../messages";

export const PAIR_TAG_CHOICES = ["tag:peer", "tag:device", "tag:ingress", "tag:admin"] as const;

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

/** Whether a connection password a user has typed meets the kernel's rules. */
export function pairPasswordValid(text: string): boolean {
  return parsePairPassword(text) !== undefined;
}

/** A pairing failure worded for people: the message key and its parameters. */
export interface PairFailure {
  key: Key;
  params?: Record<string, number>;
}

/**
 * The panel's own wording for the pairing errors (CTR-CTL-08). Undefined for anything else, which the
 * generic error text handles. A wrong password and a man in the middle are worded the same way.
 */
export function pairFailure(e: ActionError): PairFailure | undefined {
  if (e.code === "unavailable") return { key: "pair.err.unavailable" };
  if (e.code === "pair_rejected") {
    switch (e.reason) {
      case "wrong_password":
        return { key: "pair.err.wrongPassword" };
      case "no_password":
        return { key: "pair.err.noPassword" };
      case "unsupported_peer":
        return { key: "pair.err.unsupportedPeer" };
      case "unreachable":
        return { key: "pair.err.unreachable" };
      case "bad_peer":
        return { key: "pair.err.badPeer" };
    }
  }
  if (e.code === "rate_limited") {
    return { key: "pair.err.rateLimited", params: { seconds: Math.max(1, Math.ceil((e.retry_after_ms ?? 0) / 1000)) } };
  }
  if (e.code === "invalid_args" && e.field === "password") return { key: "pair.err.passwordRules" };
  if (e.code === "invalid_args" && e.reason === "fingerprint_mismatch") return { key: "pair.err.fingerprint" };
  if (e.code === "not_found") return { key: "pair.err.notFound" };
  return undefined;
}
