import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Badge } from "../../components";
import { useCopy } from "../../design-copy";
import { useResource } from "../../hooks";
import { useT } from "../../i18n";
import { applySaved } from "../../lib/reload";

type Kind = "lan" | "tailnet" | "cloudflare";
const KINDS: Kind[] = ["lan", "tailnet", "cloudflare"];
const LABEL = { lan: "mode.lan", tailnet: "mode.tailnet", cloudflare: "mode.cloudflare" } as const;

const isKind = (v: unknown): v is Kind => KINDS.includes(v as Kind);

/** The order the kernel uses: the priority list, then the paths left out in the default order, minus the disabled ones. */
function effectiveOrder(priority: Kind[]): Kind[] {
  return [...priority, ...KINDS.filter((k) => !priority.includes(k))];
}

function readSelection(parsed: unknown): { supported: boolean; order: Kind[]; disabled: Kind[] } {
  const cfg = parsed && typeof parsed === "object" ? (parsed as Record<string, unknown>) : {};
  const mesh = cfg.mesh && typeof cfg.mesh === "object" ? (cfg.mesh as Record<string, unknown>) : {};
  const sel = mesh.path_selection && typeof mesh.path_selection === "object" ? (mesh.path_selection as Record<string, unknown>) : {};
  const list = (v: unknown): Kind[] => (Array.isArray(v) ? v.filter(isKind) : []);
  return { supported: cfg.schema_version === 2, order: effectiveOrder(list(sel.priority)), disabled: list(sel.disabled) };
}

/**
 * Manual path priority: which of LAN, Tailnet and Cloudflare two devices use when several work. The first
 * enabled path that connects wins; a disabled path is never dialed. The kernel applies a change at once and
 * moves live links over (their sessions resume), so there is no restart.
 */
export function PathPriority({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const copy = useCopy();
  const t = useT();
  const config = useResource(() => getApi().getConfig(), [ctx.snapshot.selectedInstance]);
  const res = config.resource;
  const data = res.state === "ready" ? res.data : res.state === "error" ? res.stale : undefined;
  const saved = data ? readSelection(data.parsed) : undefined;
  const [order, setOrder] = useState<Kind[]>(KINDS);
  const [disabled, setDisabled] = useState<Kind[]>([]);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (saved) { setOrder(saved.order); setDisabled(saved.disabled); }
  }, [data?.etag]);
  if (!data || !saved?.supported || !ctx.can("config.write")) return null;

  const dirty = order.join() !== saved.order.join() || [...disabled].sort().join() !== [...saved.disabled].sort().join();
  const move = (index: number, by: -1 | 1): void => {
    const next = [...order];
    [next[index], next[index + by]] = [next[index + by]!, next[index]!];
    setOrder(next);
    ctx.applied(undefined);
  };
  const toggle = (kind: Kind): void => {
    setDisabled((cur) => (cur.includes(kind) ? cur.filter((k) => k !== kind) : [...cur, kind]));
    ctx.applied(undefined);
  };
  const none = disabled.length === KINDS.length;

  const save = async (): Promise<void> => {
    setBusy(true);
    try {
      const value = { mode: "manual", priority: order, ...(disabled.length ? { disabled: KINDS.filter((k) => disabled.includes(k)) } : {}) };
      const r = await getApi().patchConfig({ edits: [{ path: "/mesh/path_selection", value }], etag: data.etag, dryRun: false });
      if (!r.ok) { ctx.toast(r.error.message, true); return; }
      ctx.applied(await applySaved(r.data.restart_required));
      ctx.toast(copy("连接路径优先级已保存。", "Path priority saved."));
      config.reload();
    } finally { setBusy(false); }
  };

  return (
    <section class="card" data-testid="path-priority">
      <h3>{copy("连接路径优先级", "Connection path priority")}</h3>
      <p class="hint">{copy(
        "设备之间有多条路径可用时，按这个顺序选：第一条能连上的路径胜出。跨国 Tailscale 被阻断或不稳定时，可以把 Cloudflare 排在前面，或停用 Tailnet。保存后立即生效，正在使用的连接会换到新顺序里最靠前的可用路径，会话自动续传。",
        "When several paths work between two devices, they are tried in this order and the first that connects wins. If a cross-border Tailnet is blocked or flaky, put Cloudflare first or turn Tailnet off. A change applies at once: live connections move to the best available path in the new order and their sessions resume.",
      )}</p>
      <ol class="path-priority-list">
        {order.map((kind, i) => (
          <li key={kind} data-testid={`path-priority-${kind}`}>
            <span class="mono">{i + 1}</span>{" "}
            <label class="check">
              <input type="checkbox" checked={!disabled.includes(kind)} disabled={busy} onChange={() => toggle(kind)} />
              <span>{t(LABEL[kind])}</span>
            </label>{" "}
            {disabled.includes(kind) && <Badge kind="warn">{copy("已停用", "Off")}</Badge>}{" "}
            <button type="button" class="btn" disabled={busy || i === 0} aria-label={copy("上移", "Move up")} onClick={() => move(i, -1)} data-testid={`path-priority-up-${kind}`}>↑</button>{" "}
            <button type="button" class="btn" disabled={busy || i === order.length - 1} aria-label={copy("下移", "Move down")} onClick={() => move(i, 1)} data-testid={`path-priority-down-${kind}`}>↓</button>
          </li>
        ))}
      </ol>
      {none && <p class="hint">{copy("三条路径都停用后，设备之间无法互连。", "With all three paths off, devices cannot reach each other.")}</p>}
      <div class="row">
        <button type="button" class="btn primary" disabled={busy || !dirty || none} onClick={() => void save()} data-testid="path-priority-save">{copy("保存", "Save")}</button>
        <button type="button" class="btn" disabled={busy || !dirty} onClick={() => { setOrder(saved.order); setDisabled(saved.disabled); }}>{copy("还原", "Reset")}</button>
      </div>
    </section>
  );
}
