import { type ElectronApplication, type Page, _electron as electron, expect } from "@playwright/test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { FakeKernel, type FakeKernelOptions } from "../../testserver/server";
import { stopNativeTestProcess } from "./process-exit";

export const UI_ROOT = path.resolve(__dirname, "..", "..");

/** Navigate through the disclosure used by the production sidebar. */
export async function navigate(page: Page, id: string): Promise<void> {
  const target = page.getByTestId(`nav-${id}`);
  if (!(await target.isVisible())) await page.getByTestId("nav-advanced").click();
  await target.click();
}

export interface Harness {
  app: ElectronApplication;
  root: string;
  userData: string;
  kernel: FakeKernel;
  /** Writes (or rewrites) the kernel's manifest under the state root. */
  publish(id?: string): void;
  tray(): Promise<TraySummary>;
  clickMenu(id: string): Promise<unknown>;
  calls(): Promise<{ op: string; arg?: unknown }[]>;
  notices(): Promise<string[]>;
  openPanel(): Promise<Page>;
  close(): Promise<void>;
}

export interface TraySummary {
  icon: string;
  tooltip: string;
  menu: MenuNode[];
}

export interface MenuNode {
  id: string;
  label: string;
  enabled: boolean;
  checked?: boolean;
  submenu?: MenuNode[];
}

export function menuHas(menu: MenuNode[], id: string): MenuNode | undefined {
  for (const m of menu) {
    if (m.id === id) return m;
    const n = m.submenu ? menuHas(m.submenu, id) : undefined;
    if (n) return n;
  }
  return undefined;
}

/** Starts a fake kernel and the Electron app pointed at its state directory. */
export async function launch(opts: { kernel?: FakeKernelOptions; publish?: boolean; lang?: "zh" | "en" } = {}): Promise<Harness> {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-e2e-"));
  const kernel = await FakeKernel.start({ pingMs: 300, ...opts.kernel });
  const publish = (id = "node-a"): void => void kernel.writeManifest(root, id);
  if (opts.publish !== false) publish();

  const userData = fs.mkdtempSync(path.join(os.tmpdir(), "xmesh-ui-userdata-"));
  const args = [`--user-data-dir=${userData}`, UI_ROOT];
  // Root-owned CI containers cannot use Chromium's setuid sandbox.
  if (process.platform === "linux") args.unshift("--no-sandbox");
  const app = await electron.launch({
    args,
    env: {
      ...process.env,
      XMESH_UI_TEST: "1",
      XMESH_UI_STATE_ROOT: root,
      XMESH_UI_LANG: opts.lang ?? "zh",
    },
  });

  // `start()` finishes a few ticks after Electron reports ready.
  await expect
    .poll(() => app.evaluate(() => (globalThis as { __xmeshUiTest?: unknown }).__xmeshUiTest !== undefined))
    .toBe(true);

  if (opts.publish !== false && opts.kernel?.controlVersion === undefined) {
    // Connected and subscribed, so events the test sends next are not lost.
    await expect.poll(() => kernel.connectedStreams, { message: "app should open the event stream" }).toBe(1);
  }

  const hook = <T>(fn: string): Promise<T> => app.evaluate((_e, f) => {
    const h = (globalThis as unknown as { __xmeshUiTest: Record<string, () => unknown> }).__xmeshUiTest;
    return h[f]!();
  }, fn) as Promise<T>;

  let closing: Promise<void> | undefined;
  return {
    app,
    root,
    userData,
    kernel,
    publish,
    tray: () => hook<TraySummary>("tray"),
    clickMenu: (id) =>
      app.evaluate(
        (_e, i) =>
          (globalThis as unknown as { __xmeshUiTest: { clickMenu(id: string): Promise<unknown> } }).__xmeshUiTest.clickMenu(i),
        id,
      ),
    calls: () => hook("calls"),
    notices: () => hook("notices"),
    openPanel: async () => {
      await app.evaluate(() =>
        (globalThis as unknown as { __xmeshUiTest: { showPanel(): void } }).__xmeshUiTest.showPanel(),
      );
      const page = app.windows()[0] ?? await app.waitForEvent("window");
      await page.waitForLoadState("domcontentloaded");
      return page;
    },
    close: () => {
      if (closing) return closing;
      closing = (async () => {
        // Test teardown must bypass the production unsaved-draft beforeunload
        // guard, then wait for the native process before deleting its profile.
        const child = app.process();
        if (child.exitCode === null && child.signalCode === null) {
          await stopNativeTestProcess(child, () => app.evaluate(({ app }) => app.exit(0)));
        }
        await Promise.race([kernel.stop(), new Promise((resolve) => setTimeout(resolve, 5_000))]);
        // Chromium can release its Windows profile database handles just after
        // Electron exits. Use Node's bounded native retry instead of making a
        // test fail during cleanup with EBUSY.
        const removeTree = (directory: string): void => {
          fs.rmSync(directory, { recursive: true, force: true, maxRetries: 8, retryDelay: 250 });
        };
        removeTree(root);
        removeTree(userData);
      })();
      return closing;
    },
  };
}

export async function expectIcon(h: Harness, icon: string): Promise<void> {
  await expect.poll(async () => (await h.tray()).icon, { message: `tray icon should become ${icon}` }).toBe(icon);
}
