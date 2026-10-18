import type { JSX } from "preact";
import { Banner } from "../../components";
import { errorText, useT } from "../../i18n";
import { pairFailure } from "../../lib/pair";
import type { ActionError } from "../../../shared/ipc";

/** A failed pairing worded by the pairing messages first, then by the generic error text. */
export function PairErrorBanner({ error, testid = "pair-error" }: { error: ActionError; testid?: string }): JSX.Element {
  const t = useT();
  const failure = pairFailure(error);
  return <Banner kind="danger" testid={testid}>{failure ? t(failure.key, failure.params) : errorText(t, error)}</Banner>;
}
