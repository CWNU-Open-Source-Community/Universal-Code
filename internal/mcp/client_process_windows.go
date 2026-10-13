//go:build windows

package mcp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type clientProcessJob struct{ job windows.Handle }

func startClientProcessTree(ctx context.Context, cmd *exec.Cmd) (_ clientProcessTree, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	tree := &clientProcessJob{job: job}
	started := false
	defer func() {
		if err != nil {
			_ = tree.Close()
			if started {
				// Assignment itself may have failed while the root is suspended.
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
	attributes := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attributes = *cmd.SysProcAttr
	}
	// Like the host command runner, bind the Job before executing child code.
	// exec.Cmd closes the initial thread handle, so resume it through Toolhelp.
	attributes.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW
	attributes.HideWindow = true
	cmd.SysProcAttr = &attributes
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	started = true
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return nil, fmt.Errorf("MCP stdio process Job assignment: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := resumeClientProcess(uint32(cmd.Process.Pid)); err != nil {
		return nil, fmt.Errorf("MCP stdio process resume: %w", err)
	}
	return tree, nil
}

func resumeClientProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err := windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(thread)
		_, err = windows.ResumeThread(thread)
		return err
	}
	return errors.New("MCP stdio initial thread is unavailable")
}

func (p *clientProcessJob) Close() error {
	return errors.Join(windows.TerminateJobObject(p.job, 1), windows.CloseHandle(p.job))
}
