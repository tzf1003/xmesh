// Incremental Server-Sent Events parser (WHATWG "event stream" format).

export interface SseMessage {
  id?: string;
  event: string;
  data: string;
}

export class SseParser {
  private buffer = "";
  private eventName = "";
  private dataLines: string[] = [];
  private lastId: string | undefined;
  private pendingId: string | undefined;

  /** Feeds a decoded text chunk and returns the messages completed by it. */
  push(chunk: string): SseMessage[] {
    this.buffer += chunk;
    const out: SseMessage[] = [];
    for (;;) {
      const m = /\r\n|\n|\r/.exec(this.buffer);
      if (!m) break;
      // A trailing "\r" may be the first half of "\r\n"; wait for more input.
      if (m[0] === "\r" && m.index + 1 === this.buffer.length) break;
      const line = this.buffer.slice(0, m.index);
      this.buffer = this.buffer.slice(m.index + m[0].length);
      const msg = this.line(line);
      if (msg) out.push(msg);
    }
    return out;
  }

  private line(line: string): SseMessage | undefined {
    if (line === "") {
      if (this.dataLines.length === 0) {
        this.eventName = "";
        return undefined;
      }
      if (this.pendingId !== undefined) this.lastId = this.pendingId;
      const msg: SseMessage = {
        event: this.eventName === "" ? "message" : this.eventName,
        data: this.dataLines.join("\n"),
      };
      if (this.lastId !== undefined) msg.id = this.lastId;
      this.eventName = "";
      this.dataLines = [];
      this.pendingId = undefined;
      return msg;
    }
    if (line.startsWith(":")) return undefined;
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "event":
        this.eventName = value;
        break;
      case "data":
        this.dataLines.push(value);
        break;
      case "id":
        if (!value.includes("\0")) this.pendingId = value;
        break;
      default:
        break;
    }
    return undefined;
  }
}
