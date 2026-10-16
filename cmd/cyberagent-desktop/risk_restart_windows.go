//go:build windows && desktop && wv2runtime.error

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const riskRestartParentWaitMilliseconds = 120_000

func newRiskRestartReadyChannel() (*riskRestartReadyChannel, error) {
	token, err := newRiskRestartReadyToken()
	if err != nil {
		return nil, err
	}
	attributes := windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	var readHandle, writeHandle windows.Handle
	if err := windows.CreatePipe(&readHandle, &writeHandle, &attributes, 0); err != nil {
		return nil, err
	}
	if err := windows.SetHandleInformation(readHandle, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		windows.CloseHandle(readHandle)
		windows.CloseHandle(writeHandle)
		return nil, err
	}
	reader := os.NewFile(uintptr(readHandle), "desktop-risk-restart-ready-reader")
	writer := os.NewFile(uintptr(writeHandle), "desktop-risk-restart-ready-writer")
	if reader == nil || writer == nil {
		if reader != nil {
			_ = reader.Close()
		} else {
			windows.CloseHandle(readHandle)
		}
		if writer != nil {
			_ = writer.Close()
		} else {
			windows.CloseHandle(writeHandle)
		}
		return nil, errors.New("desktop restart ready pipe is unavailable")
	}
	return &riskRestartReadyChannel{
		reader: reader,
		writer: writer,
		value:  strconv.FormatUint(uint64(writeHandle), 10),
		token:  token,
	}, nil
}

func validRiskRestartReadyDescriptor(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed != 0 && uint64(uintptr(parsed)) == parsed
}

func configureRiskRestartHelperCommand(command *exec.Cmd, ready *riskRestartReadyChannel) {
	command.SysProcAttr = &syscall.SysProcAttr{
		// This helper becomes the Wails app. Hide only a console: HideWindow
		// sets STARTF_USESHOWWINDOW/SW_HIDE and hides Wails' first ShowWindow.
		CreationFlags:              windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(ready.writer.Fd())},
	}
}

func signalRiskRestartReady(value, token string) error {
	writer, err := openRiskRestartReadyWriter(value)
	if err != nil {
		return err
	}
	return writeRiskRestartReady(writer, token)
}

func openRiskRestartReadyWriter(value string) (*os.File, error) {
	if !validRiskRestartReadyDescriptor(value) {
		return nil, errors.New("desktop restart helper ready handle is invalid")
	}
	parsed, _ := strconv.ParseUint(value, 10, 64)
	writer := os.NewFile(uintptr(parsed), "desktop-risk-restart-ready")
	if writer == nil {
		return nil, errors.New("desktop restart helper ready handle is unavailable")
	}
	return writer, nil
}

type windowsRiskRestartParentWaiter struct {
	handle windows.Handle
	once   sync.Once
}

func prepareRiskRestartParent(parentPID int) (*windowsRiskRestartParentWaiter, error) {
	if parentPID <= 0 || parentPID != os.Getppid() {
		return nil, errors.New("desktop restart helper parent identity is invalid")
	}
	handle, err := windows.OpenProcess(
		windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false, uint32(parentPID))
	if err != nil {
		return nil, errors.New("desktop restart helper parent is unavailable")
	}

	parentExecutable, err := windowsProcessImagePath(handle)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, errors.New("desktop restart helper parent identity is unavailable")
	}
	selfExecutable, err := os.Executable()
	if err != nil || !sameWindowsExecutable(parentExecutable, selfExecutable) {
		windows.CloseHandle(handle)
		return nil, errors.New("desktop restart helper parent executable is invalid")
	}
	return &windowsRiskRestartParentWaiter{handle: handle}, nil
}

func (w *windowsRiskRestartParentWaiter) Wait() error {
	if w == nil || w.handle == 0 {
		return errors.New("desktop restart helper parent waiter is unavailable")
	}
	result, err := windows.WaitForSingleObject(w.handle, riskRestartParentWaitMilliseconds)
	if err != nil {
		return errors.New("desktop restart helper could not wait for its parent")
	}
	if result != windows.WAIT_OBJECT_0 {
		return errors.New("desktop restart helper timed out waiting for its parent")
	}
	return nil
}

func (w *windowsRiskRestartParentWaiter) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		if w.handle != 0 {
			windows.CloseHandle(w.handle)
			w.handle = 0
		}
	})
}

func windowsProcessImagePath(handle windows.Handle) (string, error) {
	buffer := make([]uint16, 32_768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil ||
		size == 0 || int(size) > len(buffer) {
		return "", errors.New("process image path is unavailable")
	}
	return windows.UTF16ToString(buffer[:size]), nil
}

func sameWindowsExecutable(left, right string) bool {
	left, leftErr := filepath.Abs(filepath.Clean(left))
	right, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil || !filepath.IsAbs(left) || !filepath.IsAbs(right) {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(left); err == nil {
		left = filepath.Clean(resolved)
	}
	if resolved, err := filepath.EvalSymlinks(right); err == nil {
		right = filepath.Clean(resolved)
	}
	return strings.EqualFold(left, right)
}
