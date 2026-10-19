import fs from "node:fs";
import path from "node:path";
import { Locks } from "../src/locks.js";
const root = process.argv[2]!,
  locks = new Locks(path.join(root, "locks"));
try {
  for (let i = 0; i < 3; i++) {
    const deadline = Date.now() + 60000;
    let release: (() => void) | undefined;
    while (!(release = locks.acquire("shared"))) {
      if (Date.now() > deadline) throw new Error("lock acquisition timed out");
      await new Promise((resolve) =>
        setTimeout(resolve, 10 + Math.floor(Math.random() * 40)),
      );
    }
    try {
      const flag = path.join(root, "critical");
      fs.writeFileSync(flag, "active", { flag: "wx" });
      const count = Number(fs.readFileSync(path.join(root, "count"), "utf8"));
      await new Promise((resolve) => setTimeout(resolve, 5));
      fs.writeFileSync(path.join(root, "count"), String(count + 1));
      fs.rmSync(flag);
    } finally {
      release();
    }
  }
} finally {
  locks.close();
}
