package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
)

// ExecutionPermissionRuntimeGrant is a process-local, non-persistent binding
// between an explicitly activated Thread/Run permission snapshot and the
// runtime generation that may consume it. Durable permission preferences never
// recreate one of these grants after process restart.
type ExecutionPermissionRuntimeGrant struct {
	Generation                 uint64
	ThreadID                   string
	ThreadPermissionSnapshotID string
	ThreadPermissionRevision   int64
	RunID                      string
	PermissionSnapshotID       string
	PermissionRevision         int64
}

type executionPermissionThreadGrant struct {
	Generation       uint64
	SnapshotID       string
	SnapshotRevision int64
}

// ExecutionPermissionRuntimeAuthority is the single mutable authority for
// dynamic Full Access. It is deliberately process-local and starts empty. All
// reads are concurrent-safe; every mutation advances a monotonic generation so
// previously issued browser/command authority can be fenced immediately.
type ExecutionPermissionRuntimeAuthority struct {
	mu         sync.RWMutex
	epoch      string
	generation uint64
	threads    map[string]executionPermissionThreadGrant
	runs       map[string]ExecutionPermissionRuntimeGrant
	runFences  map[string]uint64
	runSignals map[string]chan struct{}
}

func NewExecutionPermissionRuntimeAuthority() *ExecutionPermissionRuntimeAuthority {
	var nonce [16]byte
	epoch := ""
	if _, err := rand.Read(nonce[:]); err == nil {
		epoch = hex.EncodeToString(nonce[:])
	}
	return &ExecutionPermissionRuntimeAuthority{
		epoch:      epoch,
		threads:    make(map[string]executionPermissionThreadGrant),
		runs:       make(map[string]ExecutionPermissionRuntimeGrant),
		runFences:  make(map[string]uint64),
		runSignals: make(map[string]chan struct{}),
	}
}

// RuntimeEpoch distinguishes grants from separate authority instances. An
// empty value means a secure nonce was unavailable and consumers must fail
// closed instead of relying on a generation counter that restarts at zero.
func (a *ExecutionPermissionRuntimeAuthority) RuntimeEpoch() string {
	if a == nil {
		return ""
	}
	return a.epoch
}

func (a *ExecutionPermissionRuntimeAuthority) nextGenerationLocked() uint64 {
	a.generation++
	if a.generation == 0 {
		// A wrapped generation must never collide with an earlier authorization.
		a.generation = 1
		a.threads = make(map[string]executionPermissionThreadGrant)
		a.runs = make(map[string]ExecutionPermissionRuntimeGrant)
		for runID := range a.runSignals {
			a.clearRunFenceLocked(runID)
		}
		a.runFences = make(map[string]uint64)
	}
	return a.generation
}

// ActivateThreadFullAccess activates exactly one confirmed Thread preference
// and, when present, its atomically synchronized current Run snapshot.
func (a *ExecutionPermissionRuntimeAuthority) ActivateThreadFullAccess(
	thread ThreadExecutionPermissionSnapshot, run *RunExecutionPermissionSnapshot,
) (ExecutionPermissionRuntimeGrant, error) {
	if a == nil {
		return ExecutionPermissionRuntimeGrant{}, errors.New(
			"execution permission runtime authority is required")
	}
	if err := thread.Validate(); err != nil {
		return ExecutionPermissionRuntimeGrant{}, err
	}
	if thread.Mode != RunExecutionPermissionFull || !thread.OperatorConfirmed {
		return ExecutionPermissionRuntimeGrant{}, errors.New(
			"runtime activation requires a confirmed Full Access Thread snapshot")
	}
	if run != nil {
		if err := validateRuntimeFullAccessRun(*run); err != nil {
			return ExecutionPermissionRuntimeGrant{}, err
		}
		if run.MissionID != thread.MissionID {
			return ExecutionPermissionRuntimeGrant{}, errors.New(
				"Thread and Run Full Access runtime bindings do not match")
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	generation := a.nextGenerationLocked()
	for runID, grant := range a.runs {
		if grant.ThreadID == thread.ThreadID {
			delete(a.runs, runID)
			a.clearRunFenceLocked(runID)
		}
	}
	a.threads[thread.ThreadID] = executionPermissionThreadGrant{
		Generation: generation, SnapshotID: thread.ID, SnapshotRevision: thread.Revision,
	}
	grant := ExecutionPermissionRuntimeGrant{Generation: generation,
		ThreadID: thread.ThreadID, ThreadPermissionSnapshotID: thread.ID,
		ThreadPermissionRevision: thread.Revision}
	if run != nil {
		grant.RunID, grant.PermissionSnapshotID, grant.PermissionRevision =
			run.RunID, run.ID, run.Revision
		a.runs[run.RunID] = grant
	}
	return grant, nil
}

// ActivateRunFullAccess supports the lower-level Run permission endpoint. It
// does not create a Thread grant and therefore cannot flow into successor Runs.
func (a *ExecutionPermissionRuntimeAuthority) ActivateRunFullAccess(
	run RunExecutionPermissionSnapshot,
) (ExecutionPermissionRuntimeGrant, error) {
	if a == nil {
		return ExecutionPermissionRuntimeGrant{}, errors.New(
			"execution permission runtime authority is required")
	}
	if err := validateRuntimeFullAccessRun(run); err != nil {
		return ExecutionPermissionRuntimeGrant{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	generation := a.nextGenerationLocked()
	a.clearRunFenceLocked(run.RunID)
	grant := ExecutionPermissionRuntimeGrant{Generation: generation, RunID: run.RunID,
		PermissionSnapshotID: run.ID, PermissionRevision: run.Revision}
	a.runs[run.RunID] = grant
	return grant, nil
}

// BindThreadRun moves an already activated Thread grant to its newly
// materialized successor Run. A historical Full preference with no live
// process grant returns (zero, false, nil) and remains fail-closed.
func (a *ExecutionPermissionRuntimeAuthority) BindThreadRun(threadID string,
	run RunExecutionPermissionSnapshot,
) (ExecutionPermissionRuntimeGrant, bool, error) {
	if a == nil {
		return ExecutionPermissionRuntimeGrant{}, false, nil
	}
	threadID = strings.TrimSpace(threadID)
	if !ValidAgentID(threadID) || strings.ContainsRune(threadID, 0) {
		return ExecutionPermissionRuntimeGrant{}, false, errors.New(
			"execution permission runtime Thread id is invalid")
	}
	if err := validateRuntimeFullAccessRun(run); err != nil {
		return ExecutionPermissionRuntimeGrant{}, false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	thread, active := a.threads[threadID]
	if !active {
		return ExecutionPermissionRuntimeGrant{}, false, nil
	}
	generation := a.nextGenerationLocked()
	thread.Generation = generation
	a.threads[threadID] = thread
	for runID, grant := range a.runs {
		if grant.ThreadID == threadID {
			delete(a.runs, runID)
			a.clearRunFenceLocked(runID)
		}
	}
	grant := ExecutionPermissionRuntimeGrant{Generation: generation,
		ThreadID: threadID, ThreadPermissionSnapshotID: thread.SnapshotID,
		ThreadPermissionRevision: thread.SnapshotRevision, RunID: run.RunID,
		PermissionSnapshotID: run.ID, PermissionRevision: run.Revision}
	a.runs[run.RunID] = grant
	return grant, true, nil
}

func (a *ExecutionPermissionRuntimeAuthority) RevokeThread(threadID string) uint64 {
	if a == nil {
		return 0
	}
	threadID = strings.TrimSpace(threadID)
	a.mu.Lock()
	defer a.mu.Unlock()
	generation := a.nextGenerationLocked()
	delete(a.threads, threadID)
	for runID, grant := range a.runs {
		if grant.ThreadID == threadID {
			delete(a.runs, runID)
			a.clearRunFenceLocked(runID)
		}
	}
	return generation
}

func (a *ExecutionPermissionRuntimeAuthority) RevokeRun(runID string) uint64 {
	if a == nil {
		return 0
	}
	runID = strings.TrimSpace(runID)
	a.mu.Lock()
	defer a.mu.Unlock()
	generation := a.nextGenerationLocked()
	delete(a.runs, runID)
	a.clearRunFenceLocked(runID)
	return generation
}

// IssueRunAuthorizationFence returns the current process-local revocation epoch
// for a Run. Sibling child capabilities (for example Full CDP and MCP) share
// the epoch instead of invalidating one another merely by being created.
// RevokeRun deletes the epoch, so every previously issued child fence fails;
// the next explicit Issue creates a fresh generation.
func (a *ExecutionPermissionRuntimeAuthority) IssueRunAuthorizationFence(
	runID string,
) (uint64, error) {
	if a == nil {
		return 0, errors.New("execution permission runtime authority is required")
	}
	runID = strings.TrimSpace(runID)
	if !ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return 0, errors.New("execution permission runtime Run id is invalid")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if generation := a.runFences[runID]; generation != 0 {
		return generation, nil
	}
	generation := a.nextGenerationLocked()
	a.clearRunFenceLocked(runID)
	a.runFences[runID] = generation
	return generation, nil
}

// RotateRunAuthorizationFence invalidates every previously issued child
// capability for one Run without revoking the Run's parent Full Access grant.
// Use this only at an explicit child-permission revocation boundary; ordinary
// sibling capability creation must use IssueRunAuthorizationFence so siblings
// share, rather than replace, the current epoch.
func (a *ExecutionPermissionRuntimeAuthority) RotateRunAuthorizationFence(
	runID string,
) (uint64, error) {
	if a == nil {
		return 0, errors.New("execution permission runtime authority is required")
	}
	runID = strings.TrimSpace(runID)
	if !ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return 0, errors.New("execution permission runtime Run id is invalid")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	generation := a.nextGenerationLocked()
	a.clearRunFenceLocked(runID)
	a.runFences[runID] = generation
	return generation, nil
}

// RunAuthorizationFence observes the existing epoch without issuing authority.
// Native approvals can bind it at review and fail closed after revocation or
// restart; executing an old approval must not recreate a missing epoch.
func (a *ExecutionPermissionRuntimeAuthority) RunAuthorizationFence(runID string) (uint64, bool) {
	if a == nil {
		return 0, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	generation := a.runFences[strings.TrimSpace(runID)]
	return generation, generation != 0
}

func (a *ExecutionPermissionRuntimeAuthority) AllowsRunAuthorizationFence(
	runID string, generation uint64,
) bool {
	if a == nil || generation == 0 {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.runFences[strings.TrimSpace(runID)] == generation
}

// AllowsFullAccess rechecks the exact current immutable permission snapshot.
// The returned generation can be embedded in short-lived child authority and
// revalidated with AllowsFullAccessGeneration before every operation.
func (a *ExecutionPermissionRuntimeAuthority) AllowsFullAccess(
	run RunExecutionPermissionSnapshot,
) (uint64, bool) {
	if a == nil || run.Mode != RunExecutionPermissionFull ||
		!run.OperatorConfirmed {
		return 0, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	grant, ok := a.runs[run.RunID]
	if !ok || grant.PermissionSnapshotID != run.ID ||
		grant.PermissionRevision != run.Revision || grant.Generation == 0 {
		return 0, false
	}
	return grant.Generation, true
}

func (a *ExecutionPermissionRuntimeAuthority) AllowsFullAccessGeneration(
	run RunExecutionPermissionSnapshot, generation uint64,
) bool {
	current, allowed := a.AllowsFullAccess(run)
	return allowed && generation != 0 && current == generation
}

func (a *ExecutionPermissionRuntimeAuthority) AllowsThreadFullAccess(
	thread ThreadExecutionPermissionSnapshot,
	run *RunExecutionPermissionSnapshot,
) bool {
	if a == nil || thread.Mode != RunExecutionPermissionFull ||
		!thread.OperatorConfirmed {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	grant, ok := a.threads[thread.ThreadID]
	if !ok || grant.SnapshotID != thread.ID ||
		grant.SnapshotRevision != thread.Revision || grant.Generation == 0 {
		return false
	}
	if run == nil {
		return true
	}
	runGrant, ok := a.runs[run.RunID]
	return ok && runGrant.ThreadID == thread.ThreadID &&
		runGrant.ThreadPermissionSnapshotID == thread.ID &&
		runGrant.ThreadPermissionRevision == thread.Revision &&
		runGrant.PermissionSnapshotID == run.ID &&
		runGrant.PermissionRevision == run.Revision &&
		runGrant.Generation == grant.Generation
}

func validateRuntimeFullAccessRun(run RunExecutionPermissionSnapshot) error {
	if err := run.Validate(); err != nil {
		return err
	}
	if run.Mode != RunExecutionPermissionFull || !run.OperatorConfirmed {
		return errors.New(
			"runtime activation requires a confirmed Full Access Run snapshot")
	}
	return nil
}

// RunAuthorizationDone observes revocation of an existing fence without issuing
// permission. Passive task resources can release promptly when it is invalidated.
func (a *ExecutionPermissionRuntimeAuthority) RunAuthorizationDone(runID string, fence uint64) <-chan struct{} {
	if a == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if fence == 0 || a.runFences[runID] != fence {
		done := make(chan struct{})
		close(done)
		return done
	}
	if done := a.runSignals[runID]; done != nil {
		return done
	}
	if a.runSignals == nil {
		a.runSignals = make(map[string]chan struct{})
	}
	done := make(chan struct{})
	a.runSignals[runID] = done
	return done
}
func (a *ExecutionPermissionRuntimeAuthority) clearRunFenceLocked(runID string) {
	if done := a.runSignals[runID]; done != nil {
		close(done)
		delete(a.runSignals, runID)
	}
	delete(a.runFences, runID)
}
