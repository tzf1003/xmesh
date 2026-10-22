// Browser entry of the web management panel (src/web). It asks for the
// management key, then provides `window.xmesh` over HTTP and loads the same
// panel bundle (app.js) the desktop app shows. The page never sees the key
// again after login: the session is an HttpOnly cookie.

import { CHANNELS, PANEL_API_GLOBAL, PANEL_API_METHODS, type PanelApi } from "../../shared/ipc";
import type { CallEventData } from "../../shared/control";
import type { Snapshot } from "../../shared/state";
import { WEB_EVENTS, WEB_PATHS, WEB_REQUEST_HEADER, WEB_SESSION_GLOBAL, type WebSessionApi, type WebAnswer, type WebAsk, type WebEffect, type WebInvokeReply, type WebSessionInfo } from "../../shared/web";

const en = /^en\b/i.test(navigator.language);
const text = en
  ? {
      title: "xmesh management",
      lead: "Enter the management key of this host to open the panel.",
      key: "Management key",
      login: "Log in",
      hint: "The key is printed when xmesh-webui first starts. Show it again on the host with `xmesh-webui --print-key`.",
      wrong: "The key is not correct.",
      wait: (s: number) => `Too many attempts. Try again in ${s} s.`,
      failed: "Cannot reach the server.",
      copyTitle: "Copy this text",
      copyLead: "The browser did not allow writing to the clipboard. Select the text and copy it.",
      close: "Close",
      paths: { workspace: "Directory on this host", runnerDir: "Directory on this host", authkeyFile: "Key file on this host", authkeySave: "File to create on this host", allowlistFile: "File on this host", tokenFile: "File on this host", upload: "File on this host to upload", download: "Path on this host to save to" },
      absolute: "Enter an absolute path (starting with /).",
    }
  : {
      title: "xmesh 管理",
      lead: "输入这台主机的管理密钥后打开管理界面。",
      key: "管理密钥",
      login: "登录",
      hint: "xmesh-webui 首次启动时会打印密钥；在主机上运行 `xmesh-webui --print-key` 可再次查看。",
      wrong: "密钥不正确。",
      wait: (s: number) => `尝试次数过多，请 ${s} 秒后再试。`,
      failed: "无法连接服务器。",
      copyTitle: "复制以下内容",
      copyLead: "浏览器不允许写入剪贴板，请选中后手动复制。",
      close: "关闭",
      paths: { workspace: "服务器上的目录", runnerDir: "服务器上的目录", authkeyFile: "服务器上的 key 文件", authkeySave: "要在服务器上创建的文件", allowlistFile: "服务器上的文件", tokenFile: "服务器上的文件", upload: "要上传的服务器文件", download: "保存到服务器上的路径" },
      absolute: "请输入以 / 开头的绝对路径。",
    };

class LoggedOut extends Error {}

async function post(path: string, body: unknown): Promise<Response> {
  return fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", [WEB_REQUEST_HEADER]: "1" },
    body: JSON.stringify(body),
  });
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, props: Partial<HTMLElementTagNameMap[K]> & { class?: string } = {}, ...children: (Node | string)[]): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  const { class: cls, ...rest } = props;
  if (cls) node.className = cls;
  Object.assign(node, rest);
  node.append(...children);
  return node;
}

function showModal(title: string, body: Node[], onClose?: () => void): HTMLElement {
  const close = el("button", { type: "button", class: "web-btn" }, text.close);
  const box = el("div", { class: "web-modal", role: "dialog" }, el("h2", {}, title), ...body, el("div", { class: "web-actions" }, close));
  const layer = el("div", { class: "web-layer" }, box);
  close.addEventListener("click", () => { layer.remove(); onClose?.(); });
  document.body.append(layer);
  close.focus();
  return layer;
}

async function applyEffect(e: WebEffect): Promise<void> {
  if (e.type === "clipboard") {
    try {
      await navigator.clipboard.writeText(e.text);
    } catch {
      // Plain HTTP to a remote host is not a secure context: no clipboard API.
      const area = el("textarea", { class: "web-copy", readOnly: true, rows: Math.min(12, e.text.split("\n").length + 1), value: e.text });
      showModal(text.copyTitle, [el("p", {}, text.copyLead), area]);
      area.select();
    }
    return;
  }
  if (e.type === "open") {
    if (!/^https:\/\//.test(e.url)) return;
    const a = el("a", { href: e.url, target: "_blank", rel: "noopener noreferrer" });
    document.body.append(a);
    a.click();
    a.remove();
    return;
  }
  const url = URL.createObjectURL(new Blob([e.text], { type: "application/json" }));
  const a = el("a", { href: url, download: e.name });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

let loggingOut = false;
function backToLogin(): void {
  if (loggingOut) return;
  loggingOut = true;
  location.reload();
}

async function invoke(channelKey: string, args: unknown[]): Promise<never> {
  // `undefined` is not JSON: trailing ones are dropped, as an absent IPC argument.
  while (args.length > 0 && args[args.length - 1] === undefined) args.pop();
  const res = await post(`${WEB_PATHS.invoke}${channelKey}`, { args });
  if (res.status === 401) {
    backToLogin();
    throw new LoggedOut("login required");
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `HTTP ${res.status}`);
  }
  const reply = (await res.json()) as WebInvokeReply;
  for (const e of reply.effects) await applyEffect(e);
  return reply.result as never;
}

async function answer(a: WebAnswer): Promise<void> {
  await post(WEB_PATHS.answer, a).catch(() => undefined);
}

function onAsk(ask: WebAsk): void {
  if (ask.kind === "confirm") {
    void answer({ id: ask.id, value: window.confirm(ask.detail ? `${ask.title}\n\n${ask.detail}` : ask.title) });
    return;
  }
  let value: string | null = window.prompt(text.paths[ask.pick], ask.suggested ?? "");
  while (value !== null && !value.trim().startsWith("/")) value = window.prompt(`${text.absolute}\n${text.paths[ask.pick]}`, value);
  void answer({ id: ask.id, value: value === null ? null : value.trim() });
}

/** Mirrors the preload's names: methods call the channel of the same key, except where listed. */
const CHANNEL_KEY: Partial<Record<keyof PanelApi, keyof typeof CHANNELS>> = { checkForUpdate: "checkUpdate" };

function applyZoom(snapshot: Snapshot): void {
  // The desktop shell zooms the window; the page zooms itself here.
  const zoom = snapshot.settings.display.zoom;
  document.documentElement.style.setProperty("zoom", zoom === 1 ? "" : String(zoom));
}

export function createWebApi(): PanelApi {
  const snapshots = new Set<(s: Snapshot) => void>();
  const calls = new Set<(c: CallEventData[]) => void>();
  const events = new EventSource(WEB_PATHS.events, { withCredentials: true });
  events.addEventListener(WEB_EVENTS.snapshot, (e) => {
    const snap = JSON.parse((e as MessageEvent<string>).data) as Snapshot;
    applyZoom(snap);
    for (const l of snapshots) l(snap);
  });
  events.addEventListener(WEB_EVENTS.calls, (e) => {
    const c = JSON.parse((e as MessageEvent<string>).data) as CallEventData[];
    for (const l of calls) l(c);
  });
  events.addEventListener(WEB_EVENTS.ask, (e) => onAsk(JSON.parse((e as MessageEvent<string>).data) as WebAsk));
  events.addEventListener(WEB_EVENTS.logout, () => { events.close(); backToLogin(); });
  events.addEventListener("error", () => {
    // A closed stream with a dead session never recovers on its own.
    void fetch(WEB_PATHS.session, { credentials: "same-origin" })
      .then((r) => r.json() as Promise<WebSessionInfo>)
      .then((s) => { if (!s.authenticated) { events.close(); backToLogin(); } }, () => undefined);
  });

  const subscribe = <T>(set: Set<(v: T) => void>) => (listener: (v: T) => void): (() => void) => {
    if (typeof listener !== "function") throw new TypeError("listener must be a function");
    set.add(listener);
    return () => { set.delete(listener); };
  };

  const api: Record<string, unknown> = {
    onSnapshot: subscribe(snapshots),
    onCalls: subscribe(calls),
    // The tray never asks a browser tab to navigate.
    onNavigate: (listener: unknown) => {
      if (typeof listener !== "function") throw new TypeError("listener must be a function");
      return () => {};
    },
  };
  for (const m of PANEL_API_METHODS) {
    if (m in api) continue;
    const key = CHANNEL_KEY[m] ?? (m as keyof typeof CHANNELS);
    if (!(key in CHANNELS)) throw new Error(`no channel for ${m}`);
    api[m] = (...args: unknown[]) => invoke(key, args);
  }
  return Object.freeze(api) as unknown as PanelApi;
}

function loadPanel(): void {
  (globalThis as Record<string, unknown>)[PANEL_API_GLOBAL] = createWebApi();
  const session: WebSessionApi = { logout: () => { void post(WEB_PATHS.logout, {}).finally(backToLogin); } };
  (globalThis as Record<string, unknown>)[WEB_SESSION_GLOBAL] = Object.freeze(session);
  document.body.append(el("script", { src: "app.js" }));
}

function showLogin(info: WebSessionInfo): void {
  document.title = text.title;
  const input = el("input", { type: "password", id: "web-key", name: "key", autocomplete: "current-password", required: true, spellcheck: false });
  input.setAttribute("autofocus", "");
  const error = el("p", { class: "web-error", role: "alert" });
  const submit = el("button", { type: "submit", class: "web-btn primary" }, text.login);
  const form = el("form", { class: "web-login", noValidate: false },
    el("h1", {}, text.title),
    el("p", { class: "web-lead" }, text.lead),
    el("label", { htmlFor: "web-key" }, text.key),
    input,
    error,
    submit,
    el("p", { class: "web-hint" }, text.hint));
  const page = el("main", { class: "web-page" }, form);
  document.body.append(page);
  if (info.retryAfter) error.textContent = text.wait(info.retryAfter);
  input.focus();

  form.addEventListener("submit", (ev) => {
    ev.preventDefault();
    submit.disabled = true;
    error.textContent = "";
    void post(WEB_PATHS.login, { key: input.value }).then(async (res) => {
      if (res.ok) {
        input.value = "";
        page.remove();
        loadPanel();
        return;
      }
      const body = (await res.json().catch(() => ({}))) as { retryAfter?: number };
      error.textContent = res.status === 429 && body.retryAfter ? text.wait(body.retryAfter) : res.status === 401 ? text.wrong : text.failed;
      input.select();
    }, () => { error.textContent = text.failed; }).finally(() => { submit.disabled = false; });
  });
}

async function boot(): Promise<void> {
  let info: WebSessionInfo;
  try {
    info = (await (await fetch(WEB_PATHS.session, { credentials: "same-origin" })).json()) as WebSessionInfo;
  } catch {
    info = { authenticated: false };
  }
  if (info.authenticated) loadPanel();
  else showLogin(info);
}

void boot();
