// First-run setup behind the wizard: write the generated config and the default deny list for a new node,
// and register and start it as a service. The renderer only supplies the parameters; this module validates
// them, regenerates the file texts, and runs the two `xmesh` subcommands with fixed arguments.

import * as fs from "node:fs";
import * as path from "node:path";
import type { DataResult, SetupPreview, SetupReport, SetupStep } from "../shared/ipc";
import { AUTHKEY_FILE_NAME, DENY_FILE_NAME, generateConfig, generateDenyFile, parseSetup, resolveAuthkey } from "../shared/setup";
import { CliError, type SetupCliArgs } from "./cli";

export interface SetupHost {
  /** `<UserConfigDir>/xmesh`; each node has a directory below it. */
  stateRoot: string;
  /** Creates `file` with `content` (mode 0600), failing if it already exists. */
  writeNew(file: string, content: string): void;
  /** The current text of `file`, or undefined when it cannot be read. */
  readText(file: string): string | undefined;
  /**
   * Creates a preauthorised auth key at `file` (0600) with the saved
   * control-plane credential. Used when the wizard asks to be authorised
   * automatically; absent means the host cannot do it.
   */
  createAuthkey?(file: string, tags: string[], controlUrl?: string, user?: string): Promise<void>;
  loginTailnet?(config: string, stateDir: string, controlUrl: string): Promise<void>;
  runCli(args: SetupCliArgs): Promise<string>;
}

export function fsSetupHost(stateRoot: string, runCli: SetupHost["runCli"], createAuthkey?: SetupHost["createAuthkey"], loginTailnet?: SetupHost["loginTailnet"]): SetupHost {
  return {
    stateRoot,
    runCli,
    ...(createAuthkey !== undefined && { createAuthkey }),
    ...(loginTailnet !== undefined && { loginTailnet }),
    readText: (file) => {
      try {
        return fs.readFileSync(file, "utf8");
      } catch {
        return undefined;
      }
    },
    writeNew: (file, content) => {
      fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
      fs.writeFileSync(file, content, { flag: "wx", mode: 0o600 });
    },
  };
}

const PROBLEM_TEXT: Record<string, string> = {
  node_name: "节点名只能含字母、数字和连字符，且以字母或数字开头",
  mode: "网络模式无效",
  roles: "至少选择一个有效角色",
  workspace: "工作目录必须是绝对路径",
  control_url: "控制面地址必须是 https 地址",
  authkey_file: "auth key 文件必须是绝对路径",
  authkey: "请输入有效的 auth key，且只选择一种授权方式",
  auto_authkey: "请选择有效的入网授权方式和 Headscale 用户",
};

interface SetupPlan extends SetupPreview {
  /** The text the deny list file is created with, when it does not exist yet. */
  denyList: string;
  /** Where a pasted auth key lands; absent when the user named an existing file. */
  authkeyPath?: string;
  /** The pasted key itself; only `runSetup` writes it, and never into the config. */
  authkey?: string;
  browserLogin?: boolean;
  controlUrl?: string;
  authkeyUser?: string;
}

/** Validates the parameters and works out the two files the first run writes. Touches nothing on disk. */
function planSetup(raw: unknown, host: SetupHost): DataResult<SetupPlan> {
  const parsed = parseSetup(raw);
  if (!parsed.ok) {
    return { ok: false, error: { code: "invalid_args", message: PROBLEM_TEXT[parsed.problem] ?? "参数无效", field: parsed.problem } };
  }
  const dir = path.join(host.stateRoot, parsed.value.nodeName);
  const denyPath = path.join(dir, DENY_FILE_NAME);
  // A pasted key is written beside the config; generateConfig only ever sees
  // the path, so the key text cannot end up in xmesh.yaml.
  const resolved = resolveAuthkey(parsed.value, path.join(dir, AUTHKEY_FILE_NAME));
  return {
    ok: true,
    data: {
      configPath: path.join(dir, "xmesh.yaml"),
      denyPath,
      config: generateConfig(resolved, denyPath),
      denyList: generateDenyFile(),
      browserLogin: parsed.value.browserLogin,
      controlUrl: parsed.value.controlUrl,
      authkeyUser: parsed.value.authkeyUser,
      ...(parsed.value.authkey !== undefined && { authkeyPath: resolved.authkeyFile, authkey: parsed.value.authkey }),
      ...(parsed.value.autoAuthkey === true && { authkeyPath: resolved.authkeyFile }),
    },
  };
}

/** What the wizard's review step shows: the config text `runSetup` writes for the same parameters. */
export function previewSetup(raw: unknown, host: SetupHost): DataResult<SetupPreview> {
  const planned = planSetup(raw, host);
  if (!planned.ok) return planned;
  const { configPath, denyPath, config, authkeyPath } = planned.data;
  return { ok: true, data: { configPath, denyPath, config, ...(authkeyPath !== undefined && { authkeyPath }) } };
}

export async function runSetup(raw: unknown, host: SetupHost): Promise<DataResult<SetupReport>> {
  const planned = planSetup(raw, host);
  if (!planned.ok) return planned;
  const { configPath, denyPath, config, denyList, authkeyPath, authkey, browserLogin, controlUrl, authkeyUser } = planned.data;
  const steps: SetupStep[] = [];
  const report = (): DataResult<SetupReport> => ({ ok: true, data: { configPath, steps } });
  const conflict: DataResult<SetupReport> = {
    ok: false,
    error: { code: "conflict", message: "这个节点名已有配置文件，不会覆盖。请换一个名字，或在「设置」页修改。", field: "node_name" },
  };
  const internal = (e: unknown): DataResult<SetupReport> => ({
    ok: false,
    error: { code: "internal", message: `无法写入配置：${e instanceof Error ? e.message : String(e)}` },
  });

  // An existing config is never overwritten. One with exactly this text is a retry after install or start
  // failed, so the run goes on; anything else is the user's and is left alone, with nothing else touched.
  const existing = host.readText(configPath);
  if (existing !== undefined && existing !== config) return conflict;

  // The config names the auth key file, so the key is written first. A file
  // that is already there is kept: the node may still be using it.
  if (authkeyPath !== undefined && authkey !== undefined) {
    try {
      host.writeNew(authkeyPath, `${authkey}\n`);
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== "EEXIST") return internal(e);
      if (host.readText(authkeyPath)?.trim() !== authkey) {
        return { ok: false, error: { code: "conflict", field: "authkey", message: "该节点已有不同的 key。请使用原有文件路径，或选择新的节点名。" } };
      }
    }
  } else if (authkeyPath !== undefined && !host.readText(authkeyPath)?.trim()) {
    // The wizard asked to be authorised automatically: the saved read-write
    // credential mints a preauthorised, tagged key, so no approval is needed.
    if (host.createAuthkey === undefined) {
      return { ok: false, error: { code: "unavailable", reason: "no_mesh_service", message: "这个版本不能自动创建 auth key" } };
    }
    try {
      await host.createAuthkey(authkeyPath, ["tag:device"], controlUrl, authkeyUser);
    } catch (e) {
      return { ok: false, error: { code: "mesh_failed", message: e instanceof Error ? e.message : String(e) } };
    }
  }

  // The config names the deny list, so the list is written first. A list already there is the user's own
  // (they may have edited it), so it is kept as it is.
  try {
    host.writeNew(denyPath, denyList);
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "EEXIST") return internal(e);
  }

  if (existing === undefined) {
    try {
      host.writeNew(configPath, config);
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== "EEXIST") return internal(e);
      // Created by someone else since the read above: only an identical file is fine.
      if (host.readText(configPath) !== config) return conflict;
    }
  }
  steps.push({ id: "write-config", ok: true, detail: configPath });

  if (browserLogin) {
    if (host.loginTailnet === undefined) return { ok: false, error: { code: "unavailable", message: "当前版本不支持浏览器授权" } };
    try { await host.loginTailnet(configPath, path.dirname(configPath), controlUrl!); }
    catch (e) { return { ok: false, error: { code: "mesh_failed", message: e instanceof Error ? e.message : String(e) } }; }
  }

  for (const id of ["install", "start"] as const) {
    try {
      const args: SetupCliArgs = browserLogin && id === "install"
        ? ["install", "--config", configPath, "--state-dir", path.dirname(configPath)]
        : [id, "--config", configPath];
      const out = await host.runCli(args);
      steps.push({ id, ok: true, detail: out });
    } catch (e) {
      steps.push({ id, ok: false, detail: e instanceof CliError || e instanceof Error ? e.message : String(e) });
      break;
    }
  }
  return report();
}
