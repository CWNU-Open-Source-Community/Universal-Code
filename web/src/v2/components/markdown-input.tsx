import { useImperativeHandle, useLayoutEffect, useRef, type Ref } from "react";
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

export const composerPlainTextKey = "universal-code.composer-plain-text";
export function readComposerPlainText(): boolean {
  try { return localStorage.getItem(composerPlainTextKey) === "true"; } catch { return false; }
}

export interface MarkdownInputHandle { focus(): void }

// The durable draft remains Markdown. The editor document and undo history are
// local to this draft scope; merely mounting or switching views never rewrites it.
export function MarkdownInput({ value, onChange, onSubmit, plainText, scope, label, placeholder,
  disabled, inputRef }: {
  value: string; onChange: (value: string) => void; onSubmit: () => void;
  plainText: boolean; scope: string; label: string; placeholder: string;
  disabled: boolean; inputRef: Ref<MarkdownInputHandle>;
}) {
  const { send } = useChatPreferences();
  const mount = useRef<HTMLDivElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const editor = useRef<Editor | null>(null);
  const latest = useRef({ value, onChange, onSubmit, disabled, label, placeholder, send });
  latest.current = { value, onChange, onSubmit, disabled, label, placeholder, send };
  const representedValue = useRef(value);
  const restoreFocus = useRef(false);

  useImperativeHandle(inputRef, () => ({ focus: () => {
    if (textarea.current) textarea.current.focus(); else editor.current?.view.focus();
  } }), []);

  useLayoutEffect(() => {
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
        handleKeyDown(view, event) {
          plainPaste = event.shiftKey && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "v";
          if (event.isComposing || view.composing || event.keyCode === 229) return false;
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
        latest.current.onChange(next);
      },
    });
    editor.current = instance;
    if (restoreFocus.current) instance.commands.focus("end");
    return () => {
      restoreFocus.current = instance.view.hasFocus();
      instance.destroy(); editor.current = null;
    };
  }, [plainText, scope]);

  useLayoutEffect(() => {
    const instance = editor.current;
    if (instance) {
      instance.setEditable(!disabled, false);
      instance.view.dom.setAttribute("aria-label", label);
      instance.view.dom.setAttribute("aria-disabled", String(disabled));
      instance.view.dom.setAttribute("data-placeholder", placeholder);
      if (value !== representedValue.current) {
        // An external change is a restored/cleared draft, not a user edit.
        // Reset history so Undo cannot resurrect a sent message or another task.
        instance.commands.setContent(value, { contentType: "markdown", emitUpdate: false });
        const doc = instance.state.doc;
        instance.view.updateState(EditorState.create({
          schema: instance.schema, doc, plugins: instance.state.plugins,
          selection: TextSelection.atEnd(doc),
        }));
        representedValue.current = value;
      }
    }
    if (textarea.current) {
      textarea.current.style.height = "0px";
      textarea.current.style.height = `${Math.min(240, Math.max(38, textarea.current.scrollHeight))}px`;
      if (restoreFocus.current) { textarea.current.focus(); restoreFocus.current = false; }
    }
  }, [value, disabled, label, placeholder, plainText, scope]);

  return plainText ? <textarea aria-label={label} disabled={disabled} ref={textarea} rows={1}
    placeholder={placeholder} value={value} onChange={(event) => onChange(event.target.value)}
    onKeyDown={(event) => {
      if (event.key === "Enter" && !event.shiftKey && !event.altKey && !event.nativeEvent.isComposing && event.keyCode !== 229 &&
        sendsOnEnter(send, value, event.ctrlKey || event.metaKey)) {
        event.preventDefault(); onSubmit();
      }
    }} /> : <div className="v2-markdown-input-host" ref={mount} />;
}
