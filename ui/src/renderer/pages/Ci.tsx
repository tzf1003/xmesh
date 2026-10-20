import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, NotSupported, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import type { Key } from "../messages";
import { ciCounts } from "../../shared/ci";
import type { CiNoticeCode, CiOverview } from "../../shared/control";
import { Accounts } from "./ci/Accounts";
import { AddBinding } from "./ci/AddBinding";
import { BindingCard } from "./ci/Binding";
import { Command } from "./ci/common";
import { filterBindings, type CiFilter } from "./module-model";
import { useModuleT } from "./module-messages";
import { ModuleBox, ModuleBreadcrumb, ModuleFacts, ModuleStat, ModuleSummary } from "./module-ui";
import "./module-redesign.css";

const NOTICE_KEY: Record<CiNoticeCode, Key> = {
  linger_disabled: "ci.notice.linger_disabled", autologon_disabled: "ci.notice.autologon_disabled", git_missing: "ci.notice.git_missing", missing_dependency: "ci.notice.missing_dependency", runner_unsupported: "ci.notice.runner_unsupported",
};

export interface CiPageProps { ctx: PageContext; overview: CiOverview; reload(): void; }
type CiTab = "repos" | "accounts" | "environment";

export function Ci({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  if (!ctx.can("ci")) return <div class="page module-page" data-testid="page-ci"><ModuleBreadcrumb title={t("ci.title")}/><PageHead title={t("ci.title")} sub={t("ci.sub")}/><NotSupported what={t("ci.title")}/></div>;
  return <CiPage ctx={ctx}/>;
}

function CiPage({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT(), m = useModuleT();
  const { resource, reload } = useResource(() => getApi().ci({ op: "get" }), [ctx.snapshot.selectedInstance], 2000);
  const [adding, setAdding] = useState(false);
  const [tab, setTab] = useState<CiTab>("repos");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<CiFilter>("all");
  const status = ctx.snapshot.connection.kind === "connected" ? ctx.snapshot.connection.status : undefined;
  const host = ctx.snapshot.instances.find((instance) => instance.id === ctx.snapshot.selectedInstance)?.label || status?.hostname || ctx.snapshot.selectedInstance;
  const reset = (): void => { setQuery(""); setFilter("all"); };
  return <div class="page module-page" data-testid="page-ci">
    <ModuleBreadcrumb title={t("ci.title")}/>
    <PageHead title={t("ci.title")} sub={m("ciSub")}><div class="module-heading-actions"><button type="button" class="btn" onClick={reload} data-testid="ci-refresh">{t("common.refresh")}</button><button type="button" class="btn primary" onClick={() => setAdding(true)} data-testid="ci-add"><Icon name="plus"/>{t("ci.add")}</button></div></PageHead>
    <ResourceView resource={resource}>{(overview) => {
      const counts = ciCounts(overview.bindings);
      const running = overview.bindings.flatMap((binding) => binding.instances).filter((instance) => instance.state === "busy" || instance.job !== undefined).length;
      const attention = overview.bindings.flatMap((binding) => binding.instances).filter((instance) => instance.state === "broken" || instance.problem !== undefined).length;
      const filtered = filterBindings(overview.bindings, query, filter);
      return <>
        <ModuleSummary icon="runner" title={<>{host}<Badge>{m("currentHost")}</Badge></>} detail={<>GitHub Actions<span>·</span>{status && <>{status.os} {status.arch}<span>·</span></>}<span class={`module-state ${overview.draining ? "warn" : "ok"}`}>{overview.draining ? t("ci.draining") : m("serviceReady")}</span></>}>
          <ModuleStat value={overview.bindings.length} label={m("bindings")}/><ModuleStat value={running} label={m("busy")} tone="ok"/><ModuleStat value={counts.idle} label={m("idle")}/><ModuleStat value={attention} label={m("attention")} tone={attention ? "warn" : ""}/>
        </ModuleSummary>
        <div class="module-tabs" role="tablist" aria-label={t("ci.title")}>{(["repos", "accounts", "environment"] as const).map((id) => <button key={id} type="button" role="tab" aria-selected={tab === id} aria-controls={`ci-panel-${id}`} id={`ci-tab-${id}`} data-testid={`ci-tab-${id}`} onClick={() => setTab(id)}>{m(id === "repos" ? "repositories" : id)}{id !== "environment" && <span>{id === "repos" ? overview.bindings.length : overview.accounts.length}</span>}</button>)}</div>
        {overview.notices.length > 0 && <Banner kind="warn" testid="ci-notices"><p><strong>{t("ci.notice.title")}</strong></p><ul class="plain">{overview.notices.map((notice) => <li key={notice.code} data-testid={`ci-notice-${notice.code}`}>{t(NOTICE_KEY[notice.code] ?? "ci.notice.title")}{notice.command && <><Command text={notice.command} testid={`ci-notice-command-${notice.code}`}/><span class="hint">{t("ci.commandHint")}</span></>}</li>)}</ul></Banner>}
        {overview.draining && <Banner testid="ci-draining">{t("ci.draining")}</Banner>}
        <div class="module-layout">
          <div class="module-main">
            <div role="tabpanel" id="ci-panel-repos" aria-labelledby="ci-tab-repos" hidden={tab !== "repos"}>
              {attention > 0 && <div class="module-alert" data-testid="ci-attention"><Icon name="info"/><span>{m("runnerIssues", { n: attention })}</span><button type="button" class="module-link" onClick={() => { setQuery(""); setFilter("attention"); }}>{m("showIssues")}<Icon name="arrow"/></button></div>}
              <div class="module-toolbar"><label class="module-search"><Icon name="search"/><input type="search" value={query} onInput={(event) => setQuery(event.currentTarget.value)} placeholder={m("searchRepos")} aria-label={m("searchRepos")} data-testid="ci-search"/></label><select value={filter} onChange={(event) => setFilter(event.currentTarget.value as CiFilter)} aria-label={m("allStates")} data-testid="ci-filter"><option value="all">{m("allStates")}</option><option value="busy">{m("busy")}</option><option value="attention">{m("attention")}</option><option value="paused">{m("paused")}</option></select><small>{m("polling")}</small></div>
              {overview.bindings.length === 0 ? <div class="module-empty"><span class="module-avatar"><Icon name="folder"/></span><h2>{t("ci.empty")}</h2><p>{m("repositoriesHint")}</p><button type="button" class="btn primary" onClick={() => setAdding(true)}>{t("ci.add")}</button></div> : filtered.length === 0 ? <div class="module-empty"><h2>{m("noMatches")}</h2><button type="button" class="btn" onClick={reset}>{m("reset")}</button></div> : <div class="section-stack" data-testid="ci-bindings">{filtered.map((binding) => <BindingCard key={binding.id} ctx={ctx} overview={overview} binding={binding} reload={reload}/>)}</div>}
            </div>
            <div role="tabpanel" id="ci-panel-accounts" aria-labelledby="ci-tab-accounts" hidden={tab !== "accounts"}><Accounts ctx={ctx} overview={overview} reload={reload}/></div>
            <div role="tabpanel" id="ci-panel-environment" aria-labelledby="ci-tab-environment" hidden={tab !== "environment"}><ModuleBox title={m("runtimeTitle")} icon="device"><p>{m("runtimeHint")}</p><EnvironmentFacts overview={overview}/></ModuleBox></div>
          </div>
          <aside class="module-side">
            <ModuleBox title={m("environment")} icon="device"><div class="module-health"><Icon name="shield"/><div><strong>{m("managed")}</strong><small>{host}{status && ` · ${status.os} ${status.arch}`}</small></div></div><EnvironmentFacts overview={overview} packageTestId="ci-package"/><button type="button" class="module-link" onClick={() => setTab("environment")}>{m("runtimeSettings")}<Icon name="arrow"/></button></ModuleBox>
            <ModuleBox title={m("accounts")} action={<button type="button" class="module-link" onClick={() => setTab("accounts")}>{m("manage")}<Icon name="chevron"/></button>}>{overview.accounts.length ? overview.accounts.slice(0, 3).map((account) => <div class="module-account" key={account.id}><span class="module-account-avatar">{account.login.slice(0, 1).toUpperCase()}</span><div><strong>{account.login}</strong><small>{account.host}</small></div></div>) : <p>{m("noAccounts")}</p>}<p>{m("accountHint")}</p></ModuleBox>
            <WorkflowSnippet ctx={ctx} host={host}/>
          </aside>
        </div>
        {adding && <AddBinding ctx={ctx} overview={overview} onClose={() => setAdding(false)} onCreated={() => { setAdding(false); setTab("repos"); reset(); reload(); }}/>}
      </>;
    }}</ResourceView>
  </div>;
}

function EnvironmentFacts({ overview, packageTestId }: { overview: CiOverview; packageTestId?: string }): JSX.Element {
  const t = useT(), m = useModuleT();
  return <ModuleFacts items={[[m("instances"), overview.bindings.reduce((total, binding) => total + binding.instances.length, 0)], [m("package"), <span data-testid={packageTestId}>{overview.runner.cached && overview.runner.version ? t("ci.package", { version: overview.runner.version }) : t("ci.noPackage")}</span>], [m("sleep"), m(overview.prevent_sleep_while_busy ? "sleepPrevented" : "sleepAllowed")], [m("stopTimeout"), m("seconds", { n: overview.stop_timeout_s })]]}/>;
}

function WorkflowSnippet({ ctx, host }: { ctx: PageContext; host: string }): JSX.Element {
  const m = useModuleT();
  const snippet = `runs-on:\n  - self-hosted\n  - xmesh${host ? `\n  - xmesh-${host}` : ""}`;
  const copy = async (): Promise<void> => {
    const result = await getApi().copy("ciWorkflow");
    ctx.toast(result.ok ? m("copied") : m("copyFailed"), !result.ok);
  };
  return <ModuleBox title={m("workflow")}><p>{m("workflowHint")}</p><pre class="module-workflow" data-testid="ci-workflow">{snippet}</pre><button type="button" class="module-link" onClick={() => void copy()} data-testid="ci-workflow-copy"><Icon name="copy"/>{m("copyWorkflow")}</button></ModuleBox>;
}
