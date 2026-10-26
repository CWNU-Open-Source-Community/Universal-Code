import { useId, useImperativeHandle, useLayoutEffect, useRef, useState, type Ref } from "react";
import { Editor } from "@tiptap/core";
import { StarterKit } from "@tiptap/starter-kit";
import { Markdown } from "@tiptap/markdown";
import { TableKit } from "@tiptap/extension-table";
import { TaskItem, TaskList } from "@tiptap/extension-list";
import { Image } from "@tiptap/extension-image";
import { Slice } from "@tiptap/pm/model";
import { EditorState, TextSelection } from "@tiptap/pm/state";
import "./markdown-input.css";
import { sendsOnEnter, useChatPreferences } from "../chat-preferences";
import type { HistoryDirection } from "../use-prompt-history";
import type { APIClient } from "../../api/client";
import type { WorkspaceExplorerView } from "../../api/types";
import { completedFilePath, filePathMention, plainFilePathMention } from "./file-path-mention";
import { FilePathSuggestions, type FilePathSuggestionsHandle, type FilePathTarget } from "./file-path-suggestions";

export const composerPlainTextKey = "universal-code.composer-plain-text";
export function readComposerPlainText(): boolean {
  try { return localStorage.getItem(composerPlainTextKey) === "true"; } catch { return false; }
}

export interface MarkdownInputHandle { focus(): void; atHistoryBoundary(direction: HistoryDirection): boolean }

// The durable draft remains Markdown. The editor document and undo history are
// local to this draft scope; merely mounting or switching views never rewrites it.
export function MarkdownInput({ value, onChange, onSubmit, plainText, scope, label, placeholder,
  disabled, inputRef, filePaths, onHistoryNavigate, onHistoryCancel }: {
  value: string; onChange: (value: string) => void; onSubmit: () => void;
  plainText: boolean; scope: string; label: string; placeholder: string;
  disabled: boolean; inputRef: Ref<MarkdownInputHandle>;
  onHistoryNavigate?: (direction: HistoryDirection) => boolean;
  onHistoryCancel?: () => void;
  filePaths?: { client: APIClient; workspaceID: string;
    onAttach: (file: WorkspaceExplorerView, next: string, expected: string) => void };
}) {
  const { send } = useChatPreferences();
  const mount = useRef<HTMLDivElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const editor = useRef<Editor | null>(null);
  const latest = useRef({ value, onChange, onSubmit, disabled, label, placeholder, send, filePaths, onHistoryNavigate, onHistoryCancel });
  latest.current = { value, onChange, onSubmit, disabled, label, placeholder, send, filePaths, onHistoryNavigate, onHistoryCancel };
  const representedValue = useRef(value);
  const restoreFocus = useRef(false);
  const [target, setTarget] = useState<FilePathTarget | null>(null);
  const targetRef = useRef<FilePathTarget | null>(null);
  const dismissedPath = useRef<FilePathTarget | null>(null);
  const suggestions = useRef<FilePathSuggestionsHandle>(null);
  const listID = useId();
  const [activeOption, setActiveOption] = useState<string>();
  const composing = useRef(false);
  const atHistoryBoundary = (direction: HistoryDirection) => {
    if (latest.current.disabled || composing.current) return false;
    const input = textarea.current;
    if (input) return document.activeElement === input && input.selectionStart === input.selectionEnd &&
      input.selectionStart === (direction === "older" ? 0 : input.value.length);
    const view = editor.current?.view;
    if (!view?.hasFocus() || view.composing) return false;
    const { selection, doc } = view.state;
    return selection instanceof TextSelection && selection.empty && selection.from ===
      (direction === "older" ? TextSelection.atStart(doc).from : TextSelection.atEnd(doc).from);
  };
  const historyKey = (event: globalThis.KeyboardEvent) => {
    if (event.isComposing || event.keyCode === 229 || composing.current || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return false;
    const direction = event.key === "ArrowUp" ? "older" : event.key === "ArrowDown" ? "newer" : null;
    if (!direction || !atHistoryBoundary(direction) || !latest.current.onHistoryNavigate?.(direction)) return false;
    event.preventDefault(); return true;
  };
  const replacing = useRef(false);
  const pendingCaret = useRef<{ value: string; at: number; directory: boolean } | null>(null);
  const closePaths = () => { targetRef.current = null; setTarget(null); setActiveOption(undefined); };
  const dismissPaths = () => { dismissedPath.current = targetRef.current; closePaths(); };
  const showPaths = (next: FilePathTarget | null) => {
    if (!latest.current.filePaths || latest.current.disabled || composing.current) { closePaths(); return; }
    // Browsers may emit select after Escape or blur without moving the caret.
    if (next && JSON.stringify(next) === JSON.stringify(dismissedPath.current)) { closePaths(); return; }
    if (next) dismissedPath.current = null;
    if (JSON.stringify(next) === JSON.stringify(targetRef.current)) return;
    targetRef.current = next; setTarget(next);
  };
  const readPlainPaths = (input: HTMLTextAreaElement) => {
    const found = input.selectionStart === input.selectionEnd && document.activeElement === input
      ? plainFilePathMention(input.value, input.selectionStart) : null;
    showPaths(found ? { ...found, original: input.value } : null);
  };
  const readRichPaths = (instance: Editor) => {
    const { selection } = instance.state;
    const { $from } = selection;
    if (!instance.view.hasFocus() || instance.view.composing || !selection.empty || !$from.parent.isTextblock ||
      $from.parent.type.spec.code || $from.marks().some((mark) => mark.type.spec.code || mark.type.name === "link")) {
      closePaths(); return;
    }
    const text = $from.parent.textBetween(0, $from.parent.content.size, undefined, "\ufffc");
    const found = filePathMention(text, $from.parentOffset);
    let protectedText = false;
    if (found) $from.parent.nodesBetween(found.from, found.to, (node) => {
      if (node.marks.some((mark) => mark.type.spec.code || mark.type.name === "link")) protectedText = true;
    });
    if (protectedText) { closePaths(); return; }
    showPaths(found ? { ...found, from: found.from + $from.start(), to: found.to + $from.start(),
      original: representedValue.current } : null);
  };
  const choosePath = (selected: FilePathTarget, path: string, file?: WorkspaceExplorerView) => {
    if (targetRef.current !== selected || !latest.current.filePaths || composing.current || latest.current.disabled ||
      latest.current.value !== selected.original) return;
    const completion = completedFilePath(path, !file);
    const commit = (next: string) => file ? latest.current.filePaths!.onAttach(file, next, selected.original) : latest.current.onChange(next);
    const input = textarea.current;
    if (input) {
      if (input.value !== selected.original) return;
      const next = input.value.slice(0, selected.from) + completion.text + input.value.slice(selected.to);
      commit(next);
      pendingCaret.current = { value: next, at: selected.from + completion.caret, directory: !file };
      closePaths();
    } else {
      const instance = editor.current;
      if (!instance || instance.view.composing) return;
      const transaction = instance.state.tr.insertText(completion.text, selected.from, selected.to);
      transaction.setSelection(TextSelection.create(transaction.doc, selected.from + completion.caret));
      const next = instance.markdown!.serialize(transaction.doc.toJSON());
      commit(next);
      closePaths();
      replacing.current = true;
      instance.view.dispatch(transaction.scrollIntoView());
      replacing.current = false;
      instance.view.focus();
      if (!file) readRichPaths(instance);
    }
  };

  useLayoutEffect(() => { closePaths(); dismissedPath.current = null; pendingCaret.current = null;
    return () => { targetRef.current = null; };
  }, [scope, plainText, disabled, filePaths?.client, filePaths?.workspaceID, Boolean(filePaths)]);

  useImperativeHandle(inputRef, () => ({ focus: () => {
    if (textarea.current) textarea.current.focus(); else editor.current?.view.focus();
  }, atHistoryBoundary }), []);

  useLayoutEffect(() => {
    composing.current = false;
    if (plainText || !mount.current) return;
    representedValue.current = latest.current.value;
    let plainPaste = false;
    const instance: Editor = new Editor({
      element: mount.current,
      injectCSS: false,
      enablePasteRules: false,
      extensions: [StarterKit.configure({ underline: false, link: { openOnClick: false }, trailingNode: false }),
        Markdown.configure({ markedOptions: { gfm: true } }), TableKit, TaskList,
        TaskItem.configure({ nested: true }), Image.configure({ inline: true })],
      content: latest.current.value, contentType: "markdown", editable: !latest.current.disabled,
      editorProps: {
        attributes: { role: "textbox", "aria-multiline": "true", "aria-label": latest.current.label,
          "data-placeholder": latest.current.placeholder, class: "v2-markdown-input", dir: "auto" },
        handleDOMEvents: {
          compositionstart() { composing.current = true; latest.current.onHistoryCancel?.(); closePaths(); return false; },
          compositionend() { composing.current = false; return false; },
          blur() { dismissPaths(); return false; },
        },
        handleKeyDown(view, event) {
          plainPaste = event.shiftKey && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "v";
          if (event.isComposing || view.composing || event.keyCode === 229) return false;
          // Candidate navigation precedes both sending and prompt-history keys.
          if (suggestions.current?.keyDown(event)) return true;
          if (historyKey(event)) return true;
          if (event.key !== "Enter" || event.shiftKey || event.altKey) return false;
          const { $from } = view.state.selection;
          const fence = $from.parent.type.name === "paragraph" && /^```([\w+-]*)$/.exec($from.parent.textContent);
          if (fence && !event.ctrlKey && !event.metaKey) {
            instance.chain().deleteRange({ from: $from.start(), to: $from.end() })
              .setCodeBlock({ language: fence[1] }).run();
            return true;
          }
          // Enter continues structured content. Ctrl/Cmd+Enter always sends;
          // ordinary paragraphs retain the existing Enter-to-send behavior.
          const structured = Array.from({ length: $from.depth }, (_, index) => $from.node(index + 1))
            .some((node) => ["codeBlock", "listItem", "taskItem", "blockquote", "table", "heading"].includes(node.type.name));
          if (!sendsOnEnter(latest.current.send, latest.current.value, event.ctrlKey || event.metaKey, structured)) return false;
          event.preventDefault(); latest.current.onSubmit(); return true;
        },
        handlePaste(view, event) {
          if (event.defaultPrevented) return true;
          const text = event.clipboardData?.getData("text/plain");
          if (!text || view.state.selection.$from.parent.type.spec.code) return false;
          // Use the Markdown text on the clipboard, never styles copied from a
          // web page. Explicit plain-text paste keeps Markdown punctuation.
          if (plainPaste) {
            plainPaste = false;
            view.dispatch(view.state.tr.insertText(text).scrollIntoView());
            return true;
          }
          const document = instance.schema.nodeFromJSON(instance.markdown!.parse(text));
          view.dispatch(view.state.tr.replaceSelection(Slice.maxOpen(document.content)).scrollIntoView());
          return true;
        },
        clipboardTextSerializer(slice) {
          return instance.markdown!.serialize({ type: "doc", content: slice.content.toJSON() });
        },
      },
      onUpdate({ editor: current }) {
        const next = current.isEmpty ? "" : current.getMarkdown();
        representedValue.current = next;
        if (!replacing.current) { latest.current.onChange(next); readRichPaths(current); }
      },
      onSelectionUpdate({ editor: current }) { if (!replacing.current) readRichPaths(current); },
    });
    editor.current = instance;
    if (restoreFocus.current) instance.commands.focus("end");
    return () => {
      restoreFocus.current = instance.view.hasFocus();
      instance.destroy(); editor.current = null;
    };
  }, [plainText, scope]);

  useLayoutEffect(() => {
    if (targetRef.current && targetRef.current.original !== value) closePaths();
    const instance = editor.current;
    if (instance) {
      instance.setEditable(!disabled, false);
      instance.view.dom.setAttribute("aria-label", label);
      instance.view.dom.setAttribute("aria-disabled", String(disabled));
      instance.view.dom.setAttribute("data-placeholder", placeholder);
      if (value !== representedValue.current) {
        closePaths();
        // An external change is a restored/cleared draft, not a user edit.
        // Reset history so Undo cannot resurrect a sent message or another task.
        replacing.current = true;
        instance.commands.setContent(value, { contentType: "markdown", emitUpdate: false });
        const doc = instance.state.doc;
        instance.view.updateState(EditorState.create({
          schema: instance.schema, doc, plugins: instance.state.plugins,
          selection: TextSelection.atEnd(doc),
        }));
        representedValue.current = value;
        replacing.current = false;
      }
    }
    if (textarea.current) {
      textarea.current.style.height = "0px";
      textarea.current.style.height = `${Math.min(240, Math.max(38, textarea.current.scrollHeight))}px`;
      if (restoreFocus.current) { textarea.current.focus(); restoreFocus.current = false; }
      const pending = pendingCaret.current;
      if (pending?.value === value) {
        pendingCaret.current = null;
        textarea.current.focus(); textarea.current.setSelectionRange(pending.at, pending.at);
        if (pending.directory) readPlainPaths(textarea.current);
      }
    }
  }, [value, disabled, label, placeholder, plainText, scope]);

  useLayoutEffect(() => {
    const input = textarea.current ?? editor.current?.view.dom;
    if (!input) return;
    if (target) {
      input.setAttribute("aria-controls", listID); input.setAttribute("aria-autocomplete", "list");
      if (activeOption) input.setAttribute("aria-activedescendant", activeOption); else input.removeAttribute("aria-activedescendant");
    } else {
      input.removeAttribute("aria-controls"); input.removeAttribute("aria-activedescendant"); input.removeAttribute("aria-autocomplete");
    }
  }, [target, activeOption, listID, plainText]);

  return <div className="v2-file-path-input">{plainText ? <textarea aria-label={label} disabled={disabled} ref={textarea} rows={1}
    placeholder={placeholder} value={value} onChange={(event) => { onChange(event.target.value); readPlainPaths(event.currentTarget); }}
    onSelect={(event) => readPlainPaths(event.currentTarget)} onBlur={dismissPaths}
    onCompositionStart={() => { composing.current = true; latest.current.onHistoryCancel?.(); closePaths(); }}
    onCompositionEnd={(event) => { composing.current = false; readPlainPaths(event.currentTarget); }}
    onKeyDown={(event) => {
      if (!composing.current && suggestions.current?.keyDown(event.nativeEvent)) return;
      if (historyKey(event.nativeEvent)) return;
      if (event.key === "Enter" && !event.shiftKey && !event.altKey && !event.nativeEvent.isComposing && event.keyCode !== 229 &&
        sendsOnEnter(send, value, event.ctrlKey || event.metaKey)) {
        event.preventDefault(); onSubmit();
      }
    }} /> : <div className="v2-markdown-input-host" ref={mount} />}
    {target && filePaths && <FilePathSuggestions client={filePaths.client} workspaceID={filePaths.workspaceID}
      target={target} listID={listID} inputRef={suggestions} onActive={setActiveOption} onClose={dismissPaths} onSelect={choosePath} />}
  </div>;
}
