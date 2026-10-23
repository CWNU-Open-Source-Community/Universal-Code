package toolgateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/tools"
)

type debugTerminalExecutorStub struct {
	calls int
	scope DebugTerminalContext
	input DebugTerminalInput
}

func (s *debugTerminalExecutorStub) ExecuteDebugTerminal(_ context.Context,
	scope DebugTerminalContext, input DebugTerminalInput,
) (DebugTerminalExecutionResult, error) {
	s.calls++
	s.scope = scope
	s.input = input
	bytesWritten := 0
	if input.Action == DebugTerminalActionWrite {
		bytesWritten = len([]byte(input.Command + "\r"))
	} else if input.Action == DebugTerminalActionInterrupt {
		bytesWritten = 1
	}
	return DebugTerminalExecutionResult{
		BindingID:         "terminal-input-binding-1",
		TerminalSessionID: "user-terminal-1", Backend: "windows-conpty-user-v1",
		BaseCursor: 4, NextCursor: 11,
		Output: []byte("\x1b[31mgo1.25\x1b[0m\r\n"), State: "running",
		InputSubmitted: input.Action != DebugTerminalActionRead,
		BytesWritten:   bytesWritten,
	}, nil
}

func TestDebugTerminalPayloadIsStrictAndCanonical(t *testing.T) {
	payload := json.RawMessage(`{"version":"debug_terminal.v1","action":"write","command":"go version","max_bytes":4096,"wait_milliseconds":250}`)
	input, canonical, err := normalizeDebugTerminalPayload(payload)
	if err != nil || input.Command != "go version" || !json.Valid(canonical) {
		t.Fatalf("valid Debug terminal payload failed: %s err=%v", canonical, err)
	}
	interrupt := json.RawMessage(`{"version":"debug_terminal.v1","action":"interrupt","max_bytes":4096,"wait_milliseconds":0}`)
	input, canonical, err = normalizeDebugTerminalPayload(interrupt)
	if err != nil || input.Action != DebugTerminalActionInterrupt || string(canonical) != string(interrupt) {
		t.Fatalf("valid interrupt payload failed: %s err=%v", canonical, err)
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"version":"debug_terminal.v1","action":"interrupt","command":"whoami","max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"interrupt","command":null,"max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"interrupt","cursor":0,"max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"interrupt","data":"\u0003","max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"write","command":"\u0003","max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"write","command":"go version\nwhoami","max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"write","command":"go version","cursor":0,"max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"read","command":"whoami","max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"read","command":null,"max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"read","cursor":null,"max_bytes":4096,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"read","max_bytes":4096}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"read","max_bytes":65537,"wait_milliseconds":0}`),
		json.RawMessage(`{"version":"debug_terminal.v1","action":"write","command":"go version","max_bytes":4096,"wait_milliseconds":0,"background":true}`),
	} {
		if _, _, err := normalizeDebugTerminalPayload(invalid); err == nil {
			t.Fatalf("invalid Debug terminal payload was accepted: %s", invalid)
		}
	}
}

func TestDebugTerminalGatewayUsesFencedLeaseAndSanitizesOutput(t *testing.T) {
	state := newTrackedStructuredStore()
	executor := &debugTerminalExecutorStub{}
	gateway := New(state, policy.NewDefaultChecker()).
		WithDebugTerminalExecutor(executor)
	outcome, err := gateway.Invoke(t.Context(), ToolCall{
		Name:         DebugTerminalTool,
		Payload:      json.RawMessage(`{"version":"debug_terminal.v1","action":"write","command":"go version","max_bytes":4096,"wait_milliseconds":250}`),
		OperationKey: "1234567890abcdef1234567890abcdef",
		RunID:        "run-1", AgentID: "agent-20260818123456-abcdef012345",
		SessionID: "session-1", WorkspaceID: "workspace-1",
		LeaseID: "run-lease-1", LeaseGeneration: 1,
		RequestedBy: "run_supervisor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || executor.scope.LeaseID != "run-lease-1" ||
		executor.input.Command != "go version" || outcome.Result == nil ||
		outcome.Result.Stdout != "go1.25\r\n" ||
		strings.Contains(outcome.Result.Stdout, "\x1b") ||
		outcome.Result.Metadata["output_complete"] != "false" ||
		outcome.Result.Metadata["output_source"] != "untrusted_debug_terminal" ||
		outcome.Result.Metadata["instruction_authorized"] != "false" ||
		outcome.Result.Metadata["lease_token_exposed"] != "false" {
		t.Fatalf("unexpected Debug terminal outcome: %#v", outcome)
	}
}

func TestSanitizeDebugTerminalOutputRemovesTerminalAndUnicodeControls(t *testing.T) {
	value := sanitizeDebugTerminalOutput([]byte(
		"safe\x1b]52;c;clipboard-payload\x07-visible\x1bPdevice-control\x1b\\" +
			"\u202esecret-direction\u009b31mred\u009dhidden-title\u009c\n"))
	if strings.Contains(value, "clipboard-payload") ||
		strings.Contains(value, "device-control") ||
		strings.Contains(value, "hidden-title") ||
		strings.ContainsRune(value, '\u202e') ||
		strings.ContainsRune(value, '\u009b') ||
		value != "safe-visiblesecret-directionred\n" {
		t.Fatalf("terminal controls were not safely removed: %q", value)
	}
}

func TestDebugTerminalGatewayDeniesDangerousAndApprovalCommands(t *testing.T) {
	state := newTrackedStructuredStore()
	executor := &debugTerminalExecutorStub{}
	gateway := New(state, policy.NewDefaultChecker()).
		WithDebugTerminalExecutor(executor)
	for _, command := range []string{"rm -rf /", "nmap 127.0.0.1"} {
		payload, err := json.Marshal(DebugTerminalInput{
			Version: DebugTerminalProtocolVersion, Action: DebugTerminalActionWrite,
			Command: command, MaxBytes: 4096,
		})
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := gateway.Invoke(t.Context(), ToolCall{
			Name: DebugTerminalTool, Payload: payload,
			OperationKey: "abcdef1234567890abcdef1234567890",
			RunID:        "run-1", AgentID: "agent-20260818123456-abcdef012345",
			SessionID: "session-1", WorkspaceID: "workspace-1",
			LeaseID: "run-lease-1", LeaseGeneration: 1,
			RequestedBy: "run_supervisor",
		})
		if err != nil || outcome.Decision.Allowed || outcome.Result == nil ||
			outcome.Result.Status != StatusDenied {
			t.Fatalf("command %q was not denied: %#v err=%v", command, outcome, err)
		}
	}
	if executor.calls != 0 {
		t.Fatalf("denied Debug terminal commands reached executor %d times", executor.calls)
	}
}

type debugTerminalInterruptChecker struct {
	policy.Checker
	decision policy.Decision
}

func (p debugTerminalInterruptChecker) CheckToolCall(call tools.Call) policy.Decision {
	if call.Name == string(DebugTerminalTool) && call.Args["action"] == DebugTerminalActionInterrupt {
		return p.decision
	}
	return p.Checker.CheckToolCall(call)
}

func TestDebugTerminalGatewayInterruptHonorsPolicy(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "review"} {
		t.Run(scenario, func(t *testing.T) {
			executor := &debugTerminalExecutorStub{}
			gateway := New(newTrackedStructuredStore(), debugTerminalInterruptChecker{
				Checker: policy.NewDefaultChecker(), decision: policy.Decision{
					Allowed: scenario != "deny", NeedsApproval: scenario == "review", Reason: "interrupt host rule",
				},
			}).WithDebugTerminalExecutor(executor)
			outcome, err := gateway.Invoke(t.Context(), ToolCall{
				Name:         DebugTerminalTool,
				Payload:      json.RawMessage(`{"version":"debug_terminal.v1","action":"interrupt","max_bytes":4096,"wait_milliseconds":0}`),
				OperationKey: "debug-terminal-interrupt", RunID: "run-1", AgentID: "agent-20260818123456-abcdef012345",
				SessionID: "session-1", WorkspaceID: "workspace-1", LeaseID: "run-lease-1", LeaseGeneration: 1,
				RequestedBy: "run_supervisor",
			})
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "allow" {
				if executor.calls != 0 || outcome.Decision.Allowed || outcome.Result.Status != StatusDenied {
					t.Fatalf("denied interrupt reached executor: %+v", outcome)
				}
				return
			}
			if executor.calls != 1 || executor.input.Action != DebugTerminalActionInterrupt ||
				executor.scope.LeaseID != "run-lease-1" || outcome.Result.Metadata["bytes_written"] != "1" ||
				outcome.Result.Metadata["command_persisted"] != "false" {
				t.Fatalf("unexpected interrupt outcome: %+v", outcome)
			}
		})
	}
}
