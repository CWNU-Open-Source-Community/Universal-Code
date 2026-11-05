package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/store"
)

func TestThreadTerminalComposerCreatesFreshAuthorityFreeSuccessor(t *testing.T) {
	for _, terminalStatus := range []domain.RunStatus{
		domain.RunCompleted, domain.RunFailed, domain.RunCancelled,
	} {
		t.Run(string(terminalStatus), func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			runs := application.NewRunService(st)
			mission, initial, err := runs.Create(ctx, application.CreateRunRequest{
				Goal: "stable task identity", Profile: "review",
				Budget: domain.Budget{MaxTurns: 4}, WorkspaceID: "workspace-thread-test",
			})
			if err != nil {
				t.Fatal(err)
			}
			switch terminalStatus {
			case domain.RunCompleted:
				initial, err = runs.Start(ctx, initial.ID)
				if err == nil {
					initial, err = runs.Complete(ctx, initial.ID)
				}
			case domain.RunFailed:
				initial, err = runs.Start(ctx, initial.ID)
				if err == nil {
					initial, err = runs.Fail(ctx, initial.ID, "fixture failure")
				}
			case domain.RunCancelled:
				initial, err = runs.Cancel(ctx, initial.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			threadRecord, err := st.GetThreadByRun(ctx, initial.ID)
			if err != nil || threadRecord.ID == "" || threadRecord.ActiveRunID != "" {
				t.Fatalf("terminal projection=%#v err=%v", threadRecord, err)
			}
			result, err := application.NewThreadService(st).Submit(ctx,
				application.SubmitThreadMessageRequest{
					Version: domain.ThreadMessageProtocolVersion, ThreadID: threadRecord.ID,
					Content:      "continue the same task",
					OperationKey: "thread-terminal-continuation-0001",
					RequestedBy:  "test_operator",
				})
			if err != nil {
				t.Fatal(err)
			}
			if !result.SuccessorCreated || result.PredecessorRunID != initial.ID ||
				result.Run.ID == initial.ID || result.Run.MissionID != mission.ID ||
				result.Run.SessionID == initial.SessionID || result.Run.Status != domain.RunCreated ||
				result.Thread.ID != threadRecord.ID || result.Thread.ActiveRunID != result.Run.ID ||
				result.Message.RunID != result.Run.ID {
				t.Fatalf("unexpected successor: %#v", result)
			}
			var continuity contextmgr.ContinuitySnapshot
			if err := json.Unmarshal(result.Run.Config.ContinuityContext, &continuity); err != nil {
				t.Fatal(err)
			}
			if continuity.SourceRunID != initial.ID || continuity.Authority != (contextmgr.ContinuityAuthority{}) {
				t.Fatalf("successor continuity inherited authority: %#v", continuity)
			}
			permission, err := st.GetRunExecutionPermission(ctx, result.Run.ID)
			if err != nil || permission.Mode != domain.RunExecutionPermissionAsk ||
				permission.OperatorConfirmed || permission.ProcessEnabled ||
				permission.ExecutionAuthorized || permission.CapabilityGrant {
				t.Fatalf("successor permission=%#v err=%v", permission, err)
			}
			profile, err := st.GetRunExecutionProfile(ctx, result.Run.ID)
			if err != nil || profile.Profile != domain.RunExecutionProfilePreview ||
				profile.ProcessEnabled || profile.ExecutionAuthorized || profile.CapabilityGrant {
				t.Fatalf("successor profile=%#v err=%v", profile, err)
			}
			browser, err := st.GetRunBrowserCDPPermission(ctx, result.Run.ID)
			if err != nil || browser.Mode != domain.RunBrowserCDPPermissionRestricted ||
				browser.TransportEnabled || browser.BrowserStartAuthorized ||
				browser.CapabilityGrant {
				t.Fatalf("successor browser permission=%#v err=%v", browser, err)
			}
			if lease, found, err := st.GetRunExecutionLease(ctx, result.Run.ID); err != nil ||
				found || lease.LeaseID != "" {
				t.Fatalf("successor resurrected lease=%#v found=%t err=%v", lease, found, err)
			}
			approvals, err := st.ListApprovals(ctx, approval.ListFilter{
				RunID: result.Run.ID, Limit: 100})
			if err != nil || len(approvals) != 0 {
				t.Fatalf("successor resurrected approvals=%#v err=%v", approvals, err)
			}
			processes, err := st.ListScriptProcesses(ctx, scriptprocess.ListFilter{
				RunID: result.Run.ID, Limit: 100})
			if err != nil || len(processes) != 0 {
				t.Fatalf("successor resurrected processes=%#v err=%v", processes, err)
			}
		})
	}
}

func TestThreadContinuityChainsAcrossMultipleSuccessorRuns(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs := application.NewRunService(st)
	_, first, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "multi-run continuity", Profile: "review", Budget: domain.Budget{MaxTurns: 4},
		WorkspaceID: "workspace-thread-chain",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err = runs.Cancel(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	threadRecord, err := st.GetThreadByRun(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	threads := application.NewThreadService(st)
	second, err := threads.Submit(ctx, application.SubmitThreadMessageRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: threadRecord.ID,
		Content: "start the second run", OperationKey: "thread-chain-message-0001",
		RequestedBy: "test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := runs.Cancel(ctx, second.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	third, err := threads.Submit(ctx, application.SubmitThreadMessageRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: threadRecord.ID,
		Content: "start the third run", OperationKey: "thread-chain-message-0002",
		RequestedBy: "test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	var previous, chained contextmgr.ContinuitySnapshot
	if err := json.Unmarshal(secondRun.Config.ContinuityContext, &previous); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(third.Run.Config.ContinuityContext, &chained); err != nil {
		t.Fatal(err)
	}
	wantLineage := fmt.Sprintf("continuity:%s:%s", previous.SourceRunID,
		previous.Fingerprint)
	found := false
	for _, item := range chained.InheritedContext {
		if item == wantLineage {
			found = true
			break
		}
	}
	if !found || chained.SourceRunID != secondRun.ID ||
		chained.Authority != (contextmgr.ContinuityAuthority{}) {
		t.Fatalf("chained continuity=%#v want lineage=%q", chained, wantLineage)
	}
}

func TestThreadWaitingApprovalKeepsSameRunAndAcceptsComposerInput(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs := application.NewRunService(st)
	mission, run, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "waiting approval task", Profile: "review", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err = transitionRunForThreadTest(ctx, st, mission, run, domain.RunWaitingApproval)
	if err != nil {
		t.Fatal(err)
	}
	threadRecord, err := st.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := application.NewThreadService(st).Submit(ctx,
		application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion,
			ThreadID: threadRecord.ID, Content: "approval clarification",
			OperationKey: "thread-waiting-approval-message-0001", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if result.SuccessorCreated || result.Run.ID != run.ID ||
		result.Run.Status != domain.RunWaitingApproval || result.Message.RunID != run.ID {
		t.Fatalf("waiting approval submission changed Run: %#v", result)
	}
}

func TestConcurrentThreadContinuationCreatesExactlyOneSuccessor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cyberagent.db")
	firstStore, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	runs := application.NewRunService(firstStore)
	_, predecessor, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "concurrent continuation", Profile: "review", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	predecessor, err = runs.Cancel(ctx, predecessor.ID)
	if err != nil {
		t.Fatal(err)
	}
	threadRecord, err := firstStore.GetThreadByRun(ctx, predecessor.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	type outcome struct {
		result application.SubmitThreadMessageResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	stores := []*store.SQLiteStore{firstStore, secondStore}
	var start sync.WaitGroup
	start.Add(1)
	for index, current := range stores {
		go func(index int, current *store.SQLiteStore) {
			start.Wait()
			result, submitErr := application.NewThreadService(current).Submit(ctx,
				application.SubmitThreadMessageRequest{
					Version: domain.ThreadMessageProtocolVersion, ThreadID: threadRecord.ID,
					Content:      "concurrent continuation",
					OperationKey: fmt.Sprintf("thread-concurrent-continuation-%04d", index),
					RequestedBy:  "test_operator",
				})
			outcomes <- outcome{result: result, err: submitErr}
		}(index, current)
	}
	start.Done()
	first, second := <-outcomes, <-outcomes
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent continuation errors: first=%v second=%v", first.err, second.err)
	}
	if first.result.Run.ID != second.result.Run.ID {
		t.Fatalf("concurrent continuation diverged: %s vs %s",
			first.result.Run.ID, second.result.Run.ID)
	}
	createdCount := 0
	if first.result.SuccessorCreated {
		createdCount++
	}
	if second.result.SuccessorCreated {
		createdCount++
	}
	if createdCount != 1 {
		t.Fatalf("successor creation count=%d want=1", createdCount)
	}
	bindings, err := firstStore.ListThreadRuns(ctx, threadRecord.ID)
	if err != nil || len(bindings) != 2 || bindings[1].RunID != first.result.Run.ID ||
		bindings[1].PredecessorRunID != predecessor.ID {
		t.Fatalf("concurrent bindings=%#v err=%v", bindings, err)
	}
}

func TestThreadContinuationSurvivesStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cyberagent.db")
	first, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(first)
	_, predecessor, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "restart durable Thread", Profile: "review",
		Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	predecessor, err = runs.Cancel(ctx, predecessor.ID)
	if err != nil {
		t.Fatal(err)
	}
	threadRecord, err := first.GetThreadByRun(ctx, predecessor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	result, err := application.NewThreadService(reopened).Submit(ctx,
		application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion,
			ThreadID: threadRecord.ID, Content: "continue after process restart",
			OperationKey: "thread-restart-continuation-0001", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.SuccessorCreated || result.Thread.ID != threadRecord.ID ||
		result.PredecessorRunID != predecessor.ID || result.Run.ID == predecessor.ID {
		t.Fatalf("restart continuation=%#v", result)
	}
	bindings, err := reopened.ListThreadRuns(ctx, threadRecord.ID)
	if err != nil || len(bindings) != 2 || bindings[1].RunID != result.Run.ID {
		t.Fatalf("restart bindings=%#v err=%v", bindings, err)
	}
}

func transitionRunForThreadTest(ctx context.Context, st *store.SQLiteStore,
	mission domain.Mission, run domain.Run, target domain.RunStatus,
) (domain.Run, error) {
	expected := run.Status
	if err := run.Transition(target, time.Now().UTC()); err != nil {
		return domain.Run{}, err
	}
	event, err := events.New(run.ID, mission.ID, events.RunStatusChangedEvent,
		"thread_test", run.ID, map[string]any{"from": expected, "to": target})
	if err != nil {
		return domain.Run{}, err
	}
	if err := st.TransitionRun(ctx, run, expected, event); err != nil {
		return domain.Run{}, err
	}
	return run, nil
}

func TestThreadSuccessorInheritsBrowserOrigins(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs := application.NewRunService(st)
	_, initial, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "scoped browser thread", Profile: "review",
		Budget: domain.Budget{MaxTurns: 4}, WorkspaceID: "workspace-thread-test",
		BrowserOrigins: []string{"https://example.org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Config.BrowserOrigins) != 1 || initial.Config.BrowserOrigins[0] != "https://example.org:443" {
		t.Fatalf("creation must canonicalize the declaration: %+v", initial.Config.BrowserOrigins)
	}
	if initial, err = runs.Cancel(ctx, initial.ID); err != nil {
		t.Fatal(err)
	}
	threadRecord, err := st.GetThreadByRun(ctx, initial.ID)
	if err != nil || threadRecord.ID == "" {
		t.Fatalf("thread projection=%#v err=%v", threadRecord, err)
	}
	result, err := application.NewThreadService(st).Submit(ctx, application.SubmitThreadMessageRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: threadRecord.ID,
		Content:      "continue the scoped preview",
		OperationKey: "thread-successor-origins-0001",
		RequestedBy:  "test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.SuccessorCreated || len(result.Run.Config.BrowserOrigins) != 1 ||
		result.Run.Config.BrowserOrigins[0] != "https://example.org:443" {
		t.Fatalf("successor must inherit the declared origins: %#v", result.Run.Config.BrowserOrigins)
	}
}
