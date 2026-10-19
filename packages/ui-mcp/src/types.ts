export type {
  Step,
  Target,
  RunRequest,
  ScriptRequest,
} from "./protocol.gen.js";
export interface Account {
  storage_state?: string;
  secrets?: Record<string, string>;
}
export interface Environment {
  name?: string;
  driver: "web" | "electron";
  url?: string;
  allowed_origins?: string[];
  executable?: string;
  args?: string[];
  cwd?: string;
  env?: Record<string, string>;
  headless?: boolean;
  background_capable?: boolean;
  script_enabled?: boolean;
  trace?: boolean;
  mask?: string[];
  upload_root?: string;
  state_template?: string;
  init_module?: string;
  locators?: Record<string, import("./protocol.gen.js").Target>;
  accounts?: Record<string, Account>;
  resources?: string[];
  coverage?: string[];
  fixtures?: "real" | "mock";
}
export interface Config {
  version: 1;
  state_dir: string;
  environments: Record<string, Environment>;
  limits: {
    sessions: number;
    node_sessions: number;
    session_idle_ms: number;
    step_timeout_ms: number;
    run_timeout_ms: number;
    max_run_timeout_ms: number;
    max_steps: number;
    requests: number;
    retention_days: number;
    run_bytes: number;
    total_bytes: number;
  };
}
export type RunState =
  | "accepted"
  | "running"
  | "passed"
  | "failed"
  | "cancelled"
  | "timed_out"
  | "interrupted";
export interface Artifact {
  id: string;
  name: string;
  mime_type: string;
  bytes: number;
  sha256: string;
}
export interface RunResult {
  protocol_version: 1;
  instance_id: string;
  session_id: string;
  run_id: string;
  environment_id: string;
  driver: string;
  mode: "steps" | "script";
  coverage: string[];
  fixtures: string;
  versions: Record<string, string>;
  status: RunState;
  started_at: string;
  ended_at?: string;
  error?: ReturnType<typeof import("./errors.js").failure>;
  steps: Array<{
    id: string;
    status: "started" | "passed" | "failed" | "not_run";
    duration_ms?: number;
  }>;
  artifacts: Artifact[];
  evidence_incomplete: boolean;
  outcome_unknown?: boolean;
  events: Array<Record<string, unknown>>;
}
export interface WorkerInit {
  environment: Environment;
  session_dir: string;
  config: Config;
  account?: Account;
}
