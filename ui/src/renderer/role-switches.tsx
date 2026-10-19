import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "./app";
import { getApi } from "./api";
import { Confirm } from "./components";
import { useResource } from "./hooks";
import { errorText, useT } from "./i18n";
import { applyConfigEdits } from "./lib/apply-config";
import { applySaved } from "./lib/reload";
import { formFromConfig, ROLES, toggleRole, type Role } from "./lib/settings";
import type { ConfigPatchEdit } from "../shared/ipc";
import type { Key } from "./messages";

const HINT: Record<Role, Key> = { agent: "wizard.agentHint", gateway: "wizard.gatewayHint", ingress: "settings.ingressHint" };
interface Review { etag: string; edits: ConfigPatchEdit[]; roles: Role[]; restart: boolean }

export function RoleSwitches({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  const connection = ctx.snapshot.connection;
  const supported = ctx.can("config.read");
  const liveRoles: readonly Role[] = connection.kind === "connected" ? connection.status.roles : [];
  const restartPending = connection.kind === "connected" && connection.status.restart_required;
  const { resource, reload } = useResource(() => getApi().getConfig(), [ctx.snapshot.selectedInstance, supported, ctx.configRevision, ctx.configDirty, restartPending, liveRoles.join(",")]);
  const [busy, setBusy] = useState(false);
  const [review, setReview] = useState<Review>();
  if (connection.kind !== "connected") return null;
  const configured: readonly Role[] = resource.state === "ready" ? formFromConfig(resource.data.parsed).roles : liveRoles;

  const prepare = async (role: Role): Promise<void> => {
    if (busy || ctx.configDirty) return;
    setBusy(true);
    try {
      const current = await getApi().getConfig();
      if (!current.ok) { ctx.toast(errorText(t, current.error), true); return; }
      if (!current.data.valid) { ctx.go("config", { configView: "source" }); return; }
      const form = formFromConfig(current.data.parsed);
      const roles = toggleRole(form.roles, role, !form.roles.includes(role));
      if (roles.length === 0) { ctx.toast(t("settings.problem.roles"), true); return; }
      if (role === "ingress" && roles.includes("ingress") && (form.mode !== "tailnet" || !form.issuer)) {
        ctx.go("settings", { section: "roles", role: "ingress" });
        return;
      }
      const edits: ConfigPatchEdit[] = [{ path: "/node/roles", value: roles }];
      const parsed = current.data.parsed as { gateway?: { listen?: string } } | null;
      if (roles.includes("gateway") && !parsed?.gateway?.listen) edits.push({ path: "/gateway/listen", value: "127.0.0.1:8080" });
      const result = await applyConfigEdits(current.data.etag, edits, true);
      if (!result.ok) { ctx.toast(errorText(t, result.error), true); reload(); return; }
      setReview({ etag: current.data.etag, edits, roles, restart: result.data.restart_required });
    } finally { setBusy(false); }
  };
  const save = async (): Promise<void> => {
    if (!review || busy) return;
    setBusy(true);
    try {
      const result = await applyConfigEdits(review.etag, review.edits);
      setReview(undefined);
      if (!result.ok) { ctx.toast(errorText(t, result.error), true); reload(); return; }
      ctx.applied(await applySaved(result.data.restart_required));
      ctx.toast(t("config.saved"));
      ctx.refreshConfig();
      reload();
    } finally { setBusy(false); }
  };
  return <>
    <div class="role-capsules" role="group" aria-label={t("wizard.roles")} aria-busy={busy} data-testid="role-switches">
      {ROLES.map((role) => {
        const enabled = configured.includes(role), running = liveRoles.includes(role);
        return <button key={role} type="button" class="role-capsule" role="switch" aria-checked={enabled} data-pending={enabled !== running} disabled={busy || review !== undefined || ctx.configDirty || !ctx.can("config.write") || !supported || resource.state !== "ready" || (enabled && configured.length === 1)} title={ctx.configDirty ? t("topology.configDirty") : `${t(HINT[role])}${enabled !== running ? ` · ${t("roles.pending")}` : ""}`} onClick={() => void prepare(role)} data-testid={`role-${role}`}>
          <span class="capsule-dot" aria-hidden="true" />{role}
        </button>;
      })}
    </div>
    {review && <Confirm title={t("roles.reviewTitle")} body={<><p>{review.roles.join(" · ")}</p>{review.restart && <p class="hint">{t("apply.restart")}</p>}</>} confirmLabel={t("roles.review")} busy={busy} onCancel={() => setReview(undefined)} onConfirm={() => void save()} />}
  </>;
}
