package runner

import (
	"context"
	"strings"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
)

// CommandRuntimeJobIdentity is a metadata-only, private binding to one owned
// Job. It carries no output, intent, executable path, PID, or process handle.
type CommandRuntimeJobIdentity struct {
	ID, OperationDigest, RequestFingerprint, InvocationID string
	RunID, MissionID, SessionID, WorkspaceID, RootAgentID string
	WorkspaceRootSHA256, SpecFingerprint, OwnerID         string
	OwnerGeneration                                       int64
	Adapter                                               commandruntimeadapter.Identity
}

func CommandRuntimeIdentity(job CommandRuntimeJob) CommandRuntimeJobIdentity {
	return CommandRuntimeJobIdentity{ID: job.ID, OperationDigest: job.OperationDigest,
		RequestFingerprint: job.RequestFingerprint, InvocationID: job.InvocationID,
		RunID: job.RunID, MissionID: job.MissionID, SessionID: job.SessionID,
		WorkspaceID: job.WorkspaceID, RootAgentID: job.RootAgentID,
		WorkspaceRootSHA256: job.WorkspaceRootSHA256, SpecFingerprint: job.SpecFingerprint,
		OwnerID: job.OwnerID, OwnerGeneration: job.OwnerGeneration, Adapter: job.Adapter}
}

type CommandRuntimeServiceMetadata struct {
	Identity               CommandRuntimeJobIdentity
	WorkspaceMode          CommandRuntimeWorkspaceMode
	TreeReaped             bool
	State                  CommandRuntimeJobState
	ExitCode               *int
	CreatedAt              time.Time
	StartedAt, CompletedAt *time.Time
}

// ReadOwnedCommandRuntimeServiceState is deliberately narrower than Get: the
// host must first authorize the complete durable tuple through its Thread.
func (m *CommandRuntimeManager) ReadOwnedCommandRuntimeServiceState(ctx context.Context,
	identity CommandRuntimeJobIdentity,
) (CommandRuntimeJobSnapshot, bool, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || identity.ID == "" {
		return CommandRuntimeJobSnapshot{}, false, ErrCommandRuntimeBoundary
	}
	entry := m.entry(identity.ID)
	if entry == nil {
		return CommandRuntimeJobSnapshot{}, false, nil
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if identity != CommandRuntimeIdentity(entry.record) || identity.OwnerID != m.ownerID ||
		identity.OwnerGeneration != m.ownerGeneration || !identity.Adapter.SameBackend(m.adapter) {
		return CommandRuntimeJobSnapshot{}, false, ErrCommandRuntimeBoundary
	}
	return ProjectCommandRuntimeJob(entry.record), true, nil
}

// The retained prefix belongs to the existing artifact-limit capture. This
// bounded read lets callers recover early address evidence after ring eviction,
// without adding another output buffer or any readiness state.
func (m *CommandRuntimeManager) ReadOwnedCommandRuntimeServicePrefix(ctx context.Context,
	identity CommandRuntimeJobIdentity, maxBytes int,
) (string, string, bool, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || identity.ID == "" || maxBytes < 1 || maxBytes > MaxCommandRuntimeOutputRead/2 {
		return "", "", false, ErrCommandRuntimeBoundary
	}
	entry := m.entry(identity.ID)
	if entry == nil {
		return "", "", false, nil
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if identity != CommandRuntimeIdentity(entry.record) || identity.OwnerID != m.ownerID || identity.OwnerGeneration != m.ownerGeneration || !identity.Adapter.SameBackend(m.adapter) {
		return "", "", false, ErrCommandRuntimeBoundary
	}
	prefix := func(value string) string {
		if len(value) > maxBytes {
			value = strings.ToValidUTF8(value[:maxBytes], "")
		}
		return value
	}
	return prefix(entry.record.Stdout), prefix(entry.record.Stderr), true, nil
}

// CancelOwnedCommandRuntimeServiceJob only cleans up an exact process-local
// owner. Repeated cancellation observes stopping rather than escalating to Kill.
// It never adopts a persisted PID or turns a cold record into process authority.
func (m *CommandRuntimeManager) CancelOwnedCommandRuntimeServiceJob(ctx context.Context,
	job CommandRuntimeJob,
) (CommandRuntimeJobSnapshot, bool, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || job.Validate() != nil {
		return CommandRuntimeJobSnapshot{}, false, ErrCommandRuntimeBoundary
	}
	entry := m.entry(job.ID)
	if entry == nil {
		if job.State.Terminal() && job.TreeReaped {
			return ProjectCommandRuntimeJob(job), true, nil
		}
		return ProjectCommandRuntimeJob(job), false, ErrCommandRuntimeUncertain
	}
	entry.mu.Lock()
	identity := CommandRuntimeIdentity(job)
	if identity != CommandRuntimeIdentity(entry.record) || identity.OwnerID != m.ownerID ||
		identity.OwnerGeneration != m.ownerGeneration || !identity.Adapter.SameBackend(m.adapter) {
		entry.mu.Unlock()
		return CommandRuntimeJobSnapshot{}, false, ErrCommandRuntimeBoundary
	}
	if entry.record.State.Terminal() && !entry.record.TreeReaped {
		result := ProjectCommandRuntimeJob(entry.record)
		entry.mu.Unlock()
		return result, false, ErrCommandRuntimeUncertain
	}
	if entry.record.State.Terminal() || entry.desired != "" {
		result := ProjectCommandRuntimeJob(entry.record)
		entry.mu.Unlock()
		return result, true, nil
	}
	entry.desired = CommandRuntimeJobCancelled
	entry.record.State = CommandRuntimeJobStopping
	result := ProjectCommandRuntimeJob(entry.record)
	entry.mu.Unlock()
	entry.signal()
	return result, false, entry.process.Cancel(0)
}
