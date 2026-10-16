#!/usr/bin/env node
// Generates api/control/v1/ts/control.ts from the control channel JSON Schemas (CTR-CTL-12).
//
//   node generate.mjs          write the file
//   node generate.mjs --check  fail if the committed file differs from what the schemas produce
//
// The schemas under docs/contracts/schemas are the single source. All control-*.schema.json
// definitions are merged into one flat $defs table (names are unique, check_control.py enforces
// that), local references are rewritten to "#/$defs/<Name>", and the few references that leave the
// control schemas (config scope/nodeName, the audit event, the tool error code) are inlined.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { compile } from "json-schema-to-typescript";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "../..");
const schemaDir = path.join(root, "docs/contracts/schemas");
const outFile = path.join(root, "api/control/v1/ts/control.ts");

const read = (p) => JSON.parse(fs.readFileSync(p, "utf8"));
const schemas = new Map();
for (const f of fs.readdirSync(schemaDir).sort()) {
  if (!f.endsWith(".schema.json")) continue;
  const s = read(path.join(schemaDir, f));
  schemas.set(s.$id, s);
}

const defs = {};
const controlIds = [...schemas.keys()].filter((id) => id.startsWith("urn:xmesh:schema:control-")).sort();
for (const id of controlIds) {
  if (id === "urn:xmesh:schema:control-routes:1") continue; // 路由表另行生成
  for (const [name, def] of Object.entries(schemas.get(id).$defs ?? {})) {
    if (name in defs) throw new Error(`duplicate definition name ${name}`);
    defs[name] = { owner: id, schema: def };
  }
}

// Validation-only keywords carry no type information and make the emitted types noisy.
const strip = (node) => {
  if (Array.isArray(node)) return node.map(strip);
  if (node === null || typeof node !== "object") return node;
  const out = {};
  for (const [k, v] of Object.entries(node)) {
    if (k === "allOf" && Array.isArray(v) && v.every((x) => x && typeof x === "object" && "if" in x)) continue;
    if (k === "if" || k === "then" || k === "else" || k === "x-http-status") continue;
    out[k] = strip(v);
  }
  return out;
};

const external = {
  "urn:xmesh:schema:config:1#/$defs/nodeName": () => strip(schemas.get("urn:xmesh:schema:config:1").$defs.nodeName),
  "urn:xmesh:schema:config:1#/$defs/scope": () => strip(schemas.get("urn:xmesh:schema:config:1").$defs.scope),
  "urn:xmesh:schema:audit-event:1": () => {
    const { $schema, $id, title, ...rest } = strip(schemas.get("urn:xmesh:schema:audit-event:1"));
    return rest;
  },
  "urn:xmesh:schema:error:1#/properties/error": () => strip(schemas.get("urn:xmesh:schema:error:1").properties.error),
};
const externalNames = {
  "urn:xmesh:schema:audit-event:1": "AuditEvent",
  "urn:xmesh:schema:error:1#/properties/error": "ToolErrorCode",
};
const hoisted = {};
const rewriteRefs = (node) => {
  if (Array.isArray(node)) return node.map(rewriteRefs);
  if (node === null || typeof node !== "object") return node;
  const out = {};
  for (const [k, v] of Object.entries(node)) {
    if (k !== "$ref" || typeof v !== "string") {
      out[k] = rewriteRefs(v);
      continue;
    }
    if (v.startsWith("#/$defs/")) {
      out[k] = v;
    } else if (v in externalNames) {
      hoisted[externalNames[v]] = rewriteRefs(external[v]());
      out[k] = `#/$defs/${externalNames[v]}`;
    } else if (v in external) {
      // 内联：丢掉 $ref，把被引用的模式合并进当前节点。
      Object.assign(out, rewriteRefs(external[v]()));
    } else {
      const m = /^(urn:xmesh:schema:control-[a-z]+:[0-9]+)#\/\$defs\/([A-Za-z0-9]+)$/.exec(v);
      if (!m || !(m[2] in defs) || defs[m[2]].owner !== m[1]) throw new Error(`unresolvable $ref ${v}`);
      out[k] = `#/$defs/${m[2]}`;
    }
  }
  return out;
};

// 同文件内的 "#/$defs/X" 在合并之后仍然指向同名定义，因为定义名全局唯一。
const bundle = {
  $id: "urn:xmesh:schema:control-bundle:1",
  title: "ControlBundle",
  type: "object",
  additionalProperties: false,
  $defs: {},
};
for (const [name, { schema }] of Object.entries(defs)) bundle.$defs[name] = rewriteRefs(strip(schema));
Object.assign(bundle.$defs, hoisted);

let types = await compile(bundle, "ControlBundle", {
  bannerComment: "",
  unreachableDefinitions: true,
  additionalProperties: false,
  declareExternallyReferenced: true,
  strictIndexSignatures: true,
  style: { singleQuote: false, printWidth: 100 },
});
types = types.replace(/\/\*\*\n \* This interface was referenced by `ControlBundle`'s JSON-Schema\n \* via the `definition` "[A-Za-z0-9]+"\.\n \*\/\n/g, "");
types = types.replace(/\n \*\n \* This interface was referenced by `ControlBundle`'s JSON-Schema\n \* via the `definition` "[A-Za-z0-9]+"\./g, "");
types = types.replace(/export interface ControlBundle \{\}\n/, "");

const lastSeg = (ref) => ref.split("/").pop();
const routes = read(path.join(schemaDir, "control-routes.json"));
const common = schemas.get("urn:xmesh:schema:control-common:1").$defs;
const errorSchema = schemas.get("urn:xmesh:schema:control-error:1");
const eventsSchema = schemas.get("urn:xmesh:schema:control-events:1").$defs;

const eventTypes = eventsSchema.ControlEvent.oneOf.map((r) => eventsSchema[lastSeg(r.$ref)].properties.type.const);
const routeRows = routes.routes.map((r) => ({
  id: r.id,
  method: r.method,
  path: r.path,
  capability: r.capability,
  since: r.since,
  query: r.query && lastSeg(r.query),
  body: r.body && lastSeg(r.body),
  response: r.response && lastSeg(r.response),
  success: r.success,
  stream: r.stream,
  if_match: r.if_match,
  errors: r.errors,
}));
const typeOrUndefined = (n) => (n ? n : "undefined");
const routeTypes = routes.routes
  .map(
    (r) =>
      `  ${JSON.stringify(r.id)}: { query: ${typeOrUndefined(r.query && lastSeg(r.query))}; body: ${typeOrUndefined(
        r.body && lastSeg(r.body),
      )}; response: ${typeOrUndefined(r.response && lastSeg(r.response))} };`,
  )
  .join("\n");

const json = (v) => JSON.stringify(v, null, 2);
const tail = `
export const CONTROL_API_VERSION = ${JSON.stringify(routes.api_version)} as const;
export const CONTROL_VERSION_HEADER = "X-Xmesh-Control-Version" as const;

/** Every capability a kernel may advertise (control.json and status.control.capabilities). */
export const CONTROL_CAPABILITIES = ${json(common.Capability.enum)} as const;

/** Event "type" values on GET /v1/events, in schema order. */
export const CONTROL_EVENT_TYPES = ${json(eventTypes)} as const;

/** HTTP status for each control error code. */
export const CONTROL_ERROR_STATUS = ${json(errorSchema["x-http-status"])} as const satisfies Record<ControlErrorCode, number>;

export interface ControlRouteInfo {
  id: keyof ControlRouteTypes;
  method: "GET" | "POST" | "PUT" | "DELETE";
  path: string;
  capability: Capability;
  since: string;
  /** Names of the types in this file, or null. */
  query: string | null;
  body: string | null;
  response: string | null;
  success: 200 | 201 | 204;
  stream: boolean;
  if_match: "none" | "required";
  errors: ControlErrorCode[];
}

/** The route table (control-routes.json). */
export const CONTROL_ROUTES: readonly ControlRouteInfo[] = ${json(routeRows)};

/** Request and response types by route id. */
export interface ControlRouteTypes {
${routeTypes}
}
`;

const header = `// Code generated by scripts/gen-control-ts from docs/contracts/schemas/control-*.schema.json. DO NOT EDIT.
// Contract: docs/contracts/control.md (CTR-CTL-*). Regenerate with:
//   npm ci --prefix scripts/gen-control-ts && node scripts/gen-control-ts/generate.mjs
/* eslint-disable */

`;
let output = header + types + tail;
// 生成物自己再过一遍 prettier 式的稳定换行：compile 已经格式化，这里只保证末尾单个换行。
output = output.replace(/\n+$/, "\n");

if (process.argv.includes("--check")) {
  const current = fs.existsSync(outFile) ? fs.readFileSync(outFile, "utf8") : "";
  if (current !== output) {
    console.error(`${path.relative(root, outFile)} is out of date; run node scripts/gen-control-ts/generate.mjs`);
    process.exit(1);
  }
  console.log("control.ts matches the schemas");
} else {
  fs.mkdirSync(path.dirname(outFile), { recursive: true });
  fs.writeFileSync(outFile, output);
  console.log(`wrote ${path.relative(root, outFile)} (${output.split("\n").length} lines)`);
}
