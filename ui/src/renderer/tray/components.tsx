import type { ComponentChildren, JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { RecentCallView, TrayConnected, TrayPeer } from "../../shared/tray";
import { Icon, type IconName } from "../icons";
export type Text = (zh: string, en: string) => string;

export const Dot = ({ color = "green" }: { color?: string }): JSX.Element => (
  <span class={`dot ${color}`} aria-hidden="true" />
);
export const Button = ({
  children,
  onClick,
  disabled,
  class: cls = "",
  testid,
}: {
  children: ComponentChildren;
  onClick(): void;
  disabled?: boolean;
  class?: string;
  testid?: string;
}): JSX.Element => (
  <button type="button" class={`button ${cls}`} onClick={onClick} disabled={disabled} data-testid={testid}>
    {children}
  </button>
);
export function IconButton({
  name,
  label,
  onClick,
  disabled,
  active,
}: {
  name: IconName;
  label: string;
  onClick(): void;
  disabled?: boolean;
  active?: boolean;
}): JSX.Element {
  return (
    <button
      type="button"
      class={`icon-button ${active ? "active" : ""}`}
      title={label}
      aria-label={label}
      onClick={onClick}
      disabled={disabled}
    >
      <Icon name={name} />
    </button>
  );
}
export function Dialog({
  title,
  children,
  close,
}: {
  title: string;
  children: ComponentChildren;
  close(): void;
}): JSX.Element {
  const ref = useRef<HTMLDivElement>(null),
    closeRef = useRef(close);
  closeRef.current = close;
  useEffect(() => {
    const old = document.activeElement as HTMLElement | null,
      el = ref.current!;
    el.querySelector<HTMLElement>("button")?.focus();
    const key = (e: KeyboardEvent): void => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeRef.current();
      }
      if (e.key === "Tab") {
        const buttons = [...el.querySelectorAll<HTMLElement>("button:not(:disabled),input,select,textarea")];
        if (e.shiftKey && document.activeElement === buttons[0]) {
          e.preventDefault();
          buttons.at(-1)?.focus();
        }
        if (!e.shiftKey && document.activeElement === buttons.at(-1)) {
          e.preventDefault();
          buttons[0]?.focus();
        }
      }
    };
    el.addEventListener("keydown", key);
    return () => {
      el.removeEventListener("keydown", key);
      if (old?.isConnected) old.focus();
    };
  }, []);
  return (
    <div class="modal-scrim">
      <div class="modal" role="dialog" aria-modal="true" aria-label={title} ref={ref}>
        <div class="modal-heading">
          <h2>{title}</h2>
          <IconButton name="close" label={title} onClick={close} />
        </div>
        {children}
      </div>
    </div>
  );
}
export function peerPath(p: TrayPeer, t: Text): string {
  if (!p.online)
    return p.last_seen
      ? `${t("上次在线", "Last seen")} ${new Date(p.last_seen).toLocaleString()}`
      : t("离线", "Offline");
  if (p.active_path === "lan") return t("局域网 · 直连", "LAN · Direct");
  if (p.active_path === "direct") return t("公网直连", "Public direct");
  if (p.active_path === "cloudflare") return t("Cloudflare 通道", "Cloudflare Tunnel");
  if (p.connection === "direct") return t("点对点直连", "Direct");
  if (p.connection === "derp") return `${t("中继", "Relay")}${p.derp_region ? ` · ${p.derp_region}` : ""}`;
  return t("连接方式待确认", "Connection pending");
}
export function Calls({
  calls,
  t,
  choose,
}: {
  calls: RecentCallView[];
  t: Text;
  choose(call: RecentCallView): void;
}): JSX.Element {
  return (
    <div class="card call-list">
      {calls.length ? (
        calls.map((call) => (
          <button type="button" class="call-row" key={call.eventId} onClick={() => choose(call)}>
            <Icon name="terminal" />
            <span class="call-info">
              <span class="call-top">
                <strong title={call.oauth_client ?? call.caller}>{call.oauth_client ?? call.caller}</strong>
                <code title={call.tool}>{call.tool}</code>
                <time dateTime={call.time}>
                  {new Date(call.time).toLocaleTimeString([], {
                    hour12: false,
                  })}
                </time>
              </span>
              <span class="call-bottom">
                <span>
                  <Icon name="arrow" />
                  {call.device}
                </span>
                <span class={call.status === "error" ? "error-text" : ""}>
                  {call.status === "error" && <Dot color="red" />}
                  {call.status === "error" ? call.error : `${call.duration_ms} ms`}
                </span>
              </span>
            </span>
          </button>
        ))
      ) : (
        <div class="empty-state">
          <Icon name="activity" />
          <strong>{t("还没有调用记录", "No recent calls")}</strong>
          <p>{t("新的工具调用会显示在这里", "New tool calls will appear here")}</p>
        </div>
      )}
    </div>
  );
}
export function Devices({ c, t, choose }: { c: TrayConnected; t: Text; choose(peer: TrayPeer): void }): JSX.Element {
  const [search, setSearch] = useState(""),
    [online, setOnline] = useState(false);
  const peers = c.peers.filter(
    (p) =>
      (!online || p.online) &&
      `${p.name} ${p.display_name ?? ""} ${p.os ?? ""} ${p.addrs.join(" ")}`
        .toLocaleLowerCase()
        .includes(search.toLocaleLowerCase()),
  );
  return (
    <>
      <label class="search">
        <Icon name="search" />
        <input
          type="search"
          placeholder={t("搜索设备、系统或地址", "Search devices or addresses")}
          aria-label={t("搜索设备", "Search devices")}
          value={search}
          onInput={(e) => setSearch(e.currentTarget.value)}
        />
      </label>
      <div class="list-filter">
        <span>{t("已连接的设备", "Known devices")}</span>
        <label>
          <input type="checkbox" checked={online} onChange={(e) => setOnline(e.currentTarget.checked)} />
          {t("仅在线", "Online only")}
        </label>
      </div>
      <div class="card device-list">
        {peers.length ? (
          peers.map((p) => (
            <button
              type="button"
              key={p.name}
              class={`device-row ${p.online ? "" : "is-offline"}`}
              onClick={() => choose(p)}
            >
              <span class="device-icon">
                <Icon name="device" />
              </span>
              <span class="device-content">
                <span>
                  <strong title={p.display_name ?? p.name}>{p.display_name ?? p.name}</strong>
                  <Dot color={p.online ? "green" : "gray"} />
                </span>
                <span class="device-meta">
                  {p.os ?? t("未知系统", "Unknown OS")} · <code title={p.addrs.join(", ")}>{p.addrs[0] ?? "—"}</code>
                </span>
                <span class="device-route">{peerPath(p, t)}</span>
              </span>
              <Icon name="chevron" />
            </button>
          ))
        ) : (
          <div class="empty-state">
            <Icon name="search" />
            <strong>{t("没有匹配的设备", "No matching devices")}</strong>
          </div>
        )}
      </div>
    </>
  );
}
