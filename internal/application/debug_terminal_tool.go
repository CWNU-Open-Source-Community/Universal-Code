package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/toolgateway"
)

type DebugTerminalToolExecutor struct {
	controller DebugTerminalAgentInputController
}

func NewDebugTerminalToolExecutor(
	controller DebugTerminalAgentInputController,
) (*DebugTerminalToolExecutor, error) {
	if controller == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"debug terminal Agent-input controller is required")
	}
	return &DebugTerminalToolExecutor{controller: controller}, nil
}

func (e *DebugTerminalToolExecutor) ExecuteDebugTerminal(
	ctx context.Context,
	scope toolgateway.DebugTerminalContext,
	input toolgateway.DebugTerminalInput,
) (toolgateway.DebugTerminalExecutionResult, error) {
	if e == nil || e.controller == nil || ctx == nil {
		return toolgateway.DebugTerminalExecutionResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"debug terminal Agent-input runtime is unavailable")
	}
	if err := input.Validate(); err != nil {
		return toolgateway.DebugTerminalExecutionResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "debug terminal input is invalid", err)
	}
	binding, found, err := e.controller.Active(ctx, scope.RunID)
	if err != nil {
		return toolgateway.DebugTerminalExecutionResult{}, err
	}
	if !found {
		return toolgateway.DebugTerminalExecutionResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"operator must grant a short-lived Agent-input lease for the current Debug terminal")
	}
	cursor := input.Cursor
	inputSubmitted := false
	bytesWritten := 0
	replayed := false
	if input.Action == toolgateway.DebugTerminalActionWrite ||
		input.Action == toolgateway.DebugTerminalActionInterrupt {
		request := WriteDebugTerminalAgentInputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			BindingID:       binding.ID, OperationKey: scope.OperationKey,
			Interrupt: input.Action == toolgateway.DebugTerminalActionInterrupt,
		}
		if !request.Interrupt {
			request.Data = []byte(input.Command + "\r")
		}
		written, err := e.controller.Write(ctx, request)
		if err != nil {
			return toolgateway.DebugTerminalExecutionResult{}, err
		}
		inputSubmitted = true
		cursor = written.OutputCursor
		bytesWritten = written.BytesWritten
		replayed = written.Replayed
	}
	if err := waitForDebugTerminalOutput(ctx,
		time.Duration(input.WaitMilliseconds)*time.Millisecond); err != nil {
		return toolgateway.DebugTerminalExecutionResult{}, err
	}
	page, err := e.controller.Read(ctx,
		ReadDebugTerminalAgentOutputRequest{
			ProtocolVersion: DebugTerminalAgentInputProtocolVersion,
			BindingID:       binding.ID, Cursor: cursor, MaxBytes: input.MaxBytes,
		})
	if err != nil {
		return toolgateway.DebugTerminalExecutionResult{}, err
	}
	return toolgateway.DebugTerminalExecutionResult{
		BindingID: binding.ID, TerminalSessionID: page.TerminalSessionID,
		Backend: page.Backend, BaseCursor: page.BaseCursor,
		NextCursor: page.NextCursor, Output: append([]byte(nil), page.Data...),
		Dropped: page.Dropped, State: string(page.State),
		InputSubmitted: inputSubmitted, BytesWritten: bytesWritten,
		Replayed: replayed,
	}, nil
}

func waitForDebugTerminalOutput(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return apperror.Normalize(ctx.Err())
	case <-timer.C:
		return nil
	}
}

var _ toolgateway.DebugTerminalExecutor = (*DebugTerminalToolExecutor)(nil)
