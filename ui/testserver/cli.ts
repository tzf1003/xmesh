// Runs a fake kernel for manual UI work:
//
//   npm run fake-kernel -- [--root <dir>] [--id <name>]
//
// With no --root the manifest goes where a real kernel's would, so the UI
// finds it. Commands are read from stdin, one per line.

import * as readline from "node:readline";
import { stateRoot } from "../src/main/discovery";
import { peersFixture } from "./fixtures";
import { FakeKernel } from "./server";

const HELP = `commands:
  warn <code> <message>   add a warning (tray turns yellow)
  clear                   remove all warnings
  restart on|off          set restart_required
  drain | resume          toggle draining
  peer <name> on|off      change a peer (mac-dev-01, cloud-vm, linux-box)
  mismatch on|off         answer with an incompatible control version
  drop                    close the event streams
  quit`;

function arg(name: string): string | undefined {
  const i = process.argv.indexOf(`--${name}`);
  return i === -1 ? undefined : process.argv[i + 1];
}

async function main(): Promise<void> {
  const root = arg("root") ?? stateRoot();
  const id = arg("id") ?? "fake-node";
  const kernel = await FakeKernel.start({ pingMs: 5000 });
  const manifest = kernel.writeManifest(root, id);
  console.log(`fake kernel on ${kernel.endpoint}\nmanifest ${manifest}\n${HELP}`);

  const rl = readline.createInterface({ input: process.stdin });
  const shutdown = async (): Promise<void> => {
    await kernel.stop();
    process.exit(0);
  };
  process.on("SIGINT", () => void shutdown());
  rl.on("close", () => void shutdown());
  rl.on("line", (line) => {
    const [cmd, a, ...rest] = line.trim().split(/\s+/);
    const s = kernel.currentStatus;
    switch (cmd) {
      case "warn":
        kernel.setWarnings([...s.warnings, { code: "unclassified", severity: "warning", message: [a, ...rest].join(" ") }]);
        break;
      case "clear":
        kernel.setWarnings([]);
        break;
      case "restart":
        kernel.setStatus({ restart_required: a === "on" });
        break;
      case "drain":
        kernel.setStatus({ drain: { draining: true, mode: "keep", since: new Date().toISOString() } });
        break;
      case "resume":
        kernel.setStatus({ drain: { draining: false, mode: null, since: null } });
        break;
      case "peer": {
        const peers = peersFixture();
        kernel.setPeers(peers.map((p) => (p.name === a ? { ...p, online: rest[0] === "on" } : p)));
        break;
      }
      case "mismatch":
        kernel.setControlVersion(a === "on" ? "2.0" : "1.0");
        break;
      case "drop":
        kernel.dropStreams();
        break;
      case "quit":
        void shutdown();
        break;
      default:
        console.log(HELP);
    }
  });
}

void main();
