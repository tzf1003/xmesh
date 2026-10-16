import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { getApi } from "../../api";
import { Badge, Empty, Field, ResourceView } from "../../components";
import { useResource } from "../../hooks";
import { useT } from "../../i18n";
import type { Decision, MeshCell } from "../../../shared/mesh";
import type { MeshTabProps } from "../Mesh";
import { MeshError } from "./common";

const DECISION_KIND: Record<Decision, "ok" | "danger" | ""> = { allow: "ok", deny: "danger", unknown: "" };

/**
 * Who may call whom, in two layers: the control plane's network policy and
 * each node's own tool permissions (`agent.authz`). Cells where the layers
 * disagree are the ones worth looking at.
 */
export function MeshMatrix({ ctx }: MeshTabProps): JSX.Element {
  const t = useT();
  const [onlyBad, setOnlyBad] = useState(false);
  const { resource, reload } = useResource(() => getApi().mesh({ op: "matrix", mismatchOnly: onlyBad }), [onlyBad, ctx.snapshot.selectedInstance]);
  const connected = ctx.snapshot.connection.kind === "connected";

  return (
    <section class="card" data-testid="mesh-matrix">
      <h2>{t("mesh.matrix.title")}</h2>
      <p class="hint">{t("mesh.matrix.intro")}</p>
      {!connected && <p class="hint" data-testid="mesh-matrix-nonode">{t("mesh.matrix.noNode")}</p>}
      <div class="row">
        <Field label={t("mesh.matrix.onlyMismatch")}>
          <input type="checkbox" checked={onlyBad} onChange={(e) => setOnlyBad((e.currentTarget as HTMLInputElement).checked)} data-testid="mesh-matrix-only" />
        </Field>
        <button type="button" class="btn" onClick={reload} data-testid="mesh-matrix-reload">
          {t("common.refresh")}
        </button>
      </div>
      {resource.state === "error" && resource.stale === undefined ? (
        <MeshError error={resource.error} toast={ctx.toast} />
      ) : (
        <ResourceView resource={resource}>
          {(m) =>
            m.cells.length === 0 ? (
              <Empty>{onlyBad ? t("mesh.matrix.noMismatch") : t("mesh.matrix.none")}</Empty>
            ) : (
              <div class="table-wrap">
                <table data-testid="mesh-matrix-table">
                  <caption class="hint">{t("mesh.matrix.port", { port: m.port })}</caption>
                  <thead>
                    <tr>
                      <th scope="col">{t("mesh.matrix.src")}</th>
                      <th scope="col">{t("mesh.matrix.dst")}</th>
                      <th scope="col">{t("mesh.matrix.network")}</th>
                      <th scope="col">{t("mesh.matrix.tools")}</th>
                      <th scope="col">{t("mesh.matrix.note")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {m.cells.map((c) => (
                      <CellRow key={`${c.src}>${c.dst}`} cell={c} />
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </ResourceView>
      )}
    </section>
  );
}

function CellRow({ cell }: { cell: MeshCell }): JSX.Element {
  const t = useT();
  return (
    <tr data-testid={`mesh-cell-${cell.src}-${cell.dst}`} data-mismatch={cell.mismatch !== undefined}>
      <td>{cell.src}</td>
      <td>{cell.dst}</td>
      <td>
        <Badge kind={DECISION_KIND[cell.network]}>{t(`mesh.decision.${cell.network}` as const)}</Badge>
      </td>
      <td>
        <Badge kind={DECISION_KIND[cell.tools]}>{t(`mesh.decision.${cell.tools}` as const)}</Badge>
        {cell.allowedTools.length > 0 && <div class="hint mono">{cell.allowedTools.join(", ")}</div>}
      </td>
      <td>
        {cell.mismatch && <Badge kind="warn">{cell.mismatch}</Badge>}
        {cell.networkNote && <div class="hint">{cell.networkNote}</div>}
      </td>
    </tr>
  );
}
