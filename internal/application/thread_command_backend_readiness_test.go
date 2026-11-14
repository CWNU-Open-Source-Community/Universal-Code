package application

import (
	"context"
	"errors"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
)

type commandDefaultsUnreadySandbox struct {
	runner.CommandRuntimeSandboxExecutor
	err error
}

func (s commandDefaultsUnreadySandbox) Ready(context.Context, string) (bool, error) {
	return false, s.err
}

func TestDesktopCommandDefaultsRejectUnreadyBackendWithoutHostFallback(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, domain.RunExecutionProfileDocker, true)
	turns := NewThreadTurnService(f.st, nil, nil).WithDefaultCommandRuntime(f.mux)
	original := f.service.sandbox
	f.service.sandbox = commandDefaultsUnreadySandbox{CommandRuntimeSandboxExecutor: original}
	if _, _, err := turns.commandProfileForTurn(t.Context(), f.turn.Run); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("installed but unready selected backend did not fail before the model: %v", err)
	}
	readFailure := errors.New("controlled backend readiness read failure")
	f.service.sandbox = commandDefaultsUnreadySandbox{CommandRuntimeSandboxExecutor: original, err: readFailure}
	if _, _, err := turns.commandProfileForTurn(t.Context(), f.turn.Run); !errors.Is(err, readFailure) {
		t.Fatalf("backend read error was hidden or replaced: %v", err)
	}
	f.assertDispatchCounts(t, 0, 0)
}
