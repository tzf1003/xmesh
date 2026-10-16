import type { JSX } from "preact";
import { useState } from "preact/hooks";
import type { PageContext } from "../app";
import { getApi } from "../api";
import { Badge, Banner, Confirm, Empty, ErrorBanner, Field, NotSupported, PageHead } from "../components";
import { useT } from "../i18n";
import { ago, formatTime } from "../lib/format";
import { lacksDeviceTag, relayStats, sortPeers } from "../lib/devices";
import { PAIR_TAG_CHOICES, fingerprintMatches, groupFingerprint, parsePairText } from "../lib/pair";
import type { ActionError } from "../../shared/ipc";
import type { Peer, PairDocument } from "../../shared/control";

export function Devices({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  const c = ctx.snapshot.connection;
  if (c.kind !== "connected") return null;
  const peers = sortPeers(c.peers);
  const stats = relayStats(c.peers);
  const lan = c.status.network.mode === "lan";
  return (
    <div class="page" data-testid="page-devices">
      <PageHead title={t("nav.devices")} sub={t("devices.sub", { online: c.status.peers.online, total: c.status.peers.total })}>
        <div class="row">
          <Badge kind="ok">{t("devices.direct", { n: stats.direct })}</Badge>
          <Badge kind={stats.derp > 0 ? "warn" : ""}>{t("devices.relayed", { n: stats.derp })}</Badge>
        </div>
      </PageHead>
      {!ctx.can("peers") && <NotSupported what={t("nav.devices")} />}
      <section class="card">
        {peers.length === 0 ? (
          <Empty>{t("devices.none")}</Empty>
        ) : (
          <div class="table-wrap">
            <table data-testid="peers-table">
              <thead>
                <tr>
                  <th scope="col">{t("col.name")}</th>
                  <th scope="col">{t("col.status")}</th>
                  <th scope="col">{t("col.tags")}</th>
                  <th scope="col">{t("col.connection")}</th>
                  <th scope="col">{t("col.addrs")}</th>
                  <th scope="col">{t("col.lastSeen")}</th>
                </tr>
              </thead>
              <tbody>
                {peers.map((p) => (
                  <PeerRow key={p.node_id || p.name} peer={p} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {lan && ctx.can("pair") ? <Pairing ctx={ctx} /> : ctx.can("pair") ? <Banner>{t("pair.lanOnly")}</Banner> : <NotSupported what={t("pair.title")} />}
    </div>
  );
}

function PeerRow({ peer: p }: { peer: Peer }): JSX.Element {
  const t = useT();
  const when = ago(p.last_seen);
  return (
    <tr data-testid={`peer-${p.name}`}>
      <td>
        <strong>{p.name}</strong> {p.self && <Badge kind="info">{t("devices.self")}</Badge>}
        <div class="hint">{[p.os, p.version].filter(Boolean).join(" · ")}</div>
      </td>
      <td>{p.online ? <Badge kind="ok">{t("devices.online")}</Badge> : <Badge>{t("devices.offline")}</Badge>}</td>
      <td>
        <div class="row">
          {p.tags.map((tag) => (
            <Badge key={tag}>{tag}</Badge>
          ))}
          {lacksDeviceTag(p) && <Badge kind="warn">{t("devices.noDeviceTag")}</Badge>}
        </div>
      </td>
      <td>
        {p.connection === "direct" && <Badge kind="ok">{t("conn.direct")}</Badge>}
        {p.connection === "derp" && <Badge kind="warn">{t("conn.derp", { region: p.derp_region ?? "?" })}</Badge>}
        {p.connection === "local" && <Badge>{t("conn.local")}</Badge>}
        {p.connection === "unknown" && <Badge>{t("conn.unknown")}</Badge>}
      </td>
      <td class="mono">{p.addrs.join(", ")}</td>
      <td title={formatTime(p.last_seen)}>{when ? t(`ago.${when.unit}`, { n: when.n }) : "—"}</td>
    </tr>
  );
}

function Pairing({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const [mine, setMine] = useState<PairDocument | undefined>();
  const [mineErr, setMineErr] = useState<ActionError | undefined>();
  const exportMine = async (): Promise<void> => {
    const r = await getApi().exportPair();
    if (r.ok) {
      setMine(r.data.pair);
      setMineErr(undefined);
    } else setMineErr(r.error);
  };

  const [text, setText] = useState("");
  const [typed, setTyped] = useState("");
  const [tags, setTags] = useState<string[]>(["tag:peer", "tag:device"]);
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<ActionError | undefined>();
  const [done, setDone] = useState<{ name: string; restart: boolean } | undefined>();
  const parsed = text.trim() === "" ? undefined : parsePairText(text);
  const matches = parsed?.ok === true && fingerprintMatches(typed, parsed.pair.fingerprint);

  const doImport = async (): Promise<void> => {
    if (parsed?.ok !== true) return;
    setBusy(true);
    const r = await getApi().importPair({ pair: parsed.pair, fingerprint: typed.trim(), tags });
    setBusy(false);
    setConfirm(false);
    if (r.ok) {
      setErr(undefined);
      setDone({ name: r.data.name, restart: r.data.restart_required });
      setText("");
      setTyped("");
      ctx.toast(t("pair.imported", { name: r.data.name }));
    } else setErr(r.error);
  };

  return (
    <section class="card" data-testid="pairing">
      <h2>{t("pair.title")}</h2>
      <p class="hint">{t("pair.intro")}</p>
      <div class="row">
        <button type="button" class="btn" onClick={() => void exportMine()} data-testid="pair-export">
          {t("pair.export")}
        </button>
      </div>
      {mineErr && <ErrorBanner error={mineErr} />}
      {mine && (
        <div style={{ marginTop: "8px" }}>
          <p>
            {t("pair.myFingerprint")}: <code data-testid="pair-my-fingerprint">{groupFingerprint(mine.fingerprint)}</code>
          </p>
          <textarea readOnly rows={6} value={JSON.stringify({ pair: mine }, null, 2)} onFocus={(e) => (e.currentTarget as HTMLTextAreaElement).select()} aria-label={t("pair.myDocument")} data-testid="pair-my-doc" />
          <p class="hint">{t("pair.copyHint")}</p>
        </div>
      )}
      <hr />
      <h3>{t("pair.importTitle")}</h3>
      <Field label={t("pair.paste")}>
        <textarea rows={6} value={text} onInput={(e) => setText((e.currentTarget as HTMLTextAreaElement).value)} aria-invalid={parsed?.ok === false} data-testid="pair-paste" />
      </Field>
      {parsed?.ok === false && <Banner kind="danger">{t("pair.badDocument")}</Banner>}
      {parsed?.ok === true && (
        <>
          <p>
            {t("pair.peerName")}: <strong data-testid="pair-peer-name">{parsed.pair.name}</strong>
          </p>
          <fieldset>
            <legend>{t("pair.tags")}</legend>
            {PAIR_TAG_CHOICES.map((tag) => (
              <label class="check" key={tag}>
                <input
                  type="checkbox"
                  checked={tags.includes(tag)}
                  onChange={(e) => setTags((cur) => ((e.currentTarget as HTMLInputElement).checked ? [...cur, tag] : cur.filter((x) => x !== tag)))}
                />
                <span>{tag}</span>
              </label>
            ))}
          </fieldset>
          <Field label={t("pair.typeFingerprint")} hint={t("pair.typeFingerprintHint")}>
            <input type="text" value={typed} onInput={(e) => setTyped((e.currentTarget as HTMLInputElement).value)} autocomplete="off" spellcheck={false} aria-invalid={typed !== "" && !matches} data-testid="pair-typed" />
          </Field>
          {typed !== "" && !matches && <p class="hint" data-testid="pair-mismatch">{t("pair.mismatch")}</p>}
          <button type="button" class="btn primary" disabled={!matches || tags.length === 0 || busy} onClick={() => setConfirm(true)} data-testid="pair-import">
            {t("pair.import")}
          </button>
        </>
      )}
      {err && <ErrorBanner error={err} />}
      {done && (
        <Banner kind="ok" testid="pair-done">
          {t(done.restart ? "pair.doneRestart" : "pair.done", { name: done.name })}
        </Banner>
      )}
      {confirm && parsed?.ok === true && (
        <Confirm
          title={t("pair.confirmTitle", { name: parsed.pair.name })}
          body={
            <>
              <p>{t("pair.confirmBody")}</p>
              <p>
                <code>{groupFingerprint(parsed.pair.fingerprint)}</code>
              </p>
              <p>{tags.join(", ")}</p>
            </>
          }
          confirmLabel={t("pair.import")}
          busy={busy}
          onCancel={() => setConfirm(false)}
          onConfirm={() => void doImport()}
        />
      )}
    </section>
  );
}
