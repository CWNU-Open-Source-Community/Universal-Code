import type { Element, Root, RootContent, Text } from "hast";

const graphemes = new Intl.Segmenter(undefined, { granularity: "grapheme" });

// Decorate the parsed Markdown, keeping its links, emphasis and copyable text.
// At most the latest append is wrapped; history never accumulates character spans.
export function rehypeStreamReveal({ start }: { start: number }) {
  return (tree: Root, file: { value: unknown }) => {
    const source = String(file.value);
    const visit = (parent: Root | Element) => {
      parent.children = parent.children.flatMap((node): RootContent[] => {
        if (node.type === "element") {
          if (node.tagName !== "pre" && node.tagName !== "code") visit(node);
          return [node];
        }
        if (node.type !== "text") return [node];
        const from = node.position?.start.offset;
        const to = node.position?.end.offset;
        if (from === undefined || to === undefined || to <= start) return [node];
        let cut = 0;
        if (from < start) {
          // Entities and escapes change source offsets. Do not replay older text
          // when its rendered value cannot be split at the source boundary.
          if (node.value !== source.slice(from, to)) return [node];
          cut = node.value.length;
          for (const segment of graphemes.segment(node.value)) {
            if (segment.index >= start - from) { cut = segment.index; break; }
          }
        }
        if (cut === node.value.length) return [node];
        const prefix: Text[] = cut ? [{ type: "text", value: node.value.slice(0, cut) }] : [];
        return [...prefix, {
          type: "element", tagName: "span",
          properties: { className: ["v2-stream-reveal"], dataStreamStart: start },
          children: [{ type: "text", value: node.value.slice(cut) }],
        }];
      });
    };
    visit(tree);
  };
}
