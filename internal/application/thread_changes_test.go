package application_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func applyThreadChange(t *testing.T, st *store.SQLiteStore, run domain.Run, root, path, operation, destination, text string) fileedit.Edit {
	t.Helper()
	mission, err := st.GetMission(t.Context(), run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	proposal := fileedit.Proposal{SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID,
		WorkspaceRoot: root, Path: path, Operation: operation, DestinationPath: destination, ProposedText: text}
	if operation == fileedit.OperationCreate {
		proposal.ExpectedOriginalHash = "missing"
	}
	if operation == fileedit.OperationMove {
		proposal.ExpectedDestinationHash = "missing"
	}
	manager := fileedit.NewManager(st)
	edit, err := manager.Propose(t.Context(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	edit, err = manager.Approve(t.Context(), edit.ID, root)
	if err != nil {
		t.Fatal(err)
	}
	return edit
}

func saveThreadChangeReply(t *testing.T, st *store.SQLiteStore, run domain.Run, text string) events.Event {
	t.Helper()
	message, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "assistant", text))
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range list {
		if event.Type == events.SessionMessageEvent && event.SubjectID == fmt.Sprint(message.ID) {
			return event
		}
	}
	t.Fatal("reply event was not projected")
	return events.Event{}
}

func TestThreadChangesUseReplyIntervalsAndNetAppliedContent(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first := applyThreadChange(t, st, run, root, "app.txt", "replace", "", "intermediate\n")
	last := applyThreadChange(t, st, run, root, "app.txt", "replace", "", "after\n")
	applyThreadChange(t, st, run, root, "temporary.txt", "create", "", "temporary\n")
	applyThreadChange(t, st, run, root, "temporary.txt", "delete", "", "")
	_, err := fileedit.NewManager(st).Propose(t.Context(), fileedit.Proposal{SessionID: run.SessionID,
		WorkspaceID: "review-workspace", WorkspaceRoot: root, Path: "pending.txt", ProposedText: "unapplied\n"})
	if err != nil {
		t.Fatal(err)
	}
	// Real operator input is committed after the tools; it must not discard them.
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", "make the change")); err != nil {
		t.Fatal(err)
	}
	reply := saveThreadChangeReply(t, st, run, "first answer")
	service := application.NewThreadChangeService(st)
	result, err := service.Summary(t.Context(), domain.InitialThreadID(run.ID), reply.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Partial || !result.CountsAvailable || result.FileCount != 1 || result.Additions != 1 || result.Deletions != 1 ||
		!reflect.DeepEqual(result.Files[0].EditIDs, []string{first.ID, last.ID}) ||
		strings.Contains(result.Files[0].Diff, "intermediate") || !strings.Contains(result.Files[0].Diff, "-before\n+after\n") {
		t.Fatalf("wrong applied net changes: %+v", result)
	}
	applyThreadChange(t, st, run, root, "app.txt", "replace", "", "third\n")
	secondReply := saveThreadChangeReply(t, st, run, "second answer")
	second, err := service.Summary(t.Context(), domain.InitialThreadID(run.ID), secondReply.EventID)
	if err != nil || second.FileCount != 1 || second.Additions != 1 || second.Deletions != 1 ||
		!strings.Contains(second.Files[0].Diff, "-after\n+third\n") {
		t.Fatalf("same-Run turns mixed: %+v %v", second, err)
	}
	// Current workspace content has no bearing on a completed reply's history.
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("external later change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := st.ExportThread(t.Context(), domain.InitialThreadID(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.Summary(t.Context(), domain.InitialThreadID(run.ID), reply.EventID)
	if err != nil || !reflect.DeepEqual(again, result) {
		t.Fatalf("historical summary drifted with current state: %+v %v", again, err)
	}
	after, err := st.ExportThread(t.Context(), domain.InitialThreadID(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	before.ExportedAt, after.ExportedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reading changes mutated Thread history")
	}
}

func TestThreadChangesHandleMovesDeletesRedactionAndExternalGaps(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	for path, text := range map[string]string{"old.txt": "old\n", "deleted.txt": "deleted\n", "gap.txt": "base\n",
		"secret.txt": "token=sk-" + strings.Repeat("q", 35) + "\n"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	applyThreadChange(t, st, run, root, "old.txt", "move", "new.txt", "")
	applyThreadChange(t, st, run, root, "new.txt", "replace", "", "new\n")
	applyThreadChange(t, st, run, root, "deleted.txt", "delete", "", "")
	applyThreadChange(t, st, run, root, "secret.txt", "replace", "", "safe\n")
	applyThreadChange(t, st, run, root, "gap.txt", "replace", "", "agent version\n")
	if err := os.WriteFile(filepath.Join(root, "gap.txt"), []byte("external private content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	applyThreadChange(t, st, run, root, "gap.txt", "replace", "", "final version\n")
	reply := saveThreadChangeReply(t, st, run, "done")
	result, err := application.NewThreadChangeService(st).Summary(t.Context(), domain.InitialThreadID(run.ID), reply.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial || result.CountsAvailable || result.FileCount != 4 || result.Additions != 1 || result.Deletions != 2 {
		t.Fatalf("wrong mixed summary: %+v", result)
	}
	files := map[string]application.ThreadChangeFile{}
	for _, file := range result.Files {
		files[file.Path] = file
	}
	if files["old.txt"].Operation != "move" || files["old.txt"].DestinationPath != "new.txt" ||
		files["old.txt"].Additions != 1 || files["old.txt"].Deletions != 1 || files["deleted.txt"].Operation != "delete" {
		t.Fatalf("move/delete not preserved: %+v", files)
	}
	for path, reason := range map[string]string{"secret.txt": "secrets_redacted", "gap.txt": "non_contiguous_edits"} {
		file := files[path]
		if file.CountsAvailable || file.Diff != "" || file.Reason != reason || file.Additions != 0 || file.Deletions != 0 {
			t.Fatalf("unsafe content was attributed: %+v", file)
		}
	}
}

func TestThreadChangesKeepRunHistorySeparate(t *testing.T) {
	st, first, registry := newThreadModelSettingsFixture(t)
	workspace, err := st.GetWorkspaceInfo(t.Context(), "workspace-model-settings")
	if err != nil {
		t.Fatal(err)
	}
	applyThreadChange(t, st, first, workspace.RootPath, "run.txt", "create", "", "first\n")
	firstReply := saveThreadChangeReply(t, st, first, "first reply")
	second := executeThreadModelSettingsSwitch(t, st, first, registry).Submission.Run
	applyThreadChange(t, st, second, workspace.RootPath, "run.txt", "replace", "", "second\n")
	secondReply := saveThreadChangeReply(t, st, second, "second reply")
	service := application.NewThreadChangeService(st)
	threadID := domain.InitialThreadID(first.ID)
	old, err := service.Summary(t.Context(), threadID, firstReply.EventID)
	if err != nil || old.RunID != first.ID || old.FileCount != 1 || old.Additions != 1 || old.Deletions != 0 {
		t.Fatalf("old Run was lost: %+v %v", old, err)
	}
	current, err := service.Summary(t.Context(), threadID, secondReply.EventID)
	if err != nil || current.RunID != second.ID || current.FileCount != 1 || current.Additions != 1 || current.Deletions != 1 {
		t.Fatalf("Run histories mixed: %+v %v", current, err)
	}
}

func TestThreadChangesBoundDiffBytesWithoutChangingCounts(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	for _, path := range []string{"one.txt", "two.txt"} {
		applyThreadChange(t, st, run, root, path, "create", "", strings.Repeat("line\n", 50000))
	}
	reply := saveThreadChangeReply(t, st, run, "done")
	result, err := application.NewThreadChangeService(st).Summary(t.Context(), domain.InitialThreadID(run.ID), reply.EventID)
	if err != nil || !result.Partial || !result.CountsAvailable || result.FileCount != 2 || result.Additions != 100000 ||
		len(result.Files[0].Diff)+len(result.Files[1].Diff) > application.MaxThreadChangeDiffBytes ||
		result.Files[0].DiffTruncated || !result.Files[1].DiffTruncated || result.Files[1].Diff != "" {
		t.Fatalf("wrong bounded diff result: files=%d additions=%d partial=%v counts=%v err=%v", result.FileCount, result.Additions, result.Partial, result.CountsAvailable, err)
	}
}

func TestThreadChangesCountSeparatedEditsWithoutUnchangedLines(t *testing.T) {
	root := t.TempDir()
	st, run := threadReviewFixture(t, root)
	if err := os.WriteFile(filepath.Join(root, "separate.txt"), []byte("old first\nkeep middle\nold last\n"), 0600); err != nil {
		t.Fatal(err)
	}
	applyThreadChange(t, st, run, root, "separate.txt", "replace", "", "new first\nkeep middle\nnew last\n")
	reply := saveThreadChangeReply(t, st, run, "done")
	result, err := application.NewThreadChangeService(st).Summary(t.Context(), domain.InitialThreadID(run.ID), reply.EventID)
	if err != nil || result.Additions != 2 || result.Deletions != 2 || result.Partial ||
		!strings.Contains(result.Files[0].Diff, " keep middle\n") {
		t.Fatalf("unchanged middle lines were counted as edited: %+v %v", result, err)
	}
}
