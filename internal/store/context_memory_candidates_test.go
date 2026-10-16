package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/session"
)

func TestMemoryCandidateSQLiteRestartConfirmReplayAndDeleteDoNotResurrect(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "candidates.db")
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace, run, note := createMemoryCandidateSource(t, state, true)
	items := listMemoryCandidateTest(t, state, workspace.ID, false)
	if len(items) != 1 || items[0].Source.NoteID != note.ID || items[0].Source.RunID != run.ID || items[0].Source.CompletionKind != "completed_run" {
		t.Fatalf("queue=%#v", items)
	}
	candidate := items[0]
	assertMemoryCandidateFormalCount(t, state, workspace.ID, 0)
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	loaded, err := state.GetContextMemoryCandidate(ctx, candidate.ID, workspace.ID, time.Time{})
	if err != nil || loaded.Fingerprint != candidate.Fingerprint {
		t.Fatalf("restart lost candidate: %#v %v", loaded, err)
	}
	request := memoryCandidateReviewTest(candidate, "desktop_operator")
	first, err := state.ReviewContextMemoryCandidate(ctx, candidate.ID, request, contextmgr.MemoryCandidateConfirmed, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.MemoryID == "" || first.Candidate.Content != "" || first.Candidate.Title != "" || first.Candidate.ReviewedBy != "desktop_operator" {
		t.Fatalf("confirmation=%#v", first)
	}
	second, err := state.ReviewContextMemoryCandidate(ctx, candidate.ID, request, contextmgr.MemoryCandidateConfirmed, time.Time{})
	if err != nil || !second.Replayed || second.MemoryID != first.MemoryID {
		t.Fatalf("replay=%#v err=%v", second, err)
	}
	assertMemoryCandidateFormalCount(t, state, workspace.ID, 1)
	memory, err := state.GetContextMemory(ctx, first.MemoryID)
	if err != nil || memory.SourceRef != candidate.ID || memory.Content != note.Content || memory.CreatedBy != "desktop_operator" {
		t.Fatalf("memory=%#v err=%v", memory, err)
	}
	if deleted, err := state.DeleteContextMemory(ctx, memory.ID, memory.Version); err != nil || !deleted {
		t.Fatalf("delete=%t %v", deleted, err)
	}
	deletedReplay, err := state.ReviewContextMemoryCandidate(ctx, candidate.ID, request, contextmgr.MemoryCandidateConfirmed, time.Time{})
	if err != nil || !deletedReplay.Replayed || deletedReplay.Candidate.Content != "" {
		t.Fatalf("deleted replay=%#v %v", deletedReplay, err)
	}
	assertMemoryCandidateFormalCount(t, state, workspace.ID, 0)
	if values := listMemoryCandidateTest(t, state, workspace.ID, false); len(values) != 0 {
		t.Fatalf("deleted memory was re-proposed: %#v", values)
	}
	var payload string
	if err := state.db.QueryRowContext(ctx, `SELECT payload_json FROM context_memory_candidates WHERE id=?`, candidate.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, note.Content) || strings.Contains(payload, note.Title) {
		t.Fatalf("terminal candidate retained deleted text: %s", payload)
	}
	if _, err := state.GetContextMemory(ctx, first.MemoryID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted memory readable: %v", err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if values := listMemoryCandidateTest(t, state, workspace.ID, false); len(values) != 0 {
		t.Fatalf("restart resurrected a deleted memory: %#v", values)
	}
	assertMemoryCandidateFormalCount(t, state, workspace.ID, 0)
	if values := listMemoryCandidateTest(t, state, workspace.ID, true); len(values) != 1 || values[0].Content != "" || values[0].MemoryID != first.MemoryID {
		t.Fatalf("restart lost the content-free confirmation audit: %#v", values)
	}
}

func TestMemoryCandidateSQLiteV198MigrationDerivesExistingConclusions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v197-candidates.db")
	state := openHistoricalTestDatabase(t, path, 197)
	workspace, run, note := createMemoryCandidateSource(t, state, true)
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	values := listMemoryCandidateTest(t, upgraded, workspace.ID, false)
	if len(values) != 1 || values[0].Source.RunID != run.ID || values[0].Source.NoteID != note.ID || values[0].Content != note.Content {
		t.Fatalf("migration changed existing source: %#v", values)
	}
	assertMemoryCandidateFormalCount(t, upgraded, workspace.ID, 0)
}

func TestMemoryCandidateSQLiteReviewRejectsCrossWorkspaceAndChangedReview(t *testing.T) {
	state := openWorkItemTestStore(t)
	ctx := t.Context()
	workspace, _, _ := createMemoryCandidateSource(t, state, true)
	candidate := listMemoryCandidateTest(t, state, workspace.ID, false)[0]
	other := WorkspaceRecord{ID: idgen.New("workspace"), Name: idgen.New("workspace-name"), RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(ctx, other); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name string
		edit func(*contextmgr.ReviewMemoryCandidateRequest)
		code apperror.Code
	}{
		{"workspace", func(r *contextmgr.ReviewMemoryCandidateRequest) { r.WorkspaceID = other.ID }, apperror.CodeNotFound},
		{"fingerprint", func(r *contextmgr.ReviewMemoryCandidateRequest) { r.ExpectedFingerprint = strings.Repeat("a", 64) }, apperror.CodeConflict},
		{"version", func(r *contextmgr.ReviewMemoryCandidateRequest) { r.ExpectedVersion++ }, apperror.CodeConflict},
		{"source version", func(r *contextmgr.ReviewMemoryCandidateRequest) { r.ExpectedSourceVersion++ }, apperror.CodeConflict},
		{"explicit confirmation", func(r *contextmgr.ReviewMemoryCandidateRequest) { r.Confirm = false }, apperror.CodeInvalidArgument},
		{"model identity", func(r *contextmgr.ReviewMemoryCandidateRequest) { r.RequestedBy = "model" }, apperror.CodeInvalidArgument},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			request := memoryCandidateReviewTest(candidate, "desktop_operator")
			mutation.edit(&request)
			_, err := state.ReviewContextMemoryCandidate(ctx, candidate.ID, request, contextmgr.MemoryCandidateConfirmed, time.Time{})
			if apperror.CodeOf(apperror.Normalize(err)) != mutation.code {
				t.Fatalf("err=%v code=%s", err, apperror.CodeOf(apperror.Normalize(err)))
			}
		})
	}
	assertMemoryCandidateFormalCount(t, state, workspace.ID, 0)
	request := memoryCandidateReviewTest(candidate, "desktop_operator")
	rejected, err := state.ReviewContextMemoryCandidate(ctx, candidate.ID, request, contextmgr.MemoryCandidateRejected, time.Time{})
	if err != nil || rejected.Candidate.Status != contextmgr.MemoryCandidateRejected || rejected.Candidate.Content != "" {
		t.Fatalf("rejection=%#v %v", rejected, err)
	}
	if values := listMemoryCandidateTest(t, state, workspace.ID, false); len(values) != 0 {
		t.Fatalf("rejected candidate reappeared: %#v", values)
	}
	if _, err := state.ReviewContextMemoryCandidate(ctx, candidate.ID, request, contextmgr.MemoryCandidateConfirmed, time.Time{}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("rejected candidate confirmed: %v", err)
	}
}

func TestMemoryCandidateSQLiteConcurrentReviewIsBoundToOneActor(t *testing.T) {
	state := openWorkItemTestStore(t)
	workspace, _, _ := createMemoryCandidateSource(t, state, true)
	candidate := listMemoryCandidateTest(t, state, workspace.ID, false)[0]
	requests := []contextmgr.ReviewMemoryCandidateRequest{memoryCandidateReviewTest(candidate, "operator_one"), memoryCandidateReviewTest(candidate, "operator_two")}
	results := make([]contextmgr.MemoryCandidateReviewResult, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for i := range requests {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			results[i], errs[i] = state.ReviewContextMemoryCandidate(t.Context(), candidate.ID, requests[i], contextmgr.MemoryCandidateConfirmed, time.Time{})
		}(i)
	}
	wait.Wait()
	successes, conflicts := 0, 0
	for i := range errs {
		if errs[i] == nil {
			successes++
		} else if apperror.CodeOf(errs[i]) == apperror.CodeConflict {
			conflicts++
		} else {
			t.Fatal(errs[i])
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("reviews=%#v errors=%v", results, errs)
	}
	assertMemoryCandidateFormalCount(t, state, workspace.ID, 1)
}

func TestMemoryCandidateSQLiteThreadReplySourcesInvalidateOnRealChanges(t *testing.T) {
	for _, change := range []string{"content", "archive", "workspace", "failed run"} {
		t.Run(change, func(t *testing.T) {
			state := openWorkItemTestStore(t)
			ctx := t.Context()
			workspace, run, note := createMemoryCandidateSource(t, state, false)
			if values := listMemoryCandidateTest(t, state, workspace.ID, false); len(values) != 0 {
				t.Fatal("a merely paused Run produced candidates")
			}
			_ = recordMemoryCandidateReply(t, state, run, note, false)
			values := listMemoryCandidateTest(t, state, workspace.ID, false)
			if len(values) != 1 || values[0].Source.CompletionKind != "thread_reply" {
				t.Fatalf("reply did not produce candidate: %#v", values)
			}
			candidate := values[0]
			switch change {
			case "content":
				note.Content = "Updated durable conclusion"
				note.Version++
				note.UpdatedAt = time.Now().UTC()
				if err := state.UpdateNote(ctx, note, 1, newNoteChangedEvent(t, run.MissionID, note, domain.NoteActive)); err != nil {
					t.Fatal(err)
				}
			case "archive":
				if err := note.Transition(domain.NoteArchived, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				note.Version++
				if err := state.UpdateNote(ctx, note, 1, newNoteChangedEvent(t, run.MissionID, note, domain.NoteActive)); err != nil {
					t.Fatal(err)
				}
			case "workspace":
				other := WorkspaceRecord{ID: idgen.New("workspace"), Name: idgen.New("workspace-name"), RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
				if err := state.SaveWorkspace(ctx, other); err != nil {
					t.Fatal(err)
				}
				if _, err := state.db.ExecContext(ctx, `UPDATE missions SET workspace_id=?,scope_json=json_set(scope_json,'$.workspace_id',?) WHERE id=?`, other.ID, other.ID, run.MissionID); err != nil {
					t.Fatal(err)
				}
			case "failed run":
				if _, err := application.NewRunService(state).Fail(ctx, run.ID, "source execution failed"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := state.ReviewContextMemoryCandidate(ctx, candidate.ID, memoryCandidateReviewTest(candidate, "desktop_operator"), contextmgr.MemoryCandidateConfirmed, time.Time{})
			if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("changed source confirmed: %v", err)
			}
			stale, err := state.GetContextMemoryCandidate(ctx, candidate.ID, workspace.ID, time.Time{})
			if err != nil || stale.Status != contextmgr.MemoryCandidateStale || stale.Content != "" {
				t.Fatalf("stale=%#v %v", stale, err)
			}
			assertMemoryCandidateFormalCount(t, state, workspace.ID, 0)
		})
	}
}

func TestMemoryCandidateSQLiteQueueBoundsAndExcludesSensitivePrivateAndToolBoundaries(t *testing.T) {
	state := openWorkItemTestStore(t)
	ctx := t.Context()
	workspace, run, note := createMemoryCandidateSource(t, state, false)
	_ = recordMemoryCandidateReply(t, state, run, note, true)
	if values := listMemoryCandidateTest(t, state, workspace.ID, false); len(values) != 0 {
		t.Fatalf("tool boundary produced experience: %#v", values)
	}
	_ = recordMemoryCandidateReply(t, state, run, note, false)
	for i := 0; i < contextmgr.MaxMemoryCandidates+3; i++ {
		extra := newNoteTest(run.ID, "Bounded conclusion", "A recorded conclusion.")
		extra.Category = domain.NoteDecision
		extra.SourceRefs = []string{"README.md"}
		if i == 0 {
			extra.SourceRefs = []string{".env"}
		}
		if i == 1 {
			extra.Visibility = domain.NoteVisibilityOwner
			extra.Owner = "root"
		}
		if err := state.CreateNote(ctx, extra, newNoteCreatedEvent(t, run.MissionID, extra)); err != nil {
			t.Fatal(err)
		}
	}
	// A later completed reply binds the Notes created in the loop.
	_ = recordMemoryCandidateReply(t, state, run, note, false)
	values := listMemoryCandidateTest(t, state, workspace.ID, false)
	if len(values) != contextmgr.MaxMemoryCandidates {
		t.Fatalf("queue is not bounded: %d", len(values))
	}
	for _, value := range values {
		for _, ref := range value.Source.References {
			if ref == ".env" {
				t.Fatal("sensitive source survived")
			}
		}
	}
	assertMemoryCandidateFormalCount(t, state, workspace.ID, 0)
	var count int
	if err := state.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM context_memory_candidates WHERE workspace_id=? AND status='pending'`, workspace.ID).Scan(&count); err != nil || count != contextmgr.MaxMemoryCandidates {
		t.Fatalf("pending count=%d err=%v", count, err)
	}
}

func TestMemoryCandidateSQLiteDeliveryCheckpointWorksWithoutCompletedRunAndRejectsStaleItem(t *testing.T) {
	state := openWorkItemTestStore(t)
	workspace := WorkspaceRecord{ID: idgen.New("workspace"), Name: idgen.New("workspace-name"), RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	_, ctx, run, selection := populateStoreDeliveryGateFixtureInWorkspace(t, state, "candidate-delivery", workspace.ID)
	work := application.NewWorkItemService(state)
	item, err := work.Transition(ctx, selection.WorkItems[0].ID, 0, domain.WorkItemInProgress, "")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := recordStoreDeliveryCheckpoint(t, ctx, state, item.ID, "candidate-delivery-checkpoint-0001", false)
	currentRun, err := state.GetRun(ctx, run.ID)
	if err != nil || currentRun.Status != domain.RunPaused {
		t.Fatalf("fixture did not retain a paused Run: %#v %v", currentRun, err)
	}
	candidates := listMemoryCandidateTest(t, state, workspace.ID, false)
	var candidate contextmgr.MemoryCandidate
	for _, value := range candidates {
		if value.Source.NoteID == checkpoint.HandoffNoteID {
			candidate = value
		}
	}
	if candidate.ID == "" || candidate.Source.CompletionKind != "delivery_checkpoint" {
		t.Fatalf("paused delivery handoff was not proposed: %#v", candidates)
	}
	if _, err := work.Transition(ctx, item.ID, item.Version, domain.WorkItemBlocked, "acceptance needs revision"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReviewContextMemoryCandidate(ctx, candidate.ID, memoryCandidateReviewTest(candidate, "desktop_operator"), contextmgr.MemoryCandidateConfirmed, time.Time{}); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("stale delivery item was confirmed: %v", err)
	}
	assertMemoryCandidateFormalCount(t, state, workspace.ID, 0)
}

func createMemoryCandidateSource(t *testing.T, state *SQLiteStore, complete bool) (WorkspaceRecord, domain.Run, domain.Note) {
	t.Helper()
	ctx := t.Context()
	workspace := WorkspaceRecord{ID: idgen.New("workspace"), Name: idgen.New("workspace-name"), RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	runs := newMigrationFixtureRunService(t, state)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "retain a source-linked conclusion", Profile: "code", WorkspaceID: workspace.ID, Budget: domain.DefaultBudget()})
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	note := newNoteTest(run.ID, "Persisted parser decision", "Keep strict JSON decoding and run package tests.")
	note.Category = domain.NoteDecision
	note.SourceRefs = []string{"README.md"}
	if err := state.CreateNote(ctx, note, newNoteCreatedEvent(t, run.MissionID, note)); err != nil {
		t.Fatal(err)
	}
	if complete {
		run, err = runs.Complete(ctx, run.ID)
	} else {
		run, err = runs.Pause(ctx, run.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	return workspace, run, note
}

func recordMemoryCandidateReply(t *testing.T, state *SQLiteStore, run domain.Run, note domain.Note, toolBoundary bool) events.Event {
	t.Helper()
	ctx := t.Context()
	message := session.NewMessage(run.SessionID, "assistant", "The recorded conclusion is ready for review.")
	stored, err := state.SaveSessionMessage(ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	event, err := events.New(run.ID, run.MissionID, events.AgentTurnCompletedEvent, "run_supervisor", idgen.New("attempt"), map[string]any{"requested_lifecycle_action": "finish", "lifecycle_action": "continue", "assistant_message_id": stored.ID, "tool_round_boundary": toolBoundary})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	event, err = insertRunEventTx(ctx, tx, event)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return event
}

func memoryCandidateReviewTest(value contextmgr.MemoryCandidate, actor string) contextmgr.ReviewMemoryCandidateRequest {
	return contextmgr.ReviewMemoryCandidateRequest{WorkspaceID: value.WorkspaceID, ExpectedVersion: value.Version, ExpectedFingerprint: value.Fingerprint, ExpectedSourceVersion: value.Source.NoteVersion, RequestedBy: actor, Confirm: true}
}

func listMemoryCandidateTest(t *testing.T, state *SQLiteStore, workspaceID string, includeResolved bool) []contextmgr.MemoryCandidate {
	t.Helper()
	values, err := state.ListContextMemoryCandidates(t.Context(), contextmgr.MemoryCandidateFilter{WorkspaceID: workspaceID, IncludeResolved: includeResolved}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func assertMemoryCandidateFormalCount(t *testing.T, state *SQLiteStore, workspaceID string, want int) {
	t.Helper()
	values, err := state.ListContextMemories(context.Background(), contextmgr.MemoryFilter{Scope: contextmgr.MemoryScopeProject, ScopeID: workspaceID, IncludeDisabled: true, IncludeExpired: true}, time.Time{})
	if err != nil || len(values) != want {
		t.Fatalf("formal memory count=%d want=%d err=%v", len(values), want, err)
	}
}
