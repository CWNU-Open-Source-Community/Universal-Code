package application

import (
	"context"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/toolgateway"
)

type debugTerminalToolControllerStub struct {
	binding DebugTerminalAgentInputBinding
	found   bool
	reads   []ReadDebugTerminalAgentOutputRequest
	writes  []WriteDebugTerminalAgentInputRequest
}

func (s *debugTerminalToolControllerStub) Grant(context.Context,
	GrantDebugTerminalAgentInputRequest,
) (DebugTerminalAgentInputBinding, error) {
	return DebugTerminalAgentInputBinding{}, apperror.New(
		apperror.CodeFailedPrecondition, "not used")
}

func (s *debugTerminalToolControllerStub) Write(_ context.Context,
	request WriteDebugTerminalAgentInputRequest,
) (DebugTerminalAgentInputWriteResult, error) {
	s.writes = append(s.writes, request)
	bytesWritten := len(request.Data)
	if request.Interrupt {
		bytesWritten = 1
	}
	return DebugTerminalAgentInputWriteResult{
		ProtocolVersion:   DebugTerminalAgentInputProtocolVersion,
		BindingID:         request.BindingID,
		TerminalSessionID: s.binding.TerminalSessionID,
		BytesWritten:      bytesWritten,
		OutputCursor:      5,
	}, nil
}

func (s *debugTerminalToolControllerStub) Read(_ context.Context,
	request ReadDebugTerminalAgentOutputRequest,
) (DebugTerminalAgentOutputResult, error) {
	s.reads = append(s.reads, request)
	result := DebugTerminalAgentOutputResult{
		ProtocolVersion:   DebugTerminalAgentInputProtocolVersion,
		BindingID:         request.BindingID,
		TerminalSessionID: s.binding.TerminalSessionID,
		Backend:           "debug-terminal-test",
		BaseCursor:        2,
		NextCursor:        5,
		State:             "running",
	}
	result.NextCursor = 12
	result.Data = []byte("go1.25\r\n")
	return result, nil
}

func (s *debugTerminalToolControllerStub) Active(context.Context, string) (
	DebugTerminalAgentInputBinding, bool, error,
) {
	return s.binding, s.found, nil
}

func (s *debugTerminalToolControllerStub) Revoke(context.Context,
	RevokeDebugTerminalAgentInputRequest,
) error {
	return nil
}

func (s *debugTerminalToolControllerStub) Reconcile(context.Context) int { return 0 }
func (s *debugTerminalToolControllerStub) Shutdown(context.Context) int  { return 0 }

func TestDebugTerminalToolWritesExactCommandAndReadsFromTail(t *testing.T) {
	controller := &debugTerminalToolControllerStub{
		found: true,
		binding: DebugTerminalAgentInputBinding{
			ID: "debug-terminal-binding-1", RunID: "run-1",
			TerminalSessionID: "terminal-1",
		},
	}
	executor, err := NewDebugTerminalToolExecutor(controller)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.ExecuteDebugTerminal(t.Context(),
		toolgateway.DebugTerminalContext{
			RunID: "run-1", OperationKey: "debug-terminal-operation-0001",
		},
		toolgateway.DebugTerminalInput{
			Version: toolgateway.DebugTerminalProtocolVersion,
			Action:  toolgateway.DebugTerminalActionWrite,
			Command: "go version", MaxBytes: 4096,
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(controller.writes) != 1 ||
		string(controller.writes[0].Data) != "go version\r" ||
		controller.writes[0].OperationKey != "debug-terminal-operation-0001" {
		t.Fatalf("writes=%#v", controller.writes)
	}
	if len(controller.reads) != 1 ||
		controller.reads[0].Cursor != 5 ||
		string(result.Output) != "go1.25\r\n" || !result.InputSubmitted {
		t.Fatalf("reads=%#v result=%#v", controller.reads, result)
	}
}

func TestDebugTerminalToolRequiresOperatorBinding(t *testing.T) {
	controller := &debugTerminalToolControllerStub{}
	executor, err := NewDebugTerminalToolExecutor(controller)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.ExecuteDebugTerminal(t.Context(),
		toolgateway.DebugTerminalContext{RunID: "run-1"},
		toolgateway.DebugTerminalInput{
			Version:  toolgateway.DebugTerminalProtocolVersion,
			Action:   toolgateway.DebugTerminalActionRead,
			MaxBytes: 1024,
		})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("missing binding error=%v code=%s", err, apperror.CodeOf(err))
	}
}

func TestDebugTerminalToolInterruptUsesFixedInputAndReadsFromTail(t *testing.T) {
	controller := &debugTerminalToolControllerStub{
		found: true,
		binding: DebugTerminalAgentInputBinding{
			ID: "debug-terminal-binding-1", RunID: "run-1", TerminalSessionID: "terminal-1",
		},
	}
	executor, err := NewDebugTerminalToolExecutor(controller)
	if err != nil {
		t.Fatal(err)
	}
	input := toolgateway.DebugTerminalInput{
		Version: toolgateway.DebugTerminalProtocolVersion,
		Action:  toolgateway.DebugTerminalActionInterrupt, MaxBytes: 4096,
	}
	result, err := executor.ExecuteDebugTerminal(t.Context(), toolgateway.DebugTerminalContext{
		RunID: "run-1", OperationKey: "debug-terminal-interrupt-0001",
	}, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(controller.writes) != 1 || !controller.writes[0].Interrupt ||
		len(controller.writes[0].Data) != 0 || controller.writes[0].OperationKey != "debug-terminal-interrupt-0001" ||
		len(controller.reads) != 1 || controller.reads[0].Cursor != 5 ||
		!result.InputSubmitted || result.BytesWritten != 1 {
		t.Fatalf("writes=%+v reads=%+v result=%+v", controller.writes, controller.reads, result)
	}
	input.Command = "whoami"
	if _, err := executor.ExecuteDebugTerminal(t.Context(), toolgateway.DebugTerminalContext{RunID: "run-1"}, input); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("interrupt with a command accepted: %v", err)
	}
	if len(controller.writes) != 1 {
		t.Fatal("invalid interrupt reached controller")
	}
}

var _ DebugTerminalAgentInputController = (*debugTerminalToolControllerStub)(nil)
