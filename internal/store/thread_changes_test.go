package store

import (
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

func TestThreadReplyChangesIgnoreAnotherSessionAssistantBoundary(t *testing.T) {
	st, run, _ := newMonetarySupervisor(t)
	ctx := t.Context()
	if _, err := st.SaveSessionMessage(ctx, session.NewMessage(run.SessionID, "assistant", "previous root reply")); err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, data := range []struct {
		kind, source, subject string
		payload               map[string]any
	}{
		{events.FileEditAppliedEvent, "fileedit_store", "edit-real", map[string]any{"status": "applied"}},
		{events.SessionMessageEvent, "session_store", "other-session-message", map[string]any{
			"session_id": "other-session", "role": "assistant", "source_kind": session.SourceModelResponse}},
	} {
		event, err := events.New(run.ID, run.MissionID, data.kind, data.source, data.subject, data.payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSessionMessage(ctx, session.NewMessage(run.SessionID, "assistant", "current root reply")); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := list[len(list)-1]
	_, facts, err := st.ListThreadReplyFileEditEvents(ctx, domain.InitialThreadID(run.ID), source.EventID, 100)
	if err != nil || len(facts) != 1 || facts[0].SubjectID != "edit-real" {
		t.Fatalf("another Session truncated root changes: %+v %v", facts, err)
	}
}
