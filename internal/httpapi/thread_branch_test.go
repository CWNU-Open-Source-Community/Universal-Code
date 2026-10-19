package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

func TestThreadBranchHTTPAuthorizationContractAndIndependentContinuation(t *testing.T) {
	f := newAPIFixture(t)
	_, run, err := application.NewRunService(f.store).Create(t.Context(), application.CreateRunRequest{Goal: "branch HTTP", WorkspaceID: f.workspace.ID, Profile: "review", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", "preserve this requirement")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "assistant", "selected reply")); err != nil {
		t.Fatal(err)
	}
	list, err := f.store.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var anchor string
	for _, e := range list {
		if e.Type == events.SessionMessageEvent {
			var p struct{ Role string }
			_ = json.Unmarshal([]byte(e.PayloadJSON), &p)
			if p.Role == "assistant" {
				anchor = e.EventID
			}
		}
	}
	path := ThreadCollectionPath + "/" + domain.InitialThreadID(run.ID) + "/branches"
	body, _ := json.Marshal(ThreadBranchRequestView{Version: domain.ThreadBranchProtocolVersion, SourceEventID: anchor})
	for _, test := range []struct {
		name, token, key   string
		creation, messages bool
		status             int
	}{
		{"without control", testAccessToken, "branch-http-control", true, true, http.StatusUnauthorized},
		{"without creation", testControlToken, "branch-http-creation", false, true, http.StatusNotFound},
		{"without messages", testControlToken, "branch-http-messages", true, false, http.StatusNotFound},
		{"without key", testControlToken, "", true, true, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: test.creation, SessionMessageEnabled: test.messages})
			if err != nil {
				t.Fatal(err)
			}
			response := performSessionMessageRequest(t, api, http.MethodPost, path, test.token, test.key, "application/json", bytes.NewReader(body))
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
		})
	}
	api, err := New(f.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunCreationEnabled: true, SessionMessageEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	response := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "branch-http-original", "application/json", bytes.NewReader(body))
	var result ThreadBranchView
	decodeDataStatus(t, response, http.StatusCreated, &result)
	if result.Version != domain.ThreadBranchProtocolVersion || result.Thread.ID == domain.InitialThreadID(run.ID) || result.SourceEventID != anchor || result.Replayed || result.Run.Status != "created" {
		t.Fatalf("branch response=%+v", result)
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "branch-http-original", "application/json", bytes.NewReader(body))
	var replay ThreadBranchView
	decodeDataStatus(t, response, http.StatusCreated, &replay)
	if !replay.Replayed || replay.Thread.ID != result.Thread.ID {
		t.Fatal("HTTP retry created another branch")
	}
	message := []byte(`{"version":"thread_message_submission.v1","content":"continue this new conversation"}`)
	response = performSessionMessageRequest(t, api, http.MethodPost, ThreadCollectionPath+"/"+result.Thread.ID+"/messages", testControlToken, "branch-http-continue", "application/json", bytes.NewReader(message))
	var continued ThreadMessageControlView
	decodeDataStatus(t, response, http.StatusAccepted, &continued)
	if continued.Thread.ID != result.Thread.ID || continued.RunID != result.Run.ID {
		t.Fatal("continuation did not target the branch")
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "branch-http-unknown-field", "application/json", bytes.NewBufferString(`{"version":"thread_branch.v1","source_event_id":"`+anchor+`","execution_permission":"full_access"}`))
	if response.Code != http.StatusBadRequest {
		t.Fatal("renderer supplied authority fields were accepted")
	}
}
