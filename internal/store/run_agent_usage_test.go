package store

import (
	"context"
	"encoding/json"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

func TestRunAgentUsageIncludesModelReceiptBeforeTurnCompletion(t *testing.T) {
	for _, kind := range []string{"tool_response", "failed_usage", "late_failed_usage"} {
		t.Run(kind, func(t *testing.T) {
			st, run, lease := newMonetarySupervisor(t)
			ctx := t.Context()
			turn, err := st.BeginSupervisorTurn(ctx, lease, "charge a model response before executing its tool")
			if err != nil {
				t.Fatal(err)
			}
			attempt := llm.ModelAttempt{SupervisorAttemptID: turn.Checkpoint.AttemptID,
				Number: 1, MaxAttempts: 1, Provider: "mock", Model: "mock-code"}
			if _, err := st.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil {
				t.Fatal(err)
			}
			if kind == "late_failed_usage" {
				if _, err := application.NewRunService(st).Cancel(ctx, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			before, found, err := st.GetRootAgent(ctx, run.ID)
			if err != nil || !found {
				t.Fatalf("root before receipt: found=%t err=%v", found, err)
			}
			usage := llm.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
			payload, err := toolgateway.NormalizeStructuredMemoryPayload(toolgateway.NoteCreateTool,
				json.RawMessage(`{"title":"receipt","content":"usage recorded"}`))
			if err != nil {
				t.Fatal(err)
			}
			key := runmutation.SupervisorToolOperationKey(run.ID, turn.Checkpoint.NextTurn, "note_create", string(payload))
			callID, err := runmutation.SupervisorToolCallID(key, 1)
			if err != nil {
				t.Fatal(err)
			}
			record := func() error {
				if kind == "tool_response" {
					attempt.Outcome = llm.OutcomeSuccess
					_, err := st.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, attempt, llm.ChatResponse{
						Usage: usage, ToolCalls: []llm.ToolCall{{ID: callID, Name: "note_create", Arguments: payload}},
					})
					return err
				}
				attempt.Outcome, attempt.ErrorText = llm.OutcomePermanent, "provider returned billed failure"
				_, err := st.RecordSupervisorModelFailedWithUsage(ctx, turn.Checkpoint, attempt, usage, 0)
				return err
			}
			for replay := range 2 {
				if err := record(); err != nil {
					t.Fatal(err)
				}
				aggregate, err := st.GetRunAgentUsage(ctx, run.ID)
				if err != nil || aggregate.RootTokens != 5 || aggregate.TotalTokens != 5 {
					t.Fatalf("usage immediately after model receipt (replay %d): %+v err=%v", replay, aggregate, err)
				}
				after, found, err := st.GetRootAgent(ctx, run.ID)
				if err != nil || !found || after.Status != before.Status || after.ActiveAttemptID != before.ActiveAttemptID ||
					after.TurnsUsed != before.TurnsUsed || after.StatusReason != before.StatusReason ||
					!sameTimePointer(after.FinishedAt, before.FinishedAt) {
					t.Fatalf("accounting changed root execution state: before=%+v after=%+v err=%v", before, after, err)
				}
				if replay == 0 {
					before = after
				} else if after.Version != before.Version {
					t.Fatalf("terminal replay changed root version: %d -> %d", before.Version, after.Version)
				}
			}
		})
	}
}

func TestRunAgentUsageReconcilesRootAndSpecialistLedgers(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := context.Background()
	fixture := prepareSpecialistAttemptFixture(t, ctx, st,
		"aggregate Agent usage", 3, 64)
	attempt, _, err := st.BeginSpecialistAttempt(ctx,
		newAttemptStart(fixture, idgen.New("attempt")), "usage-reconcile-start-0001")
	if err != nil {
		t.Fatal(err)
	}
	usage := domain.AgentAttemptUsage{
		InputTokens: 4, OutputTokens: 3, TotalTokens: 7, ExecutionMillis: 11,
	}
	if _, _, err := st.RecordSpecialistAttemptUsage(ctx, attemptRef(attempt), usage,
		"usage-reconcile-charge-0001"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ContinueSpecialistAttempt(ctx, attemptRef(attempt),
		"usage-reconcile-continue-0001"); err != nil {
		t.Fatal(err)
	}

	aggregate, err := st.GetRunAgentUsage(ctx, fixture.Run.ID)
	if err != nil || aggregate.RootTokens != 0 || aggregate.SpecialistTokens != 7 ||
		aggregate.TotalTokens != 7 || aggregate.TotalExecutionMillis != 0 {
		t.Fatalf("aggregate usage is invalid: usage=%#v err=%v", aggregate, err)
	}

	if _, err := st.db.ExecContext(ctx, `UPDATE agent_nodes SET tokens_used = 1
		WHERE id = ?`, fixture.Root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetRunAgentUsage(ctx, fixture.Run.ID); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("root projection drift was accepted: code=%s err=%v", apperror.CodeOf(err), err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE agent_nodes SET tokens_used = 0
		WHERE id = ?`, fixture.Root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE agent_nodes SET tokens_used = tokens_used + 1
		WHERE id = ?`, fixture.Child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetRunAgentUsage(ctx, fixture.Run.ID); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("Specialist projection drift was accepted: code=%s err=%v",
			apperror.CodeOf(err), err)
	}
}
