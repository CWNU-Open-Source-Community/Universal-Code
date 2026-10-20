import { parseProjectFileLink } from "../../lib/chat-resources";

type Node = { type: string; value?: string; url?: string; children?: Node[]; position?: { start: { offset?: number }; end: { offset?: number } } };
const pathLike = /^(?:[a-z]:[\\/]|\.?\.?[\\/]|[\w@.-]+[\\/])[^\r\n]*\.[\w-]+(?:(?:#L|:)\d+(?::\d+)?)?$|^[\w.-]+\.[a-z][a-z\d]*(?:(?:#L|:)\d+(?::\d+)?)?$/iu;
// Explicit Markdown labels remain intact. Recognize file paths in prose and
// inline code without rewriting fenced commands, existing links or images.
export function remarkResourceLinks() {
  return (tree: Node) => {
    const visit = (parent: Node) => {
      if (!parent.children || ["link", "image", "code", "html"].includes(parent.type)) return;
      parent.children = parent.children.flatMap((node): Node[] => {
        if (node.type === "inlineCode" && pathLike.test(node.value ?? "") && parseProjectFileLink(node.value!)) {
          return [{ type: "link", url: node.value, children: [{ type: "text", value: node.value }] }];
        }
        if (node.type === "text") {
          const value = node.value ?? "", parts = value.split(/(\S+)/u).filter(Boolean);
          const isPath = (part: string) => pathLike.test(part) && /\.[a-z][\w-]*(?:(?:#L|:)\d+(?::\d+)?)?$/iu.test(part) && parseProjectFileLink(part);
          if (!parts.some(isPath)) return [node];
          let offset = node.position?.start.offset;
          return parts.map((part): Node => {
            const position = offset === undefined ? undefined : { start: { offset }, end: { offset: offset + part.length } };
            if (offset !== undefined) offset += part.length;
            const child = { type: "text", value: part, position };
            return isPath(part) ? { type: "link", url: part, children: [child], position } : child;
          });
        }
        visit(node); return [node];
      });
    };
    visit(tree);
  };
}
