import type { JSX } from "preact";
import type { PageContext } from "../../app";
import { useT } from "../../i18n";

export function DeviceSelect({
  ctx,
  value,
  disabled,
  onChange,
}: {
  ctx: PageContext;
  value: string;
  disabled?: boolean;
  onChange(device: string): void;
}): JSX.Element {
  const t = useT();
  const c = ctx.snapshot.connection;
  return (
    <label class="field">
      <span>{t("ws.device")}</span>
      <select
        value={value}
        disabled={disabled}
        data-testid="workspace-device"
        onChange={(e) => onChange(e.currentTarget.value)}
      >
        <option value="local">{t("pg.local")}</option>
        {c.kind === "connected" &&
          c.peers
            .filter((p) => !p.self)
            .map((p) => (
              <option key={p.name} value={p.name} disabled={!p.online}>
                {p.name}
                {p.online ? "" : ` · ${t("pg.offline")}`}
              </option>
            ))}
      </select>
    </label>
  );
}
