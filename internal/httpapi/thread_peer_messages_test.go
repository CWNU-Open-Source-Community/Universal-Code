package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

func TestThreadPeerHTTPReceiptTranscriptAndControlBoundary(t *testing.T) {
	f := newAPIFixture(t)
	_, target, err := application.NewRunService(f.store).Create(t.Context(), application.CreateRunRequest{Goal: "Peer recipient", WorkspaceID: f.workspace.ID, Profile: "review", ModelRoute: "review", Interactive: true, Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	turns := application.NewThreadTurnService(f.store, application.NewRunLifecycleControlService(f.store), application.NewRunExecutionHandoffService(f.store, llm.NewRouter(llm.ModelRef{}), policy.NewDefaultChecker()))
	peer := application.NewThreadPeerMessageService(f.store, turns)
	config := Config{AccessToken: testAccessToken, ControlToken: testControlToken, SessionMessageEnabled: true, RunExecutionEnabled: true, RunExecutionController: runExecutionControllerFake{}, ThreadTurnController: turns, ThreadPeerMessages: peer}
	api, err := New(f.store, config)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, targetID := domain.InitialThreadID(f.run.ID), domain.InitialThreadID(target.ID)
	path := ThreadCollectionPath + "/" + sourceID + "/peer-messages"
	body, _ := json.Marshal(ThreadPeerMessageRequestView{Version: domain.ThreadPeerMessageVersion, TargetThreadID: targetID, Content: "A plain text collaboration request"})
	unauthorized := performSessionMessageRequest(t, api, http.MethodPost, path, testAccessToken, "peer-http-write-access-token", "application/json", bytes.NewReader(body))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("read token posted peer message: %d %s", unauthorized.Code, unauthorized.Body.String())
	}
	response := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "peer-http-first-send-operation", "application/json", bytes.NewReader(body))
	var receipt domain.ThreadPeerMessageReceipt
	decodeDataStatus(t, response, http.StatusAccepted, &receipt)
	if receipt.Message.Status != "queued" || receipt.Message.SourceThreadID != sourceID || receipt.Message.TargetThreadID != targetID || receipt.Message.SourceTitle == "" || receipt.Message.TargetTitle == "" {
		t.Fatalf("receipt: %+v", receipt)
	}
	replayed := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "peer-http-first-send-operation", "application/json", bytes.NewReader(body))
	var replay domain.ThreadPeerMessageReceipt
	decodeDataStatus(t, replayed, http.StatusAccepted, &replay)
	if !replay.Replayed || replay.Message.ID != receipt.Message.ID {
		t.Fatalf("HTTP replay: %+v", replay)
	}
	for _, threadID := range []string{sourceID, targetID} {
		response := performRequest(t, api, http.MethodGet, ThreadCollectionPath+"/"+threadID+"/transcript", testAccessToken, "127.0.0.1", "127.0.0.1:32100", nil)
		var items []ThreadTranscriptItemView
		decodeDataStatus(t, response, http.StatusOK, &items)
		count := 0
		for _, item := range items {
			if item.PeerMessage != nil {
				count++
				if item.Kind != "peer_message" || item.Source != "peer" || item.InstructionAuthorized || item.PeerMessage.ID != receipt.Message.ID || item.Detail != receipt.Message.Content {
					t.Fatalf("peer transcript: %+v", item)
				}
			}
			if item.Source == "operator" && item.Detail == receipt.Message.Content {
				t.Fatalf("peer appeared as user input: %+v", item)
			}
		}
		if count != 1 {
			t.Fatalf("thread %s peer entries=%d: %+v", threadID, count, items)
		}
	}
	targetsResponse := performRequest(t, api, http.MethodGet, ThreadCollectionPath+"/"+sourceID+"/peer-targets", testAccessToken, "127.0.0.1", "127.0.0.1:32100", nil)
	var targets domain.ThreadPeerTargets
	decodeDataStatus(t, targetsResponse, http.StatusOK, &targets)
	found := false
	for _, item := range targets.Items {
		if item.ID == sourceID || item.WorkspaceID != f.workspace.ID {
			t.Fatalf("invalid peer target: %+v", item)
		}
		found = found || item.ID == targetID
	}
	if !found {
		t.Fatal("recipient missing from discovery")
	}
	invalid := []byte(`{"version":"thread_peer_message.v1","target_thread_id":"` + targetID + `","content":"`)
	invalid = append(invalid, 0xff)
	invalid = append(invalid, []byte(`"}`)...)
	bad := performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken, "peer-http-invalid-utf8-key", "application/json", bytes.NewReader(invalid))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF8 accepted: %d %s", bad.Code, bad.Body.String())
	}
	config.RunExecutionEnabled = false
	disabled, err := New(f.store, config)
	if err != nil {
		t.Fatal(err)
	}
	blocked := performSessionMessageRequest(t, disabled, http.MethodPost, path, testControlToken, "peer-http-feature-disabled", "application/json", bytes.NewReader(body))
	if blocked.Code != http.StatusNotFound || !strings.Contains(blocked.Body.String(), "disabled") {
		t.Fatalf("disabled feature returned empty success: %d %s", blocked.Code, blocked.Body.String())
	}
}
