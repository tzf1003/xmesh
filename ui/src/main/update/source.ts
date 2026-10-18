// The network side of the update check: the release list, the checksum file and the installer download.
// `fetch` is Electron's `net.fetch` in the app (it follows the system proxy) and Node's in tests.

import { createHash } from "node:crypto";
import * as fs from "node:fs";
import type { RawRelease } from "./releases";

export type FetchFn = (url: string, init?: { signal?: AbortSignal }) => Promise<Response>;

export interface Progress {
  received: number;
  total: number;
}

export interface UpdateSource {
  releases(signal: AbortSignal): Promise<RawRelease[]>;
  /** A small text asset of a release, such as SHA256SUMS.txt. */
  text(tag: string, name: string, signal: AbortSignal): Promise<string>;
  /** Streams an asset to `dest`, hashing as it goes. Throws SourceError. */
  download(tag: string, name: string, dest: string, onProgress: (p: Progress) => void, signal: AbortSignal): Promise<{ sha256: string; bytes: number }>;
}

export class SourceError extends Error {
  constructor(
    readonly code: "network" | "http" | "cancelled" | "too_large",
    message: string,
  ) {
    super(message);
    this.name = "SourceError";
  }
}

export const GITHUB_API = "https://api.github.com/repos/tzf1003/xmesh";
export const GITHUB_DOWNLOAD = "https://github.com/tzf1003/xmesh/releases/download";

/** An installer is about 150 MB; this only stops a runaway answer. */
export const MAX_DOWNLOAD_BYTES = 1024 * 1024 * 1024;
/** No data for this long counts as a dead connection. */
export const STALL_MS = 30_000;
const TAG = /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;
const ASSET = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export interface HttpSourceOptions {
  fetch: FetchFn;
  apiBase?: string;
  downloadBase?: string;
  stallMs?: number;
}

/** GitHub releases, or a directory of the same shape (`<base>/releases` and `<base>/download/<tag>/<name>`) for tests. */
export function httpSource(opts: HttpSourceOptions): UpdateSource {
  const apiBase = (opts.apiBase ?? GITHUB_API).replace(/\/+$/, "");
  const downloadBase = (opts.downloadBase ?? GITHUB_DOWNLOAD).replace(/\/+$/, "");
  const stallMs = opts.stallMs ?? STALL_MS;

  const assetUrl = (tag: string, name: string): string => {
    if (!TAG.test(tag) || !ASSET.test(name)) throw new SourceError("http", `unexpected release file ${tag}/${name}`);
    return `${downloadBase}/${tag}/${name}`;
  };

  const get = async (url: string, signal: AbortSignal): Promise<Response> => {
    let res: Response;
    try {
      res = await opts.fetch(url, { signal });
    } catch (e) {
      if (signal.aborted) throw new SourceError("cancelled", "cancelled");
      throw new SourceError("network", e instanceof Error ? e.message : String(e));
    }
    if (!res.ok) {
      // Drain nothing: the body is never read, the signal or the GC closes it.
      throw new SourceError("http", `${new URL(url).host} answered ${res.status}`);
    }
    return res;
  };

  return {
    async releases(signal) {
      const res = await get(`${apiBase}/releases?per_page=30`, signal);
      let json: unknown;
      try {
        json = await res.json();
      } catch (e) {
        throw new SourceError(signal.aborted ? "cancelled" : "network", e instanceof Error ? e.message : String(e));
      }
      if (!Array.isArray(json)) return [];
      return json.filter(isRawRelease).map((r) => ({
        ...r,
        assets: r.assets.filter((a): a is RawRelease["assets"][number] => typeof a === "object" && a !== null && typeof a.name === "string" && typeof a.size === "number"),
      }));
    },

    async text(tag, name, signal) {
      const res = await get(assetUrl(tag, name), signal);
      const len = Number(res.headers.get("content-length") ?? 0);
      if (len > 1024 * 1024) throw new SourceError("too_large", `${name} is too large`);
      try {
        return await res.text();
      } catch (e) {
        throw new SourceError(signal.aborted ? "cancelled" : "network", e instanceof Error ? e.message : String(e));
      }
    },

    async download(tag, name, dest, onProgress, signal) {
      const stall = new AbortController();
      const both = AbortSignal.any([signal, stall.signal]);
      let timer: NodeJS.Timeout | undefined;
      const arm = (): void => {
        clearTimeout(timer);
        timer = setTimeout(() => stall.abort(), stallMs);
      };
      arm();
      let out: fs.WriteStream | undefined;
      try {
        const res = await get(assetUrl(tag, name), both);
        const total = Number(res.headers.get("content-length") ?? 0);
        if (total > MAX_DOWNLOAD_BYTES) throw new SourceError("too_large", `${name} is too large`);
        if (res.body === null) throw new SourceError("network", "empty response");
        const hash = createHash("sha256");
        out = fs.createWriteStream(dest, { flags: "w", mode: 0o600 });
        const failed = new Promise<never>((_, reject) => out!.once("error", reject));
        failed.catch(() => {});
        let received = 0;
        const reader = res.body.getReader();
        for (;;) {
          const { done, value } = await Promise.race([reader.read(), failed]);
          if (done) break;
          arm();
          received += value.byteLength;
          if (received > MAX_DOWNLOAD_BYTES) throw new SourceError("too_large", `${name} is too large`);
          hash.update(value);
          if (!out.write(value)) await Promise.race([new Promise<void>((r) => out!.once("drain", r)), failed]);
          onProgress({ received, total });
        }
        await Promise.race([new Promise<void>((r) => out!.end(r)), failed]);
        if (total > 0 && received !== total) throw new SourceError("network", `got ${received} of ${total} bytes`);
        return { sha256: hash.digest("hex"), bytes: received };
      } catch (e) {
        out?.destroy();
        fs.rmSync(dest, { force: true });
        if (signal.aborted) throw new SourceError("cancelled", "cancelled");
        if (stall.signal.aborted) throw new SourceError("network", `no data for ${Math.round(stallMs / 1000)} seconds`);
        if (e instanceof SourceError) throw e;
        throw new SourceError("network", e instanceof Error ? e.message : String(e));
      } finally {
        clearTimeout(timer);
      }
    },
  };
}

function isRawRelease(v: unknown): v is RawRelease {
  if (typeof v !== "object" || v === null) return false;
  const r = v as Record<string, unknown>;
  return typeof r.tag_name === "string" && typeof r.draft === "boolean" && typeof r.prerelease === "boolean" && Array.isArray(r.assets);
}
