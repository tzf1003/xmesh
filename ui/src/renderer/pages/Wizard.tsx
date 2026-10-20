import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Field, PageHead } from "../components";
import { useQuickCopy } from "../onboarding-copy";
import type { ActionError } from "../../shared/ipc";
import { validIngressPassword, verificationPrompt, parseOnboardingInput, onboardingDiagnostics, type OnboardingInput, type OnboardingInspect, type OnboardingJob, type OnboardingPlan, type Purpose, type ClientId } from "../../shared/onboarding";

const initialInput: OnboardingInput = { schema_version: 1, purpose: "local", node_name: "", workspace: "", permission: "development", clients: [], autostart: true, network: { mode: "lan" } };
const inProgress = (job: OnboardingJob): boolean => !["succeeded", "cancelled"].includes(job.status);
export function Wizard({ ctx }: { ctx: PageContext }): JSX.Element {
  const copy = useQuickCopy();
  const [step, setStep] = useState(0);
  const [input, setInput] = useState<OnboardingInput>(initialInput);
  const [inspection, setInspection] = useState<OnboardingInspect>();
  const [plan, setPlan] = useState<OnboardingPlan>();
  const [job, setJob] = useState<OnboardingJob>();
  const [error, setError] = useState<ActionError>();
  const [busy, setBusy] = useState(false);
  const [diagnostics, setDiagnostics] = useState("");
  const [password, setPassword] = useState("");
  const [passwordConfirm, setPasswordConfirm] = useState("");
  const [replaceConfirmed, setReplaceConfirmed] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const generation = useRef(0);
  const set = <K extends keyof OnboardingInput>(key: K, value: OnboardingInput[K]): void => { setInput((v) => ({ ...v, [key]: value })); setPlan(undefined); setReplaceConfirmed(false); setError(undefined); };
  const inspect = async (): Promise<void> => {
    setBusy(true);
    try {
      const result = await getApi().onboarding({ op: "inspect" });
      if (result.ok) { setInspection(result.data); setInput((v) => ({ ...v, node_name: v.node_name || result.data.suggested_node_name, clients: loaded ? v.clients : result.data.clients.filter((client) => client.installed).map((client) => client.id).filter((id): id is ClientId => id === "codex" || id === "claude-code") })); }
      else setError(result.error);
    } catch { setError({ code: "unavailable", message: copy.inspectFailure }); }
    setLoaded(true); setBusy(false);
  };
  useEffect(() => { void inspect(); }, []);
  useEffect(() => {
    if (step !== 2 || !parseOnboardingInput(input)) return;
    const current = ++generation.current;
    setBusy(true); setError(undefined); setPlan(undefined); setReplaceConfirmed(false);
    void getApi().onboarding({ op: "plan", input }).then((r) => {
      if (current !== generation.current) return;
      if (r.ok) setPlan(r.data); else setError(r.error);
    }).catch(() => { if (current === generation.current) setError({ code: "unavailable", message: copy.reviewFailure }); }).finally(() => { if (current === generation.current) setBusy(false); });
    return () => { generation.current++; };
  }, [step, input]);
  useEffect(() => {
    if (!job || !["pending", "running", "waiting_user"].includes(job.status)) return;
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async (): Promise<void> => {
      try { const result = await getApi().onboarding({ op: "status", job: job.id }); if (active) { if (result.ok) { setJob(result.data); setError(undefined); } else setError(result.error); } }
      catch { if (active) setError({ code: "unavailable", message: copy.errorGeneric }); }
      if (active) timer = setTimeout(() => void poll(), 1500);
    };
    timer = setTimeout(() => void poll(), 500);
    return () => { active = false; clearTimeout(timer); };
  }, [job?.id, job?.status]);
  const choosePurpose = (purpose: Purpose): void => setInput((v) => ({ ...v, purpose, network: purpose === "local" ? { mode: "lan" } : { mode: "tailnet", auth_method: "browser", control_url: "https://controlplane.tailscale.com" } }));
  const runJob = async (op: "apply" | "resume" | "verify" | "cancel", existing?: OnboardingJob, openAuth = false): Promise<void> => {
    if (busy || op === "apply" && !plan) return;
    setBusy(true); setError(undefined); ctx.go("setup");
    try {
      const r = op === "apply" ? await getApi().onboarding({ op, plan: plan!.id }) : await getApi().onboarding({ op, job: (existing || job)!.id, ...(openAuth && { open_auth: true }) });
      if (r.ok) { setJob(r.data); setStep(3); } else setError(r.error);
    } catch { setError({ code: "unavailable", message: copy.errorGeneric }); }
    setBusy(false);
  };
  const openJob = (existing: OnboardingJob): void => { setJob(existing); setStep(3); setError(undefined); };
  const pick = async (kind: "workspace" | "authkeyFile"): Promise<void> => {
    const r = await getApi().pickPath(kind);
    if (r.ok && r.data.path) { if (kind === "workspace") set("workspace", r.data.path); else set("network", { ...input.network, authkey_file: r.data.path }); }
  };
  const copyDiagnostics = async (): Promise<void> => {
    try {
      const r = await getApi().onboarding({ op: "doctor", copy: true });
      const text = onboardingDiagnostics({ job, error, doctor: r });
      setDiagnostics(text);
      if (r.ok) ctx.toast(copy.copied);
    } catch { setDiagnostics(onboardingDiagnostics({ job, error })); }
  };
  const savePassword = async (): Promise<void> => {
    if (!job || busy || !validIngressPassword(password) || password !== passwordConfirm) return;
    const secret = password;
    setPassword(""); setPasswordConfirm(""); setBusy(true); setError(undefined);
    try { const result = await getApi().onboarding({ op: "set_password", job: job.id, password: secret }); if (result.ok) setJob(result.data); else setError(result.error); }
    catch { setError({ code: "unavailable", message: copy.errorGeneric }); }
    setBusy(false);
  };
  const valid = parseOnboardingInput(input) !== undefined;
  const titles = [copy.purpose, copy.basics, copy.review, copy.progress];
  const title = job?.status === "cancelled" ? copy.cancelled : job?.status === "failed" ? copy.failed : job?.status === "succeeded" ? job.readiness.client_verified ? copy.done : copy.ready : job?.status === "waiting_user" ? copy.waiting : copy.running;
  const verification = job ? verificationPrompt(job) || copy.fallbackPrompt : copy.fallbackPrompt;
  return <div class="page quick-setup" data-testid="page-setup">
    <PageHead title={copy.title} sub={copy.sub}><button class="btn small" onClick={() => ctx.go("features")}>{copy.openFeatures}</button></PageHead>
    <ol class="steps" aria-label={copy.title}>{titles.map((label, i) => <li key={label} aria-current={step === i ? "step" : undefined} class={step > i ? "done" : ""}>{i + 1}. {label}</li>)}</ol>
    {error && <Banner kind="danger" testid="setup-error"><strong>{error.message}</strong><p>{copy.impact}</p><p>{copy.nextAction}: {error.next_action || copy.retry}</p><div class="row"><button class="btn small" onClick={() => job ? void runJob("resume") : void inspect()}>{copy.refresh}</button><button class="btn small" onClick={() => void copyDiagnostics()}>{copy.copy}</button></div></Banner>}
    {step === 0 && <>
      {(inspection?.jobs ?? []).filter(inProgress).length > 0 && <section class="card" data-testid="setup-existing"><h2>{copy.existing}</h2>{inspection!.jobs.filter(inProgress).map((j) => <div class="row quick-job" key={j.id}><span class="grow"><strong>{j.node_name}</strong><span class="hint"> · {j.id}</span></span><Badge kind={j.status === "failed" ? "warn" : ""}>{j.status}</Badge><button class="btn" data-testid={`resume-${j.id}`} onClick={() => openJob(j)}>{copy.resume}</button></div>)}</section>}
      <section class="purpose-grid" aria-label={copy.purpose} data-testid="wizard-purpose">{([{ id: "local", title: copy.local, sub: copy.localSub, glyph: "⌘" }, { id: "multi-device", title: copy.multi, sub: copy.multiSub, glyph: "⇄" }, { id: "cloud", title: copy.cloud, sub: copy.cloudSub, glyph: "☁" }] as const).map((p) => <button type="button" class={`purpose-card ${input.purpose === p.id ? "selected" : ""}`} aria-pressed={input.purpose === p.id} onClick={() => choosePurpose(p.id)} data-testid={`purpose-${p.id}`} key={p.id}><span class="purpose-glyph" aria-hidden="true">{p.glyph}</span><strong>{p.title}</strong><span>{p.sub}</span></button>)}</section>
      <div class="row end"><button class="btn primary" disabled={!loaded || busy} data-testid="wizard-next" onClick={() => setStep(1)}>{copy.next}</button></div>
    </>}
    {step === 1 && <section class="card" data-testid="wizard-basics">
      <Field label={copy.name} hint={copy.nameHint}><input data-testid="wizard-name" value={input.node_name} onInput={(e) => set("node_name", e.currentTarget.value)} autocomplete="off" /></Field>
      <Field label={copy.workspace}><div class="row"><input data-testid="wizard-workspace" value={input.workspace} onInput={(e) => set("workspace", e.currentTarget.value)} spellcheck={false}/><button class="btn" data-testid="wizard-pick-workspace" onClick={() => void pick("workspace")}>{copy.choose}</button></div></Field>
      <fieldset><legend>{copy.permission}</legend><label class="check"><input type="radio" name="permission" checked={input.permission === "development"} onChange={() => set("permission", "development")} data-testid="permission-development"/><span><strong>{copy.development}</strong><br/><span class="hint">{copy.developmentHint}</span></span></label><label class="check"><input type="radio" name="permission" checked={input.permission === "readonly"} onChange={() => set("permission", "readonly")} data-testid="permission-readonly"/><span><strong>{copy.readonly}</strong><br/><span class="hint">{copy.readonlyHint}</span></span></label><p class="hint">{copy.cwd}</p></fieldset>
      <label class="check"><input type="checkbox" checked={input.autostart} onChange={(e) => set("autostart", e.currentTarget.checked)} data-testid="wizard-autostart"/>{copy.autostart}</label>
      {input.purpose !== "local" && <fieldset><legend>{copy.network}</legend>{input.purpose === "multi-device" && <div class="row"><label class="check"><input type="radio" name="network" checked={input.network.mode === "lan"} onChange={() => set("network", { mode: "lan" })} data-testid="wizard-lan"/>{copy.lan}</label><label class="check"><input type="radio" name="network" checked={input.network.mode === "tailnet"} onChange={() => set("network", { mode: "tailnet", auth_method: "browser", control_url: "https://controlplane.tailscale.com" })} data-testid="wizard-tailnet"/>{copy.tailnet}</label><label class="check"><input type="radio" name="network" checked={input.network.mode === "coexist"} onChange={() => set("network", { mode: "coexist", auth_method: "browser", control_url: "https://controlplane.tailscale.com" })} data-testid="wizard-coexist"/>{copy.lan} + {copy.tailnet}</label></div>}
        {input.network.mode !== "lan" && <><Field label={copy.control}><input type="url" value={input.network.control_url || ""} onInput={(e) => set("network", { ...input.network, control_url: e.currentTarget.value })} data-testid="wizard-control-url"/></Field><label class="check"><input type="radio" name="auth" checked={input.network.auth_method === "browser"} onChange={() => set("network", { ...input.network, auth_method: "browser", authkey_file: undefined })}/>{copy.browser}</label><label class="check"><input type="radio" name="auth" checked={input.network.auth_method === "key"} onChange={() => set("network", { ...input.network, auth_method: "key" })}/>{copy.key}</label>{input.network.auth_method === "key" && <Field label={copy.keyPath}><div class="row"><input value={input.network.authkey_file || ""} onInput={(e) => set("network", { ...input.network, authkey_file: e.currentTarget.value })} data-testid="wizard-authkey-file-path"/><button class="btn" onClick={() => void pick("authkeyFile")}>{copy.choose}</button></div></Field>}</>}
      </fieldset>}
      {!valid && input.workspace !== "" && <p class="hint">{copy.invalid}</p>}
      <div class="row end"><button class="btn" onClick={() => setStep(0)}>{copy.back}</button><button class="btn primary" disabled={!valid} data-testid="wizard-next" onClick={() => setStep(2)}>{copy.next}</button></div>
    </section>}
    {step === 2 && <section class="card" data-testid="wizard-review"><h2>{copy.clientTitle}</h2><p class="hint">{copy.clientHint}</p>
      <div class="quick-clients">{(["codex", "claude-code"] as ClientId[]).map((id) => { const detected = inspection?.clients.find((client) => client.id === id); return <label class="check" key={id}><input type="checkbox" checked={input.clients.includes(id)} onChange={(e) => set("clients", e.currentTarget.checked ? [...input.clients, id] : input.clients.filter((value) => value !== id))} data-testid={`client-${id}`}/><span><strong>{id === "codex" ? "Codex" : "Claude Code"}</strong><br/><span class="hint">{detected?.installed ? copy.installed : copy.available}{detected?.version ? ` · ${detected.version}` : ""}</span></span></label>; })}</div>
      <label class="check"><input type="checkbox" checked={input.clients.length === 0} onChange={(e) => { if (e.currentTarget.checked) set("clients", []); }} data-testid="clients-defer"/><span><strong>{copy.defer}</strong><br/><span class="hint">{copy.deferHint}</span></span></label>
      {busy && <p role="status">{copy.preparing}</p>}
      {plan && <div data-testid="setup-plan"><h2>{copy.summary}</h2><dl class="quick-summary"><dt>{copy.purpose}</dt><dd>{input.purpose === "local" ? copy.local : input.purpose === "cloud" ? copy.cloud : copy.multi}</dd><dt>{copy.name}</dt><dd>{input.node_name}</dd><dt>{copy.workspace}</dt><dd class="mono">{input.workspace}</dd><dt>{copy.permission}</dt><dd>{input.permission === "readonly" ? copy.readonly : copy.development}</dd><dt>{copy.endpoint}</dt><dd class="mono">{plan.endpoint}</dd><dt>{copy.configPath}</dt><dd class="mono">{plan.config_path}</dd></dl><ul>{plan.summary.map((line) => <li key={line}>{line}</li>)}</ul>{plan.client_changes.map((change) => <p class={change.conflict ? "quick-conflict" : "hint"} key={change.id}>{change.id}: {change.summary}<br/><span class="mono">{change.path}</span></p>)}{plan.client_changes.some((change) => change.conflict) && <Banner kind="warn"><p>{copy.conflict}</p><label class="check"><input type="checkbox" checked={replaceConfirmed} onChange={(e) => setReplaceConfirmed(e.currentTarget.checked)} data-testid="setup-replace-client"/>{copy.replaceClient}</label></Banner>}<details class="quick-yaml"><summary>{copy.yaml}</summary><pre class="preview mono" data-testid="wizard-preview">{plan.config}</pre></details><p class="hint">{copy.reviewHint}</p></div>}
      <div class="row end"><button class="btn" disabled={busy} onClick={() => setStep(1)}>{copy.back}</button><button class="btn primary" disabled={busy || !plan || plan.client_changes.some((change) => change.conflict) && !replaceConfirmed} onClick={() => void runJob("apply")} data-testid="wizard-run">{copy.apply}</button></div>
    </section>}
    {step === 3 && job && <section class="card" data-testid="wizard-result" aria-live="polite"><div class="row"><h2 class="grow">{title}</h2><Badge kind={job.status === "succeeded" ? "ok" : job.status === "failed" ? "danger" : "warn"}>{job.status}</Badge></div><p class="hint">{copy.taskId}: <span class="mono" data-testid="setup-job-id">{job.id}</span></p><p class="hint">{copy.jobSaved}</p>
      {job.status !== "cancelled" && <div class="quick-readiness" data-testid="setup-readiness"><Badge kind={job.readiness.service_ready ? "ok" : ""}>{job.readiness.service_ready ? copy.ready : copy.servicePending}</Badge><Badge kind={job.readiness.client_verified ? "ok" : "warn"}>{job.readiness.client_verified ? copy.verified : copy.pending}</Badge></div>}
      <ol class="checklist">{job.steps.map((s) => <li key={s.id} data-testid={`wizard-step-${s.id}`}><span aria-hidden="true">{["succeeded", "done"].includes(s.status) ? "✓" : s.status === "failed" ? "!" : "○"}</span><div>{s.message || s.id}{s.next_action && <p class="hint">{copy.nextAction}: {s.next_action}</p>}</div></li>)}</ol>
      {job.ingress_url && <Field label={copy.endpoint}><input class="mono" readOnly value={job.ingress_url} onFocus={(e) => e.currentTarget.select()} data-testid="setup-ingress-url"/></Field>}
      {job.next_action && <Banner kind={job.status === "failed" ? "danger" : ""}><p>{job.next_action}</p>{job.status === "failed" && <p>{copy.impact}</p>}</Banner>}
      {job.auth_url && <div class="quick-action"><p>{copy.authHint}</p><button class="btn primary" disabled={busy} data-testid="setup-open-auth" onClick={() => void runJob("resume", undefined, true)}>{copy.auth}</button></div>}
      {job.purpose === "cloud" && job.readiness.service_ready && !job.ingress_password_set && <div class="quick-action" data-testid="setup-ingress-password"><h2>{copy.ingressPassword}</h2><p class="hint">{copy.ingressPasswordHint}</p><Field label={copy.ingressPassword}><input type="password" value={password} autocomplete="new-password" onInput={(e) => setPassword(e.currentTarget.value)} data-testid="setup-password"/></Field><Field label={copy.confirmPassword}><input type="password" value={passwordConfirm} autocomplete="new-password" onInput={(e) => setPasswordConfirm(e.currentTarget.value)} data-testid="setup-password-confirm"/></Field><button class="btn primary" disabled={busy || !validIngressPassword(password) || password !== passwordConfirm} onClick={() => void savePassword()} data-testid="setup-save-password">{copy.savePassword}</button></div>}
      {job.readiness.service_ready && !job.readiness.client_verified && (job.purpose !== "cloud" || job.ingress_password_set) && <div class="quick-action" data-testid="setup-client-pending"><p>{copy.clientPendingImpact}</p><p>{copy.prompt}</p><textarea class="mono" readOnly value={verification} aria-label={copy.prompt} data-testid="setup-verification-prompt" rows={3}/><button class="btn" data-testid="setup-copy-prompt" onClick={() => void getApi().onboarding({ op: "status", job: job.id, copy_prompt: true }).then((r) => { if (r.ok) ctx.toast(copy.promptCopied); })}>{copy.copyPrompt}</button><button class="btn primary" disabled={busy} data-testid="setup-verify" onClick={() => void runJob("verify")}>{copy.verify}</button></div>}
      <div class="row end">{job.status === "cancelled" && <button class="btn primary" data-testid="setup-new" onClick={() => { setJob(undefined); setStep(0); void inspect(); }}>{copy.newSetup}</button>}{["pending", "running", "waiting_user", "failed"].includes(job.status) && <button class="btn danger" disabled={busy} data-testid="setup-cancel" onClick={() => setConfirmCancel(true)}>{copy.cancel}</button>}{job.purpose === "multi-device" && job.readiness.service_ready && <button class="btn" data-testid="setup-pair-device" onClick={() => ctx.go("devices")}>{copy.pairDevice}</button>}{["pending", "running", "waiting_user"].includes(job.status) && <button class="btn" disabled={busy} data-testid="setup-continue" onClick={() => void runJob("resume")}>{copy.resume}</button>}{job.status === "failed" && <><button class="btn" onClick={() => { setJob(undefined); setStep(1); setError(undefined); }}>{copy.edit}</button><button class="btn primary" disabled={busy} onClick={() => void runJob("resume")} data-testid="setup-retry">{copy.retry}</button></>}<button class="btn" onClick={() => void copyDiagnostics()} data-testid="setup-diagnostics">{copy.copy}</button>{job.readiness.service_ready && <button class="btn primary" onClick={() => ctx.go("features")} data-testid="wizard-finish">{copy.finish}</button>}</div>
    </section>}
    {confirmCancel && <Confirm danger title={copy.cancelTitle} body={copy.cancelHint} confirmLabel={copy.cancel} busy={busy} onCancel={() => setConfirmCancel(false)} onConfirm={() => { setConfirmCancel(false); void runJob("cancel"); }}/> }
    {diagnostics && <details class="card" open><summary>{copy.copy}</summary><textarea class="mono" readOnly rows={10} value={diagnostics} onFocus={(e) => e.currentTarget.select()} aria-label={copy.copy}/></details>}
  </div>;
}
