//go:build darwin || linux

package terminal

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"cyberagent-workbench/internal/domain"

	"github.com/creack/pty"
)

const posixTerminalCloseGrace = 250 * time.Millisecond

type posixBackend struct{}

type posixProcess struct {
	shell     ShellInfo
	mu        sync.Mutex
	closeOnce sync.Once
	pty       *os.File
	process   *os.Process
	closed    bool
	done      chan struct{}
	exitCode  int
	waitErr   error
}

func newPlatformBackend() Backend {
	return posixBackend{}
}

func (posixBackend) Name() string {
	return "posix-bash-pty-user-v1"
}

func (posixBackend) Available() bool {
	_, err := trustedPOSIXBashPath()
	return err == nil
}

func (posixBackend) Start(ctx context.Context,
	request BackendStartRequest,
) (Process, error) {
	if ctx == nil || ctx.Err() != nil || !domain.ValidAgentID(request.SessionID) ||
		!filepath.IsAbs(request.WorkspaceRoot) ||
		filepath.Clean(request.WorkspaceRoot) != request.WorkspaceRoot ||
		request.Columns < MinColumns || request.Columns > MaxColumns ||
		request.Rows < MinRows || request.Rows > MaxRows {
		return nil, ErrTerminalBoundary
	}
	root, err := os.Lstat(request.WorkspaceRoot)
	if err != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return nil, ErrTerminalBoundary
	}
	shell, err := resolveTerminalShell(request.ShellID)
	if err != nil {
		return nil, err
	}
	// The terminal is process-local but persistent. The caller's start context
	// authorizes creation only; Manager.Close owns the later lifetime.
	command := exec.Command(shell.Path,
		"--noprofile", "--norc", "+m", "-i")
	command.Dir = request.WorkspaceRoot
	command.Env = posixTerminalEnvironment()
	terminalFile, err := pty.StartWithSize(command, &pty.Winsize{
		Cols: uint16(request.Columns), Rows: uint16(request.Rows),
	})
	if err != nil || command.Process == nil {
		if terminalFile != nil {
			_ = terminalFile.Close()
		}
		return nil, errors.Join(ErrTerminalUnavailable, err)
	}
	process := &posixProcess{
		shell: shell,
		pty:   terminalFile, process: command.Process, done: make(chan struct{}),
	}
	go process.await(command)
	return process, nil
}

func (p *posixProcess) ShellInfo() ShellInfo { return p.shell }

func (p *posixProcess) Boundary() ProcessBoundary {
	return ProcessBoundary{
		UserOwned: true, AgentInputDefault: false,
		JobAssignedAtCreation: true, KillOnJobClose: true, Persistent: true,
	}
}

func (p *posixProcess) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	p.mu.Lock()
	terminalFile := p.pty
	closed := p.closed
	p.mu.Unlock()
	if closed || terminalFile == nil {
		return 0, io.EOF
	}
	count, err := terminalFile.Read(buffer)
	if errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EIO) {
		err = io.EOF
	}
	return count, err
}

func (p *posixProcess) Write(data []byte) (int, error) {
	p.mu.Lock()
	terminalFile := p.pty
	closed := p.closed
	p.mu.Unlock()
	if closed || terminalFile == nil {
		return 0, ErrTerminalClosed
	}
	return terminalFile.Write(data)
}

func (p *posixProcess) Resize(columns int, rows int) error {
	if columns < MinColumns || columns > MaxColumns ||
		rows < MinRows || rows > MaxRows {
		return ErrTerminalBoundary
	}
	p.mu.Lock()
	terminalFile := p.pty
	closed := p.closed
	p.mu.Unlock()
	if closed || terminalFile == nil {
		return ErrTerminalClosed
	}
	return pty.Setsize(terminalFile, &pty.Winsize{
		Cols: uint16(columns), Rows: uint16(rows),
	})
}

func (p *posixProcess) Wait(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, ErrTerminalBoundary
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-p.done:
		p.mu.Lock()
		exitCode, err := p.exitCode, p.waitErr
		p.mu.Unlock()
		return exitCode, err
	}
}

func (p *posixProcess) Close() error {
	var result error
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		terminalFile := p.pty
		process := p.process
		p.pty = nil
		p.mu.Unlock()
		if terminalFile != nil {
			result = errors.Join(result, terminalFile.Close())
		}
		if process == nil {
			return
		}
		select {
		case <-p.done:
			return
		default:
		}
		pid := process.Pid
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil &&
			!errors.Is(err, syscall.ESRCH) {
			result = errors.Join(result, err)
		}
		timer := time.NewTimer(posixTerminalCloseGrace)
		select {
		case <-p.done:
			timer.Stop()
			return
		case <-timer.C:
		}
		select {
		case <-p.done:
			return
		default:
		}
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil &&
			!errors.Is(err, syscall.ESRCH) {
			result = errors.Join(result, err)
		}
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			result = errors.Join(result, ErrTerminalUnavailable)
		}
	})
	return result
}

func (p *posixProcess) await(command *exec.Cmd) {
	err := command.Wait()
	exitCode := 0
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		exitCode = exitError.ExitCode()
		err = nil
	}
	p.mu.Lock()
	p.exitCode = exitCode
	p.waitErr = err
	p.mu.Unlock()
	close(p.done)
}

func trustedPOSIXBashPath() (string, error) {
	// Do not resolve an interpreter from an operator- or workspace-modified
	// PATH. These fixed locations cover the platform Bash shipped by macOS and
	// mainstream Linux distributions. EvalSymlinks accepts merged-/usr layouts
	// while the final target must still be a regular executable file.
	for _, candidate := range []string{"/bin/bash", "/usr/bin/bash"} {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !filepath.IsAbs(resolved) {
			continue
		}
		info, err := os.Lstat(resolved)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return filepath.Clean(resolved), nil
		}
	}
	return "", ErrTerminalUnavailable
}

func posixTerminalEnvironment() []string {
	allowed := map[string]struct{}{
		"HOME": {}, "LANG": {}, "LOGNAME": {}, "PATH": {}, "SHELL": {},
		"TMPDIR": {}, "USER": {}, "XDG_CACHE_HOME": {},
		"XDG_CONFIG_HOME": {}, "XDG_DATA_HOME": {},
	}
	environment := make([]string, 0, len(allowed)+8)
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		_, exactAllowed := allowed[key]
		if exactAllowed || strings.HasPrefix(key, "LC_") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment,
		"TERM=xterm-256color", "COLORTERM=truecolor", "HISTFILE=/dev/null")
	return environment
}

var _ Backend = posixBackend{}
var _ Process = (*posixProcess)(nil)
