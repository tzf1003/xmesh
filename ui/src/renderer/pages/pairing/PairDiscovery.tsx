import type { JSX } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { PageContext } from "../../app";
import { getApi } from "../../api";
import { Dialog, Empty } from "../../components";
import { useT } from "../../i18n";
import { groupFingerprint } from "../../lib/pair";
import type { ActionError } from "../../../shared/ipc";
import type { PairDiscoveredDevice } from "../../../shared/control";
import { PairConnectForm } from "./PairConnectForm";
import { PairErrorBanner } from "./common";
import { Icon } from "../../icons";
import { useCopy } from "../../design-copy";

const REFRESH_MS = 15_000;

/** Unpaired xmesh devices announcing themselves on the LAN. Each one can be connected with its password. */
export function PairDiscovery({ ctx }: { ctx: PageContext }): JSX.Element {
  const t = useT();
  const copy = useCopy();
  const [devices, setDevices] = useState<PairDiscoveredDevice[] | undefined>();
  const [err, setErr] = useState<ActionError | undefined>();
  const [open, setOpen] = useState<string | undefined>();

  const load = async (): Promise<void> => {
    const r = await getApi().discoverPairs();
    if (r.ok) {
      setDevices(r.data.devices);
      setErr(undefined);
    } else setErr(r.error);
  };
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), REFRESH_MS);
    return () => clearInterval(timer);
  }, []);

  return (
    <section class="card" data-testid="pair-discovery">
      <div class="discovery-heading"><h2>{t("pair.discoverTitle")}</h2>
        <button type="button" class="btn small" onClick={() => void load()} data-testid="pair-refresh">{t("pair.refresh")}</button>
      </div>
      <p class="hint">{t("pair.discoverIntro")}</p>
      {err && <PairErrorBanner error={err} testid="pair-discovery-error" />}
      {devices?.length === 0 && <Empty>{t("pair.noneDiscovered")}</Empty>}
      {devices && devices.length > 0 && (
        <ul class="pair-found" data-testid="pair-discovered">
          {devices.map((d) => (
            <li key={`${d.device_id}:${d.path}:${d.address}`} data-testid={`pair-device-${d.name}`}>
              <div class="discovered-device"><Icon name="device"/><div><strong>{d.name}</strong><span class="mono">{d.address}</span></div></div>
              <details class="discovery-fingerprint"><summary>{copy("证书指纹", "Certificate fingerprint")}</summary><p class="hint mono">{groupFingerprint(d.fingerprint)}</p></details>
              {open === `${d.device_id}:${d.path}:${d.address}` ? (
                <Dialog drawer title={t("pair.connect")} onClose={() => setOpen(undefined)}><p class="hint mono">{groupFingerprint(d.fingerprint)}</p><PairConnectForm
                  ctx={ctx}
                  address={d.address}
                  path={d.path}
                  fingerprint={d.fingerprint}
                  testid={`pair-connect-${d.name}`}
                  onDone={() => {
                    setOpen(undefined);
                    void load();
                  }}
                /></Dialog>
              ) : (
                <button type="button" class="btn primary" onClick={() => setOpen(`${d.device_id}:${d.path}:${d.address}`)} data-testid={`pair-open-${d.name}`}>
                  {t("pair.connect")}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
