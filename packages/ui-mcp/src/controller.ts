import fs from "node:fs";
import path from "node:path";
import { fork, type ChildProcess } from "node:child_process";
import { fileURLToPath } from "node:url";
import { randomUUID, createHash } from "node:crypto";
import type {
  Account,
  Config,
  Environment,
  RunRequest,
  ScriptRequest,
  RunResult,
  RunState,
  Target,
} from "./types.js";
import { environment, inside, privateDir } from "./config.js";
import { Evidence, atomicJSON, bytes, gc, redact } from "./artifacts.js";
import { Locks, processIdentity } from "./locks.js";
import {
  ownProcess,
  terminateProcesses,
  type OwnedProcess,
} from "./processes.js";
import { UIError, failure, requireValue } from "./errors.js";
import { checkSteps, resolveTarget, validate } from "./protocol.js";

interface Session {
  id: string;
  environment_id: string;
  environment: Environment;
  account?: Account;
  child: ChildProcess;
  dir: string;
  versions: Record<string, string>;
  lastUse: number;
  releases: Array<() => void>;
  pending: Map<
    string,
    { resolve: (value: any) => void; reject: (error: unknown) => void }
  >;
  active?: string;
  closed: boolean;
  closing?: Promise<void>;
  processes: OwnedProcess[];
  exitCleanup?: Promise<void>;
}
interface Run {
  result: RunResult;
  evidence: Evidence;
  session: Session;
  timeout?: NodeJS.Timeout;
  stopState?: RunState;
  done: Promise<void>;
  resolveDone: () => void;
  finished: boolean;
  quotaTimer?: NodeJS.Timeout;
}
function digest(input: unknown): string {
  const canonical = (v: any): any =>
    Array.isArray(v)
      ? v.map(canonical)
      : v && typeof v === "object"
        ? Object.fromEntries(
            Object.keys(v)
              .sort()
              .map((k) => [k, canonical(v[k])]),
          )
        : v;
  return createHash("sha256")
    .update(JSON.stringify(canonical(input)))
    .digest("hex");
}
function recoverUnfinished(config: Config): void {
  const root = path.join(config.state_dir, "instances");
  if (!fs.existsSync(root)) return;
  for (const entry of fs
    .readdirSync(root, { withFileTypes: true })
    .filter((e) => e.isDirectory())) {
    const dir = path.join(root, entry.name);
    if (fs.existsSync(path.join(dir, "closed.json"))) continue;
    try {
      const owner = JSON.parse(
        fs.readFileSync(path.join(dir, "owner.json"), "utf8"),
      );
      if (
        !Number.isSafeInteger(owner.pid) ||
        owner.pid <= 0 ||
        typeof owner.birth !== "string"
      )
        continue;
      let dead = false;
      try {
        process.kill(owner.pid, 0);
        const current = processIdentity(owner.pid);
        dead = !!current && current !== owner.birth;
      } catch (error) {
        dead = (error as NodeJS.ErrnoException).code === "ESRCH";
      }
      if (!dead) continue;
      const runs = path.join(dir, "runs");
      if (!fs.existsSync(runs)) continue;
      for (const run of fs
        .readdirSync(runs, { withFileTypes: true })
        .filter((e) => e.isDirectory())) {
        const file = path.join(runs, run.name, "result.json");
        fs.rmSync(path.join(runs, run.name, "script.mjs"), { force: true });
        try {
          const result = JSON.parse(fs.readFileSync(file, "utf8"));
          if (!result.ended_at)
            atomicJSON(file, {
              ...result,
              status: "interrupted",
              ended_at: new Date().toISOString(),
              outcome_unknown: true,
              evidence_incomplete: true,
              error: {
                code: "application_exited",
                message: "previous backend instance exited during run",
                details: {},
              },
            });
        } catch {
          /* Preserve incomplete journals for inspection. */
        }
      }
    } catch {
      /* An unrecognized owner is never assumed dead. */
    }
  }
}
export class Controller {
  readonly instance_id = `i_${randomUUID()}`;
  private sessions = new Map<string, Session>();
  private runs = new Map<string, Run>();
  private requests = new Map<string, { hash: string; run_id: string }>();
  private locks: Locks;
  private timer: NodeJS.Timeout;
  private closed = false;
  private closing?: Promise<void>;
  private guardian: ChildProcess;
  private guardianReady: Promise<void>;
  constructor(readonly config: Config) {
    privateDir(config.state_dir);
    recoverUnfinished(config);
    gc(config);
    this.locks = new Locks(path.join(config.state_dir, "locks"));
    privateDir(path.join(config.state_dir, "instances", this.instance_id));
    atomicJSON(
      path.join(config.state_dir, "instances", this.instance_id, "owner.json"),
      {
        pid: process.pid,
        birth: processIdentity(process.pid),
        instance_id: this.instance_id,
        started_at: new Date().toISOString(),
      },
    );
    const guardianLog = fs.openSync(
      path.join(
        config.state_dir,
        "instances",
        this.instance_id,
        "guardian.log",
      ),
      "a",
      0o600,
    );
    this.guardian = fork(
      fileURLToPath(new URL("./guardian.js", import.meta.url)),
      [
        path.join(config.state_dir, "instances", this.instance_id),
        String(process.pid),
        processIdentity(process.pid),
      ],
      {
        execArgv: [],
        stdio: ["ignore", "ignore", guardianLog, "ipc"],
        // Windows libuv's parent Job kills attached children on abrupt exit.
        // The guardian must survive that Job; its console stays hidden.
        detached: true,
        windowsHide: true,
      },
    );
    fs.closeSync(guardianLog);
    this.guardianReady = new Promise<void>((resolve, reject) => {
      const timer = setTimeout(
        () =>
          reject(
            new UIError(
              "preflight_failed",
              "process guardian did not become ready",
            ),
          ),
        15000,
      );
      timer.unref();
      this.guardian.once("error", (error) => {
        clearTimeout(timer);
        reject(error);
      });
      this.guardian.once("exit", () => {
        clearTimeout(timer);
        reject(new UIError("preflight_failed", "process guardian exited"));
      });
      this.guardian.on("message", (message: any) => {
        if (message?.type === "ready") {
          clearTimeout(timer);
          resolve();
        }
      });
    });
    void this.guardianReady.catch(() => {});
    this.guardian.unref();
    this.guardian.on("error", () => {
      void this.close().catch(() => {});
    });
    this.timer = setInterval(() => {
      for (const s of this.sessions.values())
        if (
          s.closed ||
          (!s.active && Date.now() - s.lastUse > config.limits.session_idle_ms)
        )
          void this.closeSession(s.id).catch(() => {});
    }, 1000);
    this.timer.unref();
  }
  capabilities(id?: string): Record<string, unknown> {
    const list = id
      ? [[id, environment(this.config, id)] as const]
      : Object.entries(this.config.environments);
    return {
      protocol_version: 1,
      instance_id: this.instance_id,
      drivers: ["web", "electron"],
      interaction: "browser_protocol",
      requires_foreground: false,
      limits: this.config.limits,
      actions: [
        "navigate",
        "select_page",
        "wait_page",
        "click",
        "fill",
        "press",
        "check",
        "select",
        "hover",
        "scroll",
        "upload",
        "download",
        "wait",
        "assert",
        "screenshot",
        "read",
        "response",
        "dialog",
      ],
      environments: list.map(([id, e]) => ({
        id,
        name: e.name ?? id,
        driver: e.driver,
        script_enabled: e.script_enabled ?? false,
        accounts: Object.keys(e.accounts ?? {}),
        coverage: e.coverage,
        fixtures: e.fixtures,
        preflight: this.preflight(e),
      })),
    };
  }
  preflight(e: Environment): { ready: boolean; issues: string[] } {
    const issues: string[] = [];
    if (e.driver === "web" && e.headless === false)
      issues.push("background_required");
    if (e.driver === "electron" && !e.background_capable)
      issues.push("background_support_unconfirmed");
    if (e.executable && !fs.existsSync(e.executable))
      issues.push("executable_not_found");
    if (
      process.platform === "linux" &&
      (e.driver === "electron" || e.headless === false) &&
      !process.env.DISPLAY &&
      !process.env.WAYLAND_DISPLAY
    )
      issues.push("display_unavailable");
    if (
      e.driver === "electron" &&
      e.executable &&
      fs.existsSync(e.executable)
    ) {
      // Electron keeps its fuse wire in the executable (or the framework binary on macOS).
      const framework = path.resolve(
        path.dirname(e.executable),
        "../Frameworks/Electron Framework.framework/Electron Framework",
      );
      const file = fs.existsSync(framework) ? framework : e.executable;
      try {
        const data = fs.readFileSync(file),
          index = data.indexOf("dL7pKGdnNz796PbbjQWNKmHXBZaB9tsX");
        if (index >= 0 && data[index + 32 + 2 + 3] === 48)
          issues.push("electron_inspect_disabled");
      } catch {
        issues.push("executable_unreadable");
      }
    }
    return { ready: issues.length === 0, issues };
  }
  async open(id: string, accountId?: string): Promise<Record<string, unknown>> {
    await this.guardianReady;
    requireValue(!this.closed, "session_closed", "backend is closing");
    const e = environment(this.config, id);
    requireValue(
      this.sessions.size < this.config.limits.sessions,
      "resource_busy",
      "caller session limit reached",
    );
    const account = accountId ? e.accounts?.[accountId] : undefined;
    requireValue(
      !accountId || account,
      "invalid_args",
      "unknown account configuration",
    );
    const check = this.preflight(e);
    requireValue(check.ready, "preflight_failed", check.issues.join(", "));
    requireValue(
      bytes(this.config.state_dir) < this.config.limits.total_bytes,
      "evidence_quota",
      "node evidence quota reached; run gc before opening a session",
    );
    const releases: Array<() => void> = [];
    let slot = false;
    for (let i = 0; i < this.config.limits.node_sessions; i++) {
      const release = this.locks.acquire(`slot:${i}`);
      if (release) {
        releases.push(release);
        slot = true;
        break;
      }
    }
    requireValue(slot, "resource_busy", "node session limit reached");
    let assigned = false;
    try {
      const resources = [
        ...(e.resources ?? []),
        ...(accountId ? [`account:${id}:${accountId}`] : []),
        ...(e.driver === "electron" ? [`electron:${id}`] : []),
      ];
      for (const key of [...new Set(resources)].sort()) {
        const release = this.locks.acquire(`resource:${key}`);
        requireValue(
          release,
          "resource_busy",
          "environment resource is in use",
        );
        releases.push(release);
      }
      const session_id = `s_${randomUUID()}`,
        dir = path.join(
          this.config.state_dir,
          "instances",
          this.instance_id,
          "sessions",
          session_id,
        );
      privateDir(dir);
      const child = fork(
        fileURLToPath(new URL("./worker.js", import.meta.url)),
        [],
        {
          execArgv: [],
          stdio: ["ignore", "pipe", "pipe", "ipc"],
          env: { ...process.env },
          detached: process.platform !== "win32",
          windowsHide: true,
        },
      );
      const s: Session = {
        id: session_id,
        environment_id: id,
        environment: e,
        account,
        child,
        dir,
        versions: {},
        lastUse: Date.now(),
        releases,
        pending: new Map(),
        closed: false,
        processes: child.pid
          ? [ownProcess(child.pid, process.platform !== "win32")]
          : [],
      };
      for (const owned of s.processes)
        this.guardian.send({ type: "track", process: owned });
      atomicJSON(path.join(dir, "processes.json"), s.processes);
      this.sessions.set(session_id, s);
      assigned = true;
      child.on("message", (m) => this.message(s, m));
      const stdout = (chunk: Buffer) => {
        if (s.active)
          this.record(this.runs.get(s.active)!, {
            kind: "script_output",
            text: chunk.toString("utf8").slice(0, 4096),
          });
      };
      child.stdout?.on("data", stdout);
      child.stderr?.on("data", stdout);
      child.on("error", () => this.exited(s));
      child.on("exit", () => this.exited(s));
      try {
        const opened = await Promise.race([
          this.rpc(s, "open", {
            environment: e,
            account,
            session_dir: dir,
            config: this.config,
          }),
          new Promise<never>((_, reject) => {
            const timer = setTimeout(
              () =>
                reject(
                  new UIError(
                    "preflight_failed",
                    "application launch exceeded 30 seconds",
                  ),
                ),
              30000,
            );
            timer.unref();
          }),
        ]);
        s.versions = opened.versions;
        return {
          protocol_version: 1,
          instance_id: this.instance_id,
          session_id,
          environment_id: id,
          driver: e.driver,
          coverage: e.coverage,
          fixtures: e.fixtures,
          ...opened,
        };
      } catch (error) {
        await this.stop(s);
        throw error;
      }
    } catch (error) {
      if (!assigned) for (const release of releases) release();
      throw error;
    }
  }
  private exited(s: Session): void {
    if (s.closed) return;
    s.closed = true;
    for (const p of s.pending.values())
      p.reject(new UIError("application_exited", "test worker exited"));
    s.pending.clear();
    void this.cleanupSession(s).catch(() => {});
  }
  private cleanupSession(s: Session): Promise<void> {
    if (s.exitCleanup) return s.exitCleanup;
    s.exitCleanup = terminateProcesses(s.processes).then(() => {
      // Resource locks remain held until the test processes have been terminated.
      while (s.releases.length) {
        s.releases[0]!();
        s.releases.shift();
      }
      this.sessions.delete(s.id);
    });
    s.exitCleanup = s.exitCleanup.catch((error) => {
      s.exitCleanup = undefined;
      throw error;
    });
    return s.exitCleanup;
  }
  private message(s: Session, raw: unknown): void {
    const m = raw as any;
    if (m.type === "process" && Number.isSafeInteger(m.pid) && m.pid > 0) {
      const owned = ownProcess(m.pid, !!m.group);
      s.processes.push(owned);
      atomicJSON(path.join(s.dir, "processes.json"), s.processes);
      this.guardian.send({ type: "track", process: owned });
      return;
    }
    if (m.type === "reply") {
      const p = s.pending.get(m.id);
      s.pending.delete(m.id);
      if (!p) return;
      if (m.error)
        p.reject(new UIError(m.error.code, m.error.message, m.error.details));
      else p.resolve(m.value);
      return;
    }
    const run = this.runs.get(m.run_id);
    if (!run || run.session !== s || run.finished) return;
    if (m.type === "event") this.record(run, m.event);
    if (m.type === "artifact") {
      try {
        run.result.artifacts.push(
          run.evidence.add(m.artifact.file, m.artifact.name, m.artifact.mime),
        );
      } catch {
        run.result.evidence_incomplete = true;
        try {
          fs.rmSync(inside(run.evidence.dir, m.artifact.file), { force: true });
        } catch {}
      }
    }
  }
  private rpc(s: Session, op: string, args: unknown): Promise<any> {
    requireValue(!s.closed, "session_closed", "test session is closed");
    s.lastUse = Date.now();
    return new Promise((resolve, reject) => {
      const id = randomUUID();
      s.pending.set(id, { resolve, reject });
      s.child.send({ id, op, args }, (error) => {
        if (error) {
          s.pending.delete(id);
          reject(error);
        }
      });
    });
  }
  private session(id: string): Session {
    const s = this.sessions.get(id);
    requireValue(s && !s.closed, "session_closed", "unknown or closed session");
    return s;
  }
  async snapshot(
    id: string,
    pageId?: string,
    target?: Target,
    maxLength = 20000,
  ): Promise<unknown> {
    const s = this.session(id);
    requireValue(!s.active, "resource_busy", "session has an active run");
    requireValue(
      Number.isSafeInteger(maxLength) && maxLength > 0 && maxLength <= 64000,
      "invalid_args",
      "snapshot length is out of range",
    );
    const value = await this.rpc(s, "snapshot", {
      page_id: pageId,
      target: target ? resolveTarget(target, s.environment) : undefined,
      max_length: maxLength,
    });
    return redact(value, Object.values(s.account?.secrets ?? {}));
  }
  submit(
    input: RunRequest | ScriptRequest,
    script = false,
  ): { run_id: string; status: RunState; deduplicated: boolean } {
    validate(script ? "ScriptRequest" : "RunRequest", input);
    const hash = digest(input),
      prior = this.requests.get(input.request_id);
    if (prior) {
      requireValue(
        hash === prior.hash,
        "request_conflict",
        "request_id was used for different content",
      );
      return {
        run_id: prior.run_id,
        status: this.runs.get(prior.run_id)!.result.status,
        deduplicated: true,
      };
    }
    requireValue(!this.closed, "session_closed", "backend is closing");
    requireValue(
      this.requests.size < this.config.limits.requests,
      "request_limit",
      "backend request limit reached",
    );
    const s = this.session(input.session_id);
    requireValue(!s.active, "resource_busy", "session has an active run");
    requireValue(
      !script || s.environment.script_enabled,
      "policy_denied",
      "scripts are disabled for this environment",
    );
    const timeout = input.timeout_ms ?? this.config.limits.run_timeout_ms;
    requireValue(
      timeout <= this.config.limits.max_run_timeout_ms,
      "invalid_args",
      "run timeout exceeds limit",
    );
    const steps = script
      ? undefined
      : checkSteps(
          (input as RunRequest).steps,
          s.environment,
          s.account?.secrets,
        );
    requireValue(
      !steps || steps.length <= this.config.limits.max_steps,
      "invalid_args",
      "step count exceeds limit",
    );
    if (steps)
      for (const step of steps) {
        if (step.timeout_ms)
          requireValue(
            step.timeout_ms <= timeout,
            "invalid_args",
            "step timeout exceeds run timeout",
          );
        if (step.action === "upload") {
          requireValue(
            s.environment.upload_root,
            "path_denied",
            "upload root is not configured",
          );
          inside(s.environment.upload_root, step.file!);
        }
      }
    const run_id = `r_${randomUUID()}`,
      evidence = new Evidence(
        this.config,
        this.instance_id,
        run_id,
        Object.values(s.account?.secrets ?? {}),
      );
    let resolveDone!: () => void;
    const result: RunResult = {
      protocol_version: 1,
      instance_id: this.instance_id,
      session_id: s.id,
      run_id,
      environment_id: s.environment_id,
      driver: s.environment.driver,
      mode: script ? "script" : "steps",
      coverage: s.environment.coverage ?? ["ui"],
      fixtures: s.environment.fixtures ?? "real",
      versions: s.versions,
      status: "accepted",
      started_at: new Date().toISOString(),
      steps: steps?.map((step) => ({ id: step.id, status: "not_run" })) ?? [],
      artifacts: [],
      evidence_incomplete: false,
      events: [],
    };
    evidence.save(result);
    atomicJSON(path.join(evidence.dir, "manifest.json"), {
      ...result,
      request_digest: hash,
      trace: s.environment.trace ?? false,
    });
    const run: Run = {
      result,
      evidence,
      session: s,
      done: new Promise((resolve) => {
        resolveDone = resolve;
      }),
      resolveDone,
      finished: false,
    };
    this.runs.set(run_id, run);
    this.requests.set(input.request_id, { hash, run_id });
    s.active = run_id;
    setImmediate(
      () =>
        void this.execute(
          run,
          {
            run_id,
            dir: evidence.dir,
            steps,
            ...(script
              ? {
                  code: (input as ScriptRequest).code,
                  params: (input as ScriptRequest).params,
                }
              : {}),
          },
          timeout,
        ),
    );
    return { run_id, status: "accepted", deduplicated: false };
  }
  private record(run: Run, event: Record<string, unknown>): void {
    if (!run || run.finished) return;
    const safe = redact(
      event,
      Object.values(run.session.account?.secrets ?? {}),
    );
    if (safe.kind === "step") {
      let step = run.result.steps.find((s) => s.id === safe.id);
      if (!step) {
        step = { id: String(safe.id).slice(0, 128), status: "not_run" };
        run.result.steps.push(step);
      }
      step.status = safe.status as typeof step.status;
      step.duration_ms = safe.duration_ms as number | undefined;
    }
    if (run.result.events.length >= 1000) {
      run.result.evidence_incomplete = true;
      if (safe.kind === "step")
        try {
          run.evidence.event(safe);
          run.evidence.save(run.result);
        } catch {}
      return;
    }
    run.result.events.push(safe);
    if (safe.kind === "evidence_incomplete")
      run.result.evidence_incomplete = true;
    try {
      run.evidence.event(safe);
      run.evidence.save(run.result);
    } catch {
      run.result.evidence_incomplete = true;
    }
  }
  private async execute(
    run: Run,
    args: unknown,
    timeout: number,
  ): Promise<void> {
    if (run.finished) return;
    run.result.status = "running";
    this.record(run, { kind: "run_started", time: new Date().toISOString() });
    run.timeout = setTimeout(() => {
      run.stopState = "timed_out";
      void this.stop(run.session).catch(() => {});
    }, timeout);
    run.quotaTimer = setInterval(() => {
      if (
        bytes(run.evidence.dir) > this.config.limits.run_bytes ||
        bytes(this.config.state_dir) > this.config.limits.total_bytes
      ) {
        run.stopState = "failed";
        run.result.error = failure(
          new UIError(
            "evidence_quota",
            "run stopped after exceeding evidence quota",
          ),
        );
        void this.stop(run.session).catch(() => {});
      }
    }, 500);
    try {
      await this.rpc(run.session, "run", args);
      run.result.status = "passed";
    } catch (error) {
      const detail = failure(error);
      run.result.status =
        run.stopState ??
        (detail.code === "application_exited" ? "interrupted" : "failed");
      run.result.error ??= detail;
    } finally {
      clearTimeout(run.timeout);
      clearInterval(run.quotaTimer);
      run.result.status = run.stopState ?? run.result.status;
      if (run.stopState || run.result.status === "interrupted") {
        run.result.outcome_unknown =
          run.result.steps.some((s) => s.status === "started") ||
          run.result.mode === "script";
        run.result.evidence_incomplete = true;
      }
      run.result.ended_at = new Date().toISOString();
      try {
        fs.rmSync(path.join(run.evidence.dir, "script.mjs"), { force: true });
      } catch {
        run.result.evidence_incomplete = true;
      }
      if (run.stopState) {
        try {
          await this.stop(run.session);
        } catch (error) {
          run.result.error = failure(error);
          run.result.evidence_incomplete = true;
        }
      }
      this.record(run, {
        kind: "run_finished",
        status: run.result.status,
        time: run.result.ended_at,
      });
      try {
        run.result.artifacts.push(...run.evidence.reports(run.result));
      } catch {
        run.result.evidence_incomplete = true;
      }
      run.finished = true;
      try {
        run.evidence.save(run.result);
      } catch {
        run.result.evidence_incomplete = true;
      }
      run.session.active = undefined;
      run.session.lastUse = Date.now();
      run.resolveDone();
    }
  }
  result(id: string, cursor = 0): Record<string, unknown> {
    const run = this.runs.get(id);
    requireValue(run, "not_found", "unknown run");
    requireValue(
      Number.isSafeInteger(cursor) &&
        cursor >= 0 &&
        cursor <= run.result.events.length,
      "invalid_args",
      "invalid event cursor",
    );
    return redact(
      {
        ...run.result,
        events: run.result.events.slice(cursor),
        next_cursor: run.result.events.length,
      },
      Object.values(run.session.account?.secrets ?? {}),
    );
  }
  artifact(
    id: string,
    offset?: number,
    limit?: number,
  ): Record<string, unknown> {
    for (const run of this.runs.values()) {
      const artifact = run.result.artifacts.find((a) => a.id === id);
      if (artifact)
        return {
          ...run.evidence.read(id, offset, limit),
          mime_type: artifact.mime_type,
          sha256: artifact.sha256,
        };
    }
    throw new UIError("not_found", "unknown artifact");
  }
  async cancel(id: string): Promise<unknown> {
    const run = this.runs.get(id);
    requireValue(run, "not_found", "unknown run");
    if (!run.finished) {
      run.stopState = "cancelled";
      await this.stop(run.session);
      await run.done;
    }
    return this.result(id);
  }
  private async stop(s: Session): Promise<void> {
    if (s.closing) return s.closing;
    s.closing = (async () => {
      if (s.closed) {
        await this.cleanupSession(s);
        return;
      }
      const exited = new Promise<void>((resolve) =>
        s.child.once("exit", () => resolve()),
      );
      if (s.child.connected) s.child.disconnect();
      let timer: NodeJS.Timeout | undefined;
      await Promise.race([
        exited,
        new Promise<void>((resolve) => {
          timer = setTimeout(() => {
            s.child.kill("SIGKILL");
            resolve();
          }, 8000);
        }),
      ]);
      clearTimeout(timer);
      if (!s.closed)
        await Promise.race([
          exited,
          new Promise((resolve) => setTimeout(resolve, 1000)),
        ]);
      requireValue(
        s.closed,
        "cleanup_pending",
        "worker termination has not been confirmed",
      );
      await this.cleanupSession(s);
      fs.rmSync(s.dir, { recursive: true, force: true });
    })();
    try {
      await s.closing;
    } finally {
      s.closing = undefined;
    }
  }
  async closeSession(id: string): Promise<{ closed: boolean }> {
    const s = this.sessions.get(id);
    if (!s) return { closed: true };
    if (s.active) {
      const run = this.runs.get(s.active)!;
      run.stopState = "cancelled";
      await this.stop(s);
      await run.done;
    } else await this.stop(s);
    return { closed: true };
  }
  async close(): Promise<void> {
    if (this.closing) return this.closing;
    this.closed = true;
    this.closing = this.shutdown();
    try {
      await this.closing;
    } catch (error) {
      this.closing = undefined;
      throw error;
    }
  }
  private async shutdown(): Promise<void> {
    await Promise.all(
      [...this.sessions.keys()].map((id) => this.closeSession(id)),
    );
    this.locks.close();
    clearInterval(this.timer);
    atomicJSON(
      path.join(
        this.config.state_dir,
        "instances",
        this.instance_id,
        "closed.json",
      ),
      { closed_at: new Date().toISOString() },
    );
    if (this.guardian.exitCode === null && this.guardian.signalCode === null) {
      const exited = new Promise<void>((resolve) =>
        this.guardian.once("exit", () => resolve()),
      );
      if (this.guardian.connected) this.guardian.disconnect();
      let timer: NodeJS.Timeout | undefined;
      try {
        await Promise.race([
          exited,
          new Promise<never>((_, reject) => {
            timer = setTimeout(() => {
              this.guardian.kill();
              reject(
                new UIError(
                  "cleanup_pending",
                  "guardian shutdown was not confirmed",
                ),
              );
            }, 10000);
          }),
        ]);
      } finally {
        clearTimeout(timer);
      }
    }
  }
}
