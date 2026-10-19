package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspace"
)

func TestProjectlessInitialAssignmentIsStoredWithCreation(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	manager := workspace.ProjectlessManager{Store: state}
	if _, err := manager.SetDirectory(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	draft, _, err := manager.Allocate(t.Context(), "initial-assignment-draft")
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.NewControlledRunCreationService(state).Create(t.Context(), application.ControlledRunCreationRequest{
		Version: domain.RunCreationProtocolVersion, Goal: "local chat", WorkspaceID: draft.ID, OperationKey: "initial-assignment-creation-operation", RequestedBy: "test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(created.Run.ID)
	var assignment string
	var version int
	if err := state.db.QueryRowContext(t.Context(), `SELECT project_workspace_id,version FROM thread_project_assignments WHERE thread_id=?`, threadID).Scan(&assignment, &version); err != nil {
		t.Fatal(err)
	}
	if assignment != "" || version != 1 {
		t.Fatalf("local assignment=%q version=%d", assignment, version)
	}
	thread, err := state.GetThread(t.Context(), threadID)
	if err != nil || thread.WorkspaceID != draft.ID {
		t.Fatalf("actual execution workspace lost: %+v %v", thread, err)
	}
	if _, err := state.SaveSessionMessage(t.Context(), session.NewMessage(created.Session.ID, "assistant", "branch from this reply")); err != nil {
		t.Fatal(err)
	}
	list, err := state.ListRunEvents(t.Context(), created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var anchor string
	for _, event := range list {
		if event.Type == events.SessionMessageEvent {
			var payload struct{ Role string }
			_ = json.Unmarshal([]byte(event.PayloadJSON), &payload)
			if payload.Role == "assistant" {
				anchor = event.EventID
			}
		}
	}
	branched, err := application.NewThreadBranchService(state).Branch(t.Context(), domain.ThreadBranchRequest{Version: domain.ThreadBranchProtocolVersion, ThreadID: threadID, SourceEventID: anchor, OperationKey: "local-chat-branch-operation", RequestedBy: "test_operator"})
	if err != nil || branched.Thread.ID == threadID || branched.Thread.WorkspaceID != draft.ID {
		t.Fatalf("branch blocked by directory binding: %+v %v", branched, err)
	}
}
