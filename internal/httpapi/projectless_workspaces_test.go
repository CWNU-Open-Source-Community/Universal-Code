package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/workspace"
)

func TestProjectlessWorkspaceDraftCreatesLocalThreadAndReplays(t *testing.T) {
	fixture := newAPIFixture(t)
	root := t.TempDir()
	if _, err := (workspace.ProjectlessManager{Store: fixture.store}).SetDirectory(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	allocate := func(key string) ProjectlessWorkspaceView {
		t.Helper()
		response := performSessionMessageRequest(t, api, http.MethodPost, ProjectlessWorkspacePath, testControlToken, key, "application/json", strings.NewReader(`{"version":"projectless_workspace.v1"}`))
		var view ProjectlessWorkspaceView
		decodeDataStatus(t, response, http.StatusOK, &view)
		return view
	}
	draft := allocate("local-draft-operation-0001")
	retry := allocate("local-draft-operation-0001")
	if retry.Workspace.ID != draft.Workspace.ID || !retry.Replayed {
		t.Fatalf("draft retry changed workspace: %+v %+v", draft, retry)
	}
	record, err := fixture.store.GetWorkspaceByID(t.Context(), draft.Workspace.ID)
	if err != nil || filepath.Dir(record.RootPath) != root {
		t.Fatalf("draft workspace=%+v err=%v", record, err)
	}
	if err := os.WriteFile(filepath.Join(record.RootPath, "draft-attachment.txt"), []byte("before thread creation"), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ThreadCreationControlRequestView{Version: domain.ThreadCreationProtocolVersion, Goal: "local chat", WorkspaceID: record.ID})
	create := func() ThreadCreationControlView {
		t.Helper()
		response := performSessionMessageRequest(t, api, http.MethodPost, ThreadCollectionPath, testControlToken, "local-create-operation-0001", "application/json", bytes.NewReader(body))
		var view ThreadCreationControlView
		decodeDataStatus(t, response, http.StatusAccepted, &view)
		return view
	}
	created := create()
	repeated := create()
	if created.Thread.ID != repeated.Thread.ID || !repeated.Replayed || created.Session.WorkspaceID != record.ID {
		t.Fatalf("creation did not retain draft: %+v %+v", created, repeated)
	}
	duplicate := performSessionMessageRequest(t, api, http.MethodPost, ThreadCollectionPath, testControlToken, "another-local-create-operation-0002", "application/json", bytes.NewReader(body))
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("private directory reused by independent creation: %d %s", duplicate.Code, duplicate.Body.String())
	}
	projects, err := fixture.store.ListWorkspaces(t.Context())
	if err != nil || len(projects) != 1 || projects[0].ID != fixture.workspace.ID {
		t.Fatalf("private draft in project list: %+v %v", projects, err)
	}
}

func TestProjectlessDirectoryRequiresControlAndServerImportCapability(t *testing.T) {
	fixture := newAPIFixture(t)
	manager := workspace.NewManager(t.TempDir(), fixture.store)
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true, WorkspaceImportEnabled: true, WorkspaceImporter: manager})
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, api, http.MethodGet, ProjectlessDirectoryPath, testAccessToken, "", "", nil)
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "directory_path") {
		t.Fatalf("read token saw private root: %d %s", response.Code, response.Body.String())
	}
	root := t.TempDir()
	body, _ := json.Marshal(ProjectlessDirectoryRequestView{Version: "projectless_directory.v1", DirectoryPath: root, Confirmed: true})
	response = performSessionMessageRequest(t, api, http.MethodPut, ProjectlessDirectoryPath, testAccessToken, "", "application/json", bytes.NewReader(body))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("read token changed directory: %d %s", response.Code, response.Body.String())
	}
	response = performSessionMessageRequest(t, api, http.MethodPut, ProjectlessDirectoryPath, testControlToken, "", "application/json", bytes.NewReader(body))
	var settings workspace.ProjectlessDirectory
	decodeDataStatus(t, response, http.StatusOK, &settings)
	if settings.DirectoryPath != root {
		t.Fatalf("saved root=%q want=%q", settings.DirectoryPath, root)
	}
	projects, err := fixture.store.ListWorkspaces(t.Context())
	if err != nil || len(projects) != 1 || projects[0].ID != fixture.workspace.ID {
		t.Fatalf("setting a chat directory registered a project: %+v %v", projects, err)
	}
	for _, invalid := range []string{
		`{"version":"projectless_directory.v1","directory_path":"relative","confirmed":true}`,
		`{"version":"projectless_directory.v1","directory_path":"/missing-chat-root","confirmed":false}`,
		`{"version":"projectless_directory.v1","workspace_id":"` + fixture.workspace.ID + `","confirmed":true}`,
	} {
		response = performSessionMessageRequest(t, api, http.MethodPut, ProjectlessDirectoryPath, testControlToken, "", "application/json", strings.NewReader(invalid))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid directory accepted: %d %s", response.Code, response.Body.String())
		}
	}
	desktopAPI, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	response = performSessionMessageRequest(t, desktopAPI, http.MethodPut, ProjectlessDirectoryPath, testControlToken, "", "application/json", bytes.NewReader(body))
	if response.Code != http.StatusNotFound {
		t.Fatalf("desktop API accepted server path selection: %d %s", response.Code, response.Body.String())
	}
}

func TestThreadCreationAllocatesDirectoryWhenWorkspaceOmitted(t *testing.T) {
	fixture := newAPIFixture(t)
	root := t.TempDir()
	if _, err := (workspace.ProjectlessManager{Store: fixture.store}).SetDirectory(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	requestBody := `{"version":"thread_creation.v1","goal":"new local chat"}`
	response := performSessionMessageRequest(t, api, http.MethodPost, ThreadCollectionPath, testControlToken, "no-project-operation-0001", "application/json", strings.NewReader(requestBody))
	var created ThreadCreationControlView
	decodeDataStatus(t, response, http.StatusAccepted, &created)
	record, err := fixture.store.GetWorkspaceByID(t.Context(), created.Session.WorkspaceID)
	if err != nil || filepath.Dir(record.RootPath) != root {
		t.Fatalf("thread allocated wrong directory: %+v %v", record, err)
	}
}
