import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { useT } from "../../i18n";
import type { ToolDescriptor } from "../../../shared/playground";

export function ToolForm({ tool, args, onChange, onInvalid }: { tool: ToolDescriptor; args: Record<string, unknown>; onChange(value: Record<string, unknown>): void; onInvalid(invalid: boolean): void }): JSX.Element {
  const t = useT();
  const [invalid, setInvalid] = useState<Set<string>>(new Set());
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const props = tool.inputSchema.properties as Record<string, Record<string, unknown>> | undefined;
  const required = tool.inputSchema.required as string[] | undefined;
  return <div class="tool-fields">{Object.entries(props ?? {}).map(([key, p]) => {
    const type = Array.isArray(p.type) ? p.type.find((x) => x !== "null") : p.type;
    const requiredField = required?.includes(key) === true;
    const value = args[key];
    const included = Object.hasOwn(args, key);
    const id = `tool-arg-${key}`;
    const set = (v: unknown): void => {
      const next = { ...args };
      if (v === undefined) delete next[key]; else Object.defineProperty(next, key, { value: v, writable: true, configurable: true, enumerable: true });
      onChange(next);
    };
    const parse = (text: string, json: boolean): void => {
      setDrafts((s) => ({ ...s, [key]: text }));
      const next = new Set(invalid);
      try { set(text === "" && !requiredField ? undefined : json ? JSON.parse(text) : text); next.delete(key); }
      catch { next.add(key); }
      setInvalid(next); onInvalid(next.size > 0);
    };
    let field: JSX.Element;
    if (Array.isArray(p.enum)) field = <select id={id} value={included ? JSON.stringify(value) : ""} onChange={(e) => parse(e.currentTarget.value, true)}><option value="">—</option>{p.enum.map((v) => <option key={JSON.stringify(v)} value={JSON.stringify(v)}>{String(v)}</option>)}</select>;
    else if (type === "boolean") field = <select id={id} value={included ? String(value) : ""} onChange={(e) => parse(e.currentTarget.value, true)}><option value="">—</option><option value="true">true</option><option value="false">false</option></select>;
    else if (type === "integer" || type === "number") field = <input id={id} type="number" step={type === "integer" ? 1 : "any"} min={p.minimum as number | undefined} max={p.maximum as number | undefined} value={drafts[key] ?? (value === undefined ? "" : String(value))} onInput={(e) => parse(e.currentTarget.value, true)}/>;
    else if (type === "string" && !p.anyOf && !p.oneOf) field = <input id={id} type={/password|secret|token|key/i.test(key) ? "password" : "text"} autoComplete="off" value={value === undefined ? "" : String(value)} onInput={(e) => parse(e.currentTarget.value, false)}/>;
    else field = <textarea id={id} class="mono" rows={3} aria-invalid={invalid.has(key)} placeholder={type === "array" ? "[]" : "{}"} value={drafts[key] ?? (value === undefined ? "" : JSON.stringify(value, null, 2))} onInput={(e) => parse(e.currentTarget.value, true)}/>;
    return <div class="tool-field" key={key}><label for={id}><strong class="mono">{key}</strong> <span class="hint">{String(type ?? "JSON")}</span> {requiredField && <span class="tool-required">{t("pg.required")}</span>}</label>{field}{typeof p.description === "string" && <div class="hint">{p.description}</div>}{!requiredField && included && <button type="button" class="link" onClick={() => { set(undefined); setDrafts((s) => { const n = { ...s }; delete n[key]; return n; }); const next = new Set(invalid); next.delete(key); setInvalid(next); onInvalid(next.size > 0); }}>{t("pg.clear")}</button>}</div>;
  })}</div>;
}
