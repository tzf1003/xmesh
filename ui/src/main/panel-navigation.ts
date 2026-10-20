import { PANEL_NAVIGATION_EVENT, type PanelTarget } from "../shared/tray";
interface Contents {
  isDestroyed(): boolean;
  send(channel: string, value: unknown): void;
}
/** Queues the latest destination until the exact main renderer has subscribed. */
export class PanelNavigation {
  private subscribed?: Contents;
  private pending?: PanelTarget;
  private dirty = false;
  constructor(
    private readonly contents: () => Contents | undefined,
    private readonly show: () => void,
  ) {}
  reset(): void {
    this.subscribed = undefined;
    this.dirty = false;
  }
  get isDirty(): boolean {
    return this.contents() === this.subscribed && !this.subscribed?.isDestroyed() && this.dirty;
  }
  request(target: PanelTarget): void {
    this.pending = target;
    this.show();
    this.flush();
  }
  ready(dirty: boolean): void {
    this.subscribed = this.contents();
    this.dirty = dirty;
    this.flush();
  }
  private flush(): void {
    const target = this.contents();
    if (!this.pending || !target || target !== this.subscribed || target.isDestroyed()) return;
    const value = this.pending;
    this.pending = undefined;
    target.send(PANEL_NAVIGATION_EVENT, value);
  }
}
