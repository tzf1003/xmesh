import type { PlaygroundApi, ToolResult } from "../../shared/playground";
import type { PatInfo } from "../../shared/control";

export function observationPats(pats: readonly PatInfo[], device: string): PatInfo[] {
  return pats.filter((pat) => pat.scopes.some((scope) => {
    const [area, permission, target] = scope.split(":");
    return area === "computer" && ["observe", "control"].includes(permission ?? "") && (target === device || target === "*");
  }));
}

export interface DesktopPreview {
  source: string;
  frame: string;
  capturedAt: string;
  width: number;
  height: number;
  displays: { id: string; name: string }[];
  display: string;
  targets: {id:string;name:string;app_id:string}[];
  target: string;
}

function object(result: ToolResult): Record<string, unknown> {
  const value: unknown = result.structuredContent ?? JSON.parse(result.content.filter((c) => c.type === "text").map((c) => c.text ?? "").join(""));
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid desktop response");
  if (result.isError) throw new Error(String((value as Record<string, unknown>).message ?? (value as Record<string, unknown>).error ?? "Desktop unavailable"));
  return value as Record<string, unknown>;
}

/** The viewer uses the same authenticated MCP tools and observation policy as an Agent. */
export async function observeDesktop(api: PlaygroundApi, instance: string, device: string, display: string, signal: AbortSignal, pat?: string, target = ""): Promise<DesktopPreview> {
  const call = async (tool: string, args: Record<string, unknown>, cleanup = false): Promise<ToolResult> => {
    if (!cleanup) signal.throwIfAborted();
    const id = crypto.randomUUID();
    const cancel = (): void => { void api({ op: "cancel", instance, id }); };
    if (!cleanup) signal.addEventListener("abort", cancel, { once: true });
    try {
      const result = await api({ op: "call", instance, id, name: `${device}/computer.${tool}`, arguments: args, timeoutMs: 10000, ...(pat && { pat }) });
      if (!result.ok) throw new Error(result.error.message);
      return result.data.result;
    } finally { signal.removeEventListener("abort", cancel); }
  };
  const info = object(await call("info", {}));
  const targets = Array.isArray(info.targets) ? info.targets.flatMap((target:unknown) => {
    if (!target || typeof target!=="object") return [];
    const value=target as Record<string,unknown>;
    return typeof value.id==="string" && typeof value.name==="string" && typeof value.app_id==="string" ? [{id:value.id,name:value.name,app_id:value.app_id}] : [];
  }) : [];
  if (target && !targets.some((value)=>value.id===target)) throw new Error("Selected application window is unavailable");
  const displays = Array.isArray(info.displays) ? info.displays.flatMap((d: unknown) => {
    if (!d || typeof d !== "object") return [];
    const v = d as Record<string, unknown>;
    return typeof v.id === "string" && typeof v.name === "string" ? [{ id: v.id, name: v.name }] : [];
  }) : [];
  const selected = displays.find((d) => d.id === display)?.id ?? displays[0]?.id;
  const session = object(await call("open", { mode: "observe", ttl_seconds: 10, ...(target && {interaction:"background",target_id:target}) }));
  if (typeof session.session_id !== "string" || typeof session.desktop_epoch !== "string") throw new Error("Invalid desktop session");
  const identity = { session_id: session.session_id, desktop_epoch: session.desktop_epoch };
  try {
    signal.throwIfAborted();
    const result = await call("observe", { ...identity, max_width: 1280, ...(!target && selected && { display_id: selected }) });
    const frame = object(result);
    const image = result.content.find((c) => c.type === "image");
    if (!image || !["image/png", "image/jpeg"].includes(image.mimeType ?? "") || typeof image.data !== "string" || image.data.length > 6 * 1024 * 1024 || !/^[A-Za-z0-9+/]+={0,2}$/.test(image.data) || typeof frame.frame_id !== "string" || typeof frame.captured_at !== "string" || !Number.isSafeInteger(frame.image_width) || !Number.isSafeInteger(frame.image_height) || Number(frame.image_width) < 1 || Number(frame.image_height) < 1 || Number(frame.image_width) > 8192 || Number(frame.image_height) > 8192) throw new Error("Invalid desktop image");
    signal.throwIfAborted();
    if (target && frame.target_id!==target) throw new Error("Desktop image target does not match the selected window");
    return { source: `data:${image.mimeType};base64,${image.data}`, frame: frame.frame_id, capturedAt: frame.captured_at, width: Number(frame.image_width), height: Number(frame.image_height), displays, display: String(frame.display_id ?? selected ?? ""),targets,target };
  } finally {
    // Cleanup is also attempted after a cancelled screenshot. The short lease
    // bounds an observer if the transport disappeared before close arrived.
    try { object(await call("close", identity, true)); } catch { /* Session expiry performs remote cleanup. */ }
  }
}
