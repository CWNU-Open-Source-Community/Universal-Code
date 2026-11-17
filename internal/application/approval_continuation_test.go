package application_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
)

func TestApprovalContinuationFileDecisionReturnsToOriginalTask(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(fmt.Sprintf("approve_%t", approve), func(t *testing.T) {
			st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
			var edit fileedit.Edit
			provider := &boundaryJourneyProvider{}
			provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				switch index {
				case 1:
					return boundaryPropose("proposal"), nil
				case 2:
					return textResponse(rootActionResponse(domain.RootActionWait, "Await operator review", "", "operator review required")), nil
				case 3:
					var all strings.Builder
					for _, message := range request.Messages {
						all.WriteString(message.Content)
					}
					for _, exact := range []string{input.Content, edit.ID, "System-recorded operator review results"} {
						if !strings.Contains(all.String(), exact) {
							t.Fatalf("continuation omitted %q", exact)
						}
					}
					if approve {
						for _, exact := range []string{"System-recorded direct file apply result after operator approval", "APPLIED"} {
							if !strings.Contains(all.String(), exact) {
								t.Fatalf("approved continuation omitted %q", exact)
							}
						}
						return textResponse(rootActionResponse(domain.RootActionFinish, "Applied the approved edit", "done", "")), nil
					}
					return textResponse(rootActionResponse(domain.RootActionFinish, "The proposal was denied; no file was changed", "done", "")), nil
				default:
					return nil, fmt.Errorf("unexpected model call %d", index)
				}
			}
			turns := toolBoundaryServiceWithPolicy(st, st, provider, &fileOperationPolicy{review: true})
			if _, err := turns.Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			edits, err := st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
			if err != nil || len(edits) != 1 || edits[0].Status != fileedit.StatusProposed {
				t.Fatalf("edits=%#v err=%v", edits, err)
			}
			edit = edits[0]
			action := application.FileEditDeny
			if approve {
				action = application.FileEditApproveIntent
			}
			reviewRequest := application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID, Action: action}
			review, err := application.NewFileEditReviewService(st).Review(t.Context(), reviewRequest)
			if err != nil {
				t.Fatal(err)
			}
			resumeRequest := application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID}
			result := turns.ResumeApproval(t.Context(), resumeRequest)
			if result.State != "completed" || !result.ModelCalled || result.ToolCalled || result.Replayed {
				_, _, diagnostic := st.PrepareApprovalContinuation(t.Context(), run.ID, "file_edit", edit.ID)
				t.Logf("preparation diagnostic: %v", diagnostic)
				t.Fatalf("continuation=%#v", result)
			}
			body, err := os.ReadFile(filepath.Join(root, "README.md"))
			want := "original text\n"
			if approve {
				want = "reviewed text\n"
			}
			if err != nil || string(body) != want {
				t.Fatalf("file=%q err=%v", body, err)
			}
			before := len(provider.Requests())
			replay, err := application.NewFileEditReviewService(st).Review(t.Context(), reviewRequest)
			if err != nil || !replay.Replayed || replay.Edit.Status != review.Edit.Status && (!approve || replay.Edit.Status != fileedit.StatusApplied) {
				t.Fatalf("sealed review replay=%#v err=%v", replay, err)
			}
			again := turns.ResumeApproval(t.Context(), resumeRequest)
			if again.State != "completed" || !again.Replayed || again.HandoffID != result.HandoffID || len(provider.Requests()) != before {
				t.Fatalf("duplicate continuation=%#v", again)
			}
			assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
			current, err := st.GetRun(t.Context(), run.ID)
			if err != nil || current.Status != domain.RunRunning {
				t.Fatalf("interactive finish run=%#v err=%v", current, err)
			}
		})
	}
}

func TestApprovalContinuationChainedReviewsReuseUserWithoutReplayingWrites(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	var first, second fileedit.Edit
	p := &boundaryJourneyProvider{}
	p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return boundaryPropose("first-proposal"), nil
		case 2, 4:
			return textResponse(rootActionResponse(domain.RootActionWait, "Review next file change", "", "operator review required")), nil
		case 3:
			return toolResponse("second-proposal", "workspace_change", fmt.Sprintf(`{"version":"agent-code-tools.v1","action":"propose_patch","path":"README.md","expected_sha256":%q,"replacements":[{"old_text":"reviewed text","new_text":"final text","expected_occurrences":1}]}`, fileedit.HashText("reviewed text\n"))), nil
		case 5:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Both separately approved edits applied", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true})
	if _, err := turns.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	first = approveBoundaryEdit(t, st, run)
	firstRequest := application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: first.ID}
	r1 := turns.ResumeApproval(t.Context(), firstRequest)
	if r1.State != "completed" {
		t.Fatalf("first continuation=%#v", r1)
	}
	edits, err := st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
	if err != nil || len(edits) != 2 {
		t.Fatalf("edits=%#v %v", edits, err)
	}
	for _, edit := range edits {
		if edit.ID != first.ID {
			second = edit
		}
	}
	// Reconfirm the first approval during a new wait; never consume the second.
	if replay := turns.ResumeApproval(t.Context(), firstRequest); !replay.Replayed || len(p.Requests()) != 4 {
		t.Fatalf("old review woke new wait: %#v", replay)
	}
	if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: second.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	r2 := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: second.ID})
	if r2.State != "completed" || r2.HandoffID == r1.HandoffID || len(p.Requests()) != 5 {
		t.Fatalf("second continuation=%#v calls=%d", r2, len(p.Requests()))
	}
	body, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(body) != "final text\n" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
}

func TestApprovalContinuationStopPreventsLateApprovalRevival(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	started := make(chan struct{})
	p := &boundaryJourneyProvider{}
	p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return boundaryPropose("proposal"), nil
		case 2:
			return textResponse(rootActionResponse(domain.RootActionWait, "Review", "", "operator review")), nil
		case 3:
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		case 4:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Stopped turn settled; following the new read-only request", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true})
	if _, err := turns.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	edit := approveBoundaryEdit(t, st, run)
	request := application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID}
	done := make(chan application.ApprovalContinuationResult, 1)
	go func() { done <- turns.ResumeApproval(t.Context(), request) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("continuation did not start")
	}
	state, err := turns.ExecutionState(t.Context(), input.ThreadID)
	if err != nil || state.State != "running" {
		t.Fatalf("state=%#v %v", state, err)
	}
	if _, err := turns.Interrupt(t.Context(), input.ThreadID, state.ExecutionID); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.State != "failed" || result.ErrorCode != "CANCELLED" {
			t.Fatalf("stopped=%#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not settle")
	}
	if replay := turns.ResumeApproval(t.Context(), request); replay.State != "failed" || !replay.Replayed || len(p.Requests()) != 3 {
		t.Fatalf("late approval=%#v", replay)
	}
	body, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(body) != "reviewed text\n" {
		t.Fatalf("direct apply wrote=%q", body)
	}
	next := input
	next.OperationKey = "after-stopped-review"
	next.Content = "Only explain current state."
	if _, err := turns.Execute(t.Context(), next); err != nil {
		t.Fatalf("new request blocked after safely closed continuation: %v", err)
	}
	if len(p.Requests()) != 4 {
		t.Fatalf("new user request calls=%d", len(p.Requests()))
	}
}

func TestApprovalContinuationDecisionDuringModelWaitUsesExistingOwner(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	waiting, release := make(chan struct{}), make(chan struct{})
	var edit fileedit.Edit
	p := &boundaryJourneyProvider{}
	p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return boundaryPropose("proposal"), nil
		case 2:
			close(waiting)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return textResponse(rootActionResponse(domain.RootActionWait, "Review", "", "operator review")), nil
		case 3:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Applied exact approved edit", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true})
	done := make(chan error, 1)
	go func() { _, err := turns.Execute(t.Context(), input); done <- err }()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("wait response not started")
	}
	edit = approveBoundaryEdit(t, st, run)
	request := application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID}
	if got := turns.ResumeApproval(t.Context(), request); got.State != "queued" {
		t.Fatalf("early review=%#v", got)
	}
	if got := turns.ResumeApproval(t.Context(), request); got.State != "queued" || !got.Replayed {
		t.Fatalf("duplicate queued review=%#v", got)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("existing owner failed to drain approval")
	}
	body, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(body) != "reviewed text\n" || len(p.Requests()) != 3 {
		t.Fatalf("body=%q calls=%d", body, len(p.Requests()))
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
}

func TestApprovalContinuationPreservesInputAcrossFourToolRoundBoundary(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
	var edit fileedit.Edit
	p := &boundaryJourneyProvider{}
	p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index == 1:
			return boundaryPropose("proposal"), nil
		case index == 2:
			return textResponse(rootActionResponse(domain.RootActionWait, "Review", "", "operator review")), nil
		case index >= 3 && index <= domain.MaxSupervisorToolRounds+2:
			return boundaryRead(fmt.Sprintf("read-%d", index), 1), nil
		case index == domain.MaxSupervisorToolRounds+3:
			assertBoundaryPrompt(t, request)
			return textResponse(rootActionResponse(domain.RootActionContinue, "One final read remains", "", "")), nil
		case index == domain.MaxSupervisorToolRounds+4:
			if strings.TrimSpace(boundaryContextText(t, request)) == "" {
				t.Fatal("next segment lost preceding tool-boundary evidence")
			}
			var all strings.Builder
			for _, message := range request.Messages {
				all.WriteString(message.Content)
			}
			if !strings.Contains(all.String(), edit.ID) || !strings.Contains(all.String(), "APPLIED") {
				t.Fatal("next segment lost actual completed direct apply evidence")
			}
			return boundaryRead("final-tool", 1), nil
		case index == domain.MaxSupervisorToolRounds+5:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Finished the approved task after actual final read", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true})
	if _, err := turns.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	edit = approveBoundaryEdit(t, st, run)
	result := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID})
	if result.State != "completed" || len(p.Requests()) != domain.MaxSupervisorToolRounds+5 {
		t.Fatalf("boundary stopped: %#v calls=%d", result, len(p.Requests()))
	}
	body, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(body) != "reviewed text\n" {
		t.Fatalf("file=%q", body)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
	cp, found, err := st.GetSupervisorCheckpoint(t.Context(), run.ID)
	if err != nil || !found || cp.Phase != domain.SupervisorIdle || cp.NextTurn != 4 {
		t.Fatalf("checkpoint=%#v %v", cp, err)
	}
}

func TestApprovalContinuationProposalBeforeBoundaryWaitsForExactLaterSegment(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 9, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 15)})
	var first, second fileedit.Edit
	p := &boundaryJourneyProvider{}
	p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index == 1:
			return boundaryPropose("first-proposal"), nil
		case index == 2, index == domain.MaxSupervisorToolRounds+5:
			return textResponse(rootActionResponse(domain.RootActionWait, "Review next exact edit", "", "operator review")), nil
		case index == 3:
			return toolResponse("second-proposal", "workspace_change", fmt.Sprintf(`{"version":"agent-code-tools.v1","action":"propose_patch","path":"README.md","expected_sha256":%q,"replacements":[{"old_text":"reviewed text","new_text":"final text","expected_occurrences":1}]}`, fileedit.HashText("reviewed text\n"))), nil
		case index >= 4 && index <= domain.MaxSupervisorToolRounds+2:
			return boundaryRead(fmt.Sprintf("read-%d", index), 1), nil
		case index == domain.MaxSupervisorToolRounds+3:
			return textResponse(rootActionResponse(domain.RootActionContinue, "Inspect once more before waiting", "", "")), nil
		case index == domain.MaxSupervisorToolRounds+4:
			return boundaryRead("read-after-boundary", 1), nil
		case index == domain.MaxSupervisorToolRounds+6:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Both reviewed edits complete", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true})
	if _, err := turns.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	first = approveBoundaryEdit(t, st, run)
	if result := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: first.ID}); result.State != "completed" || len(p.Requests()) != domain.MaxSupervisorToolRounds+5 {
		t.Fatalf("first=%#v calls=%d", result, len(p.Requests()))
	}
	edits, err := st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range edits {
		if edit.ID != first.ID {
			second = edit
		}
	}
	if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: second.ID, Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	if result := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: second.ID}); result.State != "completed" || len(p.Requests()) != domain.MaxSupervisorToolRounds+6 {
		t.Fatalf("second=%#v calls=%d", result, len(p.Requests()))
	}
	body, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(body) != "final text\n" {
		t.Fatalf("file=%q", body)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
}

func TestApprovalContinuationDoesNotWakeManualReviewOrOverrideNewInput(t *testing.T) {
	for _, manual := range []bool{true, false} {
		t.Run(fmt.Sprintf("manual_%t", manual), func(t *testing.T) {
			st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
			p := &boundaryJourneyProvider{}
			p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				if !manual && index == 1 {
					return boundaryPropose("proposal"), nil
				}
				if (manual && index == 1) || (!manual && index == 2) {
					return textResponse(rootActionResponse(domain.RootActionWait, "Waiting for operator input", "", "operator input required")), nil
				}
				return nil, fmt.Errorf("unrelated approval started model call %d", index)
			}
			turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true})
			if _, err := turns.Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			if manual {
				if _, err := fileedit.NewManager(st).Propose(t.Context(), fileedit.Proposal{SessionID: run.SessionID, WorkspaceID: "ws-tool-boundary", WorkspaceRoot: root, Path: "README.md", ProposedText: "reviewed text\n"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := application.NewThreadService(st).Submit(t.Context(), application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: input.ThreadID, Content: "Do not apply that edit; inspect the original file instead", OperationKey: "superseding-operator-input", RequestedBy: "test_operator"}); err != nil {
					t.Fatal(err)
				}
			}
			edit := approveBoundaryEdit(t, st, run)
			count := len(p.Requests())
			result := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID})
			if result.State != "not_started" || len(p.Requests()) != count {
				t.Fatalf("review took over new/manual input: %#v", result)
			}
			body, _ := os.ReadFile(filepath.Join(root, "README.md"))
			if string(body) != "original text\n" {
				t.Fatalf("unrelated approval wrote file %q", body)
			}
		})
	}
}

func TestApprovalContinuationDeletesDirectlyOnOperatorApproval(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	var edit fileedit.Edit
	p := &boundaryJourneyProvider{}
	p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return toolResponse("delete-proposal", "workspace_delete", fmt.Sprintf(`{"version":"agent-code-tools.v1","action":"propose","path":"README.md","confirm_path":"README.md","expected_sha256":%q}`, fileedit.HashText("original text\n"))), nil
		case 2:
			return textResponse(rootActionResponse(domain.RootActionWait, "Await operator confirmation to delete README.md", "", "delete confirmation required")), nil
		case 3:
			var all strings.Builder
			for _, message := range request.Messages {
				all.WriteString(message.Content)
			}
			for _, exact := range []string{"System-recorded direct file apply result after operator approval", edit.ID, "APPLIED"} {
				if !strings.Contains(all.String(), exact) {
					t.Fatalf("delete continuation omitted %q", exact)
				}
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "Confirmed README.md deleted directly on approval", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true, reviewDelete: true})
	if _, err := turns.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	edit = approveBoundaryEdit(t, st, run)
	result := turns.ResumeApproval(t.Context(), application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID})
	if result.State != "completed" || !result.ModelCalled || result.ToolCalled || result.Replayed {
		t.Fatalf("delete continuation=%#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("approved delete left README.md behind: %v", err)
	}
	updated, err := st.GetFileEdit(t.Context(), edit.ID)
	if err != nil || updated.Status != fileedit.StatusApplied {
		t.Fatalf("delete edit=%#v err=%v", updated, err)
	}
}

func TestApprovalContinuationRecordsCASConflictWithoutAbortingTurn(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	var edit fileedit.Edit
	p := &boundaryJourneyProvider{}
	p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return boundaryPropose("proposal"), nil
		case 2:
			return textResponse(rootActionResponse(domain.RootActionWait, "Await operator review", "", "operator review required")), nil
		case 3:
			var all strings.Builder
			for _, message := range request.Messages {
				all.WriteString(message.Content)
			}
			for _, exact := range []string{"System-recorded direct file apply result after operator approval", edit.ID, "FAILED"} {
				if !strings.Contains(all.String(), exact) {
					t.Fatalf("CAS conflict continuation omitted %q", exact)
				}
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "Reported CAS conflict cleanly without overwriting disk", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	turns := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true})
	if _, err := turns.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	edit = approveBoundaryEdit(t, st, run)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("external conflict\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := application.ApprovalContinuationRequest{RunID: run.ID, Kind: "file_edit", ProposalID: edit.ID}
	result := turns.ResumeApproval(t.Context(), req)
	if result.State != "completed" || !result.ModelCalled || result.ToolCalled || result.Replayed {
		t.Fatalf("CAS conflict continuation=%#v", result)
	}
	failedEdit, err := st.GetFileEdit(t.Context(), edit.ID)
	if err != nil || failedEdit.Status != fileedit.StatusFailed || strings.TrimSpace(failedEdit.Reason) == "" {
		t.Fatalf("CAS conflict did not transition edit to failed with reason: edit=%#v err=%v", failedEdit, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(body) != "external conflict\n" {
		t.Fatalf("CAS conflict overwrote external file: body=%q err=%v", body, err)
	}
	replay := turns.ResumeApproval(t.Context(), req)
	if replay.State != "completed" || !replay.Replayed || len(p.Requests()) != 3 {
		t.Fatalf("CAS conflict replay=%#v calls=%d", replay, len(p.Requests()))
	}
	msgs, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	evidenceCount := 0
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "System-recorded direct file apply result after operator approval") {
			evidenceCount++
		}
	}
	if evidenceCount != 1 {
		t.Fatalf("expected 1 idempotent direct-apply evidence message, got %d", evidenceCount)
	}
}

