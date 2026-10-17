package application

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

// Legacy small-allocation vectors remain useful. Production capacity is derived
// from the fully prepared request rather than this test-only allocation.
const supervisorSegmentReceiptTokenBudget = 2048

// The stdout shapes below deliberately miss the strict web_fetch projection so
// supervisorToolContextResult returns the sealed ResultJSON unchanged and the
// receipt projection is deterministic.
var segmentReceiptFatBody = "AFTER_FAT_BODY_MARKER" + strings.Repeat("长文", 2000)

func segmentReceiptEnvelope(t *testing.T, stdout string) string {
	t.Helper()
	envelope, err := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{
		Version: supervisorToolResultVersion, Tool: "web_fetch", Status: "completed", Stdout: stdout})
	if err != nil {
		t.Fatal(err)
	}
	return string(envelope)
}

func segmentReceiptCall(t *testing.T, round, position int, stdout string) domain.SupervisorToolCall {
	t.Helper()
	at := time.Date(2026, 9, 14, 10, round, position, 0, time.UTC)
	return domain.SupervisorToolCall{
		RunID: "run-receipt", AttemptID: "attempt-receipt", Turn: 3, Round: round, Position: position,
		ModelAttempt: 1, CallID: fmt.Sprintf("call-%d-%d", round, position), ToolName: "web_fetch",
		PayloadJSON:   `{"version":"web_fetch.v1","url":"https://example.com/page"}`,
		AuthorityJSON: `{}`, Status: domain.SupervisorToolCompleted,
		ResultJSON: segmentReceiptEnvelope(t, stdout), CreatedAt: at, CompletedAt: &at,
	}
}

func segmentReceiptRound(t *testing.T, round int, calls int, stdout func(position int) string) domain.SupervisorToolRound {
	t.Helper()
	at := time.Date(2026, 9, 14, 10, round, 0, 0, time.UTC)
	result := domain.SupervisorToolRound{RunID: "run-receipt", AttemptID: "attempt-receipt",
		Turn: 3, Round: round, ModelAttempt: 1, CreatedAt: at, CompletedAt: &at}
	for position := 1; position <= calls; position++ {
		result.Calls = append(result.Calls, segmentReceiptCall(t, round, position, stdout(position)))
	}
	return result
}

func segmentReceiptReferencedCallIDs(messages []llm.Message) map[string]bool {
	ids := map[string]bool{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			ids[call.ID] = true
		}
		for _, result := range message.ToolResults {
			ids[result.ToolCallID] = true
		}
	}
	return ids
}

func segmentReceiptMeasuredTokens(t *testing.T, plan supervisorSegmentReceipt, sessionID, attemptID string) int {
	t.Helper()
	message := supervisorSegmentReceiptMessage(sessionID, attemptID, plan.ReceiptContent)
	return estimateModelRequestTokens(llm.ChatRequest{Messages: []llm.Message{message}}) - 8
}

func TestSupervisorSegmentReceiptZeroChangeInsideWindow(t *testing.T) {
	rounds := []domain.SupervisorToolRound{
		segmentReceiptRound(t, 1, 2, func(int) string { return `{"protocol_version":"unsupported_shape"}` }),
		segmentReceiptRound(t, 2, 1, func(int) string { return `{"protocol_version":"unsupported_shape"}` }),
	}
	plan, err := supervisorSegmentReceiptPlan(rounds, 0, supervisorSegmentReceiptTokenBudget, "attempt-receipt")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.NativeRounds) != 2 || len(plan.ReceiptedRounds) != 0 ||
		plan.ReceiptContent != "" || plan.ReceiptTokens != 0 {
		t.Fatalf("zero receipt count changed the plan: %+v", plan)
	}
	base := llm.ChatRequest{Messages: []llm.Message{
		{Role: "system", Content: "policy"}, {Role: "user", Content: "current input"}}}
	rebuilt, err := supervisorRequestWithSegmentReceipt(base, plan, "session-receipt", "attempt-receipt")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := supervisorRequestWithToolRounds(base, rounds)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rebuilt, plain) {
		t.Fatal("a fitting window must reproduce the plain tool-round expansion exactly")
	}
}

func TestSupervisorSegmentReceiptWithdrawsOldestRoundsAndKeepsRecentNative(t *testing.T) {
	rounds := []domain.SupervisorToolRound{
		segmentReceiptRound(t, 1, 1, func(int) string { return `{"protocol_version":"unsupported_shape"}` }),
		segmentReceiptRound(t, 2, 1, func(int) string { return `{"protocol_version":"unsupported_shape"}` }),
		segmentReceiptRound(t, 3, 2, func(int) string {
			return `{"protocol_version":"unsupported_shape","snapshot":{"snapshot_id":"snap-native","next_offset":6144}}`
		}),
	}
	plan, err := supervisorSegmentReceiptPlan(rounds, 2, supervisorSegmentReceiptTokenBudget, "attempt-receipt")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ReceiptedRounds) != 2 || len(plan.NativeRounds) != 1 ||
		plan.ReceiptedRounds[0].Round != 1 || plan.ReceiptedRounds[1].Round != 2 ||
		plan.NativeRounds[0].Round != 3 {
		t.Fatalf("unexpected round split: %+v", plan)
	}
	base := llm.ChatRequest{Messages: []llm.Message{
		{Role: "system", Content: "policy"}, {Role: "user", Content: "current input"}}}
	rebuilt, err := supervisorRequestWithSegmentReceipt(base, plan, "session-receipt", "attempt-receipt")
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuilt.Messages) != 5 || rebuilt.Messages[2].Role != "user" ||
		rebuilt.Messages[3].ToolCalls[0].ID != "call-3-1" ||
		rebuilt.Messages[4].ToolResults[1].ToolCallID != "call-3-2" {
		t.Fatalf("receipt was not inserted before the retained native pairs: %+v", rebuilt.Messages)
	}
	if referenced := segmentReceiptReferencedCallIDs(rebuilt.Messages); referenced["call-1-1"] || referenced["call-2-1"] {
		t.Fatal("a receipted round reappeared as a native tool call or result pair")
	}
	if !strings.Contains(rebuilt.Messages[2].Content, "Only omitted or unknown outcomes require history_read before inferring success") {
		t.Fatal("receipt lost its omission warning")
	}
	if rebuilt.Metadata["context_segment_receipted_rounds"] != "2" ||
		rebuilt.Metadata["tool_round"] != "1" || rebuilt.Metadata["context_segment_receipt_tokens"] == "" {
		t.Fatalf("receipt metadata missing: %#v", rebuilt.Metadata)
	}
}

func TestSupervisorSegmentReceiptKeepsIdentityReadbackAndCursors(t *testing.T) {
	plainRound := segmentReceiptRound(t, 1, 1, func(int) string { return "plain non-JSON tool output" })
	cursorRound := segmentReceiptRound(t, 2, 1, func(int) string {
		return `{"protocol_version":"unsupported_shape","snapshot":{"snapshot_id":"snap-cursor","next_offset":2048},"body":"` + segmentReceiptFatBody + `"}`
	})
	plan, err := supervisorSegmentReceiptPlan([]domain.SupervisorToolRound{plainRound, cursorRound},
		2, supervisorSegmentReceiptTokenBudget, "attempt-receipt")
	if err != nil {
		t.Fatal(err)
	}
	content := plan.ReceiptContent
	for _, expected := range []string{
		"call-1-1", "call-2-1", "web_fetch", `"status":"completed"`, "result_sha256",
		`"part":"result"`, `"source_id":"tool:`, "history_read", `"next_offset":2048`, "snap-cursor",
		`"result_details_omitted":true`,
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("receipt lost %q:\n%s", expected, content)
		}
	}
	if strings.Contains(content, "AFTER_FAT_BODY_MARKER") {
		t.Fatal("receipt copied a large omitted body into the model view")
	}
	if !strings.Contains(content, session.ContentSHA256(cursorRound.Calls[0].ResultJSON)) {
		t.Fatal("receipt lost the exact sealed result digest")
	}
	if measured := segmentReceiptMeasuredTokens(t, plan, "session-receipt", "attempt-receipt"); measured != plan.ReceiptTokens {
		t.Fatalf("receipt token estimate %d differs from the plan's %d", measured, plan.ReceiptTokens)
	}
}

func TestSupervisorSegmentReceiptRejectsIncompleteOrUnpairedRounds(t *testing.T) {
	complete := segmentReceiptRound(t, 1, 1, func(int) string { return `{"protocol_version":"unsupported_shape"}` })
	pendingAt := complete.CreatedAt
	pending := complete
	pending.CompletedAt = nil
	pending.Calls = []domain.SupervisorToolCall{func() domain.SupervisorToolCall {
		call := complete.Calls[0]
		call.Status, call.ResultJSON, call.CompletedAt = domain.SupervisorToolPending, "", nil
		return call
	}()}
	unpaired := complete
	unpaired.Calls = []domain.SupervisorToolCall{func() domain.SupervisorToolCall {
		call := complete.Calls[0]
		call.Position = 2
		return call
	}()}
	for name, rounds := range map[string][]domain.SupervisorToolRound{
		"pending":  {pending},
		"unpaired": {unpaired},
		"empty": {domain.SupervisorToolRound{RunID: "run-receipt", AttemptID: "attempt-receipt",
			Turn: 1, Round: 1, ModelAttempt: 1, CreatedAt: pendingAt}},
	} {
		if _, err := supervisorSegmentReceiptPlan(rounds, 1, supervisorSegmentReceiptTokenBudget, "attempt-receipt"); err == nil {
			t.Fatalf("%s round accepted for receipting", name)
		}
	}
	if _, err := supervisorSegmentReceiptPlan([]domain.SupervisorToolRound{pending}, 0,
		supervisorSegmentReceiptTokenBudget, "attempt-receipt"); err == nil {
		t.Fatal("zero receipt count must still reject an incomplete round")
	}
	if _, err := supervisorSegmentReceiptPlan([]domain.SupervisorToolRound{complete}, -1,
		supervisorSegmentReceiptTokenBudget, "attempt-receipt"); err == nil {
		t.Fatal("negative receipt count accepted")
	}
	if _, err := supervisorSegmentReceiptPlan([]domain.SupervisorToolRound{complete}, 2,
		supervisorSegmentReceiptTokenBudget, "attempt-receipt"); err == nil {
		t.Fatal("receipt count beyond the current rounds accepted")
	}
	if _, err := supervisorSegmentReceiptPlan([]domain.SupervisorToolRound{complete}, 1,
		0, "attempt-receipt"); err == nil {
		t.Fatal("zero receipt budget accepted")
	}
}

func TestSupervisorSegmentReceiptBoundsReceiptByBudget(t *testing.T) {
	longIdentity := strings.Repeat("i", 8000)
	oversized := segmentReceiptRound(t, 1, 1, func(int) string {
		return `{"protocol_version":"unsupported_shape","id":"` + longIdentity + `","next_offset":9}`
	})
	plan, err := supervisorSegmentReceiptPlan([]domain.SupervisorToolRound{oversized}, 1,
		supervisorSegmentReceiptTokenBudget, "attempt-receipt")
	if err != nil {
		t.Fatal(err)
	}
	if plan.ReceiptTokens > supervisorSegmentReceiptTokenBudget {
		t.Fatalf("receipt exceeded its budget: %d", plan.ReceiptTokens)
	}
	if strings.Contains(plan.ReceiptContent, longIdentity) ||
		!strings.Contains(plan.ReceiptContent, `"result_details_omitted":true`) {
		t.Fatal("an unbounded projection survived the receipt budget")
	}
	if measured := segmentReceiptMeasuredTokens(t, plan, "session-receipt", "attempt-receipt"); measured != plan.ReceiptTokens {
		t.Fatalf("measured %d tokens differ from the plan's %d", measured, plan.ReceiptTokens)
	}
	rich, err := supervisorSegmentReceiptPlan([]domain.SupervisorToolRound{oversized}, 1, 100000, "attempt-receipt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rich.ReceiptContent, longIdentity) ||
		!strings.Contains(rich.ReceiptContent, `"result_excerpted":true`) {
		t.Fatal("a generous budget did not keep the bounded projection")
	}
	rounds := make([]domain.SupervisorToolRound, 0, domain.MaxSupervisorToolRounds)
	for round := 1; round <= domain.MaxSupervisorToolRounds; round++ {
		rounds = append(rounds, segmentReceiptRound(t, round, domain.MaxSupervisorToolCallsPerRound,
			func(int) string {
				return `{"protocol_version":"unsupported_shape","body":"` + segmentReceiptFatBody + `"}`
			}))
	}
	if _, err := supervisorSegmentReceiptPlan(rounds, domain.MaxSupervisorToolRounds, 128, "attempt-receipt"); err == nil {
		t.Fatal("a receipt of every current round cannot honestly fit a tiny budget")
	}
}

func TestSupervisorSegmentReceiptRebuiltRequestFitsModelWindow(t *testing.T) {
	window := llm.ContextWindow{ProtocolVersion: llm.ContextWindowProtocolVersion, WindowTokens: 16384,
		SafetyMarginTokens: 128, DefaultOutputTokens: 256, MaxOutputTokens: 512, Source: "test"}
	base := llm.ChatRequest{Messages: []llm.Message{
		{Role: "system", Content: "policy"}, {Role: "user", Content: "current input"}}, JSONMode: true}
	rounds := []domain.SupervisorToolRound{
		segmentReceiptRound(t, 1, 1, func(int) string {
			return `{"protocol_version":"unsupported_shape","body":"` + segmentReceiptFatBody + `"}`
		}),
		segmentReceiptRound(t, 2, 1, func(int) string {
			return `{"protocol_version":"unsupported_shape","body":"` + segmentReceiptFatBody + `"}`
		}),
	}
	unbounded, err := supervisorRequestWithToolRounds(base, rounds)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := constrainRequestToModelWindow(unbounded, window, modelContextLayout{}); apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("native rounds did not reproduce the mandatory context overflow: %v", err)
	}
	for receiptCount := 1; receiptCount <= len(rounds); receiptCount++ {
		plan, err := supervisorSegmentReceiptPlan(rounds, receiptCount, supervisorSegmentReceiptTokenBudget, "attempt-receipt")
		if err != nil {
			t.Fatal(err)
		}
		rebuilt, err := supervisorRequestWithSegmentReceipt(base, plan, "session-receipt", "attempt-receipt")
		if err != nil {
			t.Fatal(err)
		}
		bounded, boundedPlan, err := constrainRequestToModelWindow(rebuilt, window, modelContextLayout{})
		if err != nil {
			if receiptCount == len(rounds) {
				t.Fatalf("fully receipted request still exceeded the window: %v", err)
			}
			continue
		}
		referenced := segmentReceiptReferencedCallIDs(bounded.Messages)
		for _, receipted := range plan.ReceiptedRounds {
			for _, call := range receipted.Calls {
				if referenced[call.CallID] {
					t.Fatalf("receipted call %s reappeared after window fitting", call.CallID)
				}
			}
		}
		if boundedPlan.HistoryOmitted != 0 {
			t.Fatal("receipting must not consume the history layout")
		}
		return
	}
	t.Fatal("unreachable receipt loop exit")
}

func TestSupervisorSegmentReceiptKeepsMixedHookAndNativeMessageOrder(t *testing.T) {
	for _, hookCount := range []int{1, 3} {
		t.Run(fmt.Sprintf("hooks_%d", hookCount), func(t *testing.T) {
			base := llm.ChatRequest{Messages: []llm.Message{{Role: "system", Content: "original instructions"}, {Role: "user", Content: "original current input"}}}
			rounds := []domain.SupervisorToolRound{
				segmentReceiptRound(t, 1, 1, func(int) string { return segmentReceiptFatBody }),
				segmentReceiptRound(t, 2, 2, func(int) string { return "native result" }),
			}
			for i := 0; i < hookCount; i++ {
				round := segmentReceiptRound(t, i+3, 1, func(int) string { return "host result" })
				call := &round.Calls[0]
				call.ToolName = "command_runtime"
				call.ProjectHook = &domain.ProjectHookCallOrigin{TriggerKey: strings.Repeat("a", 64), ConfigFingerprint: strings.Repeat("b", 64), WorkspaceVersion: strings.Repeat("c", 64), Slot: "format", Target: fmt.Sprintf("file%d.go", i)}
				raw, err := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{Version: supervisorToolResultVersion, Tool: call.ToolName, Status: "completed", Stdout: fmt.Sprintf("host result %d", i)})
				if err != nil {
					t.Fatal(err)
				}
				call.ResultJSON = string(raw)
				rounds = append(rounds, round)
			}
			plan, err := supervisorSegmentReceiptPlan(rounds, 1, supervisorSegmentReceiptTokenBudget, "attempt-receipt")
			if err != nil {
				t.Fatal(err)
			}
			retained, err := supervisorRequestWithToolRounds(base, plan.NativeRounds)
			if err != nil {
				t.Fatal(err)
			}
			rebuilt, err := supervisorRequestWithSegmentReceipt(base, plan, "session-receipt", "attempt-receipt")
			if err != nil {
				t.Fatal(err)
			}
			prefix := len(base.Messages)
			if !reflect.DeepEqual(rebuilt.Messages[:prefix], base.Messages) {
				t.Fatal("receipt displaced original input or instructions")
			}
			if !reflect.DeepEqual(rebuilt.Messages[prefix], supervisorSegmentReceiptMessage("session-receipt", "attempt-receipt", plan.ReceiptContent)) {
				t.Fatal("receipt not at original base boundary")
			}
			if !reflect.DeepEqual(rebuilt.Messages[prefix+1:], retained.Messages[prefix:]) {
				t.Fatal("retained tool identity, result pairing or hook order changed")
			}
			if referenced := segmentReceiptReferencedCallIDs(rebuilt.Messages); referenced["call-1-1"] || !referenced["call-2-1"] || !referenced["call-2-2"] {
				t.Fatalf("receipt/native pairing changed: %+v", referenced)
			}
			for _, round := range rounds[2:] {
				if segmentReceiptReferencedCallIDs(rebuilt.Messages)[round.Calls[0].CallID] {
					t.Fatal("host evidence became a native tool call")
				}
			}
		})
	}
}
