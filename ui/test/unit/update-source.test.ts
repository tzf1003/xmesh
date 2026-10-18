import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import * as fs from "node:fs";
import * as http from "node:http";
import type { AddressInfo } from "node:net";
import * as os from "node:os";
import * as path from "node:path";
import { after, before, test } from "node:test";
import { SourceError, httpSource } from "../../src/main/update/source";

const BODY = Buffer.alloc(300_000, 7);
let server: http.Server;
let base = "";
let mode = "ok";
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-update-src-"));
const sig = (): AbortSignal => new AbortController().signal;

before(async () => {
  server = http.createServer((req, res) => {
    if (req.url?.startsWith("/releases")) {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify([{ tag_name: "v0.1.0", draft: false, prerelease: false, assets: [{ name: "a", size: 1 }, { nope: true }], body: null }, { bogus: 1 }]));
    } else if (req.url === "/download/v0.1.0/file.bin") {
      if (mode === "404") {
        res.statusCode = 404;
        return res.end();
      }
      res.setHeader("content-length", String(mode === "short" ? BODY.length + 10 : BODY.length));
      res.write(BODY.subarray(0, 100_000));
      if (mode === "stall") return; // never finishes
      setTimeout(() => res.end(BODY.subarray(100_000)), 20);
    } else if (req.url === "/download/v0.1.0/SHA256SUMS.txt") {
      res.end("abc  file.bin\n");
    } else if (req.url === "/download/v0.1.0/quiet.txt") {
      res.write("abc"); // then never finishes
    } else if (req.url === "/download/v0.1.0/huge.txt") {
      // chunked: no content-length to check up front
      res.write(Buffer.alloc(600_000, 97));
      setTimeout(() => res.end(Buffer.alloc(600_000, 97)), 10);
    } else {
      res.statusCode = 500;
      res.end();
    }
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});
after(async () => {
  server.closeAllConnections();
  await new Promise((r) => server.close(r));
  fs.rmSync(tmp, { recursive: true, force: true });
});

const source = (stallMs?: number) => httpSource({ fetch: (u, i) => fetch(u, i), apiBase: base, downloadBase: `${base}/download`, ...(stallMs && { stallMs }) });

test("the release list keeps well-formed releases and drops malformed ones", async () => {
  const r = await source().releases(sig());
  assert.equal(r.length, 1);
  assert.equal(r[0]!.tag_name, "v0.1.0");
  assert.deepEqual(r[0]!.assets, [{ name: "a", size: 1 }]);
});

test("a download streams to the file, hashes it and reports progress", async () => {
  mode = "ok";
  const dest = path.join(tmp, "ok.bin");
  const seen: number[] = [];
  const got = await source().download("v0.1.0", "file.bin", dest, (p) => seen.push(p.received), sig());
  assert.equal(got.bytes, BODY.length);
  assert.equal(got.sha256, createHash("sha256").update(BODY).digest("hex"));
  assert.deepEqual(fs.readFileSync(dest), BODY);
  assert.equal(seen.at(-1), BODY.length);
  assert.ok(seen.length >= 2 && seen.every((n, i) => i === 0 || n >= seen[i - 1]!));
});

test("a failing status, a short body and a dead connection each leave no file and say why", async () => {
  const dest = path.join(tmp, "bad.bin");
  mode = "404";
  await assert.rejects(source().download("v0.1.0", "file.bin", dest, () => {}, sig()), (e: SourceError) => e.code === "http");
  assert.equal(fs.existsSync(dest), false);
  mode = "short";
  await assert.rejects(source().download("v0.1.0", "file.bin", dest, () => {}, sig()), (e: SourceError) => e.code === "network");
  assert.equal(fs.existsSync(dest), false);
  mode = "stall";
  await assert.rejects(source(150).download("v0.1.0", "file.bin", dest, () => {}, sig()), (e: SourceError) => e.code === "network" && /no data/.test(e.message));
  assert.equal(fs.existsSync(dest), false);
});

test("cancelling reports cancelled and removes the partial file", async () => {
  mode = "stall";
  const ctl = new AbortController();
  const dest = path.join(tmp, "cancel.bin");
  const run = source().download("v0.1.0", "file.bin", dest, () => ctl.abort(), ctl.signal);
  await assert.rejects(run, (e: SourceError) => e.code === "cancelled");
  assert.equal(fs.existsSync(dest), false);
});

test("only plain tags and file names are fetched", async () => {
  for (const [tag, name] of [["v0.1.0", "../x"], ["../v0.1.0", "file.bin"], ["main", "file.bin"], ["v0.1.0", "a/b"], ["v0.1.0", "x?y"]] as const) {
    await assert.rejects(source().text(tag, name, sig()), (e: SourceError) => e.code === "http", `${tag}/${name}`);
  }
  assert.equal(await source().text("v0.1.0", "SHA256SUMS.txt", sig()), "abc  file.bin\n");
});

test("a text file without a content-length is still capped while it is read", async () => {
  await assert.rejects(source().text("v0.1.0", "huge.txt", sig()), (e: SourceError) => e.code === "too_large");
});

test("a text file whose server goes quiet times out instead of hanging", async () => {
  await assert.rejects(source(150).text("v0.1.0", "quiet.txt", sig()), (e: SourceError) => e.code === "network" && /no data/.test(e.message));
});

test("an unreachable host is a network error", async () => {
  const s = httpSource({ fetch: (u, i) => fetch(u, i), apiBase: "http://127.0.0.1:1", downloadBase: "http://127.0.0.1:1" });
  await assert.rejects(s.releases(sig()), (e: SourceError) => e.code === "network");
});
