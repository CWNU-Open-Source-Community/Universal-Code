package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func TestThreadChangeUndoHTTPControlPreviewApplyAndRefresh(t *testing.T) {
	f := newAPIFixture(t)
	f.api.workspaceCheckpointControlEnabled = true
	f.api.executionPermissionCapabilities.OperatorApprovalEnabled = true
	f.workspace = store.WorkspaceRecord{ID: "undo-http-workspace", Name: "undo-http", RootPath: t.TempDir()}
	if err := f.store.SaveWorkspace(t.Context(), f.workspace); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(f.store).Create(t.Context(), application.CreateRunRequest{Goal: "undo reply", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: f.workspace.ID, Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	f.run = run
	manager := fileedit.NewManager(f.store)
	e, err := manager.Propose(t.Context(), fileedit.Proposal{SessionID: f.run.SessionID, WorkspaceID: f.workspace.ID, WorkspaceRoot: f.workspace.RootPath, Path: "reply.txt", Operation: "create", ExpectedOriginalHash: "missing", ProposedText: "reply change\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewFileEditReviewService(f.store).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: f.run.ID, EditID: e.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(t.Context(), e.ID, f.workspace.RootPath); err != nil {
		t.Fatal(err)
	}
	message, err := f.store.SaveSessionMessage(t.Context(), session.NewMessage(f.run.SessionID, "assistant", "done"))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := f.store.ListRunEvents(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceID := ""
	for _, fact := range facts {
		if fact.Type == events.SessionMessageEvent && fact.SubjectID == fmt.Sprint(message.ID) {
			sourceID = fact.EventID
		}
	}
	threadID := domain.InitialThreadID(f.run.ID)
	path := ThreadCollectionPath + "/" + threadID + "/changes/undo"
	body := fmt.Sprintf(`{"version":"thread_change_undo.v1","source_event_id":%q,"confirm":true}`, sourceID)
	denied := performSessionMessageRequest(t, f.api, http.MethodPost, path, testAccessToken, "", "", strings.NewReader(body))
	assertAPIError(t, denied, http.StatusUnauthorized, "POLICY_DENIED")
	previewBody := strings.Replace(body, `"confirm":true`, `"confirm":false`, 1)
	preview := performControlMethodPathRequest(t, f.api, http.MethodPost, path, "fixed-reply-key", strings.NewReader(previewBody))
	var result application.ThreadChangeUndoResult
	decodeDataStatus(t, preview, http.StatusOK, &result)
	if result.State != "available" {
		t.Fatalf("preview: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(f.workspace.RootPath, "reply.txt")); err != nil {
		t.Fatal("preview changed file")
	}
	confirmed := performControlMethodPathRequest(t, f.api, http.MethodPost, path, "fixed-reply-key", strings.NewReader(body))
	decodeDataStatus(t, confirmed, http.StatusOK, &result)
	if result.State != "completed" {
		t.Fatalf("undo: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(f.workspace.RootPath, "reply.txt")); !os.IsNotExist(err) {
		t.Fatalf("creation not undone: %v", err)
	}
	refresh := performSessionMessageRequest(t, f.api, http.MethodGet, ThreadCollectionPath+"/"+threadID+"/changes?source_event_id="+sourceID, testAccessToken, "", "", nil)
	var summary application.ThreadChangeSummary
	decodeDataStatus(t, refresh, http.StatusOK, &summary)
	if summary.Undo == nil || summary.Undo.State != "completed" || !summary.Undo.Replayed {
		t.Fatalf("refresh lost receipt: %+v", summary.Undo)
	}
	foreign := performControlMethodPathRequest(t, f.api, http.MethodPost, ThreadCollectionPath+"/foreign-thread/changes/undo", "fixed-reply-key", strings.NewReader(body))
	assertAPIError(t, foreign, http.StatusNotFound, "NOT_FOUND")
	f.api.workspaceCheckpointControlEnabled = false
	disabled := performControlMethodPathRequest(t, f.api, http.MethodPost, path, "fixed-reply-key", strings.NewReader(body))
	assertAPIError(t, disabled, http.StatusNotFound, "NOT_FOUND")
}
