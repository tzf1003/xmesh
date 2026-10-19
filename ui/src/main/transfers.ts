import * as fs from "node:fs";
import * as path from "node:path";
import { createHash, randomUUID } from "node:crypto";
import type { PlaygroundService } from "./playground";
import type { ShellController } from "./controller";
import type { DataResult } from "../shared/ipc";
import type { TransferRequest, TransferReply, TransferState } from "../shared/transfers";

const CHUNK = 1024 * 1024;
const LEGACY_LIMIT = 10 * CHUNK;
const hash = (b: Buffer): string => createHash("sha256").update(b).digest("hex");
type Start = Extract<TransferRequest, { op: "start" }>;
interface Job {
  request: Start;
  state: TransferState;
  abort: AbortController;
  active?: string;
}

export interface TransferDesktop {
  upload(): Promise<string | undefined>;
  download(name: string): Promise<string | undefined>;
  overwrite(remote: string, device: string): Promise<boolean>;
}

/** Local paths are issued only by native dialogs. Renderer requests carry opaque tickets. */
export class TransferService {
  private starting = false;
  private tickets = new Map<string, { instance: string; file: string }>();
  private jobs = new Map<string, Job>();
  constructor(
    private readonly playground: PlaygroundService,
    private readonly controller: Pick<ShellController, "snapshot">,
    private readonly desktop: TransferDesktop,
  ) {}

  reset(): void {
    for (const job of this.jobs.values()) {
      job.abort.abort();
      if (job.active)
        void this.playground.request({
          op: "cancel",
          instance: job.request.instance,
          id: job.active,
        });
    }
    this.jobs.clear();
    this.tickets.clear();
  }
  private check(instance: string): void {
    if (
      this.controller.snapshot.selectedInstance !== instance ||
      this.controller.snapshot.connection.kind !== "connected"
    )
      throw new Error("Selected node is no longer connected");
  }
  async request(req: TransferRequest): Promise<DataResult<TransferReply>> {
    try {
      this.check(req.instance);
      if (req.op === "pick") {
        const file = await this.desktop.upload();
        this.check(req.instance);
        if (!file) return { ok: true, data: { cancelled: true } };
        const st = await fs.promises.lstat(file);
        this.check(req.instance);
        if (!st.isFile()) throw new Error("Choose a regular file");
        const ticket = randomUUID();
        if (this.tickets.size >= 8) this.tickets.delete(this.tickets.keys().next().value!);
        this.tickets.set(ticket, { instance: req.instance, file });
        return { ok: true, data: { ticket, name: path.basename(file), bytes: st.size } };
      }
      if (req.op === "status")
        return { ok: true, data: { states: [...this.jobs.values()].map((j) => ({ ...j.state })) } };
      if (req.op === "cancel") {
        const job = this.jobs.get(req.id);
        job?.abort.abort();
        if (job?.active) await this.playground.request({ op: "cancel", instance: req.instance, id: job.active });
        return { ok: true, data: { cancelled: !!job } };
      }
      if (req.op === "retry") {
        const old = this.jobs.get(req.id);
        if (!old || old.state.direction !== "upload" || old.state.code !== "conflict") throw new Error("This transfer cannot be retried with overwrite");
        return this.request({ ...old.request, force: true });
      }
      if (this.starting) throw new Error("A file dialog or transfer is already starting");
      if ([...this.jobs.values()].some((j) => !["complete", "failed", "cancelled"].includes(j.state.phase)))
        throw new Error("A file transfer is already running");
      this.starting = true;
      try {
        const pick = req.ticket ? this.tickets.get(req.ticket) : undefined;
        if (req.direction === "upload" && (!pick || pick.instance !== req.instance))
          throw new Error("Choose the upload file again");
        const file =
          req.direction === "upload"
            ? pick!.file
            : await this.desktop.download(req.path.split(/[\\/]/).at(-1) || "download.bin");
        this.check(req.instance);
        if (!file) return { ok: true, data: { cancelled: true } };
        if (req.force && !(await this.desktop.overwrite(req.path, req.device)))
          return { ok: true, data: { cancelled: true } };
        this.check(req.instance);
        const id = randomUUID();
        const job: Job = {
          request: req,
          state: {
            id,
            direction: req.direction,
            phase: "preparing",
            name: path.basename(file),
            remote: req.path,
            device: req.device,
            bytes: 0,
          },
          abort: new AbortController(),
        };
        if (this.jobs.size >= 20) this.jobs.delete(this.jobs.keys().next().value!);
        this.jobs.set(id, job);
        void this.run(job, req, file).catch((e: unknown) => {
          job.state.phase = job.abort.signal.aborted ? "cancelled" : "failed";
          job.state.error = e instanceof Error ? e.message : String(e);
          job.state.code = (e as { code?: string }).code;
        });
        return { ok: true, data: { id } };
      } finally {
        this.starting = false;
      }
    } catch (e) {
      return { ok: false, error: { code: "transfer_error", message: e instanceof Error ? e.message : String(e) } };
    }
  }
  private async remote(
    job: Job,
    req: Start,
    tool: string,
    args: Record<string, unknown>,
  ): Promise<Record<string, unknown>> {
    this.check(req.instance);
    job.abort.signal.throwIfAborted();
    const id = randomUUID();
    job.active = id;
    try {
      const out = await this.playground.call(req.instance, id, `${req.device}/${tool}`, args, 120_000);
      job.abort.signal.throwIfAborted();
      this.check(req.instance);
      const r = out.result;
      const body =
        r.structuredContent ??
        (JSON.parse(
          r.content
            .filter((c) => c.type === "text")
            .map((c) => c.text)
            .join(""),
        ) as Record<string, unknown>);
      if (r.isError) {
        const err = new Error(JSON.stringify(body)) as Error & { code: string };
        err.code = String(body.error ?? "tool_error");
        throw err;
      }
      return body;
    } finally {
      job.active = undefined;
    }
  }
  private async run(job: Job, req: Start, file: string): Promise<void> {
    const listed = await this.playground.request({
      op: "list",
      instance: req.instance,
      device: req.device,
      pattern: `fs.${req.direction}`,
    });
    if (!listed.ok) throw new Error(listed.error.message);
    const descriptor = listed.data.tools.find((t) => t.name === `${req.device}/fs.${req.direction}`);
    if (!descriptor) throw new Error(`fs.${req.direction} is unavailable on ${req.device}`);
    const props = descriptor.inputSchema.properties as Record<string, unknown> | undefined;
    const chunked = !!props?.transfer_id && !!props.byte_offset;
    const args = { path: req.path, ...(req.cwd && { cwd: req.cwd }) };
    if (req.direction === "upload") {
      const handle = await fs.promises.open(file, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW ?? 0));
      try {
        const before = await handle.stat();
        if (!before.isFile()) throw new Error("Choose a regular file");
        job.state.total = before.size;
        if (!chunked && before.size > LEGACY_LIMIT)
          throw new Error("This node supports files up to 10 MiB; update it for chunked transfers");
        const digest = createHash("sha256");
        const scratch = Buffer.alloc(CHUNK);
        let at = 0;
        while (at < before.size) {
          job.abort.signal.throwIfAborted();
          const n = (await handle.read(scratch, 0, Math.min(CHUNK, before.size - at), at)).bytesRead;
          if (!n) throw new Error("Source changed during hashing");
          digest.update(scratch.subarray(0, n));
          at += n;
        }
        const sum = digest.digest("hex");
        job.state.sha256 = sum;
        job.state.phase = "sending";
        const after = await handle.stat();
        if (after.size !== before.size || after.mtimeMs !== before.mtimeMs)
          throw new Error("Source changed during hashing");
        let transfer: string | undefined,
          offset = 0;
        do {
          const amount = chunked ? Math.min(CHUNK, before.size - offset) : before.size;
          const data = Buffer.alloc(amount);
          let read=0;
          while(read<amount) {
            const n=(await handle.read(data,read,amount-read,offset+read)).bytesRead;
            if(!n) throw new Error("Source changed during upload");
            read+=n;
          }
          const final = offset + read === before.size;
          if (final) {
            const current = await handle.stat();
            if (current.size !== before.size || current.mtimeMs !== before.mtimeMs) throw new Error("Source changed during upload");
          }
          const body = await this.remote(job, req, "fs.upload", {
            ...args,
            data: data.toString("base64"),
            sha256: sum,
            force: req.force === true,
            ...(chunked && { byte_offset: offset, final, ...(transfer && { transfer_id: transfer }) }),
          });
          offset += read;
          job.state.bytes = offset;
          if (final) {
            job.state.phase = "checking";
            if (body.sha256 !== sum || body.bytes !== before.size || (chunked && body.complete !== true))
              throw new Error("Upload response checksum or byte count mismatch");
          } else {
            if (body.next_offset !== offset || body.complete !== false || typeof body.transfer_id !== "string")
              throw new Error("Invalid upload continuation");
            transfer = body.transfer_id;
          }
          if (final) break;
        } while (true);
      } finally {
        await handle.close();
      }
    } else {
      const previous = await fs.promises.lstat(file).catch((e: NodeJS.ErrnoException) => {
        if (e.code === "ENOENT") return undefined;
        throw e;
      });
      if (previous && !previous.isFile()) throw new Error("Local destination must be a regular file");
      const temp = path.join(path.dirname(file), `.xmesh-download-${randomUUID()}.tmp`);
      let handle: fs.promises.FileHandle | undefined;
      try {
        const stat = await this.remote(job, req, "fs.stat", args);
        if (
          stat.exists !== true ||
          stat.type !== "file" ||
          !Number.isSafeInteger(stat.size) ||
          (stat.size as number) < 0
        )
          throw new Error("Remote path is not a regular file");
        if (!chunked && (stat.size as number) > LEGACY_LIMIT)
          throw new Error("This node supports files up to 10 MiB; update it for chunked transfers");
        handle = await fs.promises.open(temp, "wx", 0o600);
        job.state.total = stat.size as number;
        job.state.phase = "sending";
        const digest = createHash("sha256");
        let offset = 0,
          transfer: string | undefined;
        do {
          const body = await this.remote(job, req, "fs.download", {
            ...args,
            ...(chunked && { byte_offset: offset, chunk_size: CHUNK, ...(transfer && { transfer_id: transfer }) }),
          });
          if (
            body.encoding !== "base64" ||
            typeof body.data !== "string" ||
            body.data.length > Math.ceil((chunked ? CHUNK : LEGACY_LIMIT) / 3) * 4
          )
            throw new Error("Invalid download encoding or size");
          const bytes = Buffer.from(body.data, "base64");
          if (bytes.toString("base64") !== body.data || body.bytes !== bytes.length || body.sha256 !== hash(bytes))
            throw new Error("Download chunk checksum mismatch");
          if (chunked && body.file_bytes !== stat.size) throw new Error("Source changed during download");
          let at=0;
          while(at<bytes.length) {
            const written=await handle.write(bytes,at,bytes.length-at);
            if(!written.bytesWritten) throw new Error("Incomplete local write");
            at+=written.bytesWritten;
          }
          digest.update(bytes);
          offset += bytes.length;
          job.state.bytes = offset;
          const eof = !chunked || body.eof === true;
          if (offset > (stat.size as number) || (chunked && body.next_offset !== offset))
            throw new Error("Invalid download offset");
          if (eof) {
            job.state.phase = "checking";
            const sum = digest.digest("hex");
            if (offset !== stat.size || (chunked ? body.file_sha256 : body.sha256) !== sum)
              throw new Error("Download file checksum mismatch");
            job.state.sha256 = sum;
            break;
          }
          if (!bytes.length || typeof body.transfer_id !== "string") throw new Error("Invalid download continuation");
          transfer = body.transfer_id;
        } while (true);
        await handle.sync();
        await handle.close();
        handle = undefined;
        job.abort.signal.throwIfAborted();
        this.check(req.instance);
        await publishDownload(temp, file, previous);
      } finally {
        await handle?.close();
        await fs.promises.rm(temp, { force: true });
      }
    }
    job.state.phase = "complete";
  }
}

const sameDestination = (a: fs.Stats, b: fs.Stats): boolean =>
  a.isFile() && b.isFile() && a.ino === b.ino && a.dev === b.dev &&
  a.size === b.size && a.mtimeMs === b.mtimeMs;

/** Publish a private inode; retain the captured destination until publication succeeds. */
export async function publishDownload(temp: string, file: string, previous?: fs.Stats): Promise<void> {
  if (!previous) {
    await fs.promises.link(temp, file);
    return;
  }
  const before = await fs.promises.lstat(file);
  if (!sameDestination(before, previous) || before.ctimeMs !== previous.ctimeMs)
    throw new Error("Local destination changed during download");
  const dir = await fs.promises.mkdtemp(path.join(path.dirname(file), ".xmesh-replaced-"));
  const backup = path.join(dir, "original");
  let captured = false;
  try {
    // Check exclusive-link support before moving the existing destination.
    const probe = path.join(dir,"probe");
    await fs.promises.link(temp,probe);
    await fs.promises.unlink(probe);
    await fs.promises.rename(file, backup);
    captured = true;
    const current = await fs.promises.lstat(backup);
    if (!sameDestination(current, previous))
      throw new Error("Local destination changed during download");
    // Exclusive publication preserves a file created after the capture.
    await fs.promises.link(temp, file);
    await fs.promises.unlink(backup);
    captured = false;
  } catch (error) {
    if (captured) {
      try {
        // Restore only into an empty name; retain both files if another writer won.
        await fs.promises.link(backup, file);
        await fs.promises.unlink(backup);
        captured = false;
      } catch {
        throw new Error(`Local destination changed; captured file retained at ${backup}`, { cause: error });
      }
    }
    throw error;
  } finally {
    if (!captured) await fs.promises.rmdir(dir);
  }
}
