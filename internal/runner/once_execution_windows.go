//go:build windows

package runner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unsafe"

	"cyberagent-workbench/internal/rootfs"

	"golang.org/x/sys/windows"
)

// windowsOnceStarter creates the process suspended with the Job Object bound
// at creation time (PROC_THREAD_ATTRIBUTE_JOB_LIST), so kill-on-close
// termination covers the complete process tree with no window for children
// to escape the job.
type windowsOnceStarter struct{}

func newPlatformOnceStarter() OnceStarter { return windowsOnceStarter{} }

func (windowsOnceStarter) Name() string    { return "windows_once_process" }
func (windowsOnceStarter) Available() bool { return true }

func (windowsOnceStarter) Start(ctx context.Context, spec OnceStartSpec) (OnceStartResult, error) {
	started := OnceStartResult{StartedAt: time.Now().UTC(), StdinClosed: true}
	if err := ctx.Err(); err != nil {
		return started, err
	}
	job, err := newHostJob()
	if err != nil {
		return started, fmt.Errorf("once Job Object creation failed: %w", err)
	}
	defer windows.CloseHandle(job)
	stdout, err := newControlledPipe()
	if err != nil {
		return started, err
	}
	defer stdout.close()
	stderr, err := newControlledPipe()
	if err != nil {
		return started, err
	}
	defer stderr.close()
	stdin, err := openControlledNullInput()
	if err != nil {
		return started, err
	}
	defer windows.CloseHandle(stdin)

	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return started, err
	}
	defer attributes.Delete()
	jobHandles := []windows.Handle{job}
	if err := attributes.Update(procThreadAttributeJobList,
		unsafe.Pointer(&jobHandles[0]), unsafe.Sizeof(jobHandles[0])); err != nil {
		return started, fmt.Errorf("once Job Object binding failed: %w", err)
	}
	inherited := []windows.Handle{stdin, stdout.write, stderr.write}
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&inherited[0]), uintptr(len(inherited))*unsafe.Sizeof(inherited[0])); err != nil {
		return started, err
	}

	applicationName, err := windows.UTF16PtrFromString(spec.ExecutablePath)
	if err != nil {
		return started, ErrOnceCommandBoundary
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(
		append([]string{spec.ExecutablePath}, spec.Argv...)))
	if err != nil {
		return started, ErrOnceCommandBoundary
	}
	if err := rootfs.CheckExternalDirectory(spec.WorkingDirectory); err != nil {
		return started, fmt.Errorf("%w: %v", ErrOnceCommandBoundary, err)
	}
	directory, err := windows.UTF16PtrFromString(spec.WorkingDirectory)
	if err != nil {
		return started, ErrOnceCommandBoundary
	}
	environment := onceEnvironmentBlock(spec.Environment)
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:       uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:    windows.STARTF_USESTDHANDLES,
			StdInput: stdin, StdOutput: stdout.write, StdErr: stderr.write,
		},
		ProcThreadAttributeList: attributes.List(),
	}
	process := windows.ProcessInformation{}
	creationFlags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW |
		windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcess(applicationName, commandLine, nil, nil, true,
		creationFlags, &environment[0], directory, &startup.StartupInfo, &process); err != nil {
		return started, fmt.Errorf("once process creation failed: %w", err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	if _, err := windows.ResumeThread(process.Thread); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_, _ = waitControlledProcess(context.WithoutCancel(ctx), process.Process, 2*time.Second)
		return started, fmt.Errorf("once process resume failed: %w", err)
	}
	_ = windows.CloseHandle(stdout.write)
	stdout.write = 0
	_ = windows.CloseHandle(stderr.write)
	stderr.write = 0

	stdoutChannel := make(chan controlledOutputResult, 1)
	stderrChannel := make(chan controlledOutputResult, 1)
	outputErrorChannel := make(chan error, 2)
	stdoutRead := stdout.read
	stderrRead := stderr.read
	stdout.read = 0
	stderr.read = 0
	go readControlledOutput(stdoutRead, stdoutChannel, outputErrorChannel)
	go readControlledOutput(stderrRead, stderrChannel, outputErrorChannel)

	code, waitErr := waitControlledProcess(ctx, process.Process, time.Hour)
	// The direct process can exit successfully while descendants remain in the
	// Job. Terminate the Job before collecting pipe EOF so no background child
	// survives the one-shot authority or keeps an inherited handle open.
	if terminateErr := windows.TerminateJobObject(job, 0); terminateErr != nil &&
		!errors.Is(terminateErr, windows.ERROR_ACCESS_DENIED) && waitErr == nil {
		waitErr = terminateErr
	}
	started.CompletedAt = time.Now().UTC()
	started.ExitCode = code
	started.TreeReaped = true
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		started.TimedOut = true
	} else if ctx.Err() != nil {
		started.Cancelled = true
	}
	stdoutResult := <-stdoutChannel
	stderrResult := <-stderrChannel
	started.Stdout = onceCaptureFromControlled(stdoutResult)
	started.Stderr = onceCaptureFromControlled(stderrResult)
	if waitErr == nil && (stdoutResult.err != nil || stderrResult.err != nil) {
		waitErr = errors.Join(stdoutResult.err, stderrResult.err)
	}
	return started, waitErr
}

func onceCaptureFromControlled(result controlledOutputResult) OnceOutputCapture {
	buffer := &boundedOnceBuffer{}
	_, _ = buffer.Write(result.output.Data)
	capture := buffer.Capture()
	capture.ObservedBytes = int(result.output.ObservedBytes)
	capture.ObservedSHA256 = result.observedSHA256
	capture.Truncated = result.output.Truncated || result.err != nil
	return capture
}

// onceEnvironmentBlock builds a full replacement environment block from the
// allowlisted entries only; the agent process environment is never inherited.
func onceEnvironmentBlock(values []string) []uint16 {
	values = append([]string(nil), values...)
	// CreateProcess requires a case-insensitively sorted environment block.
	sort.Slice(values, func(left, right int) bool {
		return strings.ToUpper(values[left]) < strings.ToUpper(values[right])
	})
	block := make([]uint16, 0, 512)
	for _, value := range values {
		encoded := windows.StringToUTF16(value)
		block = append(block, encoded[:len(encoded)-1]...)
		block = append(block, 0)
	}
	return append(block, 0)
}
