package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspace"
)

func TestWorkspaceProjectControlCountsAndRename(t *testing.T) {
	f := newAPIFixture(t)
	ctx := t.Context()
	record := session.WorkspaceRecord{ID: "ws-project-details", Name: "Details project", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := f.store.SaveWorkspace(ctx, record); err != nil {
		t.Fatal(err)
	}
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + record.ID
	read := performSessionMessageRequest(t, api, http.MethodGet, path+"/project", testAccessToken, "", "", nil)
	if read.Code != 401 || strings.Contains(read.Body.String(), record.RootPath) {
		t.Fatal(read.Code, read.Body.String())
	}
	list := performSessionMessageRequest(t, api, http.MethodGet, "/api/v1/workspaces", testAccessToken, "", "", nil)
	if bytes.Contains(list.Body.Bytes(), []byte(record.RootPath)) || strings.Contains(list.Body.String(), "directory_path") {
		t.Fatal("public list exposed directory")
	}
	runs := []domain.Run{}
	for i := 0; i < 3; i++ {
		_, r, e := application.NewRunService(f.store).Create(ctx, application.CreateRunRequest{Goal: "counted chat", WorkspaceID: record.ID, Interactive: true, Profile: "review"})
		if e != nil {
			t.Fatal(e)
		}
		runs = append(runs, r)
	}
	_, err = application.NewThreadService(f.store).Submit(ctx, application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(runs[0].ID), Content: "queued input", OperationKey: "project-count-pending-input", RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	var detail WorkspaceProjectView
	decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodGet, path+"/project", testControlToken, "", "", nil), 200, &detail)
	if detail.ThreadCount != 3 || detail.ActiveThreadCount != 1 || detail.DirectoryPath != record.RootPath {
		t.Fatalf("counts=%+v", detail)
	}
	_, err = application.NewThreadProjectMoveService(f.store, nil).Move(ctx, domain.ThreadProjectMoveRequest{Version: domain.ThreadProjectMoveVersion, ThreadID: domain.InitialThreadID(runs[1].ID), ProjectWorkspaceID: f.workspace.ID, ExpectedVersion: 1, OperationKey: "project-count-move-operation", RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := f.store.GetThreadByRun(ctx, runs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.NewThreadService(f.store).Transition(ctx, thread.ID, domain.ThreadArchive, thread.Version, "operator", "project-count-archive-operation")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodPut, path+"/name", testControlToken, "", "application/json", strings.NewReader(`{"name":"  项目 / Example  "}`)), 200, &detail)
	}
	if detail.Name != "项目 / Example" || detail.DirectoryPath != record.RootPath || detail.ThreadCount != 1 || detail.ActiveThreadCount != 1 {
		t.Fatalf("renamed=%+v", detail)
	}
	stored, err := f.store.GetWorkspaceByID(ctx, record.ID)
	if err != nil || stored.RootPath != record.RootPath || stored.Name != detail.Name {
		t.Fatal(stored, err)
	}
	for _, body := range []string{`{"name":""}`, `{"name":"bad\nname"}`, `{"name":"x","directory_path":"/other"}`, `{"name":"x","name":"y"}`} {
		response := performSessionMessageRequest(t, api, http.MethodPut, path+"/name", testControlToken, "", "application/json", strings.NewReader(body))
		if response.Code != 400 {
			t.Fatal(body, response.Code, response.Body.String())
		}
	}
	body, _ := json.Marshal(WorkspaceNameRequestView{Name: f.workspace.Name})
	response := performSessionMessageRequest(t, api, http.MethodPut, path+"/name", testControlToken, "", "application/json", bytes.NewReader(body))
	if response.Code != 409 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = performSessionMessageRequest(t, api, http.MethodPut, path+"/name", testAccessToken, "", "application/json", strings.NewReader(`{"name":"no"}`))
	if response.Code != 401 {
		t.Fatal(response.Code)
	}
}
func TestWorkspaceImportOptionalName(t *testing.T) {
	f := newAPIFixture(t)
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, WorkspaceImportEnabled: true, WorkspaceImporter: workspace.NewManager("", f.store)})
	if err != nil {
		t.Fatal(err)
	}
	name := "自选 / project"
	path := t.TempDir()
	body, _ := json.Marshal(WorkspaceImportRequestView{Version: WorkspaceImportProtocolVersion, DirectoryPath: path, Confirmed: true, Name: &name})
	var view WorkspaceImportView
	decodeDataStatus(t, performSessionMessageRequest(t, api, http.MethodPost, WorkspaceImportPath, testControlToken, "", "application/json", bytes.NewReader(body)), 200, &view)
	if view.Workspace.Name != name {
		t.Fatal(view)
	}
	stored, err := f.store.GetWorkspaceByID(t.Context(), view.Workspace.ID)
	if err != nil || stored.RootPath == "" {
		t.Fatal(stored, err)
	}
}
