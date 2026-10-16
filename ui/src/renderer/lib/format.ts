// Formatting helpers. Pure and locale-free: text that needs words ("ago") is
// composed by the caller from the unit these return.

export function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/** Local time as `YYYY-MM-DD HH:mm:ss`; the input is an RFC 3339 timestamp. Unparsable input comes back unchanged. */
export function formatTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
}

export type AgoUnit = "s" | "m" | "h" | "d";

/** How long before `now` the timestamp was, in the largest whole unit; `undefined` when unparsable. */
export function ago(iso: string | null | undefined, now: number = Date.now()): { n: number; unit: AgoUnit } | undefined {
  if (!iso) return undefined;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return undefined;
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return { n: s, unit: "s" };
  if (s < 3600) return { n: Math.floor(s / 60), unit: "m" };
  if (s < 86400) return { n: Math.floor(s / 3600), unit: "h" };
  return { n: Math.floor(s / 86400), unit: "d" };
}

/** `842 ms`, `1.2 s`, `3 m 04 s`. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`;
  const s = Math.round(ms / 1000);
  if (s < 3600) return `${Math.floor(s / 60)} m ${pad2(s % 60)} s`;
  return `${Math.floor(s / 3600)} h ${pad2(Math.floor((s % 3600) / 60))} m`;
}

/** Idle seconds as a short span: `12 s`, `5 m`, `2 h`. */
export function formatIdle(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  if (seconds < 60) return `${Math.floor(seconds)} s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} m`;
  return `${Math.floor(seconds / 3600)} h`;
}

/** `used / max` as a 0..1 fraction, clamped; 0 when max is not positive. */
export function fraction(used: number, max: number): number {
  if (!(max > 0) || !(used >= 0)) return 0;
  return Math.min(1, used / max);
}

/** A value from a config diff, short enough for a table cell. */
export function formatValue(v: unknown, max = 120): string {
  if (v === undefined) return "";
  let s: string;
  try {
    s = typeof v === "string" ? JSON.stringify(v) : (JSON.stringify(v) ?? String(v));
  } catch {
    s = String(v);
  }
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}
