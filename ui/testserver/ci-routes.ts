import type {
  CiAccount,
  CiBinding,
  CiImportInspectResponse,
  CiInstance,
  CiNotice,
  CiOverview,
  CiRepo,
} from "../src/shared/control";
import type { RouteResult } from "./panel-routes";

// The fake kernel's GitHub Actions runner routes (CTR-CTL-15). State is in memory, and a binding's instances go
// straight to `idle`. What tests rely on:
//
//   - a token starting with "ghp_bad" is refused (github_rejected); "ghp_other…" belongs to the login "other-user",
//     any other to "tzf1003"; a token is never stored or served back, only its mask;
//   - `publicRepos` are public: binding one without confirm_public answers public_repo_requires_confirm;
//   - the repository list of an account has two pages (GET …/repos?page=2 for the second);
//   - import directories under /runners/: clean is fine, service has the official service, running is running,
//     ephemeral is a one-job runner, anything else has no .runner;
//   - DELETE marks the binding `removing`; the next GET drops it (the real kernel drains first);
//   - `tokens` records every secret the fake was sent, so a test can assert none came back.

const iso = (): string => new Date().toISOString();
const ok = (body: unknown, status = 200): RouteResult => ({ status, body });
const err = (status: number, error: string, message: string, extra: object = {}): RouteResult => ({ status, body: { error, message, ...extra } });
const bad = (message: string, field: string, reason?: string): RouteResult => err(400, "invalid_args", message, { field, ...(reason && { reason }) });

const REPOS_PAGE_1: Omit<CiRepo, "binding">[] = [
  { full_name: "tzf1003/xmesh", private: true },
  { full_name: "tzf1003/oss-lib", private: false },
];
const REPOS_PAGE_2: Omit<CiRepo, "binding">[] = [{ full_name: "tzf1003/notes", private: true }];

const INSPECT: Record<string, CiImportInspectResponse> = {
  clean: { target: "tzf1003/legacy", runner_name: "old-mac", labels: ["self-hosted", "macOS"], service_installed: false, running: false },
  service: {
    target: "tzf1003/legacy",
    runner_name: "old-mac",
    labels: ["self-hosted", "macOS"],
    service_installed: true,
    service_hint: "cd /runners/service && ./svc.sh stop && ./svc.sh uninstall",
    running: false,
  },
  running: { target: "tzf1003/legacy", runner_name: "old-mac", labels: ["self-hosted"], service_installed: false, running: true },
  ephemeral: { target: "tzf1003/legacy", runner_name: "old-mac", labels: ["self-hosted"], service_installed: false, running: false, ephemeral: true },
};

export class CiFake {
  accounts: CiAccount[] = [];
  bindings: CiBinding[] = [];
  notices: CiNotice[] = [];
  draining = false;
  publicRepos = new Set<string>(["tzf1003/oss-lib"]);
  /** Every secret a request carried (GitHub tokens, registration tokens). Test hook only. */
  tokens: string[] = [];

  overview(): CiOverview {
    this.bindings = this.bindings.filter((b) => !b.removing);
    return {
      accounts: this.accounts,
      bindings: this.bindings,
      notices: this.notices,
      runner: { version: "2.329.0", cached: true },
      prevent_sleep_while_busy: true,
      stop_timeout_s: 1800,
      ...(this.draining && { draining: true }),
    };
  }

  handle(method: string, p: string, params: URLSearchParams, body: unknown): RouteResult | undefined {
    const b = (body ?? {}) as Record<string, unknown>;
    if (method === "GET" && p === "/v1/ci") return ok(this.overview());
    if (method === "POST" && p === "/v1/ci/accounts") return this.createAccount(b);
    const acc = /^\/v1\/ci\/accounts\/([^/]+)(\/repos)?$/.exec(p);
    if (acc) {
      const id = decodeURIComponent(acc[1]!);
      if (method === "DELETE" && !acc[2]) return this.deleteAccount(id);
      if (method === "GET" && acc[2]) return this.repos(id, params);
    }
    if (method === "POST" && p === "/v1/ci/bindings") return this.createBinding(b);
    if (method === "POST" && p === "/v1/ci/import/inspect") return this.inspect(b);
    const bind = /^\/v1\/ci\/bindings\/([^/]+)(\/restart|\/repair)?$/.exec(p);
    if (bind) {
      const id = decodeURIComponent(bind[1]!);
      const binding = this.bindings.find((x) => x.id === id);
      if (!binding) return err(404, "not_found", "没有这个绑定");
      if (method === "PATCH" && !bind[2]) return this.patch(binding, b);
      if (method === "DELETE" && !bind[2]) {
        binding.removing = true;
        return ok(binding);
      }
      if (method === "POST" && bind[2] === "/restart") {
        for (const i of binding.instances) i.restarts += 1;
        return { status: 204 };
      }
      if (method === "POST" && bind[2] === "/repair") return this.repair(binding, b);
    }
    return undefined;
  }

  private mask(token: string): string {
    return `${token.slice(0, 10)}…${token.slice(-4)}`;
  }

  private createAccount(b: Record<string, unknown>): RouteResult {
    const token = b.token;
    if (typeof token !== "string" || !/^[A-Za-z0-9_]{20,255}$/.test(token)) return bad("token 格式不对", "token");
    this.tokens.push(token);
    if (token.startsWith("ghp_bad")) return bad("GitHub 不接受这个令牌：可能已过期、被撤销或权限不足", "token", "github_rejected");
    const host = typeof b.host === "string" ? b.host : "github.com";
    const login = token.startsWith("ghp_other") ? "other-user" : "tzf1003";
    const id = `gh-${login}`;
    const account: CiAccount = { id, host, login, token_hint: this.mask(token) };
    const at = this.accounts.findIndex((a) => a.id === id);
    if (at >= 0) this.accounts[at] = account;
    else if (this.accounts.length >= 8) return err(409, "conflict", "最多保存 8 个账号", { reason: "too_many_accounts" });
    else this.accounts.push(account);
    return ok(account);
  }

  private deleteAccount(id: string): RouteResult {
    if (!this.accounts.some((a) => a.id === id)) return err(404, "not_found", "没有这个账号", { reason: "account_not_found" });
    if (this.bindings.some((x) => x.account === id)) return err(409, "conflict", "还有绑定在用这个账号", { reason: "account_in_use" });
    this.accounts = this.accounts.filter((a) => a.id !== id);
    return { status: 204 };
  }

  private repos(id: string, params: URLSearchParams): RouteResult {
    if (!this.accounts.some((a) => a.id === id)) return err(404, "not_found", "没有这个账号", { reason: "account_not_found" });
    const second = params.get("page") === "2";
    const q = (params.get("q") ?? "").toLowerCase();
    const bound = (name: string): string | undefined => this.bindings.find((x) => x.target === name)?.id;
    const repos = (second ? REPOS_PAGE_2 : REPOS_PAGE_1)
      .filter((r) => r.full_name.toLowerCase().includes(q))
      .map((r): CiRepo => {
        const binding = bound(r.full_name);
        return binding === undefined ? { ...r } : { ...r, binding };
      });
    return ok({ repos, next_page: second ? null : "2" });
  }

  private instance(index: number, name: string): CiInstance {
    return { index, name, state: "idle", restarts: 0, since: iso() };
  }

  private createBinding(b: Record<string, unknown>): RouteResult {
    const ways = [b.account !== undefined, b.registration_token !== undefined, b.import_dir !== undefined].filter(Boolean).length;
    if (ways !== 1) return bad("三种绑定方式恰好用一种", "target");
    let target = typeof b.target === "string" ? b.target : "";
    let source: CiBinding["source"] = "token";
    let dir = "/state/ci";
    if (typeof b.import_dir === "string") {
      const found = INSPECT[b.import_dir.split("/").pop() ?? ""];
      if (!found) return bad("这个目录里没有已注册的运行器", "import_dir", "not_a_runner");
      if (found.ephemeral) return bad("这是一次性运行器", "import_dir", "ephemeral_runner");
      if (found.service_installed) return bad("这个运行器装着官方服务", "import_dir", "service_installed");
      if (found.running) return bad("这个运行器正在运行", "import_dir", "runner_running");
      target = found.target;
      source = "import";
      dir = b.import_dir;
    } else if (b.account !== undefined) {
      if (!this.accounts.some((a) => a.id === b.account)) return err(404, "not_found", "没有这个账号", { reason: "account_not_found" });
      source = "account";
    }
    if (typeof b.registration_token === "string") this.tokens.push(b.registration_token);
    if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(target)) return bad("target 应为 owner/name", "target");
    if (this.publicRepos.has(target) && b.confirm_public !== true) {
      return bad("这是公开仓库，需要再确认一次", "confirm_public", "public_repo_requires_confirm");
    }
    if (this.bindings.some((x) => x.target === target)) return err(409, "conflict", "这个仓库已经绑定过了");
    const id = typeof b.id === "string" ? b.id : target.split("/")[1]!.toLowerCase();
    if (this.bindings.some((x) => x.id === id)) return err(409, "conflict", "编号被占用");
    const concurrency = source === "import" ? 1 : typeof b.concurrency === "number" ? b.concurrency : 1;
    const binding: CiBinding = {
      id,
      scope: "repo",
      target,
      source,
      ...(source === "account" && { account: b.account as string }),
      ...(this.publicRepos.has(target) && { public: true }),
      concurrency,
      labels: ((b.labels as string[] | undefined) ?? []) as CiBinding["labels"],
      paused: false,
      removing: false,
      dir,
      instances: Array.from({ length: concurrency }, (_, n) => this.instance(n + 1, `xmesh-fake-${id}-${n + 1}`)),
    };
    this.bindings.push(binding);
    return ok(binding, 201);
  }

  private patch(binding: CiBinding, b: Record<string, unknown>): RouteResult {
    if (typeof b.concurrency === "number") {
      if (binding.source === "import" && b.concurrency !== 1) return bad("接管的运行器只有一个实例", "concurrency", "import_single_instance");
      binding.concurrency = b.concurrency;
      while (binding.instances.length > b.concurrency) binding.instances.pop();
      while (binding.instances.length < b.concurrency) binding.instances.push(this.instance(binding.instances.length + 1, `xmesh-fake-${binding.id}-${binding.instances.length + 1}`));
    }
    if (Array.isArray(b.labels)) binding.labels = b.labels as CiBinding["labels"];
    if (typeof b.account === "string") binding.account = b.account;
    if (typeof b.paused === "boolean") {
      binding.paused = b.paused;
      for (const i of binding.instances) i.state = b.paused ? "stopped" : "idle";
    }
    return ok(binding);
  }

  private repair(binding: CiBinding, b: Record<string, unknown>): RouteResult {
    if (binding.account === undefined && typeof b.registration_token !== "string") return bad("没有账号的绑定需要注册令牌", "registration_token");
    if (typeof b.registration_token === "string") this.tokens.push(b.registration_token);
    for (const i of binding.instances) {
      i.state = "idle";
      delete i.problem;
    }
    return ok(binding);
  }

  private inspect(b: Record<string, unknown>): RouteResult {
    const dir = typeof b.dir === "string" ? b.dir : "";
    const found = INSPECT[dir.split("/").pop() ?? ""];
    if (!dir.startsWith("/runners/") || !found) return err(404, "not_found", "这个目录里没有已注册的运行器", { reason: "not_a_runner" });
    return ok(found);
  }
}
