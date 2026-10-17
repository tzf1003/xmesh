import { EventEmitter } from "node:events";
import {
  type CallEventData,
  type Capability,
  ControlRequestError,
  type ControlEvent,
  type DrainMode,
  VersionMismatchError,
  hasCapability,
  isSupportedApiVersion,
} from "../shared/control";
import type { Locale } from "../shared/i18n";
import { type Connection, EMPTY_SNAPSHOT, type InstanceInfo, type Snapshot } from "../shared/state";
import { ControlClient, type EventStream } from "./client";
import { type Instance, stoppedNodes } from "./discovery";

export interface ControllerOptions {
  discover: () => Instance[];
  /** Node directories that have a config file, running or not (see discoverConfigured). */
  configured?: () => string[];
  getLoginItem: () => boolean;
  /** Tray and panel language; default Chinese. */
  locale?: Locale;
  setLoginItem: (enabled: boolean) => void;
  /** How often to look for a kernel while none is connected, and for new instances while connected. */
  pollMs?: number;
  /** First pause before reconnecting after an event stream ended; doubles while reconnects keep failing. */
  reconnectMs?: number;
  /** Longest reconnect pause. */
  maxBackoffMs?: number;
  /** Quiet period that coalesces a burst of events into one status refresh. */
  refreshDelayMs?: number;
  /** Status poll interval for kernels that do not advertise the `events` capability. */
  statusPollMs?: number;
  requestTimeoutMs?: number;
  streamIdleMs?: number;
}

export interface ControllerEvents {
  change: [snapshot: Snapshot];
  /** The newest `call` events, oldest first. Metadata only (CTR-CTL-04). */
  calls: [calls: CallEventData[]];
}

/** How many call events are kept for the panel's recent-activity list. */
const RECENT_CALLS = 100;

/** A connection that lasted at least this long counts as healthy and resets the backoff. */
const HEALTHY_MS = 3000;

/**
 * Owns the one connection to the kernel: finds the instance, reads status and
 * peers, follows the event stream, and turns it all into a Snapshot. The tray
 * and the panel are both views of that snapshot. When the kernel goes away it
 * keeps looking for it; when it comes back it reconnects and resubscribes.
 *
 * Reconnect protocol (CTR-CTL-04): the client keeps the last event id and
 * sends it as Last-Event-ID. The kernel replays what was missed or sends
 * `reset`; either way this controller re-reads status and peers, so the
 * snapshot never depends on having seen every event.
 */
export class ShellController extends EventEmitter<ControllerEvents> {
  private snap: Snapshot;
  private stopped = true;
  private explicit = "";
  private labels = new Map<string, string>();
  private found: Instance[] = [];
  private clients = new Map<string, { client: ControlClient; endpoint: string; bootId: string }>();
  private current: { id: string; client: ControlClient } | undefined;
  private stream: EventStream | undefined;
  private wake: (() => void) | undefined;
  private refreshTimer: NodeJS.Timeout | undefined;
  private refreshing = false;
  private refreshDirty = false;
  private loop: Promise<void> = Promise.resolve();
  private recent: CallEventData[] = [];

  constructor(private readonly opts: ControllerOptions) {
    super();
    this.snap = { ...EMPTY_SNAPSHOT, settings: { loginItem: opts.getLoginItem(), locale: opts.locale ?? "zh" } };
  }

  get snapshot(): Snapshot {
    return this.snap;
  }

  start(): void {
    if (!this.stopped) return;
    this.stopped = false;
    this.loop = this.run();
  }

  async stop(): Promise<void> {
    this.stopped = true;
    clearTimeout(this.refreshTimer);
    this.stream?.close();
    this.wake?.();
    await this.loop;
  }

  /** Pins the panel and tray to one node instance; an empty id goes back to automatic choice. */
  selectInstance(id: string): void {
    if (id === this.explicit) return;
    this.explicit = id;
    if (this.recent.length > 0) {
      this.recent = [];
      this.emit("calls", []);
    }
    this.stream?.close();
    this.wake?.();
  }

  /** Re-check the kernel now, e.g. after the machine woke from sleep or the session was unlocked. */
  nudge(): void {
    this.stream?.close();
    this.wake?.();
  }

  setLoginItem(enabled: boolean): void {
    this.opts.setLoginItem(enabled);
    this.update({ settings: { ...this.snap.settings, loginItem: this.opts.getLoginItem() } });
  }

  get recentCalls(): CallEventData[] {
    return this.recent.slice();
  }

  /** `keep` (default): refuse new calls, leave running work alone. `kill` also ends every PTY and lease. */
  async drain(mode: DrainMode = "keep"): Promise<void> {
    const c = this.requireCapable("drain");
    await c.drain(mode);
    await this.refresh(c);
  }

  async resume(): Promise<void> {
    const c = this.requireCapable("drain");
    await c.resume();
    await this.refresh(c);
  }

  /** The client of the instance the panel is showing, if connected and the kernel declares `capability`. */
  requireCapable(capability: Capability): ControlClient {
    const conn = this.snap.connection;
    if (!this.current || conn.kind !== "connected") {
      throw new ControlRequestError("unavailable", "xmesh kernel is not connected");
    }
    if (!hasCapability(conn.status.control.capabilities, capability)) {
      throw new ControlRequestError("not_implemented", `this kernel does not support ${capability}`);
    }
    return this.current.client;
  }

  private async run(): Promise<void> {
    const pollMs = this.opts.pollMs ?? 2000;
    const base = this.opts.reconnectMs ?? 500;
    const max = this.opts.maxBackoffMs ?? 10_000;
    let delay = base;
    while (!this.stopped) {
      const began = Date.now();
      const wasConnected = await this.connectOnce();
      if (this.stopped) break;
      if (!wasConnected) {
        delay = base;
        await this.sleep(pollMs);
      } else if (Date.now() - began >= HEALTHY_MS) {
        delay = base;
        await this.sleep(delay);
      } else {
        // Connected and dropped straight away: a kernel shedding load or restarting. Back off.
        await this.sleep(delay);
        delay = Math.min(delay * 2, max);
      }
    }
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      const t = setTimeout(done, ms);
      this.wake = done;
      function done(): void {
        clearTimeout(t);
        resolve();
      }
    });
  }

  /** One attempt to find a kernel, connect, and follow it until the stream ends. */
  private async connectOnce(): Promise<boolean> {
    const found = this.opts.discover();
    this.updateInstances(found);
    const candidates = this.explicit ? found.filter((i) => i.id === this.explicit) : found;
    let mismatch: Connection | undefined;
    const unresponsive: string[] = [];
    for (const inst of candidates) {
      if (!isSupportedApiVersion(inst.manifest.api_version)) {
        mismatch = {
          kind: "mismatch",
          kernelVersion: inst.manifest.kernel_version,
          apiVersion: inst.manifest.api_version,
        };
        continue;
      }
      const client = this.clientFor(inst);
      this.current = { id: inst.id, client };
      // Subscribe before reading, so a change between the two cannot be missed.
      const stream = client.openEvents((e) => this.onEvent(client, e));
      try {
        await this.refresh(client, inst.id);
      } catch (e) {
        stream.close();
        this.current = undefined;
        if (e instanceof VersionMismatchError) {
          mismatch = { kind: "mismatch", kernelVersion: inst.manifest.kernel_version, apiVersion: e.apiVersion };
        } else if (!(e instanceof ControlRequestError)) {
          // No HTTP reply at all (timeout, refused connection): the manifest is there but nothing answers.
          unresponsive.push(inst.id);
        }
        continue;
      }
      if (this.stopped) {
        stream.close();
        return true;
      }
      const conn = this.snap.connection;
      const caps = conn.kind === "connected" ? conn.status.control.capabilities : [];
      // A second node may start while this one is being followed.
      const watcher = setInterval(() => this.updateInstances(this.opts.discover()), this.opts.pollMs ?? 2000);
      if (hasCapability(caps, "events")) {
        this.stream = stream;
        await stream.done;
      } else {
        // Older or partial kernel: no event stream, so poll the status instead.
        stream.close();
        await this.sleep(this.opts.statusPollMs ?? 3000);
      }
      clearInterval(watcher);
      stream.close();
      this.stream = undefined;
      this.current = undefined;
      return true;
    }
    this.update({ connection: mismatch ?? { kind: "offline" }, unresponsiveNodes: unresponsive });
    return false;
  }

  private onEvent(client: ControlClient, e: ControlEvent): void {
    // `hello` only describes the stream. A call event carries nothing the snapshot shows; it feeds the recent-calls list.
    if (e.type === "hello") return;
    if (e.type === "call") {
      this.recent.push(e.data);
      if (this.recent.length > RECENT_CALLS) this.recent.splice(0, this.recent.length - RECENT_CALLS);
      this.emit("calls", this.recent.slice());
      return;
    }
    this.scheduleRefresh(client);
  }

  /** One client per instance and boot, so Last-Event-ID survives reconnects but not a kernel restart. */
  private clientFor(inst: Instance): ControlClient {
    const known = this.clients.get(inst.id);
    if (known && known.endpoint === inst.manifest.endpoint && known.bootId === inst.manifest.boot_id) {
      return known.client;
    }
    const client = new ControlClient({
      endpoint: inst.manifest.endpoint,
      kernelVersion: inst.manifest.kernel_version,
      ...(this.opts.requestTimeoutMs !== undefined && { requestTimeoutMs: this.opts.requestTimeoutMs }),
      ...(this.opts.streamIdleMs !== undefined && { streamIdleMs: this.opts.streamIdleMs }),
    });
    this.clients.set(inst.id, { client, endpoint: inst.manifest.endpoint, bootId: inst.manifest.boot_id });
    return client;
  }

  private scheduleRefresh(client: ControlClient): void {
    if (this.refreshing) {
      this.refreshDirty = true;
      return;
    }
    if (this.refreshTimer) return;
    this.refreshTimer = setTimeout(() => {
      this.refreshTimer = undefined;
      void this.refresh(client).catch(() => {
        // The event stream or the next poll notices a dead kernel.
      });
    }, this.opts.refreshDelayMs ?? 100);
  }

  private async refresh(client: ControlClient, instanceId?: string): Promise<void> {
    this.refreshing = true;
    try {
      const status = await client.getStatus();
      // The peer list is only needed for names; the summary in status covers the count.
      const peers = hasCapability(status.control.capabilities, "peers") ? await client.getPeers() : [];
      const id = instanceId ?? this.current?.id;
      if (id !== undefined) this.labels.set(id, status.hostname);
      this.update({
        connection: { kind: "connected", status, peers },
        instances: this.instanceInfos(),
        ...(id !== undefined && { selectedInstance: id }),
      });
    } catch (e) {
      if (e instanceof VersionMismatchError) {
        this.update({
          connection: { kind: "mismatch", kernelVersion: e.kernelVersion, apiVersion: e.apiVersion },
        });
      }
      throw e;
    } finally {
      this.refreshing = false;
      if (this.refreshDirty) {
        this.refreshDirty = false;
        this.scheduleRefresh(client);
      }
    }
  }

  private updateInstances(found: Instance[]): void {
    this.found = found;
    this.update({ instances: this.instanceInfos(), stoppedNodes: stoppedNodes(this.opts.configured?.() ?? [], found) });
  }

  private instanceInfos(): InstanceInfo[] {
    return this.found.map((i) => ({ id: i.id, label: this.labels.get(i.id) ?? (i.manifest.node || i.id) }));
  }

  private update(patch: Partial<Snapshot>): void {
    const next: Snapshot = { ...this.snap, ...patch };
    if (JSON.stringify(next) === JSON.stringify(this.snap)) return;
    this.snap = next;
    this.emit("change", next);
  }
}
