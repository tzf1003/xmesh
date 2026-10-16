import { createContext } from "preact";
import { useContext } from "preact/hooks";
import type { ActionError } from "../shared/ipc";
import type { Locale } from "../shared/i18n";
import { en, zh, type Key } from "./messages";

export type T = (key: Key, params?: Record<string, string | number>) => string;

export function makeT(locale: Locale): T {
  const table: Record<Key, string> = locale === "en" ? en : zh;
  return (key, params) => {
    let s = table[key];
    if (params) for (const [k, v] of Object.entries(params)) s = s.split(`{${k}}`).join(String(v));
    return s;
  };
}

export const I18nContext = createContext<{ t: T; locale: Locale }>({ t: makeT("zh"), locale: "zh" });

export function useT(): T {
  return useContext(I18nContext).t;
}

export function useLocale(): Locale {
  return useContext(I18nContext).locale;
}

const ERROR_KEYS: Record<string, Key> = {
  unavailable: "err.unavailable",
  not_implemented: "err.not_implemented",
  role_disabled: "err.role_disabled",
  unsupported_mode: "err.unsupported_mode",
  precondition_failed: "err.precondition_failed",
  precondition_required: "err.precondition_required",
  config_invalid: "err.config_invalid",
  fingerprint_mismatch: "err.fingerprint_mismatch",
  conflict: "err.conflict",
  not_found: "err.not_found",
  invalid_args: "err.invalid_args",
  forbidden: "err.forbidden",
  keychain_unavailable: "err.keychain_unavailable",
  payload_too_large: "err.payload_too_large",
};

/** A sentence for an error. Known codes get the panel's own wording; others show the kernel's message. */
export function errorText(t: T, e: ActionError): string {
  if (e.code === "conflict" && e.reason === "drain_downgrade") return t("err.drain_downgrade");
  if (e.code === "conflict" && e.reason === "password_source_not_file") return t("err.password_source_not_file");
  const key = ERROR_KEYS[e.code];
  if (key !== undefined && e.code !== "conflict" && e.code !== "not_found" && e.code !== "invalid_args") return t(key);
  return e.message || (key !== undefined ? t(key) : e.code);
}
