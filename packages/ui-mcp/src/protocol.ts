import fs from "node:fs";
import { Ajv } from "ajv";
import { requireValue, UIError } from "./errors.js";
import type { Environment, Step, Target } from "./types.js";
export const schema = JSON.parse(
  fs.readFileSync(
    new URL("../../schemas/protocol.schema.json", import.meta.url),
    "utf8",
  ),
);
const ajv = new Ajv({ allErrors: true, strict: false });
ajv.addSchema(schema);
export function validate(name: string, input: unknown): void {
  requireValue(
    JSON.stringify(input).length <= 262_144,
    "invalid_args",
    "request exceeds 256 KiB",
  );
  const check = ajv.getSchema(`${schema.$id}#/$defs/${name}`)!;
  if (!check(input))
    throw new UIError("invalid_args", "request does not match protocol", {
      issues: check.errors?.map((e) => ({
        path: e.instancePath,
        rule: e.keyword,
      })),
    });
}
export function resolveTarget(
  target: Target,
  e: Environment,
  depth = 0,
): Target {
  requireValue(
    depth < 12,
    "invalid_args",
    "locator nesting or alias cycle exceeds limit",
  );
  if (target.alias) {
    const found = e.locators?.[target.alias];
    requireValue(found, "invalid_args", "unknown locator alias");
    return resolveTarget(found, e, depth + 1);
  }
  return {
    ...target,
    within: target.within
      ? resolveTarget(target.within, e, depth + 1)
      : undefined,
  };
}
export function checkSteps(
  steps: Step[],
  e: Environment,
  secrets: Record<string, string> = {},
): Step[] {
  const ids = new Set<string>();
  return steps.map((s) => {
    requireValue(!ids.has(s.id), "invalid_args", "step ids must be unique");
    ids.add(s.id);
    requireValue(
      !s.secret || !!secrets[s.secret],
      "invalid_args",
      "unknown secret reference",
    );
    if (s.action === "navigate") {
      const u = new URL(s.value!, e.url);
      requireValue(
        ["http:", "https:"].includes(u.protocol) &&
          !u.username &&
          !u.password &&
          !!e.allowed_origins?.includes(u.origin),
        "origin_denied",
        "navigation origin is not configured",
      );
    }
    return { ...s, target: s.target ? resolveTarget(s.target, e) : undefined };
  });
}
export function toolSchema(
  name: "RunRequest" | "ScriptRequest",
): Record<string, unknown> {
  return { ...schema.$defs[name], $defs: schema.$defs };
}
