import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { type DisplayPrefs, ZOOM_STEPS, cssFamily, isFontName } from "../shared/display";
import { getApi } from "./api";
import { Dialog } from "./components";
import { useT } from "./i18n";

export const UI_FONT_FALLBACK = "var(--font-sys)";
export const CODE_FONT_FALLBACK = "var(--mono-sys)";

const UI_FONTS = ["Segoe UI Variable Text", "Segoe UI", "Microsoft YaHei UI", "PingFang SC", "SF Pro Text", "HarmonyOS Sans SC", "Noto Sans SC", "Inter", "Arial"];
const CODE_FONTS = ["Cascadia Mono", "Consolas", "SF Mono", "Menlo", "JetBrains Mono", "Fira Code", "Source Code Pro"];

/** Puts the saved preferences on the page; the zoom itself is applied by the main process. */
export function applyDisplay(prefs: DisplayPrefs): void {
  document.documentElement.dataset.theme = prefs.theme ?? "system";
  const root = document.documentElement.style;
  const ui = cssFamily(prefs.uiFont, UI_FONT_FALLBACK);
  const code = cssFamily(prefs.codeFont, CODE_FONT_FALLBACK);
  if (ui) root.setProperty("--font-ui", ui);
  else root.removeProperty("--font-ui");
  if (code) root.setProperty("--mono", code);
  else root.removeProperty("--mono");
}

export function DisplayDialog({ prefs, onClose }: { prefs: DisplayPrefs; onClose: () => void }): JSX.Element {
  const t = useT();
  const [ui, setUi] = useState(prefs.uiFont);
  const [code, setCode] = useState(prefs.codeFont);
  const valid = (v: string): boolean => v === "" || isFontName(v);
  const bad = !valid(ui) || !valid(code);
  const save = (next: Partial<DisplayPrefs>): void => void getApi().setDisplay({ ...prefs, ...next });
  return (
    <Dialog title={t("display.title")} onClose={onClose} testid="display-dialog">
      <label class="field">
        <span>{t("display.zoom")}</span>
        <div class="segs" role="group" aria-label={t("display.zoom")}>
          {ZOOM_STEPS.map((z) => (
            <button key={z} type="button" aria-pressed={prefs.zoom === z} data-testid={`zoom-${Math.round(z * 100)}`} onClick={() => save({ zoom: z })}>
              {Math.round(z * 100)}%
            </button>
          ))}
        </div>
        <span class="hint">{t("display.zoomHint")}</span>
      </label>
      <label class="field">
        <span>{t("display.uiFont")}</span>
        <input type="text" list="ui-fonts" value={ui} placeholder={t("display.fontPlaceholder")} aria-invalid={!valid(ui)} data-testid="ui-font" onInput={(e) => setUi((e.currentTarget as HTMLInputElement).value.trim())} />
        <datalist id="ui-fonts">{UI_FONTS.map((f) => <option key={f} value={f} />)}</datalist>
      </label>
      <label class="field">
        <span>{t("display.codeFont")}</span>
        <input type="text" list="code-fonts" value={code} placeholder={t("display.fontPlaceholder")} aria-invalid={!valid(code)} data-testid="code-font" onInput={(e) => setCode((e.currentTarget as HTMLInputElement).value.trim())} />
        <datalist id="code-fonts">{CODE_FONTS.map((f) => <option key={f} value={f} />)}</datalist>
        <span class="hint">{t("display.fontHint")}</span>
      </label>
      {bad && <p class="hint" role="alert" data-testid="display-invalid">{t("display.invalid")}</p>}
      <p class="card" data-testid="display-preview" style={{ fontFamily: cssFamily(ui, UI_FONT_FALLBACK) }}>
        {t("display.preview")} <code style={{ fontFamily: cssFamily(code, CODE_FONT_FALLBACK) }}>/srv/xmesh 0123</code>
      </p>
      <div class="row end">
        <button type="button" class="btn" onClick={() => { setUi(""); setCode(""); save({ zoom: 1, uiFont: "", codeFont: "" }); }} data-testid="display-reset">
          {t("display.reset")}
        </button>
        <button type="button" class="btn primary" disabled={bad || (ui === prefs.uiFont && code === prefs.codeFont)} onClick={() => save({ uiFont: ui, codeFont: code })} data-testid="display-apply">
          {t("display.apply")}
        </button>
        <button type="button" class="btn" onClick={onClose} data-testid="display-close">
          {t("display.close")}
        </button>
      </div>
    </Dialog>
  );
}
