package application_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

// Interrupt after durable preparation, before BeginBoundary, using the public
// store contract. This reproduces the old capture failure without changing DB
// rows, running a model endpoint, or writing outside this test's directory.
type beforeFileBoundaryStore struct{ *store.SQLiteStore }

func (s *beforeFileBoundaryStore) PrepareFileEditApply(ctx context.Context, op fileedit.ApplyOperation) (fileedit.ApplyOperation, *fileedit.ApplyResult, bool, error) {
	op, result, replayed, err := s.SQLiteStore.PrepareFileEditApply(ctx, op)
	if err == nil {
		err = apperror.New(apperror.CodeInternal, "fixture: capture blob missing before workspace mutation boundary")
	}
	return op, result, replayed, err
}

type oldBeforeFileBoundaryStore struct{ *beforeFileBoundaryStore }

func (s *oldBeforeFileBoundaryStore) EndFailedThreadTurn(context.Context, string, string, string) (domain.ThreadTurnFailure, bool, error) {
	return domain.ThreadTurnFailure{}, false, nil
}
func (s *oldBeforeFileBoundaryStore) FailSupervisorTurn(_ context.Context, cp domain.SupervisorCheckpoint, _ string, _ time.Duration) (domain.SupervisorCheckpoint, error) {
	return cp, nil // the old invocation returned before retaining its cause
}

func beforeFileBoundaryFixture(t *testing.T) (*store.SQLiteStore, domain.Run, string, application.ExecuteThreadTurnRequest, fileedit.Edit) {
	t.Helper()
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 12, MaxToolCalls: 30})
	input.Content, input.OperationKey = "Propose a new test file and wait for review", "file-boundary-proposal"
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{
		toolResponse("create", "workspace_change", `{"version":"agent-code-tools.v1","action":"create","path":"test/example.mjs","expected_sha256":"missing","content":"export const answer = 42;\n"}`),
		textResponse(rootActionResponse(domain.RootActionWait, "Ready for review", "", "review")),
	}}
	if _, err := toolBoundaryServiceWithPolicy(st, st, p, &fileOperationPolicy{review: true}).Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	edit := approveBoundaryEdit(t, st, run)
	input.Content, input.OperationKey = "Apply the reviewed test file then wait", "file-boundary-apply"
	return st, run, root, input, edit
}
func beforeBoundaryApply(edit fileedit.Edit) *llm.ChatResponse {
	return toolResponse("apply-create", "workspace_apply", fmt.Sprintf(`{"version":"agent-code-tools.v1","edit_id":%q,"expected_action":"create","expected_original_sha256":%q,"expected_proposed_sha256":%q}`, edit.ID, edit.OriginalHash, edit.ProposedHash))
}
func requireBoundaryFileAbsent(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "test", "example.mjs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected write: %v", err)
	}
}

func TestHistoricalFileApplyBoundaryFailureLetsNewMessageCancelThenExplicitlyRetry(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(fmt.Sprintf("original_confirmation_first_%t", confirm), func(t *testing.T) {
			verifyHistoricalFileApplyContinuation(t, confirm)
		})
	}
}

func verifyHistoricalFileApplyContinuation(t *testing.T, confirm bool) {
	st, run, root, input, edit := beforeFileBoundaryFixture(t)
	oldStore := &oldBeforeFileBoundaryStore{&beforeFileBoundaryStore{st}}
	oldProvider := &scriptedToolProvider{responses: []*llm.ChatResponse{beforeBoundaryApply(edit)}}
	first, err := toolBoundaryService(oldStore, oldStore, oldProvider).Execute(t.Context(), input)
	if err == nil || first.Execution == nil || first.Execution.Handoff.Result.ErrorCode != "internal" {
		t.Fatalf("old failure=%#v %v", first, err)
	}
	original := first.Execution.Handoff
	requireBoundaryFileAbsent(t, root)
	if _, ready, err := st.GetFailedFileApplyOperationKey(t.Context(), run.ID, edit.ID, beforeBoundaryRootAgent(t, st, run.ID)); err != nil || ready {
		t.Fatalf("unsealed failure key exposed: %v %v", ready, err)
	}

	p := &boundaryJourneyProvider{}
	p.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			requireBoundaryFileAbsent(t, root)
			text := ""
			for _, m := range request.Messages {
				text += m.Content + "\n"
			}
			if !strings.Contains(text, "Do not apply") || !strings.Contains(text, edit.ID) || !strings.Contains(text, edit.ProposedHash) ||
				!strings.Contains(text, "Recorded unstarted apply expectations (no execution authority)") || !hasToolSpec(request, "history_read") {
				t.Fatal("new message or bounded exact failed-apply evidence missing")
			}
			rounds, err := st.ListRunSupervisorToolRoundsPage(ctx, run.ID, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, round := range rounds {
				for _, call := range round.Calls {
					if call.ToolName != "workspace_apply" || call.Status != domain.SupervisorToolFailed {
						continue
					}
					ref := domain.SupervisorToolResultReference(call)
					if !strings.Contains(text, ref.SourceID) || !strings.Contains(text, ref.ExpectedSHA256) ||
						!strings.Contains(call.ResultJSON, "original detailed error was not retained") {
						t.Fatal("failed-apply projection lost its exact sealed result reference")
					}
					return toolResponse("read-exact-failed-apply", "history_read", historyRecallReadJSON(ref)), nil
				}
			}
			t.Fatal("failed apply was not sealed before the new request")
			return nil, nil
		case 2:
			requireBoundaryFileAbsent(t, root)
			if !hasToolResult(request, "original detailed error was not retained") || !hasToolResult(request, edit.ID) ||
				!hasToolResult(request, edit.ProposedHash) {
				t.Fatal("exact history_read did not recover the failed-apply detail")
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "No write was requested or performed", "left unchanged", "")), nil
		case 3:
			return beforeBoundaryApply(edit), nil
		default:
			if !hasToolResult(request, `\"file_written\":true`) {
				t.Fatal("explicit retry lost actual write result")
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "Applied the original reviewed file", "done", "")), nil
		}
	}
	current := toolBoundaryService(st, st, p)
	// Original key confirms and seals the observed failure without executing.
	if confirm {
		if _, err := current.Execute(t.Context(), input); err == nil || len(p.Requests()) != 0 {
			t.Fatalf("original confirmation executed: %v", err)
		}
	}
	requireBoundaryFileAbsent(t, root)
	next := input
	next.Content, next.OperationKey = "Do not apply the file; leave the proposal available", "cancel-file-boundary-apply"
	if _, err := current.Execute(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	requireBoundaryFileAbsent(t, root)
	proposal, err := st.GetFileEdit(t.Context(), edit.ID)
	if err != nil || proposal.Status != fileedit.StatusApproved {
		t.Fatalf("proposal consumed by tool failure: %#v %v", proposal, err)
	}
	if _, ready, err := st.GetFailedFileApplyOperationKey(t.Context(), run.ID, "different-edit", beforeBoundaryRootAgent(t, st, run.ID)); err != nil || ready {
		t.Fatalf("unmatched edit adopted: %v %v", ready, err)
	}
	if _, ready, err := st.GetFailedFileApplyOperationKey(t.Context(), run.ID, edit.ID, "different-agent"); err != nil || ready {
		t.Fatalf("unmatched actor adopted: %v %v", ready, err)
	}
	next.Content, next.OperationKey = "Now apply only the same reviewed file, keeping its exact hash", "explicit-file-boundary-retry"
	// A fresh facade models reconnect/restart; no transient map carries authority.
	result, err := toolBoundaryService(st, st, p).Execute(t.Context(), next)
	if err != nil || result.Submission.Message.Status != domain.OperatorSteeringCommitted || len(p.Requests()) != 4 {
		t.Fatalf("explicit retry=%#v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "test", "example.mjs"))
	if err != nil || fileedit.HashText(string(data)) != edit.ProposedHash {
		t.Fatalf("wrong result %q %v", data, err)
	}
	latest, found, err := st.GetRunExecutionHandoff(t.Context(), original.Operation.KeyDigest)
	if err != nil || !found || !reflect.DeepEqual(latest, original) {
		t.Fatal("old failure was rewritten")
	}
	if _, err := current.Execute(t.Context(), input); err == nil || len(p.Requests()) != 4 {
		t.Fatal("old key replayed after explicit retry")
	}
	if _, err := toolBoundaryService(st, st, p).Execute(t.Context(), next); err != nil || len(p.Requests()) != 4 {
		t.Fatal("new key replay executed the mutation twice")
	}
}

func TestFileApplyBoundaryFailureRetainsActualCause(t *testing.T) {
	st, run, root, input, edit := beforeFileBoundaryFixture(t)
	failedStore := &beforeFileBoundaryStore{st}
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{beforeBoundaryApply(edit)}}
	result, err := toolBoundaryService(failedStore, failedStore, p).Execute(t.Context(), input)
	if err == nil || result.Execution == nil {
		t.Fatalf("failure missing: %#v %v", result, err)
	}
	sealed, found, err := st.GetThreadTurnFailure(t.Context(), run.ID, result.Submission.Message.ID)
	if err != nil || !found || sealed.ErrorCode != "INTERNAL" {
		t.Fatalf("new failure not sealed: %#v %v", sealed, err)
	}
	rounds, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, round := range rounds {
		for _, call := range round.Calls {
			seen = seen || call.ToolName == "workspace_apply" && call.Status == domain.SupervisorToolFailed && strings.Contains(call.ResultJSON, "capture blob missing") && strings.Contains(call.ResultJSON, `"original_error_detail_available":"true"`)
		}
	}
	if !seen {
		t.Fatal("new exact error cause was lost")
	}
	requireBoundaryFileAbsent(t, root)
}

func TestFileApplyBoundaryFailureDoesNotSettleExistingMutationBoundary(t *testing.T) {
	st, run, root, input, edit := beforeFileBoundaryFixture(t)
	legacy := &oldBeforeFileBoundaryStore{&beforeFileBoundaryStore{st}}
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{beforeBoundaryApply(edit)}}
	first, err := toolBoundaryService(legacy, legacy, p).Execute(t.Context(), input)
	if err == nil || first.Execution == nil {
		t.Fatalf("old failure missing: %v", err)
	}
	checkpointService, err := application.NewWorkspaceCheckpointService(st, domain.ExecutionPermissionRuntimeCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkpointService.BeginBoundary(t.Context(), application.WorkspaceMutationBoundaryRequest{RunID: run.ID,
		Kind: workspacecheckpoint.TransactionFileTool, OperationKey: "already-started-boundary", TriggerReceiptID: edit.ID}); err != nil {
		t.Fatal(err)
	}
	if _, sealed, err := st.EndFailedThreadTurn(t.Context(), input.ThreadID, run.ID, first.Execution.Handoff.Operation.ID); err == nil || sealed {
		t.Fatalf("existing mutation boundary abandoned: %v %v", sealed, err)
	}
	if _, found, err := st.GetFailedFileApplyOperationKey(t.Context(), run.ID, edit.ID, beforeBoundaryRootAgent(t, st, run.ID)); err != nil || found {
		t.Fatalf("existing boundary adopted: %v %v", found, err)
	}
	requireBoundaryFileAbsent(t, root)
}

func beforeBoundaryRootAgent(t *testing.T, st *store.SQLiteStore, runID string) string {
	t.Helper()
	agent, found, err := st.GetRootAgent(t.Context(), runID)
	if err != nil || !found {
		t.Fatalf("root agent unavailable: %v", err)
	}
	return agent.ID
}
