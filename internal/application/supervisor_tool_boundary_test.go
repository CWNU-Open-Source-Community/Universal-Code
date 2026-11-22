package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runactivity"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/threadtranscript"
	"cyberagent-workbench/internal/tools"
)

type boundaryJourneyProvider struct {
	scriptedToolProvider
	respond func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error)
}

func (p *boundaryJourneyProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	index := len(p.requests)
	p.mu.Unlock()
	return p.respond(ctx, request, index)
}
func (p *boundaryJourneyProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	if response.Text != "" {
		chunks <- llm.ChatChunk{Text: response.Text}
	}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func toolBoundaryFixture(t *testing.T, budget domain.Budget) (*store.SQLiteStore, domain.Run, string, application.ExecuteThreadTurnRequest) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tool-boundary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("original text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "ws-tool-boundary", Name: "tool-boundary", RootPath: root, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Inspect and edit a file through a tool boundary", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: "ws-tool-boundary", ModelRoute: "tool-loop/model", Interactive: true, Budget: budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, run, root, application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion,
		ThreadID: domain.InitialThreadID(run.ID), Content: "Change only README.md to reviewed text; preserve this original constraint",
		OperationKey: "same-user-tool-boundary", RequestedBy: "test_operator"}
}

func toolBoundaryService(st application.RunExecutionHandoffStore, threadStore application.ThreadStore, provider llm.Provider) *application.ThreadTurnService {
	return toolBoundaryServiceWithPolicy(st, threadStore, provider, policy.NewDefaultChecker())
}

func toolBoundaryServiceWithPolicy(st application.RunExecutionHandoffStore, threadStore application.ThreadStore, provider llm.Provider, checker policy.Checker) *application.ThreadTurnService {
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	return application.NewThreadTurnService(threadStore, application.NewRunLifecycleControlService(st.(application.RunLifecycleControlStore)),
		application.NewRunExecutionHandoffService(st, router, checker).WithGeneratedContextCompaction(false)) // fixed extractive/tool-boundary script
}

func boundaryRead(id string, line int) *llm.ChatResponse {
	return toolResponse(id, "workspace_read", fmt.Sprintf(`{"version":"agent-code-tools.v1","path":"README.md","start_line":%d,"end_line":20}`, line))
}
func boundaryPropose(id string) *llm.ChatResponse {
	return toolResponse(id, "workspace_change", fmt.Sprintf(`{"version":"agent-code-tools.v1","action":"propose_patch","path":"README.md","expected_sha256":%q,"replacements":[{"old_text":"original text","new_text":"reviewed text","expected_occurrences":1}]}`, fileedit.HashText("original text\n")))
}
func approveBoundaryEdit(t *testing.T, st *store.SQLiteStore, run domain.Run) fileedit.Edit {
	t.Helper()
	edits, err := st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
	if err != nil || len(edits) != 1 {
		t.Fatalf("edits=%#v err=%v", edits, err)
	}
	if _, err := application.NewFileEditReviewService(st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: run.ID, EditID: edits[0].ID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	return edits[0]
}
func boundaryApply(id string, edit fileedit.Edit) *llm.ChatResponse {
	return toolResponse(id, "workspace_apply", fmt.Sprintf(`{"version":"agent-code-tools.v1","edit_id":%q,"expected_action":"propose_patch","expected_original_sha256":%q,"expected_proposed_sha256":%q}`, edit.ID, edit.OriginalHash, edit.ProposedHash))
}
func assertBoundaryPrompt(t *testing.T, request llm.ChatRequest) {
	t.Helper()
	if len(request.Tools) != 0 || request.Metadata["protocol_repair"] != "" || len(request.Messages) == 0 ||
		!strings.Contains(request.Messages[len(request.Messages)-1].Content, "scheduling boundary") {
		t.Fatalf("tool cap was not an ordinary no-tool handoff: %#v", request)
	}
}
func boundaryContextText(t *testing.T, request llm.ChatRequest) string {
	t.Helper()
	for _, message := range request.Messages {
		if !strings.Contains(message.Content, "Completed tool segments of this exact accepted user input") {
			continue
		}
		var value struct {
			Content    string `json:"content"`
			Authorized bool   `json:"instruction_authorized"`
		}
		start := strings.Index(message.Content, "{")
		if start < 0 || json.Unmarshal([]byte(message.Content[start:]), &value) != nil || value.Authorized || message.Role != "user" || len([]rune(value.Content)) > 128*1024 {
			t.Fatal("tool-boundary evidence is unbounded or instruction-authorized")
		}
		return value.Content
	}
	t.Fatal("next segment did not receive exact preceding tool facts")
	return ""
}

func assertOneBoundaryTranscriptInput(t *testing.T, st *store.SQLiteStore, threadID, input string) {
	t.Helper()
	var ordinal, sequence int64
	count := 0
	for {
		sources, err := st.ListThreadTranscriptSourceBefore(t.Context(), threadID, ordinal, sequence, threadtranscript.MaxSourceRecords)
		if err != nil {
			t.Fatal(err)
		}
		if len(sources) == 0 {
			break
		}
		items, err := threadtranscript.Build(threadID, sources)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.Kind == runactivity.KindOperatorInput && item.Detail == input {
				count++
			}
		}
		last := sources[len(sources)-1]
		ordinal, sequence = last.Ordinal, last.Sequence
		if len(sources) < threadtranscript.MaxSourceRecords {
			break
		}
	}
	if count != 1 {
		t.Fatalf("internal segmentation produced %d user bubbles", count)
	}
}

func TestThreadToolBoundaryReturnsCompactionFromEarlierSegment(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
	// Seed committed source history, not a fabricated summary. The first real
	// Supervisor segment must compact it; the next segment only reads that summary.
	for index := 0; index < 22; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, role,
			fmt.Sprintf("Earlier review checkpoint %d: preserve README and report actual observations. %s", index, compactionFixtureObservation()))); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, err := st.LatestContextSummary(t.Context(), run.SessionID); err != nil || found {
		t.Fatalf("fixture already had a summary: found=%t err=%v", found, err)
	}
	var firstSummaryID int64
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		summary, found, err := st.LatestContextSummary(ctx, run.SessionID)
		if err != nil || !found || summary.ID <= 0 || summary.CompactedMessageCount <= 0 {
			return nil, fmt.Errorf("first segment did not compact real history: found=%t count=%d err=%v", found, summary.CompactedMessageCount, err)
		}
		if index == 1 {
			firstSummaryID = summary.ID
		}
		if summary.ID != firstSummaryID || request.Metadata["context_summary_id"] != strconv.FormatInt(firstSummaryID, 10) ||
			request.Metadata["context_history_omitted"] != "0" {
			return nil, fmt.Errorf("later request lost or replaced the first-segment summary: metadata=%v", request.Metadata)
		}
		switch {
		case index <= domain.MaxSupervisorToolRounds:
			return boundaryRead(fmt.Sprintf("compaction-read-%d", index), 1), nil
		case index == domain.MaxSupervisorToolRounds+1:
			assertBoundaryPrompt(t, request)
			return textResponse(rootActionResponse(domain.RootActionContinue, "One final observation remains", "", "")), nil
		case index == domain.MaxSupervisorToolRounds+2:
			assertInternalInputDelivery(t, request, input.Content)
			boundaryContextText(t, request)
			return boundaryRead("compaction-final-read", 1), nil
		case index == domain.MaxSupervisorToolRounds+3:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Observed the original file", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}
	result, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input)
	if err != nil || result.Execution == nil || len(result.Execution.Execution.Steps) != 1 {
		t.Fatalf("compacting boundary journey failed: err=%v result=%#v", err, result)
	}
	step := result.Execution.Execution.Steps[0]
	if !step.ContextCompacted || step.ContextSummaryID != firstSummaryID || firstSummaryID == 0 {
		t.Fatalf("final segment hid earlier actual compaction: compacted=%t summary=%d want=%d", step.ContextCompacted, step.ContextSummaryID, firstSummaryID)
	}
	if len(provider.Requests()) != domain.MaxSupervisorToolRounds+3 || step.ToolRounds != domain.MaxSupervisorToolRounds+1 || step.ToolCalls != domain.MaxSupervisorToolRounds+1 || step.ToolBoundary ||
		step.Checkpoint.Phase != domain.SupervisorIdle || result.Submission.Message.Status != domain.OperatorSteeringCommitted {
		t.Fatalf("compaction altered ordinary segmentation: calls=%d step=%#v", len(provider.Requests()), step)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
	if content, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(content) != "original text\n" {
		t.Fatalf("read-only boundary changed the file: content=%q err=%v", content, err)
	}
}

func TestThreadToolBoundaryContinuesSixDependentWorkspaceCalls(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
	var edit fileedit.Edit
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index == 1:
			return toolResponse("list", "workspace_list", `{"version":"agent-code-tools.v1","path":".","limit":20}`), nil
		case index == 2:
			return boundaryRead("read", 1), nil
		case index == 3:
			return toolResponse("grep", "workspace_grep", `{"version":"agent-code-tools.v1","query":"original","pattern":"README.md","limit":20}`), nil
		case index >= 4 && index < domain.MaxSupervisorToolRounds:
			return boundaryRead(fmt.Sprintf("read-%d", index), 1), nil
		case index == domain.MaxSupervisorToolRounds:
			return boundaryPropose("propose"), nil
		case index == domain.MaxSupervisorToolRounds+1:
			assertBoundaryPrompt(t, request)
			edit = approveBoundaryEdit(t, st, run)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Proposal reviewed; apply it next", "", "")), nil
		case index == domain.MaxSupervisorToolRounds+2:
			assertInternalInputDelivery(t, request, input.Content)
			content := boundaryContextText(t, request)
			for _, exact := range []string{edit.ID, edit.OriginalHash, edit.ProposedHash, "workspace_change"} {
				if !strings.Contains(content, exact) {
					t.Fatalf("next segment lost %s", exact)
				}
			}
			if !hasToolSpec(request, "workspace_apply") {
				t.Fatal("next segment did not regain allowed tools")
			}
			return boundaryApply("apply", edit), nil
		case index == domain.MaxSupervisorToolRounds+3:
			return boundaryRead("verify", 1), nil
		case index == domain.MaxSupervisorToolRounds+4:
			if !hasToolResult(request, "reviewed text") {
				t.Fatal("final model did not see actual applied content")
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "Verified the reviewed file", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}
	turns := toolBoundaryService(st, st, provider)
	result, err := turns.Execute(t.Context(), input)
	if err != nil {
		t.Fatalf("dependent workspace boundary journey stopped: %v", err)
	}
	if len(provider.Requests()) != domain.MaxSupervisorToolRounds+4 || result.Submission.Message.Status != domain.OperatorSteeringCommitted || result.Execution == nil ||
		result.Execution.Handoff.Result.Status != domain.RunExecutionHandoffCompleted || result.Execution.Handoff.Result.StepsCompleted != 1 {
		t.Fatalf("one input did not remain one handoff selection: %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(content) != "reviewed text\n" {
		t.Fatalf("file=%q err=%v", content, err)
	}
	rounds, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, domain.MaxSupervisorToolRounds+10)
	if err != nil || len(rounds) != domain.MaxSupervisorToolRounds+2 {
		t.Fatalf("tool rounds=%d err=%v", len(rounds), err)
	}
	messages, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil || len(messages) != 5 {
		t.Fatalf("history=%#v err=%v", messages, err)
	}
	users := 0
	var replies []string
	evidenceByAttempt := make(map[string]int)
	for _, message := range messages {
		if err := session.ValidateStoredMessage(message); err != nil || message.SessionID != run.SessionID {
			t.Fatalf("invalid session message identity/provenance: %#v err=%v", message, err)
		}
		switch message.Role {
		case "user":
			users++
			if message.Content != input.Content || message.Provenance.SourceKind != session.SourceOperatorMessage {
				t.Fatal("fabricated user continuation")
			}
		case "assistant":
			replies = append(replies, message.Content)
		case "tool":
			if message.Provenance.SourceKind != session.SourceToolResult || message.Provenance.InstructionAuthorized {
				t.Fatal("tool evidence changed source or became authorizing")
			}
			matchedCalls := 0
			for _, round := range rounds {
				for _, call := range round.Calls {
					if message.Provenance.SourceRef != "supervisor-tools:"+call.AttemptID {
						continue
					}
					if call.Status != domain.SupervisorToolCompleted || call.ResultJSON == "" {
						t.Fatal("segment evidence recorded incomplete call")
					}
					if strings.Contains(message.Content, call.CallID) {
						if !strings.Contains(message.Content, session.ContentSHA256(call.ResultJSON)) {
							t.Fatal("segment evidence lost an exact completed result hash")
						}
						matchedCalls++
					}
				}
			}
			if matchedCalls != 6 && matchedCalls != 2 {
				t.Fatalf("unexpected segment tool evidence count=%d", matchedCalls)
			}
			if matchedCalls == 6 && !strings.Contains(message.Content, fmt.Sprintf("Earlier tool facts omitted: %d", domain.MaxSupervisorToolRounds-6)) {
				t.Fatalf("segment evidence missing omission summary: %s", message.Content)
			}
			evidenceByAttempt[message.Provenance.SourceRef]++
		default:
			t.Fatalf("unexpected extra transcript message: %#v", message)
		}
	}
	if users != 1 || len(replies) != 2 || replies[0] != "Proposal reviewed; apply it next" || replies[1] != "Verified the reviewed file" {
		t.Fatalf("segment dialogue changed: users=%d replies=%q", users, replies)
	}
	if len(evidenceByAttempt) != 2 {
		t.Fatalf("missing segment tool evidence: %#v", evidenceByAttempt)
	}
	for attempt, count := range evidenceByAttempt {
		if count != 1 {
			t.Fatalf("duplicate segment tool evidence: attempt=%s count=%d", attempt, count)
		}
	}
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil || countEventType(eventList, events.ProtocolRepairStartedEvent) != 0 {
		t.Fatalf("normal boundary recorded protocol failure: %v", err)
	}
	_, err = turns.Execute(t.Context(), input)
	if err != nil || len(provider.Requests()) != domain.MaxSupervisorToolRounds+4 {
		t.Fatalf("completed operation replayed model/tools: %v", err)
	}
}

type crashAfterToolBoundaryStore struct {
	*store.SQLiteStore
	crashed bool
}
type toolBoundaryCrash struct{}

func (s *crashAfterToolBoundaryStore) CompleteSupervisorToolBoundary(ctx context.Context, checkpoint domain.SupervisorCheckpoint, response llm.ChatResponse, action domain.RootAction, decision policy.Decision, elapsed time.Duration) (domain.Run, domain.SupervisorCheckpoint, session.TurnMessages, error) {
	run, next, messages, err := s.SQLiteStore.CompleteSupervisorToolBoundary(ctx, checkpoint, response, action, decision, elapsed)
	if err == nil && !s.crashed {
		lease, found, leaseErr := s.GetRunExecutionLease(ctx, run.ID)
		if leaseErr != nil || !found {
			panic("crash fixture lost lease")
		}
		if _, leaseErr := s.RenewRunExecutionLease(ctx, lease, 100*time.Millisecond); leaseErr != nil {
			panic(leaseErr)
		}
		s.crashed = true
		panic(toolBoundaryCrash{})
	}
	return run, next, messages, err
}

func TestThreadToolBoundaryRestartKeepsAppliedEditAndOriginalHandoff(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
	var edit fileedit.Edit
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index == 1:
			return boundaryRead("read", 1), nil
		case index == 2:
			return boundaryPropose("propose"), nil
		case index == 3:
			edit = approveBoundaryEdit(t, st, run)
			return boundaryApply("apply", edit), nil
		case index >= 4 && index <= domain.MaxSupervisorToolRounds:
			return boundaryRead(fmt.Sprintf("verify-%d", index), 1), nil
		case index == domain.MaxSupervisorToolRounds+1:
			assertBoundaryPrompt(t, request)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Applied the file; inspect it once more", "", "")), nil
		case index == domain.MaxSupervisorToolRounds+2:
			content := boundaryContextText(t, request)
			assertInternalInputDelivery(t, request, input.Content)
			if !strings.Contains(content, `"file_written":true`) || !strings.Contains(content, edit.ID) || !strings.Contains(content, edit.ProposedHash) {
				t.Fatal("restart lost completed apply facts")
			}
			return boundaryRead("inspect-after-restart", 1), nil
		case index == domain.MaxSupervisorToolRounds+3:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Verified after restart", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected restarted call %d", index)
		}
	}
	crashing := &crashAfterToolBoundaryStore{SQLiteStore: st}
	func() {
		defer func() {
			if _, ok := recover().(toolBoundaryCrash); !ok {
				t.Fatal("crash fixture did not stop after atomic rollover")
			}
		}()
		_, _ = toolBoundaryService(crashing, crashing, provider).Execute(t.Context(), input)
	}()
	checkpoint, _, err := st.GetSupervisorCheckpoint(t.Context(), run.ID)
	if err != nil || checkpoint.Phase != domain.SupervisorTurnStarted || checkpoint.NextTurn != 2 || checkpoint.PendingInput != input.Content {
		t.Fatalf("crash lost prepared next segment: %#v %v", checkpoint, err)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
	// The crashed worker's real lease must expire before takeover. No journal,
	// attempt, tool result or operator input is rewritten to simulate recovery.
	time.Sleep(150 * time.Millisecond)
	result, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input)
	if err != nil || !result.Replayed || len(provider.Requests()) != domain.MaxSupervisorToolRounds+3 {
		t.Fatalf("restart result=%#v err=%v", result, err)
	}
	rounds, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, domain.MaxSupervisorToolRounds+10)
	applies := 0
	for _, round := range rounds {
		for _, call := range round.Calls {
			if call.ToolName == "workspace_apply" {
				applies++
			}
		}
	}
	if err != nil || applies != 1 {
		t.Fatalf("restart repeated apply: %d %v", applies, err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(content) != "reviewed text\n" {
		t.Fatalf("applied file changed on restart: %q", content)
	}
}

func TestThreadToolBoundaryProtocolRepairPreservesTheSchedulingBoundary(t *testing.T) {
	st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index <= domain.MaxSupervisorToolRounds {
			return boundaryRead(fmt.Sprintf("read-%d", index), 1), nil
		}
		switch index {
		case domain.MaxSupervisorToolRounds + 1:
			assertBoundaryPrompt(t, request)
			return textResponse(`{"version":"root_lifecycle.v1","action":"continue","message":false}`), nil
		case domain.MaxSupervisorToolRounds + 2:
			if request.Metadata["protocol_repair"] != "1" || len(request.Tools) != 0 {
				t.Fatalf("actual malformed boundary response lost its protocol repair: marker=%q tools=%d messages=%d", request.Metadata["protocol_repair"], len(request.Tools), len(request.Messages))
			}
			foundBoundary := false
			for _, message := range request.Messages {
				foundBoundary = foundBoundary || strings.Contains(message.Content, "scheduling boundary")
			}
			if !foundBoundary {
				t.Fatal("repair forgot that Go is requesting an internal tool boundary")
			}
			return textResponse(rootActionResponse(domain.RootActionContinue, "One remaining read", "", "")), nil
		case domain.MaxSupervisorToolRounds + 3:
			boundaryContextText(t, request)
			if request.Metadata["protocol_repair"] != "" || !hasToolSpec(request, "workspace_read") {
				t.Fatal("next segment inherited protocol repair or lost its tools")
			}
			return boundaryRead("last-read", 1), nil
		case domain.MaxSupervisorToolRounds + 4:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Read completed", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}
	result, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input)
	if err != nil || len(provider.Requests()) != domain.MaxSupervisorToolRounds+4 || result.Execution == nil || result.Execution.Handoff.Result.StepsCompleted != 1 {
		t.Fatalf("repaired segment did not continue the original input: %#v %v", result, err)
	}
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil || countEventType(eventList, events.ProtocolRepairStartedEvent) != 1 || countEventType(eventList, events.ProtocolRepairCompletedEvent) != 1 {
		t.Fatalf("actual protocol repair evidence lost or duplicated: %v", err)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
}

func TestThreadToolBoundaryHonorsTurnBudgetAndNaturalWait(t *testing.T) {
	for _, test := range []struct {
		name           string
		turns          int
		tools          int64
		boundaryRounds int
		action         domain.RootActionKind
	}{
		{"turn_budget", 1, int64(domain.MaxSupervisorToolRounds + 10), domain.MaxSupervisorToolRounds, domain.RootActionContinue},
		{"tool_budget", 4, 4, 4, domain.RootActionContinue},
		{"real_wait", 4, int64(domain.MaxSupervisorToolRounds + 10), domain.MaxSupervisorToolRounds, domain.RootActionWait},
		{"real_finish", 4, int64(domain.MaxSupervisorToolRounds + 10), domain.MaxSupervisorToolRounds, domain.RootActionFinish},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: test.turns, MaxToolCalls: test.tools})
			provider := &boundaryJourneyProvider{}
			provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				if index <= test.boundaryRounds {
					return toolResponse(fmt.Sprintf("list-%d", index), "workspace_list", fmt.Sprintf(`{"version":"agent-code-tools.v1","path":".","limit":%d}`, (index%20)+1)), nil
				}
				if index != test.boundaryRounds+1 {
					return nil, fmt.Errorf("boundary continued past %s", test.name)
				}
				assertBoundaryPrompt(t, request)
				lastContent := request.Messages[len(request.Messages)-1].Content
				if test.name == "tool_budget" {
					if !strings.Contains(lastContent, "0 tool calls remain") || strings.Contains(lastContent, "Return continue if the same accepted user task needs more work") {
						t.Fatalf("ToolBudgetZero prompt must warn 0 tool calls remain instead of instructing continue: %q", lastContent)
					}
				} else if !strings.Contains(lastContent, "Return continue if the same accepted user task needs more work") {
					t.Fatalf("normal 24-round boundary prompt must advertise continue within remaining budget: %q", lastContent)
				}
				summary, reason := "", ""
				if test.action == domain.RootActionFinish {
					summary = "finished"
				}
				if test.action == domain.RootActionWait {
					reason = "operator review required"
				}
				return textResponse(rootActionResponse(test.action, "Observed the file", summary, reason)), nil
			}
			result, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input)
			if test.action == domain.RootActionContinue {
				var failed *application.ThreadTurnFailedError
				if !errors.As(err, &failed) || apperror.CodeOf(err) != apperror.CodeResourceExhausted {
					t.Fatalf("real budget was hidden: %#v %v", result, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(provider.Requests()) != test.boundaryRounds+1 {
				t.Fatalf("natural boundary called another model: got=%d want=%d", len(provider.Requests()), test.boundaryRounds+1)
			}
			checkpoint, _, lookupErr := st.GetSupervisorCheckpoint(t.Context(), run.ID)
			if lookupErr != nil || checkpoint.Phase == domain.SupervisorTurnStarted {
				t.Fatalf("boundary left prepared execution: %#v %v", checkpoint, lookupErr)
			}
		})
	}
}

func TestThreadToolBoundaryStopPreservesOneInputAndQueuedFollowup(t *testing.T) {
	st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
	started := make(chan struct{})
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index <= domain.MaxSupervisorToolRounds {
			return boundaryRead(fmt.Sprintf("read-%d", index), 1), nil
		}
		if index == domain.MaxSupervisorToolRounds+1 {
			return textResponse(rootActionResponse(domain.RootActionContinue, "Need another segment", "", "")), nil
		}
		if index != domain.MaxSupervisorToolRounds+2 {
			return nil, fmt.Errorf("unexpected post-stop call %d", index)
		}
		boundaryContextText(t, request)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	turns := toolBoundaryService(st, st, provider)
	done := make(chan error, 1)
	go func() { _, err := turns.Execute(t.Context(), input); done <- err }()
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("model did not start")
	}
	queued := input
	queued.Content, queued.OperationKey = "Keep this separately accepted next request", "tool-boundary-followup"
	accepted, err := turns.Execute(t.Context(), queued)
	if err != nil || accepted.Submission.Message.Status != domain.OperatorSteeringPending {
		t.Fatalf("queued input=%#v err=%v", accepted, err)
	}
	state, err := turns.ExecutionState(t.Context(), input.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := turns.Interrupt(t.Context(), input.ThreadID, state.ExecutionID); err != nil {
		t.Fatal(err)
	}
	err = awaitTurnResult(t, done)
	var stopped *application.ThreadTurnFailedError
	if !errors.As(err, &stopped) || apperror.CodeOf(err) != apperror.CodeCancelled {
		t.Fatalf("stop did not seal the current segment: %v", err)
	}
	messages, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, message := range messages {
		if message.Role == "user" {
			users++
			if message.Content != input.Content {
				t.Fatal("stop consumed the follow-up")
			}
		}
	}
	if users != 1 {
		t.Fatalf("stop duplicated original input: %d", users)
	}
	message, err := st.GetOperatorSteering(t.Context(), accepted.Submission.Message.ID)
	if err != nil || message.Status != domain.OperatorSteeringPending || message.Prepared {
		t.Fatalf("follow-up changed: %#v %v", message, err)
	}
	checkpoint, _, err := st.GetSupervisorCheckpoint(t.Context(), run.ID)
	if err != nil || checkpoint.Phase != domain.SupervisorIdle {
		t.Fatalf("stop left execution active: %#v %v", checkpoint, err)
	}
	if len(provider.Requests()) != domain.MaxSupervisorToolRounds+2 {
		t.Fatal("stop called the model again")
	}
}

type privateHistoryBoundaryProvider struct {
	boundaryJourneyProvider
}

func (p *privateHistoryBoundaryProvider) RequiresPrivateAssistantHistory(string) bool {
	return true
}

type boundaryApprovalChecker struct {
	base        policy.Checker
	needsReview func(tools.Call) bool
}

func (c boundaryApprovalChecker) CheckText(ctx, text string) policy.Decision {
	return c.base.CheckText(ctx, text)
}

func (c boundaryApprovalChecker) CheckToolCall(call tools.Call) policy.Decision {
	d := c.base.CheckToolCall(call)
	if c.needsReview != nil && c.needsReview(call) {
		d.NeedsApproval = true
	}
	return d
}

func TestThreadToolBoundaryRunsTenNativeRoundsUninterrupted(t *testing.T) {
	st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 4, MaxToolCalls: 30})
	provider := &boundaryJourneyProvider{}
	provider.respond = func(_ context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index <= 10 {
			if len(request.Tools) == 0 {
				return nil, fmt.Errorf("tools prematurely withdrawn at round %d", index)
			}
			return boundaryRead(fmt.Sprintf("ten-round-%d", index), 1), nil
		}
		if len(request.Tools) == 0 {
			return nil, errors.New("unexpected boundary tool withdrawal after 10 rounds")
		}
		return textResponse(rootActionResponse(domain.RootActionFinish, "Completed 10 native rounds in one segment", "done", "")), nil
	}
	result, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input)
	if err != nil {
		t.Fatalf("10-round native segment failed: %v", err)
	}
	step := result.Execution.Execution.Steps[0]
	if step.ToolRounds != 10 || step.ToolCalls != 10 || step.ModelAttempts != 11 || step.ToolBoundary {
		t.Fatalf("unexpected 10-round step metrics: %#v", step)
	}
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range eventList {
		if ev.Type == events.AgentTurnCompletedEvent && strings.Contains(ev.PayloadJSON, `"tool_round_boundary":true`) {
			t.Fatalf("unexpected tool_round_boundary rollover event during 10-round segment: %s", ev.PayloadJSON)
		}
	}
}

func TestThreadToolBoundaryCrossesFortyEightCallsAcrossSegmentRollover(t *testing.T) {
	st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 100})
	provider := &boundaryJourneyProvider{}
	provider.respond = func(_ context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch {
		case index <= domain.MaxSupervisorToolRounds:
			return toolBatchResponse(
				llm.ToolCall{ID: fmt.Sprintf("r%d-a", index), Name: "workspace_list", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":".","limit":10}`)},
				llm.ToolCall{ID: fmt.Sprintf("r%d-b", index), Name: "workspace_read", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":"README.md","start_line":1,"end_line":2}`)},
			), nil
		case index == domain.MaxSupervisorToolRounds+1:
			assertBoundaryPrompt(t, request)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Rolling over 48 calls into segment 2", "", "")), nil
		case index == domain.MaxSupervisorToolRounds+2:
			content := boundaryContextText(t, request)
			if !strings.Contains(content, "Selected 48 of 48 returned receipts") {
				t.Fatalf("48-call boundary receipt truncated calls: %s", content)
			}
			return boundaryRead("seg2-final", 1), nil
		default:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Completed 49 calls across rollover", "done", "")), nil
		}
	}
	result, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input)
	if err != nil {
		t.Fatalf("48-call segment rollover failed: %v", err)
	}
	step := result.Execution.Execution.Steps[0]
	if step.ToolRounds != domain.MaxSupervisorToolRounds+1 || step.ToolCalls != domain.MaxSupervisorToolRounds*2+1 {
		t.Fatalf("unexpected rollover stats: %#v", step)
	}
	eventList, err := st.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rollovers := 0
	for _, ev := range eventList {
		if ev.Type == events.AgentTurnCompletedEvent && strings.Contains(ev.PayloadJSON, `"tool_round_boundary":true`) {
			rollovers++
		}
	}
	if rollovers != 1 {
		t.Fatalf("expected 1 boundary rollover, got %d", rollovers)
	}
}

func TestThreadToolBoundaryWithdrawsToolsImmediatelyOnToolBudgetZero(t *testing.T) {
	for _, action := range []domain.RootActionKind{domain.RootActionFinish, domain.RootActionContinue} {
		t.Run(string(action), func(t *testing.T) {
			st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 4, MaxToolCalls: 2})
			provider := &boundaryJourneyProvider{}
			provider.respond = func(_ context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				if index == 1 {
					return toolBatchResponse(
						llm.ToolCall{ID: "tb-1", Name: "workspace_list", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":".","limit":10}`)},
						llm.ToolCall{ID: "tb-2", Name: "workspace_read", Arguments: json.RawMessage(`{"version":"agent-code-tools.v1","path":"README.md","start_line":1,"end_line":2}`)},
					), nil
				}
				if index != 2 {
					return nil, fmt.Errorf("unexpected extra call %d after ToolBudgetZero", index)
				}
				assertBoundaryPrompt(t, request)
				lastContent := request.Messages[len(request.Messages)-1].Content
				if !strings.Contains(lastContent, "0 tool calls remain") || strings.Contains(lastContent, "Return continue if the same accepted user task needs more work") {
					t.Fatalf("ToolBudgetZero prompt must warn 0 tool calls remain instead of instructing continue: %q", lastContent)
				}
				if action == domain.RootActionFinish {
					return textResponse(rootActionResponse(domain.RootActionFinish, "Finished on tool budget boundary", "done", "")), nil
				}
				return textResponse(rootActionResponse(domain.RootActionContinue, "Try continue past budget", "", "")), nil
			}
			result, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input)
			if action == domain.RootActionContinue {
				var failed *application.ThreadTurnFailedError
				if !errors.As(err, &failed) || apperror.CodeOf(err) != apperror.CodeResourceExhausted {
					t.Fatalf("expected CodeResourceExhausted on continue past ToolBudgetZero, got result=%#v err=%v", result, err)
				}
			} else {
				if err != nil || result.Execution.Execution.Steps[0].ToolRounds != 1 || result.Execution.Execution.Steps[0].ToolCalls != 2 {
					t.Fatalf("expected clean finish on ToolBudgetZero, got result=%#v err=%v", result, err)
				}
			}
			usage, usageErr := st.GetToolCallUsage(t.Context(), run.ID)
			if usageErr != nil || usage.Consumed != 2 || usage.Remaining != 0 {
				t.Fatalf("unexpected tool usage: %#v err=%v", usage, usageErr)
			}
		})
	}
}

func TestThreadToolBoundaryPrivateAssistantHistoryContextPressure(t *testing.T) {
	runPrivatePressure := func(t *testing.T, handler func(llm.ChatRequest, int) (*llm.ChatResponse, error)) (application.ExecuteThreadTurnResult, []*llm.ChatRequest, error) {
		t.Helper()
		st, _, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 40})
		big := strings.Repeat("private history context pressure payload line\n", 100)
		if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o600); err != nil {
			t.Fatal(err)
		}
		provider := &privateHistoryBoundaryProvider{}
		provider.respond = func(_ context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			return handler(request, index)
		}
		router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
		router.RegisterProvider(provider)
		if err := router.SetContextWindow(llm.ModelRef{Provider: provider.Name(), Model: "model"}, llm.ContextWindow{
			ProtocolVersion:     llm.ContextWindowProtocolVersion,
			WindowTokens:        16384,
			SafetyMarginTokens:  512,
			DefaultOutputTokens: 512,
			MaxOutputTokens:     1024,
			Source:              "test_private_pressure",
		}); err != nil {
			t.Fatal(err)
		}
		turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
			application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()).WithGeneratedContextCompaction(false))
		res, err := turns.Execute(t.Context(), input)
		reqs := provider.Requests()
		ptrs := make([]*llm.ChatRequest, len(reqs))
		for i := range reqs {
			ptrs[i] = &reqs[i]
		}
		return res, ptrs, err
	}

	t.Run("withdraws_tools_and_finishes_cleanly", func(t *testing.T) {
		withdrew := false
		res, reqs, err := runPrivatePressure(t, func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if len(request.Tools) == 0 {
				withdrew = true
				last := request.Messages[len(request.Messages)-1].Content
				if !strings.Contains(last, "private-history context window limit") || strings.Contains(last, "scheduling boundary") {
					return nil, fmt.Errorf("unexpected context-pressure prompt: %s", last)
				}
				return textResponse(rootActionResponse(domain.RootActionFinish, "Finished under private-history pressure", "done", "")), nil
			}
			if index > 15 {
				return nil, errors.New("context pressure did not withdraw tools before 15 rounds")
			}
			return toolResponse(fmt.Sprintf("ph-%d", index), "workspace_read", `{"version":"agent-code-tools.v1","path":"big.txt","start_line":1,"end_line":80}`), nil
		})
		if err != nil || !withdrew {
			t.Fatalf("private-history finish failed: withdrew=%t err=%v reqs=%d", withdrew, err, len(reqs))
		}
		if res.Execution.Execution.Steps[0].ToolBoundary {
			t.Fatal("private-history context pressure must not roll over via CompleteSupervisorToolBoundary")
		}
	})

	t.Run("withdraws_tools_and_blocks_continue_with_wait_or_finish_repair", func(t *testing.T) {
		sawContinueRepair := false
		res, _, err := runPrivatePressure(t, func(request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if len(request.Tools) == 0 {
				if request.Metadata["protocol_repair"] == "1" {
					sawContinueRepair = true
					last := request.Messages[len(request.Messages)-1].Content
					if strings.Contains(last, `"action":"continue"`) || !strings.Contains(last, `action="finish"`) {
						return nil, fmt.Errorf("context-pressure repair allowed continue: %s", last)
					}
					return textResponse(rootActionResponse(domain.RootActionWait, "Awaiting smaller scope", "", "context window full")), nil
				}
				return textResponse(rootActionResponse(domain.RootActionContinue, "Try continue", "", "")), nil
			}
			return toolResponse(fmt.Sprintf("phw-%d", index), "workspace_read", `{"version":"agent-code-tools.v1","path":"big.txt","start_line":1,"end_line":80}`), nil
		})
		if err != nil || !sawContinueRepair || res.Execution.Execution.Steps[0].Action.Kind != domain.RootActionWait {
			t.Fatalf("expected continue rejection -> wait repair under context pressure, got err=%v sawRepair=%t res=%#v", err, sawContinueRepair, res)
		}
	})

	t.Run("provider_context_limit_recovery_withdraws_tools", func(t *testing.T) {
		st, _, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
		provider := &privateHistoryBoundaryProvider{}
		recovered := false
		provider.respond = func(_ context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			switch index {
			case 1:
				return boundaryRead("pre-limit-read", 1), nil
			case 2:
				if len(request.Tools) == 0 {
					return nil, errors.New("expected tools on attempt before provider context_limit")
				}
				return nil, &llm.ProviderError{
					Kind: llm.OutcomePermanent, Reason: llm.ProviderFailureContextLimit,
					Provider: provider.Name(), Message: "provider context length exceeded",
				}
			case 3:
				if len(request.Tools) != 0 {
					return nil, errors.New("expected tools withdrawn on private-history context_limit recovery retry")
				}
				recovered = true
				return textResponse(rootActionResponse(domain.RootActionFinish, "Recovered after tool withdrawal", "done", "")), nil
			default:
				return nil, fmt.Errorf("unexpected call %d", index)
			}
		}
		router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
		router.RegisterProvider(provider)
		turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
			application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()).WithGeneratedContextCompaction(false))
		res, err := turns.Execute(t.Context(), input)
		if err != nil || !recovered || res.Execution.Execution.Steps[0].RequestedAction != domain.RootActionFinish {
			t.Fatalf("provider context_limit recovery failed: recovered=%t err=%v res=%#v", recovered, err, res)
		}
	})
}

func TestThreadToolBoundaryPreservesSteeringAndApprovalPastFourRounds(t *testing.T) {
	t.Run("waiting_approval_pauses_at_round_six", func(t *testing.T) {
		st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 4, MaxToolCalls: 30})
		checker := boundaryApprovalChecker{
			base:        policy.NewDefaultChecker(),
			needsReview: func(c tools.Call) bool { return c.Name == "replace_file" },
		}
		provider := &boundaryJourneyProvider{}
		provider.respond = func(_ context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			if index <= 5 {
				return boundaryRead(fmt.Sprintf("app-r-%d", index), 1), nil
			}
			if index == 6 {
				return boundaryPropose("propose-at-6"), nil
			}
			return textResponse(rootActionResponse(domain.RootActionWait, "Awaiting approval at round 6", "", "operator review required")), nil
		}
		res, err := toolBoundaryServiceWithPolicy(st, st, provider, checker).Execute(t.Context(), input)
		if err != nil || res.Execution.Execution.Steps[0].Action.Kind != domain.RootActionWait {
			t.Fatalf("expected wait for approval at round 6, got res=%#v err=%v", res, err)
		}
		edits, err := st.ListFileEdits(t.Context(), fileedit.ListFilter{SessionID: run.SessionID})
		if err != nil || len(edits) != 1 || edits[0].Status != fileedit.StatusProposed {
			t.Fatalf("expected proposed edit at round 6, got %#v err=%v", edits, err)
		}
	})

	t.Run("mid_turn_steering_supersedes_at_round_six", func(t *testing.T) {
		st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 4, MaxToolCalls: 30})
		round6Started := make(chan struct{})
		releaseRound6 := make(chan struct{})
		sawSteer := false
		provider := &boundaryJourneyProvider{}
		provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
			switch {
			case index <= 5:
				return boundaryRead(fmt.Sprintf("steer-r-%d", index), 1), nil
			case index == 6:
				close(round6Started)
				select {
				case <-releaseRound6:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return boundaryRead("stale-round-6", 1), nil
			default:
				for _, m := range request.Messages {
					if strings.Contains(m.Content, "Redirect at round 6") {
						sawSteer = true
					}
				}
				return textResponse(rootActionResponse(domain.RootActionFinish, "Steered cleanly at round 6", "done", "")), nil
			}
		}
		turns := toolBoundaryService(st, st, provider)
		done := make(chan error, 1)
		go func() {
			_, err := turns.Execute(context.Background(), input)
			done <- err
		}()
		awaitTurnSignal(t, round6Started)
		if _, err := application.NewSessionMessageSubmissionService(st).Submit(t.Context(), application.SubmitSessionMessageRequest{
			Version: domain.SessionMessageSubmissionProtocolVersion, SessionID: run.SessionID,
			Content: "Redirect at round 6", OperationKey: "steer-round-6-op-key", RequestedBy: "test_operator",
			DeliveryMode: domain.OperatorSteeringCurrentTurn,
		}); err != nil {
			close(releaseRound6)
			t.Fatal(err)
		}
		close(releaseRound6)
		if err := awaitTurnResult(t, done); err != nil || !sawSteer {
			t.Fatalf("steering at round 6 failed: err=%v sawSteer=%t", err, sawSteer)
		}
		rounds, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		superseded := 0
		for _, r := range rounds {
			for _, c := range r.Calls {
				if c.ErrorCode == "steering_superseded" {
					superseded++
				}
			}
		}
		if superseded == 0 {
			t.Fatalf("expected steering_superseded call at round 6, got rounds=%#v", rounds)
		}
	})
}
