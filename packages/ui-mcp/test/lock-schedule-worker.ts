import fs from "node:fs";
import path from "node:path";
import { Locks } from "../src/locks.js";
const root = process.argv[2]!,
  actor = process.argv[3]!;
const wait = (name: string) => {
  const end = Date.now() + 20000;
  while (!fs.existsSync(path.join(root, name))) {
    if (Date.now() > end) throw new Error(`barrier timed out: ${name}`);
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 5);
  }
};
const rename = fs.renameSync;
let first = true;
fs.renameSync = (from, to) => {
  if (first) {
    first = false;
    fs.writeFileSync(path.join(root, `${actor}.paused`), "");
    wait(`${actor}.publish`);
  }
  rename(from, to);
};
const locks = new Locks(path.join(root, "locks"));
try {
  const release = locks.acquire("account");
  fs.writeFileSync(
    path.join(root, `${actor}.${release ? "granted" : "rejected"}`),
    "",
  );
  if (release) {
    wait(`${actor}.release`);
    release();
  }
} finally {
  locks.close();
}
