import { absoluteFilePath, parseProjectFileLink } from "../../lib/chat-resources";

type Node = { type: string; value?: string; url?: string; children?: Node[]; position?: { start: { offset?: number }; end: { offset?: number } } };
const pathLike = /^(?:[a-z]:[\\/]|\.?\.?[\\/]|[\w@.-]+[\\/])[^\r\n]*\.[\w-]+(?:(?:#L|:)\d+(?::\d+)?)?$|^[\w.-]+\.[a-z][a-z\d]*(?:(?:#L|:)\d+(?::\d+)?)?$/iu;
// A bare dotted word needs a familiar file extension; domains and version or
// API names are ordinary prose. A path containing a separator is explicit.
const fileExtensions = new Set("html htm svg md mdx txt rst json jsonc yaml yml toml xml ini cfg conf log csv tsv sql pdf doc docx xls xlsx ppt pptx png jpg jpeg gif webp avif ico zip tar gz 7z js jsx ts tsx css scss sass less vue svelte py go rs java kt c h cpp hpp cs sh bash ps1 bat cmd rb php swift dart lua ipynb wasm lock".split(" "));
function proseFilePath(value: string): boolean {
  if (!pathLike.test(value)) return false;
  const file = parseProjectFileLink(value);
  if (!file) return false;
  return file.path.includes("/") || fileExtensions.has(file.path.split(".").at(-1)!.toLowerCase());
}
// Explicit Markdown labels remain intact. Recognize file paths in prose and
// inline code without rewriting fenced commands, existing links or images.
export function remarkResourceLinks() {
  return (tree: Node) => {
    const visit = (parent: Node) => {
      if (!parent.children || ["link", "image", "code", "html"].includes(parent.type)) return;
      parent.children = parent.children.flatMap((node): Node[] => {
        if (node.type === "inlineCode" && pathLike.test(node.value ?? "") && parseProjectFileLink(node.value!)) {
          // Absolute code paths may be examples or refer to another machine.
          // Preserve their literal text; an explicit Markdown link can still
          // offer native actions for an absolute file in this workspace.
          if (absoluteFilePath(parseProjectFileLink(node.value!)!.path)) return [node];
          return [{ type: "link", url: node.value, children: [{ type: "text", value: node.value }] }];
        }
        if (node.type === "text") {
          // CJK punctuation commonly touches a delivered filename, e.g.
          // pelican-bicycle.html（341 行）. Keep that punctuation in prose.
          const value = node.value ?? "";
          if (!/\.[a-z]/iu.test(value)) return [node];
          const parts = value.split(/([\s，。！？；：（）【】《》“”‘’、]+)/u).filter(Boolean);
          let offset = node.position?.start.offset;
          let linked = false;
          const children = parts.flatMap((part): Node[] => {
            const start = offset;
            if (offset !== undefined) offset += part.length;
            const text = (value: string, from = 0): Node => ({ type: "text", value,
              position: start === undefined ? undefined : { start: { offset: start + from }, end: { offset: start + from + value.length } } });
            const prefix = part.match(/^[([{<"']+/u)?.[0] ?? "";
            const suffix = part.slice(prefix.length).match(/[.,;!?)\]}>"']+$/u)?.[0] ?? "";
            const path = part.slice(prefix.length, part.length - suffix.length);
            if (!proseFilePath(path)) return [text(part)];
            linked = true;
            const child = text(path, prefix.length);
            return [...(prefix ? [text(prefix)] : []), { type: "link", url: path, children: [child], position: child.position },
              ...(suffix ? [text(suffix, prefix.length + path.length)] : [])];
          });
          return linked ? children : [node];
        }
        visit(node); return [node];
      });
    };
    visit(tree);
  };
}
