import type { APIClient } from "../api/client";
import type { PageResult, ThreadTranscriptItemView } from "../api/types";
import { formatThreadExport, readThreadExport, threadExportFilename } from "./thread-export";

const t = (_zh: string, en: string) => en;
function item(id: string, sequence: number, source = "operator", patch: Partial<ThreadTranscriptItemView> = {}): ThreadTranscriptItemView {
  return { id, sequence, run_id: "run-1", run_ordinal: 1, activity_type: "message", source,
    kind: source === "operator" ? "operator_input" : "model_update", detail: `preview ${id}`, title: "Message",
    durable: true, provisional: false, created_at: `2026-10-22T00:00:${String(sequence).padStart(2, "0")}Z`, ...patch } as ThreadTranscriptItemView;
}
function page(items: ThreadTranscriptItemView[], next_cursor = ""): PageResult<ThreadTranscriptItemView> {
  return { items, page: { limit: 100, next_cursor }, requestID: "read-1" };
}
const clientOf = (getPage: unknown, get: unknown) => ({ getPage, get }) as APIClient;

it.each(["operator", "model"])("preserves saved %s whitespace and indented code in Markdown and text", async (source) => {
  const body = "\n    const first = 1;\n    const second = 2;  \n\n";
  const entry = item("saved", 1, source);
  const result = await readThreadExport(clientOf(vi.fn(async () => page([entry])),
    vi.fn(async () => ({ message_id: entry.id, content: body }))), "thread-1", new AbortController().signal, t);
  expect(result).toHaveLength(1);
  expect(result[0].text).toBe(body);
  const role = source === "operator" ? "User" : "Assistant";
  expect(formatThreadExport("Code", result, "markdown", t)).toBe(`# Code\n\n## ${role}\n\n${body}\n`);
  expect(formatThreadExport("Code", result, "text", t)).toBe(`Code\n\n${role}\n\n${body}\n`);
});

it("preserves each assistant part and paragraph join while suppressing a repeated combined ending", async () => {
  const first = "    const first = 1;\n    const second = 2;\n";
  const second = "\n\treturn first + second;  \n";
  const repeated = `${first.trim()}\n\n${second.trim()}\n\n`;
  const bodies: Record<string, string> = { first, second, repeated, next: "\n    newRun();\n" };
  const entries = [item("first", 1, "model"), item("second", 2, "model"), item("repeated", 3, "model"),
    item("next", 4, "model", { run_id: "run-2", run_ordinal: 2 })];
  const result = await readThreadExport(clientOf(vi.fn(async () => page(entries)), vi.fn(async (path: string) => {
    const id = path.split("/").at(-1)!;
    return { message_id: id, content: bodies[id] };
  })), "thread-1", new AbortController().signal, t);
  expect(result).toHaveLength(2);
  expect(result[0]).toMatchObject({ text: `${first}\n\n${second}`, messageIDs: ["first", "second", "repeated"], branchSourceID: "repeated" });
  expect(result[1]).toMatchObject({ text: bodies.next, runId: "run-2" });
  for (const format of ["markdown", "text"] as const) {
    const prefix = format === "markdown" ? "# " : "";
    const role = format === "markdown" ? "## Assistant" : "Assistant";
    expect(formatThreadExport("Code", result, format, t))
      .toBe(`${prefix}Code\n\n${role}\n\n${first}\n\n${second}\n\n---\n\n${role}\n\n${bodies.next}\n`);
  }
});

it("keeps the first saved body's whitespace when a duplicate ending differs only in whitespace", async () => {
  const first = "    done();  \n";
  const entries = [item("first", 1, "model"), item("repeated", 2, "model")];
  const result = await readThreadExport(clientOf(vi.fn(async () => page(entries)), vi.fn(async (path: string) => {
    const id = path.split("/").at(-1)!;
    return { message_id: id, content: id === "first" ? first : "\n\ndone();\n" };
  })), "thread-1", new AbortController().signal, t);
  expect(result).toHaveLength(1);
  expect(result[0]).toMatchObject({ text: first, messageIDs: ["first", "repeated"], branchSourceID: "repeated" });
});

it("rechecks full-body visibility without removing ordinary user text that resembles a model placeholder", async () => {
  const bodies: Record<string, string> = {
    user: "  Mock response is the bug I want fixed.\n",
    synthetic: "\n  Continue mission at turn 2 using only the structured tools offered by Go when needed: hidden\n",
    internal: "  Mock response: hidden model placeholder\n",
  };
  const entries = [item("user", 1), item("synthetic", 2), item("internal", 3, "model")];
  const result = await readThreadExport(clientOf(vi.fn(async () => page(entries)), vi.fn(async (path: string) => {
    const id = path.split("/").at(-1)!;
    return { message_id: id, content: bodies[id] };
  })), "thread-1", new AbortController().signal, t);
  expect(result).toHaveLength(1);
  expect(result[0]).toMatchObject({ id: "user", text: bodies.user });
  expect(formatThreadExport("Visibility", result, "markdown", t)).not.toContain("hidden");
});

it("reads all pages across runs and full long bodies, in order, while excluding internal and provisional rows", async () => {
  const full = "原始内容\n" + "完整长文。".repeat(2500) + "\n```ts\nconst keep = '<raw>';\n```";
  const original = item("old-user", 1);
  const reply = item("old-reply", 2, "model");
  const next = item("new-user", 3, "operator", { run_id: "run-2", run_ordinal: 2 });
  const final = item("new-reply", 4, "model", { run_id: "run-2", run_ordinal: 2 });
  const getPage = vi.fn((_path: string, _query: unknown, cursor: string) => Promise.resolve(cursor
    ? page([original, reply]) : page([reply, next, final,
      item("system", 5, "harness"), item("tool", 6, "model", { activity_type: "execute", kind: "tool_call" }),
      item("stream", 7, "model", { provisional: true }),
      item("synthetic", 8, "operator", { detail: "Continue mission at turn 2 using only the structured tools offered by Go when needed: secret" }),
    ], "older")));
  const bodies: Record<string, string> = { "old-user": full, "old-reply": "Old answer", "new-user": "New request", "new-reply": "New answer" };
  const get = vi.fn((path: string) => { const id = path.split("/").at(-1)!; return Promise.resolve({ message_id: id, content: bodies[id] }); });
  const result = await readThreadExport(clientOf(getPage, get), "thread-1", new AbortController().signal, t);
  expect(result.map(({ text }) => text)).toEqual([full, "Old answer", "New request", "New answer"]);
  expect(getPage.mock.calls.map((call) => call[2])).toEqual(["", "older"]);
  expect(get.mock.calls.map(([path]) => path)).toEqual(Object.keys(bodies).map((id) => `/threads/thread-1/messages/${id}`));
  const markdown = formatThreadExport("Long history", result, "markdown", t);
  expect(markdown).toContain(full);
  expect(markdown).not.toMatch(/preview|secret|system|stream/);
});

it("merges duplicate public assistant endings and labels pending and withdrawn inputs", async () => {
  const entries = [item("pending", 1, "operator", { status: "pending" }),
    item("withdrawn", 2, "operator", { status: "cancelled" }), item("progress", 3, "model"), item("end", 4, "model")];
  const result = await readThreadExport(clientOf(vi.fn(async () => page(entries)), vi.fn(async (path: string) => {
    const id = path.split("/").at(-1)!;
    return { message_id: id, content: id === "progress" || id === "end" ? "Finished" : id };
  })), "thread-1", new AbortController().signal, t);
  expect(result).toHaveLength(3);
  const content = formatThreadExport("Report", result, "text", t);
  expect(content).toContain("User (pending)"); expect(content).toContain("User (withdrawn)");
  expect(content.match(/Finished/g)).toHaveLength(1);
  const filename = threadExportFilename('报告:/\\?<>"|*\n.', "markdown");
  expect(filename).toMatch(/^conversation-报告.+\.md$/u);
  expect(filename).not.toMatch(/[\\/:*?"<>|\u0000-\u001f]/u);
});

it("truncates export filenames without splitting Unicode or ending in separators", () => {
  const prefix = "a".repeat(99);
  expect(threadExportFilename(`${prefix}😀suffix`, "markdown")).toBe(`conversation-${prefix}😀.md`);
  expect(threadExportFilename(`${prefix} tail`, "text")).toBe(`conversation-${prefix}.txt`);
});

it("keeps ZWJ emoji families whole when truncating export filenames", () => {
  const family = "👨‍👩‍👧‍👦";
  const prefix = "a".repeat(99);
  expect(threadExportFilename(`${prefix}${family}tail`, "markdown")).toBe(`conversation-${prefix}${family}.md`);
  expect(threadExportFilename(`${prefix}a${family}`, "text")).toBe(`conversation-${prefix}a.txt`);
});

it("rejects missing full bodies instead of returning partial history", async () => {
  const get = vi.fn(async (path: string) => {
    if (path.endsWith("second")) throw new Error("body unavailable");
    return { message_id: "first", content: "Complete first message" };
  });
  await expect(readThreadExport(clientOf(vi.fn(async () => page([item("first", 1), item("second", 2)])), get),
    "thread-1", new AbortController().signal, t)).rejects.toThrow("body unavailable");
});

it.each(["repeated", "truncated"])("rejects %s pagination instead of exporting an incomplete conversation", async (failure) => {
  const getPage = vi.fn(async () => ({ ...page([item("first", 1)], failure === "repeated" ? "same" : ""),
    ...(failure === "truncated" ? { page: { limit: 100, truncated: true } } : {}) }));
  const get = vi.fn();
  await expect(readThreadExport(clientOf(getPage, get), "thread-1", new AbortController().signal, t)).rejects.toThrow(/history/);
  expect(get).not.toHaveBeenCalled();
});

it("rejects a mismatched saved body and respects cancellation", async () => {
  const getPage = vi.fn(async () => page([item("first", 1)]));
  const get = vi.fn(async () => ({ message_id: "another", content: "wrong conversation" }));
  await expect(readThreadExport(clientOf(getPage, get), "thread-1", new AbortController().signal, t)).rejects.toThrow(/did not match/);
  const abort = new AbortController(); abort.abort(); getPage.mockClear();
  await expect(readThreadExport(clientOf(getPage, get), "thread-1", abort.signal, t)).rejects.toThrow();
  expect(getPage).not.toHaveBeenCalled();
});
