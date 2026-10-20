import type { ToolDescriptor, ToolResult } from "../src/shared/playground";

/** An explicitly synthetic read-only desktop for viewer protocol tests. */
export class FakeComputer {
  captures = 0;
  readonly sessions = new Set<string>();
  readonly modes: unknown[] = [];
  deny = false;
  image = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j4p0AAAAASUVORK5CYII=";
  width = 1;
  height = 1;
  targets: {id:string;name:string;app_id:string}[]=[];
  readonly sessionTargets = new Map<string,string>();
  private serial = 0;
  tools(): ToolDescriptor[] {
    return ["info", "open", "observe", "close"].map((name) => ({ name: `local/computer.${name}`, inputSchema: { type: "object" }, annotations: { readOnlyHint: true } }));
  }
  call(tool: string, args: Record<string, unknown>): ToolResult {
    let body: Record<string, unknown>;
    const content: ToolResult["content"] = [];
    if (this.deny) body = { error: "policy_denied", message: "Observation denied by fixture policy" };
    else if (tool === "info") body = { available: true, desktop_epoch: "fixture-epoch", displays: [{ id: "fixture-display", name: "Fixture display" }], targets:this.targets };
    else if (tool === "open") {
      this.modes.push(args.mode);
      const id = `cs-fixture-${++this.serial}`;
      this.sessions.add(id);
      this.sessionTargets.set(id,String(args.target_id ?? ""));
      body = { session_id: id, desktop_epoch: "fixture-epoch", mode: "observe" };
    } else if (!this.sessions.has(String(args.session_id))) body = { error: "session_not_found" };
    else if (tool === "close") { this.sessions.delete(String(args.session_id));this.sessionTargets.delete(String(args.session_id)); body = { closed: true }; }
    else if (tool === "observe") {
      this.captures++;
      body = { frame_id: `f-${this.captures}`, desktop_epoch: "fixture-epoch", display_id: "fixture-display", captured_at: new Date().toISOString(), image_width: this.width, image_height: this.height, target_id:this.sessionTargets.get(String(args.session_id)) };
      content.push({ type: "image", mimeType: "image/png", data: this.image });
    } else body = { error: "policy_denied" };
    return { structuredContent: body, content: [{ type: "text", text: JSON.stringify(body) }, ...content], isError: typeof body.error === "string" };
  }
}
