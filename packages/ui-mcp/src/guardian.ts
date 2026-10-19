import fs from "node:fs";
import path from "node:path";
import { terminateProcesses, type OwnedProcess } from "./processes.js";
import { atomicJSON } from "./artifacts.js";
import { failure } from "./errors.js";

// An independent process survives an abruptly killed controller and owns only
// the identities registered over its private IPC channel.
const processes: OwnedProcess[] = [];
const root = process.argv[2]!;
process.on("message", (message: any) => {
  if (message.type === "track") processes.push(message.process as OwnedProcess);
});
process.on(
  "disconnect",
  () =>
    void (async () => {
      let cleanupError: ReturnType<typeof failure> | undefined;
      try {
        await terminateProcesses(processes);
      } catch (error) {
        cleanupError = failure(error);
      }
      try {
        const runs = path.join(root, "runs");
        if (fs.existsSync(runs))
          for (const entry of fs
            .readdirSync(runs, { withFileTypes: true })
            .filter((e) => e.isDirectory())) {
            const file = path.join(runs, entry.name, "result.json");
            try {
              const result = JSON.parse(fs.readFileSync(file, "utf8"));
              if (!result.ended_at) {
                result.status = "interrupted";
                result.ended_at = new Date().toISOString();
                result.outcome_unknown = true;
                result.evidence_incomplete = true;
                if (cleanupError) result.error = cleanupError;
                atomicJSON(file, result);
              }
            } catch {
              /* Partial journals remain available to the node administrator. */
            }
          }
      } finally {
        process.exit(cleanupError ? 1 : 0);
      }
    })(),
);
