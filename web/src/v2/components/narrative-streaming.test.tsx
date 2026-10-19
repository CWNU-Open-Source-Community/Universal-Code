import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { NarrativeEntry } from "../projection/narrative";
import { V2Narrative } from "./narrative";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

const client = {} as APIClient;
const assistant = (text: string, provisional = true): NarrativeEntry => ({
  id: "reply", kind: "assistant", text, createdAt: "", provisional, runId: "run-1",
});

it("reveals only new appends and does not replay a loaded, corrected or completed reply", () => {
  const content = (text: string, provisional = true) =>
    <V2Narrative client={client} entries={[assistant(text, provisional)]} threadID="thread-1" />;
  const view = render(content("已读取说明。"));
  const reveal = () => view.container.querySelector(".v2-stream-reveal");
  expect(reveal()).toBeNull();
  view.rerender(content("已读取说明。开始修改。"));
  const first = reveal();
  expect(first).toHaveTextContent("开始修改。");
  view.rerender(content("已读取说明。开始修改。完成检查。"));
  expect(reveal()).toHaveTextContent("完成检查。");
  expect(reveal()).not.toBe(first);
  expect(view.container.querySelectorAll(".v2-stream-reveal")).toHaveLength(1);
  view.rerender(content("已更正。"));
  expect(reveal()).toBeNull();
  view.rerender(content("已更正。最终回复。", false));
  expect(reveal()).toBeNull();
  expect(screen.getByRole("button", { name: "复制回复" })).toBeInTheDocument();
});

it("keeps Markdown emphasis and file links intact as unfinished markup closes", () => {
  const openFile = vi.fn();
  const content = (text: string) => <V2Narrative client={client} entries={[assistant(text)]}
    threadID="thread-1" onOpenFile={openFile} />;
  const view = render(content("已有 **加粗"));
  view.rerender(content("已有 **加粗**，查看 [README.md](README.md:3)。"));
  expect(view.container.querySelector("strong")).toHaveTextContent("加粗");
  expect(view.container.querySelector("strong .v2-stream-reveal")).toBeNull();
  fireEvent.click(screen.getByRole("link", { name: "README.md" }));
  expect(openFile).toHaveBeenCalledWith(expect.objectContaining({ path: "README.md", line: 3, runID: "run-1" }));
  const link = screen.getByRole("link", { name: "README.md" });
  view.rerender(content("已有 **加粗**，查看 [README.md](README.md:3)。继续。"));
  expect(screen.getByRole("link", { name: "README.md" })).toBe(link);
  expect(link.querySelector(".v2-stream-reveal")).toBeNull();
});

it("leaves code stable during streaming and copies the original code and reply", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  const content = (text: string, provisional = true) =>
    <V2Narrative client={client} entries={[assistant(text, provisional)]} threadID="thread-1" />;
  const start = "说明。\n\n```ts\nconst one = 1;\n";
  const full = start + "const two = 2;\n```\n\n完成 👩‍💻 é。";
  const view = render(content(start));
  const code = view.container.querySelector("pre");
  view.rerender(content(full));
  expect(view.container.querySelector("pre")).toBe(code);
  expect(code?.querySelector(".v2-stream-reveal")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "复制代码" }));
  await waitFor(() => expect(writeText).toHaveBeenLastCalledWith("const one = 1;\nconst two = 2;\n"));
  view.rerender(content(full, false));
  fireEvent.click(screen.getByRole("button", { name: "复制回复" }));
  await waitFor(() => expect(writeText).toHaveBeenLastCalledWith(full));
});

it.each([
  ["Emoji 👩", "Emoji 👩‍💻 后文", " 后文"],
  ["Accent e", "Accent é 后文", " 后文"],
  ["旧 &amp; 内容", "旧 &amp; 内容 新增", undefined],
  ["旧 \\* 内容", "旧 \\* 内容 新增", undefined],
] as const)("preserves graphemes and escaped source when appending to %s", (before, after, revealed) => {
  const content = (text: string, provisional = true) =>
    <V2Narrative client={client} entries={[assistant(text, provisional)]} threadID="thread-1" />;
  const view = render(content(before));
  view.rerender(content(after));
  expect(view.container.querySelector(".v2-stream-reveal")?.textContent).toBe(revealed);
  const streamedText = view.container.querySelector(".v2-assistant-turn > p")?.textContent;
  view.rerender(content(after, false));
  expect(view.container.querySelector(".v2-assistant-turn > p")?.textContent).toBe(streamedText);
});
