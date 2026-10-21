import type { JSX } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, ErrorBanner, NotSupported, PageHead } from "../components";
import { useT } from "../i18n";
import { diffRows, isDirty, lineRange, needsRestart, weakening } from "../lib/config";
import { applySaved } from "../lib/reload";
import { CONFIG_MAX_BYTES, type ActionError } from "../../shared/ipc";
import type { ConfigChange, ConfigResponse } from "../../shared/control";
import { formatConfigSource } from "../../shared/config-yaml";

interface Preview {
  changes: ConfigChange[];
  /** The text the preview was computed for; saving a different text needs a new preview. */
  content: string;
}

export function Config({ ctx, onDirtyChange }: { ctx: PageContext; onDirtyChange?(dirty: boolean): void }): JSX.Element {
  const t = useT();
  const supported = ctx.can("config.read");
  const writable = ctx.can("config.write");
  const [loaded, setLoaded] = useState<ConfigResponse | undefined>();
  const [draft, setDraft] = useState("");
  const [err, setErr] = useState<ActionError | undefined>();
  const [preview, setPreview] = useState<Preview | undefined>();
  const [busy, setBusy] = useState(false);
  const [askDiscard, setAskDiscard] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  useEffect(() => { onDirtyChange?.(loaded !== undefined && isDirty(formatConfigSource(loaded.content), draft)); }, [loaded, draft, onDirtyChange]);

  const load = async (): Promise<void> => {
    const r = await getApi().getConfig();
    if (!r.ok) return setErr(r.error);
    setErr(undefined);
    const content = formatConfigSource(r.data.content);
    setLoaded(r.data);
    setDraft(content);
    setPreview(undefined);
    setAskDiscard(false);
  };
  useEffect(() => {
    if (supported) void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ctx.snapshot.selectedInstance, supported]);

  if (!supported) {
    return (
      <div class="page">
        <PageHead title={t("config.title")} />
        <NotSupported what={t("config.title")} />
      </div>
    );
  }

  const dirty = loaded !== undefined && isDirty(formatConfigSource(loaded.content), draft);
  const savable = loaded !== undefined && isDirty(loaded.content, draft);
  const tooBig = new TextEncoder().encode(draft).length > CONFIG_MAX_BYTES;

  const review = async (): Promise<void> => {
    if (!loaded) return;
    setBusy(true);
    ctx.applied(undefined);
    const content = formatConfigSource(draft);
    setDraft(content);
    const r = await getApi().putConfig({ content, etag: loaded.etag, dryRun: true });
    setBusy(false);
    if (!r.ok) return setErr(r.error);
    setErr(undefined);
    // The preview and save use the same formatted text.
    setPreview({ changes: r.data.diff, content });
  };

  const save = async (): Promise<void> => {
    if (!loaded || !preview) return;
    setBusy(true);
    const r = await getApi().putConfig({ content: preview.content, etag: loaded.etag, dryRun: false });
    setBusy(false);
    setPreview(undefined);
    if (!r.ok) return setErr(r.error);
    ctx.applied(await applySaved(r.data.restart_required));
    ctx.toast(t("config.saved"));
    await load();
  };

  const jump = (line: number | undefined): void => {
    const el = area.current;
    const range = line === undefined ? undefined : lineRange(draft, line);
    if (!el || !range) return;
    el.focus();
    el.setSelectionRange(range.start, range.end);
  };

  const conflict = err?.code === "precondition_failed";
  const rows = preview ? diffRows(preview.changes) : [];
  const weak = preview ? weakening(preview.changes) : [];

  return (
    <div class="page" data-testid="config-source">
      <PageHead title={t("config.title")} sub={t("config.sub")}>
        <div class="row">
          <button type="button" class="btn" onClick={() => (dirty ? setAskDiscard(true) : void load())} data-testid="config-reload">
            {t("config.reload")}
          </button>
          <button type="button" class="btn" onClick={() => void getApi().openPath("config")}>
            {t("overview.openConfig")}
          </button>
        </div>
      </PageHead>

      {loaded && !loaded.valid && (
        <Banner kind="danger" testid="config-invalid-disk">
          {t("config.invalidOnDisk", { message: loaded.load_error?.message ?? "", line: loaded.load_error?.line ?? "?" })}
        </Banner>
      )}
      {loaded && loaded.effective_etag !== loaded.etag && loaded.valid && <Banner kind="warn" testid="config-pending">{t("config.pending")}</Banner>}
      {askDiscard && (
        <Banner kind="warn" testid="config-discard">
          <p>{t("config.discardAsk")}</p>
          <div class="row">
            <button type="button" class="btn danger" onClick={() => void load()}>
              {t("config.discard")}
            </button>
            <button type="button" class="btn" onClick={() => setAskDiscard(false)}>
              {t("common.cancel")}
            </button>
          </div>
        </Banner>
      )}
      {err ? (
        <>
          <ErrorBanner error={err} />
          {err.diagnostics && err.diagnostics.length > 0 && (
            <Banner kind="danger" testid="config-diagnostics">
              <ul>
                {err.diagnostics.map((d, i) => (
                  <li key={i}>
                    {d.line !== undefined ? (
                      <button type="button" class="btn small" onClick={() => jump(d.line)}>
                        {t("config.line", { line: d.line })}
                      </button>
                    ) : null}{" "}
                    {d.pointer && <code>{d.pointer}</code>} {d.message}
                  </li>
                ))}
              </ul>
            </Banner>
          )}
          {conflict && (
            <div class="row">
              <button type="button" class="btn" onClick={() => void load()} data-testid="config-reload-latest">
                {t("config.loadLatest")}
              </button>
            </div>
          )}
        </>
      ) : null}

      <section class="card">
        {!writable && <Banner kind="warn" testid="config-readonly">{t("config.readOnly")}</Banner>}
        <label class="field">
          <span class="hint">{t("config.editorHint")}</span>
          <textarea
            ref={area}
            class="editor"
            value={draft}
            spellcheck={false}
            readOnly={!writable || !loaded}
            aria-label={t("config.title")}
            aria-invalid={tooBig}
            onInput={(e) => {
              setDraft((e.currentTarget as HTMLTextAreaElement).value);
              setPreview(undefined);
              ctx.applied(undefined);
            }}
            data-testid="config-editor"
          />
        </label>
        {tooBig && <Banner kind="danger">{t("config.tooBig")}</Banner>}
        <div class="row">
          <button type="button" class="btn" disabled={!writable || !loaded || busy || tooBig} onClick={() => { setDraft(formatConfigSource(draft)); setPreview(undefined); }} data-testid="config-format">
            {t("config.format")}
          </button>
          <button type="button" class="btn primary" disabled={!writable || !savable || busy || tooBig} onClick={() => void review()} data-testid="config-review">
            {t("config.review")}
          </button>
          <button type="button" class="btn" disabled={!dirty} onClick={() => { setDraft(formatConfigSource(loaded?.content ?? "")); setPreview(undefined); }}>
            {t("config.revert")}
          </button>
          {dirty && <Badge kind="warn">{t("config.unsaved")}</Badge>}
        </div>
      </section>

      {preview && (
        <Confirm
          title={t("config.diffTitle")}
          confirmLabel={t("config.save")}
          danger={weak.length > 0}
          busy={busy}
          onCancel={() => setPreview(undefined)}
          onConfirm={() => void save()}
          body={
            <div data-testid="config-diff">
              {weak.length > 0 && (
                <Banner kind="danger" testid="config-weakens">
                  <p>{t("config.weakens")}</p>
                  <p class="hint">{t("config.weakensNote")}</p>
                </Banner>
              )}
              {needsRestart(preview.changes) && <Banner kind="warn" testid="config-needs-restart">{t("config.needsRestart")}</Banner>}
              {rows.length === 0 && <Banner kind="ok" testid="config-comments-only">{t("config.commentsOnly")}</Banner>}
              {rows.length > 0 && <div class="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th scope="col">{t("config.path")}</th>
                      <th scope="col">{t("config.before")}</th>
                      <th scope="col">{t("config.after")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((r, i) => (
                      <tr key={i}>
                        <td class="mono">
                          <span class={`diff-${r.op}`}>{r.op === "add" ? "+" : r.op === "remove" ? "−" : "~"}</span> {r.pointer}{" "}
                          {r.weakens && <Badge kind="danger">{t("config.weakensTag")}</Badge>} {r.restartRequired && <Badge kind="warn">{t("config.restartTag")}</Badge>}
                        </td>
                        <td class="mono">{r.before}</td>
                        <td class="mono">{r.after}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>}
            </div>
          }
        />
      )}
    </div>
  );
}
