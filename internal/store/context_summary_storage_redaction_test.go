package store

import (
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/session"
)

// SQLiteStore.saveContextSummaryTx runs contextmgr.PrepareSummaryForStorage on
// every handoff write, and every read re-validates it. Compaction had already
// masked and excerpted the record text before that pass, so the pass was applying
// secret regexes to serialized JSON and rejecting its own record: the session
// could not persist the handoff that Compact had just produced.
func TestSQLiteStoreSavesCompactedHandoffWithBoundedExcerpt(t *testing.T) {
	st, run, _ := historyFixture(t)
	ctx := t.Context()
	objective := "CONSTRAINT: report the README credential field exactly once. " +
		strings.Repeat("d", 385) + "README.md?token=supersecretvalue123 " +
		strings.Repeat("Remaining bounded diagnostic detail. ", 25)
	historyMessage(t, st, run, objective)
	for range 8 {
		historyMessage(t, st, run, "Progress: validation is still unfinished.")
	}
	stored, err := st.ListSessionMessages(ctx, run.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	messages := make([]contextmgr.Message, 0, len(stored))
	for _, message := range stored {
		messages = append(messages, session.ProjectContextMessage(message))
	}
	if _, err := contextmgr.NewManager(st, contextmgr.DefaultConfig()).
		Compact(ctx, run.SessionID, "ws-history", messages); err != nil {
		t.Fatalf("the store rejected the handoff its own compactor produced: %v", err)
	}
	summary, found, err := st.LatestContextSummary(ctx, run.SessionID)
	if err != nil || !found {
		t.Fatalf("compacted handoff missing: found=%v err=%v", found, err)
	}
	if strings.Contains(summary.Content, "supersecretvalue123") {
		t.Fatalf("stored handoff kept an unredacted credential: %q", summary.Content)
	}
	if !strings.Contains(summary.Content, "[REDACTED:") {
		t.Fatalf("stored handoff lost its redaction marker: %q", summary.Content)
	}
}
