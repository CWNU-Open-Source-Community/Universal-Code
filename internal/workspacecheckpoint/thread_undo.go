package workspacecheckpoint

import "strings"

// Thread reply undo uses the existing restore journal, with a scoped manifest
// and its own recovery owner. It must never advance the whole-workspace cursor.
const ThreadUndoReceiptPrefix = "thread-reply-undo:"
const ThreadUndoSnapshotTitle = "Thread reply undo (selected paths)"

func IsThreadUndo(t Transaction) bool {
	return t.Kind == TransactionUndo && strings.HasPrefix(t.TriggerReceiptID, ThreadUndoReceiptPrefix)
}

func IsThreadUndoCheckpoint(c Checkpoint) bool { return c.Title == ThreadUndoSnapshotTitle }
