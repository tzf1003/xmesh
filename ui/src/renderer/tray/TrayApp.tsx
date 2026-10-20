import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type {
  PanelTarget,
  RecentCallView,
  TrayApi,
  TrayCommand,
  TrayConfirmation,
  TrayPage,
  TrayPeer,
  TrayReply,
  TrayState,
} from "../../shared/tray";
import { cssFamily } from "../../shared/display";
import { Icon } from "../icons";

const api = (globalThis as { xmeshTray?: TrayApi }).xmeshTray;
if (!api) throw new Error("xmesh tray preload is unavailable");
const tray = api;
import { Dot, Button, IconButton, Dialog, peerPath, Calls, Devices, type Text } from "./components";
type Command = TrayCommand extends infer C ? (C extends TrayCommand ? Omit<C, "instance" | "bootId"> : never) : never;
type Run = (command: Command, success?: string) => Promise<TrayReply | undefined>;
type Open = (page: PanelTarget["page"], peer?: string, device?: string) => void;
const pages: [TrayPage, string, string][] = [
  ["overview", "概览", "Overview"],
  ["devices", "设备", "Devices"],
  ["services", "服务", "Services"],
  ["activity", "活动", "Activity"],
];
export function TrayApp(): JSX.Element {
  const [state, setState] = useState<TrayState>(),
    [loadError, setLoadError] = useState("");
  const [page, setPage] = useState<TrayPage>("overview"),
    [settings, setSettings] = useState(false),
    [more, setMore] = useState(false),
    [instances, setInstances] = useState(false);
  const [busy, setBusy] = useState(false),
    [message, setMessage] = useState(""),
    [error, setError] = useState("");
  const [confirm, setConfirm] = useState<TrayConfirmation>(),
    [quit, setQuit] = useState(false),
    [peer, setPeer] = useState<TrayPeer>(),
    [call, setCall] = useState<RecentCallView>(),
    [failures, setFailures] = useState(false);
  const latest = useRef(state);
  latest.current = state;
  const scroll = useRef<HTMLDivElement>(null),
    positions = useRef(new Map<string, number>()),
    selectedPages = useRef(new Map<string, TrayPage>()),
    timer = useRef<ReturnType<typeof setTimeout>>();
  const t: Text = (zh, en) => (state?.locale === "en" ? en : zh);
  useEffect(() => {
    let live = true;
    const receive = (next: TrayState): void => {
      if (live) setState((old) => (!old || next.revision >= old.revision ? next : old));
    };
    const off = tray.onState(receive);
    const present = tray.onPresent((showMore) => {
      setMore(showMore);
      setSettings(false);
      setConfirm(undefined);
      setQuit(false);
      setPeer(undefined);
      setCall(undefined);
      setError("");
    });
    void tray
      .getState()
      .then(receive)
      .catch((e) => setLoadError(String(e)));
    return () => {
      live = false;
      off();
      present();
      clearTimeout(timer.current);
    };
  }, []);
  useEffect(() => {
    if (!state) return;
    setPage(selectedPages.current.get(state.instance) ?? "overview");
    setConfirm(undefined);
    setPeer(undefined);
    setCall(undefined);
    setInstances(false);
    setSettings(false);
  }, [state?.instance]);
  useEffect(() => {
    if (!state) return;
    setPeer((previous) =>
      previous && state.connection.kind === "connected"
        ? state.connection.peers.find((p) => p.name === previous.name)
        : undefined,
    );
    if (state.connection.kind !== "connected") {
      setConfirm(undefined);
      setCall(undefined);
    }
  }, [state?.revision]);
  useEffect(() => {
    if (!state) return;
    document.documentElement.lang = state.locale === "en" ? "en" : "zh-CN";
    document.documentElement.style.setProperty(
      "--font-ui",
      cssFamily(state.display.uiFont, "var(--font-sys)") ?? "var(--font-sys)",
    );
    document.documentElement.style.setProperty(
      "--mono",
      cssFamily(state.display.codeFont, "var(--mono-sys)") ?? "var(--mono-sys)",
    );
  }, [state?.locale, state?.display.uiFont, state?.display.codeFont]);
  useEffect(() => {
    if (!state || !scroll.current) return;
    const key = `${state.instance}/${page}/${settings}`;
    scroll.current.scrollTop = positions.current.get(key) ?? 0;
    const el = scroll.current;
    return () => {
      positions.current.set(key, el.scrollTop);
    };
  }, [state?.instance, page, settings]);
  useEffect(() => {
    const key = (e: KeyboardEvent): void => {
      if (e.key === "Escape" && !confirm && !quit && !peer && !call) {
        if (more || instances) {
          setMore(false);
          setInstances(false);
        } else if (settings) setSettings(false);
        else void tray.hide();
      }
      if ((e.ctrlKey || e.metaKey) && e.key === ",") {
        e.preventDefault();
        setSettings(true);
      }
    };
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, [confirm, quit, peer, call, more, instances, settings]);
  const run: Run = async (command, success) => {
    const before = latest.current;
    if (!before || busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await tray.command({
        ...command,
        instance: before.instance,
        bootId: before.bootId,
      } as TrayCommand);
      if (latest.current?.instance !== before.instance || latest.current?.bootId !== before.bootId) return result;
      if (!result.ok) setError(result.error.message);
      else if (result.data.confirmation) setConfirm(result.data.confirmation);
      else if (success) {
        setMessage(success);
        clearTimeout(timer.current);
        timer.current = setTimeout(() => setMessage(""), 2200);
      }
      return result;
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return;
    } finally {
      setBusy(false);
    }
  };
  const open: Open = (target, peerName, device) => {
    void run({
      op: "open-panel",
      page: target,
      ...(peerName && { peer: peerName }),
      ...(device && { device }),
    });
  };
  const action = (id: string, success?: string): void => {
    void run({ op: "action", id }, success);
    setMore(false);
    setInstances(false);
  };
  const enabled = (id: string): boolean =>
    !busy && !state?.busy && !!state?.operations.find((x) => x.id === id)?.enabled;
  const switchPage = (next: TrayPage): void => {
    if (state) selectedPages.current.set(state.instance, next);
    setPage(next);
    setSettings(false);
  };
  if (!state)
    return (
      <main class="tray-panel">
        <div class="empty-state" role="status">
          {loadError || "Loading xmesh…"}
        </div>
        <Button
          onClick={() => {
            setLoadError("");
            void tray
              .getState()
              .then(setState)
              .catch((e) => setLoadError(String(e)));
          }}
        >
          Retry
        </Button>
      </main>
    );
  const c = state.connection;
  const connected = c.kind === "connected";
  const color =
    state.icon === "ok"
      ? "green"
      : ["offline"].includes(state.icon)
        ? "gray"
        : state.icon === "mismatch"
          ? "red"
          : "amber";
  const operations = state.operations.filter(
    (x) =>
      x.id.startsWith("action.startKernel.") ||
      x.id.startsWith("action.restartUnresponsive.") ||
      x.id.startsWith("action.cleanupStale."),
  );
  return (
    <main
      class="tray-panel"
      data-testid="tray-panel"
      onClick={(e) => {
        if (!(e.target as Element).closest(".more-wrap")) setMore(false);
        if (!(e.target as Element).closest(".instance-picker")) setInstances(false);
      }}
    >
      <header class="tray-header">
        <div class="tray-brand">
          <img src="icon.png" alt="" />
          <strong>xmesh</strong>
        </div>
        <div class="header-actions">
          <IconButton
            name="activity"
            label={t("刷新状态", "Refresh status")}
            disabled={busy || state.refreshing}
            onClick={() => {
              void run({ op: "refresh" }, t("已请求刷新", "Refresh requested"));
            }}
          />
          <IconButton name="arrow" label={t("打开主窗口", "Open main window")} onClick={() => open("overview")} />
          <IconButton
            name="sliders"
            label={t("设置", "Settings")}
            active={settings}
            onClick={() => setSettings(!settings)}
          />
        </div>
      </header>
      <div class="node-context">
        <div class="instance-picker">
          <button
            type="button"
            disabled={busy || state.busy || state.instances.length < 2}
            aria-haspopup="menu"
            aria-expanded={instances}
            onClick={() => setInstances(!instances)}
          >
            <Icon name="device" />
            <span title={connected ? c.hostname : state.instance}>
              {connected ? c.hostname : state.instance || t("本机", "This computer")}
            </span>
            {state.instances.length > 1 && (
              <span class="down" aria-hidden="true">
                ⌄
              </span>
            )}
          </button>
          {instances && (
            <div class="dropdown" role="menu">
              {state.instances.map((i) => (
                <button
                  role="menuitemradio"
                  aria-checked={i.id === state.instance}
                  key={i.id}
                  disabled={!enabled(`instance.${i.id}`)}
                  onClick={() => action(`instance.${i.id}`)}
                >
                  {i.label}
                  {i.id === state.instance && <Icon name="shield" />}
                </button>
              ))}
            </div>
          )}
        </div>
        <span class="node-status">
          <Dot color={color} />
          {state.status}
        </span>
      </div>
      {!settings && (
        <div class="tabs" role="tablist" aria-label={t("托盘页面", "Tray pages")}>
          {pages.map(([id, zh, en], index) => (
            <button
              type="button"
              role="tab"
              id={`tab-${id}`}
              key={id}
              aria-controls="tray-content"
              aria-selected={id === page}
              tabIndex={id === page ? 0 : -1}
              onClick={() => switchPage(id)}
              onKeyDown={(e) => {
                if (["ArrowRight", "ArrowLeft", "Home", "End"].includes(e.key)) {
                  e.preventDefault();
                  const at =
                    e.key === "Home" ? 0 : e.key === "End" ? 3 : (index + (e.key === "ArrowRight" ? 1 : 3)) % 4;
                  const next = pages[at]![0];
                  switchPage(next);
                  requestAnimationFrame(() => document.getElementById(`tab-${next}`)?.focus());
                }
              }}
            >
              {t(zh, en)}
              {id === "devices" && connected && <span class="tab-count">{c.peersTotal}</span>}
            </button>
          ))}
        </div>
      )}
      <div
        class="tray-scroll"
        ref={scroll}
        role={settings ? undefined : "tabpanel"}
        id="tray-content"
        aria-labelledby={settings ? undefined : `tab-${page}`}
      >
        {error && (
          <div class="error-banner" role="alert">
            <span>{error}</span>
            <IconButton name="close" label={t("关闭错误提示", "Dismiss error")} onClick={() => setError("")} />
          </div>
        )}
        {settings ? (
          <>
            <div class="subpage-title">
              <button type="button" class="text-button" onClick={() => setSettings(false)}>
                ← {t("返回", "Back")}
              </button>
              <h2>{t("托盘设置", "Tray settings")}</h2>
              <span />
            </div>
            <div class="card settings-card">
              <div class="setting-row">
                <span>
                  <strong>{t("登录时启动", "Open at login")}</strong>
                  <small>{t("登录电脑后自动打开 xmesh", "Open xmesh when you sign in")}</small>
                </span>
                <button
                  type="button"
                  role="switch"
                  class={`switch ${state.loginItem ? "on" : ""}`}
                  aria-label={t("登录时启动", "Open at login")}
                  aria-checked={state.loginItem}
                  disabled={!enabled("settings.loginItem")}
                  onClick={() => action("settings.loginItem")}
                >
                  <span />
                </button>
              </div>
              <button type="button" class="setting-row" onClick={() => open("settings")}>
                {t("外观与语言", "Appearance and language")}
                <Icon name="chevron" />
              </button>
            </div>
            <div class="section-label">
              {t("当前节点", "Current node")} · {state.instance}
            </div>
            <div class="card settings-card">
              {[
                ["action.openConfig", "配置文件", "Configuration"],
                ["action.openLogs", "日志目录", "Log directory"],
                ["action.openAudit", "审计目录", "Audit directory"],
                ["settings.restartKernel", "重启内核", "Restart kernel"],
                ["action.stopKernel", "停止内核", "Stop kernel"],
              ].map(([id, zh, en]) => (
                <button
                  type="button"
                  key={id}
                  class={`setting-row ${id === "action.stopKernel" ? "error-text" : ""}`}
                  disabled={!enabled(id!)}
                  onClick={() => action(id!)}
                >
                  {t(zh!, en!)}
                  <Icon name="chevron" />
                </button>
              ))}
            </div>
            <p class="tip">
              {t("停止或重启内核可能中断现有任务。", "Stopping or restarting may interrupt running tasks.")}
            </p>
          </>
        ) : !connected ? (
          <>
            <div class="unavailable card">
              <Icon name={c.kind === "mismatch" ? "info" : "network"} />
              <h2>
                {c.kind === "mismatch"
                  ? t("桌面端与内核版本不兼容", "Incompatible kernel version")
                  : state.unresponsiveNodes.length
                    ? t("内核未响应", "Kernel not responding")
                    : state.stoppedNodes.length
                      ? t("内核未运行", "Kernel is stopped")
                      : state.staleNodes.length
                        ? t("需要处理残留状态", "Stale node state")
                        : t("连接你的设备网络", "Connect your devices")}
              </h2>
              <p>
                {c.kind === "mismatch"
                  ? `${t("内核", "Kernel")} ${c.kernelVersion} · API ${c.apiVersion}`
                  : t("打开主窗口可查看节点诊断与设置。", "Open the main window for node setup and diagnostics.")}
              </p>
              {operations.map((o) => (
                <Button key={o.id} disabled={!enabled(o.id)} onClick={() => action(o.id)}>
                  {o.label}
                </Button>
              ))}
              {state.protectedStaleNodes.map((node) => (
                <p key={node}>
                  {t("系统服务状态需要管理员处理", "Administrator cleanup required")}: {node}
                </p>
              ))}
              <Button
                class="primary"
                onClick={() =>
                  open(
                    c.kind === "mismatch"
                      ? "about"
                      : state.instances.length || state.stoppedNodes.length
                        ? "overview"
                        : "setup",
                  )
                }
              >
                {c.kind === "mismatch" ? t("查看版本", "View versions") : t("打开 xmesh", "Open xmesh")}
              </Button>
            </div>
          </>
        ) : (
          <>
            {(c.draining || c.restartRequired || c.warnings.some((w) => w.severity !== "info")) && (
              <div class="notice">
                <Icon name="info" />
                <span>
                  {c.draining
                    ? t("新任务已暂停，已有会话继续运行", "New tasks paused; existing sessions continue")
                    : c.restartRequired
                      ? t("有配置等待重启后生效", "Configuration is waiting for a restart")
                      : c.warnings.find((w) => w.severity !== "info")?.message}
                </span>
                <button
                  type="button"
                  onClick={() =>
                    c.draining
                      ? action("action.resume")
                      : c.restartRequired
                        ? action("settings.restartKernel")
                        : open("runtime")
                  }
                  disabled={
                    c.draining
                      ? !enabled("action.resume")
                      : c.restartRequired
                        ? !enabled("settings.restartKernel")
                        : false
                  }
                >
                  {c.draining ? t("恢复", "Resume") : c.restartRequired ? t("重启", "Restart") : t("查看", "View")}
                </button>
              </div>
            )}
            {page === "overview" ? (
              <>
                <div class="summary-line">
                  <span>
                    <Dot />
                    {c.peersOnline} {t("台设备在线", "devices online")} · {c.network}
                  </span>
                  <button type="button" class="text-button" onClick={() => open("overview")}>
                    {t("查看拓扑", "Topology")}
                    <Icon name="chevron" />
                  </button>
                </div>
                <div class="card topology">
                  <div class="topology-main">
                    <div class="local-node">
                      <span class="node-pill">
                        <Icon name="device" />
                        {t("本机", "Local")}
                      </span>
                      <span class="node-subtitle" title={state.instance}>
                        {state.instance}
                      </span>
                    </div>
                    <span class="edge" />
                    <div class="hub">
                      <img src="icon.png" alt="xmesh" />
                    </div>
                    <div class="peer-nodes">
                      {c.peers
                        .filter((p) => p.online)
                        .slice(0, 3)
                        .map((p) => (
                          <button type="button" key={p.name} class="node-pill" onClick={() => setPeer(p)}>
                            <Dot />
                            <span title={p.display_name ?? p.name}>{p.display_name ?? p.name}</span>
                          </button>
                        ))}
                      {!c.peersOnline && <span class="node-pill">{t("等待连接设备", "No connected devices")}</span>}
                    </div>
                  </div>
                  <div class="topology-caption">
                    <span>
                      {c.peers.filter((p) => p.online).every((p) => p.trust_state === "paired") && c.peersOnline > 0
                        ? t("设备身份已验证", "Device identities verified")
                        : t("设备连接概览", "Device connections")}
                    </span>
                    <button type="button" class="text-button" onClick={() => switchPage("devices")}>
                      {c.peersOnline > 3
                        ? t(`另 ${c.peersOnline - 3} 台`, `${c.peersOnline - 3} more`)
                        : t("全部设备", "All devices")}
                    </button>
                  </div>
                </div>
                <div class="card resource-strip">
                  <button type="button" onClick={() => open("sessions")}>
                    <Icon name="terminal" />
                    <span>{t("终端会话", "Terminals")}</span>
                    <strong>
                      {c.pty.used}
                      <small> / {c.pty.max}</small>
                    </strong>
                  </button>
                  <span class="vertical-divider" />
                  <button type="button" onClick={() => open("runtime")}>
                    <Icon name="activity" />
                    <span>{t("共享执行", "Shared exec")}</span>
                    <strong>
                      {c.exec.used}
                      <small> / {c.exec.max}</small>
                    </strong>
                  </button>
                </div>
                <div class="quick-actions">
                  <Button
                    disabled={!enabled("action.copyGateway")}
                    onClick={() =>
                      action(
                        "action.copyGateway",
                        t("连接信息已复制，包含访问凭据", "Connection details copied, including credentials"),
                      )
                    }
                  >
                    <Icon name="copy" />
                    {t("复制连接信息", "Copy connection")}
                  </Button>
                  <Button
                    disabled={!enabled(c.draining ? "action.resume" : "action.drain")}
                    onClick={() => action(c.draining ? "action.resume" : "action.drain")}
                  >
                    {c.draining ? t("恢复接入", "Resume") : t("暂停接入", "Pause new tasks")}
                  </Button>
                </div>
                <div class="section-label">
                  <span>{t("最近活动", "Recent activity")}</span>
                  <button type="button" class="text-button" onClick={() => switchPage("activity")}>
                    {t("全部记录", "View all")}
                    <Icon name="chevron" />
                  </button>
                </div>
                <Calls calls={state.activity.slice(0, 3)} t={t} choose={setCall} />
              </>
            ) : page === "devices" ? (
              <>
                <div class="summary-line">
                  <span>
                    <Dot />
                    {c.peersOnline} / {c.peersTotal} {t("台远端设备", "remote devices")}
                  </span>
                  <button type="button" class="text-button" onClick={() => open("devices")}>
                    {t("管理设备", "Manage devices")}
                    <Icon name="chevron" />
                  </button>
                </div>
                <Devices key={state.instance} c={c} t={t} choose={setPeer} />
              </>
            ) : page === "services" ? (
              <>
                <div class="summary-line">
                  <span>
                    <Dot color={color} />
                    {state.status}
                  </span>
                  <button type="button" class="text-button" onClick={() => open("runtime")}>
                    {t("管理服务", "Manage services")}
                    <Icon name="chevron" />
                  </button>
                </div>
                <div class="card gateway-card">
                  <div class="service-header">
                    <strong>MCP {t("网关", "Gateway")}</strong>
                    <span class="status-label">
                      <Dot color={c.gateway ? "green" : "gray"} />
                      {c.gateway ? t("运行中", "Running") : t("未启用", "Disabled")}
                    </span>
                  </div>
                  {c.gateway ? (
                    <>
                      <code class="endpoint" title={c.gateway.url}>
                        {c.gateway.url}
                      </code>
                      <div class="connect-buttons">
                        {[
                          ["action.copyGateway", "连接信息", "Connection"],
                          ["action.copyClaude", "Claude Code", "Claude Code"],
                          ["action.copyCodex", "Codex", "Codex"],
                        ].map(([id, zh, en]) => (
                          <Button
                            key={id}
                            disabled={!enabled(id!)}
                            onClick={() =>
                              action(
                                id!,
                                t(
                                  "连接配置已复制，包含访问凭据",
                                  "Connection configuration copied, including credentials",
                                ),
                              )
                            }
                          >
                            {t(zh!, en!)}
                          </Button>
                        ))}
                      </div>
                      <p class="tip">
                        {t("复制的连接配置包含网关访问凭据。", "Copied connection configuration includes credentials.")}
                      </p>
                    </>
                  ) : (
                    <p class="tip">
                      {t(
                        "在主窗口中启用网关角色后，可生成连接配置。",
                        "Enable the gateway role in the main window to create connection configuration.",
                      )}
                    </p>
                  )}
                </div>
                <div class="section-label">
                  <span>MCP {t("后端", "Backends")}</span>
                  <span>{c.backends.length}</span>
                </div>
                <div class="card backend-list">
                  {c.backends.length ? (
                    c.backends.map((b) => (
                      <button type="button" key={b.name} class="backend-row" onClick={() => open("runtime")}>
                        <Icon name="terminal" />
                        <strong title={b.name}>{b.name}</strong>
                        <span class="tag">{b.policy}</span>
                        <span class="backend-status">
                          <Dot color={b.state === "running" ? "green" : b.state === "crashed" ? "red" : "gray"} />
                          {b.state}
                        </span>
                        <Icon name="chevron" />
                      </button>
                    ))
                  ) : (
                    <div class="empty-state">{t("尚未配置 MCP 后端", "No MCP backends configured")}</div>
                  )}
                </div>
                {c.ci && (
                  <>
                    <div class="section-label">GitHub Actions</div>
                    <button type="button" class="card runner-summary" onClick={() => open("ci")}>
                      <Icon name="runner" />
                      <span>
                        {c.ci.busy} {t("个执行中", "busy")} · {c.ci.idle} {t("个空闲", "idle")}
                        {c.ci.broken > 0 && ` · ${c.ci.broken} ${t("个异常", "need attention")}`}
                      </span>
                      <Icon name="chevron" />
                    </button>
                  </>
                )}
              </>
            ) : (
              <>
                <div class="summary-line">
                  <span>{t("最近调用 · 当前内核启动周期", "Recent calls · current kernel boot")}</span>
                  <button type="button" class="text-button" onClick={() => open("audit")}>
                    {t("查看审计", "Audit log")}
                    <Icon name="chevron" />
                  </button>
                </div>
                <div class="activity-filter">
                  <button type="button" aria-pressed={!failures} onClick={() => setFailures(false)}>
                    {t("全部", "All")}
                  </button>
                  <button type="button" aria-pressed={failures} onClick={() => setFailures(true)}>
                    {t("失败", "Failed")} {state.activity.filter((x) => x.status === "error").length}
                  </button>
                  <span class="live-label">
                    <Dot />
                    {t("实时更新", "Live")}
                  </span>
                </div>
                <Calls calls={state.activity.filter((x) => !failures || x.status === "error")} t={t} choose={setCall} />
                <p class="tip">
                  {t(
                    "仅显示调用元数据。完整历史请查看审计日志。",
                    "Call metadata only. Full history is available in the audit log.",
                  )}
                </p>
              </>
            )}
          </>
        )}
      </div>
      {message && (
        <div class="toast" role="status">
          {message}
        </div>
      )}
      <footer class="tray-footer">
        <div class="more-wrap">
          <IconButton name="info" label={t("更多操作", "More actions")} active={more} onClick={() => setMore(!more)} />
          {more && (
            <div class="dropdown footer-dropdown">
              <button type="button" onClick={() => open("about")}>
                {state.updateVersion
                  ? `${t("更新至", "Update to")} ${state.updateVersion}`
                  : t("检查更新", "Check for updates")}
              </button>
              <button type="button" disabled={!enabled("settings.help")} onClick={() => action("settings.help")}>
                {t("帮助", "Help")}
              </button>
              <button
                type="button"
                onClick={() => {
                  setMore(false);
                  setSettings(true);
                }}
              >
                {t("托盘设置", "Tray settings")}
              </button>
            </div>
          )}
        </div>
        <span class="footer-spacer" />
        <button type="button" onClick={() => open("overview")}>
          {t("打开 xmesh", "Open xmesh")}
        </button>
        <button type="button" disabled={!enabled("action.quit")} onClick={() => setQuit(true)}>
          {t("退出", "Quit")}
        </button>
      </footer>
      {confirm && (
        <Dialog
          title={
            confirm.action === "stop-kernel"
              ? t("停止此节点内核？", "Stop this kernel?")
              : t("重启此节点内核？", "Restart this kernel?")
          }
          close={() => setConfirm(undefined)}
        >
          <p>
            <strong>{confirm.hostname}</strong> · {confirm.instance}
          </p>
          <p>
            {confirm.impactKnown === false ? t("内核未响应，无法确认任务数量。重启可能中断终端、命令和 CI 任务。", "The kernel is not responding. Restarting may interrupt terminal sessions, commands and CI jobs.") : <>{t("可能中断以下任务", "The following work may be interrupted")}: {confirm.pty} PTY · {confirm.exec} exec · {confirm.ci} CI</>}
          </p>
          <div class="modal-actions">
            <Button onClick={() => setConfirm(undefined)}>{t("取消", "Cancel")}</Button>
            <Button
              class="danger"
              disabled={busy}
              testid="tray-confirm"
              onClick={() => {
                const token = confirm.token;
                setConfirm(undefined);
                void run({ op: "confirm", token });
              }}
            >
              {confirm.action === "stop-kernel" ? t("停止内核", "Stop kernel") : t("重启", "Restart")}
            </Button>
          </div>
        </Dialog>
      )}
      {quit && (
        <Dialog title={t("退出 xmesh 界面？", "Quit xmesh UI?")} close={() => setQuit(false)}>
          <p>
            {t(
              "后台内核会继续运行，现有设备连接与任务会保留。",
              "The background kernel, device connections and tasks will continue running.",
            )}
          </p>
          <div class="modal-actions">
            <Button onClick={() => setQuit(false)}>{t("取消", "Cancel")}</Button>
            <Button
              class="primary"
              onClick={() => {
                setQuit(false);
                action("action.quit");
              }}
            >
              {t("退出界面", "Quit UI")}
            </Button>
          </div>
        </Dialog>
      )}
      {peer && (
        <Dialog title={peer.display_name ?? peer.name} close={() => setPeer(undefined)}>
          <dl>
            <dt>{t("系统", "System")}</dt>
            <dd>{peer.os ?? "—"}</dd>
            <dt>{t("连接", "Connection")}</dt>
            <dd>{peerPath(peer, t)}</dd>
            <dt>{t("地址", "Addresses")}</dt>
            <dd>
              <code>{peer.addrs.join(", ") || "—"}</code>
            </dd>
          </dl>
          <div class="modal-actions">
            <Button
              disabled={busy || peer.addrs.length === 0}
              onClick={() => {
                void run({ op: "copy-peer", peer: peer.name }, t("设备地址已复制", "Device address copied"));
              }}
            >
              {t("复制地址", "Copy addresses")}
            </Button>
            <Button disabled={!peer.online} onClick={() => open("terminal", undefined, peer.name)}>
              {t("打开终端", "Open terminal")}
            </Button>
          </div>
        </Dialog>
      )}
      {call && (
        <Dialog title={t("调用详情", "Call details")} close={() => setCall(undefined)}>
          <dl>
            <dt>{t("工具", "Tool")}</dt>
            <dd>
              <code>{call.tool}</code>
            </dd>
            <dt>{t("设备", "Device")}</dt>
            <dd>{call.device}</dd>
            <dt>{t("调用方", "Caller")}</dt>
            <dd>{call.oauth_client ?? call.caller}</dd>
            <dt>{t("结果", "Result")}</dt>
            <dd>
              {call.status}
              {call.error ? ` · ${call.error}` : ""}
            </dd>
            <dt>{t("耗时", "Duration")}</dt>
            <dd>{call.duration_ms} ms</dd>
            <dt>{t("时间", "Time")}</dt>
            <dd>{new Date(call.time).toLocaleString()}</dd>
          </dl>
          <Button onClick={() => open("audit")}>{t("在审计中查看", "Open audit log")}</Button>
        </Dialog>
      )}
    </main>
  );
}
