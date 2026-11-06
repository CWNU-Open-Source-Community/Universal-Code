//go:build windows

package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"cyberagent-workbench/internal/domain"

	"golang.org/x/sys/windows"
)

const (
	procThreadAttributeJobList = 0x0002000D
	terminalProcessLimit       = 32
	terminalMemoryLimit        = 2 * 1024 * 1024 * 1024
	terminalCloseExitCode      = 125
)

type windowsBackend struct{}

type windowsProcess struct {
	shell      ShellInfo
	mu         sync.Mutex
	closeOnce  sync.Once
	reads      sync.WaitGroup
	console    windows.Handle
	inputWrite windows.Handle
	outputRead windows.Handle
	process    windows.Handle
	job        windows.Handle
	closed     bool
}

func newPlatformBackend() Backend {
	return windowsBackend{}
}

func (windowsBackend) Name() string {
	return "windows-conpty-user-v1"
}

func (windowsBackend) Available() bool {
	return runtime.GOOS == "windows"
}

func (windowsBackend) Start(ctx context.Context,
	request BackendStartRequest,
) (Process, error) {
	if ctx == nil || ctx.Err() != nil || !domainIdentifier(request.SessionID) ||
		!filepath.IsAbs(request.WorkspaceRoot) ||
		filepath.Clean(request.WorkspaceRoot) != request.WorkspaceRoot ||
		request.Columns < MinColumns || request.Columns > MaxColumns ||
		request.Rows < MinRows || request.Rows > MaxRows {
		return nil, ErrTerminalBoundary
	}
	info, err := os.Lstat(request.WorkspaceRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrTerminalBoundary
	}
	rootPointer, err := windows.UTF16PtrFromString(request.WorkspaceRoot)
	if err != nil {
		return nil, ErrTerminalBoundary
	}
	attributes, err := windows.GetFileAttributes(rootPointer)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, ErrTerminalBoundary
	}
	shellInfo, err := resolveTerminalShell(request.ShellID)
	if err != nil {
		return nil, err
	}
	shell := shellInfo.Path

	inputRead, inputWrite, err := createTerminalPipe()
	if err != nil {
		return nil, err
	}
	closeInput := true
	defer func() {
		if closeInput {
			_ = windows.CloseHandle(inputRead)
			_ = windows.CloseHandle(inputWrite)
		}
	}()
	outputRead, outputWrite, err := createTerminalPipe()
	if err != nil {
		return nil, err
	}
	closeOutput := true
	defer func() {
		if closeOutput {
			_ = windows.CloseHandle(outputRead)
			_ = windows.CloseHandle(outputWrite)
		}
	}()
	if err := windows.SetHandleInformation(inputWrite,
		windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, err
	}
	if err := windows.SetHandleInformation(outputRead,
		windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, err
	}
	console := windows.Handle(0)
	size := windows.Coord{X: int16(request.Columns), Y: int16(request.Rows)}
	if err := windows.CreatePseudoConsole(size, inputRead, outputWrite, 0,
		&console); err != nil {
		return nil, fmt.Errorf("%w: ConPTY creation failed",
			ErrTerminalUnavailable)
	}
	closeConsole := true
	defer func() {
		if closeConsole {
			windows.ClosePseudoConsole(console)
		}
	}()
	_ = windows.CloseHandle(inputRead)
	inputRead = 0
	_ = windows.CloseHandle(outputWrite)
	outputWrite = 0

	job, err := newTerminalJob()
	if err != nil {
		return nil, err
	}
	closeJob := true
	defer func() {
		if closeJob {
			_ = windows.CloseHandle(job)
		}
	}()
	attributeList, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attributeList.Delete()
	consoleAttributeValue := *(*unsafe.Pointer)(unsafe.Pointer(&console))
	if err := attributeList.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		consoleAttributeValue, unsafe.Sizeof(console)); err != nil {
		return nil, fmt.Errorf("%w: ConPTY process binding failed",
			ErrTerminalUnavailable)
	}
	jobHandles := []windows.Handle{job}
	if err := attributeList.Update(procThreadAttributeJobList,
		unsafe.Pointer(&jobHandles[0]), unsafe.Sizeof(jobHandles[0])); err != nil {
		return nil, fmt.Errorf("%w: terminal Job Object binding failed",
			ErrTerminalUnavailable)
	}
	shellPointer, err := windows.UTF16PtrFromString(shell)
	if err != nil {
		return nil, ErrTerminalBoundary
	}
	arguments := []string{shell, "-NoLogo", "-NoProfile"}
	if shellInfo.ID == "cmd" {
		arguments = []string{shell, "/D"}
	}
	commandLinePointer, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(arguments))
	if err != nil {
		return nil, ErrTerminalBoundary
	}
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:    uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags: windows.STARTF_USESTDHANDLES,
		},
		ProcThreadAttributeList: attributeList.List(),
	}
	processInformation := windows.ProcessInformation{}
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT |
		windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(shellPointer, commandLinePointer,
		nil, nil, false, flags, nil, rootPointer, &startup.StartupInfo,
		&processInformation); err != nil {
		return nil, fmt.Errorf("%w: user terminal process start failed",
			ErrTerminalUnavailable)
	}
	runtime.KeepAlive(console)
	_ = windows.CloseHandle(processInformation.Thread)
	closeInput = false
	closeOutput = false
	closeConsole = false
	closeJob = false
	return &windowsProcess{
		shell:   shellInfo,
		console: console, inputWrite: inputWrite, outputRead: outputRead,
		process: processInformation.Process, job: job,
	}, nil
}

func (p *windowsProcess) ShellInfo() ShellInfo { return p.shell }

func (p *windowsProcess) Boundary() ProcessBoundary {
	return ProcessBoundary{
		UserOwned: true, AgentInputDefault: false,
		JobAssignedAtCreation: true, KillOnJobClose: true, Persistent: true,
	}
}

func (p *windowsProcess) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	p.mu.Lock()
	handle := p.outputRead
	closed := p.closed
	if !closed && handle != 0 {
		p.reads.Add(1)
	}
	p.mu.Unlock()
	if closed || handle == 0 {
		return 0, ioEOF()
	}
	defer p.reads.Done()
	var count uint32
	err := windows.ReadFile(handle, buffer, &count, nil)
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
		errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		err = ioEOF()
	}
	return int(count), err
}

func (p *windowsProcess) Write(data []byte) (int, error) {
	p.mu.Lock()
	handle := p.inputWrite
	closed := p.closed
	p.mu.Unlock()
	if closed || handle == 0 {
		return 0, ErrTerminalClosed
	}
	var count uint32
	if err := windows.WriteFile(handle, data, &count, nil); err != nil {
		return int(count), err
	}
	return int(count), nil
}

func (p *windowsProcess) Resize(columns int, rows int) error {
	if columns < MinColumns || columns > MaxColumns ||
		rows < MinRows || rows > MaxRows {
		return ErrTerminalBoundary
	}
	p.mu.Lock()
	console := p.console
	closed := p.closed
	p.mu.Unlock()
	if closed || console == 0 {
		return ErrTerminalClosed
	}
	return windows.ResizePseudoConsole(console,
		windows.Coord{X: int16(columns), Y: int16(rows)})
}

func (p *windowsProcess) Wait(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, ErrTerminalBoundary
	}
	for {
		p.mu.Lock()
		process := p.process
		closed := p.closed
		p.mu.Unlock()
		if process == 0 {
			if closed {
				return 0, ErrTerminalClosed
			}
			return 0, ErrTerminalUnavailable
		}
		status, err := windows.WaitForSingleObject(process, 50)
		if err != nil {
			return 0, err
		}
		if status == windows.WAIT_OBJECT_0 {
			var exitCode uint32
			if err := windows.GetExitCodeProcess(process, &exitCode); err != nil {
				return 0, err
			}
			return int(exitCode), nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
	}
}

func (p *windowsProcess) Close() error {
	var result error
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		job := p.job
		process := p.process
		input := p.inputWrite
		output := p.outputRead
		console := p.console
		p.job = 0
		p.process = 0
		p.inputWrite = 0
		p.outputRead = 0
		p.console = 0
		p.mu.Unlock()
		if job != 0 {
			if err := windows.TerminateJobObject(job,
				terminalCloseExitCode); err != nil &&
				!errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				result = errors.Join(result, err)
			}
		}
		// CloseHandle on a pipe with a synchronous ReadFile can wait forever
		// while ConPTY still owns the writer. Keep draining its final frame on
		// a separate goroutine until ClosePseudoConsole breaks the channel.
		// The normal reader stops accepting new reads once closed is set.
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			if output == 0 {
				return
			}
			buffer := make([]byte, 32*1024)
			for {
				var count uint32
				if err := windows.ReadFile(output, buffer, &count, nil); err != nil || count == 0 {
					return
				}
			}
		}()
		if console != 0 {
			windows.ClosePseudoConsole(console)
		}
		<-drained
		p.reads.Wait()
		if input != 0 {
			result = errors.Join(result, windows.CloseHandle(input))
		}
		if output != 0 {
			result = errors.Join(result, windows.CloseHandle(output))
		}
		if process != 0 {
			_, _ = waitTerminalHandle(process, 2*time.Second)
			result = errors.Join(result, windows.CloseHandle(process))
		}
		if job != 0 {
			result = errors.Join(result, windows.CloseHandle(job))
		}
	})
	return result
}

func createTerminalPipe() (windows.Handle, windows.Handle, error) {
	security := windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	var readHandle windows.Handle
	var writeHandle windows.Handle
	if err := windows.CreatePipe(&readHandle, &writeHandle,
		&security, 0); err != nil {
		return 0, 0, err
	}
	return readHandle, writeHandle, nil
}

func newTerminalJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags =
		windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
			windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS |
			windows.JOB_OBJECT_LIMIT_JOB_MEMORY |
			windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	limits.BasicLimitInformation.ActiveProcessLimit = terminalProcessLimit
	limits.JobMemoryLimit = terminalMemoryLimit
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func validateTerminalShell(path string) error {
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 {
		return ErrTerminalUnavailable
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ErrTerminalBoundary
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrTerminalUnavailable
	}
	return nil
}

func terminalShellPath() (string, error) {
	windowsDirectory, err := windows.GetWindowsDirectory()
	if err != nil {
		return "", fmt.Errorf("%w: Windows directory is unavailable",
			ErrTerminalUnavailable)
	}
	windowsDirectory = filepath.Clean(windowsDirectory)
	if !filepath.IsAbs(windowsDirectory) || windowsDirectory == "." {
		return "", fmt.Errorf("%w: Windows directory is invalid",
			ErrTerminalUnavailable)
	}
	path := filepath.Join(windowsDirectory, "System32",
		"WindowsPowerShell", "v1.0", "powershell.exe")
	if err := validateTerminalShell(path); err != nil {
		return "", err
	}
	return path, nil
}

func waitTerminalHandle(process windows.Handle,
	maximum time.Duration,
) (int, error) {
	deadline := time.Now().Add(maximum)
	for {
		status, err := windows.WaitForSingleObject(process, 25)
		if err != nil {
			return 0, err
		}
		if status == windows.WAIT_OBJECT_0 {
			var exitCode uint32
			if err := windows.GetExitCodeProcess(process, &exitCode); err != nil {
				return 0, err
			}
			return int(exitCode), nil
		}
		if time.Now().After(deadline) {
			return 0, context.DeadlineExceeded
		}
	}
}

func domainIdentifier(value string) bool {
	return domain.ValidAgentID(value)
}

func ioEOF() error {
	return io.EOF
}
