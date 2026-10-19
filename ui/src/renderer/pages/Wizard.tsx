import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Banner, ErrorBanner, Field, PageHead } from "../components";
import { useT } from "../i18n";
import { INITIAL_FORM, checkForm, type WizardForm } from "../lib/setup";
import type { ActionError, PickKind, SetupPreview, SetupReport } from "../../shared/ipc";
import type { Key } from "../messages";

const STEPS = ["wizard.step.mode", "wizard.step.where", "wizard.step.review", "wizard.step.install"] as const;

export function Wizard({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const [step, setStep] = useState(0);
  const [form, setForm] = useState<WizardForm>(INITIAL_FORM);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const [report, setReport] = useState<SetupReport | undefined>();
  const [preview, setPreview] = useState<SetupPreview | undefined>();
  const check = checkForm(form);
  const set = <K extends keyof WizardForm>(k: K, v: WizardForm[K]): void => setForm((f) => ({ ...f, [k]: v }));

  const problem = check.ok ? undefined : check.problem;
  const modeOk = problem !== "node_name" && problem !== "mode" && problem !== "roles";
  const whereOk = check.ok;

  const pick = async (kind: PickKind, field: "workspace" | "authkeyFile"): Promise<void> => {
    const r = await getApi().pickPath(kind);
    if (r.ok && r.data.path) set(field, r.data.path);
  };

  // The review shows the text the main process will write, so it asks for it before the step opens.
  const review = async (): Promise<void> => {
    if (!check.ok) return;
    const r = await getApi().previewSetup(check.params);
    if (!r.ok) return ctx.toast(r.error.message, true);
    setPreview(r.data);
    setStep(2);
  };

  const run = async (): Promise<void> => {
    if (!check.ok) return;
    // The config written here shows up as a stopped node, which would move the default page away from the wizard mid-run.
    ctx.go("setup");
    setBusy(true);
    setErr(undefined);
    const request = getApi().runSetup(check.params);
    if (form.authkeySource === "paste") set("authkey", "");
    let r;
    try { r = await request; }
    catch { setBusy(false); setErr({ code: "unavailable", message: t("wizard.failed", { path: preview?.configPath ?? "" }) }); setStep(3); return; }
    if (r.ok && form.authkeySource === "paste" && preview?.authkeyPath) {
      setForm((f) => ({ ...f, authkey: "", authkeySource: "file", authkeyFile: preview.authkeyPath! }));
    }
    setBusy(false);
    setStep(3);
    if (r.ok) setReport(r.data);
    else setErr(r.error);
  };

  // After a successful install and start the kernel shows up on its own.
  const connected = ctx.snapshot.connection.kind === "connected";
  const allOk = report !== undefined && report.steps.length === 3 && report.steps.every((s) => s.ok);
  useEffect(() => {
    if (allOk && connected) ctx.toast(t("wizard.connected"));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [allOk, connected]);

  return (
    <div class="page" data-testid="page-setup">
      <PageHead title={t("nav.setup")} sub={t("wizard.sub")} />
      <ol class="steps" aria-label={t("wizard.steps")}>
        {STEPS.map((k, i) => (
          <li key={k} aria-current={i === step ? "step" : undefined} class={i < step ? "done" : ""}>
            {i + 1}. {t(k)}
          </li>
        ))}
      </ol>

      {step === 0 && (
        <section class="card" data-testid="wizard-mode">
          <Field label={t("wizard.nodeName")} hint={t("wizard.nodeNameHint")}>
            <input type="text" value={form.nodeName} onInput={(e) => set("nodeName", (e.currentTarget as HTMLInputElement).value)} autocomplete="off" aria-invalid={form.nodeName !== "" && problem === "node_name"} data-testid="wizard-name" />
          </Field>
          <fieldset>
            <legend>{t("wizard.mode")}</legend>
            <label class="check">
              <input type="radio" name="mode" checked={form.mode === "lan"} onChange={() => set("mode", "lan")} data-testid="wizard-lan" />
              <span>
                <strong>{t("mode.lan")}</strong>
                <br />
                <span class="hint">{t("wizard.lanHint")}</span>
              </span>
            </label>
            <label class="check">
              <input type="radio" name="mode" checked={form.mode === "tailnet"} onChange={() => set("mode", "tailnet")} data-testid="wizard-tailnet" />
              <span>
                <strong>{t("mode.tailnet")}</strong>
                <br />
                <span class="hint">{t("wizard.tailnetHint")}</span>
              </span>
            </label>
          </fieldset>
          <label class="check"><input type="radio" name="mode" checked={form.mode === "coexist"} onChange={() => set("mode", "coexist")} data-testid="wizard-coexist"/><span><strong>{t("mode.coexist")}</strong><br/><span class="hint">{t("device.preferLan")}</span></span></label>
          <fieldset>
            <legend>{t("wizard.roles")}</legend>
            <label class="check">
              <input type="checkbox" checked={form.agent} onChange={(e) => set("agent", (e.currentTarget as HTMLInputElement).checked)} data-testid="wizard-agent" />
              <span>
                <strong>agent</strong> <span class="hint">{t("wizard.agentHint")}</span>
              </span>
            </label>
            <label class="check">
              <input type="checkbox" checked={form.gateway} onChange={(e) => set("gateway", (e.currentTarget as HTMLInputElement).checked)} data-testid="wizard-gateway" />
              <span>
                <strong>gateway</strong> <span class="hint">{t("wizard.gatewayHint")}</span>
              </span>
            </label>
            <p class="hint">{t("wizard.ingressLater")}</p>
          </fieldset>
          <div class="row end">
            <button type="button" class="btn primary" disabled={!modeOk} onClick={() => setStep(1)} data-testid="wizard-next">
              {t("common.next")}
            </button>
          </div>
        </section>
      )}

      {step === 1 && (
        <section class="card" data-testid="wizard-where">
          <Field label={t("wizard.workspace")} hint={form.agent ? t("wizard.workspaceHint") : t("wizard.workspaceUnused")}>
            <div class="row">
              <input type="text" value={form.workspace} onInput={(e) => set("workspace", (e.currentTarget as HTMLInputElement).value)} aria-invalid={form.workspace !== "" && problem === "workspace"} data-testid="wizard-workspace" />
              <button type="button" class="btn" onClick={() => void pick("workspace", "workspace")} data-testid="wizard-pick-workspace">
                {t("wizard.choose")}
              </button>
            </div>
          </Field>
          {form.mode !== "lan" && (
            <>
              <Field label={t("wizard.controlUrl")} hint={t("wizard.controlUrlHint")}>
                <input type="text" value={form.controlUrl} onInput={(e) => set("controlUrl", (e.currentTarget as HTMLInputElement).value)} aria-invalid={problem === "control_url"} data-testid="wizard-control-url" />
              </Field>
              <fieldset data-testid="wizard-authkey-source">
                <legend>{t("wizard.authkeySource")}</legend>
                <label class="check">
                  <input type="radio" name="authkeySource" checked={form.authkeySource === "browser"} onChange={() => set("authkeySource", "browser")} data-testid="wizard-authkey-browser" />
                  <span><strong>{t("wizard.browserLogin")}</strong><br /><span class="hint">{t("wizard.browserLoginHint")}</span></span>
                </label>
                <label class="check">
                  <input type="radio" name="authkeySource" checked={form.authkeySource === "auto"} onChange={() => set("authkeySource", "auto")} data-testid="wizard-authkey-auto" />
                  <span>
                    <strong>{t("wizard.authkeyAuto")}</strong>
                    <br />
                    <span class="hint">{t("wizard.authkeyAutoHint")}</span>
                  </span>
                </label>
                <label class="check">
                  <input type="radio" name="authkeySource" checked={form.authkeySource === "paste"} onChange={() => set("authkeySource", "paste")} data-testid="wizard-authkey-paste" />
                  <span>
                    <strong>{t("wizard.authkeyPaste")}</strong>
                    <br />
                    <span class="hint">{t("wizard.authkeyHint")}</span>
                  </span>
                </label>
                <label class="check">
                  <input type="radio" name="authkeySource" checked={form.authkeySource === "file"} onChange={() => set("authkeySource", "file")} data-testid="wizard-authkey-file" />
                  <span>
                    <strong>{t("wizard.authkeyFile")}</strong>
                    <br />
                    <span class="hint">{t("wizard.authkeyFileHint")}</span>
                  </span>
                </label>
              </fieldset>
              {form.authkeySource === "auto" && (
                <Field label={t("wizard.authkeyUser")} hint={t("wizard.authkeyUserHint")}>
                  <input type="text" value={form.authkeyUser} onInput={(e) => set("authkeyUser", (e.currentTarget as HTMLInputElement).value)} data-testid="wizard-authkey-user" />
                  <button type="button" class="btn" onClick={() => ctx.go("mesh")}>{t("nav.mesh")}</button>
                </Field>
              )}
              {form.authkeySource === "paste" && (
                <Field label={t("wizard.authkey")} hint={t("wizard.authkeyHint")}>
                  <input
                    type="password"
                    value={form.authkey}
                    onInput={(e) => set("authkey", (e.currentTarget as HTMLInputElement).value)}
                    aria-invalid={form.authkey !== "" && problem === "authkey"}
                    autocomplete="off"
                    spellcheck={false}
                    placeholder={t("wizard.authkeyPlaceholder")}
                    data-testid="wizard-authkey"
                  />
                </Field>
              )}
              {form.authkeySource === "file" && (
                <Field label={t("wizard.authkeyFile")} hint={t("wizard.authkeyFileHint")}>
                  <div class="row">
                    <input
                      type="text"
                      value={form.authkeyFile}
                      onInput={(e) => set("authkeyFile", (e.currentTarget as HTMLInputElement).value)}
                      aria-invalid={form.authkeyFile !== "" && problem === "authkey_file"}
                      autocomplete="off"
                      spellcheck={false}
                      data-testid="wizard-authkey-file-path"
                    />
                    <button type="button" class="btn" onClick={() => void pick("authkeyFile", "authkeyFile")} data-testid="wizard-authkey-pick">
                      {t("wizard.choose")}
                    </button>
                  </div>
                </Field>
              )}
            </>
          )}
          {!form.agent && form.mode === "lan" && <p>{t("wizard.nothingToAsk")}</p>}
          <div class="row end">
            <button type="button" class="btn" onClick={() => setStep(0)}>
              {t("common.back")}
            </button>
            <button type="button" class="btn primary" disabled={!whereOk} onClick={() => void review()} data-testid="wizard-next">
              {t("common.next")}
            </button>
          </div>
        </section>
      )}

      {step === 2 && preview && (
        <section class="card" data-testid="wizard-review">
          <p>{t("wizard.reviewIntro")}</p>
          {busy && <Banner testid="wizard-busy">{t(form.authkeySource === "browser" ? "wizard.browserWaiting" : "wizard.installing")}</Banner>}
          <pre class="preview mono" data-testid="wizard-preview">
            {preview.config}
          </pre>
          <Banner>
            <p>{t("wizard.reviewNote")}</p>
            <p>{t("wizard.denyFile", { path: preview.denyPath })}</p>
            {preview.authkeyPath !== undefined && <p data-testid="wizard-authkey-path">{t("wizard.authkeyWritesTo", { path: preview.authkeyPath })}</p>}
          </Banner>
          <div class="row end" style={{ marginTop: "10px" }}>
            <button type="button" class="btn" onClick={() => setStep(1)}>
              {t("common.back")}
            </button>
            <button type="button" class="btn primary" disabled={busy} onClick={() => void run()} data-testid="wizard-run">
              {t("wizard.install")}
            </button>
          </div>
        </section>
      )}

      {step === 3 && (
        <section class="card" data-testid="wizard-result">
          {err && <ErrorBanner error={err} />}
          {report && (
            <>
              <ul class="checklist">
                {report.steps.map((s) => (
                  <li key={s.id} data-testid={`wizard-step-${s.id}`}>
                    <span aria-hidden="true">{s.ok ? "✓" : "✗"}</span>
                    <span>
                      {t(`wizard.do.${s.id}` as Key)}
                      {s.detail && <span class="hint"> — {s.detail}</span>}
                    </span>
                  </li>
                ))}
              </ul>
              {allOk ? (
                <Banner kind={connected ? "ok" : ""} testid="wizard-done">
                  {connected ? t("wizard.connected") : t("wizard.waiting")}
                </Banner>
              ) : (
                <Banner kind="danger" testid="wizard-failed">
                  {t("wizard.failed", { path: report.configPath })}
                </Banner>
              )}
            </>
          )}
          <div class="row end" style={{ marginTop: "10px" }}>
            {(err || (report && !allOk)) && (
              <button type="button" class="btn" onClick={() => { setReport(undefined); setErr(undefined); setStep(0); }}>
                {t("common.back")}
              </button>
            )}
            {allOk && connected && (
              <button type="button" class="btn primary" onClick={() => ctx.go("overview")} data-testid="wizard-finish">
                {t("wizard.finish")}
              </button>
            )}
          </div>
        </section>
      )}
    </div>
  );
}
