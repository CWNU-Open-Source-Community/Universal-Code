package application_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/workspace"
)

func projectMoveTarget(t *testing.T, st *store.SQLiteStore) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Target project guidance: use TARGET_RULE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "move-target", Name: "move-target", RootPath: root, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return root
}
func moveProject(t *testing.T, st *store.SQLiteStore, run domain.Run, key string) (domain.ThreadProjectMoveResult, error) {
	t.Helper()
	thread, err := st.GetThreadByRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return application.NewThreadProjectMoveService(st, nil).Move(t.Context(), domain.ThreadProjectMoveRequest{Version: domain.ThreadProjectMoveVersion, ThreadID: thread.ID, ProjectWorkspaceID: "move-target", ExpectedVersion: thread.ProjectAssignmentVersion, OperationKey: key, RequestedBy: "test_operator"})
}
func TestThreadProjectMoveCreatedRunNextMessageAndReplay(t *testing.T) {
	st, old := threadReviewFixture(t, t.TempDir())
	target := projectMoveTarget(t, st)
	ctx := t.Context()
	original, err := st.GetRun(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewThreadExecutionPermissionService(st, domain.ExecutionPermissionRuntimeCapabilities{}).Change(ctx, application.ChangeThreadExecutionPermissionRequest{ThreadID: domain.InitialThreadID(old.ID), Mode: string(domain.RunExecutionPermissionAuto), OperationKey: "move-initial-auto-permission", RequestedBy: "test_operator"}); err != nil {
		t.Fatal(err)
	}
	moved, err := moveProject(t, st, old, "move-created-operation-0001")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Thread.ID != domain.InitialThreadID(old.ID) || moved.Thread.WorkspaceID != "move-target" || moved.Thread.ProjectWorkspaceID != "move-target" || moved.Thread.ProjectAssignmentVersion != 2 {
		t.Fatalf("move=%+v", moved)
	}
	fresh, err := st.GetRun(ctx, moved.Thread.LastRunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != domain.RunCreated || fresh.MissionID == old.MissionID || fresh.SessionID == old.SessionID {
		t.Fatalf("new Run=%+v", fresh)
	}
	after, _ := st.GetRun(ctx, old.ID)
	if after.MissionID != original.MissionID || after.SessionID != original.SessionID {
		t.Fatal("old graph was rebound")
	}
	mission, _ := st.GetMission(ctx, fresh.MissionID)
	resolved, err := application.ResolveRunFileWorkspace(ctx, st, fresh, mission, nil)
	if err != nil || resolved.Workspace.RootPath != target {
		t.Fatalf("execution directory=%+v %v", resolved, err)
	}
	permission, _ := st.GetRunExecutionPermission(ctx, fresh.ID)
	if permission.Mode != domain.RunExecutionPermissionAsk {
		t.Fatalf("permission=%+v", permission)
	}
	preference, err := st.GetThreadExecutionPermission(ctx, moved.Thread.ID)
	if err != nil || preference.Mode != domain.RunExecutionPermissionAsk || preference.MissionID != fresh.MissionID {
		t.Fatalf("project execution preference=%+v %v", preference, err)
	}
	req := domain.ThreadProjectMoveRequest{Version: domain.ThreadProjectMoveVersion, ThreadID: moved.Thread.ID, ProjectWorkspaceID: "move-target", ExpectedVersion: 1, OperationKey: "move-created-operation-0001", RequestedBy: "test_operator"}
	replay, err := application.NewThreadProjectMoveService(st, nil).Move(ctx, req)
	if err != nil || !replay.Replayed || replay.Thread.LastRunID != fresh.ID {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	req.OperationKey = "stale-operation-0001"
	if _, err = application.NewThreadProjectMoveService(st, nil).Move(ctx, req); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("stale=%v", err)
	}
	req.OperationKey = "detach-operation-0001"
	req.ExpectedVersion = 2
	req.ProjectWorkspaceID = ""
	detached, err := application.NewThreadProjectMoveService(st, nil).Move(ctx, req)
	if err != nil || detached.Thread.ProjectWorkspaceID != "" || detached.Thread.WorkspaceID != "move-target" || detached.Thread.LastRunID != fresh.ID {
		t.Fatalf("detach=%+v %v", detached, err)
	}
	sent, err := application.NewThreadService(st).Submit(ctx, application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: moved.Thread.ID, Content: "next message", OperationKey: "next-message-operation-0001", RequestedBy: "test_operator"})
	if err != nil || sent.Run.ID != fresh.ID {
		t.Fatalf("next=%+v %v", sent, err)
	}
}
func TestThreadProjectMoveRejectsPendingAndConcurrentIntent(t *testing.T) {
	st, old := threadReviewFixture(t, t.TempDir())
	projectMoveTarget(t, st)
	threadID := domain.InitialThreadID(old.ID)
	_, err := application.NewThreadService(st).Submit(t.Context(), application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: threadID, Content: "pending original input", OperationKey: "queued-operation-0001", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = moveProject(t, st, old, "move-pending-operation-0001"); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeFailedPrecondition {
		t.Fatalf("pending move=%v", err)
	}
	current, _ := st.GetThread(t.Context(), threadID)
	if current.WorkspaceID == "move-target" {
		t.Fatal("pending chat moved")
	}
}
func TestThreadProjectMoveConcurrentCAS(t *testing.T) {
	st, old := threadReviewFixture(t, t.TempDir())
	projectMoveTarget(t, st)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"race-a-operation-0001", "race-b-operation-0001"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := application.NewThreadProjectMoveService(st, nil).Move(t.Context(), domain.ThreadProjectMoveRequest{Version: domain.ThreadProjectMoveVersion, ThreadID: domain.InitialThreadID(old.ID), ProjectWorkspaceID: "move-target", ExpectedVersion: 1, OperationKey: key, RequestedBy: "test_operator"})
			results <- err
		}(key)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeConflict {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}
func TestThreadProjectMoveHistoryBranchAndUndoStayAtSource(t *testing.T) {
	source := t.TempDir()
	st, old := threadReviewFixture(t, source)
	target := projectMoveTarget(t, st)
	if err := os.WriteFile(filepath.Join(source, "app.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	applyThreadChange(t, st, old, source, "app.txt", "replace", "", "changed")
	reply := saveThreadChangeReply(t, st, old, "SOURCE_HISTORY_CANARY")
	if err := os.WriteFile(filepath.Join(target, "app.txt"), []byte("target remains"), 0600); err != nil {
		t.Fatal(err)
	}
	moved, err := moveProject(t, st, old, "move-history-operation-0001")
	if err != nil {
		t.Fatal(err)
	}
	page, err := st.SearchThreadHistory(t.Context(), moved.Thread.LastRunID, domain.HistorySearchRequest{Query: "SOURCE_HISTORY_CANARY"})
	if err != nil || len(page.Records) == 0 {
		t.Fatalf("history=%+v %v", page, err)
	}
	matches, err := st.SearchThreadMessages(t.Context(), "SOURCE_HISTORY_CANARY", "", false, time.Time{}, "", 20)
	if err != nil || len(matches) != 1 || matches[0].ThreadID != moved.Thread.ID {
		t.Fatalf("saved search=%+v %v", matches, err)
	}
	located, err := st.GetThreadSearchMessage(t.Context(), moved.Thread.ID, reply.EventID)
	if err != nil || located.MessageID != reply.EventID {
		t.Fatalf("saved message location=%+v %v", located, err)
	}
	branch, err := application.NewThreadBranchService(st).Branch(t.Context(), domain.ThreadBranchRequest{Version: domain.ThreadBranchProtocolVersion, ThreadID: moved.Thread.ID, SourceEventID: reply.EventID, OperationKey: "branch-old-operation-0001", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if branch.Thread.WorkspaceID != "review-workspace" {
		t.Fatal("old reply branch used destination workspace")
	}
	undo := undoThreadReply(t, st, old, reply.EventID, true)
	if undo.State != "completed" {
		t.Fatalf("undo=%+v", undo)
	}
	if b, _ := os.ReadFile(filepath.Join(source, "app.txt")); string(b) != "original" {
		t.Fatalf("source=%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "app.txt")); string(b) != "target remains" {
		t.Fatalf("target=%s", b)
	}
}

func TestThreadProjectMoveUploadedAttachmentKeepsOriginalSource(t *testing.T) {
	st, old, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	projectMoveTarget(t, st)
	original, err := st.SaveWorkspaceFileAttachment(t.Context(), "ws-tool-boundary", "project-move-upload-original", "text/plain", "note.txt", []byte("MOVED_ATTACHMENT_CANARY"))
	if err != nil {
		t.Fatal(err)
	}
	input.Content = "Read this attachment"
	input.Files = nil
	input.Attachments = []domain.FileAttachmentReference{{ID: original.ID, WorkspaceID: original.WorkspaceID, SHA256: original.SHA256, ByteSize: original.ByteSize}}
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 1 {
			return textResponse(rootActionResponse(domain.RootActionFinish, "Read original attached note", "done", "")), nil
		}
		current, err := st.GetThread(ctx, input.ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint, found, err := st.GetSupervisorCheckpoint(ctx, current.LastRunID)
		if err != nil || !found {
			t.Fatalf("target checkpoint: %+v %v", checkpoint, err)
		}
		inventory, err := st.ListSupervisorFileAttachmentInputs(ctx, checkpoint)
		if err != nil || inventory.WorkspaceID != "move-target" || len(inventory.Files) != 1 || inventory.Files[0].WorkspaceID != original.WorkspaceID {
			t.Fatalf("native attachment input source=%+v %v", inventory, err)
		}
		var text strings.Builder
		for _, m := range request.Messages {
			text.WriteString(m.Content)
		}
		if !strings.Contains(text.String(), "MOVED_ATTACHMENT_CANARY") {
			t.Error("continuing chat lost original uploaded file evidence")
		}
		return textResponse(rootActionResponse(domain.RootActionFinish, "Continued after project move", "done", "")), nil
	}
	turns := toolBoundaryServiceWithPolicy(st, st, provider, &fileOperationPolicy{})
	first, err := turns.Execute(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := moveProject(t, st, old, "move-attachment-operation")
	if err != nil {
		t.Fatal(err)
	}
	files, err := st.ListOperatorMessageAttachments(t.Context(), old.ID, first.Submission.Message.ID)
	if err != nil || len(files) != 1 || files[0].WorkspaceID != original.WorkspaceID {
		t.Fatalf("historical attachments=%+v %v", files, err)
	}
	observed, err := st.InspectThreadTurnRequest(t.Context(), input.ThreadID, input.OperationKey, input.RequestedBy)
	if err != nil || observed.WorkspaceID != original.WorkspaceID {
		t.Fatalf("old input observation=%+v %v", observed, err)
	}
	replay, err := turns.Execute(t.Context(), input)
	if err != nil || !replay.Submission.Replayed || replay.Submission.Run.ID != old.ID {
		t.Fatalf("original upload replay=%+v %v", replay, err)
	}
	next := input
	next.Content = "Continue in the new project"
	next.Attachments = nil
	next.OperationKey = "project-move-next-turn-key"
	result, err := turns.Execute(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	if result.Submission.Run.ID != moved.Thread.LastRunID {
		t.Fatal("next turn did not use target Run")
	}
	_, foreign, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "foreign thread", WorkspaceID: "move-target", Profile: "review", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	saveThreadChangeReply(t, st, foreign, "FOREIGN_HISTORY_CANARY")
	page, err := st.SearchThreadHistory(t.Context(), moved.Thread.LastRunID, domain.HistorySearchRequest{Query: "FOREIGN_HISTORY_CANARY"})
	if err != nil || len(page.Records) != 0 {
		t.Fatalf("foreign chat leaked into history: %+v %v", page, err)
	}
	exported, err := st.ExportThread(t.Context(), moved.Thread.ID)
	if err != nil || len(exported.Missions) != 2 {
		t.Fatalf("export lost original Mission: %d %v", len(exported.Missions), err)
	}
}

func TestThreadProjectMoveRejectsRunningAndWaitingApproval(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(map[bool]string{false: "active execution", true: "waiting approval"}[waiting], func(t *testing.T) {
			st, run := threadReviewFixture(t, t.TempDir())
			projectMoveTarget(t, st)
			mission, err := st.GetMission(t.Context(), run.MissionID)
			if err != nil {
				t.Fatal(err)
			}
			run, err = application.NewRunService(st).Start(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if waiting {
				_, err = transitionRunForThreadTest(t.Context(), st, mission, run, domain.RunWaitingApproval)
			} else {
				_, err = st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "project-move-busy-owner", TTL: time.Minute})
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = moveProject(t, st, run, "move-busy-operation-0001"); err == nil {
				t.Fatal("busy chat moved")
			}
			thread, err := st.GetThreadByRun(t.Context(), run.ID)
			if err != nil || thread.WorkspaceID != "review-workspace" || thread.ProjectAssignmentVersion != 1 {
				t.Fatalf("busy move changed binding: %+v %v", thread, err)
			}
		})
	}
}

func TestThreadProjectMoveRepeatedCreatedRunAndPrivateTarget(t *testing.T) {
	st, old := threadReviewFixture(t, t.TempDir())
	target := projectMoveTarget(t, st)
	registry, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := application.NewSkillSelectionService(st, registry).Select(t.Context(), application.SelectSkillsRequest{RunID: old.ID, Names: []string{"code"}, TokenBudget: 4096, OperationKey: "project-move-old-skill-key", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := moveProject(t, st, old, "first-move-operation-0001")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := st.GetRun(t.Context(), moved.Thread.LastRunID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prepared.Config.ProjectInstructions), "TARGET_RULE") {
		t.Fatal("target guidance not loaded")
	}
	if _, found, err := st.GetSkillSelectionByRun(t.Context(), prepared.ID); err != nil || found {
		t.Fatalf("old skill selection inherited: %v %v", found, err)
	}
	original, found, err := st.GetSkillSelectionByRun(t.Context(), old.ID)
	if err != nil || !found || original.ID != selected.Selection.ID {
		t.Fatalf("old skill selection lost: %+v %v", original, err)
	}
	manager := workspace.ProjectlessManager{Store: st}
	if _, err = manager.SetDirectory(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	private, _, err := manager.Allocate(t.Context(), "move-private-target-key")
	if err != nil {
		t.Fatal(err)
	}
	req := domain.ThreadProjectMoveRequest{Version: domain.ThreadProjectMoveVersion, ThreadID: moved.Thread.ID, ProjectWorkspaceID: private.ID, ExpectedVersion: 2, OperationKey: "move-private-rejected-key", RequestedBy: "test_operator"}
	if _, err = application.NewThreadProjectMoveService(st, nil).Move(t.Context(), req); apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeInvalidArgument {
		t.Fatalf("internal target=%v", err)
	}
	req.ProjectWorkspaceID = "review-workspace"
	req.OperationKey = "move-back-operation-0001"
	back, err := application.NewThreadProjectMoveService(st, nil).Move(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	middle, err := st.GetRun(t.Context(), prepared.ID)
	if err != nil || middle.Status != domain.RunCancelled {
		t.Fatalf("intermediate created Run=%+v %v", middle, err)
	}
	if back.Thread.WorkspaceID != "review-workspace" || back.Thread.ProjectAssignmentVersion != 3 {
		t.Fatalf("second move=%+v", back)
	}
	exported, err := st.ExportThread(t.Context(), back.Thread.ID)
	if err != nil || len(exported.Runs) != 3 || len(exported.Missions) != 3 {
		t.Fatalf("repeated move export=%+v %v", exported, err)
	}
	if _, err = os.Stat(filepath.Join(target, "AGENTS.md")); err != nil {
		t.Fatal("target file moved", err)
	}
	if _, err = st.SearchThreadHistory(t.Context(), back.Thread.LastRunID, domain.HistorySearchRequest{}); err != nil {
		t.Fatal("second move lineage", err)
	}
}

func TestThreadProjectMovePreservesLegacyUnboundChatHistory(t *testing.T) {
	st, _, request := threadControlFixture(t, &scriptedToolProvider{})
	projectMoveTarget(t, st)
	thread, err := st.GetThread(t.Context(), request.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.GetRun(t.Context(), thread.LastRunID)
	if err != nil {
		t.Fatal(err)
	}
	saveThreadChangeReply(t, st, run, "LEGACY_UNBOUND_HISTORY_CANARY")
	moved, err := moveProject(t, st, run, "legacy-move-operation-0001")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := st.GetRun(t.Context(), moved.Thread.LastRunID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prepared.Config.ContinuityContext), "LEGACY_UNBOUND_HISTORY_CANARY") {
		t.Fatal("legacy history missing from next model context")
	}
}

// Completes a source reply after snapshot preparation, before the write transaction.
type projectMoveHistoryRaceStore struct {
	*store.SQLiteStore
	change func()
}

func (s projectMoveHistoryRaceStore) MoveThreadProject(ctx context.Context, r domain.ThreadProjectMoveRequest, runID string, sequence int64, mission domain.Mission, run domain.Run, mode domain.RunModeSnapshot, sess session.Session, initial []events.Event) (domain.ThreadProjectMoveResult, error) {
	s.change()
	return s.SQLiteStore.MoveThreadProject(ctx, r, runID, sequence, mission, run, mode, sess, initial)
}
func TestThreadProjectMoveRejectsHistoryChangeDuringPreparation(t *testing.T) {
	st, old := threadReviewFixture(t, t.TempDir())
	projectMoveTarget(t, st)
	raced := projectMoveHistoryRaceStore{SQLiteStore: st, change: func() { saveThreadChangeReply(t, st, old, "concurrent completed reply") }}
	_, err := application.NewThreadProjectMoveService(raced, nil).Move(t.Context(), domain.ThreadProjectMoveRequest{Version: domain.ThreadProjectMoveVersion, ThreadID: domain.InitialThreadID(old.ID), ProjectWorkspaceID: "move-target", ExpectedVersion: 1, OperationKey: "move-raced-history-operation", RequestedBy: "test_operator"})
	if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
		t.Fatalf("stale history accepted: %v", err)
	}
	thread, err := st.GetThreadByRun(t.Context(), old.ID)
	if err != nil || thread.LastRunID != old.ID || thread.ProjectAssignmentVersion != 1 {
		t.Fatalf("stale move committed: %+v %v", thread, err)
	}
}
