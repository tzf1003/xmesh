import { useEffect, useRef, useState } from "preact/hooks";
import { getApi } from "../api";
import type { ActionError } from "../../shared/ipc";
import type { MeshPolicy, MeshPolicyDocument, PolicyDocumentEdit } from "../../shared/mesh";

/** One owner for the cloud baseline and the draft shared by the policy and DERP tabs. */
export function usePolicyDraft(identity: string) {
  const [base, setBase] = useState<MeshPolicy>();
  const [document, setDocument] = useState<MeshPolicyDocument>();
  const [text, setText] = useState("");
  const textRef = useRef("");
  const [error, setError] = useState<ActionError>();
  const [busy, setBusy] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const publishingRef = useRef(false);
  const sequence = useRef(0);
  const baseRef = useRef(base); baseRef.current = base;
  const documentRef = useRef(document); documentRef.current = document;

  const load = async (): Promise<boolean> => {
    const seq = ++sequence.current;
    setBusy(true); setError(undefined);
    const cloud = await getApi().mesh({ op: "policy.show" });
    if (seq !== sequence.current) return false;
    if (!cloud.ok) { setBusy(false); setError(cloud.error); return false; }
    const parsed = await getApi().mesh({ op: "policy.document", edit: { text: cloud.data.exists ? cloud.data.text : "{}\n" } });
    if (seq !== sequence.current) return false;
    setBusy(false);
    if (!parsed.ok) { setError(parsed.error); return false; }
    // The baseline and its derived document enter the workspace together.
    const baseline = { ...cloud.data, version: cloud.data.exists ? cloud.data.version : "absent" };
    setBase(baseline); baseRef.current = baseline;
    setDocument(parsed.data); documentRef.current = parsed.data;
    setText(parsed.data.text); textRef.current = parsed.data.text;
    return true;
  };

  useEffect(() => {
    setBase(undefined); setDocument(undefined);
    setText(""); textRef.current = "";
    baseRef.current = undefined; documentRef.current = undefined;
    void load();
    return () => { ++sequence.current; };
  }, [identity]);

  const edit = async (change: Omit<PolicyDocumentEdit, "text"> & { text?: string }): Promise<boolean> => {
    if (publishingRef.current) return false;
    if (!baseRef.current || !documentRef.current) return false;
    const seq = ++sequence.current;
    setBusy(true); setError(undefined);
    const r = await getApi().mesh({ op: "policy.document", edit: { ...change, text: change.text ?? textRef.current } });
    if (seq !== sequence.current) return false;
    setBusy(false);
    if (!r.ok) { setError(r.error); return false; }
    setDocument(r.data); documentRef.current = r.data;
    setText(r.data.text); textRef.current = r.data.text;
    return true;
  };
  const dirty = base !== undefined && text !== (base.exists ? base.text : "{}\n");
  useEffect(() => {
    if (!dirty) return;
    const leave = (e: BeforeUnloadEvent): void => { e.preventDefault(); e.returnValue = ""; };
    window.addEventListener("beforeunload", leave);
    return () => window.removeEventListener("beforeunload", leave);
  }, [dirty]);
  const writeText = (value: string): void => {
    if (publishingRef.current) return;
    ++sequence.current;
    setBusy(false); setError(undefined); setText(value); textRef.current = value;
  };
  const lock = (value: boolean): void => { publishingRef.current = value; setPublishing(value); };
  const isCurrent = (value: string, version: string): boolean => textRef.current === value && baseRef.current?.version === version;
  return { base, document, text, writeText, error, busy: busy || publishing, dirty, load, edit, lock, isCurrent };
}

export type PolicyDraft = ReturnType<typeof usePolicyDraft>;
