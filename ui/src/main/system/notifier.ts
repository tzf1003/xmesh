/** The slice of Electron's `Notification` instance used here. */
export interface NotificationHandle {
  show(): void;
  on(event: "click" | "close" | "failed", listener: () => void): unknown;
}

export interface NotifierDeps {
  supported: () => boolean;
  create: (opts: { title: string; body: string }) => NotificationHandle;
  now: () => number;
  onClick: () => void;
  title: string;
  /** Text for a burst of notices collapsed into one. */
  summary: (count: number) => string;
  /** Called when the OS refuses to show a notification (blocked in Focus Assist / Do Not Disturb settings, unsigned app, …). */
  onFailed?: () => void;
}

export interface Item {
  /** Stable key; the same key is not shown again within `repeatMs`. */
  key: string;
  text: string;
}

/** More than this many in one batch become a single summary. */
const BURST = 3;
/** A flapping peer should not fire a notification per flap. */
const REPEAT_MS = 60_000;
/** Hard ceiling in a sliding window, so a misbehaving kernel cannot fill the notification centre. */
const WINDOW_MS = 60_000;
const WINDOW_MAX = 6;

/**
 * Desktop notifications with a manual throttle: per-key repeat suppression,
 * burst collapsing and a global rate cap. Clicking one opens the panel.
 * Handles are kept until closed, because on macOS an unreferenced
 * notification can be collected before its click handler runs.
 */
export class Notifier {
  private readonly lastByKey = new Map<string, number>();
  private readonly shownAt: number[] = [];
  private readonly live = new Set<NotificationHandle>();

  constructor(private readonly d: NotifierDeps) {}

  push(items: Item[]): void {
    if (items.length === 0 || !this.d.supported()) return;
    const now = this.d.now();
    const fresh = items.filter((i) => {
      const last = this.lastByKey.get(i.key);
      return last === undefined || now - last >= REPEAT_MS;
    });
    for (const i of fresh) this.lastByKey.set(i.key, now);
    this.prune(now);
    if (fresh.length === 0) return;
    if (fresh.length > BURST) {
      this.show(this.d.summary(fresh.length), now);
      return;
    }
    for (const i of fresh) this.show(i.text, now);
  }

  private prune(now: number): void {
    while (this.shownAt.length > 0 && now - (this.shownAt[0] as number) >= WINDOW_MS) this.shownAt.shift();
    for (const [k, t] of this.lastByKey) if (now - t >= REPEAT_MS) this.lastByKey.delete(k);
  }

  private show(body: string, now: number): void {
    if (this.shownAt.length >= WINDOW_MAX) return;
    this.shownAt.push(now);
    const n = this.d.create({ title: this.d.title, body });
    this.live.add(n);
    const release = (): void => void this.live.delete(n);
    n.on("click", () => {
      release();
      this.d.onClick();
    });
    n.on("close", release);
    n.on("failed", () => {
      release();
      this.d.onFailed?.();
    });
    n.show();
  }
}
