import type { JSX } from "preact";
import { Banner } from "../../components";
import { errorText, useT } from "../../i18n";
import { ciFailure } from "../../lib/ci";
import type { ActionError } from "../../../shared/ipc";

/** A failed CI request: the panel's own sentence for the reasons it knows, else the kernel's message. */
export function CiError({ error, testid = "ci-error" }: { error: ActionError; testid?: string }): JSX.Element {
  const t = useT();
  const key = ciFailure(error);
  return (
    <Banner kind="danger" testid={testid}>
      {key !== undefined ? t(key) : errorText(t, error)}
    </Banner>
  );
}

/** A command to run in a terminal. A click selects all of it, so it can be copied without a clipboard permission. */
export function Command({ text, testid }: { text: string; testid?: string }): JSX.Element {
  const select = (e: JSX.TargetedMouseEvent<HTMLElement>): void => {
    const sel = window.getSelection();
    if (sel === null) return;
    sel.selectAllChildren(e.currentTarget);
  };
  return (
    <code class="command" onClick={select} data-testid={testid} tabIndex={0}>
      {text}
    </code>
  );
}
