import { act, renderHook, waitFor } from "@testing-library/react";
import { useState } from "react";
import type { APIClient } from "../api/client";
import type { ThreadTranscriptItemView } from "../api/types";
import { readPromptHistory, usePromptHistory } from "./use-prompt-history";

const t = (_zh: string, en: string) => en;
function item(id: string, order: number, patch: Partial<ThreadTranscriptItemView> = {}): ThreadTranscriptItemView {
  return { id, run_id: `run-${order}`, run_ordinal: order, sequence: 1, source: "operator", kind: "operator_input",
    activity_type: "message", detail: `preview ${id}`, title: "Message", durable: true, provisional: false,
    created_at: `2026-10-22T00:00:${String(order).padStart(2, "0")}Z`, ...patch } as ThreadTranscriptItemView;
}
const page = (items: ThreadTranscriptItemView[], next_cursor = "") => ({ items, page: { limit: 100, next_cursor }, requestID: "1" });
const clientOf = (getPage: unknown, get = vi.fn()) => ({ getPage, get }) as unknown as APIClient;

it("reads all pages newest first across runs and excludes unsent, internal, stream and empty inputs", async () => {
  const getPage = vi.fn((_path, _query, cursor) => Promise.resolve(cursor ? page([item("old", 1)]) : page([
    item("new", 2), item("new", 2), item("pending", 3, { status: "pending" }), item("cancelled", 4, { status: "cancelled" }),
    item("internal", 5, { detail: "Continue mission at turn 2 using only the structured tools offered by Go when needed: hidden" }),
    item("assistant", 6, { source: "model", kind: "model_update" }), item("live", 7, { provisional: true }),
    item("empty", 8, { detail: "" }), item("undurable", 9, { durable: false }),
  ], "older")));
  expect(await readPromptHistory(clientOf(getPage), "thread-a", new AbortController().signal, t)).toEqual(["new", "old"]);
  expect(getPage.mock.calls.map((args) => args[2])).toEqual(["", "older"]);
});

it.each(["repeated", "truncated"])("rejects %s history instead of treating a partial index as complete", async (reason) => {
  const getPage = vi.fn(async () => ({ ...page([item("one", 1)], "same"),
    ...(reason === "truncated" ? { page: { limit: 100, truncated: true } } : {}) }));
  await expect(readPromptHistory(clientOf(getPage), "thread-a", new AbortController().signal, t)).rejects.toThrow(/history/);
});

function harness(client: APIClient) {
  return renderHook(({ scope = "a", blocked = false, boundary = true }) => {
    const [value, setValue] = useState("");
    const history = usePromptHistory({ client, threadID: scope, scope, blocked, value,
      apply: (next, expected) => setValue((current) => current === expected ? next : current), atBoundary: () => boundary });
    return { history, value, edit: (text: string) => { history.cancel(); setValue(text); } };
  }, { initialProps: { scope: "a", blocked: false, boundary: true } });
}

it("fetches full bodies only as recalled, walks older/newer, and returns to the original empty draft", async () => {
  const full = "完整正文\n" + "长消息".repeat(2000);
  const get = vi.fn(async (path: string) => ({ message_id: path.split("/").at(-1), content: path.endsWith("new") ? full : "Older" }));
  const ui = harness(clientOf(vi.fn(async () => page([item("old", 1), item("new", 2)])), get));
  act(() => { expect(ui.result.current.history.navigate("older")).toBe(true); });
  await waitFor(() => expect(ui.result.current.value).toBe(full));
  expect(get).toHaveBeenCalledTimes(1);
  act(() => { ui.result.current.history.navigate("older"); });
  await waitFor(() => expect(ui.result.current.value).toBe("Older"));
  act(() => { ui.result.current.history.navigate("newer"); });
  await waitFor(() => expect(ui.result.current.value).toBe(full));
  expect(get).toHaveBeenCalledTimes(2);
  act(() => { ui.result.current.history.navigate("newer"); });
  expect(ui.result.current.value).toBe(""); expect(ui.result.current.history.notice).toBeNull();
});

it.each(["edit", "scope", "attachment", "selection", "cancel"])("does not apply a late body after %s changes", async (change) => {
  let finish!: (body: unknown) => void;
  const get = vi.fn(() => new Promise((resolve) => { finish = resolve; }));
  const ui = harness(clientOf(vi.fn(async () => page([item("one", 1)])), get));
  act(() => { ui.result.current.history.navigate("older"); });
  await waitFor(() => expect(get).toHaveBeenCalledOnce());
  if (change === "edit") act(() => ui.result.current.edit("手工编辑或引用的草稿"));
  else if (change === "scope") ui.rerender({ scope: "b", blocked: false, boundary: true });
  else if (change === "attachment") ui.rerender({ scope: "a", blocked: true, boundary: true });
  else if (change === "selection") ui.rerender({ scope: "a", blocked: false, boundary: false });
  else act(() => { ui.result.current.history.navigate("newer"); });
  await act(async () => finish({ message_id: "one", content: "Must not replace the draft" }));
  expect(ui.result.current.value).toBe(change === "edit" ? "手工编辑或引用的草稿" : "");
  expect(ui.result.current.history.notice).toBeNull();
});

it("keeps the draft after a failed read and retries without using preview text", async () => {
  const get = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue({ message_id: "one", content: "Full input" });
  const ui = harness(clientOf(vi.fn(async () => page([item("one", 1)])), get));
  act(() => { ui.result.current.history.navigate("older"); });
  await waitFor(() => expect(ui.result.current.history.notice?.error).toBe(true));
  expect(ui.result.current.value).toBe("");
  act(() => { ui.result.current.history.navigate("older"); });
  await waitFor(() => expect(ui.result.current.value).toBe("Full input"));
  act(() => ui.result.current.edit("Edited"));
  act(() => { expect(ui.result.current.history.navigate("newer")).toBe(false); });
  expect(ui.result.current.value).toBe("Edited");
});
