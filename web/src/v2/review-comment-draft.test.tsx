import { renderHook } from "@testing-library/react";
import { DraftDocument, type DraftSnapshot } from "./draft-document";
import { V2RecoveryProvider, useV2RecoveryStore } from "./recovery-storage";
import { appendReviewComment } from "./review-comment-draft";

const scope = { key: "thread:review-task", workspaceID: "review-project" };
const original: DraftSnapshot = { text: "  Keep my draft.\n", files: [{ id: "file-a", path: "notes.txt", digest: "a".repeat(64), partial: false, redacted: false }],
  images: [{ id: "image-a", workspace_id: scope.workspaceID, sha256: "b".repeat(64), mime_type: "image/png", byte_size: 48, width: 2, height: 3, name: "proof.png" }] };
function setup() {
  const store = renderHook(() => useV2RecoveryStore(), { wrapper: ({ children }) =>
    <V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="review-comments">{children}</V2RecoveryProvider> }).result.current!;
  const document = new DraftDocument(store);
  const state = document.update(scope, original);
  return { store, document, draft: { document, scope, state, changeText: vi.fn() } };
}
beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

it("appends against the latest draft, retaining whitespace, attachments and a different task across reload", () => {
  const { store, document, draft } = setup();
  const other = { ...scope, key: "thread:other-task" };
  document.update(other, { text: "Other task" });
  document.update(scope, { text: `${original.text}More recent text` });
  expect(appendReviewComment(draft, "First line comment")).toBe("added");
  expect(appendReviewComment(draft, "Second line comment")).toBe("added");
  const reloaded = new DraftDocument(store);
  expect(reloaded.read(scope).snapshot).toEqual({ ...original,
    text: `${original.text}More recent text\n\nFirst line comment\n\nSecond line comment` });
  expect(reloaded.read(other).snapshot.text).toBe("Other task");
});

it("blocks an append when another window has left conflicting versions", () => {
  const { store, document, draft } = setup();
  const other = new DraftDocument(store);
  const remote = other.read(scope);
  document.update(scope, { text: "Window A" }, draft.state.ref);
  other.update(scope, { text: "Window B" }, remote.ref);
  expect(document.read(scope).conflict).toBe(true);
  const before = document.read(scope).heads;
  expect(appendReviewComment(draft, "Do not lose either draft")).toBe("blocked");
  expect(document.read(scope).heads).toEqual(before);
});

it("reports failed persistence while retaining the comment and attachments in memory, and blocks repeat appends", () => {
  const { store, document, draft } = setup();
  vi.spyOn(store, "write").mockImplementation(() => { throw new Error("Disk full"); });
  expect(appendReviewComment(draft, "Retained feedback")).toBe("unsaved");
  expect(document.read(scope).snapshot).toEqual({ ...original, text: `${original.text}\n\nRetained feedback` });
  expect(appendReviewComment(draft, "Retained feedback")).toBe("blocked");
  expect(document.read(scope).snapshot.text.match(/Retained feedback/gu)).toHaveLength(1);
});

it("preserves migration errors instead of bypassing the draft hook's guard", () => {
  const { document, draft } = setup();
  expect(appendReviewComment({ ...draft, state: { ...draft.state, error: "Unreadable legacy draft" } }, "feedback")).toBe("blocked");
  expect(document.read(scope).snapshot).toEqual(original);
});
