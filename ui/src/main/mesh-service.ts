// Tailnet administration for the panel. Each request becomes a fixed argv for
// `xmesh mesh ... --json`; there is no shell, every value was validated by
// parseMeshRequest and is attached with `--flag=value` so a value can never be
// read as a flag. The control-plane credential stays in the CLI's keychain
// profile. The one secret this process handles is the one a `login` request
// carries: it is written to the CLI's stdin, never to argv, and is not kept
// anywhere after that. The only other secret the CLI produces (a pre-auth key)
// goes to a 0600 file the user chose, and only the path comes back.

import { execFile } from "node:child_process";
import {
  type DerpRegionInput,
  type MeshOp,
  type MeshRequest,
  type MeshResults,
  normalizeResult,
  parseMeshRequest,
} from "../shared/mesh";
import type { ActionError, DataResult } from "../shared/ipc";
import { xmeshBinary } from "./cli";

export interface MeshRun {
  /** Exit code; 127 when the binary was not found. */
  code: number;
  stdout: string;
  stderr: string;
  missing?: boolean;
}

/** `input` is written to the command's stdin; only `login` uses it. */
export type MeshRunner = (args: string[], input?: string) => Promise<MeshRun>;

const MESH_TIMEOUT_MS = 150_000;
const MAX_OUTPUT = 8 * 1024 * 1024;

/** Runs the real CLI. Never rejects: a failure is a `MeshRun`. */
export function execMeshRunner(bin: () => string = xmeshBinary): MeshRunner {
  return (args, input) =>
    new Promise((resolve) => {
      const file = bin();
      const child = execFile(file, args, { timeout: MESH_TIMEOUT_MS, windowsHide: true, maxBuffer: MAX_OUTPUT, encoding: "utf8" }, (err, stdout, stderr) => {
        if (!err) return resolve({ code: 0, stdout, stderr });
        const e = err as NodeJS.ErrnoException & { killed?: boolean };
        if (e.code === "ENOENT") return resolve({ code: 127, stdout: "", stderr: `找不到 xmesh 可执行文件：${file}`, missing: true });
        if (e.killed) return resolve({ code: 124, stdout, stderr: `${stderr}\n操作超时，没有等到控制面的回应`.trim() });
        resolve({ code: typeof e.code === "number" ? e.code : 1, stdout, stderr: stderr || e.message });
      });
      if (input !== undefined) {
        // When the binary is missing the callback reports it; a write to the dead pipe must not be an unhandled error.
        child.stdin?.on("error", () => undefined);
        child.stdin?.end(input);
      }
    });
}

function regionArg(r: DerpRegionInput): string {
  const parts = [`code=${r.code}`, `name=${r.name}`, `host=${r.host}`];
  if (r.ipv4 !== undefined) parts.push(`ipv4=${r.ipv4}`);
  if (r.ipv6 !== undefined) parts.push(`ipv6=${r.ipv6}`);
  if (r.id !== undefined) parts.push(`id=${r.id}`);
  if (r.derpPort !== undefined) parts.push(`derp-port=${r.derpPort}`);
  if (r.stunPort !== undefined) parts.push(`stun-port=${r.stunPort}`);
  if (r.certName !== undefined) parts.push(`cert-name=${r.certName}`);
  return `--region=${parts.join(",")}`;
}

/**
 * argv after `xmesh`. `nodeConfig` is the connected node's own config file, so
 * the CLI can match devices to xmesh roles and read `agent.authz`.
 */
export function buildMeshArgs(req: MeshRequest, nodeConfig?: string): string[] {
  const node = nodeConfig === undefined ? [] : [`--node-config=${nodeConfig}`];
  const apply = (on: boolean): string[] => (on ? ["--apply", "--yes"] : []);
  const expect = (v: string | undefined): string[] => (v === undefined ? [] : [`--expect-version=${v}`]);
  switch (req.op) {
    case "login":
      // The secret is not here: `--secret-file=-` tells the CLI to read it from stdin.
      return [
        "mesh",
        "login",
        "--json",
        `--kind=${req.kind}`,
        `--access=${req.access}`,
        "--secret-file=-",
        ...(req.url !== undefined ? [`--url=${req.url}`] : []),
        ...(req.tailnet !== undefined ? [`--tailnet=${req.tailnet}`] : []),
        ...(req.clientId !== undefined ? [`--client-id=${req.clientId}`] : []),
      ];
    case "status":
      return ["mesh", "status", "--json", ...node];
    case "tags":
      return ["mesh", "tags", "--json", `--device=${req.device}`, `--set=${req.tags.join(",")}`, ...(req.apply ? ["--apply"] : [])];
    case "onboard":
      return [
        "mesh",
        "authkey",
        "--json",
        `--tags=${req.tags.join(",")}`,
        `--ttl=${req.ttl}`,
        `--out=${req.file}`,
        ...(req.ephemeral ? ["--ephemeral"] : []),
        ...(req.user !== undefined ? [`--user=${req.user}`] : []),
        ...(req.overwrite ? ["--overwrite"] : []),
      ];
    case "policy.show":
      return ["mesh", "policy", "--json", "--show"];
    case "policy.snapshots":
      return ["mesh", "policy", "--json", "--snapshots"];
    case "policy.template":
      return [
        "mesh",
        "policy",
        "--json",
        `--template=${req.template}`,
        `--owner=${req.owner}`,
        ...(req.port !== undefined ? [`--port=${req.port}`] : []),
        ...expect(req.expectVersion),
        ...apply(req.apply),
      ];
    case "policy.rollback":
      return ["mesh", "policy", "--json", `--rollback=${req.snapshot}`, ...expect(req.expectVersion), ...apply(req.apply)];
    case "matrix":
      return ["mesh", "matrix", "--json", ...(req.port !== undefined ? [`--port=${req.port}`] : []), ...(req.mismatchOnly ? ["--mismatch-only"] : []), ...node];
    case "derp.plan":
      return [
        "mesh",
        "derp",
        req.apply ? "apply" : "plan",
        "--json",
        ...req.regions.map(regionArg),
        ...(req.omitDefault ? ["--omit-default"] : []),
        ...(req.skipStun ? ["--skip-stun"] : []),
        ...expect(req.expectVersion),
        ...(req.apply ? ["--yes"] : []),
      ];
    case "derp.probe":
      return [
        "mesh",
        "derp",
        "probe",
        "--json",
        ...req.regions.map(regionArg),
        ...(req.skipStun ? ["--skip-stun"] : []),
        ...(req.compareOfficial ? ["--compare-official"] : []),
      ];
    case "derp.headscale":
      return ["mesh", "derp", "headscale", "--json", ...req.regions.map(regionArg), ...(req.omitDefault ? ["--omit-default"] : [])];
    case "derp.sync":
      return [
        "mesh",
        "derp",
        "sync",
        "--json",
        ...("allowlist" in req.target
          ? [`--allowlist=${req.target.allowlist}`]
          : [`--admission-url=${req.target.url}`, `--admission-token-file=${req.target.tokenFile}`]),
        ...(req.allDevices ? ["--all-devices"] : []),
      ];
  }
}

/** Ops whose non-zero exit still carries a useful JSON document (a failed probe, a rolled-back write). */
const JSON_ON_FAILURE: readonly MeshOp[] = ["derp.plan", "derp.probe"];

function firstLines(s: string, max = 600): string {
  return s
    .trim()
    .split(/\r?\n/)
    .filter((l) => l.trim() !== "")
    .slice(0, 4)
    .join("\n")
    .slice(0, max);
}

/** Turns the CLI's stderr into the panel's error vocabulary. */
export function meshError(run: MeshRun): ActionError {
  const text = firstLines(run.stderr) || `xmesh mesh exited with code ${run.code}`;
  if (run.missing) return { code: "unavailable", reason: "binary_missing", message: text };
  if (text.includes("没有保存的凭据")) return { code: "no_credential", message: text };
  if (text.includes("预览时的策略版本")) return { code: "conflict", reason: "stale_version", message: text };
  if (text.includes("system keychain")) return { code: "keychain_unavailable", message: text };
  if (run.code === 124) return { code: "unavailable", reason: "timeout", message: text };
  return { code: "mesh_failed", message: text };
}

function parseJson(s: string): unknown {
  try {
    return JSON.parse(s);
  } catch {
    return undefined;
  }
}

export class MeshService {
  /**
   * `nodeConfig` returns the selected node's config path while the kernel is
   * connected; the CLI uses it to match devices with xmesh roles.
   */
  constructor(
    private readonly run: MeshRunner,
    private readonly nodeConfig: () => string | undefined = () => undefined,
  ) {}

  /** `raw` is whatever the renderer sent. */
  async request(raw: unknown): Promise<DataResult<MeshResults[MeshOp]>> {
    const parsed = parseMeshRequest(raw);
    if (!parsed.ok) return { ok: false, error: { code: "invalid_args", message: `invalid ${parsed.field}`, field: parsed.field } };
    const req = parsed.value;
    let result: MeshRun;
    try {
      result = await this.run(buildMeshArgs(req, this.nodeConfig()), req.op === "login" ? req.secret : undefined);
    } catch (e) {
      return { ok: false, error: { code: "unavailable", message: e instanceof Error ? e.message : String(e) } };
    }
    const json = parseJson(result.stdout);
    if (result.code !== 0) {
      if (JSON_ON_FAILURE.includes(req.op) && typeof json === "object" && json !== null) {
        return { ok: true, data: normalizeResult(req.op, json) };
      }
      return { ok: false, error: meshError(result) };
    }
    if (json === undefined) return { ok: false, error: { code: "mesh_failed", message: "xmesh mesh returned output that is not JSON" } };
    return { ok: true, data: normalizeResult(req.op, json) };
  }
}
