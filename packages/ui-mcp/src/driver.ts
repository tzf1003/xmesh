import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { randomUUID } from "node:crypto";
import {
  chromium,
  _electron,
  expect,
  type BrowserContext,
  type Browser,
  type BrowserServer,
  type Page,
  type Locator,
  type FrameLocator,
  type ElectronApplication,
} from "@playwright/test";
import type { WorkerInit, Step, Target } from "./types.js";
import { privateDir, inside } from "./config.js";
import { requireValue, UIError } from "./errors.js";

type Emit = (event: Record<string, unknown>) => void;
export class Driver {
  context!: BrowserContext;
  browser?: Browser;
  browserServer?: BrowserServer;
  electronApp?: ElectronApplication;
  page!: Page;
  pages = new Map<string, Page>();
  versions: Record<string, string> = { node: process.version };
  private responses: Array<{ url: string; status: number }> = [];
  private seenPages = new Set<Page>();
  private initCleanup?: () => Promise<void>;
  constructor(
    readonly init: WorkerInit,
    private emit: Emit,
  ) {}
  async open(): Promise<Record<string, unknown>> {
    const { environment: e, session_dir: dir } = this.init;
    privateDir(dir);
    privateDir(path.join(dir, "state"));
    if (e.state_template)
      fs.cpSync(e.state_template, path.join(dir, "state"), { recursive: true });
    if (e.driver === "web") {
      this.browserServer = await chromium.launchServer({
        executablePath: e.executable,
        headless: true,
        args: e.args,
      });
      this.emit({
        kind: "process",
        pid: this.browserServer.process().pid,
        group: process.platform !== "win32",
      });
      this.browser = await chromium.connect(this.browserServer.wsEndpoint());
      this.context = await this.browser.newContext({
        storageState: this.init.account?.storage_state,
        acceptDownloads: true,
      });
      this.versions.browser = this.browser.version();
      await this.context.route("**/*", async (route) => {
        const req = route.request();
        if (req.isNavigationRequest()) {
          const url = new URL(req.url());
          if (!e.allowed_origins?.includes(url.origin)) {
            this.emit({ kind: "navigation_denied", origin: url.origin });
            await route.abort("blockedbyclient");
            return;
          }
        }
        await route.continue();
      });
    } else {
      this.electronApp = await _electron.launch({
        executablePath: e.executable,
        args: [
          "--disable-renderer-backgrounding",
          "--disable-backgrounding-occluded-windows",
          "--disable-background-timer-throttling",
          `--user-data-dir=${path.join(dir, "profile")}`,
          ...(e.args ?? []),
        ],
        cwd: e.cwd,
        env: {
          ...process.env,
          ...e.env,
          XMESH_UI_STATE_ROOT: path.join(dir, "state"),
          XMESH_UI_BACKGROUND_TEST: "1",
        },
        timeout: 30_000,
      });
      this.emit({
        kind: "process",
        pid: this.electronApp.process().pid,
        group: process.platform !== "win32",
      });
      this.context = this.electronApp.context();
      this.versions.electron = await this.electronApp.evaluate(
        () => process.versions.electron!,
      );
    }
    this.context.setDefaultTimeout(this.init.config.limits.step_timeout_ms);
    this.context.setDefaultNavigationTimeout(
      this.init.config.limits.step_timeout_ms,
    );
    this.context.on("page", (p) => this.register(p));
    for (const p of this.context.pages()) this.register(p);
    if (e.init_module) {
      const module = await import(pathToFileURL(e.init_module).href);
      requireValue(
        typeof module.default === "function",
        "preflight_failed",
        "init module must export a default function",
      );
      const cleanup = await module.default({
        context: this.context,
        electronApp: this.electronApp,
        sessionDir: dir,
        environment: e,
        registerProcess: (pid: number) =>
          this.emit({ kind: "process", pid, group: false }),
      });
      if (typeof cleanup === "function") this.initCleanup = cleanup;
    }
    this.page =
      e.driver === "electron"
        ? await this.electronApp!.firstWindow()
        : (this.context.pages()[0] ?? (await this.context.newPage()));
    this.seenPages.add(this.page);
    this.register(this.page);
    if (this.electronApp) {
      const state = await this.electronApp.evaluate(({ BrowserWindow }) => ({
        supported: !!(
          globalThis as unknown as { __xmeshUiBackgroundTest?: boolean }
        ).__xmeshUiBackgroundTest,
        visible: BrowserWindow.getAllWindows().some(
          (w: { isVisible(): boolean }) => w.isVisible(),
        ),
        focused: BrowserWindow.getAllWindows().some(
          (w: { isFocused(): boolean }) => w.isFocused(),
        ),
      }));
      requireValue(
        state.supported && !state.visible && !state.focused,
        "background_required",
        "Electron test build must acknowledge background mode and keep all windows hidden and unfocused",
      );
    }
    if (e.driver === "web")
      await this.page.goto(e.url!, { waitUntil: "domcontentloaded" });
    return {
      versions: this.versions,
      pages: this.listPages(),
      page_id: this.pageId(this.page),
    };
  }
  private register(page: Page): void {
    if ([...this.pages.values()].includes(page)) return;
    const id = `p_${randomUUID()}`;
    this.pages.set(id, page);
    page.on("console", (m) =>
      this.emit({
        kind: "console",
        level: m.type(),
        text: m.text().slice(0, 2048),
      }),
    );
    page.on("pageerror", (e) =>
      this.emit({ kind: "pageerror", message: e.message.slice(0, 2048) }),
    );
    page.on("response", (r) => {
      const url = new URL(r.url());
      url.search = "";
      url.hash = "";
      this.responses.push({ url: r.url(), status: r.status() });
      if (this.responses.length > 100) this.responses.shift();
      this.emit({ kind: "response", url: url.href, status: r.status() });
    });
    page.on("close", () => {
      this.pages.delete(id);
      this.emit({ kind: "page_closed", page_id: id });
    });
  }
  pageId(p: Page): string {
    return [...this.pages.entries()].find(([, value]) => value === p)![0];
  }
  listPages(): Array<{ page_id: string; url: string }> {
    return [...this.pages].map(([page_id, p]) => ({ page_id, url: p.url() }));
  }
  locator(
    t: Target,
    scope: Page | FrameLocator | Locator = this.page,
  ): Locator {
    const frame = t.frame ? scope.frameLocator(t.frame) : scope;
    const base = t.within ? this.locator(t.within, frame) : frame;
    if (t.testId) return base.getByTestId(t.testId);
    if (t.role)
      return base.getByRole(t.role as Parameters<Page["getByRole"]>[0], {
        name: t.name,
        exact: true,
      });
    if (t.label) return base.getByLabel(t.label, { exact: true });
    if (t.text) return base.getByText(t.text, { exact: true });
    if (t.css) return base.locator(t.css);
    throw new UIError("invalid_args", "unresolved locator");
  }
  async snapshot(
    pageId?: string,
    target?: Target,
    maxLength = 20000,
  ): Promise<Record<string, unknown>> {
    if (pageId) this.selectPage(pageId);
    const base = target ? this.locator(target) : this.page.locator("body");
    const aria = await base.ariaSnapshot({
      timeout: this.init.config.limits.step_timeout_ms,
    });
    const testIds = await base.locator("[data-testid]").evaluateAll((nodes) =>
      nodes.slice(0, 200).map((n) => ({
        testId: n.getAttribute("data-testid"),
        tag: n.tagName.toLowerCase(),
      })),
    );
    return {
      page_id: this.pageId(this.page),
      url: this.page.url(),
      pages: this.listPages(),
      aria: aria.slice(0, maxLength),
      truncated: aria.length > maxLength,
      test_ids: testIds,
    };
  }
  private selectPage(id: string): void {
    const p = this.pages.get(id);
    requireValue(p, "not_found", "unknown page");
    this.page = p;
    this.seenPages.add(p);
  }
  beginRun(): void {
    this.responses = [];
  }
  async assertBackground(): Promise<void> {
    if (!this.electronApp) return;
    const windows = await this.electronApp.evaluate(({ BrowserWindow }) =>
      BrowserWindow.getAllWindows().map(
        (w: { isVisible(): boolean; isFocused(): boolean }) => ({
          visible: w.isVisible(),
          focused: w.isFocused(),
        }),
      ),
    );
    requireValue(
      windows.every(
        (w: { visible: boolean; focused: boolean }) => !w.visible && !w.focused,
      ),
      "background_required",
      "test window became visible or focused",
    );
  }
  async step(s: Step, dir: string): Promise<unknown> {
    await this.assertBackground();
    const timeout = s.timeout_ms ?? this.init.config.limits.step_timeout_ms;
    const target = s.target ? this.locator(s.target) : undefined;
    const click = () => target!.click({ timeout });
    switch (s.action) {
      case "navigate":
        return this.page
          .goto(new URL(s.value!, this.init.environment.url).href, {
            timeout,
            waitUntil: "domcontentloaded",
          })
          .then(() => undefined);
      case "select_page":
        this.selectPage(s.page_id!);
        return { page_id: s.page_id };
      case "wait_page": {
        const p =
          [...this.pages.values()].find((p) => !this.seenPages.has(p)) ??
          (await this.context.waitForEvent("page", { timeout }));
        await p.waitForLoadState("domcontentloaded", { timeout });
        this.page = p;
        this.seenPages.add(p);
        return { page_id: this.pageId(p) };
      }
      case "click":
        return click();
      case "fill":
        return target!.fill(
          s.secret ? this.init.account!.secrets![s.secret]! : s.value!,
          { timeout },
        );
      case "press":
        return target!.press(s.value!, { timeout });
      case "check":
        return target!.setChecked(s.checked ?? true, { timeout });
      case "select":
        return target!.selectOption(s.value!, { timeout });
      case "hover":
        return target!.hover({ timeout });
      case "scroll":
        await target!.scrollIntoViewIfNeeded({ timeout });
        if (s.x || s.y) await this.page.mouse.wheel(s.x ?? 0, s.y ?? 0);
        return;
      case "upload": {
        const root = this.init.environment.upload_root;
        requireValue(root, "path_denied", "upload root is not configured");
        return target!.setInputFiles(inside(root, s.file!), { timeout });
      }
      case "download": {
        const [download] = await Promise.all([
          this.page.waitForEvent("download", { timeout }),
          click(),
        ]);
        const file = path.join(dir, `download-${randomUUID()}`);
        await download.saveAs(file);
        return {
          artifact: {
            file,
            name: download.suggestedFilename(),
            mime: "application/octet-stream",
          },
        };
      }
      case "dialog":
        this.page.once("dialog", (d) => {
          void (s.accept ? d.accept(s.value) : d.dismiss());
        });
        return;
      case "wait":
      case "assert": {
        const e = s.expect!;
        if (e.url !== undefined)
          return expect(this.page).toHaveURL(e.url, { timeout });
        if (e.visible !== undefined)
          return e.visible
            ? expect(target!).toBeVisible({ timeout })
            : expect(target!).toBeHidden({ timeout });
        if (e.enabled !== undefined)
          return e.enabled
            ? expect(target!).toBeEnabled({ timeout })
            : expect(target!).toBeDisabled({ timeout });
        if (e.text !== undefined)
          return expect(target!).toHaveText(e.text, { timeout });
        if (e.containsText !== undefined)
          return expect(target!).toContainText(e.containsText, { timeout });
        if (e.value !== undefined)
          return expect(target!).toHaveValue(e.value, { timeout });
        if (e.count !== undefined)
          return expect(target!).toHaveCount(e.count, { timeout });
        return;
      }
      case "response": {
        const url = new URL(s.value!, this.init.environment.url).href;
        const cached = [...this.responses].reverse().find((r) => r.url === url);
        const status =
          cached?.status ??
          (
            await this.page.waitForResponse((r) => r.url() === url, { timeout })
          ).status();
        requireValue(
          status === s.status,
          "assertion_failed",
          "response status mismatch",
        );
        return { status };
      }
      case "read":
        return { text: (await target!.innerText({ timeout })).slice(0, 16000) };
      case "screenshot":
        return {
          artifact: await this.screenshot(
            dir,
            s.name ?? "screenshot.png",
            target,
          ),
        };
    }
  }
  async screenshot(
    dir: string,
    name: string,
    target?: Locator,
  ): Promise<{ file: string; name: string; mime: string }> {
    const file = path.join(dir, `screenshot-${randomUUID()}.png`);
    const mask = [
      this.page.locator('input[type="password"]'),
      ...(this.init.environment.mask ?? []).map((s) => this.page.locator(s)),
    ];
    const timeout = this.init.config.limits.step_timeout_ms;
    if (target) await target.screenshot({ path: file, mask, timeout });
    else await this.page.screenshot({ path: file, mask, timeout });
    return { file, name, mime: "image/png" };
  }
  async close(): Promise<void> {
    try {
      if (this.electronApp) await this.electronApp.close();
      else {
        await this.browser?.close();
        await this.browserServer?.close();
      }
    } finally {
      await this.initCleanup?.();
    }
  }
}
