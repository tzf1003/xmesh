import type { ComponentChildren, JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { ActionError } from "../shared/ipc";
import { errorText, useT } from "./i18n";
import type { Resource } from "./hooks";

export function PageHead({ title, sub, children }: { title: string; sub?: string; children?: ComponentChildren }): JSX.Element {
  return (
    <div class="page-head">
      <div class="grow">
        <h1>{title}</h1>
        {sub && <p class="sub">{sub}</p>}
      </div>
      {children}
    </div>
  );
}

export function Badge({ kind = "", children }: { kind?: "" | "ok" | "warn" | "danger" | "info"; children: ComponentChildren }): JSX.Element {
  return <span class={`badge ${kind}`}>{children}</span>;
}

export function Banner({ kind = "", children, testid }: { kind?: "" | "ok" | "warn" | "danger"; children: ComponentChildren; testid?: string }): JSX.Element {
  return (
    <div class={`banner ${kind}`} role={kind === "danger" ? "alert" : "status"} data-testid={testid}>
      {children}
    </div>
  );
}

export function ErrorBanner({ error, testid = "error" }: { error: ActionError; testid?: string }): JSX.Element {
  const t = useT();
  return (
    <Banner kind="danger" testid={testid}>
      {errorText(t, error)}
    </Banner>
  );
}

export function Empty({ children }: { children: ComponentChildren }): JSX.Element {
  return <div class="empty">{children}</div>;
}

/** Common rendering for the loading and error states of a kernel resource. */
export function ResourceView<T>({
  resource,
  children,
}: {
  resource: Resource<T>;
  children: (data: T, stale: boolean) => ComponentChildren;
}): JSX.Element {
  const t = useT();
  if (resource.state === "loading") return <Empty>{t("common.loading")}</Empty>;
  if (resource.state === "error") {
    return (
      <>
        <ErrorBanner error={resource.error} />
        {resource.stale !== undefined && <>{children(resource.stale, true)}</>}
      </>
    );
  }
  return <>{children(resource.data, false)}</>;
}

/** A dialog the user must answer. Escape cancels; focus starts on the first button and stays inside. */
export function Dialog({
  title,
  children,
  onClose,
  testid = "dialog",
}: {
  title: string;
  children: ComponentChildren;
  onClose: () => void;
  testid?: string;
}): JSX.Element {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    const previous = document.activeElement as HTMLElement | null;
    el?.querySelector<HTMLElement>("input, textarea, select, button")?.focus();
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== "Tab" || !el) return;
      const items = [...el.querySelectorAll<HTMLElement>("input, textarea, select, button:not([disabled]), a[href]")].filter((x) => !x.hasAttribute("disabled"));
      if (items.length === 0) return;
      const first = items[0]!;
      const last = items[items.length - 1]!;
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      previous?.focus?.();
    };
  }, [onClose]);
  return (
    <div class="overlay">
      <div class="dialog" role="dialog" aria-modal="true" aria-label={title} ref={ref} data-testid={testid}>
        <h2>{title}</h2>
        {children}
      </div>
    </div>
  );
}

export function Confirm({
  title,
  body,
  confirmLabel,
  danger = false,
  busy = false,
  onConfirm,
  onCancel,
}: {
  title: string;
  body: ComponentChildren;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}): JSX.Element {
  const t = useT();
  return (
    <Dialog title={title} onClose={onCancel} testid="confirm">
      <div>{body}</div>
      <div class="row end" style={{ marginTop: "12px" }}>
        <button type="button" class="btn" onClick={onCancel} data-testid="confirm-cancel">
          {t("common.cancel")}
        </button>
        <button type="button" class={`btn ${danger ? "danger" : "primary"}`} onClick={onConfirm} disabled={busy} data-testid="confirm-ok">
          {confirmLabel}
        </button>
      </div>
    </Dialog>
  );
}

export interface Toast {
  id: number;
  text: string;
  error: boolean;
}

/** Transient messages; `push` is stable across renders. */
export function useToasts(): { toasts: Toast[]; push: (text: string, error?: boolean) => void } {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(1);
  const push = useRef((text: string, error = false): void => {
    const id = next.current++;
    setToasts((l) => [...l, { id, text, error }]);
    setTimeout(() => setToasts((l) => l.filter((x) => x.id !== id)), error ? 8000 : 3500);
  });
  return { toasts, push: push.current };
}

export function Toasts({ toasts }: { toasts: Toast[] }): JSX.Element {
  return (
    <div class="toasts" role="status" aria-live="polite">
      {toasts.map((x) => (
        <div key={x.id} class={`toast ${x.error ? "error" : ""}`} data-testid="toast">
          {x.text}
        </div>
      ))}
    </div>
  );
}

/** A labelled form control. `hint` goes under the control. */
export function Field({ label, hint, children }: { label: string; hint?: string; children: ComponentChildren }): JSX.Element {
  return (
    <label class="field">
      <span>{label}</span>
      {children}
      {hint && <span class="hint">{hint}</span>}
    </label>
  );
}

export function Meter({ used, max, label }: { used: number; max: number; label: string }): JSX.Element {
  const pct = max > 0 ? Math.min(100, Math.round((used / max) * 100)) : 0;
  const barRef = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    // CSSOM assignment is allowed under the panel CSP; an inline style attribute is not.
    if (barRef.current) barRef.current.style.width = `${pct}%`;
  }, [pct]);
  return (
    <div class="stat">
      <h3>{label}</h3>
      <div class="value">
        {used} / {max}
      </div>
      <div class={`bar ${pct >= 100 ? "full" : ""}`} role="progressbar" aria-valuenow={used} aria-valuemin={0} aria-valuemax={max} aria-label={label}>
        <span ref={barRef} />
      </div>
    </div>
  );
}
