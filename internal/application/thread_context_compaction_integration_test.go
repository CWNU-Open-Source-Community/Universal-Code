package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
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

func manualCompactRequest(run domain.Run, key string) domain.ThreadContextCompactionRequest {
	return domain.ThreadContextCompactionRequest{Version: domain.ThreadContextCompactionVersion, ThreadID: domain.InitialThreadID(run.ID), ExpectedRunID: run.ID, OperationKey: "manual-context-operation-" + key, RequestedBy: "manual_context_test", Focus: "保留未完成任务"}
}
func manualCompactFixture(t *testing.T) (*store.SQLiteStore, *generatedProtocolProvider, domain.Run, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manual-context.db")
	st := openHistoryRecallStore(t, path)
	t.Cleanup(func() { _ = st.Close() })
	_, run := createHistoryRecallRun(t, st, "manual-context", "stable")
	p := newGeneratedProtocolProvider()
	turns, _ := generatedThreadServices(st, p, nil)
	for i := 0; i < 4; i++ {
		content := fmt.Sprintf("普通对话 %d 保留任务进展", i)
		if i < 2 {
			content += strings.Repeat("旧的详细实验过程与证据，仅作为历史。", 95)
		}
		submitHistoryRecall(t, turns, run.ID, fmt.Sprintf("manual-context-seed-operation-%d", i), content)
	}
	return st, p, run, path
}
func TestManualThreadCompactShrinksNextRequestKeepsOriginalAndReplaysAfterReopen(t *testing.T) {
	st, p, run, path := manualCompactFixture(t)
	before, err := st.ListSessionMessages(t.Context(), run.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	_, normal := p.splitRequests()
	beforeBytes := len(generatedRequestText(normal[len(normal)-1].Request))
	svc := application.NewThreadContextCompactionService(st, generatedFixtureRouter(p))
	request := manualCompactRequest(run, "manual-success")
	result, err := svc.Compact(t.Context(), request)
	if err != nil || result.Status != "completed" || !result.Generated || result.SummaryID == 0 || p.generatedCount() != 1 {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, p.generatedCount())
	}
	active, err := st.ListSessionMessages(t.Context(), run.SessionID, false)
	if err != nil || len(active) != 4 || len(active) >= len(before) {
		t.Fatalf("active=%d before=%d err=%v", len(active), len(before), err)
	}
	all, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil || len(all) != len(before) {
		t.Fatalf("original history changed: before=%d all=%d err=%v", len(before), len(all), err)
	}
	for i := range all {
		if all[i].Content != before[i].Content || all[i].Provenance.ContentSHA256 != before[i].Provenance.ContentSHA256 {
			t.Fatal("original source body/digest changed")
		}
	}
	usage, err := st.GetModelUsage(t.Context(), run.ID)
	if err != nil || usage.TotalTokens != int64(p.usage().TotalTokens) {
		t.Fatalf("actual usage missing: %+v known=%+v err=%v", usage, p.usage(), err)
	}
	agentUsage, err := st.GetRunAgentUsage(t.Context(), run.ID)
	if err != nil || agentUsage.ContextCompactionTokens != 124 || agentUsage.TotalTokens != usage.TotalTokens {
		t.Fatalf("operation token accounting=%+v err=%v", agentUsage, err)
	}
	turns, _ := generatedThreadServices(st, p, nil)
	submitHistoryRecall(t, turns, run.ID, "manual-next-request", "继续尚未完成的任务")
	_, normal = p.splitRequests()
	next := generatedRequestText(normal[len(normal)-1].Request)
	if !strings.Contains(next, generatedFixtureMarker) || len(next) >= beforeBytes {
		t.Fatalf("next request did not use reduced actual history: before=%d after=%d", beforeBytes, len(next))
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openHistoryRecallStore(t, path)
	defer reopened.Close()
	replay, err := application.NewThreadContextCompactionService(reopened, generatedFixtureRouter(p)).Compact(t.Context(), request)
	if err != nil || !replay.Replayed || replay.SummaryID != result.SummaryID || p.generatedCount() != 1 {
		t.Fatalf("durable replay=%+v err=%v calls=%d", replay, err, p.generatedCount())
	}
}
func TestThreadManualThenAutomaticCompactionKeepsRefundCorrectionInProviderRequest(t *testing.T) {
	const goal = "Reject negative amount_cents for all ledger entries."
	const correction = "Accept negative amount_cents for refunds. Zero is valid."
	const stale = "The ledger rejects all negative amount_cents, including returns."
	const report = "List unresolved checks when reporting progress."

	path := filepath.Join(t.TempDir(), "ledger.db")
	st := openHistoryRecallStore(t, path)
	defer func() { _ = st.Close() }()
	const workspace = "ledger-workspace"
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{
		ID: workspace, Name: workspace, RootPath: t.TempDir(), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: goal, Profile: "code", WorkspaceID: workspace, ModelRoute: "tool-loop/model",
		Interactive: true, Budget: domain.Budget{MaxTurns: 20, MaxToolCalls: 10},
	})
	if err != nil {
		t.Fatal(err)
	}

	p := newGeneratedProtocolProvider()
	p.auxiliary = func(_ context.Context, _ llm.ChatRequest, round int) (*llm.ChatResponse, error) {
		summary := correction
		if round == 2 {
			summary = stale + " Preserve the CSV header and category column, keep reporting errors on stderr and write a regression test for duplicate IDs. Import validation remains unfinished."
		}
		return &llm.ChatResponse{Text: historyRecallJSON(map[string]string{
			"version": contextmgr.GeneratedHandoffVersion, "summary": summary,
		})}, nil
	}
	p.normal = func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
		if strings.Contains(req.Messages[len(req.Messages)-1].Content, report) {
			// A long progress report brings the following ordinary turn to the automatic threshold.
			return textResponse(rootActionResponse(domain.RootActionFinish, strings.Repeat("历史样例。", 560), "End this reply", "")), nil
		}
		return historyRecallFinish(), nil
	}
	turns, _ := generatedThreadServices(st, p, nil)
	sequence := 0
	submit := func(input string) domain.OperatorSteeringMessage {
		t.Helper()
		sequence++
		result, err := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: input,
			OperationKey: fmt.Sprintf("ledger-correction-input-%02d", sequence), RequestedBy: "test_operator",
		})
		assertHistoryRecallSettled(t, result, err)
		return result.Submission.Message
	}
	submit(goal)
	operator := submit(correction)
	submit("Keep the ledger output in cents.")
	submit("Report diagnostics on stderr.")
	first, err := application.NewThreadContextCompactionService(st, generatedFixtureRouter(p)).
		Compact(t.Context(), manualCompactRequest(run, "ledger-initial"))
	if err != nil || !first.Generated {
		t.Fatalf("first manual compaction = %+v, error = %v", first, err)
	}

	// The later examples push the correction outside the separate recent-originals allowance.
	submit("Keep the import header unchanged. Reference examples:\n" + strings.Repeat("旧表账目。", 158))
	submit("Keep the category column unchanged. Reference examples:\n" + strings.Repeat("账务示例。", 158))
	submit("Write a regression test for duplicate row IDs.")
	submit(report)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = openHistoryRecallStore(t, path)
	turns, _ = generatedThreadServices(st, p, nil)
	submit("Continue the validation work.")

	summary, found, err := st.LatestContextSummary(t.Context(), run.SessionID)
	if err != nil || !found || summary.PreviousSummaryID != first.SummaryID || p.generatedCount() != 2 {
		t.Fatalf("automatic compaction after reopen: previous = %d, calls = %d, error = %v", summary.PreviousSummaryID, p.generatedCount(), err)
	}
	_, normal := p.splitRequests()
	next := generatedRequestText(normal[len(normal)-1].Request)
	if !strings.Contains(next, correction) {
		t.Fatalf("next provider request lost the refund correction from message %d; stale conclusion present = %t",
			operator.SessionMessageID, strings.Contains(next, stale))
	}
	if !strings.Contains(next, summary.Content) {
		t.Fatal("next provider request did not contain the committed summary")
	}
	var envelope struct {
		Records []struct {
			Role                  string
			SourceMessageID       int64  `json:"source_message_id"`
			SourceKind            string `json:"source_kind"`
			SourceContentSHA256   string `json:"source_content_sha256"`
			InstructionAuthorized bool   `json:"instruction_authorized"`
			Content               string
		}
	}
	if err := json.Unmarshal([]byte(summary.Content), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, record := range envelope.Records {
		if record.SourceMessageID == operator.SessionMessageID && record.Role == "user" && record.SourceKind == session.SourceOperatorMessage &&
			record.InstructionAuthorized && record.SourceContentSHA256 == operator.ContentSHA256 && record.Content == correction {
			return
		}
	}
	t.Fatalf("delivered summary lost the wording or operator provenance of message %d", operator.SessionMessageID)
}

func TestManualThreadCompactBusyDoesNotCancelBusinessTurn(t *testing.T) {
	st, p, run, _ := manualCompactFixture(t)
	defer st.Close()
	acquired, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "business-owner", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.NewThreadContextCompactionService(st, generatedFixtureRouter(p)).Compact(t.Context(), manualCompactRequest(run, "manual-busy"))
	if apperror.CodeOf(err) != apperror.CodeConflict || !strings.Contains(err.Error(), "thread_context_busy") || p.generatedCount() != 0 {
		t.Fatalf("busy err=%v calls=%d", err, p.generatedCount())
	}
	lease, found, err := st.GetRunExecutionLease(t.Context(), run.ID)
	if err != nil || !found || lease.LeaseID != acquired.Lease.LeaseID || !lease.ActiveAt(time.Now()) {
		t.Fatalf("business lease changed: %+v %t %v", lease, found, err)
	}
}

func TestManualThreadCompactPendingInputWithoutActiveLeaseIsBusy(t *testing.T) {
	st, p, run, _ := manualCompactFixture(t)
	_, err := st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{
		RunID: run.ID, SessionID: run.SessionID, Content: "压缩前先处理这个新要求",
		OperationKey: "manual-pending-input", RequestedBy: "manual_context_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease, found, err := st.GetRunExecutionLease(t.Context(), run.ID); err != nil || (found && lease.ActiveAt(time.Now())) {
		t.Fatalf("fixture unexpectedly owns an active execution lease: %+v %v", lease, err)
	}
	before, _ := st.ListSessionMessages(t.Context(), run.SessionID, false)
	_, err = application.NewThreadContextCompactionService(st, generatedFixtureRouter(p)).Compact(t.Context(), manualCompactRequest(run, "pending-input"))
	if apperror.CodeOf(err) != apperror.CodeConflict || !strings.Contains(err.Error(), "thread_context_busy") || p.generatedCount() != 0 {
		t.Fatalf("pending input was treated as idle: %v calls=%d", err, p.generatedCount())
	}
	after, _ := st.ListSessionMessages(t.Context(), run.SessionID, false)
	if len(before) != len(after) {
		t.Fatal("busy compaction changed history")
	}
}
func TestManualThreadCompactCancellationAndSourceCASDoNotReplaceHistory(t *testing.T) {
	for _, kind := range []string{"cancel", "message", "competition"} {
		t.Run(kind, func(t *testing.T) {
			st, p, run, path := manualCompactFixture(t)
			defer st.Close()
			before, _ := st.ListSessionMessages(t.Context(), run.SessionID, false)
			entered, release := make(chan struct{}), make(chan struct{})
			p.auxiliary = func(ctx context.Context, _ llm.ChatRequest, _ int) (*llm.ChatResponse, error) {
				close(entered)
				if kind == "cancel" {
					<-ctx.Done()
					return generatedValidResponse(1), ctx.Err()
				}
				<-release
				return generatedValidResponse(1), nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := manualCompactRequest(run, "manual-"+kind)
			service := application.NewThreadContextCompactionService(st, generatedFixtureRouter(p))
			done := make(chan error, 1)
			go func() { _, err := service.Compact(ctx, request); done <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("summary was not dispatched")
			}
			switch kind {
			case "cancel":
				cancel()
			case "message":
				if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", "生成期间的新用户消息")); err != nil {
					t.Fatal(err)
				}
				close(release)
			case "competition":
				other := openHistoryRecallStore(t, path)
				_, err := application.NewThreadContextCompactionService(other, generatedFixtureRouter(p)).Compact(t.Context(), manualCompactRequest(run, "different-key"))
				if apperror.CodeOf(err) != apperror.CodeConflict {
					t.Fatalf("competing compact err=%v", err)
				}
				_, err = application.NewThreadContextCompactionService(other, generatedFixtureRouter(p)).Compact(t.Context(), request)
				if apperror.CodeOf(err) != apperror.CodeConflict || !strings.Contains(err.Error(), "will not be sent again") {
					t.Fatalf("pending retry err=%v", err)
				}
				other.Close()
				// A new source invalidates the first operation as well.
				if _, err = st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", "竞争之后的新消息")); err != nil {
					t.Fatal(err)
				}
				close(release)
			}
			err := <-done
			if err == nil {
				t.Fatal("cancelled/stale summary committed")
			}
			if kind != "cancel" && apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("CAS error=%v", err)
			}
			_, found, readErr := st.LatestContextSummary(t.Context(), run.SessionID)
			if readErr != nil || found {
				t.Fatalf("summary installed after failure: %t %v", found, readErr)
			}
			active, readErr := st.ListSessionMessages(t.Context(), run.SessionID, false)
			want := len(before)
			if kind != "cancel" {
				want++
			}
			if readErr != nil || len(active) != want {
				t.Fatalf("active history mutated: got=%d want=%d err=%v", len(active), want, readErr)
			}
			replay, err := service.Compact(t.Context(), request)
			if err == nil || replay.Status == "completed" || p.generatedCount() != 1 {
				t.Fatalf("failed key re-dispatched: %+v err=%v calls=%d", replay, err, p.generatedCount())
			}
			usage, err := st.GetModelUsage(t.Context(), run.ID)
			if err != nil || usage.TotalTokens != int64(p.usage().TotalTokens) {
				t.Fatalf("failed candidate still has real charge: %+v known=%+v err=%v", usage, p.usage(), err)
			}
		})
	}
}
func TestManualThreadCompactTerminalRunRecordsIndependentMonetaryCharge(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "terminal-context.db"))
	defer st.Close()
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "保留终结任务的事实", Profile: "code", ModelRoute: "tool-loop/model", Budget: domain.Budget{MaxTurns: 10, MaxCostUSD: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err = st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", fmt.Sprintf("历史原文 %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	run, err = application.NewRunService(st).Cancel(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	snapshot := pricing.Snapshot{ProtocolVersion: pricing.ProtocolVersion, ID: "manual-price", Source: pricing.SourceOperatorImport, Currency: pricing.CurrencyUSD, ImportedBy: "test", ImportedAt: now, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour), Entries: []pricing.Entry{{Provider: "tool-loop", Model: "model", InputPerMillionMicros: 1000000, OutputPerMillionMicros: 2000000}}}
	snapshot.Fingerprint = pricing.Fingerprint(snapshot)
	if _, _, err = st.ImportPriceSnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	p := newGeneratedProtocolProvider()
	result, err := application.NewThreadContextCompactionService(st, generatedFixtureRouter(p)).Compact(t.Context(), manualCompactRequest(run, "terminal-compact"))
	if err != nil || result.Status != "completed" {
		t.Fatalf("terminal result=%+v err=%v", result, err)
	}
	money, err := st.GetMonetaryUsage(t.Context(), run.ID)
	if err != nil || money.SettledMicros != 147 {
		t.Fatalf("known auxiliary charge missing: %+v err=%v", money, err)
	}
	current, err := st.GetRun(t.Context(), run.ID)
	if err != nil || current.Status != domain.RunCancelled {
		t.Fatalf("terminal Run was reopened: %+v %v", current, err)
	}
	var usage llm.Usage
	if err = json.Unmarshal(result.Usage, &usage); err != nil || usage.TotalTokens != 124 {
		t.Fatalf("result usage=%s err=%v", result.Usage, err)
	}
}

func TestManualThreadCompactRecoversOversizedSummaryInputWithExplicitCoverage(t *testing.T) {
	st, p, run, _ := manualCompactFixture(t)
	before, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	router := generatedFixtureRouter(p)
	window := llm.DefaultContextWindow()
	window.WindowTokens = 8192
	if err := router.SetContextWindow(llm.ModelRef{Provider: "tool-loop", Model: "model"}, window); err != nil {
		t.Fatal(err)
	}
	result, err := application.NewThreadContextCompactionService(st, router).Compact(t.Context(), manualCompactRequest(run, "oversized-input"))
	if err != nil || result.Status != "completed" || result.InputMessagesOmitted <= 0 || p.generatedCount() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, p.generatedCount())
	}
	calls, _ := p.splitRequests()
	sent := calls[0].Request
	var payload struct {
		Goal    string                              `json:"original_goal"`
		Focus   string                              `json:"optional_summary_focus,omitempty"`
		History contextmgr.SummaryGenerationRequest `json:"history"`
		Omitted int                                 `json:"input_messages_omitted"`
	}
	if err := json.Unmarshal([]byte(sent.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Omitted != result.InputMessagesOmitted || len(payload.History.Messages) != result.RemovedMessages-result.InputMessagesOmitted {
		t.Fatalf("false input coverage: %+v result=%+v", payload, result)
	}
	actualFingerprint := payload.History.InputFingerprint
	payload.History.InputFingerprint = ""
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	if actualFingerprint != hex.EncodeToString(digest[:]) || result.InputFingerprint != actualFingerprint || sent.Metadata["input_fingerprint"] != actualFingerprint {
		t.Fatal("input fingerprint does not describe the actual selected payload")
	}
	estimate, _ := strconv.Atoi(sent.Metadata["context_input_estimate"])
	limit, _ := strconv.Atoi(sent.Metadata["context_input_limit"])
	if estimate <= 0 || estimate > limit {
		t.Fatalf("request exceeds window: estimate=%d limit=%d", estimate, limit)
	}
	summary, found, err := st.LatestContextSummary(t.Context(), run.SessionID)
	if err != nil || !found || !strings.Contains(summary.Content, "Summary input coverage") || !strings.Contains(summary.Content, "originals remain readable") {
		t.Fatalf("missing permanent coverage notice: %s err=%v", summary.Content, err)
	}
	var envelope struct {
		Generated *contextmgr.GeneratedSummary `json:"generated"`
	}
	if err := json.Unmarshal([]byte(summary.Content), &envelope); err != nil || envelope.Generated == nil || envelope.Generated.InputFingerprint != actualFingerprint || envelope.Generated.InputMessagesOmitted != result.InputMessagesOmitted {
		t.Fatalf("summary coverage/receipt mismatch: %s %v", summary.Content, err)
	}
	all, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil || len(all) != len(before) {
		t.Fatalf("all history changed: %d %d %v", len(all), len(before), err)
	}
	for i := range all {
		if all[i].Content != before[i].Content || all[i].Provenance.ContentSHA256 != before[i].Provenance.ContentSHA256 {
			t.Fatal("original source changed")
		}
	}
	turns, _ := generatedThreadServices(st, p, nil)
	submitHistoryRecall(t, turns, run.ID, "manual-after-oversized-input", "继续未完成任务")
	_, normal := p.splitRequests()
	if !strings.Contains(generatedRequestText(normal[len(normal)-1].Request), "Summary input coverage") {
		t.Fatal("next request lost bounded coverage notice")
	}
}

func TestManualThreadCompactUnknownChargeRemainsTerminalAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown-context.db")
	st := openHistoryRecallStore(t, path)
	_, run := createHistoryRecallRun(t, st, "manual-unknown", "stable")
	for i := 0; i < 7; i++ {
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", fmt.Sprintf("original unknown history %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	p := newGeneratedProtocolProvider()
	p.auxiliary = func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error) {
		return nil, errors.New("transport closed after dispatch; billing outcome unknown")
	}
	req := manualCompactRequest(run, "unknown-terminal")
	_, err := application.NewThreadContextCompactionService(st, generatedFixtureRouter(p)).Compact(t.Context(), req)
	if err == nil || p.generatedCount() != 1 {
		t.Fatalf("err=%v calls=%d", err, p.generatedCount())
	}
	usage, err := st.GetRunAgentUsage(t.Context(), run.ID)
	if err != nil || usage.ContextCompactionTokens <= 0 {
		t.Fatalf("unknown call lost conservative accounting: %+v %v", usage, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = openHistoryRecallStore(t, path)
	defer st.Close()
	_, err = application.NewThreadContextCompactionService(st, generatedFixtureRouter(p)).Compact(t.Context(), req)
	if err == nil || p.generatedCount() != 1 {
		t.Fatalf("unknown operation retried after restart: err=%v calls=%d", err, p.generatedCount())
	}
	all, err := st.ListSessionMessages(t.Context(), run.SessionID, false)
	if err != nil || len(all) != 7 {
		t.Fatalf("failed history changed: %d %v", len(all), err)
	}
	if _, found, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || found {
		t.Fatalf("unknown summary published: found=%t err=%v", found, err)
	}
}
