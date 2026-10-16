import * as path from "node:path";
import { Menu, type MenuItemConstructorOptions, Tray, nativeImage } from "electron";
import type { Translate } from "../shared/i18n";
import { type Snapshot, type TrayIcon, deriveIcon } from "../shared/state";
import { type Action, type MenuItemSpec, buildMenu, statusLabelKey } from "./menu";

const ASSETS = path.join(__dirname, "..", "..", "..", "assets", "tray");

export function trayIconPath(icon: TrayIcon, platform: NodeJS.Platform = process.platform): string {
  return path.join(ASSETS, `tray-${icon}${platform === "darwin" ? "Template" : ""}.png`);
}

function toTemplate(items: MenuItemSpec[], run: (a: Action) => void): MenuItemConstructorOptions[] {
  return items.map((i): MenuItemConstructorOptions => {
    if (i.kind === "separator") return { type: "separator" };
    const o: MenuItemConstructorOptions = { id: i.id, label: i.label, enabled: i.enabled };
    if (i.kind === "checkbox" || i.kind === "radio") {
      o.type = i.kind;
      o.checked = i.checked ?? false;
    }
    if (i.submenu) o.submenu = toTemplate(i.submenu, run);
    const action = i.action;
    if (action) o.click = () => run(action);
    return o;
  });
}

export class TrayView {
  private readonly tray: Tray;
  private lastKey = "";
  private lastSpec: MenuItemSpec[] = [];
  private lastIcon: TrayIcon = "offline";
  private lastTooltip = "";

  constructor(
    private readonly t: Translate,
    private readonly run: (a: Action) => void,
    onClick: () => void,
  ) {
    this.tray = new Tray(nativeImage.createFromPath(trayIconPath("offline")));
    // Windows: click opens the panel, right-click the menu. macOS: click shows the menu.
    if (process.platform !== "darwin") this.tray.on("click", onClick);
  }

  update(snap: Snapshot): void {
    const icon = deriveIcon(snap.connection);
    const spec = buildMenu(snap, this.t);
    const tooltip = this.t("tray.tooltip", { status: this.t(statusLabelKey(snap)) });
    const key = JSON.stringify([icon, tooltip, spec]);
    if (key === this.lastKey) return;
    this.lastKey = key;
    this.lastSpec = spec;
    this.lastIcon = icon;
    this.lastTooltip = tooltip;
    this.tray.setImage(nativeImage.createFromPath(trayIconPath(icon)));
    this.tray.setToolTip(tooltip);
    this.tray.setContextMenu(Menu.buildFromTemplate(toTemplate(spec, this.run)));
  }

  /** For the end-to-end tests: what the tray currently shows. */
  describe(): { icon: TrayIcon; tooltip: string; menu: MenuItemSpec[] } {
    return { icon: this.lastIcon, tooltip: this.lastTooltip, menu: this.lastSpec };
  }

  destroy(): void {
    this.tray.destroy();
  }
}
