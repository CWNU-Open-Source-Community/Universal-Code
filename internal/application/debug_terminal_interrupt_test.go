package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/policy"
	terminalruntime "cyberagent-workbench/internal/terminal"
	"cyberagent-workbench/internal/tools"
)

func grantDebugTerminalInterruptFixture(t *testing.T, f debugTerminalAgentFixture) DebugTerminalAgentInputBinding {
	t.Helper()
	binding, err := f.service.Grant(t.Context(), GrantDebugTerminalAgentInputRequest{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		RunID:           f.run.ID, TerminalSessionID: f.sessionID, RequestedBy: "test_operator",
		ConfirmFullAccess: true, ConfirmAgentTerminalInput: true, TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestDebugTerminalInterruptIsFixedAuditedAndExactlyOnce(t *testing.T) {
	f := newDebugTerminalAgentFixture(t, false)
	binding := grantDebugTerminalInterruptFixture(t, f)
	request := WriteDebugTerminalAgentInputRequest{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
		BindingID:       binding.ID, OperationKey: "debug-interrupt-once", Interrupt: true,
	}
	result, err := f.service.Write(t.Context(), request)
	if err != nil || result.BytesWritten != 1 || result.DataBytes != 1 || result.Replayed ||
		result.DataSHA256 != terminalruntime.AgentInputDataDigest([]byte{0x03}) {
		t.Fatalf("interrupt=%+v err=%v", result, err)
	}
	replayed, err := f.service.Write(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.OperationDigest != result.OperationDigest || replayed.OutputCursor != result.OutputCursor {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	conflict := request
	conflict.Interrupt, conflict.Data = false, []byte("go version\r")
	if _, err := f.service.Write(t.Context(), conflict); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("command reused interrupt operation key: %v", err)
	}
	for _, data := range [][]byte{{0x03}, {0x03, '\r'}, {0x04}, []byte("whoami\r")} {
		invalid := request
		invalid.Data = data
		if _, err := f.service.Write(t.Context(), invalid); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
			t.Fatalf("interrupt accepted custom bytes %q: %v", data, err)
		}
	}
	input, writes := f.process.snapshot()
	if input != "\x03" || writes != 1 {
		t.Fatalf("input=%q writes=%d", input, writes)
	}
	records, err := f.state.ListRunEvents(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, record := range records {
		if record.Type != events.DebugTerminalAgentInputPreparedEvent && record.Type != events.DebugTerminalAgentInputCompletedEvent {
			continue
		}
		counts[record.Type]++
		var payload struct {
			DataSHA256 string `json:"data_sha256"`
			DataBytes  int    `json:"data_bytes"`
		}
		if err := json.Unmarshal([]byte(record.PayloadJSON), &payload); err != nil || payload.DataBytes != 1 || payload.DataSHA256 != result.DataSHA256 {
			t.Fatalf("unexpected interrupt audit: %s err=%v", record.PayloadJSON, err)
		}
	}
	if counts[events.DebugTerminalAgentInputPreparedEvent] != 1 || counts[events.DebugTerminalAgentInputCompletedEvent] != 1 {
		t.Fatalf("audit counts=%v", counts)
	}
}

type debugTerminalInterruptPolicy struct {
	policy.Checker
	decision policy.Decision
}

func (p debugTerminalInterruptPolicy) CheckToolCall(call tools.Call) policy.Decision {
	if call.Name == "debug_terminal" && call.Args["action"] == "interrupt" {
		return p.decision
	}
	return p.Checker.CheckToolCall(call)
}

// Revoke after application revalidation, at the entry to the native bridge.
type debugTerminalInterruptRevocationBridge struct {
	DebugTerminalAgentInputBridge
	revoke func()
}

func (b debugTerminalInterruptRevocationBridge) Write(ctx context.Context, request terminalruntime.AgentWriteRequest) (terminalruntime.AgentWriteResult, error) {
	b.revoke()
	return b.DebugTerminalAgentInputBridge.Write(ctx, request)
}

func TestDebugTerminalInterruptRequiresCurrentAuthority(t *testing.T) {
	for _, scenario := range []string{"missing", "expired", "revoked", "permission", "workspace", "policy_denied", "policy_review", "native_revocation"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDebugTerminalAgentFixture(t, false)
			binding := grantDebugTerminalInterruptFixture(t, f)
			switch scenario {
			case "missing":
				binding.ID = "missing-terminal-binding"
			case "expired":
				f.service.now = func() time.Time { return binding.ExpiresAt.Add(time.Second) }
			case "revoked":
				if err := f.service.Revoke(t.Context(), RevokeDebugTerminalAgentInputRequest{
					ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID,
					RequestedBy: "test_operator", OperatorConfirmed: true,
				}); err != nil {
					t.Fatal(err)
				}
			case "permission":
				if _, err := f.permission.Change(t.Context(), ChangeRunExecutionPermissionRequest{
					RunID: f.run.ID, Mode: "ask", OperationKey: "interrupt-permission-change",
					RequestedBy: "test_operator", Reason: "end terminal input",
				}); err != nil {
					t.Fatal(err)
				}
			case "workspace":
				workspace, err := f.state.GetWorkspaceByID(t.Context(), binding.WorkspaceID)
				if err != nil {
					t.Fatal(err)
				}
				workspace.RootPath = t.TempDir()
				if err := f.state.SaveWorkspace(t.Context(), workspace); err != nil {
					t.Fatal(err)
				}
			case "policy_denied", "policy_review":
				f.service.checker = debugTerminalInterruptPolicy{Checker: policy.NewDefaultChecker(), decision: policy.Decision{
					Allowed: scenario == "policy_review", NeedsApproval: scenario == "policy_review", Reason: "interrupt host rule",
				}}
			case "native_revocation":
				f.service.bridge = debugTerminalInterruptRevocationBridge{DebugTerminalAgentInputBridge: f.service.bridge,
					revoke: func() { f.service.capabilities.RuntimeAuthority.RevokeRun(f.run.ID) }}
			}
			if _, err := f.service.Write(t.Context(), WriteDebugTerminalAgentInputRequest{
				ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID,
				OperationKey: "interrupt-without-authority", Interrupt: true,
			}); err == nil {
				t.Fatal("interrupt accepted without current authority")
			}
			if input, writes := f.process.snapshot(); input != "" || writes != 0 {
				t.Fatalf("unauthorized native input=%q writes=%d", input, writes)
			}
		})
	}
}

func TestDebugTerminalInterruptDoesNotRetryAmbiguousWrite(t *testing.T) {
	f := newDebugTerminalAgentFixture(t, true)
	binding := grantDebugTerminalInterruptFixture(t, f)
	request := WriteDebugTerminalAgentInputRequest{
		ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID,
		OperationKey: "interrupt-ambiguous", Interrupt: true,
	}
	for range 2 {
		if _, err := f.service.Write(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
			t.Fatalf("ambiguous interrupt error=%v", err)
		}
	}
	if _, writes := f.process.snapshot(); writes != 1 {
		t.Fatalf("ambiguous interrupt retried %d times", writes)
	}
}
