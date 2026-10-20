import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm } from "../components";
import { useT } from "../i18n";
import { KERNEL_OPERATIONS, type KernelNode, type KernelOperation } from "../../shared/kernel";

export function KernelManager({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const [nodes, setNodes] = useState<KernelNode[]>([]);
  const [chosen, setChosen] = useState("");
  const [busy, setBusy] = useState<KernelOperation | "list" | "">("list");
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState<{ op: KernelOperation; node: string; name: string }>();
  const selected = nodes.find((n) => n.id === chosen) ?? nodes.find((n) => n.id === ctx.snapshot.selectedInstance) ?? nodes[0];
  const updating = ctx.snapshot.update.phase === "downloading" || ctx.snapshot.update.phase === "installing";

  const refresh = async (): Promise<void> => {
    setBusy("list");
    try {
      const r = await getApi().kernel({ op: "list" });
      if (r.ok) { setNodes(r.data.nodes); setError(""); }
      else setError(r.error.message);
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(""); }
  };
  useEffect(() => { void refresh(); }, []);

  const run = async (op: KernelOperation, node: string): Promise<void> => {
    setConfirm(undefined);
    setBusy(op);
    setError("");
    try {
      const r = await getApi().kernel({ op, node });
      if (r.ok) { setNodes(r.data.nodes); ctx.toast(t("kernel.done", { action: t(`kernel.${op}`) })); }
      else setError(r.error.message);
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(""); }
  };

  return <section class="card" data-testid="kernel-manager" aria-busy={busy !== ""}>
    <h2>{t("kernel.title")}</h2>
    <p class="hint">{t("kernel.sub")}</p>
    {nodes.length === 0 && busy !== "list" ? <>
      <p>{t("kernel.empty")}</p>
      <button class="btn" type="button" onClick={() => ctx.go("setup")}>{t("kernel.setup")}</button>
    </> : selected && <>
      <div class="row">
        <label>{t("kernel.node")} <select value={selected.id} disabled={!!busy} onChange={(e) => setChosen(e.currentTarget.value)} data-testid="kernel-node">
          {nodes.map((n) => <option key={n.id} value={n.id}>{n.name}</option>)}
        </select></label>
        <Badge kind={selected.running ? "ok" : ""}>{t(selected.running ? "kernel.running" : "kernel.stopped")}</Badge>
      </div>
      {!selected.managed && <Banner kind="warn">{t("kernel.admin")}</Banner>}
      <div class="row" style={{ flexWrap: "wrap" }}>
        {KERNEL_OPERATIONS.map((op) => <button key={op} type="button" class={`btn ${op === "uninstall" ? "danger" : ""}`}
          data-testid={`kernel-op-${op}`} disabled={updating || !!busy || !selected.managed || ((op === "install" || op === "start") && selected.running) || (op === "stop" && !selected.running)}
          onClick={() => op === "install" || op === "start" ? void run(op, selected.id) : setConfirm({ op, node: selected.id, name: selected.name })}>{t(`kernel.${op}`)}</button>)}
      </div>
    </>}
    <div class="row">
      <button class="btn" type="button" disabled={!!busy} onClick={() => void refresh()} data-testid="kernel-refresh">{t("kernel.refresh")}</button>
      {busy && <span role="status">{busy === "list" ? t("common.loading") : t("kernel.working", { action: t(`kernel.${busy}`) })}</span>}
    </div>
    {error && <Banner kind="danger" testid="kernel-operation-error">{t("kernel.error", { detail: error })}</Banner>}
    {confirm && <Confirm title={t("kernel.confirm", { name: confirm.name, action: t(`kernel.${confirm.op}`) })}
      body={<><p>{t("kernel.interrupt")}</p><p>{t("kernel.keep")}</p>{confirm.op === "reinstall" && <p>{t("kernel.reinstallHint")}</p>}</>}
      confirmLabel={t(`kernel.${confirm.op}`)} onCancel={() => setConfirm(undefined)} onConfirm={() => void run(confirm.op, confirm.node)} />}
  </section>;
}
