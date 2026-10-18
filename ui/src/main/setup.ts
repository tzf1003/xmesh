// First-run setup behind the wizard: write the generated config and the default deny list for a new node,
// and register and start it as a service. The renderer only supplies the parameters; this module validates
// them, regenerates the file texts, and runs the two `xmesh` subcommands with fixed arguments.

import * as fs from "node:fs";
import * as path from "node:path";
import type { DataResult, SetupPreview, SetupReport, SetupStep } from "../shared/ipc";
import { DENY_FILE_NAME, generateConfig, generateDenyFile, parseSetup } from "../shared/setup";
import { CliError, type SetupCliArgs } from "./cli";

export interface SetupHost {
  /** `<UserConfigDir>/xmesh`; each node has a directory below it. */
  stateRoot: string;
  /** Creates `file` with `content` (mode 0600), failing if it already exists. */
  writeNew(file: string, content: string): void;
  /** The current text of `file`, or undefined when it cannot be read. */
  readText(file: string): string | undefined;
  runCli(args: SetupCliArgs): Promise<string>;
}

export function fsSetupHost(stateRoot: string, runCli: SetupHost["runCli"]): SetupHost {
  return {
    stateRoot,
    runCli,
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
};

interface SetupPlan extends SetupPreview {
  /** The text the deny list file is created with, when it does not exist yet. */
  denyList: string;
}

/** Validates the parameters and works out the two files the first run writes. Touches nothing on disk. */
function planSetup(raw: unknown, host: SetupHost): DataResult<SetupPlan> {
  const parsed = parseSetup(raw);
  if (!parsed.ok) {
    return { ok: false, error: { code: "invalid_args", message: PROBLEM_TEXT[parsed.problem] ?? "参数无效", field: parsed.problem } };
  }
  const dir = path.join(host.stateRoot, parsed.value.nodeName);
  const denyPath = path.join(dir, DENY_FILE_NAME);
  return {
    ok: true,
    data: {
      configPath: path.join(dir, "xmesh.yaml"),
      denyPath,
      config: generateConfig(parsed.value, denyPath),
      denyList: generateDenyFile(),
    },
  };
}

/** What the wizard's review step shows: the config text `runSetup` writes for the same parameters. */
export function previewSetup(raw: unknown, host: SetupHost): DataResult<SetupPreview> {
  const planned = planSetup(raw, host);
  if (!planned.ok) return planned;
  const { configPath, denyPath, config } = planned.data;
  return { ok: true, data: { configPath, denyPath, config } };
}

export async function runSetup(raw: unknown, host: SetupHost): Promise<DataResult<SetupReport>> {
  const planned = planSetup(raw, host);
  if (!planned.ok) return planned;
  const { configPath, denyPath, config, denyList } = planned.data;
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

  for (const id of ["install", "start"] as const) {
    try {
      const out = await host.runCli([id, "--config", configPath]);
      steps.push({ id, ok: true, detail: out });
    } catch (e) {
      steps.push({ id, ok: false, detail: e instanceof CliError || e instanceof Error ? e.message : String(e) });
      break;
    }
  }
  return report();
}
