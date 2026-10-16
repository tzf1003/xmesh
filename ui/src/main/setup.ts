// First-run setup behind the wizard: write the generated config for a new node
// and register and start it as a service. The renderer only supplies the
// parameters; this module validates them, regenerates the file text, and runs
// the two `xmesh` subcommands with fixed arguments.

import * as fs from "node:fs";
import * as path from "node:path";
import type { DataResult, SetupParams, SetupReport, SetupStep } from "../shared/ipc";
import { generateConfig, parseSetup } from "../shared/setup";
import { CliError, type SetupCliArgs } from "./cli";

export interface SetupHost {
  /** `<UserConfigDir>/xmesh`; each node has a directory below it. */
  stateRoot: string;
  /** Creates `file` with `content` (mode 0600), failing if it already exists. */
  writeNew(file: string, content: string): void;
  runCli(args: SetupCliArgs): Promise<string>;
}

export function fsSetupHost(stateRoot: string, runCli: SetupHost["runCli"]): SetupHost {
  return {
    stateRoot,
    runCli,
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

export async function runSetup(raw: unknown, host: SetupHost): Promise<DataResult<SetupReport>> {
  const parsed = parseSetup(raw);
  if (!parsed.ok) {
    return { ok: false, error: { code: "invalid_args", message: PROBLEM_TEXT[parsed.problem] ?? "参数无效", field: parsed.problem } };
  }
  const params: SetupParams = parsed.value;
  const configPath = path.join(host.stateRoot, params.nodeName, "xmesh.yaml");
  const steps: SetupStep[] = [];
  const report = (): DataResult<SetupReport> => ({ ok: true, data: { configPath, steps } });

  try {
    host.writeNew(configPath, generateConfig(params));
    steps.push({ id: "write-config", ok: true, detail: configPath });
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === "EEXIST") {
      return { ok: false, error: { code: "conflict", message: "这个节点名已有配置文件，不会覆盖。请换一个名字，或在「配置」页修改。", field: "node_name" } };
    }
    return { ok: false, error: { code: "internal", message: `无法写入配置：${e instanceof Error ? e.message : String(e)}` } };
  }

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
