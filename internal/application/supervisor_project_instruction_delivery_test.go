package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func TestSupervisorProjectInstructionDeliveryPreservesRequiredOutboundContentUnderPressure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "required-pressure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	root := t.TempDir()
	required := "REQUIRED_PINNED_BEGIN\n" + strings.Repeat("r", 40*1024) + "\nREQUIRED_PINNED_END"
	run, snapshot := newDeliveryTestRun(t, st, root, "src", map[string]string{
		"AGENTS.md": required, "CLAUDE.md": "EXCLUDED_SUPERSEDED_RULE",
		"src/AGENTS.md": "OPTIONAL_RULE_BEGIN\n" + strings.Repeat("o", 12*1024),
	}, map[string]projectconfig.InstructionRequirement{
		"AGENTS.md": projectconfig.InstructionMandatory, "CLAUDE.md": projectconfig.InstructionExcluded,
		"src/AGENTS.md": projectconfig.InstructionOptional,
	}, false)
	for index := range 24 {
		if _, err := application.NewNoteService(st).Create(t.Context(), application.CreateNoteRequest{
			RunID: run.ID, Title: fmt.Sprintf("High priority decision %02d", index),
			Content:  strings.Repeat("optional diagnostic detail ", 100),
			Category: "decision", Pinned: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		toolResponse("required-pressure-item", "work_item_create", `{"title":"Required rules retained"}`),
		textResponse(rootActionResponse(domain.RootActionContinue, "required rule delivered", "", "")),
	}}
	result, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID)
	if err != nil || result.ModelAttempts != 2 || result.ToolCalls != 1 {
		t.Fatalf("required pressure turn failed: result=%+v err=%v", result, err)
	}
	requests := provider.Requests()
	if len(requests) != 2 || !hasToolResults(requests[1]) {
		t.Fatalf("actual tool loop requests=%d", len(requests))
	}
	for _, request := range requests {
		assertRequiredDeliveryRequest(t, request, snapshot)
		for _, message := range request.Messages {
			if strings.Contains(message.Content, "EXCLUDED_SUPERSEDED_RULE") || strings.Contains(message.Content, "OPTIONAL_RULE_BEGIN") {
				t.Fatal("excluded or budget-omitted source was sent")
			}
		}
	}
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, event := range eventList {
		if event.Type != events.ModelStartedEvent || event.Source != "model_gateway" {
			continue
		}
		var payload struct {
			Context *llm.ModelContextAudit `json:"context"`
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil || payload.Context == nil {
			t.Fatalf("missing actual start context: %v", err)
		}
		starts++
		omittedNote, omittedOptional, excluded := false, false, false
		for _, source := range payload.Context.Omitted {
			omittedNote = omittedNote || source.Kind == "note"
			omittedOptional = omittedOptional || (source.Kind == "project_instruction" &&
				source.SourceID == "omitted/budget/"+snapshot.DeliverySourceID(2) && source.Tokens > 0)
			excluded = excluded || (source.Kind == "project_instruction" &&
				source.SourceID == "omitted/operator_excluded/"+snapshot.DeliverySourceID(1) && source.Tokens > 0)
		}
		if !omittedNote || !omittedOptional || !excluded {
			t.Fatalf("omission audit lacks actual reason or estimates: %+v", payload.Context)
		}
	}
	if starts != len(requests) {
		t.Fatalf("model starts=%d requests=%d", starts, len(requests))
	}
}

func TestSupervisorProjectInstructionDeliverySupportsNativeInstructionPaths(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "native-instructions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, snapshot := newDeliveryTestRun(t, st, t.TempDir(), ".", map[string]string{
		"AGENTS.md":          "SUPERSEDED_AGENT_RULE",
		"AGENTS.override.md": "REQUIRED_AGENT_OVERRIDE",
		".claude/CLAUDE.md":  "REQUIRED_CLAUDE_DIRECTORY_RULE",
	}, map[string]projectconfig.InstructionRequirement{
		"AGENTS.override.md": projectconfig.InstructionMandatory,
		".claude/CLAUDE.md":  projectconfig.InstructionMandatory,
	}, false)
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		textResponse(rootActionResponse(domain.RootActionContinue, "native project guidance delivered", "", "")),
	}}
	if _, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	requests := provider.Requests()
	if len(requests) != 1 || len(snapshot.Sources) != 2 {
		t.Fatalf("unexpected native instruction delivery: requests=%d sources=%+v", len(requests), snapshot.Sources)
	}
	assertRequiredDeliveryRequest(t, requests[0], snapshot)
	for _, message := range requests[0].Messages {
		if strings.Contains(message.Content, "SUPERSEDED_AGENT_RULE") {
			t.Fatal("same-directory AGENTS.md was delivered despite the override")
		}
	}
}

func TestSupervisorProjectInstructionDeliveryWithClaudeDirectoryTarget(t *testing.T) {
	for _, target := range []string{".claude/CLAUDE.md", ".claude/rules/nested/file.go"} {
		t.Run(target, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "claude-target.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			run, snapshot := newDeliveryTestRun(t, st, t.TempDir(), target, map[string]string{
				".claude/CLAUDE.md":            "REQUIRED_CLAUDE_CONTAINING_SCOPE",
				".claude/rules/nested/file.go": "package fixture\n",
			}, map[string]projectconfig.InstructionRequirement{
				".claude/CLAUDE.md": projectconfig.InstructionMandatory,
			}, false)
			if len(snapshot.Sources) != 1 || snapshot.Sources[0].Scope != "" || snapshot.Sources[0].Depth != 0 {
				t.Fatalf("hidden instruction lost its containing scope: %+v", snapshot.Sources)
			}
			inspection, err := application.NewProjectInstructionService(st).Inspect(t.Context(), run.ID, "")
			if err != nil || inspection.Stale || inspection.Live.Fingerprint != snapshot.Fingerprint {
				t.Fatalf("pinned hidden target could not be inspected: %+v err=%v", inspection, err)
			}
			provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
				textResponse(rootActionResponse(domain.RootActionContinue, "hidden target guidance delivered", "", "")),
			}}
			if _, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			requests := provider.Requests()
			if len(requests) != 1 {
				t.Fatalf("provider requests=%d want=1", len(requests))
			}
			assertRequiredDeliveryRequest(t, requests[0], snapshot)
		})
	}
}

func TestSupervisorProjectInstructionDeliveryOmitsLargeEscapedExcludedSource(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "excluded-escaped-bound.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, snapshot := newDeliveryTestRun(t, st, t.TempDir(), "src", map[string]string{
		"AGENTS.md":     "EXCLUDED_LARGE_ESCAPED_RULE\n" + strings.Repeat("\"", 65500),
		"src/AGENTS.md": "SMALL_CURRENT_MANDATORY_RULE",
	}, map[string]projectconfig.InstructionRequirement{
		"AGENTS.md": projectconfig.InstructionExcluded, "src/AGENTS.md": projectconfig.InstructionMandatory,
	}, false)
	if err := snapshot.Validate(); err != nil || len(snapshot.Sources[0].Content) > snapshot.Limits.MaxFileBytes {
		t.Fatalf("escaped excluded source must remain inside the snapshot input bounds: %v", err)
	}
	// Even this subset of the production envelope exceeds the delivery-size
	// bound. The full excluded envelope must be omitted without being sent.
	minimum, err := json.Marshal(struct {
		Content string `json:"content"`
		SHA256  string `json:"content_sha256"`
	}{snapshot.Sources[0].Content, snapshot.Sources[0].ContentSHA256})
	if err != nil || len(minimum) <= contextmgr.MaxContextSectionBytes {
		t.Fatalf("fixture did not reach the escaped delivery boundary: bytes=%d err=%v", len(minimum), err)
	}
	t.Logf("excluded_source_bytes=%d snapshot_file_limit=%d envelope_subset_bytes=%d delivery_limit=%d",
		len(snapshot.Sources[0].Content), snapshot.Limits.MaxFileBytes, len(minimum), contextmgr.MaxContextSectionBytes)
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		textResponse(rootActionResponse(domain.RootActionContinue, "required source delivered and excluded source omitted", "", "")),
	}}
	result, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID)
	requests := provider.Requests()
	if err != nil || result.ModelAttempts != 1 || len(requests) != 1 {
		t.Fatalf("valid excluded source blocked required delivery: err=%v outbound=%d", err, len(requests))
	}
	assertRequiredDeliveryRequest(t, requests[0], snapshot)
	for _, message := range requests[0].Messages {
		if strings.Contains(message.Content, "EXCLUDED_LARGE_ESCAPED_RULE") {
			t.Fatal("excluded content reached the Provider")
		}
	}
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, event := range eventList {
		if event.Type != events.ModelStartedEvent || event.Source != "model_gateway" {
			continue
		}
		starts++
		var payload struct {
			Context *llm.ModelContextAudit `json:"context"`
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil || payload.Context == nil {
			t.Fatalf("actual start lost the source audit: %v", err)
		}
		required, excluded := false, false
		for _, source := range payload.Context.Included {
			required = required || source.SourceID == snapshot.DeliverySourceID(1) && source.Tokens > 0
			if source.SourceID == snapshot.DeliverySourceID(0) {
				t.Fatal("excluded source was audited as included")
			}
		}
		for _, source := range payload.Context.Omitted {
			excluded = excluded || source.SourceID == "omitted/operator_excluded/"+snapshot.DeliverySourceID(0) && source.Tokens > 0
		}
		if !required || !excluded {
			t.Fatalf("actual request and omission audit disagree: %+v", payload.Context)
		}
	}
	if starts != 1 || countEventType(eventList, events.SupervisorToolExecutionStartedEvent) != 0 {
		t.Fatalf("excluded-only content changed dispatch: model starts=%d", starts)
	}
}

func TestSupervisorProjectInstructionDeliveryStopsBeforeModelOrToolsWhenRequiredCannotFit(t *testing.T) {
	for _, test := range []struct {
		name    string
		size    int
		window  int
		history bool
		escaped bool
	}{
		{"selection_budget", 63 * 1024, 0, false, false},
		{"selection_before_generated_compaction", 63 * 1024, 0, true, false},
		{"complete_request_budget", 12 * 1024, 4096, false, false},
		{"serialized_required_bound", 65500, 0, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "required-refusal.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			padding := strings.Repeat("r", test.size)
			if test.escaped {
				padding = strings.Repeat("\"", test.size)
			}
			run, _ := newDeliveryTestRun(t, st, t.TempDir(), ".", map[string]string{
				"AGENTS.md": "REQUIRED_TOO_LARGE\n" + padding,
			}, map[string]projectconfig.InstructionRequirement{"AGENTS.md": projectconfig.InstructionMandatory}, test.history)
			if test.history {
				if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
					t.Fatal(err)
				}
				for range 24 {
					if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", "bounded old task evidence")); err != nil {
						t.Fatal(err)
					}
				}
			}
			provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
				toolResponse("must-not-dispatch", "work_item_create", `{"title":"Must not exist"}`),
			}}
			ref := llm.ModelRef{Provider: provider.Name(), Model: "model"}
			router := llm.NewRouter(ref)
			router.RegisterProvider(provider)
			if test.window > 0 {
				if err := router.SetContextWindow(ref, llm.ContextWindow{
					ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: test.window,
					SafetyMarginTokens: 128, DefaultOutputTokens: 512, MaxOutputTokens: 512, Source: "required_delivery_test",
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, err = application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithGeneratedContextCompaction(test.history).Step(t.Context(), run.ID)
			if apperror.CodeOf(err) != apperror.CodeResourceExhausted || len(provider.Requests()) != 0 {
				t.Fatalf("required delivery silently degraded: error=%v outbound=%d", err, len(provider.Requests()))
			}
			eventList, err := st.ListRunEvents(t.Context(), run.ID)
			if err != nil || countEventType(eventList, events.ModelStartedEvent) != 0 ||
				countEventType(eventList, events.SupervisorToolExecutionStartedEvent) != 0 {
				t.Fatalf("refused request dispatched work: %v", err)
			}
			items, err := st.ListWorkItems(t.Context(), domain.WorkItemFilter{RunID: run.ID})
			if err != nil || len(items) != 0 {
				t.Fatalf("refused request wrote a tool result: %v", err)
			}
			if _, found, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || found {
				t.Fatalf("required refusal reached compaction before checking delivery: found=%t err=%v", found, err)
			}
		})
	}
}

func TestSupervisorProjectInstructionDeliveryKeepsConfirmedNestedScopeAndSupersededSourceExcluded(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "required-nested-scope.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	root := t.TempDir()
	run, snapshot := newDeliveryTestRun(t, st, root, "module/file.go", map[string]string{
		"AGENTS.md":        "SUPERSEDED_ROOT_RULE_MUST_NOT_REVIVE",
		"module/AGENTS.md": "CONFIRMED_MODULE_RULE",
	}, map[string]projectconfig.InstructionRequirement{
		"AGENTS.md": projectconfig.InstructionExcluded, "module/AGENTS.md": projectconfig.InstructionMandatory,
	}, false)
	if len(snapshot.Conflicts) != 1 || snapshot.Sources[1].Scope != "module" ||
		snapshot.Sources[1].Precedence <= snapshot.Sources[0].Precedence {
		t.Fatal("fixture lacks a scoped directory override")
	}
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		textResponse(rootActionResponse(domain.RootActionContinue, "confirmed nested source used", "", "")),
		textResponse(rootActionResponse(domain.RootActionContinue, "same pinned nested source retained", "", "")),
	}}
	if _, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "module", "AGENTS.md"), []byte("UNCONFIRMED_NEW_MODULE_RULE"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if len(provider.Requests()) != 2 {
		t.Fatal("nested scope did not execute two ordinary turns")
	}
	for _, request := range provider.Requests() {
		assertRequiredDeliveryRequest(t, request, snapshot)
		for _, message := range request.Messages {
			if strings.Contains(message.Content, "SUPERSEDED_ROOT_RULE_MUST_NOT_REVIVE") || strings.Contains(message.Content, "UNCONFIRMED_NEW_MODULE_RULE") {
				t.Fatal("superseded or unconfirmed rules entered actual scoped context")
			}
		}
	}
}

func TestSupervisorProjectInstructionDeliveryPinsDiskDriftAndRecoversPendingToolAcrossReopen(t *testing.T) {
	dbPath, root := filepath.Join(t.TempDir(), "required-reopen.db"), t.TempDir()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	run, snapshot := newDeliveryTestRun(t, st, root, ".", map[string]string{
		"AGENTS.md": "PINNED_REQUIRED_BEFORE_RESTART",
	}, map[string]projectconfig.InstructionRequirement{"AGENTS.md": projectconfig.InstructionMandatory}, false)
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		toolResponse("required-before-restart", "work_item_create", `{"title":"Durable required item"}`),
		textResponse(rootActionResponse(domain.RootActionContinue, "required restart recovered", "", "")),
	}}
	first, err := newToolLoopSupervisor(&failOnceToolResultStore{SQLiteStore: st, fail: true}, provider).Step(t.Context(), run.ID)
	if apperror.CodeOf(err) != apperror.CodeInternal || first.Checkpoint.Phase != domain.SupervisorTurnStarted {
		t.Fatalf("fixture has no recoverable pending tool: result=%+v err=%v", first, err)
	}
	assertRequiredDeliveryRequest(t, provider.Requests()[0], snapshot)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("UNCONFIRMED_DISK_REPLACEMENT"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID)
	if err != nil || !resumed.Recovered || resumed.ToolCalls != 1 || resumed.ModelAttempts != 2 {
		t.Fatalf("pinned tool could not resume: result=%+v err=%v", resumed, err)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("reopen changed model call count: %d", len(requests))
	}
	assertRequiredDeliveryRequest(t, requests[1], snapshot)
	for _, message := range requests[1].Messages {
		if strings.Contains(message.Content, "UNCONFIRMED_DISK_REPLACEMENT") {
			t.Fatal("restart refreshed instructions without confirmation")
		}
	}
	items, err := st.ListWorkItems(t.Context(), domain.WorkItemFilter{RunID: run.ID})
	if err != nil || len(items) != 1 {
		t.Fatalf("reopen duplicated the tool effect: count=%d err=%v", len(items), err)
	}
}

func TestSupervisorProjectInstructionDeliveryRejectsPendingToolFromRefreshedContract(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "required-stale-origin.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	root := t.TempDir()
	run, snapshot := newDeliveryTestRun(t, st, root, ".", map[string]string{
		"AGENTS.md": "ORIGINAL_REQUIRED_TOOL_ORIGIN",
	}, map[string]projectconfig.InstructionRequirement{"AGENTS.md": projectconfig.InstructionMandatory}, false)
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		toolResponse("stale-required-origin", "work_item_create", `{"title":"Original item"}`),
	}}
	_, err = newToolLoopSupervisor(&failOnceToolResultStore{SQLiteStore: st, fail: true}, provider).Step(t.Context(), run.ID)
	if apperror.CodeOf(err) != apperror.CodeInternal || len(provider.Requests()) != 1 {
		t.Fatalf("fixture did not retain a pending source: %v", err)
	}
	assertRequiredDeliveryRequest(t, provider.Requests()[0], snapshot)
	service := application.NewRunService(st)
	if _, err := service.Pause(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("REFRESHED_REQUIRED_TOOL_ORIGIN"), 0600); err != nil {
		t.Fatal(err)
	}
	instructions := application.NewProjectInstructionService(st)
	state, err := instructions.Inspect(t.Context(), run.ID, ".")
	if err != nil {
		t.Fatal(err)
	}
	_, err = instructions.RefreshWithDelivery(t.Context(), run.ID, ".", state.Pinned.Snapshot.Fingerprint,
		state.Live.Fingerprint, "cli_operator", true, []projectconfig.InstructionSourceDelivery{
			{Path: state.Live.Sources[0].Path, ContentSHA256: state.Live.Sources[0].ContentSHA256, Requirement: projectconfig.InstructionMandatory},
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID)
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || len(provider.Requests()) != 1 {
		t.Fatalf("refreshed rules reused stale tool origin: err=%v outbound=%d", err, len(provider.Requests()))
	}
	after, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil || countEventType(after, events.SupervisorToolExecutionStartedEvent) != countEventType(before, events.SupervisorToolExecutionStartedEvent) {
		t.Fatalf("stale tool was dispatched again: %v", err)
	}
}

func TestSupervisorProjectInstructionDeliverySurvivesCompactionDuringNativeToolRound(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "required-compaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, snapshot := newDeliveryTestRun(t, st, t.TempDir(), ".", map[string]string{
		"AGENTS.md": "REQUIRED_THROUGH_COMPACTION_BEGIN\n" + strings.Repeat("rule ", 300) + "\nREQUIRED_THROUGH_COMPACTION_END",
	}, map[string]projectconfig.InstructionRequirement{"AGENTS.md": projectconfig.InstructionMandatory}, true)
	provider := &contextPressureToolProvider{scriptedToolProvider: &scriptedToolProvider{responses: []*llm.ChatResponse{
		textResponse(rootActionResponse(domain.RootActionFinish, "original preserved", "reply complete", "")),
		textResponse(rootActionResponse(domain.RootActionFinish, "correction preserved", "reply complete", "")),
		toolResponse("required-compaction-item", "work_item_create", `{"title":"Required during compaction"}`),
		textResponse(rootActionResponse(domain.RootActionFinish, "required compacted tool result", "reply complete", "")),
	}}}
	ref := llm.ModelRef{Provider: provider.Name(), Model: "model"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(provider)
	var summaryID int64
	provider.afterResponse = func(ctx context.Context, index int, request llm.ChatRequest) error {
		if index != 3 && index != 4 {
			return nil
		}
		summary, found, err := st.LatestContextSummary(ctx, run.SessionID)
		if err != nil {
			return err
		}
		if index == 3 {
			estimate, err := strconv.Atoi(request.Metadata["context_input_estimate"])
			if err != nil || found || estimate < 8192 {
				return fmt.Errorf("fixture did not reach uncompacted native pressure: estimate=%d summary=%t err=%v", estimate, found, err)
			}
			return router.SetContextWindow(ref, llm.ContextWindow{
				ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: estimate - 1024 + 512 + 128,
				SafetyMarginTokens: 128, DefaultOutputTokens: 512, MaxOutputTokens: 512, Source: "required_compaction_test",
			})
		}
		if !found || !hasToolResults(request) || request.Metadata["context_summary_id"] != strconv.FormatInt(summary.ID, 10) {
			return fmt.Errorf("rebuilt native request lacks actual summary and paired tool result")
		}
		summaryID = summary.ID
		return nil
	}
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
		application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()).WithGeneratedContextCompaction(false))
	submit := func(key, content string) {
		t.Helper()
		result, err := turns.Execute(t.Context(), application.ExecuteThreadTurnRequest{
			Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID),
			Content: content, OperationKey: key, RequestedBy: "test_operator",
		})
		if err != nil || result.Execution == nil || result.Execution.Handoff.Result == nil ||
			result.Execution.Handoff.Result.Status != domain.RunExecutionHandoffCompleted {
			t.Fatalf("required compaction turn failed: key=%s err=%v", key, err)
		}
	}
	padding := strings.Repeat("Historical diagnostic detail remains evidence, not a new instruction. ", 180)
	submit("required-original", "ORIGINAL_REQUIRED_TASK "+padding)
	submit("required-correction", "CORRECTION_REQUIRED_TASK "+padding)
	before, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	rawBefore := continuityRawMessages(t, before)
	submit("required-compaction-tool", "CURRENT_REQUIRED_TOOL_INPUT: create one work item then finish this reply")
	requests := provider.Requests()
	if len(requests) != 4 || summaryID == 0 {
		t.Fatalf("required test did not compact between actual tool requests: outbound=%d summary=%d", len(requests), summaryID)
	}
	for _, request := range requests {
		assertRequiredDeliveryRequest(t, request, snapshot)
	}
	after, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	rawAfter := continuityRawMessages(t, after)
	for id, content := range rawBefore {
		if rawAfter[id] != content {
			t.Fatalf("compaction rewrote raw historical source %d", id)
		}
	}
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil || countEventType(eventList, events.SupervisorToolExecutionStartedEvent) != 1 {
		t.Fatalf("compaction replayed tool dispatch: %v", err)
	}
}

func TestSupervisorProjectInstructionDeliveryRejectsRefreshWhileModelOwnsExecutionLease(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "required-inflight-refresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, snapshot := newDeliveryTestRun(t, st, t.TempDir(), ".", map[string]string{
		"AGENTS.md": "REQUIRED_AT_ORIGINAL_DISPATCH",
	}, map[string]projectconfig.InstructionRequirement{"AGENTS.md": projectconfig.InstructionMandatory}, false)
	provider := &scriptedToolProvider{respond: func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		service := application.NewRunService(st)
		if _, err := service.Pause(t.Context(), run.ID); err != nil {
			return nil, err
		}
		state, err := application.NewProjectInstructionService(st).Inspect(t.Context(), run.ID, ".")
		if err != nil {
			return nil, err
		}
		_, err = application.NewProjectInstructionService(st).RefreshWithDelivery(t.Context(), run.ID, ".",
			state.Pinned.Snapshot.Fingerprint, state.Live.Fingerprint, "cli_operator", true,
			[]projectconfig.InstructionSourceDelivery{{Path: snapshot.Sources[0].Path,
				ContentSHA256: snapshot.Sources[0].ContentSHA256, Requirement: projectconfig.InstructionExcluded,
				ExclusionReason: "withdrawn"}})
		if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			return nil, fmt.Errorf("refresh did not reject an active model lease: %v", err)
		}
		current, err := st.GetRun(t.Context(), run.ID)
		if err != nil || current.Config.ProjectInstructionsFingerprint != snapshot.Fingerprint {
			return nil, fmt.Errorf("in-flight refresh changed pinned rules: %v", err)
		}
		if _, err := service.Resume(t.Context(), run.ID); err != nil {
			return nil, err
		}
		return textResponse(rootActionResponse(domain.RootActionContinue, "original classified rule retained", "", "")), nil
	}}
	_, err = newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID)
	if err != nil || len(provider.Requests()) != 1 {
		t.Fatalf("in-flight refresh did not preserve the confirmed model contract: err=%v outbound=%d", err, len(provider.Requests()))
	}
	assertRequiredDeliveryRequest(t, provider.Requests()[0], snapshot)
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil || countEventType(eventList, events.SupervisorToolExecutionStartedEvent) != 0 {
		t.Fatalf("in-flight refresh started a stale tool: %v", err)
	}
	items, err := st.ListWorkItems(t.Context(), domain.WorkItemFilter{RunID: run.ID})
	if err != nil || len(items) != 0 {
		t.Fatalf("in-flight stale tool committed an effect: %v", err)
	}
}

func TestSupervisorProjectInstructionDeliveryIncludesPinnedRulesInGeneratedSummaryDispatch(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "required-generated-summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	root := t.TempDir()
	run, snapshot := newDeliveryTestRun(t, st, root, "src", map[string]string{
		"AGENTS.md": "REQUIRED_GENERATED_PINNED_BEGIN\n" + strings.Repeat("retain source scope and acceptance constraints; ", 80) + "\nREQUIRED_GENERATED_PINNED_END",
		"CLAUDE.md": "EXCLUDED_GENERATED_RULE", "src/AGENTS.md": "OPTIONAL_GENERATED_RULE",
	}, map[string]projectconfig.InstructionRequirement{
		"AGENTS.md": projectconfig.InstructionMandatory, "CLAUDE.md": projectconfig.InstructionExcluded,
		"src/AGENTS.md": projectconfig.InstructionOptional,
	}, true)
	for index := range 24 {
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user",
			fmt.Sprintf("Historical checkpoint %02d: keep original acceptance constraints and report observed evidence.", index))); err != nil {
			t.Fatal(err)
		}
	}
	before, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	rawBefore := continuityRawMessages(t, before)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("UNCONFIRMED_GENERATED_REPLACEMENT"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := newGeneratedProtocolProvider()
	trackGeneratedFixture(t, provider)
	turns, _ := generatedThreadServices(st, provider, nil)
	submitHistoryRecall(t, turns, run.ID, "required-generated-summary", "Continue with the confirmed project rules and original historical constraints.")
	generated, normal := provider.splitRequests()
	if len(generated) != 1 || len(normal) != 1 || generated[0].Transport != "chat" || len(generated[0].Request.Tools) != 0 {
		t.Fatalf("fixture did not dispatch one actual auxiliary request and continuation: auxiliary=%d normal=%d", len(generated), len(normal))
	}
	for _, captured := range append(generated, normal...) {
		assertRequiredDeliveryRequest(t, captured.Request, snapshot)
		if strings.Contains(generatedRequestText(captured.Request), "UNCONFIRMED_GENERATED_REPLACEMENT") {
			t.Fatal("summary or continuation silently refreshed the pinned rule")
		}
	}
	for _, marker := range []string{"OPTIONAL_GENERATED_RULE", "EXCLUDED_GENERATED_RULE"} {
		if strings.Contains(generatedRequestText(generated[0].Request), marker) {
			t.Fatalf("auxiliary request included an inapplicable source: %s", marker)
		}
	}
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, event := range eventList {
		if event.Type != events.ModelStartedEvent || event.Source != "model_gateway" {
			continue
		}
		var payload struct {
			Purpose string                 `json:"purpose"`
			Context *llm.ModelContextAudit `json:"context"`
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil || payload.Context == nil {
			t.Fatalf("model start lost its required-source audit: %v", err)
		}
		starts++
		included := false
		for _, source := range payload.Context.Included {
			included = included || source.SourceID == snapshot.DeliverySourceID(0)
		}
		if !included {
			t.Fatal("actual model start omitted the mandatory source identity")
		}
		if payload.Purpose == generatedFixturePurpose {
			optional, excluded := false, false
			for _, source := range payload.Context.Omitted {
				optional = optional || source.SourceID == "omitted/auxiliary_scope/"+snapshot.DeliverySourceID(2) && source.Tokens > 0
				excluded = excluded || source.SourceID == "omitted/operator_excluded/"+snapshot.DeliverySourceID(1) && source.Tokens > 0
			}
			if !optional || !excluded {
				t.Fatalf("auxiliary omissions claimed delivery or an incorrect reason: %+v", payload.Context)
			}
		}
	}
	if starts != 2 || countEventType(eventList, events.SupervisorToolExecutionStartedEvent) != 0 {
		t.Fatalf("summary accounting or tool boundary changed: starts=%d", starts)
	}
	summary, found, err := st.LatestContextSummary(t.Context(), run.SessionID)
	if err != nil || !found {
		t.Fatalf("actual generated handoff was not persisted: %v", err)
	}
	assertGeneratedSummaryReceipt(t, summary)
	after, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	rawAfter := continuityRawMessages(t, after)
	for id, content := range rawBefore {
		if rawAfter[id] != content {
			t.Fatalf("generated compaction rewrote source message %d", id)
		}
	}
}

func TestSupervisorProjectInstructionDeliveryRefusesOversizedSummaryBeforeDispatch(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "required-summary-window.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, snapshot := newDeliveryTestRun(t, st, t.TempDir(), ".", map[string]string{
		"AGENTS.md": "REQUIRED_SUMMARY_WINDOW_BEGIN\n" + strings.Repeat("r", 32*1024) + "\nREQUIRED_SUMMARY_WINDOW_END",
	}, map[string]projectconfig.InstructionRequirement{"AGENTS.md": projectconfig.InstructionMandatory}, true)
	for index := range 24 {
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user",
			fmt.Sprintf("Observed historical checkpoint %02d: %s", index, strings.Repeat("diagnostic detail remains evidence; ", 130)))); err != nil {
			t.Fatal(err)
		}
	}
	provider := newGeneratedProtocolProvider()
	trackGeneratedFixture(t, provider)
	turns, _ := generatedThreadServices(st, provider, nil)
	submitHistoryRecall(t, turns, run.ID, "required-summary-window", "Continue under the same required project rules.")
	generated, normal := provider.splitRequests()
	if len(generated) != 0 || len(normal) != 1 {
		t.Fatalf("oversized auxiliary input reached the Provider or lost continuation: auxiliary=%d normal=%d", len(generated), len(normal))
	}
	assertRequiredDeliveryRequest(t, normal[0].Request, snapshot)
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	fallback := false
	for _, event := range eventList {
		if event.Type == "session.context_compacted" {
			var payload struct {
				Generated bool   `json:"generated"`
				Reason    string `json:"generation_fallback_reason"`
			}
			if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			fallback = fallback || !payload.Generated && strings.Contains(payload.Reason, "generation_input_window")
		}
		if event.Type == events.ModelStartedEvent && strings.Contains(event.PayloadJSON, `"purpose":"context_compaction"`) {
			t.Fatal("refused auxiliary request created a model-start receipt")
		}
	}
	if !fallback || countEventType(eventList, events.ModelStartedEvent) != 1 || countEventType(eventList, events.SupervisorToolExecutionStartedEvent) != 0 {
		t.Fatal("window refusal did not retain a truthful extractive fallback and zero auxiliary/tool starts")
	}
	history, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 24 {
		found := false
		prefix := fmt.Sprintf("Observed historical checkpoint %02d:", index)
		for _, message := range history {
			found = found || message.Role == "user" && strings.HasPrefix(message.Content, prefix)
		}
		if !found {
			t.Fatalf("window fallback erased historical source %02d", index)
		}
	}
}

func TestSupervisorProjectInstructionDeliveryPreservesCompletePinnedRulesAcrossRetry(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "required-retry-refresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, snapshot := newDeliveryTestRun(t, st, t.TempDir(), ".", map[string]string{
		"AGENTS.md": "REQUIRED_AT_RETRY_ORIGIN",
	}, map[string]projectconfig.InstructionRequirement{"AGENTS.md": projectconfig.InstructionMandatory}, false)
	provider := &scriptedToolProvider{respond: func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index == 0 {
			return nil, llm.NewProviderError(llm.OutcomeRetryable, "tool-loop", "test retryable failure", nil)
		}
		return textResponse(rootActionResponse(domain.RootActionContinue, "required retry recovered", "", "")), nil
	}}
	supervisor := newToolLoopSupervisor(st, provider).WithModelRetryPolicy(application.ModelRetryPolicy{
		MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
	})
	result, err := supervisor.Step(t.Context(), run.ID)
	if err != nil || result.ModelAttempts != 2 || len(provider.Requests()) != 2 {
		t.Fatalf("required retry did not retain the pinned contract: err=%v outbound=%d", err, len(provider.Requests()))
	}
	for _, request := range provider.Requests() {
		assertRequiredDeliveryRequest(t, request, snapshot)
	}
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil || countEventType(eventList, events.ModelStartedEvent) != 2 ||
		countEventType(eventList, events.SupervisorToolExecutionStartedEvent) != 0 {
		t.Fatalf("required retry lost actual start accounting: %v", err)
	}
}

func newDeliveryTestRun(t *testing.T, st *store.SQLiteStore, root, target string,
	files map[string]string, requirements map[string]projectconfig.InstructionRequirement, interactive bool,
) (domain.Run, projectconfig.InstructionSnapshot) {
	t.Helper()
	for path, content := range files {
		filename := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "delivery-test-workspace", Name: "delivery test",
		RootPath: root, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := projectconfig.DiscoverInstructions(t.Context(), root, target)
	if err != nil {
		t.Fatal(err)
	}
	classes := make([]projectconfig.InstructionSourceDelivery, len(snapshot.Sources))
	for index, source := range snapshot.Sources {
		var requirement projectconfig.InstructionRequirement
		for path, value := range requirements {
			if strings.EqualFold(path, source.Path) {
				requirement = value
			}
		}
		classes[index] = projectconfig.InstructionSourceDelivery{Path: source.Path,
			ContentSHA256: source.ContentSHA256, Requirement: requirement}
		if classes[index].Requirement == projectconfig.InstructionExcluded {
			classes[index].ExclusionReason = "superseded"
		}
	}
	snapshot, err = projectconfig.ClassifyInstructionSnapshot(snapshot, classes)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "retain explicitly required pinned project rules", Profile: "review", WorkspaceID: "delivery-test-workspace",
		ModelRoute: "tool-loop/model", Interactive: interactive, Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 6},
		ProjectInstructions: &snapshot, RequestedBy: "cli_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !interactive {
		if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
			t.Fatal(err)
		}
	}
	return run, snapshot
}

// Decode real Provider requests and compare the complete pinned content and
// source metadata. Audit labels alone do not establish delivery.
func assertRequiredDeliveryRequest(t *testing.T, request llm.ChatRequest, snapshot projectconfig.InstructionSnapshot) {
	t.Helper()
	for index, item := range snapshot.Delivery.Sources {
		if item.Requirement != projectconfig.InstructionMandatory {
			continue
		}
		expected := snapshot.Sources[index]
		found := 0
		for _, message := range request.Messages {
			var envelope struct {
				Version string `json:"version"`
				Source  struct {
					Path          string `json:"path"`
					Scope         string `json:"scope"`
					Kind          string `json:"kind"`
					ContentSHA256 string `json:"content_sha256"`
					Snapshot      string `json:"snapshot_fingerprint"`
					Precedence    int    `json:"precedence"`
					Trust         string `json:"trust"`
				} `json:"source"`
				Authority projectconfig.InstructionAuthority      `json:"authority"`
				Content   string                                  `json:"content"`
				Delivery  projectconfig.InstructionSourceDelivery `json:"delivery"`
			}
			if json.Unmarshal([]byte(message.Content), &envelope) != nil || envelope.Version != "project_instruction_guidance.v1" || envelope.Source.Path != item.Path {
				continue
			}
			found++
			if message.Role != "user" || envelope.Content != expected.Content || envelope.Source.ContentSHA256 != expected.ContentSHA256 ||
				envelope.Source.Snapshot != snapshot.Fingerprint || envelope.Source.Scope != expected.Scope || envelope.Source.Kind != expected.Kind ||
				envelope.Source.Precedence != expected.Precedence || envelope.Source.Trust != expected.Trust || envelope.Authority != expected.Authority ||
				envelope.Delivery != item {
				t.Fatalf("actual outbound request changed required content, binding, scope, precedence or authority for %s", item.Path)
			}
		}
		if found != 1 {
			t.Fatalf("actual outbound request contains %d complete required envelopes for %s", found, item.Path)
		}
	}
}
