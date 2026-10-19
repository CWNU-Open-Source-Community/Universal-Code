package httpapi

import (
	"net/http"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestThreadPeerTranscriptPreservesSenderAndAuthority(t *testing.T) {
	f := newAPIFixture(t)
	_, target, err := application.NewRunService(f.store).Create(t.Context(), application.CreateRunRequest{
		Goal: "Peer recipient", WorkspaceID: f.workspace.ID, Profile: "review", ModelRoute: "review",
		Interactive: true, Budget: domain.Budget{MaxTurns: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	peer := application.NewThreadPeerMessageService(f.store, application.NewThreadTurnService(f.store, nil, nil))
	sourceID, targetID := domain.InitialThreadID(f.run.ID), domain.InitialThreadID(target.ID)
	receipt, err := peer.Send(t.Context(), domain.SendThreadPeerMessageRequest{
		SourceThreadID: sourceID, TargetThreadID: targetID, Content: "A model collaboration request",
		OperationKey: "peer-transcript-model-message", RequestedBy: "run_supervisor",
		SourceRunID: f.run.ID, SourceAttemptID: f.checkpoint.AttemptID,
		SourceLeaseID: f.checkpoint.LeaseID, SourceLeaseGeneration: f.checkpoint.LeaseGeneration,
	})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(f.store, Config{AccessToken: testAccessToken})
	if err != nil {
		t.Fatal(err)
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
}
