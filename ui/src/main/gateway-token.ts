// Where the gateway token for the copied connect commands comes from. The kernel never returns a token
// except once, at creation or rotation; afterwards the token file is the only copy, and this module reads
// it in the main process. The renderer gets a boolean-like result at most, never the token body.

import * as fs from "node:fs";
import * as path from "node:path";
import type { PatInfo, PatList } from "../shared/control";
import { isSafeToken } from "../shared/gateway";
import type { ShellController } from "./controller";

/** A token file that cannot be used. The message is for the log and the tray, not for the page. */
export class TokenUnavailableError extends Error {
  constructor(reason: string) {
    super(`gateway token unavailable: ${reason}`);
    this.name = "TokenUnavailableError";
  }
}

/** The slice of `fs` the reader uses, so tests can drive it without touching the disk. */
export interface TokenFiles {
  lstat(p: string): { isFile(): boolean; size: number };
  readFile(p: string): string;
}

export const realTokenFiles: TokenFiles = {
  lstat: (p) => fs.lstatSync(p),
  readFile: (p) => fs.readFileSync(p, "utf8"),
};

/** A token is a few dozen characters; anything much larger is not a token file. */
const MAX_TOKEN_FILE_BYTES = 4096;

/** The PAT rotation and the upgrade notice follow. The copied commands carry its token too, so rotating it updates them. */
export const DEFAULT_PAT_NAME = "default";

/** The first PAT by name, compared as plain strings. */
export function firstPatByName(pats: readonly PatInfo[]): PatInfo | undefined {
  let first: PatInfo | undefined;
  for (const p of pats) if (first === undefined || p.name < first.name) first = p;
  return first;
}

/** The PAT the copied commands use: "default" when it exists, otherwise the first PAT by name. */
export function copyPat(pats: readonly PatInfo[]): PatInfo | undefined {
  return pats.find((p) => p.name === DEFAULT_PAT_NAME) ?? firstPatByName(pats);
}

/** The token in `file`, trimmed. Refuses anything that is not a regular file holding one safe token. */
export function readTokenFile(file: string, files: TokenFiles = realTokenFiles): string {
  if (!path.isAbsolute(file)) throw new TokenUnavailableError("token path is not absolute");
  let st: { isFile(): boolean; size: number };
  try {
    st = files.lstat(file);
  } catch {
    throw new TokenUnavailableError("token file is missing");
  }
  // lstat, not stat: a symlink is not a regular file and is refused.
  if (!st.isFile()) throw new TokenUnavailableError("token path is not a regular file");
  if (st.size === 0 || st.size > MAX_TOKEN_FILE_BYTES) throw new TokenUnavailableError("token file is empty or too large");
  let text: string;
  try {
    text = files.readFile(file).trim();
  } catch {
    throw new TokenUnavailableError("token file cannot be read");
  }
  if (text === "") throw new TokenUnavailableError("token file is empty");
  if (!isSafeToken(text)) throw new TokenUnavailableError("token file holds characters a connect command cannot carry");
  return text;
}

/** Reads the gateway token through the panel's own kernel connection (so it needs the `pats` capability). */
export class GatewayCredentials {
  constructor(
    private readonly controller: Pick<ShellController, "requireCapable">,
    private readonly files: TokenFiles = realTokenFiles,
  ) {}

  async read(name?: string): Promise<string> {
    let list: unknown;
    try {
      list = await this.controller.requireCapable("pats").call("GET", "/v2/pats");
    } catch (e) {
      throw new TokenUnavailableError(e instanceof Error ? e.message : String(e));
    }
    const pats = (list as Partial<PatList> | null)?.pats;
    const pat = Array.isArray(pats) ? (name === undefined ? copyPat(pats) : pats.find((p) => p.name === name)) : undefined;
    if (pat === undefined) throw new TokenUnavailableError(name === undefined ? "the kernel has no PAT" : "the selected PAT is unavailable");
    return readTokenFile(pat.token_file, this.files);
  }
}
