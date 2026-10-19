import { createHash, randomUUID } from "node:crypto";
import type { ToolDescriptor } from "../src/shared/playground";
const hash = (data: Buffer) => createHash("sha256").update(data).digest("hex");
export class FakeWorkspace {
  readonly files = new Map<string, Buffer>();
  readonly uploads = new Map<string, Buffer[]>();
  readonly terminals = new Map<
    string,
    { id: string; owner: string; name: string; state: string; cols: number; rows: number; cwd: string; output: string }
  >();
  corruptDownload = false;
  delayMs = 0;
  chunked = true;
  tools(): ToolDescriptor[] {
    const defs: Record<string, string[]> = {
      "pty.open": ["shell", "cwd", "env", "cols", "rows", "name"],
      "pty.list": [],
      "pty.read": ["session_id", "cursor", "wait_ms", "max_bytes"],
      "pty.write": ["session_id", "data"],
      "pty.resize": ["session_id", "cols", "rows"],
      "pty.signal": ["session_id", "signal"],
      "pty.transfer": ["session_id", "to_caller"],
      "pty.close": ["session_id"],
      "fs.stat": ["path", "cwd"],
      "fs.list": ["path", "cwd", "depth"],
      "fs.upload": [
        "path",
        "cwd",
        "data",
        "sha256",
        "force",
        ...(this.chunked ? ["transfer_id", "byte_offset", "final"] : []),
      ],
      "fs.download": ["path", "cwd", ...(this.chunked ? ["transfer_id", "byte_offset", "chunk_size"] : [])],
    };
    return Object.entries(defs).map(([name, fields]) => ({
      name: `local/${name}`,
      description: name,
      inputSchema: { type: "object", properties: Object.fromEntries(fields.map((f) => [f, { type: "string" }])) },
    }));
  }
  async call(
    name: string,
    a: Record<string, unknown>,
    owner: string,
    signal: AbortSignal,
  ): Promise<Record<string, unknown>> {
    const wait = async (ms: number) =>
      new Promise<void>((resolve, reject) => {
        const end = () => {
          signal.removeEventListener("abort", abort);
          resolve();
        };
        const timer = setTimeout(end, ms);
        const abort = () => {
          clearTimeout(timer);
          reject(new Error("cancelled"));
        };
        signal.addEventListener("abort", abort, { once: true });
      });
    const p = String(a.path);
    if (name.startsWith("fs.") && this.delayMs) await wait(this.delayMs);
    if (name === "fs.stat") {
      const b = this.files.get(p);
      return { exists: !!b, type: "file", size: b?.length ?? 0, resolved_path: p };
    }
    if (name === "fs.list")
      return {
        resolved_path: p === "." ? "/work" : p,
        entries: [...this.files].map(([file, b]) => ({ name: file.split("/").at(-1), type: "file", size: b.length })),
      };
    if (name === "fs.upload") {
      if (this.files.has(p) && !a.force) return { error: "conflict", op: "fs.upload", path: p };
      const id = String(a.transfer_id ?? randomUUID());
      const parts = this.uploads.get(id) ?? [];
      const offset = parts.reduce((s, b) => s + b.length, 0);
      if (a.byte_offset !== undefined && a.byte_offset !== offset)
        return { error: "invalid_args", field: "byte_offset" };
      parts.push(Buffer.from(String(a.data), "base64"));
      const all = Buffer.concat(parts);
      if (a.final !== false) {
        if (a.sha256 !== hash(all)) {
          this.uploads.delete(id);
          return { error: "invalid_args", field: "sha256" };
        }
        this.files.set(p, all);
        this.uploads.delete(id);
        return { resolved_path: p, sha256: hash(all), bytes: all.length, complete: true };
      }
      this.uploads.set(id, parts);
      return { transfer_id: id, next_offset: all.length, complete: false, bytes: all.length };
    }
    if (name === "fs.download") {
      const all = this.files.get(p);
      if (!all) return { error: "not_found" };
      const start = Number(a.byte_offset ?? 0),
        size = Number(a.chunk_size ?? all.length);
      const b = all.subarray(start, start + size),
        next = start + b.length,
        eof = next === all.length;
      return {
        resolved_path: p,
        encoding: "base64",
        data: b.toString("base64"),
        sha256: this.corruptDownload ? "0".repeat(64) : hash(b),
        bytes: b.length,
        next_offset: next,
        file_bytes: all.length,
        eof,
        ...(eof ? { file_sha256: hash(all) } : { transfer_id: String(a.transfer_id ?? randomUUID()) }),
      };
    }
    if (name === "pty.open") {
      const id = `p-${randomUUID().slice(0, 8)}`;
      this.terminals.set(id, {
        id,
        owner,
        name: String(a.name ?? "shell"),
        state: "active",
        cols: Number(a.cols),
        rows: Number(a.rows),
        cwd: String(a.cwd ?? "/work"),
        output: "\x1b[32mxmesh terminal\x1b[0m\r\n$ ",
      });
      return { session_id: id, owner, pid: 123, resolved_path: "/work" };
    }
    if (name === "pty.list")
      return {
        caller_id: owner,
        sessions: [...this.terminals.values()].map(({ output: _output, ...s }) => ({
          ...s,
          idle_s: 0,
          pid: s.state === "closed" ? 0 : 123,
        })),
      };
    const terminal = this.terminals.get(String(a.session_id));
    if (!terminal) return { error: "session_not_found" };
    if (name === "pty.read") {
      let data = terminal.output.slice(Number(a.cursor ?? 0));
      if (!data && terminal.state !== "closed") {
        await wait(100);
        data = terminal.output.slice(Number(a.cursor ?? 0));
      }
      return { data, next_cursor: terminal.output.length, eof: terminal.state === "closed", truncated: false };
    }
    if (name === "pty.transfer" && terminal.state === "orphaned") {
      terminal.owner = String(a.to_caller);
      terminal.state = "active";
      return { owner: terminal.owner, state: "active" };
    }
    if (terminal.owner !== owner) return { error: "not_owner" };
    if (name === "pty.write") {
      terminal.output += String(a.data).replace(/\r/g, "\r\n$ ");
      return { written: String(a.data).length };
    }
    if (name === "pty.resize") {
      terminal.cols = Number(a.cols);
      terminal.rows = Number(a.rows);
      return { cols: terminal.cols, rows: terminal.rows };
    }
    if (name === "pty.close") {
      terminal.state = "closed";
      return { state: "closed" };
    }
    if (name === "pty.signal") {
      terminal.output += `\r\n[${String(a.signal)}]\r\n`;
      return { sent: true };
    }
    if (name === "pty.transfer") {
      terminal.owner = String(a.to_caller);
      return { owner: terminal.owner, state: terminal.state };
    }
    return {};
  }
}
