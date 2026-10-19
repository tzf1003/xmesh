import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { Confirm } from "../components";
import { useT } from "../i18n";
import { Config } from "./Config";
import { Settings } from "./Settings";

export function Configuration({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const [view, setView] = useState<"form" | "source">(ctx.focus?.configView ?? "form");
  const [dirty, setDirty] = useState(false);
  const [pending, setPending] = useState<"form" | "source">();
  useEffect(() => ctx.setConfigDirty(dirty), [dirty, ctx.setConfigDirty]);
  useEffect(() => {
    if (ctx.focus?.configView && !dirty) setView(ctx.focus.configView);
  }, [ctx.focus?.configView]);
  const choose = (next: "form" | "source"): void => {
    if (view === next) return;
    if (dirty) setPending(next);
    else setView(next);
  };
  return <div class="page configuration-page" data-testid="page-config">
    <div class="tabs" role="tablist" aria-label={t("nav.config")}>
      <button type="button" class="tab" role="tab" aria-selected={view === "form"} onClick={() => choose("form")} data-testid="config-form-tab">{t("nav.form")}</button>
      <button type="button" class="tab" role="tab" aria-selected={view === "source"} onClick={() => choose("source")} data-testid="config-source-tab">{t("nav.source")}</button>
    </div>
    <div role="tabpanel">{view === "form" ? <Settings ctx={ctx} onDirtyChange={setDirty}/> : <Config ctx={ctx} onDirtyChange={setDirty}/>}</div>
    {pending && <Confirm title={t("config.discardAsk")} body={<p>{t("topology.configDirty")}</p>} confirmLabel={t("config.discard")} onCancel={() => setPending(undefined)} onConfirm={() => { setView(pending); setPending(undefined); setDirty(false); }}/>}
  </div>;
}
