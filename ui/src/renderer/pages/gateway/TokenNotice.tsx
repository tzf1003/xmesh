import type { JSX } from "preact";
import { useState } from "preact/hooks";
import { Banner } from "../../components";
import { useT } from "../../i18n";
import { noticeDue, noticeStorage, readSeen, writeSeen } from "../../lib/token-notice";
import type { PatInfo } from "../../../shared/control";
import type { GatewayCopyKind } from "../../../shared/ipc";

/**
 * Shown once per token file change, so people whose configs predate the token learn to update them.
 * Dismissing it remembers the PAT's modified_at for this viewer; the kernel stays the source of truth.
 */
export function TokenNotice({
  pat,
  url,
  onCopy,
}: {
  pat: PatInfo;
  /** The localhost address: this machine's own clients use it. */
  url: string;
  onCopy: (kind: GatewayCopyKind, url: string) => void;
}): JSX.Element | null {
  const t = useT();
  const [dismissedAt, setDismissedAt] = useState<string | undefined>(undefined);
  const store = noticeStorage();
  const at = pat.modified_at;
  if (at === null || at === dismissedAt || !noticeDue(pat, readSeen(store, pat.name))) return null;
  const dismiss = (): void => {
    // Hidden for this session even when the value cannot be stored.
    writeSeen(store, pat.name, at);
    setDismissedAt(at);
  };
  return (
    <Banner testid="token-notice">
      <p>
        <strong>{t("notice.gatewayToken")}</strong>
      </p>
      <div class="row">
        <button type="button" class="btn small" onClick={() => onCopy("gatewayUrl", url)} data-testid="token-notice-copy-url">
          {t("overview.copyUrl")}
        </button>
        <button type="button" class="btn small" onClick={() => onCopy("claudeCodeCommand", url)} data-testid="token-notice-copy-claude">
          {t("overview.copyClaude")}
        </button>
        <button type="button" class="btn small" onClick={() => onCopy("codexConfig", url)} data-testid="token-notice-copy-codex">
          {t("overview.copyCodex")}
        </button>
        <button type="button" class="btn small primary" onClick={dismiss} data-testid="token-notice-dismiss">
          {t("notice.gatewayTokenDismiss")}
        </button>
      </div>
    </Banner>
  );
}
