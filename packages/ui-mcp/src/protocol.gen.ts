/* Generated from schemas/protocol.schema.json. Run npm run generate. */

export type UIProtocol = RunRequest | ScriptRequest;
export type Target = {
  testId?: string;
  role?: string;
  name?: string;
  label?: string;
  text?: string;
  css?: string;
  alias?: string;
  within?: Target;
  frame?: string;
} & Target1 & {
    testId?: string;
    role?: string;
    name?: string;
    label?: string;
    text?: string;
    css?: string;
    alias?: string;
    within?: Target;
    frame?: string;
  } & Target1;
export type Target1 =
  | {
      [k: string]: unknown;
    }
  | {
      [k: string]: unknown;
    }
  | {
      [k: string]: unknown;
    }
  | {
      [k: string]: unknown;
    }
  | {
      [k: string]: unknown;
    }
  | {
      [k: string]: unknown;
    };

export interface RunRequest {
  protocol_version: 1;
  session_id: string;
  request_id: string;
  timeout_ms?: number;
  /**
   * @minItems 1
   * @maxItems 100
   */
  steps: [Step, ...Step[]];
}
export interface Step {
  id: string;
  action:
    | "navigate"
    | "select_page"
    | "wait_page"
    | "click"
    | "fill"
    | "press"
    | "check"
    | "select"
    | "hover"
    | "scroll"
    | "upload"
    | "download"
    | "wait"
    | "assert"
    | "screenshot"
    | "read"
    | "response"
    | "dialog";
  target?: Target;
  expect?: Expectation;
  value?: string;
  secret?: string;
  checked?: boolean;
  page_id?: string;
  file?: string;
  name?: string;
  timeout_ms?: number;
  x?: number;
  y?: number;
  status?: number;
  accept?: boolean;
}
export interface Expectation {
  visible?: boolean;
  enabled?: boolean;
  text?: string;
  containsText?: string;
  value?: string;
  count?: number;
  url?: string;
}
export interface ScriptRequest {
  protocol_version: 1;
  session_id: string;
  request_id: string;
  timeout_ms?: number;
  code: string;
  params?: {
    [k: string]: unknown;
  };
}
