import type { V2ManagedDraft } from "./draft-context";

export type ReviewCommentResult = "added" | "unsaved" | "blocked";

/** Append to the current document, preserving attachments and other windows' versions. */
export function appendReviewComment(draft: V2ManagedDraft, comment: string): ReviewCommentResult {
  if (draft.state.error) return "blocked";
  const current = draft.document.read(draft.scope);
  if (current.conflict || current.error) return "blocked";
  const result = draft.document.update(draft.scope, (previous) => ({
    ...previous, text: previous.text ? `${previous.text}\n\n${comment}` : comment,
  }), current.ref);
  // A failed local-storage write still retains the complete snapshot in memory.
  return result.persisted && !result.error && !result.conflict ? "added" : "unsaved";
}
