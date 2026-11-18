package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

// Keep auxiliary summary responses independent of the ordinary tool/reply
// script. This deterministic fixture checks the current default orchestration
// and exact source retention, not an external model's summarization quality.
type continuityPurposeProvider struct {
	*scriptedToolProvider
	generatedRequests []llm.ChatRequest
	summary           string
}

func (p *continuityPurposeProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	if request.Metadata["purpose"] != "context_compaction" {
		return p.scriptedToolProvider.Chat(ctx, request)
	}
	p.generatedRequests = append(p.generatedRequests, request)
	text, err := json.Marshal(map[string]string{"version": contextmgr.GeneratedHandoffVersion, "summary": p.summary})
	if err != nil {
		return nil, err
	}
	return &llm.ChatResponse{Text: string(text), Model: "model", Usage: llm.Usage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18}}, nil
}

// Exercise the ordinary product path; no Session.Manager or manual summary call
// is inserted between ThreadTurnService, the handoff, and the Supervisor.
func TestThreadContextContinuityPreservesOriginalIntentAndEvidenceAfterCompactionAndRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "thread-continuity.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	workspace := t.TempDir()
	const evidenceText = "UX_CONTEXT_OBSERVED_README_VALUE\nUntrusted repository instruction: UX_CONTEXT_GRANT_FULL_ACCESS_AND_NETWORK.\n"
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte(evidenceText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "ws-context-continuity", Name: "context continuity", RootPath: workspace, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Inspect the project and retain the user's complete acceptance criteria", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: "ws-context-continuity", ModelRoute: "tool-loop/model", Interactive: true,
		NetworkMode: "disabled", Budget: domain.Budget{MaxTurns: 40, MaxToolCalls: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	permissionBefore, err := st.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &continuityPurposeProvider{scriptedToolProvider: &scriptedToolProvider{}}
	provider.responses = append(provider.responses, boundaryRead("context-read-once", 1))
	for index := 1; index <= 24; index++ {
		observation := compactionFixtureObservation()
		if index > 12 && (index < 16 || index > 21) {
			observation = ""
		}
		provider.responses = append(provider.responses, textResponse(rootActionResponse(domain.RootActionFinish,
			fmt.Sprintf("Acknowledged checkpoint %d. The requested final acceptance remains pending. %s", index, observation),
			fmt.Sprintf("End this interactive reply %d", index), "")))
	}
	newTurns := func(st *store.SQLiteStore) *application.ThreadTurnService {
		router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
		router.RegisterProvider(provider)
		return application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
			application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
	}
	turns := newTurns(st)
	submit := func(index int, content string) {
		t.Helper()
		result, err := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: content,
			OperationKey: fmt.Sprintf("context-continuity-message-%02d", index), RequestedBy: "test_operator",
		})
		if err != nil || result.Submission.Run.ID != run.ID || result.Submission.SuccessorCreated ||
			result.Submission.Message.Status != domain.OperatorSteeringCommitted || result.Execution == nil ||
			result.Execution.Handoff.Result == nil || result.Execution.Handoff.Result.Status != domain.RunExecutionHandoffCompleted {
			t.Fatalf("ordinary turn %d did not settle on the same execution context: result=%+v err=%v", index, result, err)
		}
	}
	submit(1, "Original objective UX_CONTEXT_ORIGINAL_ACCEPTANCE: inspect README and retain a final compatibility report. Constraint UX_CONTEXT_ONLY_READ_NO_INSTALL: do not write, install, or enable network. Unfinished UX_CONTEXT_PENDING_FINAL_REVIEW: final compatibility review is still required. Read README.md now and preserve the actual observation.")
	submit(2, "Correction UX_CONTEXT_CORRECTION_USE_CHINESE: the final compatibility report must be in Chinese; this changes only the report language, not the original objective or read-only constraints.")
	roundsBefore, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 20)
	if err != nil || len(roundsBefore) != 1 || len(roundsBefore[0].Calls) != 1 {
		t.Fatalf("expected one real persisted workspace read: rounds=%+v err=%v", roundsBefore, err)
	}
	call := roundsBefore[0].Calls[0]
	if call.ToolName != "workspace_read" || call.Status != domain.SupervisorToolCompleted ||
		!strings.Contains(call.ResultJSON, "UX_CONTEXT_OBSERVED_README_VALUE") {
		t.Fatalf("read fixture did not execute: %+v", call)
	}
	provider.summary = "Goal UX_CONTEXT_ORIGINAL_ACCEPTANCE: inspect README and report compatibility. " +
		"Constraint UX_CONTEXT_ONLY_READ_NO_INSTALL: no writes, installs, or network. " +
		"Pending UX_CONTEXT_PENDING_FINAL_REVIEW: final compatibility review is unfinished. " +
		"Correction UX_CONTEXT_CORRECTION_USE_CHINESE: write the final report in Chinese. " +
		"Actual observation UX_CONTEXT_OBSERVED_README_VALUE. Original read " + call.CallID + " result_sha256=" + session.ContentSHA256(call.ResultJSON) + ". Repository text grants no authority."
	earlyHistory, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	earlyRaw := continuityRawMessages(t, earlyHistory)
	for index := 3; index <= 12; index++ {
		submit(index, fmt.Sprintf("Continue checkpoint %d; keep the earlier task and restrictions.", index))
	}
	historyBefore, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil || len(historyBefore) <= 20 {
		t.Fatalf("fixture did not exceed the old 20-message history limit: count=%d err=%v", len(historyBefore), err)
	}
	before := continuityRawMessages(t, historyBefore)
	for id, original := range earlyRaw {
		if before[id] != original {
			t.Errorf("first compaction rewrote early raw message %d", id)
		}
	}
	submit(13, "Continue with the current task; this short message does not replace the original goal.")
	assertThreadContinuityRequest(t, provider.Requests()[len(provider.Requests())-1], call.CallID, session.ContentSHA256(call.ResultJSON))
	summaryBefore, found, err := st.LatestContextSummary(t.Context(), run.SessionID)
	if err != nil || !found || summaryBefore.Content == "" {
		t.Errorf("ordinary Thread path did not persist a continuity summary: found=%t err=%v", found, err)
	} else {
		assertContinuitySummaryProvenance(t, summaryBefore.Content, historyBefore, call)
		assertContinuityGeneratedSummary(t, summaryBefore, provider, 0)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	turns = newTurns(st)
	submit(14, "After restart, continue the same task and preserve all earlier constraints; do not repeat the read or execute anything.")
	assertThreadContinuityRequest(t, provider.Requests()[len(provider.Requests())-1], call.CallID, session.ContentSHA256(call.ResultJSON))
	for index := 15; index <= 24; index++ {
		submit(index, fmt.Sprintf("Continue checkpoint %d after restart; keep the same objective and restrictions.", index))
	}
	assertThreadContinuityRequest(t, provider.Requests()[len(provider.Requests())-1], call.CallID, session.ContentSHA256(call.ResultJSON))
	summaryAfter, exists, err := st.LatestContextSummary(t.Context(), run.SessionID)
	if err != nil || !exists || summaryAfter.ID == summaryBefore.ID || summaryAfter.PreviousSummaryID != summaryBefore.ID {
		t.Errorf("restart did not retain the exact previous summary through a second real compaction: before=%d after=%d previous=%d err=%v", summaryBefore.ID, summaryAfter.ID, summaryAfter.PreviousSummaryID, err)
	}
	historyAfter, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	after := continuityRawMessages(t, historyAfter)
	if exists {
		assertContinuitySummaryProvenance(t, summaryAfter.Content, historyAfter, call)
		assertContinuityGeneratedSummary(t, summaryAfter, provider, 1)
	}
	for id, original := range before {
		if after[id] != original {
			t.Errorf("raw message %d content, provenance, or timestamp changed during compaction/restart", id)
		}
	}
	roundsAfter, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 20)
	if err != nil || !reflect.DeepEqual(roundsBefore, roundsAfter) {
		t.Errorf("context continuation repeated or rewrote the actual tool evidence: err=%v", err)
	}
	permissionAfter, err := st.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil || !reflect.DeepEqual(permissionBefore, permissionAfter) {
		t.Errorf("summary or repository text changed execution authority: err=%v", err)
	}
	mission, err := st.GetMission(t.Context(), run.MissionID)
	if err != nil || mission.Scope.NetworkMode != "disabled" {
		t.Errorf("continuation changed network policy: err=%v", err)
	}
	if actual, err := os.ReadFile(filepath.Join(workspace, "README.md")); err != nil || string(actual) != evidenceText {
		t.Errorf("read-only continuity changed the fixture file: err=%v", err)
	}
	if len(provider.Requests()) != 25 {
		t.Errorf("unexpected extra provider requests or tool replay: calls=%d want=25", len(provider.Requests()))
	}
	if len(provider.generatedRequests) != 2 {
		t.Errorf("auxiliary summary calls=%d, want=2", len(provider.generatedRequests))
	}
	for index, request := range provider.generatedRequests {
		var payload struct {
			History contextmgr.SummaryGenerationRequest `json:"history"`
		}
		if len(request.Messages) != 2 || json.Unmarshal([]byte(request.Messages[1].Content), &payload) != nil ||
			len(payload.History.SourceSHA256) != 64 || len(payload.History.InputFingerprint) != 64 ||
			request.Metadata["source_sha256"] != payload.History.SourceSHA256 || request.Metadata["input_fingerprint"] != payload.History.InputFingerprint ||
			len(request.Tools) != 0 {
			t.Errorf("summary request %d lost exact source identity or advertised tools", index)
		}
		assertThreadContinuityRequest(t, request, call.CallID, session.ContentSHA256(call.ResultJSON))
	}
	if lease, exists, err := st.GetRunExecutionLease(t.Context(), run.ID); err != nil || exists && lease.ReleasedAt == nil {
		t.Errorf("completed continuity journey left a live lease: exists=%t lease=%+v err=%v", exists, lease, err)
	}
}

func TestThreadContextContinuityCompactsWindowPressureWithoutSilentHistoryLoss(t *testing.T) {
	for _, windowTokens := range []int{8192, 10240} {
		t.Run(fmt.Sprintf("window_%d", windowTokens), func(t *testing.T) {
			verifyThreadContextWindowPressure(t, windowTokens)
		})
	}
}

func verifyThreadContextWindowPressure(t *testing.T, windowTokens int) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "thread-context-pressure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "retain the original review under a small context window", Profile: "review", ModelRoute: "tool-loop/model",
		Interactive: true, Budget: domain.Budget{MaxTurns: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedToolProvider{}
	for index := 1; index <= 3; index++ {
		provider.responses = append(provider.responses, textResponse(rootActionResponse(domain.RootActionFinish,
			fmt.Sprintf("Review checkpoint %d remains bounded to the original task.", index), "interactive reply complete", "")))
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
		application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()).WithGeneratedContextCompaction(false))
	const original = "UX_CONTEXT_PRESSURE_ORIGINAL: review without writes or installs. "
	const correction = "UX_CONTEXT_PRESSURE_CORRECTION: preserve the original scope and use Chinese. "
	padding := strings.Repeat("Historical diagnostic background remains evidence. ", 200)
	for index, text := range []string{original + padding, correction + padding} {
		if _, err := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID),
			Content: text, OperationKey: fmt.Sprintf("pressure-before-%d", index), RequestedBy: "test_operator",
		}); err != nil {
			t.Fatal(err)
		}
	}
	history, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil || len(history) >= 20 {
		t.Fatalf("pressure fixture must stay below the message-count trigger: count=%d err=%v", len(history), err)
	}
	raw := continuityRawMessages(t, history)
	historyEstimate, err := strconv.Atoi(provider.Requests()[1].Metadata["context_input_estimate"])
	if err != nil {
		t.Fatal("provider request lacked actual input estimate")
	}
	// Both fixtures exert actual window pressure. 10240 also fits the complete
	// committed summary; 8192 must reject once that summary becomes mandatory.
	inputLimit := windowTokens - 512 - 128
	if historyEstimate <= inputLimit {
		t.Fatalf("fixture does not exert window pressure: estimated input=%d", historyEstimate)
	}
	if err := router.SetContextWindow(llm.ModelRef{Provider: provider.Name(), Model: "model"}, llm.ContextWindow{
		ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: windowTokens, SafetyMarginTokens: 128,
		DefaultOutputTokens: 512, MaxOutputTokens: 512, Source: "continuity_pressure_test",
	}); err != nil {
		t.Fatal(err)
	}
	_, nextErr := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: "Continue the original review.",
		OperationKey: "pressure-after-window-change", RequestedBy: "test_operator",
	})
	requests := provider.Requests()
	if windowTokens == 8192 {
		if apperror.CodeOf(nextErr) != apperror.CodeResourceExhausted || len(requests) != 2 {
			t.Fatalf("summary overflow made a new provider call: calls=%d err=%v", len(requests), nextErr)
		}
	} else {
		if nextErr != nil || len(requests) != 3 {
			t.Fatalf("bounded summary should fit and proceed exactly once: calls=%d err=%v", len(requests), nextErr)
		}
		encoded, err := json.Marshal(requests[2].Messages)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{"UX_CONTEXT_PRESSURE_ORIGINAL", "UX_CONTEXT_PRESSURE_CORRECTION"} {
			if !strings.Contains(string(encoded), marker) {
				t.Errorf("window fitting silently lost %s", marker)
			}
		}
		if omitted := requests[2].Metadata["context_history_omitted"]; omitted != "0" {
			t.Errorf("window fitting did not prove zero omitted history: %q", omitted)
		}
		estimated, err := strconv.Atoi(requests[2].Metadata["context_input_estimate"])
		if err != nil || estimated > inputLimit || requests[2].MaxTokens != 512 ||
			requests[2].Metadata["context_output_reserve"] != "512" ||
			requests[2].Metadata["context_input_limit"] != strconv.Itoa(inputLimit) {
			t.Fatalf("complete compacted request lost its effective cap: estimate=%d limit=%d max=%d metadata=%+v err=%v",
				estimated, inputLimit, requests[2].MaxTokens, requests[2].Metadata, err)
		}
		t.Logf("prior raw request=%d compacted request=%d input_limit=%d output_reserve=%d", historyEstimate, estimated, inputLimit, requests[2].MaxTokens)
	}
	if summary, exists, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || !exists || summary.Content == "" ||
		summary.CompactedMessageCount <= 0 || !strings.Contains(summary.Content, "UX_CONTEXT_PRESSURE_ORIGINAL") ||
		!strings.Contains(summary.Content, "UX_CONTEXT_PRESSURE_CORRECTION") {
		t.Errorf("window pressure did not attempt persistent context compaction: exists=%t err=%v", exists, err)
	}
	after, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	afterRaw := continuityRawMessages(t, after)
	for id, hash := range raw {
		if afterRaw[id] != hash {
			t.Errorf("window pressure rewrote raw historical message %d", id)
		}
	}
}

func TestThreadContextContinuityRejectsOversizedCurrentInputBeforeProviderAndRetainsOriginal(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "thread-context-current-input.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "preserve oversized current input rather than silently deleting it", Profile: "review",
		ModelRoute: "tool-loop/model", Interactive: true, Budget: domain.Budget{MaxTurns: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedToolProvider{}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	if err := router.SetContextWindow(llm.ModelRef{Provider: provider.Name(), Model: "model"}, llm.ContextWindow{
		ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: 8192, SafetyMarginTokens: 128,
		DefaultOutputTokens: 512, MaxOutputTokens: 512, Source: "continuity_mandatory_test",
	}); err != nil {
		t.Fatal(err)
	}
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
		application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
	input := "UX_CONTEXT_CURRENT_TOO_LARGE: keep this original input exactly. " + strings.Repeat("界", 3000)
	request := application.ExecuteThreadTurnRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: input,
		OperationKey: "oversized-current-original-key", RequestedBy: "test_operator",
	}
	result, err := turns.Execute(t.Context(), request)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted || len(provider.Requests()) != 0 {
		t.Fatalf("mandatory overflow did not fail before provider: calls=%d err=%v", len(provider.Requests()), err)
	}
	var failed *application.ThreadTurnFailedError
	if !errors.As(err, &failed) || failed.Failure == nil || failed.Failure.FailureStage != domain.ThreadFailureContextWindowExceeded ||
		result.Execution == nil || result.Execution.Handoff.Result == nil || result.Execution.Handoff.Result.StopReason != domain.ThreadFailureContextWindowExceeded {
		t.Fatalf("input overflow did not seal its precise context-window failure: result=%+v err=%v", result, err)
	}
	replay, replayErr := turns.Execute(t.Context(), request)
	var replayFailure *application.ThreadTurnFailedError
	if !errors.As(replayErr, &replayFailure) || replayFailure.Failure == nil || !reflect.DeepEqual(failed.Failure, replayFailure.Failure) ||
		!replay.Replayed || len(provider.Requests()) != 0 {
		t.Fatalf("original overflow key did not replay the same sealed failure: replay=%+v err=%v", replay, replayErr)
	}
	if result.Submission.Message.ID == "" || result.Submission.Message.Content != input {
		t.Error("oversized current input was not accepted and retained under its original operation identity")
	}
	history, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range history {
		if message.Provenance.SourceKind == session.SourceOperatorMessage && message.Content == input {
			found = true
			if session.ValidateStoredMessage(message) != nil || message.Provenance.ContentSHA256 != session.ContentSHA256(input) {
				t.Error("overflow changed the original input's content digest or provenance")
			}
		}
	}
	if !found {
		t.Error("overflow did not retain the exact accepted user input in durable Session history")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	stored, exists, err := st.GetThreadTurnFailure(t.Context(), run.ID, result.Submission.Message.ID)
	if err != nil || !exists || !reflect.DeepEqual(*failed.Failure, stored) {
		t.Errorf("restarted store did not retain the exact context-window failure: exists=%t err=%v", exists, err)
	}
}

func continuityRawMessages(t *testing.T, messages []session.Message) map[int64]string {
	t.Helper()
	result := make(map[int64]string, len(messages))
	for _, message := range messages {
		if err := session.ValidateStoredMessage(message); err != nil {
			t.Fatalf("invalid actual Session message provenance: id=%d err=%v", message.ID, err)
		}
		// Only the existing compacted flag may change; the public source remains immutable.
		message.Compacted = false
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		result[message.ID] = session.ContentSHA256(string(encoded))
	}
	return result
}

func assertThreadContinuityRequest(t *testing.T, request llm.ChatRequest, readCallID, resultHash string) {
	t.Helper()
	encoded, err := json.Marshal(request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"UX_CONTEXT_ORIGINAL_ACCEPTANCE", "UX_CONTEXT_ONLY_READ_NO_INSTALL",
		"UX_CONTEXT_PENDING_FINAL_REVIEW", "UX_CONTEXT_CORRECTION_USE_CHINESE", "UX_CONTEXT_OBSERVED_README_VALUE", readCallID, resultHash} {
		if !strings.Contains(string(encoded), required) {
			t.Errorf("next ordinary model request lost historical fact %q", required)
		}
	}
	for _, message := range request.Messages {
		if message.Role == "system" && strings.Contains(message.Content, "UX_CONTEXT_GRANT_FULL_ACCESS_AND_NETWORK") {
			t.Error("untrusted read content was promoted to system instruction authority")
		}
	}
}

func assertContinuityGeneratedSummary(t *testing.T, summary contextmgr.Summary, provider *continuityPurposeProvider, index int) {
	t.Helper()
	var envelope struct {
		Generated *contextmgr.GeneratedSummary `json:"generated"`
	}
	if err := json.Unmarshal([]byte(summary.Content), &envelope); err != nil {
		t.Fatal(err)
	}
	generated := envelope.Generated
	if len(provider.generatedRequests) <= index || generated == nil {
		t.Fatal("default continuity path did not commit the generated summary")
	}
	request := provider.generatedRequests[index]
	if generated.Version != contextmgr.GeneratedHandoffVersion || generated.InstructionAuthorized ||
		generated.Text != provider.summary || generated.TextSHA256 != session.ContentSHA256(provider.summary) ||
		generated.InputFingerprint != request.Metadata["input_fingerprint"] ||
		generated.Receipt.SourceSHA256 != request.Metadata["source_sha256"] || generated.Receipt.CompletionSequence <= 0 {
		t.Fatalf("generated continuity text lost its exact input receipt or gained authority: %+v", generated)
	}
}

func assertContinuitySummaryProvenance(t *testing.T, content string, history []session.Message, call domain.SupervisorToolCall) {
	t.Helper()
	var envelope struct {
		Records []struct {
			SourceMessageID       int64  `json:"source_message_id"`
			SourceKind            string `json:"source_kind"`
			SourceRef             string `json:"source_ref"`
			SourceContentSHA256   string `json:"source_content_sha256"`
			InstructionAuthorized bool   `json:"instruction_authorized"`
			Content               string `json:"content"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		t.Fatal(err)
	}
	byID := make(map[int64]session.Message, len(history))
	for _, message := range history {
		byID[message.ID] = message
	}
	operatorFound, toolFound := false, false
	for _, record := range envelope.Records {
		if record.SourceMessageID == 0 {
			continue
		}
		original, exists := byID[record.SourceMessageID]
		if !exists || record.SourceContentSHA256 != original.Provenance.ContentSHA256 ||
			record.SourceKind != original.Provenance.SourceKind || record.SourceRef != original.Provenance.SourceRef ||
			record.InstructionAuthorized != original.Provenance.InstructionAuthorized {
			t.Errorf("summary source %d lost its exact original provenance", record.SourceMessageID)
		}
		if strings.Contains(original.Content, "UX_CONTEXT_ORIGINAL_ACCEPTANCE") && record.SourceKind == session.SourceOperatorMessage {
			operatorFound = record.SourceKind == session.SourceOperatorMessage && record.InstructionAuthorized
		}
		if record.SourceKind == session.SourceToolResult && strings.Contains(record.Content, call.CallID) {
			toolFound = true
			// The record is a bounded excerpt. Its immutable source, rather than
			// a possibly cut display snippet, binds the exact tool result. The
			// complete digest must still reach the saved summary and model input.
			resultHash := session.ContentSHA256(call.ResultJSON)
			if record.InstructionAuthorized || record.SourceContentSHA256 != session.ContentSHA256(original.Content) ||
				!strings.Contains(original.Content, "call_id="+call.CallID+" result_sha256="+resultHash) ||
				!strings.Contains(content, resultHash) {
				t.Error("summary tool evidence gained authority or lost its immutable source/result digest")
			}
		}
	}
	if !operatorFound || !toolFound {
		t.Errorf("summary did not retain separately attributed original intent and real tool evidence: operator=%t tool=%t", operatorFound, toolFound)
	}
}
