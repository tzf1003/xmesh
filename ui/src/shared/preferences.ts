import type { Locale } from "./i18n";

export interface AppPreferences { locale: Locale; closeToTray: boolean; firstRunSeen: boolean }
export const DEFAULT_APP_PREFERENCES: AppPreferences = { locale: "zh", closeToTray: true, firstRunSeen: false };
export function parseAppPreferences(value: unknown): AppPreferences | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const v = value as Record<string, unknown>;
  if (Object.keys(v).some(k => !["locale", "closeToTray", "firstRunSeen"].includes(k)) || !["zh", "en"].includes(v.locale as string) || typeof v.closeToTray !== "boolean" || typeof v.firstRunSeen !== "boolean") return undefined;
  return { locale: v.locale as Locale, closeToTray: v.closeToTray, firstRunSeen: v.firstRunSeen };
}
