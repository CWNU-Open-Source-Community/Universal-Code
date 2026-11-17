package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func TestToolBoundaryContextCallsSupportsMoreThan32Calls(t *testing.T) {
	ctx := context.Background()
	st, run := newThreadTaskTest(t)
	input := queueThreadTask(t, st, run, "more-than-32")
	_, lease := prepareThreadTask(t, st, run, 1)
	cp := beginThreadTask(t, st, lease, input)

	// Record 6 rounds of 8 calls each = 48 completed calls (> 32 old cap).
	const rounds = 6
	for r := 0; r < rounds; r++ {
		attempt := startToolRepairModel(t, st, cp, 0, r)
		attempt.Outcome = llm.OutcomeSuccess
		calls := make([]llm.ToolCall, 0, domain.MaxSupervisorToolCallsPerRound)
		for pos := 1; pos <= domain.MaxSupervisorToolCallsPerRound; pos++ {
			raw := fmt.Sprintf(`{"title":"r%d-p%d","content":"note-%d-%d"}`, r+1, pos, r+1, pos)
			payload, err := toolgateway.NormalizeStructuredMemoryPayload(toolgateway.NoteCreateTool, json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			key := runmutation.SupervisorToolOperationKey(cp.RunID, cp.NextTurn, "note_create", string(payload))
			callID, err := runmutation.SupervisorToolCallID(key, r+1)
			if err != nil {
				t.Fatal(err)
			}
			calls = append(calls, llm.ToolCall{ID: callID, Name: "note_create", Arguments: payload})
		}
		var err error
		cp, err = st.RecordSupervisorModelCompleted(ctx, cp, attempt, llm.ChatResponse{
			Provider: "test", Model: "model",
			Usage:     llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			ToolCalls: calls,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls {
			if _, err := st.RecordSupervisorToolExecutionStarted(ctx, cp, call.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.RecordSupervisorToolResult(ctx, cp, domain.SupervisorToolResult{
				CallID: call.ID, Status: domain.SupervisorToolCompleted, ResultJSON: `{"status":"completed"}`, CompletedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	endAttempt := startToolRepairModel(t, st, cp, 0, rounds)
	endAttempt.Outcome = llm.OutcomeSuccess
	resp := llm.ChatResponse{Text: "continue", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	var err error
	cp, err = st.RecordSupervisorModelCompleted(ctx, cp, endAttempt, resp)
	if err != nil {
		t.Fatal(err)
	}
	action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "continue"}
	_, nextCP, _, err := st.CompleteSupervisorToolBoundary(ctx, cp, resp, action, policy.Decision{Allowed: true}, 0)
	if err != nil {
		t.Fatalf("CompleteSupervisorToolBoundary early rollover failed: %v", err)
	}

	got, err := st.ToolBoundaryContextCalls(ctx, nextCP)
	if err != nil {
		t.Fatal(err)
	}
	want := rounds * domain.MaxSupervisorToolCallsPerRound
	if len(got) != want {
		t.Fatalf("ToolBoundaryContextCalls returned %d calls, want %d (> 32)", len(got), want)
	}
}

func TestPrepareSupervisorToolBoundaryEarlyRolloverPreconditions(t *testing.T) {
	ctx := context.Background()

	t.Run("rejects_zero_rounds", func(t *testing.T) {
		st, run := newThreadTaskTest(t)
		input := queueThreadTask(t, st, run, "zero-rounds")
		_, lease := prepareThreadTask(t, st, run, 1)
		cp := beginThreadTask(t, st, lease, input)

		attempt := startToolRepairModel(t, st, cp, 0, 0)
		attempt.Outcome = llm.OutcomeSuccess
		resp := llm.ChatResponse{Text: "continue", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
		var err error
		cp, err = st.RecordSupervisorModelCompleted(ctx, cp, attempt, resp)
		if err != nil {
			t.Fatal(err)
		}
		action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "continue"}
		if _, _, _, err := st.CompleteSupervisorToolBoundary(ctx, cp, resp, action, policy.Decision{Allowed: true}, 0); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			t.Fatalf("zero-round boundary err=%v, want CodeFailedPrecondition", err)
		}
	})

	t.Run("allows_early_rollover_after_one_round", func(t *testing.T) {
		st, run := newThreadTaskTest(t)
		input := queueThreadTask(t, st, run, "early-one-round")
		_, lease := prepareThreadTask(t, st, run, 1)
		cp := beginThreadTask(t, st, lease, input)

		attempt := startToolRepairModel(t, st, cp, 0, 0)
		attempt.Outcome = llm.OutcomeSuccess
		call := toolRepairNote(t, cp)
		var err error
		cp, err = st.RecordSupervisorModelCompleted(ctx, cp, attempt, llm.ChatResponse{
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, ToolCalls: []llm.ToolCall{call},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.RecordSupervisorToolExecutionStarted(ctx, cp, call.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.RecordSupervisorToolResult(ctx, cp, domain.SupervisorToolResult{
			CallID: call.ID, Status: domain.SupervisorToolCompleted, ResultJSON: `{"status":"completed"}`, CompletedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		endAttempt := startToolRepairModel(t, st, cp, 0, 1)
		endAttempt.Outcome = llm.OutcomeSuccess
		resp := llm.ChatResponse{Text: "continue", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
		cp, err = st.RecordSupervisorModelCompleted(ctx, cp, endAttempt, resp)
		if err != nil {
			t.Fatal(err)
		}
		action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "continue"}
		_, nextCP, _, err := st.CompleteSupervisorToolBoundary(ctx, cp, resp, action, policy.Decision{Allowed: true}, 0)
		if err != nil {
			t.Fatalf("early boundary rollover failed: %v", err)
		}
		if nextCP.Phase != domain.SupervisorTurnStarted || nextCP.NextTurn != 2 {
			t.Fatalf("unexpected next checkpoint: %+v", nextCP)
		}
	})

	t.Run("rejects_exhausted_tool_budget", func(t *testing.T) {
		st, run := newThreadTaskTest(t)
		input := queueThreadTask(t, st, run, "budget-exhausted")
		_, lease := prepareThreadTask(t, st, run, 1)
		cp := beginThreadTask(t, st, lease, input)

		attempt := startToolRepairModel(t, st, cp, 0, 0)
		attempt.Outcome = llm.OutcomeSuccess
		call := toolRepairNote(t, cp)
		var err error
		cp, err = st.RecordSupervisorModelCompleted(ctx, cp, attempt, llm.ChatResponse{
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, ToolCalls: []llm.ToolCall{call},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.RecordSupervisorToolExecutionStarted(ctx, cp, call.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.RecordSupervisorToolResult(ctx, cp, domain.SupervisorToolResult{
			CallID: call.ID, Status: domain.SupervisorToolCompleted, ResultJSON: `{"status":"completed"}`, CompletedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		// Mark run tool-call budget as fully consumed (100 of 100).
		if _, err := st.db.ExecContext(ctx, `INSERT INTO run_tool_usage (run_id, consumed, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(run_id) DO UPDATE SET consumed=excluded.consumed, updated_at=excluded.updated_at`,
			run.ID, run.Budget.MaxToolCalls, ts(time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
		endAttempt := startToolRepairModel(t, st, cp, 0, 1)
		endAttempt.Outcome = llm.OutcomeSuccess
		resp := llm.ChatResponse{Text: "continue", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
		cp, err = st.RecordSupervisorModelCompleted(ctx, cp, endAttempt, resp)
		if err != nil {
			t.Fatal(err)
		}
		action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: "continue"}
		if _, _, _, err := st.CompleteSupervisorToolBoundary(ctx, cp, resp, action, policy.Decision{Allowed: true}, 0); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
			t.Fatalf("exhausted tool budget boundary err=%v, want CodeResourceExhausted", err)
		}
	})
}

func TestPrepareApprovalContinuationSupportsWorkspaceDeleteAndAppliedOrFailedOutcomes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		toolName   string
		operation  string
		review     application.FileEditReviewAction
		finalState string
		wantWord   string
	}{
		{
			name:       "workspace_change_applied",
			toolName:   "workspace_change",
			operation:  fileedit.OperationCreate,
			review:     application.FileEditApproveIntent,
			finalState: fileedit.StatusApplied,
			wantWord:   "applied",
		},
		{
			name:       "workspace_change_failed",
			toolName:   "workspace_change",
			operation:  fileedit.OperationCreate,
			review:     application.FileEditApproveIntent,
			finalState: fileedit.StatusFailed,
			wantWord:   "failed",
		},
		{
			name:       "workspace_delete_applied",
			toolName:   "workspace_delete",
			operation:  fileedit.OperationDelete,
			review:     application.FileEditApproveIntent,
			finalState: fileedit.StatusApplied,
			wantWord:   "applied",
		},
		{
			name:       "workspace_delete_denied",
			toolName:   "workspace_delete",
			operation:  fileedit.OperationDelete,
			review:     application.FileEditDeny,
			finalState: fileedit.StatusDenied,
			wantWord:   "denied",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, run := newThreadTaskTest(t)
			mission, err := st.GetMission(ctx, run.MissionID)
			if err != nil {
				t.Fatal(err)
			}
			ws, err := st.GetWorkspaceByID(ctx, mission.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			targetName := "target.txt"
			if tc.operation == fileedit.OperationDelete {
				if err := os.WriteFile(filepath.Join(ws.RootPath, targetName), []byte("delete me\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			input := queueThreadTask(t, st, run, tc.name)
			op, lease := prepareThreadTask(t, st, run, 1)
			turn, err := st.BeginSupervisorSteeringTurnForMessage(ctx, lease, input)
			if err != nil {
				t.Fatal(err)
			}
			cp := turn.Checkpoint

			manager := fileedit.NewManager(st)
			prop := fileedit.Proposal{
				SessionID: run.SessionID, WorkspaceID: ws.ID, WorkspaceRoot: ws.RootPath,
				Path: targetName, Operation: tc.operation,
			}
			if tc.operation == fileedit.OperationCreate {
				prop.ExpectedOriginalHash = "missing"
				prop.ProposedText = "created\n"
			} else {
				prop.ExpectedOriginalHash = fileedit.HashText("delete me\n")
			}
			edit, err := manager.Propose(ctx, prop)
			if err != nil {
				t.Fatal(err)
			}

			perm, err := st.GetRunExecutionPermission(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			scope := toolgateway.AgentCodeCapabilityContext{
				RunID: run.ID, MissionID: mission.ID, RootAgentID: turn.Agent.ID,
				WorkspaceID: ws.ID, RootFingerprint: strings.Repeat("c", 64),
				Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Role: turn.Agent.Role, Profile: turn.Agent.Profile,
				PermissionMode: perm.Mode, ModeRevision: turn.Mode.Revision, PermissionRevision: perm.Revision,
			}
			auth, err := toolgateway.NewAgentCodeCallAuthority(scope, run.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			authJSON, err := toolgateway.EncodeAgentCodeCallAuthority(auth)
			if err != nil {
				t.Fatal(err)
			}

			var payload []byte
			if tc.toolName == "workspace_change" {
				payload, err = toolgateway.NormalizeAgentCodePayload(toolgateway.WorkspaceChangeTool,
					json.RawMessage(fmt.Sprintf(`{"version":"agent-code-tools.v1","action":"create","path":%q,"expected_sha256":"missing","content":"created\n"}`, targetName)))
			} else {
				payload, err = toolgateway.NormalizeAgentCodePayload(toolgateway.WorkspaceDeleteTool,
					json.RawMessage(fmt.Sprintf(`{"version":"agent-code-tools.v1","action":"propose","path":%q,"confirm_path":%q,"expected_sha256":%q}`, targetName, targetName, prop.ExpectedOriginalHash)))
			}
			if err != nil {
				t.Fatal(err)
			}
			opKey := runmutation.SupervisorToolOperationKey(run.ID, cp.NextTurn, tc.toolName, string(payload))
			callID, err := runmutation.SupervisorToolCallID(opKey, 1)
			if err != nil {
				t.Fatal(err)
			}
			attempt := startToolRepairModel(t, st, cp, 0, 0)
			attempt.Outcome = llm.OutcomeSuccess
			cp, err = st.RecordSupervisorModelCompleted(ctx, cp, attempt, llm.ChatResponse{
				Provider: "test", Model: "model", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
				ToolCalls: []llm.ToolCall{{ID: callID, Name: tc.toolName, Arguments: payload, Authority: authJSON}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.RecordSupervisorToolExecutionStarted(ctx, cp, callID); err != nil {
				t.Fatal(err)
			}
			resBytes, _ := json.Marshal(map[string]any{
				"version": "supervisor_tool_result.v1",
				"tool":    tc.toolName,
				"status":  "completed",
				"metadata": map[string]string{
					"edit_id":   edit.ID,
					"status":    "proposed",
					"operation": tc.operation,
				},
				"stdout": "{}",
			})
			if _, _, err := st.RecordSupervisorToolResult(ctx, cp, domain.SupervisorToolResult{
				CallID: callID, Status: domain.SupervisorToolCompleted, ResultJSON: string(resBytes), CompletedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}

			waitAttempt := startToolRepairModel(t, st, cp, 0, 1)
			waitAttempt.Outcome = llm.OutcomeSuccess
			waitAction := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionWait, Message: "Await review", Reason: "operator review"}
			waitJSON, _ := json.Marshal(waitAction)
			waitResp := llm.ChatResponse{Text: string(waitJSON), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
			cp, err = st.RecordSupervisorModelCompleted(ctx, cp, waitAttempt, waitResp)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := st.CompleteSupervisorTurn(ctx, cp, waitResp, waitAction, policy.Decision{Allowed: true}, 0); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.CompleteRunExecutionHandoff(ctx, op.ID, lease, domain.RunExecutionHandoffCompleted, "root_wait", "", 1, true, true); err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.ReleaseRunExecutionLease(ctx, lease); err != nil {
				t.Fatal(err)
			}

			if _, err := application.NewFileEditReviewService(st).Review(ctx, application.ReviewFileEditRequest{
				Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edit.ID, Action: tc.review,
			}); err != nil {
				t.Fatal(err)
			}
			switch tc.finalState {
			case fileedit.StatusApplied:
				if _, err := manager.Approve(ctx, edit.ID, ws.RootPath); err != nil {
					t.Fatal(err)
				}
			case fileedit.StatusFailed:
				if err := os.WriteFile(filepath.Join(ws.RootPath, targetName), []byte("conflicting content\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := manager.Approve(ctx, edit.ID, ws.RootPath); err == nil {
					t.Fatal("expected conflict failure on Approve")
				}
			}

			cont, ready, err := st.PrepareApprovalContinuation(ctx, run.ID, "file_edit", edit.ID)
			if err != nil || !ready {
				t.Fatalf("PrepareApprovalContinuation ready=%t err=%v", ready, err)
			}
			var evidence string
			if err := st.db.QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.approval_continuation.evidence')
				FROM run_events WHERE run_id=? AND subject_id=? AND type='run.execution_handoff_requested'`,
				run.ID, cont.Handoff.Operation.ID).Scan(&evidence); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(evidence, edit.ID) || !strings.Contains(evidence, tc.wantWord) {
				t.Fatalf("evidence missing %q or %q: %s", edit.ID, tc.wantWord, evidence)
			}
			if strings.Contains(evidence, "workspace_apply") {
				t.Fatalf("evidence for %s must not instruct workspace_apply: %s", tc.finalState, evidence)
			}
		})
	}
}
