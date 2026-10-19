package httpapi

import (
	"bytes"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestThreadProjectMoveHTTPAuthorizationAndReplay(t *testing.T) {
	f := newAPIFixture(t)
	_, run, err := application.NewRunService(f.store).Create(t.Context(), application.CreateRunRequest{Goal: "move HTTP", WorkspaceID: f.workspace.ID, Profile: "review", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "http-project-target", Name: "http target", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	path := ThreadCollectionPath + "/" + domain.InitialThreadID(run.ID) + "/project"
	body, _ := json.Marshal(ThreadProjectMoveRequestView{Version: domain.ThreadProjectMoveVersion, ProjectWorkspaceID: "http-project-target", ExpectedVersion: 1})
	denied := performSessionMessageRequest(t, api, http.MethodPost, path, testAccessToken, "project-move-http-operation", "application/json", bytes.NewReader(body))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("access-only=%d %s", denied.Code, denied.Body)
	}
	response := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "project-move-http-operation", "application/json", bytes.NewReader(body))
	var result ThreadProjectMoveView
	decodeDataStatus(t, response, http.StatusOK, &result)
	if result.Thread.ProjectWorkspaceID != "http-project-target" || result.Thread.ProjectAssignmentVersion != 2 || result.Thread.ID != domain.InitialThreadID(run.ID) {
		t.Fatalf("response=%+v", result)
	}
	replay := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "project-move-http-operation", "application/json", bytes.NewReader(body))
	decodeDataStatus(t, replay, http.StatusOK, &result)
	if !result.Replayed {
		t.Fatal("retry did not replay")
	}
}
