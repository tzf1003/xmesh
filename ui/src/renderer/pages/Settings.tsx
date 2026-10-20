import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Dialog, PageHead } from "../components";
import { Icon } from "../icons";
import { useCopy } from "../design-copy";
import { DEFAULT_APP_PREFERENCES } from "../../shared/preferences";
import { isFontName, ZOOM_STEPS } from "../../shared/display";
import { About } from "./About";

function SettingRow({ title, sub, children }: { title: string; sub: string; children: ComponentChildren }): JSX.Element {
  return <div class="setting-row"><div><strong>{title}</strong><p>{sub}</p></div><div class="setting-control">{children}</div></div>;
}
export function Settings({ ctx }: { ctx: PageContext }): JSX.Element {
  const c = useCopy();
  const [tab, setTab] = useState(ctx.focus?.settingsTab ?? "general");
  const [diagnostics, setDiagnostics] = useState(false);
  const [font, setFont] = useState(ctx.snapshot.settings.display.uiFont);
  const [code, setCode] = useState(ctx.snapshot.settings.display.codeFont);
  useEffect(() => { if (ctx.focus?.settingsTab) setTab(ctx.focus.settingsTab); }, [ctx.focus?.settingsTab]);
  const prefs = { ...DEFAULT_APP_PREFERENCES, ...ctx.snapshot.settings.preferences, locale: ctx.snapshot.settings.locale };
  const display = ctx.snapshot.settings.display;
  const run = async (p: ReturnType<ReturnType<typeof getApi>["setLoginItem"]>): Promise<void> => { const r = await p; if (!r.ok) ctx.toast(r.error.message, true); };
  const setDisplay = (patch: Partial<typeof display>): void => { void run(getApi().setDisplay({ ...display, ...patch })); };
  const setPrefs = (patch: Partial<typeof prefs>): void => { void run(getApi().setAppPreferences({ ...prefs, ...patch })); };
  return <div class="page control-page settings-page" data-testid="page-settings">
    <PageHead title={c("设置", "Settings")} sub={c("让 xmesh 更贴合你的工作习惯。", "Make xmesh fit the way you work.")} />
    <div class="settings-layout"><nav class="settings-local-nav" aria-label={c("设置分类", "Settings sections")}>
      {([ ["general", c("通用", "General"), "sliders"], ["diagnostics", c("存储与诊断", "Storage & diagnostics"), "folder"], ["about", c("关于与更新", "About & updates"), "info"] ] as const).map(([id,label,icon]) => <button type="button" aria-current={tab === id ? "page" : undefined} data-testid={`settings-tab-${id}`} onClick={() => { setTab(id); ctx.go("settings", { settingsTab: id }); }}><Icon name={icon}/><span>{label}</span></button>)}
    </nav><div class="settings-main">
      {tab === "general" && <>
        <section class="settings-card"><h2>{c("外观与语言", "Appearance & language")}</h2>
          <SettingRow title={c("外观", "Appearance")} sub={c("选择适合当前环境的配色。", "Choose the appearance for your workspace.")}><div class="theme-choices">{([ ["system",c("跟随系统", "System")], ["dark", c("深色", "Dark")], ["light", c("浅色", "Light")] ] as const).map(([theme,label]) => <button type="button" data-testid={`theme-${theme}`} aria-pressed={(display.theme ?? "system") === theme} onClick={() => setDisplay({ theme })}><span class={`theme-demo ${theme}`}/><span>{label}</span></button>)}</div></SettingRow>
          <SettingRow title={c("界面语言", "Language")} sub={c("应用到页面与菜单。", "Used by pages and menus.")}><select aria-label={c("界面语言", "Language")} value={prefs.locale} onChange={e => setPrefs({ locale: e.currentTarget.value as "zh" | "en" })}><option value="zh">简体中文</option><option value="en">English</option></select></SettingRow>
          <SettingRow title={c("界面字体", "Interface font")} sub={c("填写本机已安装的字体，留空使用系统默认。", "Use an installed typeface, or leave empty for the system default.")}><input aria-label={c("界面字体", "Interface font")} value={font} onInput={e => setFont(e.currentTarget.value)} onBlur={() => { if (!font || isFontName(font)) setDisplay({ uiFont: font }); }} data-testid="settings-ui-font" placeholder={c("系统默认", "System default")}/></SettingRow>
          <SettingRow title={c("代码字体", "Code font")} sub={c("终端、标识与路径使用等宽字体。", "Typeface for terminals, identifiers and paths.")}><input aria-label={c("代码字体", "Code font")} value={code} onInput={e => setCode(e.currentTarget.value)} onBlur={() => { if (!code || isFontName(code)) setDisplay({ codeFont: code }); }} data-testid="settings-code-font" placeholder="SF Mono"/></SettingRow>
          <SettingRow title={c("界面缩放", "Zoom")} sub={c("调整界面的文字与控件大小。", "Adjust text and control sizes.")}><select value={display.zoom} aria-label={c("界面缩放", "Zoom")} onChange={e => setDisplay({ zoom: Number(e.currentTarget.value) })}>{ZOOM_STEPS.map(z => <option value={z}>{Math.round(z * 100)}%</option>)}</select></SettingRow>
        </section>
        <section class="settings-card"><h2>{c("启动与窗口", "Startup & windows")}</h2>
          <SettingRow title={c("登录后启动 xmesh", "Start xmesh at login")} sub={c("登录系统后自动启动应用。", "Start the app when you sign in.")}><input type="checkbox" role="switch" aria-label={c("登录后启动 xmesh", "Start xmesh at login")} checked={ctx.snapshot.settings.loginItem} onChange={e => void run(getApi().setLoginItem(e.currentTarget.checked))}/></SettingRow>
          <SettingRow title={c("关闭窗口后保留在菜单栏", "Keep running in the tray")} sub={c("关闭面板后可从菜单栏重新打开。", "Reopen the panel from the tray after closing it.")}><input type="checkbox" role="switch" aria-label={c("关闭窗口后保留在菜单栏", "Keep running in the tray")} checked={prefs.closeToTray} onChange={e => setPrefs({ closeToTray: e.currentTarget.checked })}/></SettingRow>
        </section><p class="hint settings-context"><Icon name="info"/>{c("Agent、Gateway 与 Ingress 的设置可从顶部角色菜单打开。CI 和 Tailscale 在各自模块中管理。", "Open role settings from the top bar. CI and Tailscale settings live in their modules.")}</p>
      </>}
      {tab === "diagnostics" && <>
        <section class="settings-card"><h2>{c("本机数据", "Local data")}</h2>
          <SettingRow title={c("设备名称", "Device name")} sub={c("设备名称与联网方式在本机设备设置中调整。", "Manage the node name and networking in device settings.")}><button type="button" class="btn" onClick={() => ctx.openSettings("node")}>{c("本机设置", "Device settings")}</button></SettingRow>
          {([ ["config",c("配置位置", "Configuration location")], ["logs",c("应用日志", "Application logs")], ["audit",c("审计记录", "Audit records")] ] as const).map(([kind,title]) => <SettingRow title={title} sub={c("打开当前节点的实际目录。", "Open the current node's directory.")}><button type="button" class="btn" onClick={() => void run(getApi().openPath(kind))}>{c("打开位置", "Open folder")}</button></SettingRow>)}
        </section><section class="settings-card"><h2>{c("诊断与恢复", "Diagnostics & recovery")}</h2><SettingRow title={c("诊断摘要", "Diagnostic summary")} sub={c("查看应用版本、角色状态和连接概况。", "Inspect role and connection state.")}><button type="button" class="btn" onClick={() => setDiagnostics(true)}>{c("查看摘要", "View summary")}</button></SettingRow><SettingRow title={c("配置恢复", "Configuration recovery")} sub={c("配置文件无法读取时，用源码视图定位并修复错误。", "Recover an invalid configuration with the source editor.")}><button type="button" class="text-button" onClick={() => ctx.go("config", { configView: "source" })}>{c("修复配置", "Repair configuration")}</button></SettingRow></section>
      </>}
      {tab === "about" && <About ctx={ctx} />}
      {tab !== "about" && <details class="maintenance-tools" data-testid="settings-maintenance"><summary>{c("维护工具", "Maintenance tools")}<Icon name="chevron"/></summary><p class="hint">{c("需要调整初始设备或接入方式时，可重新运行设置向导。", "Run setup again to configure a device or client.")}</p><button type="button" class="text-button" data-testid="settings-open-setup" onClick={() => ctx.go("setup")}>{c("重新运行设置向导", "Run setup again")}<Icon name="arrow"/></button></details>}
    </div></div>
    {diagnostics && <Dialog title={c("诊断摘要", "Diagnostics")} onClose={() => setDiagnostics(false)}><dl class="overview-facts"><dt>{c("连接状态", "Connection")}</dt><dd>{ctx.snapshot.connection.kind}</dd><dt>{c("角色", "Roles")}</dt><dd>{ctx.snapshot.connection.kind === "connected" ? ctx.snapshot.connection.status.roles.join(", ") : "—"}</dd><dt>{c("当前节点", "Current node")}</dt><dd>{ctx.snapshot.selectedInstance || "—"}</dd><dt>{c("应用版本", "App version")}</dt><dd>{ctx.snapshot.update.current}</dd></dl><button type="button" class="btn" onClick={() => setDiagnostics(false)}>{c("关闭", "Close")}</button></Dialog>}
  </div>;
}
