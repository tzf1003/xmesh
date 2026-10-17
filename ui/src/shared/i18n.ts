// String table. Chinese is the default; English is reserved (docs/design/12 "面板").
// Keys are the single source of user-visible text in the main process.

const zh = {
  "tray.tooltip": "xmesh：{status}",
  "status.offline": "内核未运行",
  "status.mismatch": "内核版本不匹配",
  "status.ok": "正常",
  "status.degraded": "降级",
  "status.draining": "排空中",
  "status.restart": "需要重启",
  "menu.node": "节点：{name}",
  "menu.state": "状态：{status}",
  "menu.network": "网络模式：{mode}",
  "menu.peers": "在线对端：{count}",
  "menu.pty": "终端 {used}/{max}",
  "menu.exec": "执行 {used}/{max}",
  "menu.openPanel": "打开面板",
  "menu.drain": "暂停接收新调用",
  "menu.resume": "恢复接收新调用",
  "menu.copyGateway": "复制本机 Gateway 地址",
  "menu.copyClaude": "复制 Claude Code 连接命令",
  "menu.copyCodex": "复制 Codex 连接配置",
  "menu.copyFailed": "复制失败：读不到本机 Gateway 的令牌",
  "menu.openConfig": "打开配置文件",
  "menu.openLogs": "打开日志目录",
  "menu.openAudit": "打开审计目录",
  "menu.settings": "设置",
  "menu.loginItem": "登录时启动 UI",
  "menu.restartKernel": "重启内核",
  "menu.instances": "切换节点",
  "menu.quit": "退出托盘",
  "menu.stopKernel": "停止内核…",
  "menu.upgrade": "查看升级说明",
  "menu.mismatchHint": "内核 {kernel}，控制接口 {api}，请升级",
  "menu.offlineHint": "未找到运行中的 xmesh 内核",
  "dialog.stopKernel.title": "停止内核",
  "dialog.stopKernel.detail": "这会终止本机所有终端（PTY）和进行中的命令。",
  "dialog.stopKernel.confirm": "停止内核",
  "dialog.cancel": "取消",
  "dialog.cliFailed": "命令执行失败：{command}",
  "notice.peerOnline": "设备 {name} 已上线",
  "notice.peerOffline": "设备 {name} 已离线",
  "notice.degraded": "xmesh 状态降级，打开菜单查看",
  "notice.restartRequired": "配置变更需要重启内核",
  "notice.quotaExhausted": "{what}配额已用尽",
  "notice.quota.pty": "终端",
  "notice.quota.exec": "执行",
  "notice.title": "xmesh",
  "notice.burst": "xmesh 有 {count} 条状态变化，打开菜单查看",
  "hint.trayOverflow": "xmesh 在通知区域运行。看不到图标时，点任务栏的 ^，把 xmesh 拖到任务栏上固定。",
  "hint.loginItem.requiresApproval": "请在「系统设置 > 通用 > 登录项」中允许 xmesh。",
  "hint.loginItem.disabledByUser": "已在任务管理器的「启动应用」中关闭，请在那里重新启用。",
  "hint.loginItem.unavailable": "只有安装后的 xmesh 才能设置登录时启动。",
} as const;

export type MessageKey = keyof typeof zh;

const en: Record<MessageKey, string> = {
  "tray.tooltip": "xmesh: {status}",
  "status.offline": "Kernel not running",
  "status.mismatch": "Kernel version mismatch",
  "status.ok": "Healthy",
  "status.degraded": "Degraded",
  "status.draining": "Draining",
  "status.restart": "Restart required",
  "menu.node": "Node: {name}",
  "menu.state": "Status: {status}",
  "menu.network": "Network mode: {mode}",
  "menu.peers": "Peers online: {count}",
  "menu.pty": "Terminals {used}/{max}",
  "menu.exec": "Exec {used}/{max}",
  "menu.openPanel": "Open panel",
  "menu.drain": "Pause new calls",
  "menu.resume": "Resume new calls",
  "menu.copyGateway": "Copy local Gateway address",
  "menu.copyClaude": "Copy Claude Code connect command",
  "menu.copyCodex": "Copy Codex connect config",
  "menu.copyFailed": "Copy failed: the local Gateway token could not be read",
  "menu.openConfig": "Open config file",
  "menu.openLogs": "Open logs folder",
  "menu.openAudit": "Open audit folder",
  "menu.settings": "Settings",
  "menu.loginItem": "Start UI at login",
  "menu.restartKernel": "Restart kernel",
  "menu.instances": "Switch node",
  "menu.quit": "Quit tray",
  "menu.stopKernel": "Stop kernel…",
  "menu.upgrade": "Upgrade instructions",
  "menu.mismatchHint": "Kernel {kernel}, control API {api}: please upgrade",
  "menu.offlineHint": "No running xmesh kernel found",
  "dialog.stopKernel.title": "Stop kernel",
  "dialog.stopKernel.detail": "This ends every terminal (PTY) and running command on this machine.",
  "dialog.stopKernel.confirm": "Stop kernel",
  "dialog.cancel": "Cancel",
  "dialog.cliFailed": "Command failed: {command}",
  "notice.peerOnline": "Device {name} is online",
  "notice.peerOffline": "Device {name} went offline",
  "notice.degraded": "xmesh is degraded, open the menu for details",
  "notice.restartRequired": "A config change needs a kernel restart",
  "notice.quotaExhausted": "{what} quota exhausted",
  "notice.quota.pty": "Terminal",
  "notice.quota.exec": "Exec",
  "notice.title": "xmesh",
  "notice.burst": "xmesh has {count} status changes, open the menu for details",
  "hint.trayOverflow": "xmesh runs in the notification area. If the icon is hidden, click ^ on the taskbar and drag xmesh onto the taskbar to pin it.",
  "hint.loginItem.requiresApproval": "Allow xmesh in System Settings > General > Login Items.",
  "hint.loginItem.disabledByUser": "It is turned off in Task Manager > Startup apps; turn it back on there.",
  "hint.loginItem.unavailable": "Starting at login only works for the installed xmesh.",
};

export type Locale = "zh" | "en";

const tables: Record<Locale, Record<MessageKey, string>> = { zh, en };

export function resolveLocale(env: string | undefined): Locale {
  return env !== undefined && env.toLowerCase().startsWith("en") ? "en" : "zh";
}

export type Translate = (key: MessageKey, params?: Record<string, string | number>) => string;

export function translator(locale: Locale): Translate {
  const table = tables[locale];
  return (key, params) => {
    let s = table[key];
    if (params) {
      for (const [k, v] of Object.entries(params)) s = s.split(`{${k}}`).join(String(v));
    }
    return s;
  };
}

export const MESSAGE_KEYS = Object.keys(zh) as MessageKey[];
export const TABLES = tables;
