//go:build !windows

package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type unixCommandRuntimeStarter struct{}

func newPlatformCommandRuntimeStarter() commandRuntimeStarter { return unixCommandRuntimeStarter{} }
func (unixCommandRuntimeStarter) Name() string                { return "posix-command-runtime-v2" }
func (unixCommandRuntimeStarter) Available() bool             { return true }

type unixCommandRuntimeProcess struct {
	mu             sync.Mutex
	command        *exec.Cmd
	stdin          io.WriteCloser
	stdout         io.ReadCloser
	stderr         io.ReadCloser
	pid            int
	pgid           int
	guardian       *exec.Cmd
	guardianSignal *os.File
	waitOnce       sync.Once
	waitDone       chan struct{}
	exitCode       int
	waitErr        error
}

func (unixCommandRuntimeStarter) Start(ctx context.Context, _ CommandRuntimeScope,
	spec CommandRuntimeResolvedSpec,
) (commandRuntimeProcess, error) {
	if validateCommandRuntimeLaunchDirectory(spec) != nil ||
		commandRuntimeFileDigestMatches(spec.ExecutablePath, spec.ExecutableSHA256) != nil ||
		validateCommandRuntimePythonInvocation(spec) != nil {
		return nil, ErrCommandRuntimeBoundary
	}
	command := exec.Command(spec.ExecutablePath, spec.CanonicalArgv...)
	if invocation := commandRuntimePythonInvocationPath(spec); invocation != "" {
		command.Args[0] = invocation
	}
	command.Dir = spec.AbsoluteDirectory
	command.Env = append([]string(nil), spec.Environment...)
	command.SysProcAttr = commandRuntimeSysProcAttr()
	stdout, stdoutChild, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderr, stderrChild, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutChild.Close()
		return nil, err
	}
	command.Stdout = stdoutChild
	command.Stderr = stderrChild
	var stdin io.WriteCloser
	if spec.Spec.StdinPolicy == CommandRuntimeStdinPipe {
		stdin, err = command.StdinPipe()
		if err != nil {
			_ = stdout.Close()
			_ = stdoutChild.Close()
			_ = stderr.Close()
			_ = stderrChild.Close()
			return nil, err
		}
	}
	cleanup := func() {
		if stdin != nil {
			_ = stdin.Close()
		}
		_ = stdout.Close()
		_ = stdoutChild.Close()
		_ = stderr.Close()
		_ = stderrChild.Close()
	}
	if err := CheckCommandRuntimeDispatch(ctx, spec); err != nil {
		cleanup()
		return nil, err
	}
	if validateCommandRuntimeLaunchDirectory(spec) != nil ||
		commandRuntimeFileDigestMatches(spec.ExecutablePath, spec.ExecutableSHA256) != nil ||
		validateCommandRuntimePythonInvocation(spec) != nil {
		cleanup()
		return nil, ErrCommandRuntimeBoundary
	}
	if err := command.Start(); err != nil {
		cleanup()
		return nil, err
	}
	// Cmd.Wait closes descriptors created by StdoutPipe and StderrPipe as soon
	// as the direct child exits. That races the runtime collectors for short
	// commands and can discard their final output. Own the pipes explicitly and
	// release only the parent's child-facing copies after a successful launch;
	// descendants keep their inherited copies until Wait reaps the process group.
	closeErr := errors.Join(stdoutChild.Close(), stderrChild.Close())
	if closeErr != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
		if stdin != nil {
			_ = stdin.Close()
		}
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, closeErr
	}
	guardian, guardianSignal, err := startCommandRuntimeGuardian(command.Process.Pid)
	if err != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
		if stdin != nil {
			_ = stdin.Close()
		}
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	return &unixCommandRuntimeProcess{command: command, stdin: stdin,
		stdout: stdout, stderr: stderr, pid: command.Process.Pid,
		pgid: command.Process.Pid, guardian: guardian,
		guardianSignal: guardianSignal, waitDone: make(chan struct{})}, nil
}

func (p *unixCommandRuntimeProcess) Ownership() CommandRuntimeProcessOwnership {
	return CommandRuntimeProcessOwnership{PID: p.pid, ProcessGroup: p.pgid,
		JobAssignedAtCreation: true, KillOnClose: true}
}
func (p *unixCommandRuntimeProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *unixCommandRuntimeProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *unixCommandRuntimeProcess) WriteStdin(data []byte) (int, error) {
	p.mu.Lock()
	input := p.stdin
	p.mu.Unlock()
	if input == nil {
		return 0, ErrCommandRuntimeJobClosed
	}
	return input.Write(data)
}
func (p *unixCommandRuntimeProcess) CloseStdin() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin == nil {
		return nil
	}
	err := p.stdin.Close()
	p.stdin = nil
	return err
}
func (p *unixCommandRuntimeProcess) Wait() (int, error) {
	p.waitOnce.Do(func() {
		p.waitErr = p.command.Wait()
		if p.waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(p.waitErr, &exitErr) {
				p.exitCode = exitErr.ExitCode()
				p.waitErr = nil
			}
		}
		_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
		p.waitErr = errors.Join(p.waitErr, p.finishGuardian(true))
		close(p.waitDone)
	})
	<-p.waitDone
	return p.exitCode, p.waitErr
}
func (p *unixCommandRuntimeProcess) Cancel(grace time.Duration) error {
	if err := syscall.Kill(-p.pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	// Reap concurrently with the grace period. Darwin reports EPERM rather
	// than ESRCH when a process group still exists but contains only zombies;
	// without a waiter, a successful TERM can therefore be mistaken for a
	// failed escalation. Wait is idempotent and also reaps any remaining owned
	// descendants before publishing waitDone.
	go func() { _, _ = p.Wait() }()
	deadline := time.Now().Add(grace)
	for grace > 0 && time.Now().Before(deadline) {
		select {
		case <-p.waitDone:
			return nil
		default:
		}
		if err := syscall.Kill(-p.pgid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-p.waitDone:
		return nil
	default:
	}
	return p.Kill()
}
func (p *unixCommandRuntimeProcess) Kill() error {
	err := syscall.Kill(-p.pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
func (p *unixCommandRuntimeProcess) Close() error {
	select {
	case <-p.waitDone:
	default:
		_ = p.Kill()
		_, _ = p.Wait()
	}
	var result error
	result = errors.Join(result, p.CloseStdin())
	p.mu.Lock()
	stdout, stderr := p.stdout, p.stderr
	p.stdout, p.stderr = nil, nil
	p.mu.Unlock()
	if stdout != nil {
		result = errors.Join(result, stdout.Close())
	}
	if stderr != nil {
		result = errors.Join(result, stderr.Close())
	}
	return result
}

func startCommandRuntimeGuardian(processGroup int) (*exec.Cmd, *os.File, error) {
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	guardian := exec.Command("/bin/sh", "-c",
		`if IFS= read -r signal <&3; then exit 0; fi; kill -KILL -"$1" 2>/dev/null || true`,
		"command-runtime-guardian", strconv.Itoa(processGroup))
	guardian.Env = []string{"PATH=/usr/bin:/bin", "HOME=", "TERM=dumb"}
	guardian.ExtraFiles = []*os.File{readPipe}
	guardian.Stdin = nil
	guardian.Stdout = io.Discard
	guardian.Stderr = io.Discard
	guardian.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := guardian.Start(); err != nil {
		_ = readPipe.Close()
		_ = writePipe.Close()
		return nil, nil, err
	}
	_ = readPipe.Close()
	return guardian, writePipe, nil
}

func (p *unixCommandRuntimeProcess) finishGuardian(completed bool) error {
	p.mu.Lock()
	guardian, signal := p.guardian, p.guardianSignal
	p.guardian = nil
	p.guardianSignal = nil
	p.mu.Unlock()
	var result error
	if signal != nil {
		if completed {
			_, result = signal.Write([]byte("complete\n"))
		}
		result = errors.Join(result, signal.Close())
	}
	if guardian != nil {
		result = errors.Join(result, guardian.Wait())
	}
	return result
}

func commandRuntimeFileDigestMatches(path, expected string) error {
	actual, err := commandRuntimeFileSHA256(path)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrCommandRuntimeBoundary
	}
	return nil
}

func cleanupCommandRuntimeOrphan(_, _ int) {
	// Never signal a persisted process group after restart: the identifier may
	// have been recycled. Live ownership is enforced before shutdown; Linux
	// additionally uses Pdeathsig for the direct process.
}
