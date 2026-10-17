package application_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/coordinator"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/testfixtures/legacyskill"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSubagentRunnerExecutesInternalNoToolContinuation(t *testing.T) {
	provider := &specialistTestProvider{responses: []llm.ChatResponse{{
		Text: specialistResponse(t, domain.SpecialistAction{
			Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
			Message: "continue the focused review",
		}), Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	}}}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 2, 32)
	result, err := runner.Step(context.Background(), run.ID, child.ID)
	if err != nil || result.AttemptStatus != domain.AgentAttemptContinued ||
		result.Action.Kind != domain.SpecialistActionContinue || result.Usage.TotalTokens != 5 ||
		result.ModelOutcome != llm.OutcomeSuccess || result.ModelAttempts != 1 {
		t.Fatalf("Specialist continuation failed: result=%#v err=%v", result, err)
	}
	updated, err := st.GetAgentNode(context.Background(), child.ID)
	if err != nil || updated.Status != domain.AgentReady || updated.TurnsUsed != 1 ||
		updated.TokensUsed != 5 || updated.ActiveAttemptID != "" {
		t.Fatalf("continued Specialist projection is invalid: child=%#v err=%v", updated, err)
	}
	messages, err := st.ListSessionMessages(context.Background(), child.SessionID, true)
	if err != nil || len(messages) != 2 || messages[0].Role != "user" ||
		messages[1].Role != "assistant" || messages[1].Content != "continue the focused review" {
		t.Fatalf("Specialist Session history is invalid: messages=%#v err=%v", messages, err)
	}
	if len(provider.requests) != 1 || len(provider.requests[0].Tools) != 0 ||
		!provider.requests[0].JSONMode ||
		provider.requests[0].Metadata["response_schema"] != domain.SpecialistLifecycleVersion ||
		provider.requests[0].Metadata["context_window_source"] != "conservative_default" ||
		provider.requests[0].Metadata["context_input_estimate"] == "" ||
		provider.requests[0].MaxTokens != 32 {
		t.Fatalf("Specialist provider request escaped its no-tool budget: %#v", provider.requests)
	}
	if result.SkillItems != 1 || result.SkillTokens <= 0 ||
		result.SkillBudget != skills.DefaultSpecialistContextTokenBudget ||
		result.SkillRecovered || provider.requests[0].Metadata["skill_items"] != "1" {
		t.Fatalf("minimal Specialist Skill context was not delivered: %#v request=%#v",
			result, provider.requests[0])
	}
	var codeSkill, planSkill bool
	for _, message := range provider.requests[0].Messages {
		codeSkill = codeSkill || strings.Contains(message.Content, "Code workflow")
		planSkill = planSkill || strings.Contains(message.Content, "Plan/Delivery workflow")
	}
	if !codeSkill || planSkill {
		t.Fatalf("Specialist Skill minimization drifted: code=%t plan=%t messages=%#v",
			codeSkill, planSkill, provider.requests[0].Messages)
	}
	assertSpecialistEventCounts(t, st, run.ID, map[string]int{
		events.ModelStartedEvent: 1, events.ModelCompletedEvent: 1,
		events.PolicyDecisionEvent: 1, events.AgentAttemptUsageRecordedEvent: 1,
		events.AgentTurnCompletedEvent:              1,
		events.SpecialistSkillContextPreparedEvent:  1,
		events.SpecialistSkillContextCommittedEvent: 1,
	})
}

func TestSubagentRunnerDeliversOnlyOperatorDesignatedExternalSkill(t *testing.T) {
	provider := &specialistTestProvider{responses: []llm.ChatResponse{{
		Text: specialistResponse(t, domain.SpecialistAction{
			Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
			Message: "used bounded external workflow guidance",
		}), Usage: llm.Usage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6},
	}}}
	st, run, child, runner := newSubagentRunnerFixtureWithExternal(t, provider,
		domain.Budget{MaxTurns: 10}, 2, 64, true)
	result, err := runner.Step(context.Background(), run.ID, child.ID)
	if err != nil || result.ExternalSkillItems != 1 || result.ExternalSkillTokens <= 0 ||
		result.ExternalSkillBudget != result.ExternalSkillTokens ||
		result.ExternalSkillBudget <= skills.DefaultExternalSpecialistTokenBudget ||
		result.ExternalSkillBudget > skills.MaxExternalSpecialistTokenBudget ||
		result.ExternalSkillRecovered {
		t.Fatalf("external Specialist Skill delivery failed: result=%#v err=%v", result, err)
	}
	if len(provider.requests) != 1 ||
		provider.requests[0].Metadata["external_skill_items"] != "1" ||
		len(provider.requests[0].Tools) != 0 {
		t.Fatalf("external Specialist request escaped its boundary: %#v", provider.requests)
	}
	found := false
	for _, message := range provider.requests[0].Messages {
		if strings.Contains(message.Content, "External Specialist review") {
			found = true
			if message.Role != "user" ||
				!strings.Contains(message.Content, `"workflow_guidance":true`) ||
				!strings.Contains(message.Content, `"tool_grant":false`) ||
				!strings.Contains(message.Content, `"file_write_grant":false`) ||
				!strings.Contains(message.Content, `"scope_expansion":false`) ||
				!strings.Contains(message.Content, `"delegation_grant":false`) ||
				!strings.Contains(message.Content, `"audience":"specialist"`) {
				t.Fatalf("external Specialist guidance authority widened: %#v", message)
			}
		}
	}
	if !found {
		t.Fatalf("designated external Specialist guidance was not delivered: %#v",
			provider.requests[0].Messages)
	}
	assertSpecialistEventCounts(t, st, run.ID, map[string]int{
		events.ExternalSpecialistSkillContextPreparedEvent:  1,
		events.ExternalSpecialistSkillContextCommittedEvent: 1,
	})
}

func TestSubagentRunnerInjectsOnlyParentInstructionAndChildOwnedMemory(t *testing.T) {
	provider := &specialistTestProvider{responses: []llm.ChatResponse{{
		Text: specialistResponse(t, domain.SpecialistAction{
			Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
			Message: "used the assigned child context",
		}), Usage: llm.Usage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
	}}}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 2, 128)
	ctx := context.Background()
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent was not found: found=%t err=%v", found, err)
	}
	workService := application.NewWorkItemService(st)
	childWork, err := workService.Create(ctx, application.CreateWorkItemRequest{
		RunID: run.ID, Title: "child-owned review", Description: "inspect bounded evidence",
		Priority: "high", OwnerAgentID: child.ID,
		AcceptanceCriteria: []string{"produce a scoped conclusion"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootWork, err := workService.Create(ctx, application.CreateWorkItemRequest{
		RunID: run.ID, Title: "root-only planning", OwnerAgentID: root.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	noteService := application.NewNoteService(st)
	childNote, err := noteService.Create(ctx, application.CreateNoteRequest{
		RunID: run.ID, Title: "child evidence", Content: "bounded observation for the child",
		Category: "observation", Visibility: "owner", OwnerAgentID: child.ID, Pinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rootNote, err := noteService.Create(ctx, application.CreateNoteRequest{
		RunID: run.ID, Title: "root private note", Content: "must not reach the child",
		Category: "decision", Visibility: "root", OwnerAgentID: root.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	instructionPayload, err := json.Marshal(domain.AgentInstructionPayload{
		Version:     domain.SpecialistInstructionVersion,
		Instruction: "Use the child-owned work and note; do not expand scope.",
	})
	if err != nil {
		t.Fatal(err)
	}
	instruction, replayed, err := st.SendAgentMessage(ctx, domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, SenderAgentID: root.ID,
		RecipientAgentID: child.ID, Kind: domain.AgentMessageInstruction,
		Semantic: domain.AgentMessageSemanticMessage, PayloadJSON: string(instructionPayload),
	}, "specialist-runner-instruction-0001")
	if err != nil || replayed {
		t.Fatalf("parent instruction failed: message=%#v replayed=%t err=%v",
			instruction, replayed, err)
	}
	result, err := runner.Step(ctx, run.ID, child.ID)
	if err != nil || result.AttemptStatus != domain.AgentAttemptContinued ||
		result.ParentInstructions != 1 || result.ContextRecovered ||
		result.OwnedWorkItems != 1 || result.OwnedNotes != 1 ||
		result.ContextSources != 5 || result.ContextOmitted != 0 ||
		result.ContextTokens <= 0 || result.ContextTokens > result.ContextTokenBudget {
		t.Fatalf("Specialist child context result is invalid: result=%#v err=%v", result, err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected one Provider request, got %d", len(provider.requests))
	}
	request := provider.requests[0]
	input := request.Messages[len(request.Messages)-1].Content
	for _, required := range []string{
		domain.SpecialistContextVersion, "Use the child-owned work", childWork.ID, childNote.ID,
	} {
		if !strings.Contains(input, required) {
			t.Fatalf("Specialist input omitted %q: %s", required, input)
		}
	}
	for _, forbidden := range []string{instruction.ID, rootWork.ID, rootNote.ID,
		"root-only planning", "must not reach the child"} {
		if strings.Contains(input, forbidden) {
			t.Fatalf("Specialist input exposed forbidden context %q: %s", forbidden, input)
		}
	}
	if request.Metadata["parent_instructions"] != "1" ||
		request.Metadata["owned_work_items"] != "1" || request.Metadata["owned_notes"] != "1" ||
		request.Metadata["context_sources"] != "5" {
		t.Fatalf("Specialist context metadata is invalid: %#v", request.Metadata)
	}
	messages, err := st.ListAgentMessages(ctx, child.ID, false, 10)
	if err != nil || len(messages) != 1 || messages[0].Status != domain.AgentMessageConsumed {
		t.Fatalf("successful child turn did not consume its instruction: messages=%#v err=%v",
			messages, err)
	}
	timeline, err := st.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	modelStarted := ""
	var delivered struct {
		Fingerprint string `json:"task_brief_fingerprint"`
	}
	if err := json.Unmarshal([]byte(input), &delivered); err != nil || len(delivered.Fingerprint) != 64 {
		t.Fatalf("current child input lacks its exact task fingerprint: %v", err)
	}
	for _, event := range timeline {
		if event.Type == events.ModelStartedEvent && event.Source == "specialist_model_gateway" {
			modelStarted = event.PayloadJSON
		}
	}
	if modelStarted == "" || !strings.Contains(modelStarted, instruction.ID) ||
		!strings.Contains(modelStarted, childWork.ID) || !strings.Contains(modelStarted, childNote.ID) ||
		strings.Contains(modelStarted, "Use the child-owned work") ||
		strings.Contains(modelStarted, "bounded observation for the child") {
		t.Fatalf("Specialist context provenance leaked content or lost source IDs: %s", modelStarted)
	}
	var provenance struct {
		Context llm.ModelContextAudit `json:"context"`
	}
	if err := json.Unmarshal([]byte(modelStarted), &provenance); err != nil {
		t.Fatal(err)
	}
	bound := false
	for _, source := range provenance.Context.Included {
		bound = bound || source.Kind == "specialist_task_brief" && source.SourceID == delivered.Fingerprint
	}
	if !bound {
		t.Fatal("model start receipt differs from the actual delivered task fingerprint")
	}
}

func TestSubagentRunnerFinishesWithCompletionReport(t *testing.T) {
	report := domain.CompletionReport{
		Version: domain.CompletionReportVersion, Outcome: domain.CompletionSucceeded,
		Summary: "review completed safely", WorkItemIDs: []string{}, NoteIDs: []string{},
	}
	provider := &specialistTestProvider{responses: []llm.ChatResponse{{
		Text: specialistResponse(t, domain.SpecialistAction{
			Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionFinish,
			Message: "the assigned review is complete", Report: &report,
		}), Usage: llm.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
	}}}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 2, 32)
	result, err := runner.Step(context.Background(), run.ID, child.ID)
	if err != nil || result.AttemptStatus != domain.AgentAttemptFinished ||
		result.Completion.Report.Outcome != domain.CompletionSucceeded ||
		result.Completion.AttemptID != result.AttemptID {
		t.Fatalf("Specialist finish failed: result=%#v err=%v", result, err)
	}
	updated, err := st.GetAgentNode(context.Background(), child.ID)
	if err != nil || updated.Status != domain.AgentCompleted || updated.FinishedAt == nil {
		t.Fatalf("finished Specialist projection is invalid: child=%#v err=%v", updated, err)
	}
	childSession, err := st.GetSession(context.Background(), child.SessionID)
	if err != nil || childSession.Status != session.StatusArchived {
		t.Fatalf("finished Specialist Session was not archived: session=%#v err=%v",
			childSession, err)
	}
	root, found, err := st.GetRootAgent(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent was not found: found=%t err=%v", found, err)
	}
	inbox, err := st.ListAgentMessages(context.Background(), root.ID, true, 10)
	if err != nil || len(inbox) != 1 || inbox[0].Kind != domain.AgentMessageResult {
		t.Fatalf("completion did not reach root inbox: messages=%#v err=%v", inbox, err)
	}
}

func TestSubagentRunnerRetriesTransportFailureAndChargesOnce(t *testing.T) {
	provider := &specialistTestProvider{
		failures: []error{llm.NewProviderError(llm.OutcomeRetryable,
			"specialist-test", "temporary reset", nil), nil},
		responses: []llm.ChatResponse{{}, {
			Text: specialistResponse(t, domain.SpecialistAction{
				Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
				Message: "recovered after retry",
			}), Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
		}},
	}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 2, 32)
	runner.WithModelRetryPolicy(application.ModelRetryPolicy{MaxAttempts: 2})
	result, err := runner.Step(context.Background(), run.ID, child.ID)
	if err != nil || result.ModelAttempts != 2 || result.Usage.TotalTokens != 4 ||
		result.AttemptStatus != domain.AgentAttemptContinued {
		t.Fatalf("Specialist retry failed: result=%#v err=%v", result, err)
	}
	assertSpecialistEventCounts(t, st, run.ID, map[string]int{
		events.ModelStartedEvent: 2, events.ModelFailedEvent: 1,
		events.ModelCompletedEvent: 1, events.AgentAttemptUsageRecordedEvent: 1,
	})
}

func TestSubagentRunnerRepairsLifecycleOnceWithoutPersistingInvalidOutput(t *testing.T) {
	secret := "raw-invalid-output-marker-" + strings.Repeat("q", 24)
	valid := domain.SpecialistAction{
		Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
		Message: "continue after the bounded protocol repair",
	}
	provider := &specialistTestProvider{responses: []llm.ChatResponse{
		{Text: "not-json " + secret,
			Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}},
		{Text: specialistResponse(t, valid),
			Usage: llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}},
	}}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 2, 32)
	result, err := runner.Step(context.Background(), run.ID, child.ID)
	if err != nil || result.AttemptStatus != domain.AgentAttemptContinued ||
		result.ProtocolRepairs != 1 || result.ModelAttempts != 2 ||
		result.ModelOutcome != llm.OutcomeSuccess || result.Usage.TotalTokens != 9 {
		t.Fatalf("Specialist repair did not complete exactly once: result=%#v err=%v",
			result, err)
	}
	if len(provider.requests) != 2 ||
		provider.requests[1].Metadata["protocol_repair"] != "1" ||
		len(provider.requests[1].Tools) != 0 ||
		provider.requests[1].MaxTokens >= provider.requests[0].MaxTokens {
		t.Fatalf("repair request did not preserve its bounded no-tool shape: %#v",
			provider.requests)
	}
	for _, message := range provider.requests[1].Messages {
		if strings.Contains(message.Content, secret) {
			t.Fatalf("repair request retained raw invalid output: %#v", message)
		}
	}
	messages, err := st.ListSessionMessages(context.Background(), child.SessionID, true)
	if err != nil || len(messages) != 2 || messages[1].Content != valid.Message {
		t.Fatalf("repair did not commit only the valid exchange: messages=%#v err=%v",
			messages, err)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, secret) {
			t.Fatalf("raw invalid output entered Session history: %#v", messages)
		}
	}
	repair, found, err := st.GetSpecialistProtocolRepair(context.Background(), result.AttemptID)
	if err != nil || !found || repair.Status != domain.SpecialistRepairCompleted ||
		repair.RequestedModelAttempt != 1 || repair.ResolvedModelAttempt != 2 {
		t.Fatalf("repair ledger is incomplete: repair=%#v found=%t err=%v",
			repair, found, err)
	}
	timeline, err := st.ListRunEvents(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range timeline {
		if strings.Contains(event.PayloadJSON, secret) {
			t.Fatalf("raw invalid output entered the audit stream: %#v", event)
		}
	}
	assertSpecialistEventCounts(t, st, run.ID, map[string]int{
		events.AgentProtocolRepairRequestedEvent: 1,
		events.AgentProtocolRepairStartedEvent:   1,
		events.AgentProtocolRepairCompletedEvent: 1,
	})
}

func TestSubagentRunnerKeepsTransportRetriesIndependentAcrossRepair(t *testing.T) {
	valid := domain.SpecialistAction{
		Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
		Message: "continue after independent transport retries",
	}
	provider := &specialistTestProvider{
		failures: []error{
			llm.NewProviderError(llm.OutcomeRetryable, "specialist-test", "primary reset", nil),
			nil,
			llm.NewProviderError(llm.OutcomeRetryable, "specialist-test", "repair reset", nil),
			nil,
		},
		responses: []llm.ChatResponse{
			{},
			{Text: `{"action":"continue"}`,
				Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}},
			{},
			{Text: specialistResponse(t, valid),
				Usage: llm.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}},
		},
	}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 2, 32)
	runner.WithModelRetryPolicy(application.ModelRetryPolicy{MaxAttempts: 2})
	result, err := runner.Step(context.Background(), run.ID, child.ID)
	if err != nil || result.AttemptStatus != domain.AgentAttemptContinued ||
		result.ModelAttempts != 4 || result.ProtocolRepairs != 1 ||
		result.Usage.TotalTokens != 5 {
		t.Fatalf("independent Specialist retries failed: result=%#v err=%v", result, err)
	}
	timeline, err := st.ListRunEvents(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var phases [][2]int
	for _, event := range timeline {
		if event.Type != events.ModelStartedEvent ||
			event.Source != "specialist_model_gateway" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
			t.Fatal(err)
		}
		phases = append(phases, [2]int{
			int(payload["protocol_repair"].(float64)),
			int(payload["transport_attempt"].(float64)),
		})
	}
	want := [][2]int{{0, 1}, {0, 2}, {1, 1}, {1, 2}}
	if len(phases) != len(want) {
		t.Fatalf("unexpected model phase ledger: %#v", phases)
	}
	for index := range want {
		if phases[index] != want[index] {
			t.Fatalf("model phase %d=%v want=%v", index, phases[index], want[index])
		}
	}
}

func TestSubagentRunnerAbortsRepairWhenTotalTokenBudgetIsExhausted(t *testing.T) {
	provider := &specialistTestProvider{responses: []llm.ChatResponse{{
		Text:  `{"action":"continue"}`,
		Usage: llm.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3},
	}}}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10, MaxTokens: 3}, 2, 2)
	result, err := runner.Step(context.Background(), run.ID, child.ID)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted ||
		result.AttemptStatus != domain.AgentAttemptCrashed ||
		result.ModelAttempts != 1 || result.ProtocolRepairs != 1 ||
		result.Usage.TotalTokens != 3 || len(provider.requests) != 1 {
		t.Fatalf("exhausted repair budget was not contained: result=%#v code=%s err=%v",
			result, apperror.CodeOf(err), err)
	}
	repair, found, loadErr := st.GetSpecialistProtocolRepair(context.Background(), result.AttemptID)
	if loadErr != nil || !found || repair.Status != domain.SpecialistRepairAborted {
		t.Fatalf("budget-aborted repair is not durable: repair=%#v found=%t err=%v",
			repair, found, loadErr)
	}
	assertSpecialistEventCounts(t, st, run.ID, map[string]int{
		events.AgentProtocolRepairRequestedEvent: 1,
		events.AgentProtocolRepairStartedEvent:   0,
		events.AgentProtocolRepairFailedEvent:    1,
	})
}

func TestSubagentRunnerCancellationAbortsPendingRepair(t *testing.T) {
	provider := &specialistTestProvider{
		responses: []llm.ChatResponse{{
			Text:  `{"action":"continue"}`,
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		}, {}},
		block: true, blockAt: 1, started: make(chan struct{}),
	}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 2, 32)
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result application.SpecialistTurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runner.Step(ctx, run.ID, child.ID)
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("Specialist repair provider did not start")
	}
	cancel()
	select {
	case got := <-done:
		if apperror.CodeOf(got.err) != apperror.CodeCancelled ||
			got.result.AttemptStatus != domain.AgentAttemptCrashed ||
			got.result.ProtocolRepairs != 1 || got.result.ModelAttempts != 2 ||
			got.result.ModelOutcome != llm.OutcomeCancelled ||
			got.result.Usage.TotalTokens != 2 {
			t.Fatalf("cancelled repair was not contained: result=%#v code=%s err=%v",
				got.result, apperror.CodeOf(got.err), got.err)
		}
		repair, found, loadErr := st.GetSpecialistProtocolRepair(context.Background(),
			got.result.AttemptID)
		if loadErr != nil || !found || repair.Status != domain.SpecialistRepairAborted {
			t.Fatalf("cancelled repair is not durable: repair=%#v found=%t err=%v",
				repair, found, loadErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Specialist repair did not return")
	}
}

func TestSubagentRunnerRefusesContinueAfterChildBudgetExhaustion(t *testing.T) {
	provider := &specialistTestProvider{responses: []llm.ChatResponse{{
		Text: specialistResponse(t, domain.SpecialistAction{
			Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
			Message: "request another turn",
		}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}}}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 1, 16)
	result, err := runner.Step(context.Background(), run.ID, child.ID)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted ||
		result.AttemptStatus != domain.AgentAttemptCrashed || result.Usage.TotalTokens != 2 {
		t.Fatalf("exhausted child controlled its own continuation: result=%#v code=%s err=%v",
			result, apperror.CodeOf(err), err)
	}
	updated, loadErr := st.GetAgentNode(context.Background(), child.ID)
	if loadErr != nil || updated.Status != domain.AgentFailed || updated.FinishedAt == nil {
		t.Fatalf("exhausted child was not terminated: child=%#v err=%v", updated, loadErr)
	}
	childSession, loadErr := st.GetSession(context.Background(), child.SessionID)
	if loadErr != nil || childSession.Status != session.StatusArchived {
		t.Fatalf("exhausted child Session was not archived: session=%#v err=%v",
			childSession, loadErr)
	}
}

func TestSubagentRunnerRejectsMalformedToolAndDangerousResponses(t *testing.T) {
	tests := []struct {
		name      string
		response  llm.ChatResponse
		wantCode  apperror.Code
		wantModel llm.Outcome
	}{
		{
			name: "malformed lifecycle",
			response: llm.ChatResponse{Text: `{"action":"continue"}`,
				Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}},
			wantCode: apperror.CodeFailedPrecondition, wantModel: llm.OutcomeInvalidResponse,
		},
		{
			name: "tool call",
			response: llm.ChatResponse{
				Text: specialistResponse(t, domain.SpecialistAction{
					Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
					Message: "request a tool",
				}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
				ToolCalls: []llm.ToolCall{{
					ID: "call-1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo hi"}`),
				}},
			},
			wantCode: apperror.CodeFailedPrecondition, wantModel: llm.OutcomeInvalidResponse,
		},
		{
			name: "policy denial",
			response: llm.ChatResponse{
				Text: specialistResponse(t, domain.SpecialistAction{
					Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
					Message: "run masscan against 0.0.0.0/0",
				}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
			},
			wantCode: apperror.CodePolicyDenied, wantModel: llm.OutcomeSuccess,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			responses := []llm.ChatResponse{test.response}
			if test.wantModel == llm.OutcomeInvalidResponse {
				responses = append(responses, test.response)
			}
			provider := &specialistTestProvider{responses: responses}
			st, run, child, runner := newSubagentRunnerFixture(t, provider,
				domain.Budget{MaxTurns: 10}, 2, 32)
			result, err := runner.Step(context.Background(), run.ID, child.ID)
			if apperror.CodeOf(err) != test.wantCode ||
				result.AttemptStatus != domain.AgentAttemptCrashed ||
				result.ModelOutcome != test.wantModel || result.Usage.TotalTokens <= 0 {
				t.Fatalf("unsafe response was not contained: result=%#v code=%s err=%v",
					result, apperror.CodeOf(err), err)
			}
			if test.wantModel == llm.OutcomeInvalidResponse &&
				(result.ProtocolRepairs != 1 || result.ModelAttempts != 2) {
				t.Fatalf("invalid response did not use exactly one repair: %#v", result)
			}
			messages, err := st.ListSessionMessages(context.Background(), child.SessionID, true)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "policy denial" && len(messages) != 0 {
				t.Fatalf("policy-denied output entered Session history: %#v", messages)
			}
			timeline, err := st.ListRunEvents(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range timeline {
				if strings.HasPrefix(event.Type, "tool.") {
					t.Fatalf("unsafe child response executed a tool: %#v", event)
				}
			}
		})
	}
}

func TestSubagentRunnerCancellationCrashesAttemptBeforeLeaseRelease(t *testing.T) {
	provider := &specialistTestProvider{block: true, started: make(chan struct{})}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 2, 32)
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result application.SpecialistTurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runner.Step(ctx, run.ID, child.ID)
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-provider.started:
	case <-time.After(3 * time.Second):
		t.Fatal("Specialist provider did not start")
	}
	cancel()
	select {
	case got := <-done:
		if apperror.CodeOf(got.err) != apperror.CodeCancelled ||
			got.result.AttemptStatus != domain.AgentAttemptCrashed ||
			got.result.ModelOutcome != llm.OutcomeCancelled {
			t.Fatalf("cancelled Specialist did not commit failure: result=%#v code=%s err=%v",
				got.result, apperror.CodeOf(got.err), got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Specialist did not return")
	}
	updated, err := st.GetAgentNode(context.Background(), child.ID)
	if err != nil || updated.Status != domain.AgentReady || updated.ActiveAttemptID != "" {
		t.Fatalf("cancelled child was left running: child=%#v err=%v", updated, err)
	}
	lease, found, err := st.GetRunExecutionLease(context.Background(), run.ID)
	if err != nil || !found || lease.Status != domain.RunExecutionLeaseReleased {
		t.Fatalf("cancelled child lease was not released: lease=%#v found=%t err=%v",
			lease, found, err)
	}
}

func TestSubagentRunnerRecoversExpiredWorkerBeforeFreshTurn(t *testing.T) {
	provider := &specialistTestProvider{responses: []llm.ChatResponse{{
		Text: specialistResponse(t, domain.SpecialistAction{
			Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue,
			Message: "fresh worker resumed safely",
		}), Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}}}
	st, run, child, runner := newSubagentRunnerFixture(t, provider,
		domain.Budget{MaxTurns: 10}, 3, 32)
	ctx := context.Background()
	oldLease, err := st.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: run.ID, OwnerID: "expired-specialist-worker", TTL: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent was not found: found=%t err=%v", found, err)
	}
	oldAttemptID := idgen.New("attempt")
	oldAttempt, _, err := st.BeginSpecialistAttempt(ctx, domain.AgentAttemptStart{
		AttemptID: oldAttemptID, RunID: run.ID, AgentID: child.ID,
		ParentAgentID: root.ID, Lease: oldLease.Lease, StartedAt: time.Now().UTC(),
	}, "expired-specialist-start-0001")
	if err != nil {
		t.Fatal(err)
	}
	instructionPayload, err := json.Marshal(domain.AgentInstructionPayload{
		Version:     domain.SpecialistInstructionVersion,
		Instruction: "Preserve this parent instruction across lease takeover.",
	})
	if err != nil {
		t.Fatal(err)
	}
	instruction, replayed, err := st.SendAgentMessage(ctx, domain.AgentMessage{
		ID: idgen.New("agentmsg"), RunID: run.ID, SenderAgentID: root.ID,
		RecipientAgentID: child.ID, Kind: domain.AgentMessageInstruction,
		Semantic: domain.AgentMessageSemanticMessage, PayloadJSON: string(instructionPayload),
	}, "expired-specialist-instruction-0001")
	if err != nil || replayed {
		t.Fatalf("takeover instruction failed: message=%#v replayed=%t err=%v",
			instruction, replayed, err)
	}
	if _, err := st.PrepareSpecialistContext(ctx, domain.AgentAttemptRef{
		RunID: run.ID, AgentID: child.ID, AttemptID: oldAttempt.ID,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(oldLease.Lease.ExpiresAt) + 30*time.Millisecond)
	result, err := runner.Step(ctx, run.ID, child.ID)
	if err != nil || result.RecoveredAttempts != 1 || result.Turn != 2 ||
		result.AttemptStatus != domain.AgentAttemptContinued || result.ParentInstructions != 1 {
		t.Fatalf("fresh worker did not recover child: result=%#v err=%v", result, err)
	}
	oldAttempt, found, err = st.GetAgentAttempt(ctx, oldAttemptID)
	if err != nil || !found || oldAttempt.Status != domain.AgentAttemptCrashed ||
		oldAttempt.Failure.Code != "worker_lost" {
		t.Fatalf("expired attempt was not fenced: attempt=%#v found=%t err=%v",
			oldAttempt, found, err)
	}
	messages, err := st.ListAgentMessages(ctx, child.ID, false, 10)
	if err != nil || len(messages) != 1 || messages[0].ID != instruction.ID ||
		messages[0].Status != domain.AgentMessageConsumed {
		t.Fatalf("takeover did not deliver instruction exactly once: messages=%#v err=%v",
			messages, err)
	}
}

func newSubagentRunnerFixture(t testing.TB, provider llm.Provider, budget domain.Budget,
	turnLimit int64, tokenLimit int64,
) (*store.SQLiteStore, domain.Run, domain.AgentNode, *application.SubagentRunner) {
	return newSubagentRunnerFixtureWithExternal(t, provider, budget, turnLimit,
		tokenLimit, false)
}

func newSubagentRunnerFixtureWithExternal(t testing.TB, provider llm.Provider,
	budget domain.Budget, turnLimit int64, tokenLimit int64, withExternal bool, workspaceRoots ...string,
) (*store.SQLiteStore, domain.Run, domain.AgentNode, *application.SubagentRunner) {
	return newScopedSubagentFixture(t, provider, budget, turnLimit, tokenLimit, withExternal, domain.SpecialistAdmission{AllowedReadPaths: []string{"."}, ToolCallLimit: 64}, "", workspaceRoots...)
}

func newScopedSubagentFixture(t testing.TB, provider llm.Provider, budget domain.Budget, turnLimit, tokenLimit int64, withExternal bool, limits domain.SpecialistAdmission, home string, workspaceRoots ...string) (*store.SQLiteStore, domain.Run, domain.AgentNode, *application.SubagentRunner) {
	t.Helper()
	if home == "" {
		home = t.TempDir()
	}
	st, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	service := application.NewRunService(st)
	workspaceID := ""
	if len(workspaceRoots) > 0 {
		workspaceID = "specialist-workspace"
		if err := st.SaveWorkspace(ctx, store.WorkspaceRecord{ID: workspaceID, Name: "specialist-workspace", RootPath: workspaceRoots[0], CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	_, run, err := service.Create(ctx, application.CreateRunRequest{
		Goal: "perform a bounded Specialist review", Profile: "code",
		ModelRoute: provider.Name() + "/model", Budget: budget,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewSkillSelectionService(st, registry).Select(ctx,
		application.SelectSkillsRequest{
			RunID: run.ID, Names: []string{"code", "plan-delivery"},
			OperationKey: "specialist-runner-skills-0001", RequestedBy: "operator",
		}); err != nil {
		t.Fatal(err)
	}
	if withExternal {
		objects, err := skills.NewLocalPackageObjectStore(home)
		if err != nil {
			t.Fatal(err)
		}
		raw := buildSpecialistExternalPackage(t)
		// This fixture exercises retained legacy selections, so start from an
		// actual pre-existing intent instead of using the new-install writer.
		parsed, err := skills.ParsePackage(raw)
		if err != nil {
			t.Fatal(err)
		}
		key := runmutation.Fingerprint("skill_package_install_operation.v1", "specialist-external-import-0001")
		historical, err := skills.NewPackageInstallation(idgen.New("historical-skill"), parsed, domain.ExecutionSurfaceCode, key, "operator", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		legacyskill.Seed(t, filepath.Join(home, "cyberagent.db"), historical)
		installed, err := application.NewSkillPackageRegistryService(st, objects, registry).
			Import(ctx, application.ImportSkillPackageRequest{
				Raw: raw, Surface: domain.ExecutionSurfaceCode,
				OperationKey: "specialist-external-import-0001",
				InstalledBy:  "operator", ConfirmUntrusted: true,
			})
		if err != nil {
			t.Fatal(err)
		}
		ref := skills.FormatInstalledPackageRef(installed.Package.Installation.Name,
			installed.Package.Installation.Version)
		if _, err := application.NewExternalSkillSelectionService(st).Select(ctx,
			application.SelectExternalSkillsRequest{
				RunID: run.ID, PackageRefs: []string{ref}, SpecialistRef: ref,
				OperationKey: "specialist-external-selection-0001",
				RequestedBy:  "operator", ConfirmUntrustedContext: true,
			}); err != nil {
			t.Fatal(err)
		}
	}
	run, err = service.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := st.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent was not created: found=%t err=%v", found, err)
	}
	coord, err := coordinator.NewWithSpecialistAdmission(st,
		coordinator.SpecialistAdmissionPolicy{
			MaxChildren: max(1, limits.MaxChildren), MaxTurnsPerChild: turnLimit, MaxTokensPerChild: tokenLimit,
		})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := coord.AdmitSpecialist(ctx, coordinator.AdmitSpecialistRequest{
		RunID: run.ID, ParentAgentID: root.ID, Title: "internal model Specialist",
		Skills: []string{"model.chat"}, TurnLimit: turnLimit, TokenLimit: tokenLimit,
		AllowedReadPaths: limits.AllowedReadPaths, ToolCallLimit: limits.ToolCallLimit, CostLimitMicros: limits.CostLimitMicros,
		Worker: limits.Worker,
		IdempotencyKey: "specialist-runner-admit-0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	runner := application.NewSubagentRunner(st, router, policy.NewDefaultChecker())
	return st, run, admitted.Agent, runner
}

func buildSpecialistExternalPackage(t testing.TB) []byte {
	t.Helper()
	content := []byte("# External Specialist review\n\n" +
		strings.Repeat("Verify repository evidence before conclusions.\n", 30))
	digest := sha256.Sum256(content)
	manifest := skills.Manifest{
		Protocol: skills.ProtocolVersion, Name: "external-specialist-review", Version: "1.0.0",
		Description: "Untrusted external Specialist workflow.",
		Profiles:    []domain.Profile{domain.ProfileCode},
		ToolDependencies: []toolgateway.ToolName{
			toolgateway.ListWorkspaceTool, toolgateway.ReadFileTool,
		},
		ContentPath: skills.PackageContentPath, ContentSHA256: hex.EncodeToString(digest[:]),
		ContentBytes:           len(content),
		ContentTokenUpperBound: skills.ContentTokenUpperBound(content),
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{name: skills.PackageManifestPath, data: manifestRaw},
		{name: skills.PackageContentPath, data: content},
	} {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type specialistTestProvider struct {
	respond   func(int, llm.ChatRequest) (llm.ChatResponse, error)
	mu        sync.Mutex
	responses []llm.ChatResponse
	failures  []error
	requests  []llm.ChatRequest
	calls     int
	block     bool
	blockAt   int
	started   chan struct{}
	startOnce sync.Once
}

func (*specialistTestProvider) Name() string { return "specialist-test" }

func (*specialistTestProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: "specialist-test"}}, nil
}

func (p *specialistTestProvider) Chat(ctx context.Context,
	request llm.ChatRequest,
) (*llm.ChatResponse, error) {
	p.mu.Lock()
	index := p.calls
	p.calls++
	p.requests = append(p.requests, request)
	block := p.block && index == p.blockAt
	p.mu.Unlock()
	if block {
		p.startOnce.Do(func() { close(p.started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if index < len(p.failures) && p.failures[index] != nil {
		return nil, p.failures[index]
	}
	if p.respond != nil {
		response, err := p.respond(index, request)
		response.Provider = p.Name()
		response.Model = "model"
		return &response, err
	}
	if index >= len(p.responses) {
		return nil, errors.New("Specialist test response exhausted")
	}
	response := p.responses[index]
	response.Provider = p.Name()
	response.Model = "model"
	return &response, nil
}

func (p *specialistTestProvider) StreamChat(ctx context.Context,
	request llm.ChatRequest,
) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func (*specialistTestProvider) SupportsTools(string) bool    { return false }
func (*specialistTestProvider) SupportsVision(string) bool   { return false }
func (*specialistTestProvider) SupportsJSONMode(string) bool { return true }

func specialistResponse(t testing.TB, action domain.SpecialistAction) string {
	t.Helper()
	encoded, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func assertSpecialistEventCounts(t testing.TB, st *store.SQLiteStore, runID string,
	want map[string]int,
) {
	t.Helper()
	timeline, err := st.ListRunEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, event := range timeline {
		counts[event.Type]++
	}
	for eventType, expected := range want {
		if counts[eventType] != expected {
			t.Fatalf("event %s count=%d want=%d timeline=%#v",
				eventType, counts[eventType], expected, timeline)
		}
	}
}
