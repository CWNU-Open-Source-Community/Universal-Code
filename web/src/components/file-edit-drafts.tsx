import { createContext, useCallback, useContext, useEffect, useId, useMemo, useReducer, useRef, useState, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { skipToken, useQuery } from "@tanstack/react-query";
import type { FileEditProposalSourceView } from "../api/types";
import { useModalFocusTrap } from "../hooks/use-modal-focus-trap";
import { useLocale } from "../lib/locale";
import { useV2RecoveryStore } from "../v2/recovery-storage";
import { getV2DraftDocument } from "../v2/draft-context";
import { DraftDocument, type DraftRef, type DraftState } from "../v2/draft-document";

type FileDraft = { version: 1; base: string; baseSHA256: string; text: string };
const snapshot = (draft: FileDraft | null) => ({ text: draft ? JSON.stringify(draft) : "", files: [], images: [] });
function validFileDraft(value: unknown): value is FileDraft {
  return !!value && typeof value === "object" && "version" in value && value.version === 1 &&
    "base" in value && typeof value.base === "string" && "text" in value && typeof value.text === "string" &&
    "baseSHA256" in value && typeof value.baseSHA256 === "string";
}
function decodeDraft(text: string): FileDraft | null {
  if (!text) return null;
  const value: unknown = JSON.parse(text);
  if (!validFileDraft(value)) throw new Error("文件草稿记录格式异常，原版本已保留。");
  return value;
}

export function useFileEditDraft(runID: string, source: FileEditProposalSourceView) {
  const store = useV2RecoveryStore();
  const key = `file-edit-draft:${JSON.stringify([source.workspace_id, runID, source.path])}`;
  const scope = useMemo(() => ({ key: `file-edit:${JSON.stringify([runID, source.path])}`,
    workspaceID: source.workspace_id }), [runID, source.path, source.workspace_id]);
  const initialDocument = useMemo(() => store ? getV2DraftDocument(store) : new DraftDocument(null), [store]);
  const { data: document = initialDocument } = useQuery<DraftDocument>({
    queryKey: ["file-edit-draft-document", key], queryFn: skipToken, gcTime: Infinity,
    initialData: initialDocument,
  });
  const [, refresh] = useReducer((value: number) => value + 1, 0);
  useEffect(() => {
    // QueryClient can retain unsaved drafts across recovery-provider remounts.
    // Listen through the current store, whose browser connection is active.
    const unsubscribe = document.subscribe(scope, refresh, store);
    refresh();
    return unsubscribe;
  }, [document, scope, store]);

  // Keep the former shared key as a migration source. All subsequent writes
  // use DraftDocument's single-writer branches and exact-version settlement.
  let error: string | undefined;
  const legacy = store?.read<unknown>(key, null);
  if (legacy && !validFileDraft(legacy)) store?.reject(key);
  try { store?.assertReadable(key); }
  catch (failure) { error = failure instanceof Error ? failure.message : String(failure); }
  const state = document.read(scope, snapshot(validFileDraft(legacy) ? legacy : null));
  let draft: FileDraft | null = null;
  let ref = state.ref;
  let versions: Array<{ ref: DraftRef; draft: FileDraft }> = [];
  try {
    draft = decodeDraft(state.snapshot.text);
    versions = state.heads.flatMap((head) => {
      const value = decodeDraft(head.snapshot.text);
      return value ? [{ ref: head.ref, draft: value }] : [];
    });
    // A settlement is an empty descendant of only the sent version. Prefer
    // any remaining edit when reopening alongside that completed branch.
    if (!draft && versions.length) ({ draft, ref } = versions[0]);
  } catch (failure) { error = failure instanceof Error ? failure.message : String(failure); }
  const readError = error;
  error ??= state.error ?? undefined;
  const requireSaved = (result: DraftState) => {
    if (!result.persisted || readError) throw new Error(readError ?? result.error ?? "文件草稿尚未保存。");
  };
  const save = (draft: FileDraft | null, requirePersistence = false) => {
    const result = document.update(scope, snapshot(draft), ref);
    if (requirePersistence) requireSaved(result);
    return result;
  };
  const text = draft?.text ?? source.content;
  return {
    draft, ref, text, persisted: state.persisted && !error, error, versions, conflict: versions.length > 1,
    headToken: state.headToken,
    resolve: (token: string, selected: DraftRef) => document.resolve(scope, token, selected),
    changedBase: Boolean(draft && draft.baseSHA256 !== source.content_sha256),
    change: (text: string) => {
      const base = draft ?? { version: 1 as const, base: source.content, baseSHA256: source.content_sha256, text };
      save(text === base.base && base.baseSHA256 === source.content_sha256 ? null : { ...base, text });
    },
    rebase: () => save(text === source.content ? null : {
      version: 1, base: source.content, baseSHA256: source.content_sha256, text,
    }),
    keep: () => save(draft, true),
    discard: () => save(null, true),
    captureSubmission: (text: string) => {
      if (draft?.text !== text) throw new Error("文件草稿已变化，请重新核对后提交。");
      return { text, ref: ref ?? save(draft).ref };
    },
    submitted: (submittedRef: DraftRef | null) => {
      if (!submittedRef) return;
      if (store) document.settle(scope, submittedRef);
      else {
        const current = document.read(scope).ref;
        if (current?.branchID === submittedRef.branchID && current.seq === submittedRef.seq) {
          document.update(scope, snapshot(null), submittedRef);
        }
      }
    },
  };
}

export type FileEditLeaveEntry = {
  path: string; dirty: boolean; busy: boolean; canSave: boolean;
  save: () => Promise<unknown>; keep: () => void; discard: () => void;
};
type LeaveRequest = (proceed: () => void, cancel?: () => void) => void;
type LeaveContext = {
  register: (id: string, entry: RefObject<FileEditLeaveEntry>) => () => void;
  request: LeaveRequest;
};
const LeaveContext = createContext<LeaveContext | null>(null);
const proceedImmediately: LeaveRequest = (proceed) => proceed();

// One guard covers file switches, panel changes and application navigation.
// Draft contents remain in the existing recovery store and scoped QueryClient.
export function FileEditDraftProvider({ children }: { children: ReactNode }) {
  const { t } = useLocale();
  const entries = useRef(new Map<string, RefObject<FileEditLeaveEntry>>());
  const proceeding = useRef(false);
  const [pending, setPending] = useState<{ entries: RefObject<FileEditLeaveEntry>[]; proceed: () => void; cancel?: () => void } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const cancel = () => { if (!busy) { pending?.cancel?.(); setPending(null); setError(""); } };
  const dialog = useModalFocusTrap<HTMLElement>(Boolean(pending), cancel, busy, undefined, { isolateBackground: true });
  const register = useCallback<LeaveContext["register"]>((id, entry) => {
    entries.current.set(id, entry);
    return () => { entries.current.delete(id); };
  }, []);
  const request = useCallback<LeaveRequest>((proceed, cancel) => {
    if (proceeding.current) { proceed(); return; }
    const dirty = [...entries.current.values()].filter(({ current }) => current.dirty || current.busy);
    if (!dirty.length) { proceed(); return; }
    setError("");
    setPending({ entries: dirty, proceed, cancel });
  }, []);
  const context = useMemo(() => ({ register, request }), [register, request]);
  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if ([...entries.current.values()].some(({ current }) => current.dirty || current.busy)) {
        event.preventDefault(); event.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, []);
  const choose = async (choice: "save" | "keep" | "discard") => {
    if (!pending || busy) return;
    setBusy(true); setError("");
    try {
      for (const entry of pending.entries) {
        if (entry.current.busy) throw new Error(t("提案正在提交，请完成后再离开。", "A proposal is being submitted. Wait before leaving."));
        if (!entry.current.dirty) continue;
        if (choice === "save" && !entry.current.canSave) throw new Error(t("请先返回编辑器处理来源变化或过期，再保存提案。", "Resolve the changed or expired source in the editor before saving."));
        await entry.current[choice]();
      }
      setPending(null);
      proceeding.current = true;
      try { pending.proceed(); } finally { proceeding.current = false; }
    } catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
    finally { setBusy(false); }
  };
  return <LeaveContext.Provider value={context}>{children}
    {pending && createPortal(<div className="v2-overlay" role="presentation">
      <section className="v2-dialog file-edit-leave-dialog" aria-labelledby="file-edit-leave-title" aria-modal="true" role="dialog" ref={dialog} tabIndex={-1}>
        <header><h2 id="file-edit-leave-title">{t("处理未提交的文件修改", "Unsubmitted file edits")}</h2></header>
        <p>{pending.entries.map(({ current }) => current.path).join("、")}</p>
        <p>{t("保存为提案后仍需审阅；也可以保留本机草稿，稍后继续编辑。", "Save a proposal for review, or keep a local draft to continue later.")}</p>
        {error && <p role="alert">{error}</p>}
        <footer>
          <button disabled={busy} onClick={cancel} type="button">{t("继续编辑", "Continue editing")}</button>
          <button disabled={busy} onClick={() => void choose("discard")} type="button">{t("放弃修改", "Discard edits")}</button>
          <button disabled={busy} onClick={() => void choose("keep")} type="button">{t("保留草稿", "Keep draft")}</button>
          <button className="primary" disabled={busy || pending.entries.some(({ current }) => current.dirty && !current.canSave)}
            onClick={() => void choose("save")} type="button">{t("保存为提案", "Save as proposal")}</button>
        </footer>
      </section>
    </div>, document.body)}
  </LeaveContext.Provider>;
}

export function useFileEditLeave() { return useContext(LeaveContext)?.request ?? proceedImmediately; }

export function useFileEditLeaveEntry(entry: FileEditLeaveEntry) {
  const context = useContext(LeaveContext);
  const id = useId();
  const ref = useRef(entry);
  ref.current = entry;
  useEffect(() => context?.register(id, ref), [context, id]);
}
