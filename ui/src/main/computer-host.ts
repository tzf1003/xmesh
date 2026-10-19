import { execFile } from "node:child_process";
import { xmeshBinary } from "./cli";

/** The renderer can request setup but cannot choose the program, arguments or OS endpoint. */
export function setupComputerHost(binary: string = xmeshBinary()): Promise<void> {
  return new Promise((resolve, reject) => {
    execFile(binary, ["computer", "setup", "--json"], { timeout: 30000, windowsHide: true, maxBuffer: 64 * 1024 }, (error, stdout) => {
      let result: { ok?: boolean; error?: { message?: string } };
      try { result = JSON.parse(stdout) as typeof result; }
      catch { reject(new Error("桌面服务初始化未返回有效结果")); return; }
      if (error || result.ok !== true) { reject(new Error(result.error?.message ?? "桌面服务初始化失败")); return; }
      resolve();
    });
  });
}
