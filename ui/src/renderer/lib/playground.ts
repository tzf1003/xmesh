import type { ToolDescriptor } from "../../shared/playground";

export function objectArgs(text: string): Record<string, unknown> {
  const v: unknown = JSON.parse(text);
  if (!v || typeof v !== "object" || Array.isArray(v)) throw new Error("arguments must be a JSON object");
  return v as Record<string, unknown>;
}

export function argumentError(tool: ToolDescriptor, text: string): string | undefined {
  try {
    const args = objectArgs(text);
    if (JSON.stringify(args).includes('"[redacted]"')) return "[redacted]";
    return validateBasic(tool.inputSchema, args, "$", 0);
  } catch (e) { return e instanceof Error ? e.message : String(e); }
}

export function initialArgs(tool: ToolDescriptor): Record<string, unknown> {
  const props = tool.inputSchema.properties as Record<string, Record<string, unknown>> | undefined;
  return Object.fromEntries(Object.entries(props ?? {}).filter(([, p]) => p.default !== undefined).map(([k, p]) => [k, p.default]));
}

export function toolGroup(name: string): string { return name.split("/").at(-1)!.split(".")[0]!; }

// A CSP-safe form check. Full JSON Schema validation remains the remote tool's responsibility.
function validateBasic(schema: Record<string, unknown>, value: unknown, path: string, depth: number): string | undefined {
  if (depth > 20) return undefined;
  const types = Array.isArray(schema.type) ? schema.type : schema.type ? [schema.type] : [];
  const matches = (t: unknown): boolean => t === "null" ? value === null : t === "array" ? Array.isArray(value) : t === "object" ? !!value && typeof value === "object" && !Array.isArray(value) : t === "integer" ? Number.isInteger(value) : typeof value === t;
  if (types.length && !types.some(matches)) return `${path}: ${types.join(" | ")}`;
  if (Array.isArray(schema.enum) && !schema.enum.some((x) => JSON.stringify(x) === JSON.stringify(value))) return `${path}: enum`;
  if (typeof value === "number" && ((typeof schema.minimum === "number" && value < schema.minimum) || (typeof schema.maximum === "number" && value > schema.maximum))) return `${path}: range`;
  if (typeof value === "string" && ((typeof schema.minLength === "number" && value.length < schema.minLength) || (typeof schema.maxLength === "number" && value.length > schema.maxLength))) return `${path}: length`;
  if (Array.isArray(value) && schema.items && typeof schema.items === "object") {
    for (let i = 0; i < value.length; i++) { const e = validateBasic(schema.items as Record<string, unknown>, value[i], `${path}[${i}]`, depth + 1); if (e) return e; }
  } else if (value && typeof value === "object" && !Array.isArray(value)) {
    const obj = value as Record<string, unknown>;
    const required = schema.required as string[] | undefined;
    for (const k of required ?? []) if (!Object.hasOwn(obj, k)) return `${path}.${k}: required`;
    const props = schema.properties as Record<string, Record<string, unknown>> | undefined;
    for (const [k, v] of Object.entries(obj)) { const child = props?.[k]; if (child && typeof child === "object") { const e = validateBasic(child, v, `${path}.${k}`, depth + 1); if (e) return e; } }
  }
  return undefined;
}
