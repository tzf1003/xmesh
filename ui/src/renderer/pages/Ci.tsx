import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Banner, Empty, NotSupported, PageHead, ResourceView } from "../components";
import { useResource } from "../hooks";
import { useT } from "../i18n";
import type { Key } from "../messages";
import type { CiNoticeCode, CiOverview } from "../../shared/control";
import { Accounts } from "./ci/Accounts";
import { AddBinding } from "./ci/AddBinding";
import { BindingCard } from "./ci/Binding";
import { Command } from "./ci/common";

const NOTICE_KEY: Record<CiNoticeCode, Key> = {
  linger_disabled: "ci.notice.linger_disabled",
  autologon_disabled: "ci.notice.autologon_disabled",
  git_missing: "ci.notice.git_missing",
  missing_dependency: "ci.notice.missing_dependency",
  runner_unsupported: "ci.notice.runner_unsupported",
};

/** The page's data and the things every part of it needs. */
export interface CiPageProps {
  ctx: PageContext;
  overview: CiOverview;
  reload(): void;
}

/**
 * GitHub Actions runners the kernel hosts for this node (CTR-CTL-15): bindings of repositories, the runner
 * processes behind each, and the GitHub accounts used to register them.
 */
export function Ci({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  if (!ctx.can("ci")) {
    return (
      <div class="page" data-testid="page-ci">
        <PageHead title={t("ci.title")} sub={t("ci.sub")} />
        <NotSupported what={t("ci.title")} />
      </div>
    );
  }
  return <CiPage ctx={ctx} />;
}

function CiPage({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const { resource, reload } = useResource(() => getApi().ci({ op: "get" }), [ctx.snapshot.selectedInstance], 2000);
  const [adding, setAdding] = useState(false);
  return (
    <div class="page" data-testid="page-ci">
      <PageHead title={t("ci.title")} sub={t("ci.sub")}>
        <button type="button" class="btn primary" onClick={() => setAdding(true)} data-testid="ci-add">
          {t("ci.add")}
        </button>
      </PageHead>
      <ResourceView resource={resource}>
        {(overview) =>
          <>
              {overview.notices.length > 0 && (
                <Banner kind="warn" testid="ci-notices">
                  <p>
                    <strong>{t("ci.notice.title")}</strong>
                  </p>
                  <ul class="plain">
                    {overview.notices.map((n) => (
                      <li key={n.code} data-testid={`ci-notice-${n.code}`}>
                        {t(NOTICE_KEY[n.code] ?? "ci.notice.title")}
                        {n.command && (
                          <>
                            <Command text={n.command} testid={`ci-notice-command-${n.code}`} />
                            <span class="hint">{t("ci.commandHint")}</span>
                          </>
                        )}
                      </li>
                    ))}
                  </ul>
                </Banner>
              )}
              {overview.draining && <Banner testid="ci-draining">{t("ci.draining")}</Banner>}
              <p class="hint" data-testid="ci-package">
                {overview.runner.cached && overview.runner.version ? t("ci.package", { version: overview.runner.version }) : t("ci.noPackage")}
              </p>
              {overview.bindings.length === 0 ? (
                <Empty>{t("ci.empty")}</Empty>
              ) : (
                <div class="section-stack" data-testid="ci-bindings">
                  {overview.bindings.map((b) => (
                    <BindingCard key={b.id} ctx={ctx} overview={overview} binding={b} reload={reload} />
                  ))}
                </div>
              )}
              <Accounts ctx={ctx} overview={overview} reload={reload} />
              {adding && (
                <AddBinding
                  ctx={ctx}
                  overview={overview}
                  onClose={() => setAdding(false)}
                  onCreated={() => {
                    setAdding(false);
                    reload();
                  }}
                />
              )}
          </>
        }
      </ResourceView>
    </div>
  );
}

