package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
)

func TestThreadRequestObservationHTTPReadsAfterRestartWithoutControlOrModel(t *testing.T) {
	fixture := newAPIFixture(t)
	key := "http-original-create-observation"
	created, err := application.NewControlledRunCreationService(fixture.store).Create(t.Context(), application.ControlledRunCreationRequest{
		Version: domain.RunCreationProtocolVersion, Goal: "creation receipt", WorkspaceID: fixture.workspace.ID, OperationKey: key, RequestedBy: "http_thread_operator"})
	if err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(fixture.store)
	if _, err = runs.Start(t.Context(), created.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Fail(t.Context(), created.Run.ID, "fixture later task failure"); err != nil {
		t.Fatal(err)
	}
	if err = fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// Neither a control token nor model/lifecycle/execution controllers exist.
	api, err := New(reopened, Config{AccessToken: testAccessToken})
	if err != nil {
		t.Fatal(err)
	}
	path := ThreadCreationRequestPath + "?workspace_id=" + url.QueryEscape(fixture.workspace.ID)
	response := performSessionMessageRequest(t, api, http.MethodGet, path, testAccessToken, key, "", nil)
	var got ThreadRequestObservationView
	decodeDataStatus(t, response, http.StatusOK, &got)
	if got.Kind != "creation" || got.State != "completed" || !got.Settled || got.ThreadID != domain.InitialThreadID(created.Run.ID) {
		t.Fatalf("creation observation=%#v", got)
	}
	response = performSessionMessageRequest(t, api, http.MethodGet, path, testAccessToken, "http-absent-create-observation", "", nil)
	got = ThreadRequestObservationView{}
	decodeDataStatus(t, response, http.StatusOK, &got)
	if got.State != "not_received" || got.Settled || got.RunID != "" {
		t.Fatalf("missing lookup created work: %#v", got)
	}
}

func TestThreadRequestObservationHTTPMCPInputSurvivesRestart(t *testing.T) {
	fixture := newAPIFixture(t)
	runs := application.NewRunService(fixture.store)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{
		Goal: "Observe MCP selection", Profile: "review", WorkspaceID: fixture.workspace.ID, Interactive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(run.ID)
	key := "http-observe-mcp-original"
	intent := domain.ThreadMessageIntentRequest{
		ThreadID: threadID, OperationKey: key, RequestedBy: "http_thread_operator", Content: "Use selected MCP context",
		MCPReferences: []domain.MCPReference{
			{ServerID: "selected-mcp", CapabilityFingerprint: strings.Repeat("a", 64), Method: "resources/read", Arguments: json.RawMessage(`{"uri":"fixture://notes"}`)},
			{ServerID: "selected-mcp", CapabilityFingerprint: strings.Repeat("a", 64), Method: "prompts/get", Arguments: json.RawMessage(`{"name":"review","arguments":{"language":"Go"}}`)},
		},
	}
	if _, err := fixture.store.ReserveThreadMessageIntent(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	queued, err := fixture.store.CommitThreadMessage(t.Context(), intent, run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// The observation server has no model, MCP client, or execution controller.
	api, err := New(reopened, Config{AccessToken: testAccessToken})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	defer server.Close()
	before, err := reopened.ExportThread(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			server.URL+ThreadCollectionPath+"/"+threadID+"/turn-request", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+testAccessToken)
		request.Header.Set("Idempotency-Key", key)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Data ThreadRequestObservationView `json:"data"`
		}
		err = json.NewDecoder(response.Body).Decode(&envelope)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || envelope.Data.State != "received" ||
			envelope.Data.Settled || envelope.Data.MessageID != queued.Message.ID || envelope.Data.MessageStatus != "pending" {
			t.Fatalf("MCP input could not be confirmed after restart: status=%d result=%#v err=%v", response.StatusCode, envelope.Data, err)
		}
	}
	after, err := reopened.ExportThread(t.Context(), threadID)
	after.ExportedAt = before.ExportedAt
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("MCP observation changed history: %v", err)
	}
}

func TestThreadRequestObservationHTTPBoundariesAndPendingIntentDoNotWrite(t *testing.T) {
	fixture := newAPIFixture(t)
	threadID := domain.InitialThreadID(fixture.run.ID)
	key := "http-observe-file-original"
	intent := domain.ThreadMessageIntentRequest{ThreadID: threadID, OperationKey: key, RequestedBy: "http_thread_operator", Content: "original unpublished file request",
		Files: []domain.WorkspaceFileReference{{SourceKind: "workspace_file", Path: "README.md", ExpectedSHA256: strings.Repeat("a", 64)}}}
	if _, err := fixture.store.ReserveThreadMessageIntent(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken})
	if err != nil {
		t.Fatal(err)
	}
	path := ThreadCollectionPath + "/" + threadID + "/turn-request"
	before, err := fixture.store.ExportThread(t.Context(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	var got ThreadRequestObservationView
	response := performSessionMessageRequest(t, api, http.MethodGet, path, testAccessToken, key, "", nil)
	decodeDataStatus(t, response, http.StatusOK, &got)
	if got.State != "received" || got.Settled || got.MessageID != "" || got.ThreadID != threadID {
		t.Fatalf("reserved observation=%#v", got)
	}
	if strings.Contains(response.Body.String(), intent.Content) || strings.Contains(response.Body.String(), key) || strings.Contains(response.Body.String(), "README.md") {
		t.Fatal("observation exposed original payload or raw key")
	}
	for _, test := range []struct {
		method, token, key, path, body string
		status                         int
	}{
		{http.MethodGet, "", key, path, "", http.StatusUnauthorized},
		{http.MethodGet, testAccessToken, "", path, "", http.StatusBadRequest},
		{http.MethodGet, testAccessToken, key, path + "?unexpected=1", "", http.StatusBadRequest},
		{http.MethodGet, testAccessToken, key, path, "{}", http.StatusBadRequest},
		{http.MethodPost, testAccessToken, key, path, "{}", http.StatusMethodNotAllowed},
	} {
		response = performSessionMessageRequest(t, api, test.method, test.path, test.token, test.key, "", strings.NewReader(test.body))
		if response.Code != test.status {
			t.Fatalf("%s %s got %d %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
	after, err := fixture.store.ExportThread(t.Context(), threadID)
	after.ExportedAt = before.ExportedAt
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("GET/boundary checks changed history: %v", err)
	}
}
