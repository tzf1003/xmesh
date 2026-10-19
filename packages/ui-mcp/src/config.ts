import fs from "node:fs";
import path from "node:path";
import { parse } from "yaml";
import { Ajv } from "ajv";
import { UIError, requireValue } from "./errors.js";
import type { Config, Environment } from "./types.js";

const ajv = new Ajv({ allErrors: true, strict: false });
ajv.addSchema(
  JSON.parse(
    fs.readFileSync(
      new URL("../../schemas/protocol.schema.json", import.meta.url),
      "utf8",
    ),
  ),
);
const configCheck = ajv.compile(
  JSON.parse(
    fs.readFileSync(
      new URL("../../schemas/environments.schema.json", import.meta.url),
      "utf8",
    ),
  ),
);

export const defaults: Config["limits"] = {
  sessions: 2,
  node_sessions: 4,
  session_idle_ms: 600_000,
  step_timeout_ms: 10_000,
  run_timeout_ms: 120_000,
  max_run_timeout_ms: 600_000,
  max_steps: 100,
  requests: 10_000,
  retention_days: 7,
  run_bytes: 100 * 1024 * 1024,
  total_bytes: 2 * 1024 * 1024 * 1024,
};
const envKeys = new Set([
  "name",
  "driver",
  "url",
  "allowed_origins",
  "executable",
  "args",
  "cwd",
  "env",
  "headless",
  "background_capable",
  "script_enabled",
  "trace",
  "mask",
  "upload_root",
  "state_template",
  "init_module",
  "locators",
  "accounts",
  "resources",
  "coverage",
  "fixtures",
]);
export function loadConfig(file: string): Config {
  const source = fs.realpathSync(file);
  const raw = parse(fs.readFileSync(source, "utf8")) as Config;
  requireValue(
    configCheck(raw),
    "invalid_config",
    "configuration does not match environment schema",
  );
  requireValue(
    raw && typeof raw === "object" && raw.version === 1,
    "invalid_config",
    "config version must be 1",
  );
  requireValue(
    typeof raw.state_dir === "string" &&
      raw.environments &&
      typeof raw.environments === "object",
    "invalid_config",
    "state_dir and environments are required",
  );
  for (const key of Object.keys(raw))
    requireValue(
      ["version", "state_dir", "environments", "limits"].includes(key),
      "invalid_config",
      `unknown config field: ${key}`,
    );
  const resolve = (p: string) => path.resolve(path.dirname(source), p);
  raw.state_dir = resolve(raw.state_dir);
  raw.limits = { ...defaults, ...raw.limits };
  for (const [key, value] of Object.entries(raw.limits))
    requireValue(
      key in defaults && Number.isSafeInteger(value) && value > 0,
      "invalid_config",
      `invalid limit: ${key}`,
    );
  requireValue(
    raw.limits.max_steps <= 100 &&
      raw.limits.max_run_timeout_ms <= 600_000 &&
      raw.limits.run_timeout_ms <= raw.limits.max_run_timeout_ms &&
      raw.limits.run_bytes <= raw.limits.total_bytes,
    "invalid_config",
    "limits exceed protocol bounds",
  );
  for (const [id, e] of Object.entries(raw.environments)) {
    requireValue(
      /^[\w-]{1,63}$/.test(id) && e && typeof e === "object",
      "invalid_config",
      "invalid environment id",
    );
    for (const key of Object.keys(e))
      requireValue(
        envKeys.has(key),
        "invalid_config",
        `unknown environment field: ${key}`,
      );
    requireValue(
      ["web", "electron"].includes(e.driver),
      "invalid_config",
      `${id}: unsupported driver`,
    );
    for (const key of ["headless", "script_enabled", "trace"] as const)
      if (e[key] !== undefined)
        requireValue(
          typeof e[key] === "boolean",
          "invalid_config",
          `${id}: ${key} must be boolean`,
        );
    for (const key of [
      "args",
      "mask",
      "resources",
      "coverage",
      "allowed_origins",
    ] as const)
      if (e[key] !== undefined)
        requireValue(
          Array.isArray(e[key]) && e[key]!.every((v) => typeof v === "string"),
          "invalid_config",
          `${id}: ${key} must be strings`,
        );
    for (const key of [
      "executable",
      "cwd",
      "upload_root",
      "state_template",
      "init_module",
    ] as const)
      if (e[key] !== undefined) {
        requireValue(
          typeof e[key] === "string",
          "invalid_config",
          `${id}: invalid ${key}`,
        );
        e[key] = resolve(e[key]!);
      }
    if (e.env)
      requireValue(
        Object.values(e.env).every((v) => typeof v === "string"),
        "invalid_config",
        `${id}: env must contain strings`,
      );
    if (e.driver === "web") {
      requireValue(
        typeof e.url === "string",
        "invalid_config",
        `${id}: url is required`,
      );
      const url = new URL(e.url);
      requireValue(
        ["http:", "https:"].includes(url.protocol) &&
          !url.username &&
          !url.password,
        "invalid_config",
        `${id}: URL must be HTTP(S) without credentials`,
      );
      e.allowed_origins ??= [url.origin];
      for (const origin of e.allowed_origins)
        requireValue(
          new URL(origin).origin === origin,
          "invalid_config",
          `${id}: expected URL origins`,
        );
    } else
      requireValue(
        !!e.executable,
        "invalid_config",
        `${id}: executable is required`,
      );
    if (e.accounts)
      for (const account of Object.values(e.accounts)) {
        requireValue(
          account && typeof account === "object",
          "invalid_config",
          `${id}: invalid account`,
        );
        for (const key of Object.keys(account))
          requireValue(
            ["storage_state", "secrets"].includes(key),
            "invalid_config",
            `${id}: unknown account field`,
          );
        if (account.storage_state)
          account.storage_state = resolve(account.storage_state);
        if (account.secrets)
          requireValue(
            Object.values(account.secrets).every(
              (v) => typeof v === "string" && v.length > 0,
            ),
            "invalid_config",
            `${id}: secrets must be nonempty strings`,
          );
      }
    e.fixtures ??= "real";
    requireValue(
      ["real", "mock"].includes(e.fixtures),
      "invalid_config",
      "invalid fixtures mode",
    );
    e.coverage ??= ["ui"];
  }
  return raw;
}
export function environment(config: Config, id: string): Environment {
  const e = config.environments[id];
  if (!e) throw new UIError("not_found", "unknown environment");
  return e;
}
export function privateDir(dir: string): void {
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  if (process.platform !== "win32") fs.chmodSync(dir, 0o700);
}
export function inside(root: string, input: string): string {
  const base = fs.realpathSync(root),
    file = fs.realpathSync(path.resolve(base, input));
  const relative = path.relative(base, file);
  requireValue(
    relative !== ".." &&
      !relative.startsWith(`..${path.sep}`) &&
      !path.isAbsolute(relative),
    "path_denied",
    "file is outside the configured root",
  );
  requireValue(
    fs.statSync(file).isFile(),
    "path_denied",
    "expected a regular file",
  );
  return file;
}
