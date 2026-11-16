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
		if start < 0 || json.Unmarshal([]byte(message.Content[start:]), &value) != nil || value.Authorized || message.Role != "user" || len([]rune(value.Content)) > 16*1024 {
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
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
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
		case index <= 4:
			return boundaryRead(fmt.Sprintf("compaction-read-%d", index), 1), nil
		case index == 5:
			assertBoundaryPrompt(t, request)
			return textResponse(rootActionResponse(domain.RootActionContinue, "One final observation remains", "", "")), nil
		case index == 6:
			assertInternalInputDelivery(t, request, input.Content)
			boundaryContextText(t, request)
			return boundaryRead("compaction-final-read", 1), nil
		case index == 7:
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
	if len(provider.Requests()) != 7 || step.ToolRounds != 5 || step.ToolCalls != 5 || step.ToolBoundary ||
		step.Checkpoint.Phase != domain.SupervisorIdle || result.Submission.Message.Status != domain.OperatorSteeringCommitted {
		t.Fatalf("compaction altered ordinary segmentation: calls=%d step=%#v", len(provider.Requests()), step)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
	if content, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(content) != "original text\n" {
		t.Fatalf("read-only boundary changed the file: content=%q err=%v", content, err)
	}
}

func TestThreadToolBoundaryContinuesSixDependentWorkspaceCalls(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	var edit fileedit.Edit
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return toolResponse("list", "workspace_list", `{"version":"agent-code-tools.v1","path":".","limit":20}`), nil
		case 2:
			return boundaryRead("read", 1), nil
		case 3:
			return toolResponse("grep", "workspace_grep", `{"version":"agent-code-tools.v1","query":"original","pattern":"README.md","limit":20}`), nil
		case 4:
			return boundaryPropose("propose"), nil
		case 5:
			assertBoundaryPrompt(t, request)
			edit = approveBoundaryEdit(t, st, run)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Proposal reviewed; apply it next", "", "")), nil
		case 6:
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
		case 7:
			return boundaryRead("verify", 1), nil
		case 8:
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
		t.Fatalf("six-call journey stopped: %v", err)
	}
	if len(provider.Requests()) != 8 || result.Submission.Message.Status != domain.OperatorSteeringCommitted || result.Execution == nil ||
		result.Execution.Handoff.Result.Status != domain.RunExecutionHandoffCompleted || result.Execution.Handoff.Result.StepsCompleted != 1 {
		t.Fatalf("one input did not remain one handoff selection: %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(content) != "reviewed text\n" {
		t.Fatalf("file=%q err=%v", content, err)
	}
	rounds, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 10)
	if err != nil || len(rounds) != 6 {
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
					if call.Status != domain.SupervisorToolCompleted || call.ResultJSON == "" ||
						!strings.Contains(message.Content, call.CallID) || !strings.Contains(message.Content, session.ContentSHA256(call.ResultJSON)) {
						t.Fatal("segment evidence lost an exact completed tool call or result hash")
					}
					matchedCalls++
				}
			}
			if matchedCalls != 4 && matchedCalls != 2 {
				t.Fatalf("unexpected segment tool evidence count=%d", matchedCalls)
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
	if err != nil || len(provider.Requests()) != 8 {
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
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	var edit fileedit.Edit
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			return boundaryRead("read", 1), nil
		case 2:
			return boundaryPropose("propose"), nil
		case 3:
			edit = approveBoundaryEdit(t, st, run)
			return boundaryApply("apply", edit), nil
		case 4:
			return boundaryRead("verify", 1), nil
		case 5:
			assertBoundaryPrompt(t, request)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Applied the file; inspect it once more", "", "")), nil
		case 6:
			content := boundaryContextText(t, request)
			assertInternalInputDelivery(t, request, input.Content)
			if !strings.Contains(content, `"file_written":true`) || !strings.Contains(content, edit.ID) || !strings.Contains(content, edit.ProposedHash) {
				t.Fatal("restart lost completed apply facts")
			}
			return boundaryRead("inspect-after-restart", 1), nil
		case 7:
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
	if err != nil || !result.Replayed || len(provider.Requests()) != 7 {
		t.Fatalf("restart result=%#v err=%v", result, err)
	}
	rounds, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 10)
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
	st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index <= 4 {
			return boundaryRead(fmt.Sprintf("read-%d", index), 1), nil
		}
		switch index {
		case 5:
			assertBoundaryPrompt(t, request)
			return textResponse(`{"version":"root_lifecycle.v1","action":"continue","message":false}`), nil
		case 6:
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
		case 7:
			boundaryContextText(t, request)
			if request.Metadata["protocol_repair"] != "" || !hasToolSpec(request, "workspace_read") {
				t.Fatal("next segment inherited protocol repair or lost its tools")
			}
			return boundaryRead("last-read", 1), nil
		case 8:
			return textResponse(rootActionResponse(domain.RootActionFinish, "Read completed", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected model call %d", index)
		}
	}
	result, err := toolBoundaryService(st, st, provider).Execute(t.Context(), input)
	if err != nil || len(provider.Requests()) != 8 || result.Execution == nil || result.Execution.Handoff.Result.StepsCompleted != 1 {
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
		name   string
		turns  int
		tools  int64
		action domain.RootActionKind
	}{{"turn_budget", 1, 20, domain.RootActionContinue}, {"tool_budget", 4, 4, domain.RootActionContinue}, {"real_wait", 4, 20, domain.RootActionWait}, {"real_finish", 4, 20, domain.RootActionFinish}} {
		t.Run(test.name, func(t *testing.T) {
			st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: test.turns, MaxToolCalls: test.tools})
			provider := &boundaryJourneyProvider{}
			provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				if index <= 4 {
					return toolResponse(fmt.Sprintf("list-%d", index), "workspace_list", fmt.Sprintf(`{"version":"agent-code-tools.v1","path":".","limit":%d}`, index)), nil
				}
				if index != 5 {
					return nil, fmt.Errorf("boundary continued past %s", test.name)
				}
				assertBoundaryPrompt(t, request)
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
			if len(provider.Requests()) != 5 {
				t.Fatal("natural boundary called another model")
			}
			checkpoint, _, lookupErr := st.GetSupervisorCheckpoint(t.Context(), run.ID)
			if lookupErr != nil || checkpoint.Phase == domain.SupervisorTurnStarted {
				t.Fatalf("boundary left prepared execution: %#v %v", checkpoint, lookupErr)
			}
		})
	}
}

func TestThreadToolBoundaryStopPreservesOneInputAndQueuedFollowup(t *testing.T) {
	st, run, _, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	started := make(chan struct{})
	provider := &boundaryJourneyProvider{}
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if index <= 4 {
			return boundaryRead(fmt.Sprintf("read-%d", index), 1), nil
		}
		if index == 5 {
			return textResponse(rootActionResponse(domain.RootActionContinue, "Need another segment", "", "")), nil
		}
		if index != 6 {
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
	awaitTurnSignal(t, started)
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
	if len(provider.Requests()) != 6 {
		t.Fatal("stop called the model again")
	}
}
