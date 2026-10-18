//go:build windows

package runner

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var diagnosticGetProcessIOCounters = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIoCounters")

func startFixedPowerShellDiagnostic(t *testing.T, process *windowsCommandRuntimeProcess) func() {
	t.Helper()
	stop, stopped := make(chan struct{}), make(chan struct{})
	started := time.Now()
	sample := func() {
		status, waitErr := windows.WaitForSingleObject(process.process, 0)
		var exitCode uint32
		exitErr := windows.GetExitCodeProcess(process.process, &exitCode)
		var created, exited, kernel, user windows.Filetime
		timeErr := windows.GetProcessTimes(process.process, &created, &exited, &kernel, &user)
		var counters windows.IO_COUNTERS
		ok, _, ioErr := diagnosticGetProcessIOCounters.Call(uintptr(process.process), uintptr(unsafe.Pointer(&counters)))
		cpu := func(value windows.Filetime) time.Duration {
			return time.Duration(uint64(value.HighDateTime)<<32|uint64(value.LowDateTime)) * 100 * time.Nanosecond
		}
		t.Logf("same-process sample elapsed=%s pid=%d wait_status=%d wait_error=%v exit=%d exit_error=%v kernel=%s user=%s time_error=%v io_ok=%t io_error=%v read_ops=%d write_ops=%d other_ops=%d read_bytes=%d write_bytes=%d other_bytes=%d", time.Since(started), process.pid, status, waitErr, exitCode, exitErr, cpu(kernel), cpu(user), timeErr, ok != 0, ioErr, counters.ReadOperationCount, counters.WriteOperationCount, counters.OtherOperationCount, counters.ReadTransferCount, counters.WriteTransferCount, counters.OtherTransferCount)
	}
	sample()
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		stackSamples := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sample()
				if helper := os.Getenv("TRAVERSE_FIXED_STACK_HELPER"); helper != "" && stackSamples < 2 {
					stackSamples++
					if status, err := windows.WaitForSingleObject(process.process, 0); err == nil && status == uint32(windows.WAIT_TIMEOUT) {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						value, err := exec.CommandContext(ctx, helper, strconv.Itoa(process.pid)).CombinedOutput()
						cancel()
						t.Logf("same-process managed sample error=%v\n%s", err, value)
					}
				}
			}
		}
	}()
	return func() { close(stop); <-stopped }
}
