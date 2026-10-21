import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { getApi } from "../../api";
import { Banner, Field } from "../../components";
import { useT } from "../../i18n";
import { TTL_CHOICES, parseTagText } from "../../lib/mesh";
import type { ActionError } from "../../../shared/ipc";
import type { MeshKeyResult } from "../../../shared/mesh";
import type { MeshTabProps } from "../Mesh";
import { MeshError } from "./common";

/**
 * Creates a pre-auth key for a new device. The CLI writes the key to a file
 * (0600) that the user picked; this page never sees the secret, only the path.
 */
export function MeshOnboard({ ctx, status }: MeshTabProps): JSX.Element {
  const t = useT();
  const headscale = status.provider === "headscale";
  const [tags, setTags] = useState("");
  const [ttl, setTtl] = useState<(typeof TTL_CHOICES)[number]>("1h");
  const [ephemeral, setEphemeral] = useState(false);
  const [user, setUser] = useState("");
  const [file, setFile] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const [made, setMade] = useState<MeshKeyResult | undefined>();
  const parsed = parseTagText(tags, true);
  const userOk = !headscale || /^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$/.test(user);

  const pick = async (): Promise<void> => {
    const r = await getApi().pickPath("authkeySave");
    if (r.ok && r.data.path) setFile(r.data.path);
  };

  const create = async (): Promise<void> => {
    if (!parsed.ok || file === "") return;
    setBusy(true);
    setErr(undefined);
    setMade(undefined);
    // The native save dialog already asked before replacing an existing file.
    const r = await getApi().mesh({ op: "onboard", tags: parsed.tags, ttl, ephemeral, ...(headscale && { user }), file, overwrite: true });
    setBusy(false);
    if (r.ok) {
      setMade(r.data);
      ctx.toast(t("mesh.onboard.created"));
    } else setErr(r.error);
  };

  return (
    <section class="card" data-testid="mesh-onboard">
      <h2>{t("mesh.onboard.title")}</h2>
      <p class="hint">{t("mesh.onboard.intro")}</p>
      <Field label={t("mesh.onboard.tags")} hint={t("mesh.onboard.tagsHint")}>
        <input type="text" value={tags} onInput={(e) => setTags((e.currentTarget as HTMLInputElement).value)} aria-invalid={!parsed.ok} autocomplete="off" data-testid="mesh-onboard-tags" />
      </Field>
      <Field label={t("mesh.onboard.ttl")}>
        <select value={ttl} onChange={(e) => setTtl((e.currentTarget as HTMLSelectElement).value as typeof ttl)} data-testid="mesh-onboard-ttl">
          {TTL_CHOICES.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
      </Field>
      {headscale && (
        <Field label={t("mesh.onboard.user")} hint={t("mesh.onboard.userHint")}>
          <input type="text" value={user} onInput={(e) => setUser((e.currentTarget as HTMLInputElement).value)} aria-invalid={!userOk} autocomplete="off" data-testid="mesh-onboard-user" />
        </Field>
      )}
      <label class="check">
        <input type="checkbox" checked={ephemeral} onChange={(e) => setEphemeral((e.currentTarget as HTMLInputElement).checked)} data-testid="mesh-onboard-ephemeral" />
        <span>
          <strong>{t("mesh.onboard.ephemeral")}</strong>
          <br />
          <span class="hint">{t("mesh.onboard.ephemeralHint")}</span>
        </span>
      </label>
      <Field label={t("mesh.onboard.file")} hint={t("mesh.onboard.fileHint")}>
        <div class="row">
          <input type="text" readOnly value={file} placeholder="—" aria-label={t("mesh.onboard.file")} data-testid="mesh-onboard-file" />
          <button type="button" class="btn" onClick={() => void pick()} data-testid="mesh-onboard-pick">
            {t("wizard.choose")}
          </button>
        </div>
      </Field>
      {!parsed.ok && <Banner kind="danger" testid="mesh-onboard-invalid">{t("mesh.tags.invalid")}</Banner>}
      {err && <MeshError error={err} toast={ctx.toast} />}
      <div class="row">
        <button type="button" class="btn primary" disabled={!parsed.ok || file === "" || !userOk || busy} onClick={() => void create()} data-testid="mesh-onboard-create">
          {t("mesh.onboard.create")}
        </button>
      </div>
      {made && (
        <Banner kind="ok" testid="mesh-onboard-done">
          <p>{t("mesh.onboard.done", { id: made.id || "—" })}</p>
          <p>
            {t("mesh.onboard.writtenTo")} <code data-testid="mesh-onboard-path">{made.file}</code>
          </p>
          <p>{t("mesh.onboard.next", { file: made.file })}</p>
        </Banner>
      )}
    </section>
  );
}
