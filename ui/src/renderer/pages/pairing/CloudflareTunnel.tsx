import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Banner, Confirm, Field } from "../../components";
import { useCopy } from "../../design-copy";
import { useResource } from "../../hooks";
import { useCloudflareLogin } from "./cloudflare-login";
import type { ActionError } from "../../../shared/ipc";
import { parseMeshRequest } from "../../../shared/mesh";
import type { CloudflareSetupResult } from "../../../shared/mesh";

type Step = "name" | "plan" | "login";

/**
 * First-time Cloudflare Tunnel setup for this device. The work runs in the xmesh CLI with the API token
 * saved in this machine's keychain: it creates the tunnel, DNS name and Access app, and writes the
 * tunnel credentials next to the node's config file. The panel only ever sees the plan and the hostname.
 */
export function CloudflareTunnel({ ctx, defaultName }: { ctx: PageContext; defaultName: string }): JSX.Element | null {
  const copy = useCopy();
  const status = useResource(() => getApi().mesh({ op: "cloudflare.status" }), [ctx.snapshot.selectedInstance]);
  const [step, setStep] = useState<Step>();
  const [name, setName] = useState(defaultName.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 63));
  const [plan, setPlan] = useState<CloudflareSetupResult>();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError>();
  const [removing, setRemoving] = useState<string>();
  const cf = useCloudflareLogin();
  if (!ctx.can("pair")) return null;
  const res = status.resource;
  const data = res.state === "ready" ? res.data : res.state === "error" ? res.stale : undefined;
  const devices = data && "devices" in data ? data.devices : [];
  const configured = data && "configuredHostname" in data ? data.configuredHostname : "";

  const close = (): void => { setStep(undefined); setPlan(undefined); setErr(undefined); cf.reset(); };
  const nameRequest = parseMeshRequest({ op: "cloudflare.setup", name, apply: false });

  // A rejected call (no bridge, IPC failure) must not leave the buttons disabled.
  const guarded = async (work: () => Promise<void>): Promise<void> => {
    setBusy(true);
    setErr(undefined);
    try {
      await work();
    } catch (e) {
      setErr({ code: "unavailable", message: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  const preview = (): Promise<void> => guarded(async () => {
    if (!nameRequest.ok) return;
    const r = await getApi().mesh(nameRequest.value);
    if (r.ok) { setPlan(r.data as CloudflareSetupResult); setStep("plan"); return; }
    if (r.error.code === "no_credential") { setStep("login"); return; }
    setErr(r.error);
  });

  const apply = (): Promise<void> => guarded(async () => {
    const r = await getApi().mesh({ op: "cloudflare.setup", name, apply: true });
    if (!r.ok) { setErr(r.error); return; }
    close();
    status.reload();
    const note = r.data.applied === false ? "" : (r.data as CloudflareSetupResult).cloudflaredNote ?? "";
    ctx.toast(copy("隧道已创建。重启 xmesh 后生效。", "Tunnel created. Restart xmesh to use it.") + (note === "" ? "" : ` ${note}`));
  });

  const saveLogin = async (): Promise<void> => {
    let saved = false;
    await guarded(async () => {
      if (!cf.request) return;
      const r = await getApi().mesh(cf.request);
      if (!r.ok) { setErr(r.error); return; }
      saved = true;
    });
    if (!saved) return;
    cf.reset();
    setStep("name");
    await preview();
  };

  const remove = (): Promise<void> => guarded(async () => {
    if (!removing) return;
    const r = await getApi().mesh({ op: "cloudflare.remove", name: removing });
    if (!r.ok) { setErr(r.error); return; }
    setRemoving(undefined);
    status.reload();
    ctx.toast(copy("已删除该设备在 Cloudflare 上的资源。", "The device's Cloudflare resources were deleted."));
  });

  return (
    <section class="card" data-testid="cloudflare-tunnel">
      <h3>Cloudflare Tunnel</h3>
      <p class="hint">{copy(
        "本机没有公网地址时，让 xmesh 通过 Cloudflare 为本机建一条隧道，别的设备就能经 Cloudflare 连到本机。没有安装 cloudflared 时 xmesh 会自动下载官方版本。令牌存在系统钥匙串里，没有钥匙串的主机会存到私有文件。",
        "If this device has no public address, xmesh can create a Cloudflare tunnel for it so other devices reach it through Cloudflare. xmesh downloads the official cloudflared when it is missing. The token stays in the system keychain, or in a private file on hosts without one.",
      )}</p>
      {configured !== "" && (
        <p class="hint" data-testid="cloudflare-configured">{copy("本机使用的隧道主机名：", "This device's tunnel hostname: ")}<span class="mono">{configured}</span>{copy("（修改后需重启 xmesh 生效）", " (restart xmesh after changes)")}</p>
      )}
      {devices.length > 0 && (
        <ul class="plain-list">
          {devices.map((d) => (
            <li key={d.name} data-testid={`cloudflare-device-${d.name}`}>
              <strong>{d.name}</strong> <span class="mono">{d.hostname}</span>{" "}
              <button type="button" class="btn danger" disabled={busy} onClick={() => { setErr(undefined); setRemoving(d.name); }} data-testid={`cloudflare-remove-${d.name}`}>{copy("删除", "Delete")}</button>
            </li>
          ))}
        </ul>
      )}
      <div class="row">
        <button type="button" class="btn" disabled={busy} onClick={() => { setErr(undefined); setStep("name"); }} data-testid="cloudflare-setup">{copy("为本机创建隧道", "Create a tunnel for this device")}</button>
      </div>
      {err && !step && !removing && <Banner kind="danger" testid="cloudflare-error">{err.message}</Banner>}
      {step === "name" && (
        <Confirm
          title={copy("为本机创建 Cloudflare 隧道", "Create a Cloudflare tunnel for this device")}
          confirmLabel={copy("预览", "Preview")}
          body={<>
            <Field label={copy("设备名", "Device name")} hint={copy("用于主机名和资源名：字母、数字和连字符。", "Used in hostnames and resource names: letters, digits and hyphens.")}>
              <input value={name} onInput={(e) => setName(e.currentTarget.value.trim())} data-testid="cloudflare-name" />
            </Field>
            {err && <Banner kind="danger" testid="cloudflare-error">{err.message}</Banner>}
          </>}
          busy={busy || !nameRequest.ok}
          onCancel={close}
          onConfirm={() => void preview()}
        />
      )}
      {step === "plan" && plan && (
        <Confirm
          title={copy("确认创建？", "Create these?")}
          confirmLabel={copy("创建", "Create")}
          body={<>
            <p>{copy(`将为 ${name} 在 ${plan.domain ?? ""} 下创建：`, `Will create for ${name} under ${plan.domain ?? ""}:`)}</p>
            <ul>{plan.creates.map((c) => <li key={c}>{c}</li>)}</ul>
            <p class="hint">{copy("隧道令牌与 Access 密钥写入配置文件旁的 cloudflare 目录（仅本人可读），并更新配置。", "The tunnel token and Access secret are written, owner-only, to a cloudflare directory next to the config file, and the config is updated.")}</p>
            {err && <Banner kind="danger" testid="cloudflare-error">{err.message}</Banner>}
          </>}
          busy={busy}
          onCancel={close}
          onConfirm={() => void apply()}
        />
      )}
      {step === "login" && (
        <Confirm
          title={copy("先保存 Cloudflare 令牌", "Save a Cloudflare token first")}
          confirmLabel={copy("保存并预览", "Save and preview")}
          body={<>
            <p class="hint">{copy("本机还没有保存 Cloudflare 令牌。", "No Cloudflare token is saved on this device.")}</p>
            {cf.fields}
            {err && <Banner kind="danger" testid="cloudflare-error">{err.message}</Banner>}
          </>}
          busy={busy || !cf.request}
          onCancel={close}
          onConfirm={() => void saveLogin()}
        />
      )}
      {removing && (
        <Confirm
          danger
          title={copy("删除隧道资源？", "Delete the tunnel resources?")}
          confirmLabel={copy("删除", "Delete")}
          body={<>
            <p>{copy(`会删除 ${removing} 在 Cloudflare 上的隧道、DNS 记录和 Access 应用；这是最后一台时还会删除网格服务令牌。如果删除的正是本机在用的隧道，还会清掉配置文件里的 mesh.cloudflare 与对应的密钥文件，重启 xmesh 后生效。`, `Deletes the tunnel, DNS record and Access app of ${removing}; the mesh service token goes too if it is the last device. If it is the tunnel this device runs, mesh.cloudflare and its secret files are removed too; restart xmesh for it to take effect.`)}</p>
            {err && <Banner kind="danger" testid="cloudflare-error">{err.message}</Banner>}
          </>}
          busy={busy}
          onCancel={() => { setRemoving(undefined); setErr(undefined); }}
          onConfirm={() => void remove()}
        />
      )}
    </section>
  );
}
