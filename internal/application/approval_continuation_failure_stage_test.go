package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runactivity"
)

func TestApprovalContinuationSealsExactModelFailureStageBeforeClosingInput(t *testing.T) {
	for _, tc := range []struct {
		name, stage, notice string
		response            *llm.ChatResponse
	}{
		{"empty", domain.ThreadFailureEmptyModelResponse, "模型没有返回有效答复", textResponse(" \t\n")},
		{"tool", domain.ThreadFailureToolRequestRejected, "该批请求尚未执行", toolResponse("invalid-tool", "note_create", `{"title":123,"private":"must-not-enter-stage-event"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantCalls := 3
			if tc.name == "tool" {
				wantCalls = 4
			}
			st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
			provider := &boundaryJourneyProvider{}
			provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				switch index {
				case 1:
					return boundaryPropose("proposal"), nil
				case 2:
					return textResponse(rootActionResponse(domain.RootActionWait, "Review", "", "operator review")), nil
				case 3, 4:
					return tc.response, nil
				default:
					return nil, fmt.Errorf("unexpected model call %d", index)
				}
			}
			turns := toolBoundaryService(st, st, provider)
			if _, err := turns.Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			waiting, err := st.GetRun(t.Context(), run.ID)
			if err != nil || waiting.Status != domain.RunPaused {
				t.Fatalf("ordinary approval wait did not pause: %#v err=%v", waiting, err)
			}
			if recovery, found, err := st.GetThreadRunRecovery(t.Context(), input.ThreadID); err != nil || found {
				t.Fatalf("ordinary approval wait was classified as failed recovery: %#v found=%t err=%v", recovery, found, err)
			}
			edit := approveBoundaryEdit(t, st, run)
			request := application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID}
			result := turns.ResumeApproval(t.Context(), request)
			if result.State != "failed" || result.ErrorCode != "FAILED_PRECONDITION" || len(provider.Requests()) != wantCalls {
				t.Fatalf("continuation=%#v calls=%d", result, len(provider.Requests()))
			}
			paused, err := st.GetRun(t.Context(), run.ID)
			if err != nil || paused.Status != domain.RunPaused {
				t.Fatalf("failed approval continuation did not return control to the operator: %#v %v", paused, err)
			}
			cp, _, err := st.GetSupervisorCheckpoint(t.Context(), run.ID)
			if err != nil || cp.Phase != domain.SupervisorIdle || cp.AttemptID != "" || cp.NextTurn != 3 {
				t.Fatalf("checkpoint was not safely closed: %#v %v", cp, err)
			}
			recovery, found, err := st.GetThreadRunRecovery(t.Context(), input.ThreadID)
			if err != nil || !found || recovery.FailureStage != tc.stage || !recovery.Quiescent ||
				recovery.RunID != run.ID || recovery.HandoffOperationID != result.HandoffID {
				t.Fatalf("closed checkpoint lost exact cause: %#v %v", recovery, err)
			}
			list, err := st.ListRunEvents(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			sealed := ""
			for _, event := range list {
				if event.Type == events.RunExecutionHandoffCompletedEvent && event.SubjectID == result.HandoffID {
					var payload map[string]any
					if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
						t.Fatal(err)
					}
					if payload["failure_stage"] != tc.stage || payload["failure_attempt_id"] == "" ||
						strings.Contains(event.PayloadJSON, "must-not-enter-stage-event") {
						t.Fatalf("invalid sealed category: %s", event.PayloadJSON)
					}
					sealed = event.PayloadJSON
				}
			}
			projection, err := runactivity.Build(run.ID, list, false)
			if err != nil {
				t.Fatal(err)
			}
			noticed := false
			for _, item := range projection.Items {
				noticed = noticed || strings.HasPrefix(item.Detail, "审批已保存") && strings.Contains(item.Detail, tc.notice)
			}
			if sealed == "" || !noticed {
				t.Fatalf("stage or conversation notice missing: %q %+v", sealed, projection.Items)
			}
			again := turns.ResumeApproval(t.Context(), request)
			if !again.Replayed || again.HandoffID != result.HandoffID || len(provider.Requests()) != wantCalls {
				t.Fatalf("replay changed execution: %#v", again)
			}
			after, err := st.ListRunEvents(t.Context(), run.ID)
			if err != nil || len(after) != len(list) {
				t.Fatalf("replay changed history: before=%d after=%d err=%v", len(list), len(after), err)
			}
			stored, err := st.GetFileEdit(t.Context(), edit.ID)
			if err != nil || stored.Status != "applied" {
				t.Fatalf("saved direct apply changed: %#v %v", stored, err)
			}
			savedApproval, appErr := st.GetApprovalByProposal(t.Context(), edit.ID)
			if appErr != nil || savedApproval.Status != "approved" {
				t.Fatalf("saved approval changed: %#v %v", savedApproval, appErr)
			}
			assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)

			// Paused failures remain recoverable, but never across an active
			// execution lease or a different failed handoff identity.
			lease, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{
				RunID: run.ID, OwnerID: "approval-failure-fence", TTL: time.Minute,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _, _ = st.ReleaseRunExecutionLease(context.Background(), lease.Lease) })
			recoverRequest := application.RecoverThreadRunRequest{Version: domain.ThreadRunRecoveryProtocolVersion,
				ThreadID: input.ThreadID, RunID: run.ID, HandoffOperationID: result.HandoffID,
				OperationKey: "recover-paused-approval-failure", RequestedBy: "test_operator"}
			recoveries := application.NewThreadRunRecoveryService(st)
			if _, err := recoveries.Recover(t.Context(), recoverRequest); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("paused failure crossed an active lease: %v", err)
			}
			if _, _, _, err := st.RecoverThreadRunFromFailedHandoff(t.Context(), input.ThreadID, run.ID,
				result.HandoffID, recoverRequest.RequestedBy, recoverRequest.OperationKey); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("store recovery crossed an active lease: %v", err)
			}
			if _, _, err := st.ReleaseRunExecutionLease(t.Context(), lease.Lease); err != nil {
				t.Fatal(err)
			}
			changed := recoverRequest
			changed.HandoffOperationID = "run-handoff-unrelated"
			if _, err := recoveries.Recover(t.Context(), changed); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("paused failure accepted a different handoff: %v", err)
			}
			recovered, err := recoveries.Recover(t.Context(), recoverRequest)
			if err != nil || recovered.FailedRun.Status != domain.RunFailed || recovered.Replayed ||
				len(provider.Requests()) != wantCalls {
				t.Fatalf("exact paused failure recovery changed execution: %#v calls=%d err=%v", recovered, len(provider.Requests()), err)
			}
		})
	}
}

func TestApprovalContinuationFailureCommitsAcceptedMidTurnCorrection(t *testing.T) {
	st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return boundaryPropose("proposal"), nil
		case 2:
			return textResponse(rootActionResponse(domain.RootActionWait, "Review", "", "operator review")), nil
		case 3:
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "Stale completion", "done", "")), nil
		case 4:
			return textResponse(" \t\n"), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}
	turns := toolBoundaryService(st, st, provider)
	if _, err := turns.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	edit := approveBoundaryEdit(t, st, run)
	request := application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID}
	done := make(chan application.ApprovalContinuationResult, 1)
	go func() { done <- turns.ResumeApproval(context.Background(), request) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("approval continuation did not reach provider")
	}
	correction := "Stop further edits; explain the approved proposal before doing anything else"
	accepted, err := st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: correction,
		OperationKey: "midturn-approval-failure-0001", RequestedBy: "test_operator",
		DeliveryMode: domain.OperatorSteeringCurrentTurn,
	})
	if err != nil || accepted.Message.ID == "" {
		t.Fatalf("correction admission=%#v err=%v", accepted, err)
	}
	releaseOnce.Do(func() { close(release) })
	var result application.ApprovalContinuationResult
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("failed continuation did not settle")
	}
	if result.State != "failed" || result.ErrorCode != "FAILED_PRECONDITION" {
		t.Fatalf("continuation=%#v", result)
	}
	requests := provider.Requests()
	if len(requests) != 4 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	correctedRequest := false
	for _, message := range requests[3].Messages {
		correctedRequest = correctedRequest || strings.Contains(message.Content, correction)
	}
	if !correctedRequest {
		t.Fatalf("accepted correction missing from actual continuation: %#v", requests[3].Messages)
	}
	stored, err := st.GetOperatorSteering(t.Context(), accepted.Message.ID)
	if err != nil || stored.Status != domain.OperatorSteeringCommitted || stored.SessionMessageID == 0 {
		t.Fatalf("failed continuation orphaned correction: %#v err=%v", stored, err)
	}
	checkpoint, found, err := st.GetSupervisorCheckpoint(t.Context(), run.ID)
	if err != nil || !found || checkpoint.Phase != domain.SupervisorIdle || checkpoint.AttemptID != "" {
		t.Fatalf("failed continuation did not close attempt: %#v found=%t err=%v", checkpoint, found, err)
	}
	messages, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	correctionIndex, failureIndex := -1, -1
	for i, message := range messages {
		if message.Content == correction {
			correctionIndex = i
		}
		if strings.Contains(message.Content, "This failed continuation did not complete their requested work") {
			failureIndex = i
		}
	}
	if correctionIndex < 0 || failureIndex <= correctionIndex {
		t.Fatalf("correction and failure caveat are not ordered in history: %#v", messages)
	}
	replayed := turns.ResumeApproval(t.Context(), request)
	if !replayed.Replayed || replayed.HandoffID != result.HandoffID || len(provider.Requests()) != 4 {
		t.Fatalf("replay changed failed continuation: %#v calls=%d", replayed, len(provider.Requests()))
	}
}
