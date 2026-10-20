import { useState } from "preact/hooks";
import type { JSX } from "preact";
import type { PageContext } from "../app";
import { Badge, Confirm, Empty, NotSupported, PageHead } from "../components";
import { getApi } from "../api";
import { useT } from "../i18n";
import { ago, formatTime } from "../lib/format";
import { deviceInfoState, lacksDeviceTag, relayStats, sortPeers } from "../lib/devices";
import type { Peer } from "../../shared/control";
import { PairDocuments } from "./pairing/PairDocuments";
import { PairDiscovery } from "./pairing/PairDiscovery";
import { PairManual } from "./pairing/PairConnectForm";
import { PairPassword } from "./pairing/PairPassword";

export function Devices({ ctx }: { ctx: PageContext }): JSX.Element | null {
  const t = useT();
  const c = ctx.snapshot.connection;
  if (c.kind !== "connected") return null;
  const peers = sortPeers(c.peers);
  const stats = relayStats(c.peers);
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
                  <PeerRow key={p.device_id || p.node_id || p.name} peer={p} ctx={ctx} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {ctx.can("pair") ? (
        <>
          <PairPassword ctx={ctx} />
          <PairDiscovery ctx={ctx} />
          <PairManual ctx={ctx} />
          <PairDocuments ctx={ctx} />
        </>
      ) : (
        <NotSupported what={t("pair.title")} />
      )}
    </div>
  );
}

function PeerRow({ peer: p, ctx }: { peer: Peer; ctx: PageContext }): JSX.Element {
  const t = useT();
  const [revoke, setRevoke] = useState(false);
  const [busy, setBusy] = useState(false);
  const when = ago(p.last_seen);
  const info = deviceInfoState(p);
  return (
    <tr data-testid={`peer-${p.name}`}>
      <td>
        <strong>{p.display_name || p.name}</strong> {p.self && <Badge kind="info">{t("devices.self")}</Badge>}
        {p.device_id && <div class="hint mono" title={p.device_id}>{p.device_id.slice(0, 16)}…</div>}
        <div class="hint">{[p.os, p.version].filter(Boolean).join(" · ")}</div>
        {info === "pending" && <div class="hint" data-testid={`peer-info-${p.name}`}>{t("devices.infoPending")}</div>}
        {info === "failed" && <div class="hint mono" data-testid={`peer-info-${p.name}`}>{p.info?.error}</div>}
      </td>
      <td>
        {p.connection_state === "reconnecting" ? <Badge kind="warn">{t("device.reconnecting")}</Badge> : p.connection_state === "identity_conflict" ? <Badge kind="danger">{t("device.conflict")}</Badge> : p.online ? <Badge kind="ok">{t("devices.online")}</Badge> : <Badge>{t("devices.offline")}</Badge>}
        {info === "failed" && <> <Badge kind="warn">{t("devices.infoFailed")}</Badge></>}
      </td>
      <td>
        <div class="row">
          {p.tags.map((tag) => (
            <Badge key={tag}>{tag}</Badge>
          ))}
          {lacksDeviceTag(p) && <Badge kind="warn">{t("devices.noDeviceTag")}</Badge>}
        </div>
      </td>
      <td>
        {p.active_path && <Badge kind="info">{t(p.active_path === "lan" ? "mode.lan" : "mode.tailnet")}</Badge>}
        {p.paths?.filter((path) => path.reachable && path.kind !== p.active_path).map((path) => <div class="hint" key={`${path.kind}:${path.address}`}>{t("device.standby", {path: t(path.kind === "lan" ? "mode.lan" : "mode.tailnet")})}</div>)}
        {p.connection === "direct" && <Badge kind="ok">{t("conn.direct")}</Badge>}
        {p.connection === "derp" && <Badge kind="warn">{t("conn.derp", { region: p.derp_region ?? "?" })}</Badge>}
        {p.connection === "local" && <Badge>{t("conn.local")}</Badge>}
        {p.connection === "unknown" && <Badge>{t("conn.unknown")}</Badge>}
      </td>
      <td class="mono">{p.addrs.join(", ")}</td>
      <td title={formatTime(p.last_seen)}><div>{when ? t(`ago.${when.unit}`, { n: when.n }) : "—"}</div>
        {!p.self && p.device_id && ctx.can("pair") && <button type="button" class="text-button" onClick={() => setRevoke(true)} data-testid={`device-revoke-${p.name}`}>{t("device.revoke")}</button>}
        {revoke && <Confirm danger confirmLabel={t("device.revoke")} title={t("device.revoke")} body={t("device.revokeHint", {name: p.display_name || p.name})} busy={busy} onCancel={() => setRevoke(false)} onConfirm={() => { setBusy(true); void getApi().revokeDevice(p.device_id!).then((result) => {setBusy(false); setRevoke(false); ctx.toast(result.ok ? t("device.revoked") : result.error.message, !result.ok);}); }}/>}
      </td>
    </tr>
  );
}
