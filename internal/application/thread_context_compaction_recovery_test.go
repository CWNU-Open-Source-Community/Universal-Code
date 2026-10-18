package application_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/pricing"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

type compactionCrashBeforeCommitStore struct{ *store.SQLiteStore }

func (s compactionCrashBeforeCommitStore) CommitThreadContextCompaction(context.Context, int64, contextmgr.Result) (domain.ThreadContextCompactionResult, error) {
	return domain.ThreadContextCompactionResult{}, errors.New("simulated process exit after saving provider response")
}
func (s compactionCrashBeforeCommitStore) FailThreadContextCompaction(context.Context, int64, error) error {
	return nil // A dead process cannot execute deferred cleanup.
}

func compactionRecoveryRequest(run domain.Run, operationID, action string) domain.ThreadContextCompactionRecoveryRequest {
	return domain.ThreadContextCompactionRecoveryRequest{Version: domain.ThreadContextCompactionRecoveryVersion,
		ThreadID: domain.InitialThreadID(run.ID), OperationID: operationID, ExpectedRunID: run.ID,
		Action: action, RequestedBy: "recovery_test"}
}

func TestManualThreadCompactRecoveryAfterReopenReusesGeneratedSummary(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_source", true: "changed_source"}[changed], func(t *testing.T) {
			st, p, run, path := manualCompactFixture(t)
			before, _ := st.ListSessionMessages(t.Context(), run.SessionID, false)
			_, err := application.NewThreadContextCompactionService(compactionCrashBeforeCommitStore{st}, generatedFixtureRouter(p)).Compact(t.Context(), manualCompactRequest(run, "crash"))
			if err == nil || p.generatedCount() != 1 {
				t.Fatalf("crash err=%v calls=%d", err, p.generatedCount())
			}
			if err = st.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openHistoryRecallStore(t, path)
			defer reopened.Close()
			// No router: recovery must work from durable data without any model call.
			svc := application.NewThreadContextCompactionService(reopened, nil)
			list, err := svc.ListOperations(t.Context(), domain.InitialThreadID(run.ID))
			if err != nil || len(list.Operations) != 1 || list.Operations[0].Status != "generated" || !list.Operations[0].UsageKnown {
				t.Fatalf("operations=%+v err=%v", list, err)
			}
			req := compactionRecoveryRequest(run, list.Operations[0].OperationID, "resume")
			if changed {
				if _, err = reopened.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", "a new requirement")); err != nil {
					t.Fatal(err)
				}
				_, err = svc.Recover(t.Context(), req)
				if apperror.CodeOf(err) != apperror.CodeConflict {
					t.Fatalf("stale summary recovered: %v", err)
				}
				req.Action = "discard"
				view, err := svc.Recover(t.Context(), req)
				if err != nil || view.Status != "failed" {
					t.Fatalf("discard=%+v %v", view, err)
				}
				active, _ := reopened.ListSessionMessages(t.Context(), run.SessionID, false)
				if len(active) != len(before)+1 {
					t.Fatal("discard replaced original history")
				}
				return
			}
			view, err := svc.Recover(t.Context(), req)
			if err != nil || view.Status != "completed" || view.Result == nil || p.generatedCount() != 1 {
				t.Fatalf("resume=%+v err=%v calls=%d", view, err, p.generatedCount())
			}
			replay, err := svc.Recover(t.Context(), req)
			if err != nil || replay.Result == nil || !replay.Result.Replayed || replay.Result.SummaryID != view.Result.SummaryID {
				t.Fatalf("replay=%+v %v", replay, err)
			}
			all, _ := reopened.ListSessionMessages(t.Context(), run.SessionID, true)
			active, _ := reopened.ListSessionMessages(t.Context(), run.SessionID, false)
			if len(all) != len(before) || len(active) != 4 {
				t.Fatal("recovery changed originals or preserved window")
			}
		})
	}
}

func TestManualThreadCompactRecoveryDiscardsPendingAndAccountsLateResponse(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "started"}[started], func(t *testing.T) {
			st, p, run, path := manualCompactFixture(t)
			req := manualCompactRequest(run, "pending")
			op, _, err := st.BeginThreadContextCompaction(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.PrepareThreadContextCompactionCall(t.Context(), op.ID, "tool-loop", "model", 1000, strings.Repeat("a", 64), 0); err != nil {
				t.Fatal(err)
			}
			if started {
				if err = st.StartThreadContextCompactionCall(t.Context(), op.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err = st.Close(); err != nil {
				t.Fatal(err)
			}
			st = openHistoryRecallStore(t, path)
			defer st.Close()
			svc := application.NewThreadContextCompactionService(st, generatedFixtureRouter(p))
			recovery := compactionRecoveryRequest(run, op.OperationID, "resume")
			if _, err = svc.Recover(t.Context(), recovery); apperror.CodeOf(err) != apperror.CodeConflict || p.generatedCount() != 0 {
				t.Fatalf("pending resent: %v", err)
			}
			recovery.Action = "discard"
			view, err := svc.Recover(t.Context(), recovery)
			if err != nil || view.Status != "failed" || view.UsageKnown {
				t.Fatalf("discard=%+v %v", view, err)
			}
			if _, err = svc.Recover(t.Context(), recovery); err != nil {
				t.Fatal(err)
			}
			if err = st.StartThreadContextCompactionCall(t.Context(), op.ID); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("discarded worker can dispatch: %v", err)
			}
			if started {
				usage, err := st.GetRunAgentUsage(t.Context(), run.ID)
				if err != nil || usage.ContextCompactionTokens != 1000 {
					t.Fatalf("unknown charge released: %+v %v", usage, err)
				}
				actual := llm.Usage{InputTokens: 11, OutputTokens: 4, TotalTokens: 15}
				late, err := st.CompleteThreadContextCompactionCall(t.Context(), op.ID, "late summary must not be adopted", &actual, time.Second, nil)
				if err != nil || late.Status != "failed" || late.ResponseText != "" {
					t.Fatalf("late response=%+v %v", late, err)
				}
				usage, err = st.GetRunAgentUsage(t.Context(), run.ID)
				if err != nil || usage.ContextCompactionTokens != 15 {
					t.Fatalf("late charge lost: %+v %v", usage, err)
				}
				if _, err = st.CompleteThreadContextCompactionCall(t.Context(), op.ID, "duplicate", &actual, time.Second, nil); apperror.CodeOf(err) != apperror.CodeConflict {
					t.Fatalf("duplicate terminal=%v", err)
				}
			}
			if _, found, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || found {
				t.Fatalf("discard installed a summary: %t %v", found, err)
			}
			if _, err = svc.Compact(t.Context(), req); err == nil || p.generatedCount() != 0 {
				t.Fatalf("old key redispatched: %v", err)
			}
			result, err := svc.Compact(t.Context(), manualCompactRequest(run, "explicit-new-attempt"))
			if err != nil || result.Status != "completed" || p.generatedCount() != 1 {
				t.Fatalf("new attempt blocked: %+v %v", result, err)
			}
		})
	}
}

func TestManualThreadCompactRecoveryBindsThreadAndRun(t *testing.T) {
	st, _, run, _ := manualCompactFixture(t)
	op, _, err := st.BeginThreadContextCompaction(t.Context(), manualCompactRequest(run, "binding"))
	if err != nil {
		t.Fatal(err)
	}
	svc := application.NewThreadContextCompactionService(st, nil)
	req := compactionRecoveryRequest(run, op.OperationID, "discard")
	req.ThreadID = "thread-another"
	if _, err = svc.Recover(t.Context(), req); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("cross-thread recovery: %v", err)
	}
	req = compactionRecoveryRequest(run, op.OperationID, "discard")
	req.ExpectedRunID = "run-another"
	if _, err = svc.Recover(t.Context(), req); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("wrong-run recovery: %v", err)
	}
	list, err := svc.ListOperations(t.Context(), domain.InitialThreadID(run.ID))
	if err != nil || len(list.Operations) != 1 || list.Operations[0].Status != "prepared" {
		t.Fatalf("unauthorized mutation: %+v %v", list, err)
	}
}

func TestManualThreadCompactRecoveryPreservesMonetaryReservation(t *testing.T) {
	for _, phase := range []string{"prepared", "started", "generated"} {
		t.Run(phase, func(t *testing.T) {
			st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "recovery-budget.db"))
			defer st.Close()
			_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "恢复压缩", Profile: "code", ModelRoute: "tool-loop/model", Budget: domain.Budget{MaxTurns: 10, MaxCostUSD: 1}})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			prices := pricing.Snapshot{ProtocolVersion: pricing.ProtocolVersion, ID: "recovery-price", Source: pricing.SourceOperatorImport, Currency: pricing.CurrencyUSD, ImportedBy: "test", ImportedAt: now, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour), Entries: []pricing.Entry{{Provider: "tool-loop", Model: "model", InputPerMillionMicros: 1000000, OutputPerMillionMicros: 2000000}}}
			prices.Fingerprint = pricing.Fingerprint(prices)
			if _, _, err = st.ImportPriceSnapshot(t.Context(), prices); err != nil {
				t.Fatal(err)
			}
			op, _, err := st.BeginThreadContextCompaction(t.Context(), manualCompactRequest(run, "money"))
			if err != nil {
				t.Fatal(err)
			}
			if err = st.PrepareThreadContextCompactionCall(t.Context(), op.ID, "tool-loop", "model", 1000, strings.Repeat("a", 64), 0); err != nil {
				t.Fatal(err)
			}
			money := application.NewMonetaryBudgetService(st)
			attempt := llm.ModelAttempt{Number: int(op.ID), Provider: "tool-loop", Model: "model"}
			reserved, err := money.ReserveModelCall(t.Context(), run, domain.MonetaryScopeContextCompaction, attempt, llm.ChatRequest{Model: "model", MaxTokens: 100, Messages: []llm.Message{{Role: "user", Content: "summarize history"}}})
			if err != nil || reserved.ReservedMicros <= 0 {
				t.Fatalf("reserve=%+v %v", reserved, err)
			}
			actual := llm.Usage{InputTokens: 11, OutputTokens: 4, TotalTokens: 15}
			if phase != "prepared" {
				if err = st.StartThreadContextCompactionCall(t.Context(), op.ID); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "generated" {
				if _, err = st.CompleteThreadContextCompactionCall(t.Context(), op.ID, "saved candidate", &actual, time.Second, nil); err != nil {
					t.Fatal(err)
				}
			}
			svc := application.NewThreadContextCompactionService(st, nil)
			req := compactionRecoveryRequest(run, op.OperationID, "discard")
			if _, err = svc.Recover(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			usage, err := st.GetMonetaryUsage(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch phase {
			case "prepared":
				if usage.ReleasedMicros != reserved.ReservedMicros || usage.SettledMicros != 0 {
					t.Fatalf("unsent exposure not released: %+v", usage)
				}
			case "started":
				if usage.ReservedMicros != reserved.ReservedMicros || usage.ReleasedMicros != 0 || usage.SettledMicros != 0 {
					t.Fatalf("unknown exposure erased: %+v", usage)
				}
				if _, err = st.CompleteThreadContextCompactionCall(t.Context(), op.ID, "late discarded candidate", &actual, time.Second, nil); err != nil {
					t.Fatal(err)
				}
				usage, err = st.GetMonetaryUsage(t.Context(), run.ID)
				if err != nil || usage.SettledMicros != 19 || usage.ReleasedMicros != reserved.ReservedMicros-19 {
					t.Fatalf("late usage not settled: %+v %v", usage, err)
				}
			case "generated":
				if usage.SettledMicros != 19 || usage.ReleasedMicros != reserved.ReservedMicros-19 {
					t.Fatalf("saved usage not settled: %+v", usage)
				}
			}
			if _, err = svc.Recover(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			replayed, err := st.GetMonetaryUsage(t.Context(), run.ID)
			if err != nil || replayed.SettledMicros != usage.SettledMicros || replayed.ReleasedMicros != usage.ReleasedMicros {
				t.Fatalf("recovery settled twice: %+v %+v %v", usage, replayed, err)
			}
		})
	}
}
