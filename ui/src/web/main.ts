// xmesh web management panel for Linux hosts: the desktop panel served over
// HTTP(S) behind a management key. Same services as the Electron shell
// (src/main/main.ts) minus the tray and native dialogs.
//
//   node xmesh-webui.js [--listen 127.0.0.1:7480] [--data-dir DIR] [--key-file FILE]
//                       [--tls-cert FILE --tls-key FILE] [--rotate-key] [--print-key] [--lang zh|en]

import * as fs from "node:fs";
import * as http from "node:http";
import * as os from "node:os";
import * as https from "node:https";
import * as path from "node:path";
import { parseArgs } from "node:util";
import { resolveLocale, translator, type Translate } from "../shared/i18n";
import { Actions } from "../main/actions";
import { CliError, cliCommandText, kernelServiceInfo, kernelServiceOptions, runKernelCli, runXmeshSetup } from "../main/cli";
import { ShellController } from "../main/controller";
import { DisplayStore } from "../main/display-store";
import { discoverConfigured, discoverInstances, stateRoots, userConfigDir } from "../main/discovery";
import { GatewayCredentials } from "../main/gateway-token";
import { createHandlers } from "../main/handlers";
import { discoverKernelTargets, KernelLifecycle, recoverKernelPaths, type KernelTarget } from "../main/kernel-lifecycle";
import { kernelConfigNode, kernelInstallOptions } from "../main/kernel-install-options";
import { MeshService, execMeshRunner, meshCredentialPath } from "../main/mesh-service";
import { createOnboardingService } from "../main/onboarding";
import { PanelService } from "../main/panel-service";
import { PlaygroundService } from "../main/playground";
import { PreferencesStore } from "../main/preferences-store";
import { TransferService } from "../main/transfers";
import { linuxApplier } from "../main/update/apply";
import { KernelConfigs } from "../main/update/kernel-configs";
import { httpSource } from "../main/update/source";
import { UpdateStore } from "../main/update/store";
import { UpdateActions } from "../main/update/service";
import { Updater } from "../main/update/updater";
import { loadKey } from "./auth";
import { SERVICE_NAME, findXmesh, installService, realDeps, uninstallService, unitDir, type Deps } from "./service";
import { WebBridge, webPlatform } from "./bridge";
import { WebServer } from "./server";

const DEFAULT_LISTEN = "127.0.0.1:7480";

export interface WebOptions {
  host: string;
  port: number;
  dataDir: string;
  keyFile: string;
  tlsCert?: string;
  tlsKey?: string;
  /** Public origins a reverse proxy serves the panel under. */
  allowedOrigins: string[];
  rotateKey: boolean;
  printKey: boolean;
  lang?: string;
}

/** `host:port`, `[v6]:port` or `:port` (all interfaces). */
export function parseListen(v: string): { host: string; port: number } | undefined {
  const m = /^(?:\[([0-9A-Fa-f:.]+)\]|([A-Za-z0-9.-]*)):(\d{1,5})$/.exec(v);
  if (!m) return undefined;
  const port = Number(m[3]);
  if (!Number.isInteger(port) || port < 1 || port > 65535) return undefined;
  const host = m[1] ?? (m[2] === "" ? "0.0.0.0" : m[2]!);
  return { host, port };
}

/** Flags and the env list as exact `scheme://host[:port]` origins; anything else is an error, not a silent no-op. */
export function parseAllowedOrigins(flags: string[] | undefined, envList: string | undefined): string[] {
  const raw = [...(flags ?? []), ...(envList ?? "").split(",")].map((s) => s.trim()).filter((s) => s !== "");
  return [...new Set(raw.map((v) => {
    let u: URL;
    try { u = new URL(v); } catch { throw new Error(`--allowed-origin must be scheme://host[:port]: ${v}`); }
    if ((u.protocol !== "http:" && u.protocol !== "https:") || u.origin !== v.replace(/\/+$/, "").toLowerCase()) {
      throw new Error(`--allowed-origin must be scheme://host[:port]: ${v}`);
    }
    return u.origin;
  }))];
}

export function isLoopback(host: string): boolean {
  return host === "localhost" || host === "::1" || /^127\./.test(host);
}

function usage(): string {
  return [
    "usage: xmesh-webui [options]",
    "",
    `  --listen ADDR      address to serve on (default ${DEFAULT_LISTEN}; env XMESH_WEBUI_LISTEN)`,
    "  --data-dir DIR     panel settings and the key file (default $XDG_CONFIG_HOME/xmesh-webui)",
    "  --key-file FILE    management key file, 0600 (default <data-dir>/webui.key)",
    "  --tls-cert FILE    serve HTTPS with this certificate chain (PEM)",
    "  --tls-key FILE     private key for --tls-cert (PEM)",
    "  --allowed-origin O browser origin to accept behind a reverse proxy, e.g. https://panel.example.com:8088",
    "                     (repeatable; env XMESH_WEBUI_ALLOWED_ORIGINS, comma separated)",
    "  --rotate-key       replace the management key, print the new one, and continue",
    "  --print-key        print the management key and exit",
    "  --lang zh|en       panel language when nothing is saved (env XMESH_UI_LANG)",
    "",
    "systemd user service (run as the user that runs the xmesh kernel):",
    "  xmesh-webui install [--listen ADDR] [--tls-cert F --tls-key F] [--data-dir D] [--key-file F]",
    "                      [--allowed-origin O]",
    "                      [--key-stdin | --rotate-key] [--xmesh-bin PATH] [--lang zh|en]",
    "                      [--insecure] [--no-start] [--linger] [--print]",
    "  xmesh-webui uninstall [--purge]",
  ].join("\n");
}

export function parseWebArgs(argv: string[], env: NodeJS.ProcessEnv = process.env): WebOptions {
  const { values } = parseArgs({
    args: argv,
    strict: true,
    options: {
      listen: { type: "string" },
      "data-dir": { type: "string" },
      "key-file": { type: "string" },
      "tls-cert": { type: "string" },
      "tls-key": { type: "string" },
      "allowed-origin": { type: "string", multiple: true },
      "rotate-key": { type: "boolean" },
      "print-key": { type: "boolean" },
      lang: { type: "string" },
      help: { type: "boolean", short: "h" },
    },
  });
  if (values.help) {
    console.log(usage());
    process.exit(0);
  }
  const listen = parseListen(values.listen ?? env.XMESH_WEBUI_LISTEN ?? DEFAULT_LISTEN);
  if (!listen) throw new Error("--listen must be host:port");
  if ((values["tls-cert"] === undefined) !== (values["tls-key"] === undefined)) throw new Error("--tls-cert and --tls-key go together");
  const dataDir = path.resolve(values["data-dir"] ?? env.XMESH_WEBUI_DATA_DIR ?? path.join(userConfigDir(process.platform, env), "xmesh-webui"));
  return {
    ...listen,
    dataDir,
    keyFile: path.resolve(values["key-file"] ?? env.XMESH_WEBUI_KEY_FILE ?? path.join(dataDir, "webui.key")),
    ...(values["tls-cert"] !== undefined && { tlsCert: values["tls-cert"], tlsKey: values["tls-key"] }),
    allowedOrigins: parseAllowedOrigins(values["allowed-origin"], env.XMESH_WEBUI_ALLOWED_ORIGINS),
    rotateKey: values["rotate-key"] === true,
    printKey: values["print-key"] === true,
    ...((values.lang ?? env.XMESH_UI_LANG) !== undefined && { lang: values.lang ?? env.XMESH_UI_LANG }),
  };
}

/** dist/web next to this file once bundled (scripts/build-web.mjs); the tsc build runs from dist/src/web. */
function publicRoot(): string {
  const candidates = [path.join(__dirname, "public"), path.join(__dirname, "..", "..", "web", "public")];
  const found = candidates.find((d) => fs.existsSync(path.join(d, "index.html")));
  if (!found) throw new Error(`panel files not found (looked in ${candidates.join(", ")}); run npm run build:web`);
  return found;
}

function version(): string {
  if (process.env.XMESH_WEBUI_VERSION) return process.env.XMESH_WEBUI_VERSION;
  try {
    return fs.readFileSync(path.join(__dirname, "VERSION"), "utf8").trim();
  } catch {}
  try {
    return (JSON.parse(fs.readFileSync(path.join(__dirname, "..", "..", "..", "package.json"), "utf8")) as { version: string }).version;
  } catch {
    return "0.0.0";
  }
}

export async function startWeb(opts: WebOptions): Promise<{ close: () => Promise<void>; url: string }> {
  fs.mkdirSync(opts.dataDir, { recursive: true, mode: 0o700 });
  const loaded = loadKey(opts.keyFile, opts.rotateKey);
  let key = loaded.key;
  if (loaded.created) {
    console.error(`xmesh-webui: 新的管理密钥 / new management key: ${key}`);
    console.error(`xmesh-webui: 已保存到 / saved in ${opts.keyFile} (0600)`);
  }

  const preferences = new PreferencesStore(path.join(opts.dataDir, "preferences.json"), resolveLocale(opts.lang));
  let locale = preferences.get().locale;
  const t: Translate = (k, params) => translator(locale)(k, params);
  const roots = stateRoots();
  const display = new DisplayStore(path.join(opts.dataDir, "display.json"));
  const log = (line: string): void => console.error(line);

  // The panel updates itself from the release's install-linux.sh when it was installed by it (see apply.ts); the
  // kernel services are restarted by that script, so the updater here stops and starts no nodes.
  const feed = process.env.XMESH_UPDATE_FEED;
  const script = (() => { try { return fs.realpathSync(process.argv[1] ?? ""); } catch { return ""; } })();
  const updater = new Updater({
    version: version(),
    packaged: fs.existsSync(path.join(__dirname, "VERSION")),
    platform: process.platform,
    arch: process.arch,
    kernelBundled: false,
    execPath: process.execPath,
    pid: process.pid,
    dataDir: path.join(opts.dataDir, "update"),
    source: httpSource({
      fetch: (url, init) => fetch(url, init),
      // XMESH_UPDATE_FEED: a directory that serves `releases` and `download/<tag>/<file>` like GitHub does.
      ...(feed && /^https?:\/\//.test(feed) && { apiBase: feed, downloadBase: `${feed.replace(/\/+$/, "")}/download` }),
    }),
    applier: process.platform === "linux" ? linuxApplier(script, path.join(unitDir(process.env, os.homedir()), `${SERVICE_NAME}.service`)) : undefined,
    store: new UpdateStore(path.join(opts.dataDir, "update.json")),
    runningNodes: () => [],
    stopNode: async () => "absent",
    startNode: async () => {},
    quit: () => { void web?.close().finally(() => process.exit(0)); },
    openPath: async () => "",
    notify: (n) => log(`xmesh-webui: ${n.kind} ${n.version}`),
  });
  let web: { close: () => Promise<void> } | undefined;

  const kernelConfigs = new KernelConfigs(path.join(opts.dataDir, "kernel-configs.json"), runXmeshSetup);
  const controller = new ShellController({
    getUpdate: () => updater.state,
    discover: () => discoverInstances(roots),
    configured: () => [...new Set([...discoverConfigured(roots), ...kernelConfigs.nodes()])],
    cliCommand: cliCommandText(),
    locale,
    getLoginItem: () => false,
    getDisplay: () => display.get(),
    getAppPreferences: () => preferences.get(),
    setAppPreferences: (next) => { const saved = preferences.set(next); if (saved) locale = next.locale; return saved; },
    // Theme and zoom are applied by the page; here they are only saved.
    setDisplay: (prefs) => { if (!display.set(prefs)) log(`xmesh-webui: ${t("display.saveFailed")}`); },
    setLoginItem: () => {},
  });

  const serviceIdentity = (target: KernelTarget): string => target.serviceNode ?? target.instance?.manifest.node ?? kernelConfigNode(target);
  const kernelLifecycle = new KernelLifecycle({
    blocked: () => false,
    targets: () => discoverKernelTargets(roots, (id) => kernelConfigs.paths(id), undefined, kernelConfigs.nodes()),
    resolve: async (target) => {
      const serviceNode = serviceIdentity(target);
      return recoverKernelPaths(target, serviceNode, async () => {
        if (target.instance) {
          await kernelConfigs.capture(target.id, target.instance);
          return kernelConfigs.paths(target.id)!;
        }
        throw new Error("控制通道不可用");
      }, () => kernelServiceInfo({ ...target, serviceNode }),
      (paths) => kernelConfigs.register(target.id, paths, serviceNode));
    },
    installOptions: kernelInstallOptions,
    serviceOptions: kernelServiceOptions,
    installed: async (id, paths) => {
      const serviceNode = kernelConfigNode(paths);
      kernelConfigs.register(id, paths, serviceNode);
      return serviceNode;
    },
    run: runKernelCli,
    changed: () => controller.nudge(),
  });

  let server: WebServer | undefined;
  const bridge = new WebBridge((session, ask) => server?.sendAsk(session, ask) ?? false);
  // Start, stop and restart go through the lifecycle, which finds the service by the registration it recorded
  // or the config's node name; the node name in the control manifest is not always the one the service was
  // installed under.
  const platform = webPlatform(bridge, (message) => log(`xmesh-webui: ${message}`), async (op, node) => {
    const result = await kernelLifecycle.request({ op, node });
    if (!result.ok) throw new CliError(op, result.error.message);
  });
  const actions = new Actions(controller, platform, t, new GatewayCredentials(controller));

  const meshCredential = meshCredentialPath(roots[0]!);
  const meshService = new MeshService(execMeshRunner(), () => {
    const c = controller.snapshot.connection;
    const file = c.kind === "connected" ? c.status.paths?.config_file : undefined;
    return typeof file === "string" && file !== "" ? file : undefined;
  }, () => meshCredential);
  const panelService = new PanelService(controller);
  const playground = new PlaygroundService(controller, {
    copy: (text) => bridge.effect({ type: "clipboard", text }),
    // The result is saved by the browser, on the viewer's machine.
    save: async (text) => { bridge.effect({ type: "download", name: "xmesh-tool-result.json", text }); return true; },
  });
  // Transfers move files between this host and a device: the local side is a path on this host.
  const transfers = new TransferService(playground, controller, {
    upload: () => bridge.pickPath("upload"),
    download: (name) => bridge.pickPath("download", name),
    overwrite: (remote, device) => platform.confirm({
      title: locale === "en" ? "Overwrite remote file?" : "覆盖远端文件？", detail: `${device}: ${remote}`,
      confirmLabel: locale === "en" ? "Overwrite" : "覆盖", cancelLabel: locale === "en" ? "Cancel" : "取消",
    }),
  });
  let playgroundKey = "";
  controller.on("change", (s) => {
    const c = s.connection;
    const k = JSON.stringify([s.selectedInstance, c.kind, c.kind === "connected" ? c.status.control.boot_id : "", c.kind === "connected" ? c.status.gateway : null]);
    if (k !== playgroundKey) { playgroundKey = k; transfers.reset(); void playground.close(); }
  });

  const handlers = createHandlers({
    kernel: (request) => kernelLifecycle.request(request),
    onboarding: createOnboardingService(roots[0]!, async (url) => platform.openExternal(url), undefined, (text) => platform.writeClipboard(text)),
    playground,
    transfers,
    controller,
    actions,
    service: panelService,
    previewSetup: () => ({ ok: false, error: { code: "setup_migrated", message: "请使用开始使用向导生成设置计划。" } }),
    setup: async () => ({ ok: false, error: { code: "setup_migrated", message: "请使用开始使用向导继续设置任务。" } }),
    mesh: (request) => meshService.request(request),
    update: new UpdateActions(updater),
  });

  const secure = opts.tlsCert !== undefined;
  server = new WebServer({ key: () => key, handlers, controller, bridge, publicRoot: publicRoot(), secure, allowedOrigins: opts.allowedOrigins, log });
  const listener = secure
    ? https.createServer({ cert: fs.readFileSync(opts.tlsCert!), key: fs.readFileSync(opts.tlsKey!) }, server.handle)
    : http.createServer(server.handle);
  // SSE streams stay open; only the headers have a deadline.
  listener.requestTimeout = 0;
  listener.headersTimeout = 30_000;

  await new Promise<void>((resolve, reject) => {
    listener.once("error", reject);
    listener.listen(opts.port, opts.host, () => { listener.off("error", reject); resolve(); });
  });
  controller.start();
  updater.on("change", (state) => controller.setUpdate(state));
  void updater.finishPending().finally(() => updater.start());

  const addr = listener.address();
  const port = typeof addr === "object" && addr ? addr.port : opts.port;
  const shown = opts.host.includes(":") ? `[${opts.host}]` : opts.host;
  const url = `${secure ? "https" : "http"}://${shown}:${port}/`;
  log(`xmesh-webui: 管理界面 / panel at ${url}`);
  if (!secure && !isLoopback(opts.host)) {
    log("xmesh-webui: 警告：未启用 TLS 却监听非本机地址，管理密钥与会话会以明文传输。请使用 --tls-cert/--tls-key，或只监听 127.0.0.1 并经 SSH 隧道访问。");
  }

  // SIGHUP re-reads the key file, so a key written by hand applies without a restart.
  const reload = (): void => {
    try {
      const next = loadKey(opts.keyFile).key;
      if (next !== key) { key = next; server?.logoutAll(); log("xmesh-webui: 管理密钥已更新，所有会话已退出"); }
    } catch (e) {
      log(`xmesh-webui: ${e instanceof Error ? e.message : String(e)}`);
    }
  };
  process.on("SIGHUP", reload);

  const handle = {
    url,
    close: async () => {
      updater.stop();
      process.off("SIGHUP", reload);
      server?.close();
      transfers.reset();
      await playground.close();
      await controller.stop();
      listener.closeAllConnections();
      await new Promise<void>((resolve) => listener.close(() => resolve()));
    },
  };
  web = handle;
  return handle;
}

function readStdinLine(): string {
  return fs.readFileSync(0, "utf8").split(/\r?\n/, 1)[0]!.trim();
}

/** `install` and `uninstall`; returns the process exit code. */
export function runServiceCommand(cmd: "install" | "uninstall", argv: string[], env: NodeJS.ProcessEnv = process.env, deps: Deps = realDeps(), stdinLine: () => string = readStdinLine, script: string = fs.realpathSync(process.argv[1] ?? "")): number {
  const { values } = parseArgs({
    args: argv,
    strict: true,
    options: {
      listen: { type: "string" }, "data-dir": { type: "string" }, "key-file": { type: "string" },
      "tls-cert": { type: "string" }, "tls-key": { type: "string" }, "allowed-origin": { type: "string", multiple: true }, "xmesh-bin": { type: "string" }, lang: { type: "string" },
      "key-stdin": { type: "boolean" }, "rotate-key": { type: "boolean" }, insecure: { type: "boolean" },
      "no-start": { type: "boolean" }, linger: { type: "boolean" }, print: { type: "boolean" }, purge: { type: "boolean" },
      help: { type: "boolean", short: "h" },
    },
  });
  if (values.help) {
    console.log(usage());
    return 0;
  }
  // The service is a systemd user service, so its default directory is the Linux (XDG) one.
  const dataDir = path.resolve(values["data-dir"] ?? env.XMESH_WEBUI_DATA_DIR ?? path.join(userConfigDir("linux", env, deps.home), "xmesh-webui"));
  const keyFile = path.resolve(values["key-file"] ?? env.XMESH_WEBUI_KEY_FILE ?? path.join(dataDir, "webui.key"));
  try {
    if (cmd === "uninstall") {
      uninstallService({ dataDir, keyFile, ...(values.purge && { purge: true }) }, deps);
      return 0;
    }
    const listen = values.listen ?? env.XMESH_WEBUI_LISTEN ?? DEFAULT_LISTEN;
    if (!parseListen(listen)) throw new Error("--listen must be host:port");
    if (values["key-stdin"] && values["rotate-key"]) throw new Error("--key-stdin and --rotate-key exclude each other");
    const lang = values.lang ?? env.XMESH_UI_LANG;
    const allowedOrigins = parseAllowedOrigins(values["allowed-origin"], env.XMESH_WEBUI_ALLOWED_ORIGINS);
    const xmeshBin = values["xmesh-bin"] ?? findXmesh(env);
    if (xmeshBin === undefined) deps.out("警告：PATH 中找不到 xmesh，面板的重启、停止等操作会不可用；可用 --xmesh-bin 指定。");
    const result = installService({
      node: process.execPath, script, listen, dataDir, keyFile,
      ...(values["tls-cert"] !== undefined && { tlsCert: values["tls-cert"] }),
      ...(values["tls-key"] !== undefined && { tlsKey: values["tls-key"] }),
      ...(allowedOrigins.length > 0 && { allowedOrigins }),
      ...(xmeshBin !== undefined && { xmeshBin: path.resolve(xmeshBin) }),
      ...(lang !== undefined && { lang }),
      ...(values["key-stdin"] && { key: stdinLine() }),
      ...(values["rotate-key"] && { rotateKey: true }),
      ...(values.insecure && { insecure: true }), ...(values["no-start"] && { noStart: true }),
      ...(values.linger && { linger: true }), ...(values.print && { print: true }),
    }, deps);
    // Only a key made here is shown; one the user typed or supplied is never echoed back.
    if (result.key !== undefined && !values["key-stdin"]) deps.out(`管理密钥 / management key: ${result.key}`);
    if (!values.print) deps.out(`密钥文件 ${result.keyFile}（0600）；随时可用 --print-key 查看`);
    return 0;
  } catch (e) {
    console.error(`xmesh-webui: ${e instanceof Error ? e.message : String(e)}`);
    return 1;
  }
}

if (require.main === module) {
  const sub = process.argv[2];
  if (sub === "install" || sub === "uninstall") {
    try {
      process.exit(runServiceCommand(sub, process.argv.slice(3)));
    } catch (e) {
      console.error(`xmesh-webui: ${e instanceof Error ? e.message : String(e)}\n\n${usage()}`);
      process.exit(2);
    }
  }
  let opts: WebOptions;
  try {
    opts = parseWebArgs(process.argv.slice(2));
  } catch (e) {
    console.error(`xmesh-webui: ${e instanceof Error ? e.message : String(e)}\n\n${usage()}`);
    process.exit(2);
  }
  if (opts.printKey) {
    try {
      console.log(loadKey(opts.keyFile, opts.rotateKey).key);
      process.exit(0);
    } catch (e) {
      console.error(`xmesh-webui: ${e instanceof Error ? e.message : String(e)}`);
      process.exit(1);
    }
  }
  startWeb(opts).then((web) => {
    const stop = (): void => { void web.close().finally(() => process.exit(0)); };
    process.once("SIGINT", stop);
    process.once("SIGTERM", stop);
  }, (e: unknown) => {
    console.error(`xmesh-webui: ${e instanceof Error ? e.message : String(e)}`);
    process.exit(1);
  });
}
