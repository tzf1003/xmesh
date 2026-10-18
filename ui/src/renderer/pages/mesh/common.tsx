import type { JSX } from "preact";
import { getApi } from "../../api";
import { Banner, ErrorBanner } from "../../components";
import { useT } from "../../i18n";
import { clipLines } from "../../lib/mesh";
import { useSnapshot } from "../../hooks";
import type { ActionError } from "../../../shared/ipc";
import { type MeshPlan, meshLoginCommands } from "../../../shared/mesh";

/** An error from a `xmesh mesh` call. A missing credential gets the login steps instead of a bare message. */
export function MeshError({ error, toast }: { error: ActionError; toast: (text: string, error?: boolean) => void }): JSX.Element {
  const t = useT();
  const commands = meshLoginCommands(useSnapshot().cliCommand);
  if (error.code !== "no_credential") return <ErrorBanner error={error} testid="mesh-error" />;
  const copy = async (kind: "meshLoginRead" | "meshLoginWrite" | "meshLoginReadHeadscale" | "meshLoginWriteHeadscale"): Promise<void> => {
    const r = await getApi().copy(kind);
    toast(r.ok ? t("mesh.copied") : t("mesh.copyFailed"), !r.ok);
  };
  return (
    <Banner kind="warn" testid="mesh-no-credential">
      <p>
        <strong>{t("mesh.noCredential.title")}</strong>
      </p>
      <p>{t("mesh.noCredential.body")}</p>
      {(
        [
          ["read", "meshLoginRead", "meshLoginReadHeadscale", "mesh-copy-read", "mesh-copy-read-headscale"],
          ["write", "meshLoginWrite", "meshLoginWriteHeadscale", "mesh-copy-write", "mesh-copy-write-headscale"],
        ] as const
      ).map(([access, tsKind, hsKind, tsId, hsId]) => (
        <div key={access}>
          <p>{t(access === "read" ? "mesh.noCredential.read" : "mesh.noCredential.write")}</p>
          <ul class="plain">
            <li>
              Tailscale: <code>{commands[access]}</code>{" "}
              <button type="button" class="btn small" onClick={() => void copy(tsKind)} data-testid={tsId}>
                {t("mesh.copy")}
              </button>
            </li>
            <li>
              Headscale: <code>{commands[access === "read" ? "readHeadscale" : "writeHeadscale"]}</code>{" "}
              <button type="button" class="btn small" onClick={() => void copy(hsKind)} data-testid={hsId}>
                {t("mesh.copy")}
              </button>
            </li>
          </ul>
        </div>
      ))}
      <p class="hint">{t("mesh.noCredential.hint")}</p>
    </Banner>
  );
}

const DIFF_LINES = 400;

/** A unified diff with added and removed lines coloured; long diffs are clipped. */
export function DiffView({ diff, testid = "mesh-diff" }: { diff: string; testid?: string }): JSX.Element {
  const t = useT();
  const { text, clipped } = clipLines(diff, DIFF_LINES);
  return (
    <>
      <pre class="preview" data-testid={testid}>
        {text.split("\n").map((line, i) => (
          <div key={i} class={line.startsWith("+") && !line.startsWith("+++") ? "diff-add" : line.startsWith("-") && !line.startsWith("---") ? "diff-remove" : ""}>
            {line || " "}
          </div>
        ))}
      </pre>
      {clipped > 0 && <p class="hint">{t("mesh.diffClipped", { n: clipped })}</p>}
    </>
  );
}

/** What a policy plan changes, with warnings and, for a control plane that cannot be written remotely, the full text to install by hand. */
export function PlanView({ plan }: { plan: MeshPlan }): JSX.Element {
  const t = useT();
  return (
    <div data-testid="mesh-plan">
      {plan.warnings.length > 0 && (
        <Banner kind="warn" testid="mesh-plan-warnings">
          <ul>
            {plan.warnings.map((w, i) => (
              <li key={i}>{w}</li>
            ))}
          </ul>
        </Banner>
      )}
      {!plan.changed ? (
        <Banner testid="mesh-plan-unchanged">{t("mesh.plan.unchanged")}</Banner>
      ) : (
        <>
          <p class="hint">{t("mesh.plan.base", { version: plan.baseVersion || "—" })}</p>
          <DiffView diff={plan.diff} />
        </>
      )}
      {plan.changed && !plan.writable && (
        <Banner kind="warn" testid="mesh-plan-manual">
          <p>{t("mesh.plan.manual")}</p>
          <textarea class="editor" readOnly value={plan.after} aria-label={t("mesh.plan.manualText")} data-testid="mesh-plan-after" />
        </Banner>
      )}
    </div>
  );
}
