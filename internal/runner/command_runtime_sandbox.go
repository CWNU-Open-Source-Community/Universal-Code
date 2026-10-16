package runner

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
)

// CommandRuntimeSandboxResult is the small terminal boundary a sandbox backend
// supplies to the shared Job manager. The backend owns isolation, cancellation,
// and complete tree reaping; the manager continues to own cursor framing,
// sanitization, durable Job state, Artifact capture, and restart fencing.
type CommandRuntimeSandboxResult struct {
	ExitCode   int
	Stdout     []byte
	Stderr     []byte
	TreeReaped bool
}

func (r CommandRuntimeSandboxResult) Validate(maximum int) error {
	if r.ExitCode < 0 || !r.TreeReaped || maximum < 1 ||
		len(r.Stdout) > maximum || len(r.Stderr) > maximum ||
		len(r.Stdout)+len(r.Stderr) > maximum {
		return ErrCommandRuntimeBoundary
	}
	return nil
}

// CommandRuntimeSandboxExecutor adapts an already-isolated backend without
// exposing backend choice to the model-facing command-runtime.v2 schema.
// Execute reports TreeReaped only after confirming the complete owned tree has
// stopped. An uncertain cleanup retains a stopping Job for recovery. Cancellation
// is delivered through ctx.
type CommandRuntimeSandboxExecutor interface {
	Identity() commandruntimeadapter.Identity
	Available() bool
	ExecuteSandboxCommand(context.Context, CommandRuntimeScope,
		CommandRuntimeResolvedSpec, io.ReadCloser) (CommandRuntimeSandboxResult, error)
}

// ReconcileSandboxCommandRuntimeStartup is a recovery-only entry point. The
// composition root must first verify removal of all owned backend resources.
// No executor is installed and disabled backends gain no launch capability.
func ReconcileSandboxCommandRuntimeStartup(ctx context.Context, store CommandRuntimeStore,
	identity commandruntimeadapter.Identity,
) (int, error) {
	if ctx == nil || store == nil || identity.Kind != commandruntimeadapter.KindSandboxedWorkspace || !identity.Executable() {
		return 0, ErrCommandRuntimeBoundary
	}
	return (&CommandRuntimeManager{store: store, adapter: identity}).ReconcileStartup(ctx)
}

// NewSandboxCommandRuntimeManager reuses the mature Run-owned Job state
// machine for a sandbox backend. A sandbox Job intentionally persists no host
// PID/process-group identifier because neither is safe cross-backend authority.
func NewSandboxCommandRuntimeManager(store CommandRuntimeStore,
	executor CommandRuntimeSandboxExecutor, ownerID string,
) (*CommandRuntimeManager, error) {
	if executor == nil || !executor.Available() {
		return nil, ErrCommandRuntimeUnavailable
	}
	identity := executor.Identity()
	if identity.Kind != commandruntimeadapter.KindSandboxedWorkspace ||
		!identity.Executable() {
		return nil, ErrCommandRuntimeUnavailable
	}
	generation := time.Now().UTC().UnixNano()
	return newCommandRuntimeManagerWithAdapter(store,
		commandRuntimeSandboxStarter{executor: executor}, identity, ownerID, generation)
}

type commandRuntimeSandboxStarter struct {
	executor CommandRuntimeSandboxExecutor
}

func (s commandRuntimeSandboxStarter) Name() string {
	if s.executor == nil {
		return ""
	}
	return s.executor.Identity().BackendIdentity
}

func (s commandRuntimeSandboxStarter) Available() bool {
	return s.executor != nil && s.executor.Available() &&
		s.executor.Identity().Kind == commandruntimeadapter.KindSandboxedWorkspace &&
		s.executor.Identity().Executable()
}

func (s commandRuntimeSandboxStarter) Start(ctx context.Context,
	scope CommandRuntimeScope, spec CommandRuntimeResolvedSpec,
) (commandRuntimeProcess, error) {
	if !s.Available() || !scope.Adapter.SameBackend(s.executor.Identity()) ||
		(spec.Spec.StdinPolicy == CommandRuntimeStdinClosed &&
			(spec.Spec.InitialStdin != "" || !spec.Spec.CloseInitialStdin)) ||
		(spec.Spec.StdinPolicy != CommandRuntimeStdinClosed &&
			spec.Spec.StdinPolicy != CommandRuntimeStdinPipe) {
		return nil, ErrCommandRuntimeBoundary
	}
	owned, err := ownedCommandRuntimeDispatchContext(ctx, spec)
	if err != nil {
		return nil, err
	}
	return newCommandRuntimeSandboxProcess(owned, s.executor, scope, spec), nil
}

type commandRuntimeSandboxProcess struct {
	cancel            context.CancelFunc
	stdinReader       *io.PipeReader
	stdinWriter       *io.PipeWriter
	stdoutReader      *io.PipeReader
	stdoutWriter      *io.PipeWriter
	stderrReader      *io.PipeReader
	stderrWriter      *io.PipeWriter
	done              chan struct{}
	closeOnce         sync.Once
	stdinMu           sync.Mutex
	mu                sync.Mutex
	exitCode          int
	waitErr           error
	treeReaped        bool
	admissionRequired bool
	admitted          chan struct{}
}

func newCommandRuntimeSandboxProcess(parent context.Context, executor CommandRuntimeSandboxExecutor,
	scope CommandRuntimeScope, spec CommandRuntimeResolvedSpec,
) *commandRuntimeSandboxProcess {
	var stdinReader *io.PipeReader
	var stdinWriter *io.PipeWriter
	if spec.Spec.StdinPolicy == CommandRuntimeStdinPipe {
		stdinReader, stdinWriter = io.Pipe()
	}
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	executionContext, cancel := context.WithCancel(context.WithoutCancel(parent))
	process := &commandRuntimeSandboxProcess{cancel: cancel,
		stdinReader: stdinReader, stdinWriter: stdinWriter,
		stdoutReader: stdoutReader, stdoutWriter: stdoutWriter,
		stderrReader: stderrReader, stderrWriter: stderrWriter,
		done: make(chan struct{}), exitCode: 125, admitted: make(chan struct{})}
	if adapter, ok := executor.(interface{ CommandRuntimeAdmissionRequired() bool }); ok && adapter.CommandRuntimeAdmissionRequired() {
		process.admissionRequired = true
		var acknowledge sync.Once
		executionContext = context.WithValue(executionContext, commandRuntimeAdmissionKey{}, func() {
			acknowledge.Do(func() { close(process.admitted) })
		})
	}
	go process.execute(executionContext, executor, scope, spec)
	return process
}

func (p *commandRuntimeSandboxProcess) waitForAdmission(ctx context.Context) error {
	if !p.admissionRequired {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.admitted:
		return ctx.Err()
	case <-p.done:
		select {
		case <-p.admitted:
			return ctx.Err()
		default:
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.waitErr != nil {
			return p.waitErr
		}
		return ErrCommandRuntimeBoundary
	}
}

func (p *commandRuntimeSandboxProcess) execute(ctx context.Context,
	executor CommandRuntimeSandboxExecutor, scope CommandRuntimeScope,
	spec CommandRuntimeResolvedSpec,
) {
	var stdin io.ReadCloser
	if p.stdinReader != nil {
		stdin = p.stdinReader
	}
	result, err := executor.ExecuteSandboxCommand(ctx, scope, spec, stdin)
	if p.stdinReader != nil {
		_ = p.stdinReader.Close()
	}
	if validationErr := result.Validate(spec.Spec.Output.ArtifactBytes); validationErr != nil {
		err = errors.Join(err, validationErr)
		result.ExitCode = 125
		result.Stdout = nil
		if err != nil {
			result.Stderr = []byte(err.Error())
			if len(result.Stderr) > spec.Spec.Output.ArtifactBytes {
				result.Stderr = result.Stderr[:spec.Spec.Output.ArtifactBytes]
			}
		}
	}
	// Native launch and preflight failures can have no child stderr. Preserve
	// their diagnostic in the existing bounded, sanitized output stream.
	if err != nil && len(result.Stderr) == 0 {
		remaining := spec.Spec.Output.ArtifactBytes - len(result.Stdout)
		if remaining > 0 {
			result.Stderr = []byte(err.Error())
			if len(result.Stderr) > remaining {
				result.Stderr = result.Stderr[:remaining]
			}
		}
	}
	if len(result.Stdout) > 0 {
		_, _ = p.stdoutWriter.Write(result.Stdout)
	}
	if len(result.Stderr) > 0 {
		_, _ = p.stderrWriter.Write(result.Stderr)
	}
	_ = p.stdoutWriter.Close()
	_ = p.stderrWriter.Close()
	p.mu.Lock()
	p.exitCode = result.ExitCode
	p.waitErr = err
	p.treeReaped = result.TreeReaped
	p.mu.Unlock()
	close(p.done)
}

func (p *commandRuntimeSandboxProcess) TreeReaped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.treeReaped
}

func (*commandRuntimeSandboxProcess) Ownership() CommandRuntimeProcessOwnership {
	return CommandRuntimeProcessOwnership{JobAssignedAtCreation: true, KillOnClose: true}
}

func (p *commandRuntimeSandboxProcess) Stdout() io.ReadCloser { return p.stdoutReader }
func (p *commandRuntimeSandboxProcess) Stderr() io.ReadCloser { return p.stderrReader }

func (p *commandRuntimeSandboxProcess) WriteStdin(data []byte) (int, error) {
	p.stdinMu.Lock()
	writer := p.stdinWriter
	p.stdinMu.Unlock()
	if writer == nil {
		return 0, ErrCommandRuntimeJobClosed
	}
	return writer.Write(data)
}

func (p *commandRuntimeSandboxProcess) CloseStdin() error {
	p.stdinMu.Lock()
	writer := p.stdinWriter
	p.stdinWriter = nil
	p.stdinMu.Unlock()
	if writer == nil {
		return nil
	}
	return writer.Close()
}

func (p *commandRuntimeSandboxProcess) Wait() (int, error) {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode, p.waitErr
}

func (p *commandRuntimeSandboxProcess) Cancel(grace time.Duration) error {
	p.cancel()
	_ = p.CloseStdin()
	if grace <= 0 {
		return nil
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
		return ErrCommandRuntimeUnavailable
	}
}

func (p *commandRuntimeSandboxProcess) Kill() error {
	p.cancel()
	_ = p.CloseStdin()
	return nil
}

func (p *commandRuntimeSandboxProcess) Close() error {
	p.closeOnce.Do(func() {
		p.cancel()
		_ = p.CloseStdin()
		if p.stdinReader != nil {
			_ = p.stdinReader.Close()
		}
		_ = p.stdoutReader.Close()
		_ = p.stderrReader.Close()
	})
	return nil
}

var _ commandRuntimeProcess = (*commandRuntimeSandboxProcess)(nil)
