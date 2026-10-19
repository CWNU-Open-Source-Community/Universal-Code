package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/session"
)

func TestThreadChangesHTTPIsReadOnlyAndThreadReplyScoped(t *testing.T) {
	fixture := newAPIFixture(t)
	ctx := t.Context()
	manager := fileedit.NewManager(fixture.store)
	edit, err := manager.Propose(ctx, fileedit.Proposal{SessionID: fixture.run.SessionID, WorkspaceID: fixture.workspace.ID,
		WorkspaceRoot: fixture.workspace.RootPath, Path: "changed.txt", Operation: fileedit.OperationCreate,
		ExpectedOriginalHash: "missing", ProposedText: "recorded change\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewFileEditReviewService(fixture.store).Review(ctx, application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: fixture.run.ID, EditID: edit.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(ctx, edit.ID, fixture.workspace.RootPath); err != nil {
		t.Fatal(err)
	}
	reply, err := fixture.store.SaveSessionMessage(ctx, session.NewMessage(fixture.run.SessionID, "assistant", "change completed"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := fixture.store.ListRunEvents(ctx, fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sourceID string
	for _, event := range list {
		if event.Type == events.SessionMessageEvent && event.SubjectID == fmt.Sprint(reply.ID) {
			sourceID = event.EventID
		}
	}
	threadID := domain.InitialThreadID(fixture.run.ID)
	path := ThreadCollectionPath + "/" + threadID + "/changes"
	before, err := fixture.store.ExportThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, fixture.api, http.MethodGet, path+"?source_event_id="+url.QueryEscape(sourceID), testAccessToken, "", "", nil)
	var result application.ThreadChangeSummary
	decodeDataStatus(t, response, http.StatusOK, &result)
	if result.Version != application.ThreadChangeSummaryVersion || result.ThreadID != threadID || result.SourceEventID != sourceID ||
		result.RunID != fixture.run.ID || result.FileCount != 1 || result.Additions != 1 || result.Deletions != 0 ||
		!result.CountsAvailable || result.Partial || result.Files[0].EditIDs[0] != edit.ID {
		t.Fatalf("wrong reply summary: %+v", result)
	}
	for _, test := range []struct {
		method, requestPath, token string
		status                     int
	}{
		{http.MethodGet, path, testAccessToken, http.StatusBadRequest},
		{http.MethodGet, path + "?source_event_id=" + sourceID + "&unknown=true", testAccessToken, http.StatusBadRequest},
		{http.MethodGet, path + "?source_event_id=" + sourceID + "&source_event_id=" + sourceID, testAccessToken, http.StatusBadRequest},
		{http.MethodGet, path + "?source_event_id=unknown-event", testAccessToken, http.StatusNotFound},
		{http.MethodGet, ThreadCollectionPath + "/other-thread/changes?source_event_id=" + sourceID, testAccessToken, http.StatusNotFound},
		{http.MethodGet, path + "?source_event_id=" + sourceID, "", http.StatusUnauthorized},
		{http.MethodPost, path + "?source_event_id=" + sourceID, testAccessToken, http.StatusMethodNotAllowed},
	} {
		response := performSessionMessageRequest(t, fixture.api, test.method, test.requestPath, test.token, "", "", nil)
		if response.Code != test.status {
			t.Fatalf("%s %s expected %d got %d: %s", test.method, test.requestPath, test.status, response.Code, response.Body.String())
		}
	}
	// A file-edit event is not a reply source, even though it belongs to this task.
	for _, event := range list {
		if event.Type == events.FileEditAppliedEvent {
			response := performSessionMessageRequest(t, fixture.api, http.MethodGet, path+"?source_event_id="+event.EventID, testAccessToken, "", "", nil)
			assertAPIError(t, response, http.StatusNotFound, "NOT_FOUND")
		}
	}
	after, err := fixture.store.ExportThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	before.ExportedAt, after.ExportedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("GET changes changed durable history")
	}
}
