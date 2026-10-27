import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import type { NarrativeEntry } from "../projection/narrative";
import { V2Narrative } from "./narrative";

const markdown = vi.hoisted(() => vi.fn(({ children }: { children: string }) => <p>{children}</p>));
vi.mock("react-markdown", () => ({ default: markdown }));
afterEach(() => { cleanup(); markdown.mockClear(); });

it("shows stored message times with a full local date and timezone, without replacing missing timestamps", () => {
  const createdAt = "2026-10-21T09:16:43Z";
  const entries: NarrativeEntry[] = [
    { id: "user", kind: "user", text: "Request", createdAt, provisional: false },
    { id: "reply", kind: "assistant", text: "Reply", createdAt, provisional: false },
    { id: "missing", kind: "assistant", text: "Older reply", createdAt: "", provisional: false },
    { id: "invalid", kind: "assistant", text: "Invalid time", createdAt: "invalid", provisional: false },
    { id: "live", kind: "assistant", text: "Streaming", createdAt, provisional: true },
  ];
  const { container } = render(<V2Narrative client={{} as APIClient} entries={entries} threadID="thread" />);
  const times = container.querySelectorAll("time");
  expect(times).toHaveLength(2);
  for (const time of times) {
    expect(time).toHaveAttribute("datetime", "2026-10-21T09:16:43.000Z");
    expect(time.textContent).toMatch(/^\d{2}:\d{2}$/u);
    expect(time.title).toContain("2026");
    expect(time.title).toContain("43");
    expect(time).toHaveAccessibleName("Message time: " + time.title);
  }
});

it("copies and reuses processed user text, quotes only the selection, and branches from that input", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
  const onCompose = vi.fn(); const onBranch = vi.fn().mockResolvedValue(undefined);
  const entry: NarrativeEntry = { kind: "user", id: "queued-event", branchSourceID: "queued-event", status: "committed",
    text: "Keep the first part and quote this.", createdAt: "", provisional: false };
  const get = vi.fn().mockResolvedValue({ message_id: entry.id, content: entry.text });
  render(<V2Narrative client={{ get } as unknown as APIClient} entries={[entry]} threadID="thread" onCompose={onCompose} onBranch={onBranch} />);
  await user.click(screen.getByRole("button", { name: "Copy message" }));
  expect(writeText).toHaveBeenCalledWith(entry.text);
  await user.click(screen.getByRole("button", { name: "Reuse text in composer" }));
  expect(onCompose).toHaveBeenLastCalledWith(entry.text);
  const node = screen.getByText(entry.text).firstChild!;
  const range = document.createRange(); range.setStart(node, 24); range.setEnd(node, 34);
  window.getSelection()!.removeAllRanges(); window.getSelection()!.addRange(range);
  await user.click(screen.getByRole("button", { name: "Quote message" }));
  expect(onCompose).toHaveBeenLastCalledWith("> quote this\n\n");
  await user.click(screen.getByRole("button", { name: "Branch into new chat" }));
  expect(onBranch).toHaveBeenCalledWith("queued-event", expect.any(String));
  expect(screen.getByText(entry.text)).toBeInTheDocument();
});

it("renders only changing output while reusing a thousand historical Markdown rows", () => {
  const client = {} as APIClient;
  const history: NarrativeEntry[] = Array.from({ length: 1_000 }, (_, index) => ({
    id: `history-${index}`, kind: "assistant", text: `Historical ${index}`, createdAt: "", provisional: false,
  }));
  const live = (text: string): NarrativeEntry => ({ id: "live", kind: "assistant", text, createdAt: "", provisional: true });
  const view = render(<V2Narrative client={client} entries={[...history, live("First token")]} threadID="thread-1" />);
  expect(markdown).toHaveBeenCalledTimes(1_001);
  for (let revision = 1; revision <= 20; revision++) {
    view.rerender(<V2Narrative client={client} entries={[...history, live(`Token ${revision}`)]} threadID="thread-1" />);
  }
  expect(markdown).toHaveBeenCalledTimes(1_021);
  expect(screen.getByText("Token 20")).toBeInTheDocument();
  // A refetch can rebuild equal projection objects; Markdown still stays cached.
  view.rerender(<V2Narrative client={client} entries={history.map((entry) => ({ ...entry }))} threadID="thread-1" />);
  expect(markdown).toHaveBeenCalledTimes(1_021);
  const revised = history.map((entry, index) => index === 500 ? { ...entry, text: "Corrected durable answer" } : entry);
  view.rerender(<V2Narrative client={client} entries={revised} threadID="thread-1" />);
  expect(markdown).toHaveBeenCalledTimes(1_022);
  expect(screen.getByText("Corrected durable answer")).toBeInTheDocument();
});

it("loads the full stored user message for copy and reuse, and does not replace a failed load with its preview", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
  const full = "Original text ".repeat(1000) + "FINAL_REQUIREMENT";
  const get = vi.fn().mockResolvedValue({ message_id: "input", content: full });
  const onCompose = vi.fn();
  render(<V2Narrative client={{ get } as unknown as APIClient} entries={[{ id: "input", kind: "user", text: "Original text…",
    createdAt: "", provisional: false, status: "committed" }]} threadID="thread" onCompose={onCompose} />);
  expect(get).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Copy message" }));
  expect(writeText).toHaveBeenCalledWith(full);
  await user.click(screen.getByRole("button", { name: "Reuse text in composer" }));
  expect(onCompose).toHaveBeenCalledExactlyOnceWith(full);
  get.mockRejectedValueOnce(new Error("Offline"));
  await user.click(screen.getByRole("button", { name: "Reuse text in composer" }));
  expect(screen.getByRole("alert")).toHaveTextContent("Offline");
  expect(onCompose).toHaveBeenCalledOnce();
});

it("copies the full reply and branches from its final durable event with icon actions", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
  let finish!: () => void;
  const onBranch = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
  const entry: NarrativeEntry = { id: "first-event", branchSourceID: "last-event", kind: "assistant",
    text: "**Full answer**\n\nSecond part", createdAt: "", provisional: false };
  render(<V2Narrative client={{} as APIClient} entries={[entry]} threadID="thread-1" onBranch={onBranch} />);

  const copy = screen.getByRole("button", { name: "Copy response" });
  const branch = screen.getByRole("button", { name: "Branch into new chat" });
  expect(copy.querySelector("svg")).not.toBeNull();
  expect(branch.querySelector("svg")).not.toBeNull();
  await user.click(copy);
  expect(writeText).toHaveBeenCalledWith(entry.text);
  expect(screen.getByRole("button", { name: "Response copied" })).toBeInTheDocument();
  await user.dblClick(branch);
  expect(onBranch).toHaveBeenCalledExactlyOnceWith("last-event", expect.stringMatching(/^thread-branch-/u));
  expect(screen.getByRole("button", { name: "Creating new chat…" })).toBeDisabled();
  await act(async () => finish());
  expect(screen.getByRole("button", { name: "Branch into new chat" })).toBeEnabled();
});

it("retains the same branch request when retrying a failed response", async () => {
  const user = userEvent.setup();
  const onBranch = vi.fn().mockRejectedValueOnce(new Error("Connection lost")).mockResolvedValue(undefined);
  render(<V2Narrative client={{} as APIClient} entries={[{ id: "reply", branchSourceID: "source-event", kind: "assistant",
    text: "Answer", createdAt: "", provisional: false }]} threadID="thread-1" onBranch={onBranch} />);
  await user.click(screen.getByRole("button", { name: "Branch into new chat" }));
  expect(screen.getByRole("alert")).toHaveTextContent("Connection lost");
  await user.click(screen.getByRole("button", { name: "Branch into new chat" }));
  expect(onBranch.mock.calls[1]).toEqual(onBranch.mock.calls[0]);
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

it("waits for a saved reply before exposing branch actions", () => {
  const client = {} as APIClient;
  const onBranch = vi.fn();
  const entry: NarrativeEntry = { id: "reply", kind: "assistant", text: "Streaming", createdAt: "", provisional: true };
  const view = render(<V2Narrative client={client} entries={[entry]} threadID="thread-1" onBranch={onBranch} />);
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
  view.rerender(<V2Narrative client={client} entries={[{ ...entry, provisional: false }]} threadID="thread-1" onBranch={onBranch} />);
  expect(screen.getByRole("button", { name: "Branch into new chat" })).toBeDisabled();
});
