// Tailnet administration turns validated requests into fixed argv for
// `xmesh mesh ... --json`. Values use --flag=value and run without a shell.
// The CLI stores and reads the control-plane credential in a private JSON file.
// Login secrets go through stdin; pasted DERP tokens use a temporary private
// file removed after the call. Auth keys are written to the user's chosen file,
// and their paths are returned to the panel.

import { execFile } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
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
export function buildMeshArgs(req: MeshRequest, nodeConfig?: string, credentialFile?: string): string[] {
  const node = nodeConfig === undefined ? [] : [`--node-config=${nodeConfig}`];
  // Every subcommand takes the shared flags; the file replaces the keychain profile.
  const cred = credentialFile === undefined ? [] : [`--credential-file=${credentialFile}`];
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
        ...cred,
        ...(req.url !== undefined ? [`--url=${req.url}`] : []),
        ...(req.tailnet !== undefined ? [`--tailnet=${req.tailnet}`] : []),
        ...(req.clientId !== undefined ? [`--client-id=${req.clientId}`] : []),
      ];
    case "status":
      return ["mesh", "status", "--json", ...cred, ...node];
    case "share.tailnet":
      return ["mesh", "share", "--json", ...cred, `--device=${req.device}`, ...(req.user !== undefined ? [`--user=${req.user}`] : []), ...(nodeConfig === undefined ? [] : [`--config=${nodeConfig}`]), "--apply"];
    case "cloudflare.share":
      // Cloudflare's token lives in the keychain, not in the panel's credential file.
      return ["cloudflare", "share", "--json", `--device=${req.device}`, ...(nodeConfig === undefined ? [] : [`--config=${nodeConfig}`]), "--apply"];
    case "cloudflare.discover":
      return ["cloudflare", "discover", "--json", "--secret-file=-"];
    case "cloudflare.status":
      return ["cloudflare", "status", "--json"];
    case "cloudflare.setup":
      return ["cloudflare", "setup", "--json", `--name=${req.name}`, ...(nodeConfig === undefined ? [] : [`--config=${nodeConfig}`]), ...(req.apply ? ["--apply"] : [])];
    case "cloudflare.remove":
      return ["cloudflare", "remove", "--json", `--name=${req.name}`, "--apply"];
    case "cloudflare.login":
      return ["cloudflare", "login", "--json", `--account=${req.account}`, `--zone=${req.zone}`, `--domain=${req.domain}`, "--secret-file=-"];
    case "tags":
      return ["mesh", "tags", "--json", ...cred, `--device=${req.device}`, `--set=${req.tags.join(",")}`, ...(req.apply ? ["--apply"] : [])];
    case "onboard":
      return [
        "mesh",
        "authkey",
        "--json",
        ...cred,
        `--tags=${req.tags.join(",")}`,
        `--ttl=${req.ttl}`,
        `--out=${req.file}`,
        ...(req.ephemeral ? ["--ephemeral"] : []),
        ...(req.user !== undefined ? [`--user=${req.user}`] : []),
        ...(req.overwrite ? ["--overwrite"] : []),
      ];
    case "policy.show":
      return ["mesh", "policy", "--json", ...cred, "--show"];
    case "policy.document":
      return ["mesh", "policy", "document", "--json"];
    case "policy.publish":
      return ["mesh", "policy", "publish", "--json", ...cred, `--expect-version=${req.expectVersion}`, ...apply(req.apply)];
    case "policy.snapshots":
      return ["mesh", "policy", "--json", ...cred, "--snapshots"];
    case "policy.template":
      return [
        "mesh",
        "policy",
        "--json",
        ...cred,
        `--template=${req.template}`,
        `--owner=${req.owner}`,
        ...(req.port !== undefined ? [`--port=${req.port}`] : []),
        ...expect(req.expectVersion),
        ...apply(req.apply),
      ];
    case "policy.rollback":
      return ["mesh", "policy", "--json", ...cred, `--rollback=${req.snapshot}`, ...expect(req.expectVersion), ...apply(req.apply)];
    case "matrix":
      return ["mesh", "matrix", "--json", ...cred, ...(req.port !== undefined ? [`--port=${req.port}`] : []), ...(req.mismatchOnly ? ["--mismatch-only"] : []), ...node];
    case "derp.plan":
      return [
        "mesh",
        "derp",
        req.apply ? "apply" : "plan",
        "--json",
        ...cred,
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
        ...cred,
        ...req.regions.map(regionArg),
        ...(req.skipStun ? ["--skip-stun"] : []),
        ...(req.compareOfficial ? ["--compare-official"] : []),
      ];
    case "derp.headscale":
      return ["mesh", "derp", "headscale", "--json", ...cred, ...req.regions.map(regionArg), ...(req.omitDefault ? ["--omit-default"] : [])];
    case "derp.sync":
      if ("token" in req.target) throw new Error("A pasted token must be written to a private file before building argv");
      return [
        "mesh",
        "derp",
        "sync",
        "--json",
        ...cred,
        ...("allowlist" in req.target
          ? [`--allowlist=${req.target.allowlist}`]
          : [`--admission-url=${req.target.url}`, `--admission-token-file=${req.target.tokenFile}`]),
        ...(req.allDevices ? ["--all-devices"] : []),
      ];
  }
}

/** Ops whose non-zero exit still carries a useful JSON document (a failed probe, a rolled-back write). */
const JSON_ON_FAILURE: readonly MeshOp[] = ["derp.plan", "derp.probe"];

/** The credential file the panel manages. The CLI validates, writes and reads this per-user file. */
export const MESH_CREDENTIAL_FILE = "mesh-credential.json";

/** A credential file lands beside the app's other per-user state. */
export function meshCredentialPath(stateRoot: string): string {
  return path.join(stateRoot, "mesh", MESH_CREDENTIAL_FILE);
}

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
  if (text.includes("没有保存的凭据") || text.includes("还没有保存 Cloudflare 令牌")) return { code: "no_credential", message: text };
  if (text.includes("预览时的策略版本") || text.includes("meshadmin: policy changed since it was read")) return { code: "conflict", reason: "stale_version", message: text };
  // A read-only Tailscale OAuth credential is refused by the control plane with
  // a 403 that names the scope. That is a different problem from "not logged in".
  if (text.includes("insufficient_scope") || text.includes("HTTP 403")) return { code: "insufficient_scope", message: text };
  if (text.includes("HTTP 401") || text.includes("invalid_client")) return { code: "invalid_credential", message: text };
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
    /** 0600 JSON file the CLI reads instead of the keychain. */
    private readonly credentialFile: () => string | undefined = () => undefined,
  ) {}

  /** `raw` is whatever the renderer sent. */
  async request(raw: unknown): Promise<DataResult<MeshResults[MeshOp]>> {
    const parsed = parseMeshRequest(raw);
    if (!parsed.ok) return { ok: false, error: { code: "invalid_args", message: `invalid ${parsed.field}`, field: parsed.field } };
    // A pasted admission token becomes a 0600 file for the duration of the
    // call; the token itself never reaches argv or the environment.
    let dir: string | undefined;
    let req = parsed.value;
    let result: MeshRun;
    try {
      if (req.op === "derp.sync" && "token" in req.target) {
        dir = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-admission-"));
        if (process.platform !== "win32") fs.chmodSync(dir, 0o700);
        const file = path.join(dir, "token");
        fs.writeFileSync(file, req.target.token, { flag: "wx", mode: 0o600 });
        req = { ...req, target: { url: req.target.url, tokenFile: file } };
      }
      let input: string | undefined;
      if (req.op === "login" || req.op === "cloudflare.login" || req.op === "cloudflare.discover") input = req.secret;
      if (req.op === "policy.document") input = JSON.stringify({ ...req.edit, ...(req.edit.regions !== undefined && { regions: req.edit.regions.map((r) => ({ id: r.id ?? 0, code: r.code, name: r.name, hostname: r.host, ipv4: r.ipv4, ipv6: r.ipv6, derp_port: r.derpPort, stun_port: r.stunPort, cert_name: r.certName })) }) });
      if (req.op === "policy.publish") input = JSON.stringify({ text: req.text });
      result = await this.run(buildMeshArgs(req, this.nodeConfig(), this.credentialFile()), input);
    } catch {
      return { ok: false, error: { code: "unavailable", message: "无法完成控制面操作，请检查凭据文件和内核程序后重试" } };
    } finally {
      if (dir !== undefined) {
        try { fs.rmSync(dir, { recursive: true, force: true }); } catch { /* The OS may delay deletion while closing handles. */ }
      }
    }
    const json = parseJson(result.stdout);
    if (result.code !== 0) {
      if (JSON_ON_FAILURE.includes(req.op) && typeof json === "object" && json !== null) {
        return { ok: true, data: normalizeResult(req.op, json) };
      }
      return { ok: false, error: meshError(result) };
    }
    if (json === undefined) return { ok: false, error: { code: "mesh_failed", message: "xmesh mesh returned output that is not JSON" } };
    try { return { ok: true, data: normalizeResult(req.op, json) }; }
    catch { return { ok: false, error: { code: "mesh_failed", message: "xmesh 返回了无效的策略文档" } }; }
  }
}
