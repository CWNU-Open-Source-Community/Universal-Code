package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
)

func TestMemoryCandidateHTTPReviewUsesControlBoundaryAndExactSource(t *testing.T) {
	fixture := newAPIFixture(t)
	note := createHTTPMemoryCandidateSource(t, fixture)
	path := "/api/v1/memory-candidates?workspace_id=" + fixture.workspace.ID
	var candidates []contextmgr.MemoryCandidate
	decodeData(t, fixture.get(t, path), &candidates)
	if len(candidates) != 1 || candidates[0].Source.NoteID != note.ID || candidates[0].Content != note.Content {
		t.Fatalf("queue=%#v", candidates)
	}
	candidate := candidates[0]
	var before []contextmgr.Memory
	decodeData(t, fixture.get(t, "/api/v1/memories?scope=project&scope_id="+fixture.workspace.ID), &before)
	if len(before) != 0 {
		t.Fatalf("queue inspection created memory: %#v", before)
	}
	body := memoryCandidateConfirmRequestView{WorkspaceID: candidate.WorkspaceID, ExpectedVersion: candidate.Version, ExpectedFingerprint: candidate.Fingerprint, ExpectedSourceVersion: candidate.Source.NoteVersion, Confirm: true}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	confirmPath := "/api/v1/memory-candidates/" + candidate.ID + "/confirm"
	unauthorized := continuityHTTPMutation(t, fixture.api, http.MethodPost, confirmPath, testAccessToken, string(encoded))
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "POLICY_DENIED")
	changed := body
	changed.ExpectedSourceVersion++
	changedJSON, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, continuityHTTPMutation(t, fixture.api, http.MethodPost, confirmPath, testControlToken, string(changedJSON)), http.StatusConflict, "CONFLICT")
	missingConfirm := body
	missingConfirm.Confirm = false
	missingJSON, err := json.Marshal(missingConfirm)
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, continuityHTTPMutation(t, fixture.api, http.MethodPost, confirmPath, testControlToken, string(missingJSON)), http.StatusBadRequest, "INVALID_ARGUMENT")
	var confirmed contextmgr.MemoryCandidateReviewResult
	decodeData(t, continuityHTTPMutation(t, fixture.api, http.MethodPost, confirmPath, testControlToken, string(encoded)), &confirmed)
	if confirmed.Replayed || confirmed.MemoryID == "" || confirmed.Candidate.Status != contextmgr.MemoryCandidateConfirmed || confirmed.Candidate.Content != "" || confirmed.Candidate.ReviewedBy != "http_control" {
		t.Fatalf("confirmation=%#v", confirmed)
	}
	var memory contextmgr.Memory
	decodeData(t, fixture.get(t, "/api/v1/memories/"+confirmed.MemoryID), &memory)
	if memory.Content != note.Content || memory.ScopeID != fixture.workspace.ID || memory.SourceRef != candidate.ID {
		t.Fatalf("memory=%#v", memory)
	}
	var replay contextmgr.MemoryCandidateReviewResult
	decodeData(t, continuityHTTPMutation(t, fixture.api, http.MethodPost, confirmPath, testControlToken, string(encoded)), &replay)
	if !replay.Replayed || replay.MemoryID != confirmed.MemoryID {
		t.Fatalf("replay=%#v", replay)
	}
	decodeData(t, fixture.get(t, path), &candidates)
	if len(candidates) != 0 {
		t.Fatalf("resolved candidate remained pending: %#v", candidates)
	}
	var deleted contextMemoryDeleteView
	decodeData(t, continuityHTTPMutation(t, fixture.api, http.MethodDelete, "/api/v1/memories/"+memory.ID, testControlToken, `{"expected_version":1}`), &deleted)
	if !deleted.Deleted {
		t.Fatal("confirmed memory was not deletable through existing control")
	}
	response := fixture.get(t, "/api/v1/memory-candidates/"+candidate.ID+"?workspace_id="+fixture.workspace.ID)
	var audit contextmgr.MemoryCandidate
	decodeData(t, response, &audit)
	if audit.Content != "" || audit.Title != "" || strings.Contains(response.Body.String(), note.Content) {
		t.Fatalf("candidate exposed deleted memory: %s", response.Body.String())
	}
	decodeData(t, continuityHTTPMutation(t, fixture.api, http.MethodPost, confirmPath, testControlToken, string(encoded)), &replay)
	var after []contextmgr.Memory
	decodeData(t, fixture.get(t, "/api/v1/memories?scope=project&scope_id="+fixture.workspace.ID), &after)
	if len(after) != 0 {
		t.Fatalf("retry resurrected deleted memory: %#v", after)
	}
}

func TestMemoryCandidateHTTPRejectAndStrictBoundary(t *testing.T) {
	fixture := newAPIFixture(t)
	_ = createHTTPMemoryCandidateSource(t, fixture)
	var candidates []contextmgr.MemoryCandidate
	decodeData(t, fixture.get(t, "/api/v1/memory-candidates?workspace_id="+fixture.workspace.ID), &candidates)
	candidate := candidates[0]
	path := "/api/v1/memory-candidates/" + candidate.ID + "/reject"
	body := memoryCandidateRejectRequestView{WorkspaceID: fixture.workspace.ID, ExpectedVersion: candidate.Version, ExpectedFingerprint: candidate.Fingerprint, ExpectedSourceVersion: candidate.Source.NoteVersion}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.TrimSuffix(string(encoded), "}") + `,"requested_by":"model"}`
	assertAPIError(t, continuityHTTPMutation(t, fixture.api, http.MethodPost, path, testControlToken, unknown), http.StatusBadRequest, "INVALID_ARGUMENT")
	fixture.api.controlEnabled = false
	assertAPIError(t, continuityHTTPMutation(t, fixture.api, http.MethodPost, path, testControlToken, string(encoded)), http.StatusNotFound, "NOT_FOUND")
	fixture.api.controlEnabled = true
	var rejected contextmgr.MemoryCandidateReviewResult
	decodeData(t, continuityHTTPMutation(t, fixture.api, http.MethodPost, path, testControlToken, string(encoded)), &rejected)
	if rejected.Candidate.Status != contextmgr.MemoryCandidateRejected || rejected.Candidate.Content != "" || rejected.MemoryID != "" {
		t.Fatalf("rejection=%#v", rejected)
	}
	decodeData(t, fixture.get(t, "/api/v1/memory-candidates?workspace_id="+fixture.workspace.ID), &candidates)
	if len(candidates) != 0 {
		t.Fatalf("dismissal reappeared: %#v", candidates)
	}
	var audit []contextmgr.MemoryCandidate
	decodeData(t, fixture.get(t, "/api/v1/memory-candidates?workspace_id="+fixture.workspace.ID+"&include_resolved=true"), &audit)
	if len(audit) != 1 || audit[0].Status != contextmgr.MemoryCandidateRejected {
		t.Fatalf("audit=%#v", audit)
	}
	assertAPIError(t, fixture.get(t, "/api/v1/memory-candidates"), http.StatusBadRequest, "INVALID_ARGUMENT")
	assertAPIError(t, fixture.get(t, "/api/v1/memory-candidates?workspace_id="+fixture.workspace.ID+"&limit=51"), http.StatusBadRequest, "INVALID_ARGUMENT")
}

func createHTTPMemoryCandidateSource(t *testing.T, fixture *apiFixture) domain.Note {
	t.Helper()
	runs := application.NewRunService(fixture.store)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "Remember a proven parsing conclusion", Profile: "code", WorkspaceID: fixture.workspace.ID, Budget: domain.DefaultBudget()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	note, err := application.NewNoteService(fixture.store).Create(t.Context(), application.CreateNoteRequest{RunID: run.ID, Title: "Parser conclusion", Content: "Keep the strict decoder and run package tests.", Category: "decision", Visibility: "run", SourceRefs: []string{"README.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Complete(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	return note
}
