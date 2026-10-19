import assert from "node:assert/strict";
import { test } from "node:test";
import { COMPUTER_EMERGENCY_SHORTCUT, registerComputerShortcut } from "../../src/main/computer-shortcut";
import { ShellController } from "../../src/main/controller";

test("emergency shortcut publishes OS acceptance and routes the registered callback", () => {
  let callback: (() => void) | undefined;
  let stopped = false;
  const accepted = registerComputerShortcut({ register(key, stop) { assert.equal(key, COMPUTER_EMERGENCY_SHORTCUT); callback = stop; return true; } }, () => { stopped = true; });
  assert.equal(accepted, true);
  callback!();
  assert.equal(stopped, true);
});

test("reserved or rejected emergency shortcuts remain unavailable in the snapshot", () => {
  const c = new ShellController({ discover: () => [], getLoginItem: () => false, setLoginItem: () => {} });
  c.setComputerShortcutAvailable(true);
  assert.equal(c.snapshot.computerShortcutAvailable, true);
  for (const register of [() => false, () => { throw new Error("reserved"); }]) {
    c.setComputerShortcutAvailable(registerComputerShortcut({ register }, () => { assert.fail("unregistered callback fired"); }));
    assert.equal(c.snapshot.computerShortcutAvailable, false);
  }
});
