import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import type { CallEventData } from "../shared/control";
import type { ActionError, DataResult } from "../shared/ipc";
import { EMPTY_SNAPSHOT, type Snapshot } from "../shared/state";
import { getApi } from "./api";

/** The shell's snapshot: loaded once, then pushed by the main process. */
export function useSnapshot(): Snapshot {
  const [snap, setSnap] = useState<Snapshot>(EMPTY_SNAPSHOT);
  useEffect(() => {
    const api = getApi();
    let live = true;
    const off = api.onSnapshot((s) => {
      if (live) setSnap(s);
    });
    void api.getSnapshot().then((s) => {
      if (live) setSnap(s);
    });
    return () => {
      live = false;
      off();
    };
  }, []);
  return snap;
}

export function useRecentCalls(): CallEventData[] {
  const [calls, setCalls] = useState<CallEventData[]>([]);
  useEffect(() => {
    const api = getApi();
    let live = true;
    const off = api.onCalls((c) => {
      if (live) setCalls(c);
    });
    void api.getRecentCalls().then((c) => {
      if (live) setCalls(c);
    });
    return () => {
      live = false;
      off();
    };
  }, []);
  return calls;
}

export type Resource<T> =
  | { state: "loading" }
  | { state: "ready"; data: T }
  | { state: "error"; error: ActionError; stale?: T };

export interface ResourceHandle<T> {
  resource: Resource<T>;
  reload(): void;
}

/**
 * Loads a kernel resource and keeps it fresh. `deps` restarts the load (for
 * example when the node instance changes); `pollMs` refreshes it while the
 * component is mounted. An error after a good load keeps the last data as `stale`.
 */
export function useResource<T>(load: () => Promise<DataResult<T>>, deps: unknown[], pollMs?: number): ResourceHandle<T> {
  const [resource, setResource] = useState<Resource<T>>({ state: "loading" });
  const loadRef = useRef(load);
  loadRef.current = load;
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let live = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const run = async (): Promise<void> => {
      let r: DataResult<T>;
      try {
        r = await loadRef.current();
      } catch (e) {
        r = { ok: false, error: { code: "internal", message: e instanceof Error ? e.message : String(e) } };
      }
      if (!live) return;
      setResource((prev) =>
        r.ok
          ? { state: "ready", data: r.data }
          : { state: "error", error: r.error, ...(prev.state === "ready" && { stale: prev.data }), ...(prev.state === "error" && prev.stale !== undefined && { stale: prev.stale }) },
      );
      if (pollMs) timer = setTimeout(() => void run(), pollMs);
    };
    setResource((prev) => (prev.state === "ready" ? prev : { state: "loading" }));
    void run();
    return () => {
      live = false;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick, pollMs]);

  const reload = useCallback(() => setTick((n) => n + 1), []);
  return { resource, reload };
}
