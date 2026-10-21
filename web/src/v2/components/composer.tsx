import { localUIMessage } from "../../lib/local-ui-message";
import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowUp, LoaderCircle, Paperclip, ClipboardPaste, FolderOpen, Database, Command, SquarePen } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import { useLocale } from "../../lib/locale";
import type { WorkspaceView } from "../../api/types";
import { V2ModelRouteControl, type V2PendingModelRoute } from "./model-route-control";
import { V2PermissionControl } from "./permission-control";
import { V2ComposerAddMenu, type ComposerAddAction } from "./composer-add-menu";
import { V2FileContext, useV2FileReferences, type V2FileReference } from "./file-context";
import { useV2ThreadSubmissions, V2SubmissionError, v2TurnOutcomeKnown, type V2TurnInput } from "../use-thread-turn";
import { useV2PersistenceWarning, useV2RecoveryStore } from "../recovery-storage";
import { acceptedImageTypes, imageIdentities, maximumImages, type WorkspaceImageAttachment } from "../../api/image-attachments";
import { fileAttachmentIdentities, maximumFileAttachments, maximumFileBytes, type WorkspaceFileAttachment } from "../../api/file-attachments";
import { useV2FileInput, V2FileAttachments } from "./file-input";
import { clipboardFiles, normalizeClipboardFile } from "../clipboard-files";
import { desktopClipboardFilesAvailable } from "../../lib/desktop-bridge";
import { useNativeClipboard } from "../use-native-clipboard";
import { useV2ImageInput, V2ImagePreview } from "./image-input";
import { useV2ImageCapability } from "../use-image-capability";
import { requireV2DraftVersion, useV2DraftDocument } from "../draft-context";
import type { V2DraftVersion } from "../draft-version";
import { maximumMCPReferences, mcpReferenceIdentities, type MCPDraftReference } from "../../api/mcp-references";
import { V2MCPReferenceChips, V2MCPReferencePicker, useV2MCPReferences } from "./mcp-reference-picker";
import { MarkdownInput, composerPlainTextKey, readComposerPlainText, type MarkdownInputHandle } from "./markdown-input";
import { v2AttachmentImportKey } from "../attachment-keys";

const maximumContentBytes = 16 * 1024;

export const v2ComposerNotSubmitted = "not_submitted" as const;
export type V2ComposerSubmitResult = void | typeof v2ComposerNotSubmitted;

export function V2Composer({ client, threadID, workspaceID, runID, workspaces, disabled = false, submitDisabled = false,
  projectID, allowProjectless = false,
  placeholder, newThreadControls, threadControls, runActive = false, onManageModels,
  pendingModelRoute, onPendingModelRouteChange, onWorkspaceChange, onSubmit, onStop, draft, onDraftChange,
  fileReferenceUnavailableReason, confirmedSubmission, presentedSubmissionErrors = [] }: {
  client: APIClient;
  threadID: string;
  runID?: string;
  workspaceID: string;
  projectID?: string;
  allowProjectless?: boolean;
  workspaces: WorkspaceView[];
  disabled?: boolean;
  submitDisabled?: boolean;
  placeholder?: string;
  newThreadControls?: ReactNode;
  threadControls?: (hasUnsentDraft: boolean) => ReactNode;
  runActive?: boolean;
  onManageModels?: (prepareForDraft?: boolean) => void;
  pendingModelRoute?: V2PendingModelRoute | null;
  onPendingModelRouteChange?: (route: V2PendingModelRoute | null) => void;
  onWorkspaceChange: (workspaceID: string) => void;
  onSubmit: (content: string, files?: V2FileReference[], images?: WorkspaceImageAttachment[], draftVersion?: V2DraftVersion, attachments?: WorkspaceFileAttachment[], mcpReferences?: MCPDraftReference[]) => Promise<V2ComposerSubmitResult>;
  onStop?: () => boolean;
  draft?: string;
  onDraftChange?: (content: string, expected?: string) => void;
  fileReferenceUnavailableReason?: string;
  confirmedSubmission?: V2TurnInput | null;
  presentedSubmissionErrors?: ReadonlyArray<Pick<V2TurnInput, "threadID" | "workspaceID" | "operationKey">>;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const inputPlaceholder = placeholder ?? t("输入消息…", "Type a message…");
  const [localContent, setLocalContent] = useState("");
  const [plainText, setPlainText] = useState(readComposerPlainText);
  const [modeSaveError, setModeSaveError] = useState(false);
  const pendingContents = useMemo(() => ({ current: new Set<string>() }), [workspaceID, threadID]);
  const submitting = pendingContents.current.size > 0;
  const creatingLocalChat = !threadID && allowProjectless && projectID === "" && submitting;
  const attachmentsDisabled = disabled || creatingLocalChat;
  const managedDraft = useV2DraftDocument(workspaceID, threadID);
  const references = useV2FileReferences(workspaceID, threadID);
  const mcp = useV2MCPReferences(workspaceID, threadID);
  const [mcpPicker, setMCPPicker] = useState<"resource" | "prompt" | null>(null);
  const images = useV2ImageInput({ client, workspaceID, threadID, disabled: attachmentsDisabled });
  const attachments = useV2FileInput({ client, workspaceID, threadID, disabled: attachmentsDisabled });
  const captureNativeImport = () => {
    const captured = managedDraft?.document.capture(managedDraft.scope, managedDraft.state.ref);
    return (incomingImages: WorkspaceImageAttachment[], incomingFiles: WorkspaceFileAttachment[]) => {
      const merge = <T extends { id: string }>(current: T[], incoming: T[], maximum: number) => {
        const next = [...current, ...incoming.filter((item) => !current.some(({ id }) => id === item.id))];
        if (next.length > maximum) throw new Error(t("附件数量超过上限：每条消息最多 4 张图片和 4 个文件。已保存的附件会保留，请先移除多余附件再加入。", "Each message supports up to 4 images and 4 files. Saved attachments are kept; remove extras before adding more."));
        return next;
      };
      if (captured && managedDraft) {
        const next = managedDraft.document.updateCaptured(captured, (current) => ({ ...current,
          images: merge(current.images, incomingImages, maximumImages),
          attachments: merge(current.attachments ?? [], incomingFiles, maximumFileAttachments) }));
        if (next.error || !next.persisted) throw new Error(next.error || t("附件尚未可靠保存到草稿。", "Attachments have not been saved reliably to the draft."));
      } else {
        const nextImages = merge(images.images, incomingImages, maximumImages);
        const nextFiles = merge(attachments.attachments, incomingFiles, maximumFileAttachments);
        images.update(() => nextImages); attachments.update(() => nextFiles);
      }
    };
  };
  const nativeClipboard = useNativeClipboard({ workspaceID, threadID, disabled: attachmentsDisabled,
    captureImport: captureNativeImport, onImported: (incomingImages, incomingFiles) => captureNativeImport()(incomingImages, incomingFiles) });
  const pasteGesture = useRef<{ handled: boolean; plain: boolean } | null>(null);
  const composing = useRef(false);
  const attachmentPicker = useRef<HTMLInputElement>(null);
  const addTriggerRef = useRef<HTMLButtonElement>(null);
  const [referencePickerOpen, setReferencePickerOpen] = useState(false);
  const importStatus = useMemo(() => ({ busy: false, error: "", textNotice: "" }), [workspaceID, threadID]);
  const [, refreshImport] = useState(0);
  const addFiles = async (input: File[]) => {
    if (attachmentsDisabled || !client.hasThreadControl || !input.length) return;
    if (!workspaceID) {
      importStatus.error = t("先选择项目，再粘贴或拖入附件；本次文件尚未加入。", "Choose a project before pasting or dropping attachments. These files have not been added.");
      refreshImport((value) => value + 1); return;
    }
    if (importStatus.busy || images.uploading || attachments.uploading || nativeClipboard.busy ||
      queryClient.getQueryData<{ pending: () => boolean }>(v2AttachmentImportKey(workspaceID, threadID))?.pending()) {
      importStatus.error = t("上一批附件仍在保存，本次文件未加入；请完成后再次粘贴或拖入。", "The previous attachments are still saving. These files have not been added; paste or drop them again when saving finishes.");
      refreshImport((value) => value + 1); return;
    }
    importStatus.busy = true; importStatus.error = ""; refreshImport((value) => value + 1);
    // The live getter stays accurate if the composer unmounts during an upload.
    queryClient.setQueryData(v2AttachmentImportKey(workspaceID, threadID), { pending: () => importStatus.busy });
    try {
      // Capture once before reading any bytes. Both halves of a mixed batch
      // share the original draft branch while later typing continues on it.
      const captured = managedDraft?.document.capture(managedDraft.scope, managedDraft.state.ref);
      const selected = await Promise.all(input.map(normalizeClipboardFile));
      await images.add(selected.filter((file) => acceptedImageTypes.includes(file.type)), captured);
      await attachments.add(selected.filter((file) => !acceptedImageTypes.includes(file.type)), captured);
    } catch (failure) { importStatus.error = failure instanceof Error ? failure.message : t("附件未加入", "Attachments were not added"); }
    finally { importStatus.busy = false; refreshImport((value) => value + 1); }
  };
  const imageCapability = useV2ImageCapability(client, threadID, pendingModelRoute, images.images.length > 0, runActive);
  const recovery = useV2RecoveryStore();
  const persistenceWarning = useV2PersistenceWarning();
  const submissions = useV2ThreadSubmissions(threadID);
  const pendingSubmissions = submissions.filter(({ pending }) => pending);
  const content = managedDraft?.state.snapshot.text ?? draft ?? localContent;
  const setContent = (next: string, expected?: string) => {
    if (managedDraft) managedDraft.changeText(next, expected);
    else if (onDraftChange) onDraftChange(next, expected);
    else setLocalContent((current) => expected === undefined || current === expected ? next : current);
  };
  const currentContentRef = useRef(content);
  currentContentRef.current = content;
  // Keep the input mounted while the initial project loads, but scope local
  // submission bookkeeping so a late response cannot affect another draft.
  const pendingFiles = useMemo(() => ({ current: new Map<string, string[]>() }), [workspaceID, threadID]);
  const pendingImages = useMemo(() => ({ current: new Map<string, string[]>() }), [workspaceID, threadID]);
  const pendingAttachments = useMemo(() => ({ current: new Map<string, string[]>() }), [workspaceID, threadID]);
  const pendingMCP = useMemo(() => ({ current: new Map<string, string[]>() }), [workspaceID, threadID]);
  const [, refreshPending] = useState(0);
  const scope = JSON.stringify([workspaceID, threadID]);
  const currentScope = useRef(scope);
  currentScope.current = scope;
  const [error, setError] = useState<{
    message: string; reason: unknown; inputFingerprint: string; operationKey?: string;
  } | null>(null);
  useEffect(() => setError(null), [scope]);
  useEffect(() => setMCPPicker(null), [scope]);
  useEffect(() => { if (creatingLocalChat) { setMCPPicker(null); setReferencePickerOpen(false); } }, [creatingLocalChat]);
  useEffect(() => { if (disabled || fileReferenceUnavailableReason) setMCPPicker(null); }, [disabled, fileReferenceUnavailableReason]);
  useEffect(() => {
    setError((current) => {
      if (!current) return current;
      // Capture the original request identity before another confirmation can
      // replace its transport error. A later draft can have its own failure.
      const operationKey = current.operationKey ?? submissions.findLast(({ input, error: reason }) =>
        reason === current.reason && input.workspaceID === workspaceID &&
        JSON.stringify([input.content, input.files ?? [], imageIdentities(input.images), fileAttachmentIdentities(input.attachments), mcpReferenceIdentities(input.mcpReferences)]) === current.inputFingerprint)?.input.operationKey;
      if (operationKey && confirmedSubmission?.threadID === threadID &&
        confirmedSubmission.workspaceID === workspaceID && confirmedSubmission.operationKey === operationKey) return null;
      return operationKey && operationKey !== current.operationKey ? { ...current, operationKey } : current;
    });
  }, [confirmedSubmission, threadID, workspaceID, submissions, error?.reason, error?.inputFingerprint]);
  const inputRef = useRef<MarkdownInputHandle>(null);
  const normalized = content.trim();
  const [editorExpanded, setEditorExpanded] = useState(false);
  const hasUnsentDraft = Boolean(normalized || references.files.length || mcp.references.length || images.images.length || images.uploading || attachments.attachments.length || attachments.uploading || attachments.pending || nativeClipboard.pending || nativeClipboard.busy || importStatus.busy ||
    managedDraft?.state.conflict || managedDraft?.state.error);
  const byteLength = new TextEncoder().encode(normalized).byteLength;
  const attachmentByteLength = new TextEncoder().encode(content).byteLength;
  const textAttachmentDisabled = attachmentsDisabled || !client.hasThreadControl || !workspaceID || importStatus.busy || images.uploading ||
    attachments.uploading || attachments.pending || nativeClipboard.busy || nativeClipboard.pending ||
    Boolean(managedDraft?.state.conflict || managedDraft?.state.error);
  const convertTextToAttachment = async () => {
    if (textAttachmentDisabled || !content) return;
    const original = content;
    importStatus.textNotice = ""; importStatus.busy = true; refreshImport((value) => value + 1);
    queryClient.setQueryData(v2AttachmentImportKey(workspaceID, threadID), { pending: () => importStatus.busy });
    try {
      const captured = managedDraft?.document.capture(managedDraft.scope, managedDraft.state.ref);
      const name = t("粘贴的文本.txt", "Pasted text.txt");
      const saved = await attachments.add([new File([original], name, { type: "text/plain" })], captured);
      if (!saved.length) return;
      // Only clear text after the full file and its draft reference are saved.
      // Later edits and another draft's text must survive an in-flight upload.
      if (captured && managedDraft) {
        managedDraft.document.updateCaptured(captured, (current) => ({ ...current, text: current.text === original ? "" : current.text }));
      } else if (currentScope.current === scope && currentContentRef.current === original) setContent("", original);
      if (currentScope.current === scope && currentContentRef.current !== original && currentContentRef.current !== "") {
        importStatus.textNotice = t("附件已保存；上传期间修改过的正文已保留。", "Attachment saved. Edits made during upload have been kept.");
      }
    } catch (failure) { importStatus.error = failure instanceof Error ? failure.message : t("文本附件未保存", "Text attachment was not saved"); }
    finally { importStatus.busy = false; refreshImport((value) => value + 1); }
  };
  const restoreTextAttachment = (file: WorkspaceFileAttachment, text: string) => {
    if (attachmentsDisabled) return;
    if (managedDraft) managedDraft.document.update(managedDraft.scope, (current) => ({ ...current,
      text: current.text ? `${current.text}\n\n${text}` : text,
      attachments: (current.attachments ?? []).filter(({ id }) => id !== file.id),
    }), managedDraft.state.ref);
    else {
      setContent(content ? `${content}\n\n${text}` : text);
      attachments.update((current) => current.filter(({ id }) => id !== file.id));
    }
    requestAnimationFrame(() => inputRef.current?.focus());
  };
  const fingerprint = JSON.stringify([normalized, references.files, imageIdentities(images.images), fileAttachmentIdentities(attachments.attachments), mcpReferenceIdentities(mcp.references)]);
  const sameMessagePending = pendingContents.current.has(fingerprint) ||
    pendingSubmissions.some(({ input }) => JSON.stringify([input.content, input.files ?? [], imageIdentities(input.images), fileAttachmentIdentities(input.attachments), mcpReferenceIdentities(input.mcpReferences)]) === fingerprint);
  const ready = !disabled && !submitDisabled && !creatingLocalChat && !managedDraft?.state.conflict && !managedDraft?.state.error && !images.uploading && !attachments.uploading && !attachments.pending && !nativeClipboard.pending && !nativeClipboard.busy && !importStatus.busy && (!images.images.length || imageCapability.allowed) && !sameMessagePending && Boolean(workspaceID) && (byteLength > 0 || images.images.length > 0 || attachments.attachments.length > 0 || mcp.references.length > 0) &&
    byteLength <= maximumContentBytes && !(fileReferenceUnavailableReason && (references.files.length > 0 || mcp.references.length > 0));
  // Low-priority hints share a single caption line instead of stacking.
  const draftSavedLocally = Boolean(recovery) && !persistenceWarning &&
    (!managedDraft || managedDraft.state.persisted && !managedDraft.state.error) &&
    (content || references.files.length > 0 || images.images.length > 0 || attachments.attachments.length > 0 || mcp.references.length > 0);
  const showImageCapability = images.images.length > 0;

  const submit = async (event?: FormEvent<HTMLFormElement>) => {
    event?.preventDefault();
    // A descendant portal can contain its own form (settings or file search).
    if (event && event.target !== event.currentTarget) return;
    if (!ready) return;
    const submittedFiles = references.files;
    const submittedImages = images.images;
    const submittedAttachments = attachments.attachments;
    const submittedMCP = mcp.references;
    pendingFiles.current.set(fingerprint, submittedFiles.map(({ id }) => id));
    pendingImages.current.set(fingerprint, submittedImages.map(({ id }) => id));
    pendingAttachments.current.set(fingerprint, submittedAttachments.map(({ id }) => id));
    pendingMCP.current.set(fingerprint, submittedMCP.map(({ id }) => id));
    pendingContents.current.add(fingerprint);
    refreshPending((value) => value + 1);
    setError(null);
    const submittedContent = content;
    try {
      let result: V2ComposerSubmitResult;
      if (managedDraft) {
        const version = requireV2DraftVersion(managedDraft, { text: content, files: submittedFiles, images: submittedImages, attachments: submittedAttachments, mcpReferences: submittedMCP });
        result = submittedMCP.length ? await onSubmit(normalized, submittedFiles, submittedImages, version, submittedAttachments, submittedMCP)
          : await onSubmit(normalized, submittedFiles, submittedImages, version, submittedAttachments);
      }
      else if (submittedMCP.length) result = await onSubmit(normalized, submittedFiles, submittedImages, undefined, submittedAttachments, submittedMCP);
      else if (submittedAttachments.length) result = await onSubmit(normalized, submittedFiles, submittedImages, undefined, submittedAttachments);
      else if (submittedImages.length) result = await onSubmit(normalized, submittedFiles, submittedImages);
      else if (submittedFiles.length) result = await onSubmit(normalized, submittedFiles);
      else result = await onSubmit(normalized);
      if (result === v2ComposerNotSubmitted) return;
      if (!managedDraft) {
        references.update((current) => current.filter(({ id }) => !submittedFiles.some((file) => file.id === id)));
        images.update((current) => current.filter(({ id }) => !submittedImages.some((image) => image.id === id)));
        attachments.update((current) => current.filter(({ id }) => !submittedAttachments.some((file) => file.id === id)));
        mcp.update((current) => current.filter(({ id }) => !submittedMCP.some((item) => item.id === id)));
        setContent("", submittedContent);
      }
      if (currentScope.current === scope && currentContentRef.current === submittedContent) {
        setEditorExpanded(false); inputRef.current?.focus();
      }
    } catch (failure) {
      const reason = failure instanceof V2SubmissionError ? failure.reason : failure;
      const operationKey = failure instanceof V2SubmissionError &&
        failure.submission.threadID === threadID && failure.submission.workspaceID === workspaceID
        ? failure.submission.operationKey : undefined;
      if (currentScope.current === scope) setError({ reason,
        message: reason instanceof APIRequestError && reason.turnFailed === true
          ? reason.code === "CANCELLED"
            ? t("本轮已停止，输入和已完成的工作已保留。可发送新消息继续。", "This turn stopped. Your input and completed work are kept. Send a new message to continue.")
            : t("本轮执行失败，输入和已完成的工作已保留。可发送新消息继续。", "This turn failed. Your input and completed work are kept. Send a new message to continue.")
          : reason instanceof Error ? reason.message : t("消息发送失败", "Message could not be sent"),
        inputFingerprint: JSON.stringify([normalized, submittedFiles, imageIdentities(submittedImages), fileAttachmentIdentities(submittedAttachments), mcpReferenceIdentities(submittedMCP)]), operationKey });
    } finally {
      pendingContents.current.delete(fingerprint);
      pendingFiles.current.delete(fingerprint);
      pendingImages.current.delete(fingerprint);
      pendingAttachments.current.delete(fingerprint);
      pendingMCP.current.delete(fingerprint);
      refreshPending((value) => value + 1);
    }
  };
  const onKeyDown = (event: KeyboardEvent<HTMLFormElement>) => {
    if (!event.nativeEvent.isComposing && !event.altKey &&
        ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "v" || event.shiftKey && event.key === "Insert")) {
      const gesture = { handled: false, plain: event.shiftKey && event.key.toLowerCase() === "v" };
      pasteGesture.current = gesture;
      // Let WebView perform its normal edit/undo first. A DOM FileList wins;
      // only a single explicit paste gesture may fall back to CF_HDROP.
      setTimeout(() => {
        if (pasteGesture.current === gesture) pasteGesture.current = null;
        if (!gesture.handled && !gesture.plain && desktopClipboardFilesAvailable()) void nativeClipboard.onPasteFallback();
      }, 0);
    }
  };
  const stopFromInput = (event: KeyboardEvent<HTMLFormElement>) => {
    if (event.key !== "Escape" || event.defaultPrevented || event.repeat || disabled || !onStop ||
      event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || composing.current ||
      event.nativeEvent.isComposing || event.keyCode === 229) return;
    const target = event.target;
    // React portals also propagate through this form. Only the message editor
    // may interrupt; menus and dialogs retain their own Escape behavior.
    if (!(target instanceof Element) || !event.currentTarget.contains(target) || target.closest("[inert]") ||
      !target.closest('textarea, .v2-markdown-input[contenteditable="true"]') ||
      document.querySelector('[role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"]')) return;
    if (onStop()) { event.preventDefault(); event.stopPropagation(); }
  };

  const toggleEditorMode = (trigger: HTMLElement | null) => {
    const next = !plainText;
    setPlainText(next);
    try { localStorage.setItem(composerPlainTextKey, String(next)); setModeSaveError(false); }
    catch { setModeSaveError(true); }
    requestAnimationFrame(() => {
      if (trigger && document.activeElement === trigger && !trigger.closest("[inert]")) inputRef.current?.focus();
    });
  };
  const addActions: ComposerAddAction[] = [];
  if (client.hasThreadControl) addActions.push({ id: "upload", label: t("添加图片或文件", "Add images or files"),
    detail: !workspaceID ? t("先选择项目，再添加附件", "Select a project before adding attachments") : t("从电脑选择，也可直接粘贴或拖入", "Choose from your computer, paste, or drag files here"),
    icon: <Paperclip aria-hidden="true" size={18} />,
    disabled: disabled || !workspaceID || importStatus.busy || images.uploading || attachments.uploading,
    onSelect: () => attachmentPicker.current?.click() });
  if (workspaceID && client.hasEvidenceAttachment) addActions.push({ id: "reference", label: t("引用项目文件", "Reference project files"),
    detail: fileReferenceUnavailableReason || t("选择项目中已有的文件作为参考", "Choose existing project files as references"),
    icon: <FolderOpen aria-hidden="true" size={18} />, disabled: disabled || Boolean(fileReferenceUnavailableReason),
    onSelect: () => setReferencePickerOpen(true) });
  if (workspaceID && client.hasExtensionControl && client.hasThreadControl) {
    let unavailable = "";
    if (fileReferenceUnavailableReason) unavailable = t("当前任务结束并处理待批准操作后可引用 MCP 内容", "MCP references are available after the task and pending approvals are complete");
    else if (mcp.references.length >= maximumMCPReferences) unavailable = t("每条消息最多 4 个 MCP 引用", "Up to 4 MCP references per message");
    addActions.push({ id: "mcp-resource", label: t("引用 MCP 资源", "Reference MCP resources"), detail: unavailable || t("选择服务器资源或填写资源模板", "Choose server resources or fill in a resource template"),
      icon: <Database aria-hidden="true" size={18} />, disabled: disabled || Boolean(unavailable), onSelect: () => setMCPPicker("resource") },
    { id: "mcp-prompt", label: t("使用 MCP Prompt", "Use MCP prompt"), detail: unavailable || t("选择 Prompt 并填写参数", "Choose a prompt and fill in its arguments"),
      icon: <Command aria-hidden="true" size={18} />, disabled: disabled || Boolean(unavailable), onSelect: () => setMCPPicker("prompt") });
  }
  if (client.hasThreadControl && desktopClipboardFilesAvailable()) addActions.push({ id: "paste", label: t("粘贴文件", "Paste files"),
    detail: t("添加从资源管理器复制的文件 · Ctrl+V", "Add files copied from File Explorer · Ctrl+V"),
    icon: <ClipboardPaste aria-hidden="true" size={18} />,
    disabled: disabled || !workspaceID || nativeClipboard.busy || nativeClipboard.pending,
    onSelect: () => void nativeClipboard.onPasteFallback() });
  addActions.push({ id: "editor-mode", compactOnly: true,
    label: plainText ? t("切换为 Markdown 编辑", "Switch to Markdown editing") : t("切换为纯文本编辑", "Switch to plain text editing"),
    detail: t("切换输入方式，保留当前草稿", "Change the editor and keep your draft"),
    icon: <SquarePen aria-hidden="true" size={18} />, disabled, onSelect: () => toggleEditorMode(addTriggerRef.current) });

  return <form className="v2-composer" data-empty={!hasUnsentDraft} data-editor-expanded={editorExpanded}
    onSubmit={(event) => void submit(event)} onKeyDownCapture={(event) => {
      // A running turn takes precedence over Tiptap's native Escape binding.
      stopFromInput(event); onKeyDown(event);
    }}
    onCompositionStartCapture={() => { composing.current = true; }}
    onCompositionEndCapture={() => { composing.current = false; }} onPasteCapture={(event) => {
    if (pasteGesture.current?.plain) return;
    const files = clipboardFiles(event.clipboardData);
    if (files.length) {
      if (pasteGesture.current) pasteGesture.current.handled = true;
      // The editor retains accompanying text and its undo entry.
      if (!event.clipboardData.getData?.("text/plain")) event.preventDefault();
      void addFiles(files);
    } else if (Array.from(event.clipboardData.types ?? []).includes("Files") && desktopClipboardFilesAvailable()) {
      if (pasteGesture.current) pasteGesture.current.handled = true;
      void nativeClipboard.onPasteFallback();
    }
  }} onDropCapture={(event) => {
    const files = clipboardFiles(event.dataTransfer);
    if (files.length) { event.preventDefault(); event.stopPropagation(); void addFiles(files); }
  }} onDragOver={images.onDragOver}>
    <div className="v2-composer-surface">
      <button className="v2-composer-compact-trigger" type="button" aria-label={t("展开消息编辑器", "Expand message editor")} disabled={disabled}
        onClick={() => { setEditorExpanded(true); requestAnimationFrame(() => inputRef.current?.focus()); }}>{inputPlaceholder}</button>
      <V2MCPReferenceChips references={mcp.references} disabled={disabled} pendingIDs={[
        ...[...pendingMCP.current.values()].flat(), ...submissions.filter(({ pending, error }) => pending || Boolean(error) && !v2TurnOutcomeKnown(error))
          .flatMap(({ input }) => input.mcpReferences?.map(({ id }) => id) ?? []),
      ]} onRemove={(id) => mcp.update((current) => current.filter((item) => item.id !== id))} />
      {mcpPicker && !disabled && <V2MCPReferencePicker client={client} key={scope} kind={mcpPicker}
        workspaceID={workspaceID} runID={runID} returnFocusRef={addTriggerRef} onClose={() => setMCPPicker(null)}
        onSelect={(item) => mcp.update((current) => current.length < maximumMCPReferences ? [...current, item] : current)} />}
      <V2ImagePreview client={client} images={images.images}
        pendingIDs={[...[...pendingImages.current.values()].flat(), ...submissions.filter(({ pending, error }) =>
          pending || Boolean(error) && !v2TurnOutcomeKnown(error)).flatMap(({ input }) => input.images?.map(({ id }) => id) ?? [])]}
        onRemove={(id) => images.update((current) => current.filter((image) => image.id !== id))} />
      <V2FileAttachments client={client} attachments={attachments.attachments}
        onRestore={attachmentsDisabled ? undefined : restoreTextAttachment}
        pendingIDs={[...[...pendingAttachments.current.values()].flat(), ...submissions.filter(({ pending, error }) =>
          pending || Boolean(error) && !v2TurnOutcomeKnown(error)).flatMap(({ input }) => input.attachments?.map(({ id }) => id) ?? [])]}
        onRemove={(id) => attachments.update((current) => current.filter((file) => file.id !== id))} />
      {workspaceID && client.hasEvidenceAttachment && <V2FileContext client={client}
        disabled={disabled} files={references.files} pendingIDs={[
          ...[...pendingFiles.current.values()].flat(),
          ...pendingSubmissions.flatMap(({ input }) => input.files?.map(({ id }) => id) ?? []),
        ]} unavailableReason={fileReferenceUnavailableReason} open={referencePickerOpen}
        onOpenChange={setReferencePickerOpen} returnFocusRef={addTriggerRef}
        onChange={references.update} workspaceID={workspaceID} />}
      <MarkdownInput label={threadID ? t("继续对话", "Continue conversation") : t("开始新对话", "Start a new conversation")} disabled={disabled}
        onChange={(next) => { setContent(next); setError(null); }} onSubmit={() => void submit()}
        placeholder={inputPlaceholder} inputRef={inputRef} value={content} scope={scope} plainText={plainText} />
      {byteLength >= 4096 && <div className="v2-composer-text-attachment">
        <span className={byteLength > maximumContentBytes ? "is-over-limit" : undefined}>
          {t("正文", "Message")} {(byteLength / 1024).toFixed(1)} / 16 KiB</span>
        <button type="button" disabled={textAttachmentDisabled || attachmentByteLength > maximumFileBytes}
          onClick={() => void convertTextToAttachment()}>{attachments.uploading ? t("正在保存…", "Saving…") : t("转为文本附件", "Convert to text attachment")}</button>
      </div>}
      <div className="v2-composer-footer">
        {!threadID && <label className="v2-workspace-picker">
          <FolderOpen aria-hidden="true" size={14} />
          <span className="sr-only">{t("工作区", "Workspace")}</span>
          <select aria-label={t("选择工作区", "Select workspace")} disabled={submitting || (!allowProjectless && workspaces.length === 0)}
            onChange={(event) => onWorkspaceChange(event.target.value)} value={projectID ?? workspaceID}>
            {allowProjectless && <option value="">{t("本地聊天", "Local chats")}</option>}
            {workspaces.map((workspace) => <option key={workspace.id} value={workspace.id}>
              {workspace.name}
            </option>)}
          </select>
        </label>}
        <div className="v2-composer-tools">
          <V2ComposerAddMenu actions={addActions} disabled={attachmentsDisabled} key={scope} triggerRef={addTriggerRef} />
          <button className="v2-composer-mode-toggle" type="button" disabled={disabled}
            aria-label={plainText ? t("切换为 Markdown 编辑", "Switch to Markdown editing") : t("切换为纯文本编辑", "Switch to plain text editing")}
            title={plainText ? t("显示格式；Ctrl+Enter 发送", "Show formatting; Ctrl+Enter to send") : t("编辑 Markdown 原文", "Edit Markdown source")}
            onClick={(event) => toggleEditorMode(event.currentTarget)}><SquarePen aria-hidden="true" size={15} /><span className="v2-composer-optional-label">{plainText ? t("纯文本", "Plain text") : "Markdown"}</span></button>
          {client.hasThreadControl &&
            <input ref={attachmentPicker} type="file" multiple hidden disabled={attachmentsDisabled} aria-label={t("选择图片或文件", "Choose images or files")}
              onChange={(event) => { const files = Array.from(event.target.files ?? []); event.target.value = ""; void addFiles(files); }} />}
          {!threadID && newThreadControls}
          {threadID && threadControls?.(hasUnsentDraft)}
          {threadID && <V2PermissionControl client={client} threadID={threadID}
            onOpenModelSettings={onManageModels ? () => onManageModels(false) : undefined} />}
        </div>
        <div className="v2-composer-actions">
          {onManageModels && (threadID || onPendingModelRouteChange) &&
            <V2ModelRouteControl client={client} onManageModels={onManageModels}
              onPendingRouteChange={onPendingModelRouteChange} pendingRoute={pendingModelRoute}
              runActive={runActive} threadID={threadID} />}
          <button aria-label={t("发送消息", "Send message")} className="v2-send-button" disabled={!ready} type="submit">
            {sameMessagePending ? <LoaderCircle aria-hidden="true" className="spin" size={18} />
              : <ArrowUp aria-hidden="true" size={19} />}
          </button>
        </div>
      </div>
    </div>
    {workspaceID && references.files.length > 0 && fileReferenceUnavailableReason &&
      <p className="v2-composer-caption" role="status">{fileReferenceUnavailableReason}
        {t(" 已选引用与草稿会保留；先移除项目文件引用，即可发送补充文字和上传附件。", " Your references and draft are kept. Remove project file references to send text and uploaded attachments.")}</p>}
    {mcp.references.length > 0 && fileReferenceUnavailableReason && <p className="v2-composer-caption" role="status">
      {t("MCP 引用需在当前任务结束并处理待批准操作后发送。草稿和已选内容会保留。", "Send MCP references after the task and pending approvals are complete. Your draft and selected content are kept.")}</p>}
    {byteLength > maximumContentBytes && <p className="v2-composer-error">{t("正文不能超过 16 KiB，可转为文本附件后继续发送。", "Message text cannot exceed 16 KiB. Convert it to a text attachment to continue.")}</p>}
    {attachmentByteLength > maximumFileBytes && <p className="v2-composer-error">{t("文本附件不能超过 5 MiB，请拆分内容。", "Text attachments cannot exceed 5 MiB. Split the content into smaller files.")}</p>}
    {importStatus.textNotice && <p className="v2-composer-caption" role="status">{importStatus.textNotice}</p>}
    {modeSaveError && <p className="v2-composer-caption" role="status">{t("编辑模式已切换，但偏好未能保存；下次打开时会使用默认模式。", "Editor mode changed, but the preference could not be saved. The default mode will be used next time.")}</p>}
    {images.uploading && <p className="v2-composer-caption" role="status">{t("正在保存图片，完成后可发送…", "Saving images. You can send them when saving finishes…")}</p>}
    {(showImageCapability || draftSavedLocally) && <p className="v2-composer-caption" role="status">
      {showImageCapability && <>{imageCapability.hint}
        {!imageCapability.allowed && onManageModels && <button className="v2-composer-chip" type="button"
          onClick={() => onManageModels(false)}>{t("模型设置", "Model settings")}</button>}</>}
      {showImageCapability && draftSavedLocally && " · "}
      {draftSavedLocally && t("草稿与附件保存在此设备", "Draft and attachments saved on this device")}
    </p>}
    {images.error && <p className="v2-composer-error" role="alert">{images.error}</p>}
    {attachments.notice}
    {nativeClipboard.notice}
    {(attachments.error || importStatus.error) && <p className="v2-composer-error" role="alert">{attachments.error || importStatus.error}</p>}
    {error && !presentedSubmissionErrors.some((input) => input.threadID === threadID &&
      input.workspaceID === workspaceID && input.operationKey === error.operationKey) &&
      <p className="v2-composer-error" role="alert">{localUIMessage(error.message, t)}</p>}
  </form>;
}
