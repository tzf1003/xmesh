import type { JSX } from "preact";
import type { PageContext } from "../app";
import { Badge, Banner, Empty, NotSupported, PageHead } from "../components";
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
      {lan && ctx.can("pair") ? (
        <>
          <PairPassword ctx={ctx} />
          <PairDiscovery ctx={ctx} />
          <PairManual ctx={ctx} />
          <PairDocuments ctx={ctx} />
        </>
      ) : ctx.can("pair") ? (
        <Banner>{t("pair.lanOnly")}</Banner>
      ) : (
        <NotSupported what={t("pair.title")} />
      )}
    </div>
  );
}

function PeerRow({ peer: p }: { peer: Peer }): JSX.Element {
  const t = useT();
  const when = ago(p.last_seen);
  const info = deviceInfoState(p);
  return (
    <tr data-testid={`peer-${p.name}`}>
      <td>
        <strong>{p.name}</strong> {p.self && <Badge kind="info">{t("devices.self")}</Badge>}
        <div class="hint">{[p.os, p.version].filter(Boolean).join(" · ")}</div>
        {info === "pending" && <div class="hint" data-testid={`peer-info-${p.name}`}>{t("devices.infoPending")}</div>}
        {info === "failed" && <div class="hint mono" data-testid={`peer-info-${p.name}`}>{p.info?.error}</div>}
      </td>
      <td>
        {p.online ? <Badge kind="ok">{t("devices.online")}</Badge> : <Badge>{t("devices.offline")}</Badge>}
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
