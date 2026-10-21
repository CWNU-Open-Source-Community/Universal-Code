package store

import (
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

func replyUpdateFixture(t *testing.T) (*SQLiteStore, domain.Run) {
	t.Helper()
	st := openWorkItemTestStore(t)
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "reply updates", Profile: "review", Interactive: true, Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	return st, run
}

func replyUpdateBaseline(t *testing.T, st *SQLiteStore) string {
	t.Helper()
	items, cursor, more, err := st.ListThreadReplyUpdates(t.Context(), "", 100)
	if err != nil || len(items) != 0 || cursor == "" || more {
		t.Fatalf("baseline: %#v %q %v %v", items, cursor, more, err)
	}
	return cursor
}

func appendReplyUpdateEvent(t *testing.T, st *SQLiteStore, run domain.Run, kind, source string, payload any) string {
	t.Helper()
	event, err := events.New(run.ID, run.MissionID, kind, source, run.ID, payload)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	event, err = insertRunEventTx(t.Context(), tx, event)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return event.EventID
}

func collectReplyUpdates(t *testing.T, st *SQLiteStore, cursor string, limit int) ([]domain.ThreadSearchMatch, string) {
	t.Helper()
	var all []domain.ThreadSearchMatch
	for range 100 {
		items, next, more, err := st.ListThreadReplyUpdates(t.Context(), cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, items...)
		if !more {
			return all, next
		}
		if cursor == next {
			t.Fatal("continuation made no progress")
		}
		cursor = next
	}
	t.Fatal("reply update pagination did not finish")
	return nil, ""
}

func replyUpdateIDs(items []domain.ThreadSearchMatch) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.MessageID
	}
	return ids
}

func TestThreadReplyUpdatesBaselineInternalPagesAndConcurrentAppend(t *testing.T) {
	st, run := replyUpdateFixture(t)
	branchReplyEvent(t, st, run, "old answer")
	cursor := replyUpdateBaseline(t, st)
	appendReplyUpdateEvent(t, st, run, events.ModelStartedEvent, "model", map[string]string{"private": "hidden provider payload"})
	first := branchReplyEvent(t, st, run, "first new answer")
	second := branchReplyEvent(t, st, run, "second new answer")
	items, next, more, err := st.ListThreadReplyUpdates(t.Context(), cursor, 1)
	if err != nil || len(items) != 0 || !more || next == cursor {
		t.Fatalf("internal source page: %#v %q %v %v", items, next, more, err)
	}
	third := branchReplyEvent(t, st, run, "appended after the first page")
	got, next := collectReplyUpdates(t, st, next, 1)
	if !reflect.DeepEqual(replyUpdateIDs(got), []string{first, second, third}) {
		t.Fatalf("append shifted the source cursor: %#v", got)
	}
	for _, item := range got {
		if item.ThreadID != domain.InitialThreadID(run.ID) || item.RunOrdinal != 1 || item.Content != "" || item.ThreadTitle != "" {
			t.Fatalf("wrong identity or leaked text: %#v", item)
		}
	}
	items, same, more, err := st.ListThreadReplyUpdates(t.Context(), next, 100)
	if err != nil || len(items) != 0 || more || same != next {
		t.Fatalf("idle poll: %#v %v", items, err)
	}
	replayed, _ := collectReplyUpdates(t, st, cursor, 100)
	if !reflect.DeepEqual(replyUpdateIDs(replayed), replyUpdateIDs(got)) {
		t.Fatal("cursor replay changed identities")
	}
}

func TestThreadReplyUpdatesPublicBoundaryAndDistinctSavedReplyIdentities(t *testing.T) {
	st, run := replyUpdateFixture(t)
	branchReplyEvent(t, st, run, "same answer")
	cursor := replyUpdateBaseline(t, st)
	for _, message := range []session.Message{
		session.NewMessage(run.SessionID, "system", "hidden system prompt"),
		session.NewEvidenceMessage(run.SessionID, session.SourceToolResult, "private-tool", "private tool output"),
		session.NewMessage(run.SessionID, "user", "Continue mission at turn 2 using only the structured tools offered by Go when needed: original request"),
		session.NewMessage(run.SessionID, "assistant", "Mock response hidden"),
	} {
		if _, err := st.SaveSessionMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	// Equal text in a later synthetic turn has a new saved identity. The feed
	// must not confuse presentation merging with durable operation replay.
	repeated := branchReplyEvent(t, st, run, "same answer")
	b := branchReplyEvent(t, st, run, "B")
	bAgain := branchReplyEvent(t, st, run, "B")
	appendReplyUpdateEvent(t, st, run, events.OperatorSteeringQueuedEvent, "operator", map[string]string{"content": "pending or cancelled input"})
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", "repeat that answer")); err != nil {
		t.Fatal(err)
	}
	afterUser := branchReplyEvent(t, st, run, "same answer")
	appendReplyUpdateEvent(t, st, run, events.ToolStartedEvent, "tool", map[string]string{"tool": "read_file", "private": "hidden output"})
	afterTool := branchReplyEvent(t, st, run, "same answer")
	long := strings.Repeat("完整答复。", 1500)
	longA := branchReplyEvent(t, st, run, long+" A")
	longB := branchReplyEvent(t, st, run, long+" B")
	commentary := appendReplyUpdateEvent(t, st, run, events.ModelPublicCommentaryEvent, "model", map[string]string{"text": "公开进度说明"})
	appendReplyUpdateEvent(t, st, run, events.ModelPublicCommentaryEvent, "thread_branch", map[string]string{"text": "copied historical answer", "source_event_id": longA})
	got, _ := collectReplyUpdates(t, st, cursor, 1)
	if !reflect.DeepEqual(replyUpdateIDs(got), []string{repeated, b, bAgain, afterUser, afterTool, longA, longB, commentary}) {
		t.Fatalf("public/duplicate boundary: %#v", replyUpdateIDs(got))
	}
}

func TestThreadReplyUpdatesLongInternalHistoryUsesOnlySourcePages(t *testing.T) {
	st, run := replyUpdateFixture(t)
	branchReplyEvent(t, st, run, "already displayed final")
	cursor := replyUpdateBaseline(t, st)
	for range 250 {
		appendReplyUpdateEvent(t, st, run, events.ModelStartedEvent, "model", map[string]string{"private": "internal"})
	}
	newReply := branchReplyEvent(t, st, run, "new final")
	items, next, more, err := st.ListThreadReplyUpdates(t.Context(), cursor, 100)
	if err != nil || len(items) != 0 || !more || next == cursor {
		t.Fatalf("bounded source page: %#v %v %v", items, more, err)
	}
	got, _ := collectReplyUpdates(t, st, next, 100)
	if !reflect.DeepEqual(replyUpdateIDs(got), []string{newReply}) {
		t.Fatalf("internal pages skipped or duplicated a reply: %#v", got)
	}
}

func TestThreadReplyUpdatesCrossRunArchiveDeleteAndRealBranch(t *testing.T) {
	st, run := replyUpdateFixture(t)
	selected := branchReplyEvent(t, st, run, "source answer")
	cursor := replyUpdateBaseline(t, st)
	branch, err := application.NewThreadBranchService(st).Branch(t.Context(), domain.ThreadBranchRequest{
		Version: domain.ThreadBranchProtocolVersion, ThreadID: domain.InitialThreadID(run.ID),
		SourceEventID: selected, OperationKey: "reply-update-branch", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if copied, _ := collectReplyUpdates(t, st, cursor, 100); len(copied) != 0 {
		t.Fatalf("branch copy was a reply: %#v", copied)
	}
	branchNew := branchReplyEvent(t, st, branch.Run, "new answer in branch")
	runs := application.NewRunService(st)
	if _, err := runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Fail(t.Context(), run.ID, "continue fixture"); err != nil {
		t.Fatal(err)
	}
	continued, err := application.NewThreadService(st).Submit(t.Context(), application.SubmitThreadMessageRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: "next run",
		OperationKey: "reply-updates-successor", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	successor := branchReplyEvent(t, st, continued.Run, "source answer")
	if _, err := runs.Start(t.Context(), continued.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Fail(t.Context(), continued.Run.ID, "archive fixture"); err != nil {
		t.Fatal(err)
	}
	thread, err := st.GetThread(t.Context(), continued.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := application.NewThreadService(st).Transition(t.Context(), thread.ID, domain.ThreadArchive, thread.Version, "test_operator", "reply-updates-archive")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := collectReplyUpdates(t, st, cursor, 100)
	if !reflect.DeepEqual(replyUpdateIDs(got), []string{branchNew, successor}) || got[1].RunOrdinal != 2 || got[1].ThreadStatus != domain.ThreadArchived {
		t.Fatalf("cross-Run/archive updates: %#v", got)
	}
	if _, err := application.NewThreadService(st).Transition(t.Context(), thread.ID, domain.ThreadDelete, archived.Version, "test_operator", "reply-updates-delete"); err != nil {
		t.Fatal(err)
	}
	got, _ = collectReplyUpdates(t, st, cursor, 100)
	if !reflect.DeepEqual(replyUpdateIDs(got), []string{branchNew}) {
		t.Fatalf("deleted reply exposed: %#v", got)
	}
}

func TestThreadReplyUpdatesErrorsNeverReturnPartialCursorAndRejectOtherStores(t *testing.T) {
	st, run := replyUpdateFixture(t)
	cursor := replyUpdateBaseline(t, st)
	first := branchReplyEvent(t, st, run, "good answer")
	second := branchReplyEvent(t, st, run, "answer with broken source")
	var created string
	if err := st.db.QueryRow(`SELECT created_at FROM run_events WHERE event_id=?`, second).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE run_events SET created_at='' WHERE event_id=?`, second); err != nil {
		t.Fatal(err)
	}
	items, next, more, err := st.ListThreadReplyUpdates(t.Context(), cursor, 100)
	if err == nil || items != nil || next != "" || more {
		t.Fatalf("partial success escaped: %#v %q %v %v", items, next, more, err)
	}
	if _, err := st.db.Exec(`UPDATE run_events SET created_at=? WHERE event_id=?`, created, second); err != nil {
		t.Fatal(err)
	}
	got, _ := collectReplyUpdates(t, st, cursor, 100)
	if !reflect.DeepEqual(replyUpdateIDs(got), []string{first, second}) {
		t.Fatal("retry lost the first reply")
	}
	other, _ := replyUpdateFixture(t)
	for _, bad := range []string{replyUpdateBaseline(t, other), "not-a-cursor", encodeThreadReplyCursor(threadReplyCursor{Version: 1, Store: st.DataStoreID(), After: 1 << 60})} {
		if _, _, _, err := st.ListThreadReplyUpdates(t.Context(), bad, 100); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
			t.Fatalf("invalid cursor accepted: %v", err)
		}
	}
}

func TestThreadReplyUpdatesMetadataChangesDoNotCreateReplies(t *testing.T) {
	st, run := replyUpdateFixture(t)
	branchReplyEvent(t, st, run, "existing reply")
	cursor := replyUpdateBaseline(t, st)
	threadID := domain.InitialThreadID(run.ID)
	if _, err := st.RenameThread(t.Context(), threadID, "renamed chat"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PinThread(t.Context(), threadID, true); err != nil {
		t.Fatal(err)
	}
	items, next, more, err := st.ListThreadReplyUpdates(t.Context(), cursor, 100)
	if err != nil || len(items) != 0 || more || next != cursor {
		t.Fatalf("metadata looked like a new reply: %#v %v", items, err)
	}
}
