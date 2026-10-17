package application

import (
	"context"
	"errors"
	"sort"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
)

// CommandRuntimeRuntime is the complete process-owned service used by API,
// Desktop, CLI, and UI-evidence composition roots. It exposes no method that
// lets a caller choose an adapter; selection remains a consequence of current
// Run profile, permission, and installed readiness.
type CommandRuntimeRuntime interface {
	AgentBrowserServiceReadiness
	ThreadApplicationServiceCommandRuntime
	toolgateway.CommandRuntimeExecutor
	toolgateway.CommandRuntimeAdvertiser
	InstalledCommandRuntimeAdapters() []commandruntimeadapter.Identity
	Reconcile(context.Context) (int, error)
	RunReconciler(context.Context, time.Duration) error
	Shutdown(context.Context) error
	cleanupUIEvidenceJob(context.Context, uiEvidenceCommandCleanupBinding) (
		runner.CommandRuntimeJobSnapshot, error)
}

// CommandRuntimeMultiplexer keeps command-runtime.v2 adapter-neutral while
// routing an already Go-bound authority snapshot to exactly one installed
// implementation. Duplicate or ambiguous installation fails closed.
type CommandRuntimeMultiplexer struct {
	adapters []*CommandRuntimeService
}

func NewCommandRuntimeMultiplexer(adapters ...*CommandRuntimeService) (
	*CommandRuntimeMultiplexer, error,
) {
	if len(adapters) == 0 {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"at least one Command Runtime adapter is required")
	}
	seen := make(map[string]struct{}, len(adapters))
	value := &CommandRuntimeMultiplexer{adapters: make([]*CommandRuntimeService, 0,
		len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil || adapter.manager == nil || !adapter.manager.Available() ||
			!adapter.adapter.Executable() {
			return nil, apperror.New(apperror.CodeFailedPrecondition,
				"Command Runtime adapter is unavailable")
		}
		backendKey := string(adapter.adapter.Kind) + "\x00" + adapter.adapter.Backend +
			"\x00" + adapter.adapter.BackendIdentity
		if _, duplicate := seen[backendKey]; duplicate {
			return nil, apperror.New(apperror.CodeConflict,
				"Command Runtime adapter identity is duplicated")
		}
		seen[backendKey] = struct{}{}
		value.adapters = append(value.adapters, adapter)
	}
	sort.Slice(value.adapters, func(i, j int) bool {
		left, right := value.adapters[i].adapter, value.adapters[j].adapter
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Backend < right.Backend
	})
	return value, nil
}

func (m *CommandRuntimeMultiplexer) InstalledCommandRuntimeAdapters() []commandruntimeadapter.Identity {
	if m == nil {
		return []commandruntimeadapter.Identity{}
	}
	result := make([]commandruntimeadapter.Identity, 0, len(m.adapters))
	for _, adapter := range m.adapters {
		if identity, installed := adapter.InstalledCommandRuntimeAdapter(); installed {
			result = append(result, identity)
		}
	}
	return result
}

func (s *CommandRuntimeService) InstalledCommandRuntimeAdapters() []commandruntimeadapter.Identity {
	if identity, installed := s.InstalledCommandRuntimeAdapter(); installed {
		return []commandruntimeadapter.Identity{identity}
	}
	return []commandruntimeadapter.Identity{}
}

func (m *CommandRuntimeMultiplexer) AdvertisedCommandRuntimeAdapter(ctx context.Context,
	runID string, permission domain.RunExecutionPermissionMode,
) (commandruntimeadapter.Identity, bool, error) {
	if m == nil {
		return commandruntimeadapter.Identity{}, false, nil
	}
	var selected commandruntimeadapter.Identity
	found := false
	for _, adapter := range m.adapters {
		identity, available, err := adapter.AdvertisedCommandRuntimeAdapter(ctx,
			runID, permission)
		if err != nil {
			return commandruntimeadapter.Identity{}, false, err
		}
		if !available {
			continue
		}
		if found {
			return commandruntimeadapter.Identity{}, false, apperror.New(
				apperror.CodeConflict, "Command Runtime adapter selection is ambiguous")
		}
		selected, found = identity, true
	}
	return selected, found, nil
}

func (m *CommandRuntimeMultiplexer) ExecuteCommandRuntime(ctx context.Context,
	scope toolgateway.CommandRuntimeContext, input toolgateway.CommandRuntimeInput,
) (toolgateway.CommandRuntimeExecutionResult, error) {
	if m == nil || !scope.Adapter.Executable() {
		return toolgateway.CommandRuntimeExecutionResult{}, apperror.New(
			apperror.CodePolicyDenied, "Command Runtime adapter authority is unavailable")
	}
	for _, adapter := range m.adapters {
		if scope.Adapter.SameBackend(adapter.adapter) {
			return adapter.ExecuteCommandRuntime(ctx, scope, input)
		}
	}
	return toolgateway.CommandRuntimeExecutionResult{}, apperror.New(
		apperror.CodePolicyDenied, "Command Runtime adapter authority is stale")
}

func (m *CommandRuntimeMultiplexer) Reconcile(ctx context.Context) (int, error) {
	if m == nil {
		return 0, apperror.New(apperror.CodeFailedPrecondition,
			"Command Runtime adapters are unavailable")
	}
	total := 0
	var result error
	for _, adapter := range m.adapters {
		count, err := adapter.Reconcile(ctx)
		total += count
		result = errors.Join(result, err)
	}
	return total, result
}

func (m *CommandRuntimeMultiplexer) RunReconciler(ctx context.Context,
	interval time.Duration,
) error {
	if m == nil || ctx == nil || interval < 100*time.Millisecond ||
		interval > time.Minute {
		return apperror.New(apperror.CodeInvalidArgument,
			"Command Runtime reconciliation interval is invalid")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := m.Reconcile(ctx); err != nil && ctx.Err() == nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(),
				runner.MaxCommandRuntimeCancelGrace+time.Second)
			_ = m.Shutdown(shutdownCtx)
			cancel()
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (m *CommandRuntimeMultiplexer) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	var result error
	for _, adapter := range m.adapters {
		result = errors.Join(result, adapter.Shutdown(ctx))
	}
	return result
}

func (m *CommandRuntimeMultiplexer) cleanupUIEvidenceJob(ctx context.Context,
	binding uiEvidenceCommandCleanupBinding,
) (runner.CommandRuntimeJobSnapshot, error) {
	if m == nil || len(m.adapters) == 0 {
		return runner.CommandRuntimeJobSnapshot{}, apperror.New(
			apperror.CodeFailedPrecondition, "Command Runtime adapters are unavailable")
	}
	record, err := m.adapters[0].store.GetCommandRuntimeJob(ctx, binding.JobID)
	if err != nil {
		return runner.CommandRuntimeJobSnapshot{}, commandRuntimeError(err)
	}
	for _, adapter := range m.adapters {
		if record.Adapter.SameBackend(adapter.adapter) {
			return adapter.cleanupUIEvidenceJob(ctx, binding)
		}
	}
	return runner.ProjectCommandRuntimeJob(record), apperror.New(
		apperror.CodeConflict, "UI evidence Command Runtime adapter is stale")
}

var _ CommandRuntimeRuntime = (*CommandRuntimeService)(nil)
var _ CommandRuntimeRuntime = (*CommandRuntimeMultiplexer)(nil)
