//go:build windows

package application

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"golang.org/x/sys/windows"
)

var fixedOperatorGetProcessIOCounters = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIoCounters")

var errFixedOperatorDiagnosticBinding = errors.New("native diagnostic process binding changed")

type fixedOperatorWindowsCounters struct {
	kernelCPU, userCPU time.Duration
	io                 windows.IO_COUNTERS
}

// These kernel counters observe CPU and I/O activity. They cannot distinguish
// startup, script execution, or output read errors, or identify a timeout's
// cause. No environment,
// memory, command text, filesystem contents, or pipe handles are inspected.
// Calls have no retries, network access, or synchronous wait-chain/pipe queries.
func fixedOperatorNativeProcessSample(job runner.CommandRuntimeJob) string {
	if job.State != runner.CommandRuntimeJobRunning || job.PID <= 0 || job.StartedAt == nil ||
		job.OwnerID != "fixed-command-owner" || job.Adapter.BackendIdentity != runner.RestrictedFixedCommandBackend {
		return ""
	}
	prefix := fmt.Sprintf("%s pid=%d raw_stdout=%d raw_stderr=%d", time.Now().UTC().Format(time.RFC3339Nano),
		job.PID, job.StdoutObservedBytes, job.StderrObservedBytes)
	started := time.Now()
	counters, err := readFixedOperatorWindowsCounters(job)
	prefix += " query_elapsed=" + time.Since(started).String()
	if err != nil {
		return fmt.Sprintf("%s unavailable=%v", prefix, err)
	}
	return fmt.Sprintf("%s kernel_cpu=%s user_cpu=%s read_ops=%d write_ops=%d other_ops=%d read_bytes=%d write_bytes=%d other_bytes=%d",
		prefix, counters.kernelCPU, counters.userCPU,
		counters.io.ReadOperationCount, counters.io.WriteOperationCount, counters.io.OtherOperationCount,
		counters.io.ReadTransferCount, counters.io.WriteTransferCount, counters.io.OtherTransferCount)
}

func readFixedOperatorWindowsCounters(job runner.CommandRuntimeJob) (fixedOperatorWindowsCounters, error) {
	if job.PID <= 0 || job.StartedAt == nil || job.CreatedAt.IsZero() || job.ExecutablePath == "" {
		return fixedOperatorWindowsCounters{}, errFixedOperatorDiagnosticBinding
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, uint32(job.PID))
	if err != nil {
		return fixedOperatorWindowsCounters{}, err
	}
	defer windows.CloseHandle(process)
	// A durable PID is not authority. Reject a recycled PID or a foreign
	// process before reading its counters; require our child, image and exact
	// creation interval from this live Job's native dispatch/handoff.
	var basic windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(process, windows.ProcessBasicInformation, unsafe.Pointer(&basic),
		uint32(unsafe.Sizeof(basic)), nil); err != nil {
		return fixedOperatorWindowsCounters{}, err
	}
	if basic.InheritedFromUniqueProcessId != uintptr(os.Getpid()) {
		return fixedOperatorWindowsCounters{}, errFixedOperatorDiagnosticBinding
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return fixedOperatorWindowsCounters{}, err
	}
	creationTime := time.Unix(0, created.Nanoseconds())
	if creationTime.Before(job.CreatedAt) || creationTime.After(*job.StartedAt) {
		return fixedOperatorWindowsCounters{}, errFixedOperatorDiagnosticBinding
	}
	image := make([]uint16, 32768)
	size := uint32(len(image))
	if err := windows.QueryFullProcessImageName(process, 0, &image[0], &size); err != nil {
		return fixedOperatorWindowsCounters{}, err
	}
	if !strings.EqualFold(filepath.Clean(windows.UTF16ToString(image[:size])), filepath.Clean(job.ExecutablePath)) {
		return fixedOperatorWindowsCounters{}, errFixedOperatorDiagnosticBinding
	}
	var counters windows.IO_COUNTERS
	success, _, callErr := fixedOperatorGetProcessIOCounters.Call(uintptr(process), uintptr(unsafe.Pointer(&counters)))
	if success == 0 {
		return fixedOperatorWindowsCounters{}, fmt.Errorf("GetProcessIoCounters: %w", callErr)
	}
	toDuration := func(value windows.Filetime) time.Duration {
		return time.Duration(uint64(value.HighDateTime)<<32|uint64(value.LowDateTime)) * 100 * time.Nanosecond
	}
	return fixedOperatorWindowsCounters{kernelCPU: toDuration(kernel), userCPU: toDuration(user), io: counters}, nil
}

func TestFixedOperatorWindowsNativeDiagnostic(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestFixedOperatorWindowsNativeDiagnosticChild$")
	hideSubprocessWindow(command)
	t.Cleanup(func() {
		cancel()
		if command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	command.Env = []string{"TRAVERSE_FIXED_DIAGNOSTIC_CHILD=1"}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if marker, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || marker != "diagnostic-ready\n" {
		t.Fatalf("diagnostic helper startup: %q %v", marker, err)
	}
	job := runner.CommandRuntimeJob{PID: command.Process.Pid, CreatedAt: created, StartedAt: &started,
		ExecutablePath: executable, State: runner.CommandRuntimeJobRunning, OwnerID: "fixed-command-owner",
		Adapter: commandruntimeadapter.Identity{BackendIdentity: runner.RestrictedFixedCommandBackend}}
	counters, err := readFixedOperatorWindowsCounters(job)
	if err != nil || counters.io.WriteOperationCount == 0 || counters.io.WriteTransferCount == 0 {
		t.Fatalf("live owned child counters: %+v %v", counters, err)
	}
	if sample := fixedOperatorNativeProcessSample(job); !strings.Contains(sample, "write_bytes=") || strings.Contains(sample, "unavailable=") {
		t.Fatalf("missing native counters: %q", sample)
	}
	// This child stays alive until our cleanup closes stdin. The sampler test
	// does not depend on the incidental lifetime of a fast fixed command.
	diagnostics := &fixedOperatorDiagnosticStore{}
	diagnostics.startNativeSamples()
	t.Cleanup(diagnostics.stopNativeSamples)
	diagnostics.enqueueNativeSample(job)
	select {
	case <-diagnostics.nativeSampled:
	case <-time.After(5 * time.Second):
		t.Fatal("owned live child was not sampled")
	}
	diagnostics.mu.Lock()
	entries := append([]string(nil), diagnostics.nativeEntries...)
	diagnostics.mu.Unlock()
	if len(entries) != 1 || !strings.Contains(entries[0], "write_bytes=") || strings.Contains(entries[0], "unavailable=") {
		t.Fatalf("asynchronous owned child counters: %q", entries)
	}
	for _, change := range []struct {
		name   string
		modify func(*runner.CommandRuntimeJob)
	}{
		{"recycled-creation", func(job *runner.CommandRuntimeJob) { job.CreatedAt = started.Add(time.Hour) }},
		{"different-image", func(job *runner.CommandRuntimeJob) { job.ExecutablePath = filepath.Join(t.TempDir(), "foreign.exe") }},
		{"foreign-parent", func(job *runner.CommandRuntimeJob) { job.PID = os.Getpid() }},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := job
			change.modify(&changed)
			if _, err := readFixedOperatorWindowsCounters(changed); !errors.Is(err, errFixedOperatorDiagnosticBinding) {
				t.Fatalf("changed native binding was sampled: %v", err)
			}
		})
	}
	job.State = runner.CommandRuntimeJobCompleted
	if sample := fixedOperatorNativeProcessSample(job); sample != "" {
		t.Fatalf("terminal PID sampled: %q", sample)
	}
	t.Run("fixed-manager-live-sample", func(t *testing.T) {
		f, request := newFixedOperatorFixture(t, domain.RunCreated, runner.ControlledCommandGoVersion, 0)
		result, err := f.service.RunOperatorCommand(t.Context(), request)
		if err != nil || result.Job.State != runner.CommandRuntimeJobCompleted || !result.Job.TreeReaped {
			t.Fatalf("fixed diagnostic fixture failed: %+v %v", result, err)
		}
		diagnostics := f.service.store.(*fixedOperatorDiagnosticStore)
		diagnostics.mu.Lock()
		entries := append([]string(nil), diagnostics.nativeEntries...)
		diagnostics.mu.Unlock()
		for _, entry := range entries {
			if strings.Contains(entry, "write_bytes=") && !strings.Contains(entry, "unavailable=") {
				t.Log("live fixed manager native sample:", entry)
				return
			}
		}
		// A short-lived native process can exit before the independent sampler
		// runs. That is diagnostic unavailability, not a command/test failure.
		t.Logf("fixed manager native samples unavailable: %q", entries)
	})
}

func TestFixedOperatorWindowsNativeDiagnosticChild(t *testing.T) {
	if os.Getenv("TRAVERSE_FIXED_DIAGNOSTIC_CHILD") != "1" {
		return
	}
	fmt.Println("diagnostic-ready")
	_, _ = io.ReadFull(os.Stdin, make([]byte, 1))
	os.Exit(0)
}
