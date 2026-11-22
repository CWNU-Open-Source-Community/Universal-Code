import { readFileSync } from "node:fs";
import { URL as FileURL } from "node:url";
import { render } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { NarrativeEntry } from "../projection/narrative";
import { MarkdownInput } from "./markdown-input";
import { V2Narrative } from "./narrative";

const styles = ["../../styles.css", "../styles.css", "./markdown-input.css"].map((file) =>
  readFileSync(new FileURL(file, import.meta.url), "utf8")).join("\n");

it("uses the chat and code sizes for saved messages with Han paragraphs, English, nested lists and code", () => {
  const text = [
    "第一段。", "**第二段** mixed.", "English paragraph.", "Before the list.",
    "- **First**\n\n  Continued.\n\n  - Nested\n\n- **Second**",
    "```text\nfirst\nsecond\n```", "An `inline` example.",
  ].join("\n\n");
  const entries: NarrativeEntry[] = [
    { id: "user", kind: "user", text, createdAt: "", provisional: false },
    { id: "reply", kind: "assistant", text, createdAt: "", provisional: false },
  ];
  const view = render(<><style>{styles}</style>
    <V2Narrative client={{} as APIClient} entries={entries} threadID="thread" /></>);
  const messages = view.container.querySelectorAll<HTMLElement>(".v2-user-markdown, .v2-assistant-turn");
  expect(messages).toHaveLength(2);
  for (const message of messages) {
    const paragraphs = [...message.querySelectorAll(":scope > p")];
    expect(paragraphs.map((p) => p.hasAttribute("data-han-text"))).toEqual([true, true, false, false, false]);
    expect(paragraphs.map((p) => getComputedStyle(p).marginBottom)).toEqual(["4px", "11px", "11px", "10px", "0px"]);
    for (const paragraph of [paragraphs[0], paragraphs[2]]) {
      expect(getComputedStyle(paragraph).fontSize).toBe("16px");
      expect(getComputedStyle(paragraph).lineHeight).toBe("24px");
      expect(getComputedStyle(paragraph).letterSpacing).toBe("normal");
    }
    const list = message.querySelector("ul")!;
    expect(getComputedStyle(list).paddingInlineStart).toBe("21px");
    expect(getComputedStyle(list).marginBottom).toBe("10px");
    expect(getComputedStyle(list.querySelector(":scope > li + li")!).marginTop).toBe("8px");
    expect(getComputedStyle(list.querySelector("li > p")!).marginBottom).toBe("0px");
    expect(getComputedStyle(list.querySelector("li > p + p")!).marginTop).toBe("11px");
    expect(getComputedStyle(list.querySelector("li > ul")!).marginTop).toBe("8px");
    expect(getComputedStyle(list.querySelector("strong")!).fontWeight).toBe("600");
    expect(getComputedStyle(message.querySelector(".v2-markdown-code-block")!).marginTop).toBe("14px");
    expect(getComputedStyle(message.querySelector("pre code")!).fontSize).toBe("15px");
    expect(getComputedStyle(message.querySelector("pre code")!).lineHeight).toBe("22.5px");
    expect(getComputedStyle(message.querySelector("p code")!).fontSize).toBe("14.72px");
  }
  view.container.style.setProperty("--prayu-message-size", "20px");
  view.container.style.setProperty("--prayu-code-size", "19px");
  for (const message of messages) {
    for (const paragraph of message.querySelectorAll(":scope > p")) {
      expect(getComputedStyle(paragraph).fontSize).toBe("20px");
      expect(getComputedStyle(paragraph).lineHeight).toBe("28px");
      expect(getComputedStyle(paragraph).letterSpacing).toBe("normal");
    }
    expect(getComputedStyle(message.querySelector("li strong")!).lineHeight).toBe("28px");
    expect(getComputedStyle(message.querySelector("p code")!).fontSize).toBe("18.4px");
    expect(getComputedStyle(message.querySelector("pre code")!).fontSize).toBe("19px");
    expect(getComputedStyle(message.querySelector("pre code")!).lineHeight).toBe("28.5px");
  }
});

it("keeps paragraphs flush in the rich editor and uses the selected sizes in both editing modes", () => {
  const editor = (plainText: boolean) => <><style>{styles}</style><div className="v2-composer">
    <MarkdownInput value={"第一段。\n\nEnglish paragraph.\n\n```text\ncode\n```"} onChange={vi.fn()} onSubmit={vi.fn()}
      plainText={plainText} scope="sizing" label="Message" placeholder="" disabled={false} inputRef={null} />
  </div></>;
  const view = render(editor(false));
  const rich = view.getByRole("textbox");
  expect(getComputedStyle(rich.querySelector("p + p")!).marginTop).toBe("0px");
  expect(getComputedStyle(rich.querySelector("pre code")!).lineHeight).toBe("20px");
  view.container.style.setProperty("--prayu-message-size", "20px");
  view.container.style.setProperty("--prayu-code-size", "19px");
  expect(getComputedStyle(rich.querySelector("p")!).lineHeight).toBe("28px");
  expect(getComputedStyle(rich.querySelector("pre code")!).fontSize).toBe("19px");
  expect(getComputedStyle(rich.querySelector("pre code")!).lineHeight).toBe("24px");
  view.rerender(editor(true));
  expect(getComputedStyle(view.getByRole("textbox")).fontSize).toBe("20px");
  expect(getComputedStyle(view.getByRole("textbox")).lineHeight).toBe("28px");
});
