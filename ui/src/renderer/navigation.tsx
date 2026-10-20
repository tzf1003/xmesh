import type { JSX } from "preact";
import { useMemo, useState } from "preact/hooks";
import type { PageId, PageFocus } from "./app";
import type { Snapshot } from "../shared/state";
import type { Key } from "./messages";
import { Dialog } from "./components";
import { Icon, type IconName } from "./icons";
import { useT } from "./i18n";
import { peerKey } from "./lib/topology";

export const NAVIGATION: readonly { id: PageId; label: Key; icon: IconName; group: "network" | "access" | "runtime" | "modules" | "settings" }[] = [
  { id: "overview", label: "topology.title", icon: "topology", group: "network" },
  { id: "devices", label: "nav.devices", icon: "device", group: "network" },
  { id: "runtime", label: "topology.operations", icon: "sliders", group: "runtime" },
  { id: "terminal", label: "ws.terminal", icon: "terminal", group: "runtime" },
  { id: "transfers", label: "ws.files", icon: "code", group: "runtime" },
  { id: "playground", label: "pg.title", icon: "code", group: "runtime" },
  { id: "sessions", label: "nav.sessions", icon: "terminal", group: "network" },
  { id: "computer", label: "nav.computer", icon: "device", group: "runtime" },
  { id: "authz", label: "nav.authz", icon: "shield", group: "access" },
  { id: "ingress", label: "nav.ingress", icon: "globe", group: "access" },
  { id: "ci", label: "nav.ci", icon: "runner", group: "modules" },
  { id: "mesh", label: "nav.mesh", icon: "network", group: "modules" },
  { id: "settings", label: "nav.settings", icon: "sliders", group: "settings" },
];
export const pageAvailable = (id: PageId, connected: boolean): boolean => connected || ["overview", "mesh", "settings", "about", "setup"].includes(id);

export function CommandPalette({ snapshot, onClose, go }: { snapshot: Snapshot; onClose(): void; go(page: PageId, focus?: PageFocus): void }): JSX.Element {
  const t = useT();
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);
  const results = useMemo(() => {
    const q = query.trim().toLocaleLowerCase();
    const c = snapshot.connection;
    const pages = NAVIGATION.filter((n) => pageAvailable(n.id, c.kind === "connected")).map((n) => ({ id: n.id, label: t(n.label), icon: n.icon, focus: undefined as PageFocus | undefined }));
    pages.push({ id: "about", label: t("nav.about"), icon: "info", focus: undefined });
    if (c.kind === "connected") {
      pages.push({ id: "audit", label: t("nav.audit"), icon: "terminal", focus: { sessionsTab: "history" } });
      for (const role of ["agent", "gateway", "ingress"] as const) pages.push({ id: "settings", label: `${role[0]!.toUpperCase()}${role.slice(1)} · ${t("nav.settings")}`, icon: "sliders", focus: { role } });
    }
    const devices = c.kind === "connected" ? c.peers.map((p) => ({ id: "overview" as const, label: p.name, icon: "device" as const, focus: { peer: peerKey(p) } as PageFocus })) : [];
    return [...devices, ...pages].filter((n) => n.label.toLocaleLowerCase().includes(q));
  }, [query, snapshot.connection, snapshot.instances.length, t]);
  const choose = (at: number): void => { const result = results[at]; if (result) { go(result.id, result.focus); onClose(); } };
  return <Dialog title={t("nav.find")} onClose={onClose} testid="command-palette">
    <div onKeyDown={(e) => {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); setIndex((n) => results.length ? (n + (e.key === "ArrowDown" ? 1 : results.length - 1)) % results.length : 0); }
      if (e.key === "Enter") { e.preventDefault(); choose(index); }
    }}>
      <label class="command-search"><Icon name="search"/><input type="search" role="combobox" aria-expanded="true" aria-autocomplete="list" aria-activedescendant={results[index] ? `command-result-${index}` : undefined} value={query} onInput={(e) => { setQuery(e.currentTarget.value); setIndex(0); }} aria-label={t("nav.find")} aria-controls="command-results" data-testid="command-input"/></label>
      <div id="command-results" role="listbox" class="command-results" aria-label={t("nav.find")}>
        {results.length === 0 && <div class="empty">{t("nav.findEmpty")}</div>}
        {results.map((r, i) => <button type="button" role="option" id={`command-result-${i}`} aria-selected={i === index} key={`${r.id}:${r.label}`} class={`command-result ${i === index ? "highlighted" : ""}`} onClick={() => choose(i)}><Icon name={r.icon}/>{r.label}<Icon name="chevron"/></button>)}
      </div>
    </div>
    <button type="button" class="btn small" onClick={onClose}>{t("nav.close")}</button>
  </Dialog>;
}
