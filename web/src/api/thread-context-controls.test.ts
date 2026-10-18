import type { APIClient } from "./client";
import { parseThreadCompactionOperations, recoverThreadCompaction, type ThreadCompactionOperation } from "./thread-context-controls";

const operation: ThreadCompactionOperation = { operation_id: "saved-operation", run_id: "old-run", session_id: "session-1",
  status: "generated", source_sha256: "a".repeat(64), created_at: "2026-10-18T00:00:00Z", usage_known: true };
const list = (operations: unknown[] = [operation]) => ({ version: "thread_context_compaction_operations.v1", thread_id: "thread-1", operations });

test("compaction history validates thread identity, unique operations, and completed result ownership", () => {
  expect(parseThreadCompactionOperations(list(), "thread-1").operations).toEqual([operation]);
  expect(() => parseThreadCompactionOperations(list(), "another-thread")).toThrow();
  expect(() => parseThreadCompactionOperations(list([operation, operation]), "thread-1")).toThrow();
  expect(() => parseThreadCompactionOperations(list([{ ...operation, status: "completed", result: {
    version: "thread_context_compaction.v1", thread_id: "another-thread", run_id: "old-run", session_id: "session-1",
    status: "completed", summary_id: 1, removed_messages: 8, preserved_messages: 4,
    source_sha256: operation.source_sha256, generated: true, replayed: false,
  } }]), "thread-1")).toThrow();
});

test.each(["operation_id", "run_id", "session_id", "source_sha256"])("recovery rejects a response for a different %s", async (field) => {
  const postControl = vi.fn().mockResolvedValue({ ...operation, [field]: field === "source_sha256" ? "b".repeat(64) : "different" });
  await expect(recoverThreadCompaction({ postControl } as unknown as APIClient, "thread-1", operation, "resume")).rejects.toThrow();
});

test("recovery encodes identities and keeps the operation's run as the expected source", async () => {
  const source = { ...operation, operation_id: "saved/operation" };
  const postControl = vi.fn().mockResolvedValue(source);
  await recoverThreadCompaction({ postControl } as unknown as APIClient, "thread/id", source, "discard");
  expect(postControl).toHaveBeenCalledExactlyOnceWith("/threads/thread%2Fid/context/compactions/saved%2Foperation/recover",
    { version: "thread_context_compaction_recovery.v1", expected_run_id: "old-run", action: "discard" }, "thread-context-recover-saved/operation-discard");
});
