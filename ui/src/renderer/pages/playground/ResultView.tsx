import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { CallOutcome } from "../../../shared/playground";
import { getApi } from "../../api";
import { Badge } from "../../components";
import { useT } from "../../i18n";

export function ResultView({ outcome, instance }: { outcome: CallOutcome; instance: string }): JSX.Element {
  const t = useT();
  const [view, setView] = useState("structured");
  const [expanded, setExpanded] = useState(false);
  const r = outcome.result;
  const text = view === "raw" ? JSON.stringify(r, null, 2) : view === "text" ? r.content.filter((c) => c.type === "text").map((c) => c.text).join("\n") : JSON.stringify(r.structuredContent ?? r.content, null, 2);
  const large = text.length > 65536;
  return <section class="card tool-result" data-testid="tool-result" aria-live="polite"><div class="row"><h2>{t("pg.result")}</h2><Badge kind={r.isError ? "danger" : "ok"}>{t(r.isError ? "pg.toolError" : "pg.success")}</Badge><span class="hint mono">{outcome.durationMs} ms · {new TextEncoder().encode(JSON.stringify(r)).length.toLocaleString()} B</span><div class="spacer"/><select aria-label={t("pg.result")} value={view} onChange={(e) => setView(e.currentTarget.value)}><option value="structured">{t("pg.structured")}</option><option value="text">{t("pg.text")}</option><option value="raw">{t("pg.raw")}</option></select></div><div class="row"><button type="button" class="btn small" onClick={() => void getApi().playground({ op: "copy", instance, text })}>{t("pg.copy")}</button><button type="button" class="btn small" onClick={() => void getApi().playground({ op: "save", instance, text: JSON.stringify(r, null, 2) })}>{t("pg.save")}</button></div><pre tabIndex={0}>{large && !expanded ? text.slice(0, 65536) + "\n…" : text}</pre>{large && <button type="button" class="btn" onClick={() => setExpanded(!expanded)}>{t(expanded ? "pg.collapse" : "pg.expand")}</button>}</section>;
}
