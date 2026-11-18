package domain

import (
	"fmt"
	"testing"
	"time"
)

func TestSupervisorToolRoundValidationTracksTerminalCalls(t *testing.T) {
	now := time.Now().UTC()
	pending := SupervisorToolCall{
		RunID: "run-1", Turn: 1, AttemptID: "attempt-1", Round: 1, Position: 1, ModelAttempt: 1,
		CallID: "toolu_0123456789abcdef01234567", ToolName: "work_item_create",
		PayloadJSON: `{"title":"Plan"}`, Status: SupervisorToolPending, CreatedAt: now,
	}
	round := SupervisorToolRound{
		RunID: "run-1", Turn: 1, AttemptID: "attempt-1", Round: 1, ModelAttempt: 1,
		Calls: []SupervisorToolCall{pending}, CreatedAt: now,
	}
	if err := round.Validate(); err != nil || round.Complete() {
		t.Fatalf("pending supervisor tool round is invalid: %#v err=%v", round, err)
	}
	completed := now.Add(time.Second)
	round.Calls[0].Status = SupervisorToolCompleted
	round.Calls[0].ResultJSON = `{"status":"completed"}`
	round.Calls[0].CompletedAt = &completed
	round.CompletedAt = &completed
	if err := round.Validate(); err != nil || !round.Complete() {
		t.Fatalf("completed supervisor tool round is invalid: %#v err=%v", round, err)
	}
	round.Calls[0].ErrorCode = "unexpected"
	if err := round.Validate(); err == nil {
		t.Fatal("completed supervisor tool call accepted an error code")
	}
}

func TestSupervisorToolResultRejectsUnboundedOrPendingState(t *testing.T) {
	now := time.Now().UTC()
	valid := SupervisorToolResult{
		CallID: "toolu_0123456789abcdef01234567", Status: SupervisorToolDenied,
		ResultJSON: `{"status":"denied"}`, ErrorCode: "POLICY_DENIED", CompletedAt: now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.Status = SupervisorToolPending
	if err := valid.Validate(); err == nil {
		t.Fatal("pending supervisor tool result was accepted")
	}
}

func TestSupervisorToolCallRejectsOperatorRootAttribution(t *testing.T) {
	call := SupervisorToolCall{
		RunID: "run-operator", AgentID: "agent-root-operator",
		AgentAttribution: AgentAttributionOperatorRoot,
		Turn:             1, AttemptID: "attempt-supervisor", Round: 1, Position: 1,
		ModelAttempt: 1, CallID: "toolu_operator_masquerade",
		ToolName: "note_create", PayloadJSON: `{}`,
		Status: SupervisorToolPending, CreatedAt: time.Now().UTC(),
	}
	if err := call.Validate(); err == nil {
		t.Fatal("Supervisor tool call accepted operator-root attribution")
	}
}

func TestSupervisorToolCallAcceptsEveryDurableToolName(t *testing.T) {
	now := time.Now().UTC()
	for _, toolName := range []string{
		"work_item_create",
		"note_create",
		"specialist_delegation_propose",
		"plan_delivery_propose",
		"controlled_command_propose",
		"host_command_propose",
		"debug_terminal",
		"command_runtime",
		"mcp_tool_call",
		"web_search",
		"source_search",
		"web_fetch",
		"web_citation",
		"workspace_list",
		"workspace_read",
		"workspace_glob",
		"workspace_grep",
		"workspace_change",
		"workspace_apply",
		"workspace_delete",
		"github_review_evidence_list",
		"github_review_evidence_read",
		"browser_status",
		"browser_navigate",
		"browser_snapshot",
		"browser_click",
		"browser_type",
		"browser_screenshot",
		"browser_move",
	} {
		t.Run(toolName, func(t *testing.T) {
			call := SupervisorToolCall{
				RunID: "run-1", Turn: 1, AttemptID: "attempt-1", Round: 1,
				Position: 1, ModelAttempt: 1, CallID: "toolu_0123456789abcdef01234567",
				ToolName: toolName, PayloadJSON: `{}`, Status: SupervisorToolPending,
				CreatedAt: now,
			}
			if isAgentCodeSupervisorTool(toolName) || toolName == "command_runtime" ||
				toolName == "mcp_tool_call" ||
				isWebEvidenceSupervisorTool(toolName) || isBrowserActionSupervisorTool(toolName) {
				call.AuthorityJSON = `{}`
			}
			if err := call.Validate(); err != nil {
				t.Fatalf("durable tool %q was rejected: %v", toolName, err)
			}
			if isWebEvidenceSupervisorTool(toolName) {
				call.AuthorityJSON = ""
				if err := call.Validate(); err == nil {
					t.Fatalf("web tool %q without durable authority was accepted", toolName)
				}
			}
		})
	}
}

func TestSupervisorBrowserActionCallRequiresAuthority(t *testing.T) {
	call := SupervisorToolCall{
		RunID: "run-browser", Turn: 1, AttemptID: "attempt-browser", Round: 1,
		Position: 1, ModelAttempt: 1, CallID: "toolu_browser_status",
		ToolName: "browser_status", PayloadJSON: `{"version":"browser_status.v1"}`,
		Status: SupervisorToolPending, CreatedAt: time.Now().UTC(),
	}
	if err := call.Validate(); err == nil {
		t.Fatal("browser action without durable authority was accepted")
	}
	call.AuthorityJSON = `{}`
	if err := call.Validate(); err != nil {
		t.Fatalf("authority-bound browser action was rejected: %v", err)
	}
}

func TestSupervisorMCPCallRequiresAuthority(t *testing.T) {
	call := SupervisorToolCall{
		RunID: "run-mcp", Turn: 1, AttemptID: "attempt-mcp", Round: 1,
		Position: 1, ModelAttempt: 1, CallID: "toolu_mcp_call",
		ToolName: "mcp_tool_call", PayloadJSON: `{"version":"mcp-client.v1"}`,
		Status: SupervisorToolPending, CreatedAt: time.Now().UTC(),
	}
	if err := call.Validate(); err == nil {
		t.Fatal("MCP call without durable authority was accepted")
	}
	call.AuthorityJSON = `{}`
	if err := call.Validate(); err != nil {
		t.Fatalf("authority-bound MCP call was rejected: %v", err)
	}
}

func TestSupervisorGitHubEvidenceCallsAreDurableAndAuthorityBound(t *testing.T) {
	for _, toolName := range []string{"github_review_evidence_list", "github_review_evidence_read"} {
		t.Run(toolName, func(t *testing.T) {
			call := SupervisorToolCall{
				RunID: "run-github-evidence", Turn: 1, AttemptID: "attempt-github-evidence",
				Round: 1, Position: 1, ModelAttempt: 1,
				CallID: "toolu_github_evidence", ToolName: toolName,
				PayloadJSON: `{}`, Status: SupervisorToolPending, CreatedAt: time.Now().UTC(),
			}
			if err := call.Validate(); err == nil {
				t.Fatal("GitHub evidence call without durable authority was accepted")
			}
			call.AuthorityJSON = `{}`
			if err := call.Validate(); err != nil {
				t.Fatalf("authority-bound GitHub evidence call was rejected: %v", err)
			}
		})
	}
}

func TestSupervisorBrowserMoveRequiresDurableAuthority(t *testing.T) {
	call := SupervisorToolCall{RunID: "run-browser", Turn: 1, AttemptID: "attempt-browser", Round: 1, Position: 1, ModelAttempt: 1, CallID: "move", ToolName: "browser_move", PayloadJSON: `{"version":"browser_move.v2","point":{"x":40,"y":30,"document_epoch":1,"viewport":{"width":800,"height":600}}}`, Status: SupervisorToolPending, CreatedAt: time.Now().UTC()}
	if err := call.Validate(); err == nil {
		t.Fatal("browser move without durable authority was accepted")
	}
	call.AuthorityJSON = `{}`
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorToolThroughputBoundsAndConstants(t *testing.T) {
	if MaxSupervisorToolCallsPerRound != 8 {
		t.Fatalf("MaxSupervisorToolCallsPerRound = %d, want 8", MaxSupervisorToolCallsPerRound)
	}
	if MaxSupervisorToolRounds != 24 {
		t.Fatalf("MaxSupervisorToolRounds = %d, want 24", MaxSupervisorToolRounds)
	}
	if MaxSupervisorExecutionRounds != 217 {
		t.Fatalf("MaxSupervisorExecutionRounds = %d, want 217", MaxSupervisorExecutionRounds)
	}
	if MaxSupervisorBoundaryContextCalls != 256 {
		t.Fatalf("MaxSupervisorBoundaryContextCalls = %d, want 256", MaxSupervisorBoundaryContextCalls)
	}

	now := time.Now().UTC()
	calls := make([]SupervisorToolCall, 0, MaxSupervisorToolCallsPerRound)
	for pos := 1; pos <= MaxSupervisorToolCallsPerRound; pos++ {
		call := SupervisorToolCall{
			RunID: "run-throughput", Turn: 1, AttemptID: "attempt-1", Round: MaxSupervisorExecutionRounds,
			Position: pos, ModelAttempt: 1, CallID: fmt.Sprintf("toolu_pos_%d", pos),
			ToolName: "note_create", PayloadJSON: `{}`, Status: SupervisorToolPending, CreatedAt: now,
		}
		if err := call.Validate(); err != nil {
			t.Fatalf("position %d at round %d rejected: %v", pos, MaxSupervisorExecutionRounds, err)
		}
		calls = append(calls, call)
	}
	round := SupervisorToolRound{
		RunID: "run-throughput", Turn: 1, AttemptID: "attempt-1", Round: MaxSupervisorExecutionRounds,
		ModelAttempt: 1, Calls: calls, CreatedAt: now,
	}
	if err := round.Validate(); err != nil {
		t.Fatalf("8-call round at max execution round rejected: %v", err)
	}

	tooHighPos := calls[0]
	tooHighPos.Position = MaxSupervisorToolCallsPerRound + 1
	if err := tooHighPos.Validate(); err == nil {
		t.Fatalf("position %d unexpectedly accepted", tooHighPos.Position)
	}

	tooManyCalls := append(append([]SupervisorToolCall(nil), calls...), SupervisorToolCall{
		RunID: "run-throughput", Turn: 1, AttemptID: "attempt-1", Round: MaxSupervisorExecutionRounds,
		Position: MaxSupervisorToolCallsPerRound + 1, ModelAttempt: 1, CallID: "toolu_pos_9",
		ToolName: "note_create", PayloadJSON: `{}`, Status: SupervisorToolPending, CreatedAt: now,
	})
	tooLargeRound := round
	tooLargeRound.Calls = tooManyCalls
	if err := tooLargeRound.Validate(); err == nil {
		t.Fatal("9-call round unexpectedly accepted")
	}

	tooHighRound := calls[0]
	tooHighRound.Round = MaxSupervisorExecutionRounds + 1
	if err := tooHighRound.Validate(); err == nil {
		t.Fatalf("round %d unexpectedly accepted", tooHighRound.Round)
	}
}

