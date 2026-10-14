package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func TestWebAccessSelectionRevokesEarlierThreadFetchGrant(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "web-fetch-mode-revocation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	_, run := createStructuredToolTestRun(t, ctx, state, "approve one public HTTPS fetch")
	if _, err := application.NewRunService(state).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	lease := acquireTestRunExecutionLease(t, ctx, state, run.ID)
	turn, err := state.BeginSupervisorTurn(ctx, lease, "fetch public evidence")
	if err != nil {
		t.Fatal(err)
	}
	started := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 3,
		Provider: "test", Model: "model"}
	if inserted, err := state.RecordSupervisorModelStarted(ctx, turn.Checkpoint, started); err != nil || !inserted {
		t.Fatalf("record model start: inserted=%t err=%v", inserted, err)
	}
	canonicalURL := "https://docs.example.com/reference"
	payload := json.RawMessage(`{"version":"web_fetch.v1","url":"` + canonicalURL + `"}`)
	operationKey := runmutation.SupervisorToolOperationKey(run.ID,
		turn.Checkpoint.NextTurn, "web_fetch", string(payload))
	callID, err := runmutation.SupervisorToolCallID(operationKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	secondURL := "https://papers.example.org/latest"
	secondPayload := json.RawMessage(`{"version":"web_fetch.v1","url":"` + secondURL + `"}`)
	secondOperationKey := runmutation.SupervisorToolOperationKey(run.ID,
		turn.Checkpoint.NextTurn, "web_fetch", string(secondPayload))
	secondCallID, err := runmutation.SupervisorToolCallID(secondOperationKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	completed := started
	completed.Outcome = llm.OutcomeSuccess
	root, found, err := state.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root agent missing: found=%t err=%v", found, err)
	}
	authority, err := toolgateway.NewWebEvidenceCallAuthority(
		toolgateway.WebEvidenceCapabilityContext{RunID: run.ID, MissionID: run.MissionID,
			SessionID: run.SessionID, RootAgentID: root.ID, WorkspaceID: "ws-structured",
			Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver,
			Role: domain.AgentRoleRoot, Profile: domain.ProfileCode,
			PermissionMode:     domain.RunExecutionPermissionConservative,
			PermissionRevision: 1, ModeRevision: 1, NetworkMode: "allowlist",
			AllowedTargets: []string{"search.example.com"}, ProviderAvailable: true,
			ProviderFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	encodedAuthority, err := toolgateway.EncodeWebEvidenceCallAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := state.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, completed,
		llm.ChatResponse{Provider: "test", Model: "model",
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			ToolCalls: []llm.ToolCall{
				{ID: callID, Name: "web_fetch", Arguments: payload,
					Authority: encodedAuthority},
				{ID: secondCallID, Name: "web_fetch", Arguments: secondPayload,
					Authority: encodedAuthority},
			}})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.NextTurn != turn.Checkpoint.NextTurn {
		t.Fatalf("checkpoint turn changed: got=%d want=%d", checkpoint.NextTurn,
			turn.Checkpoint.NextTurn)
	}
	fingerprint := approval.Fingerprint(domain.WebFetchAuthorizationProtocolVersion,
		run.ID, run.SessionID, callID, canonicalURL, "docs.example.com")
	value, authorized, err := state.PrepareWebFetchAuthorization(ctx,
		domain.WebFetchAuthorizationRequest{ID: "web-fetch-authorization-1",
			RunID: run.ID, MissionID: run.MissionID, SessionID: run.SessionID,
			WorkspaceID: "ws-structured", SupervisorTurn: checkpoint.NextTurn,
			SupervisorToolCallID: callID, CanonicalURL: canonicalURL,
			ExactTarget: "docs.example.com", RequestFingerprint: fingerprint,
			RequestedBy: "run_supervisor"})
	if err != nil || authorized || value.Status != domain.WebFetchAuthorizationPending {
		t.Fatalf("prepare authorization: value=%+v authorized=%t err=%v", value, authorized, err)
	}
	if _, _, err := state.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	waiting, err := state.GetRun(ctx, run.ID)
	if err != nil || waiting.Status != domain.RunWaitingApproval {
		t.Fatalf("Run did not enter waiting_approval: run=%+v err=%v", waiting, err)
	}
	replay, replayAuthorized, err := state.PrepareWebFetchAuthorization(ctx,
		domain.WebFetchAuthorizationRequest{ID: "ignored-replay-id", RunID: run.ID,
			MissionID: run.MissionID, SessionID: run.SessionID, WorkspaceID: "ws-structured",
			SupervisorTurn: checkpoint.NextTurn, SupervisorToolCallID: callID,
			CanonicalURL: canonicalURL, ExactTarget: "docs.example.com",
			RequestFingerprint: fingerprint, RequestedBy: "run_supervisor"})
	if err != nil || replayAuthorized || replay.ID != value.ID || replay.ApprovalID != value.ApprovalID {
		t.Fatalf("prepare replay: value=%+v authorized=%t err=%v", replay, replayAuthorized, err)
	}

	decided, _, err := state.DecideWebFetchAuthorization(ctx, value.ID, domain.WebFetchAuthorizationThread, true, "approve-thread-before-web-mode", "operator", "")
	if err != nil {
		t.Fatal(err)
	}
	thread, err := state.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := getThreadWebFetchGrantTx(ctx, state.db, thread.ID, decided.ExactTarget); err != nil || !found {
		t.Fatalf("old grant absent before selection: found=%t err=%v", found, err)
	}
	if _, _, err := state.ResumeWebFetchAuthorizationRun(ctx, decided.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(state).Pause(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunWebAccessService(state).Change(ctx, application.ChangeRunWebAccessRequest{Version: application.RunWebAccessControlProtocolVersion, RunID: run.ID, ExpectedModeRevision: 1, WebAccessMode: "rules", OperationKey: "revoke-old-thread-web-grant"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := getThreadWebFetchGrantTx(ctx, state.db, thread.ID, decided.ExactTarget); err != nil || found {
		t.Fatalf("old Thread grant survived new selection: found=%t err=%v", found, err)
	}
	preserved, err := state.GetWebFetchAuthorization(ctx, decided.ID)
	if err != nil || preserved.Status != domain.WebFetchAuthorizationApproved {
		t.Fatalf("historical grant changed: %+v err=%v", preserved, err)
	}
}
