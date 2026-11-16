//go:build windows

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"cyberagent-workbench/internal/sandboxtest"

	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	if windowsTestIsSandboxChild() {
		os.Exit(m.Run())
	}
	restore, err := sandboxtest.PrepareHost()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "prepare Windows Local Sandbox host ACLs: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := restore(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "restore Windows Local Sandbox host ACLs: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func windowsTestIsSandboxChild() bool {
	if os.Getenv("TRAVERSE_BOARD_LOCAL_CHILD") != "" ||
		os.Getenv("TRAVERSE_BOARD_TREE_CHILD") != "" ||
		os.Getenv("TRAVERSE_BOARD_LIMIT_CHILD") != "" {
		return true
	}
	// Namespace subprocesses exercise only the OS lock. CI's parent TestMain
	// already owns the host ACL fixture; the child must not prepare it again.
	mode := os.Getenv("SBX_NAMESPACE_LOCK_HELPER")
	return len(os.Args) == 2 && os.Args[1] == "-test.run=^TestSBXNamespaceProcessHelper$" &&
		(mode == "contend" || mode == "exit-without-close")
}

func TestWindowsLocalSandboxReadinessUsesRealAppContainerProcess(t *testing.T) {
	ownerRoot := filepath.Clean(filepath.Join(windowsTestTempDir(t), "owners"))
	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(ownerRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Errorf("close Local Sandbox backend: %v", err)
		}
	})
	readiness, err := backend.Readiness(context.Background(),
		LocalRuntimeCapabilities{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready || readiness.Status != LocalReadinessReady ||
		!readiness.AppContainerToken || !readiness.ZeroNetworkCapabilities ||
		!readiness.CreationTimeJobBinding || readiness.CapabilityGrant {
		windowsBackend, ok := backend.(*windowsLocalBackend)
		if !ok {
			t.Fatalf("unexpected Local Sandbox readiness: %#v", readiness)
		}
		windowsBackend.mu.Lock()
		diagnosticErr := windowsBackend.probeReadinessLocked(context.Background())
		windowsBackend.mu.Unlock()
		var processFailure *localReadinessProcessFailure
		if errors.As(diagnosticErr, &processFailure) {
			t.Fatalf("unexpected Local Sandbox readiness: %#v; diagnostic: %v",
				readiness, processFailure)
		}
		t.Fatalf("unexpected Local Sandbox readiness: %#v; diagnostic_type=%T",
			readiness, diagnosticErr)
	}
}

func TestWindowsLocalSandboxRuntimeGenerationIsInstanceScoped(t *testing.T) {
	base := windowsTestTempDir(t)
	first, err := NewPlatformLocalBackend(WithLocalOwnerRoot(
		filepath.Clean(filepath.Join(base, "owners-first"))))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewPlatformLocalBackend(WithLocalOwnerRoot(
		filepath.Clean(filepath.Join(base, "owners-second"))))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.Generation() == second.Generation() ||
		!validDigest(first.Generation()) || !validDigest(second.Generation()) {
		t.Fatalf("Local Sandbox runtime generation was reusable across instances")
	}
}

func TestWindowsLocalSandboxCloseInvalidatesCachedReadiness(t *testing.T) {
	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(
		filepath.Clean(filepath.Join(windowsTestTempDir(t), "owners"))))
	if err != nil {
		t.Fatal(err)
	}
	ready, err := backend.Readiness(context.Background(),
		LocalRuntimeCapabilities{Enabled: true})
	if err != nil || !ready.Ready {
		t.Fatalf("initial readiness=%#v err=%v", ready, err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	closed, err := backend.Readiness(context.Background(),
		LocalRuntimeCapabilities{Enabled: true})
	if err != nil || closed.Ready || closed.Status != LocalReadinessUnavailable ||
		closed.ReasonCode != LocalReasonProcessUnavailable {
		t.Fatalf("closed backend reused cached readiness=%#v err=%v", closed, err)
	}
}

func TestWindowsLocalSandboxExecutesInDrydockAndDeniesHostAndNetwork(t *testing.T) {
	if os.Getenv("TRAVERSE_BOARD_LOCAL_CHILD") == "1" {
		t.Fatal("parent test unexpectedly entered child mode")
	}
	base := windowsTestTempDir(t)
	ownerRoot := filepath.Clean(filepath.Join(base, "owners"))
	drydock := filepath.Clean(filepath.Join(base, "drydock"))
	outside := filepath.Clean(filepath.Join(base, "outside", "sentinel.txt"))
	if err := os.MkdirAll(drydock, 0o700); err != nil {
		t.Fatal(err)
	}
	userHome := filepath.Join(drydock, ".traverse-board", "home")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	userNote := filepath.Join(userHome, "user-note.txt")
	if err := os.WriteFile(userNote, []byte("existing user content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(outside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("host-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	allPackagesTarget := windowsTestAllApplicationPackagesSentinel(t,
		filepath.Join(base, "all-application-packages"))
	crossDrive := windowsTestCrossDriveSentinel(t, filepath.VolumeName(drydock))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	udpListener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpListener.Close()
	credentialTarget := "TraverseBoard.LocalSandbox.Test." + localFingerprint(t.Name())[:24]
	if err := writeWindowsTestCredential(credentialTarget); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteWindowsTestCredential(credentialTarget) })

	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(ownerRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Errorf("close Local Sandbox backend: %v", err)
		}
	})
	readiness, err := backend.Readiness(context.Background(),
		LocalRuntimeCapabilities{Enabled: true})
	if err != nil || !readiness.Ready {
		t.Fatalf("Local Sandbox not ready: %#v err=%v", readiness, err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	toolchainRoot := windowsTestCanonicalRoot(t, filepath.Dir(executable))
	executable = filepath.Join(toolchainRoot, filepath.Base(executable))
	toolchainSentinel := filepath.Join(toolchainRoot,
		"local-read-only-"+localFingerprint(t.Name())[:16]+".txt")
	if err := os.WriteFile(toolchainSentinel, []byte("toolchain-read-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(toolchainSentinel) })
	drydockDigest, _ := LocalHostPathDigest(drydock)
	toolchainDigest, _ := LocalHostPathDigest(toolchainRoot)
	digest := localFingerprint("test-binding")
	request := LocalRunRequest{Manifest: Manifest{
		ProtocolVersion: ManifestProtocolVersion, Backend: BackendLocal,
		Command: CommandSpec{Executable: "/test-toolchain/" + filepath.ToSlash(filepath.Base(executable)),
			Arguments: []string{"-test.run=^TestWindowsLocalSandboxChildProbe$", "--",
				outside, listener.Addr().String(), udpListener.LocalAddr().String(),
				toolchainSentinel, credentialTarget, crossDrive, allPackagesTarget},
			WorkingDirectory: "/workspace"},
		Mounts:  []Mount{{Source: ".", Target: "/workspace", Access: MountReadWrite}},
		Network: NetworkScope{Mode: "disabled"},
		Resources: ResourceLimits{CPUQuotaMillis: 1000, MemoryBytes: 256 * 1024 * 1024,
			PIDs: 4, MaxOutputBytes: 1024 * 1024},
		Environment: []EnvironmentBinding{{Name: "TRAVERSE_BOARD_LOCAL_CHILD",
			Source: EnvironmentLiteral, Value: "1"}},
		Output: OutputSpec{CaptureStdout: true, CaptureStderr: true,
			Paths: []string{"/workspace/child-proof.txt"}},
		TimeoutSeconds: 20, Cancellation: CancellationSpec{GracePeriodMillis: 100}},
		Binding: LocalExecutionBinding{RunID: "run-local-test", MissionID: "mission-local-test",
			SessionID: "session-local-test", WorkspaceID: "workspace-local-test",
			DrydockID: "drydock-local-test", DrydockRoot: drydock,
			DrydockPathSHA256: drydockDigest, DrydockRootFingerprint: digest,
			DrydockBindingFingerprint: localFingerprint("drydock-binding"),
			DrydockGeneration:         1, PermissionSnapshotID: "permission-local-test",
			PermissionRevision: 1, ProfileSnapshotID: "profile-local-test",
			ProfileRevision: 1, InteractionSnapshotID: "interaction-local-test",
			InteractionRevision: 1, CapabilityGeneration: localFingerprint("capability"),
			LeaseID: "lease-local-test", LeaseGeneration: 1,
			OperationKeySHA256: localFingerprint("operation"),
			RuntimeGeneration:  backend.Generation()},
		ToolchainInputs: []LocalToolchainInput{{ID: "test-toolchain", Root: toolchainRoot,
			VirtualRoot: "/test-toolchain", RootSHA256: toolchainDigest}},
		MaxDiskWriteBytes: 16 * 1024 * 1024}
	result, runErr := backend.Run(context.Background(), request)
	if runErr != nil {
		t.Fatalf("run Local Sandbox: %v\nstdout=%s\nstderr=%s", runErr,
			result.Stdout.Data, result.Stderr.Data)
	}
	if result.Status != LocalExecutionCompleted || result.ExitCode != 0 ||
		!result.TreeReaped || !result.ProfileDeleted || !result.ACLsRestored ||
		result.CapabilityGrant {
		t.Fatalf("unexpected Local Sandbox result: %#v", result)
	}
	if content, err := os.ReadFile(userNote); err != nil || string(content) != "existing user content" {
		t.Fatalf("preexisting project HOME content changed: %q err=%v", content, err)
	}
	if entries, err := os.ReadDir(userHome); err != nil || len(entries) != 1 {
		t.Fatalf("runtime polluted the project HOME: %v err=%v", entries, err)
	}
	if _, err := os.Lstat(filepath.Join(drydock, ".traverse-board", "tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime created project tmp: %v", err)
	}
	if entries, err := os.ReadDir(ownerRoot); err != nil || len(entries) != 1 || entries[0].Name() != localOwnerLockName {
		t.Fatalf("runtime owner/scratch not removed: %v err=%v", entries, err)
	}
	payload, err := os.ReadFile(filepath.Join(drydock, "child-proof.txt"))
	if err != nil || strings.TrimSpace(string(payload)) != "sandboxed" {
		t.Fatalf("Drydock proof missing: %q err=%v", payload, err)
	}
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(150 * time.Millisecond))
	connection, acceptErr := listener.Accept()
	if acceptErr == nil {
		connection.Close()
		t.Fatal("AppContainer reached host loopback listener")
	}
	if !errors.Is(acceptErr, os.ErrDeadlineExceeded) {
		t.Fatalf("unexpected loopback listener error: %v", acceptErr)
	}
	if err := udpListener.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	if count, _, readErr := udpListener.ReadFrom(buffer); readErr == nil {
		t.Fatalf("AppContainer sent %d UDP bytes to host loopback", count)
	} else if !errors.Is(readErr, os.ErrDeadlineExceeded) {
		t.Fatalf("unexpected UDP listener error: %v", readErr)
	}
}

func TestWindowsLocalSandboxStreamsAndClosesStdin(t *testing.T) {
	base := windowsTestTempDir(t)
	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(
		filepath.Join(base, "owners")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if readiness, err := backend.Readiness(t.Context(),
		LocalRuntimeCapabilities{Enabled: true}); err != nil || !readiness.Ready {
		t.Fatalf("Local Sandbox not ready: %#v err=%v", readiness, err)
	}
	drydock := filepath.Join(base, "drydock")
	if err := os.MkdirAll(drydock, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	toolchainRoot := windowsTestCanonicalRoot(t, filepath.Dir(executable))
	request := localWindowsTestRequest(t, backend, drydock, toolchainRoot,
		"/test-toolchain", "/test-toolchain/"+filepath.ToSlash(filepath.Base(executable)),
		[]string{"-test.run=^TestWindowsLocalSandboxStdinChild$"})
	request.Manifest.Environment = []EnvironmentBinding{{Name: "TRAVERSE_BOARD_LOCAL_CHILD",
		Source: EnvironmentLiteral, Value: "stdin"}}
	request.StdinPipe = true
	reader, writer := io.Pipe()
	type completed struct {
		result LocalExecutionResult
		err    error
	}
	done := make(chan completed, 1)
	go func() {
		result, runErr := backend.RunWithStdin(t.Context(), request, reader)
		done <- completed{result: result, err: runErr}
	}()
	for _, part := range []string{"first line\n", "second line\n"} {
		if written, err := writer.Write([]byte(part)); err != nil || written != len(part) {
			t.Fatalf("write Local Sandbox stdin=%d err=%v", written, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-done:
		if value.err != nil || value.result.Status != LocalExecutionCompleted ||
			!strings.HasPrefix(string(value.result.Stdout.Data),
				"first line\nsecond line\n") ||
			!value.result.StdinClosed || !value.result.TreeReaped {
			t.Fatalf("Local Sandbox stdin result=%#v err=%v", value.result, value.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Local Sandbox stdin execution did not terminate")
	}
}

func TestWindowsLocalSandboxCompilesAndTestsInsideDrydock(t *testing.T) {
	base := windowsTestTempDir(t)
	ownerRoot := filepath.Clean(filepath.Join(base, "owners"))
	drydock := filepath.Clean(filepath.Join(base, "drydock"))
	if err := os.MkdirAll(drydock, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":         "module localproof\n\ngo 1.25.0\n",
		"answer.go":      "package localproof\n\nfunc Answer() int { return 42 }\n",
		"answer_test.go": "package localproof\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) { if Answer() != 42 { t.Fatal(Answer()) } }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(drydock, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(ownerRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Errorf("close Local Sandbox backend: %v", err)
		}
	})
	readiness, err := backend.Readiness(context.Background(),
		LocalRuntimeCapabilities{Enabled: true})
	if err != nil || !readiness.Ready {
		t.Fatalf("Local Sandbox not ready: %#v err=%v", readiness, err)
	}
	goRootOutput, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	goRoot := windowsTestCanonicalRoot(t,
		filepath.Clean(strings.TrimSpace(string(goRootOutput))))
	request := localWindowsTestRequest(t, backend, drydock, goRoot, "/go-toolchain",
		"/go-toolchain/bin/go.exe", []string{"test", "./...", "-count=1"})
	request.Manifest.Resources = ResourceLimits{CPUQuotaMillis: 4000,
		MemoryBytes: 3 * 1024 * 1024 * 1024, PIDs: 128,
		MaxOutputBytes: 2 * 1024 * 1024}
	request.Manifest.Environment = []EnvironmentBinding{
		{Name: "GOMAXPROCS", Source: EnvironmentLiteral, Value: "2"},
		{Name: "GOFLAGS", Source: EnvironmentLiteral, Value: "-p=2"},
	}
	request.Manifest.TimeoutSeconds = 60
	request.MaxDiskWriteBytes = 512 * 1024 * 1024
	result, runErr := backend.Run(context.Background(), request)
	if runErr != nil {
		t.Fatalf("go test in Local Sandbox: %v\nstdout=%s\nstderr=%s", runErr,
			result.Stdout.Data, result.Stderr.Data)
	}
	if result.Status != LocalExecutionCompleted || result.ExitCode != 0 ||
		!strings.Contains(string(result.Stdout.Data), "ok") {
		t.Fatalf("unexpected go test result: %#v stdout=%s stderr=%s", result,
			result.Stdout.Data, result.Stderr.Data)
	}
}

func TestWindowsLocalSandboxTimeoutAndCancellationReapProcessTree(t *testing.T) {
	if os.Getenv("TRAVERSE_BOARD_TREE_CHILD") != "" {
		t.Fatal("parent test unexpectedly entered tree child mode")
	}
	for _, testCase := range []struct {
		name   string
		cancel bool
	}{
		{name: "timeout"},
		{name: "cancellation", cancel: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// Keep the private owner/profile root comparable to the supported
			// application-data layout; t.TempDir's complete test name can make
			// Windows nested process initialization depend on unrelated path length.
			base, err := os.MkdirTemp("", "local-tree-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(base); err != nil {
					t.Error(err)
				}
			})
			base = windowsTestCanonicalRoot(t, base)
			ownerRoot := filepath.Clean(filepath.Join(base, "owners"))
			drydock := filepath.Clean(filepath.Join(base, "drydock"))
			if err := os.MkdirAll(drydock, 0o700); err != nil {
				t.Fatal(err)
			}
			backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(ownerRoot))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := backend.Close(); err != nil {
					t.Errorf("close Local Sandbox backend: %v", err)
				}
			})
			readiness, err := backend.Readiness(context.Background(),
				LocalRuntimeCapabilities{Enabled: true})
			if err != nil || !readiness.Ready {
				t.Fatalf("Local Sandbox not ready: %#v err=%v", readiness, err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			toolchainRoot := windowsTestCanonicalRoot(t, filepath.Dir(executable))
			executable = filepath.Join(toolchainRoot, filepath.Base(executable))
			pidPath := filepath.Join(drydock, "grandchild.pid")
			request := localWindowsTestRequest(t, backend, drydock,
				toolchainRoot, "/test-toolchain",
				"/test-toolchain/"+filepath.ToSlash(filepath.Base(executable)),
				[]string{"-test.run=^TestWindowsLocalSandboxTreeChild$", "--", pidPath})
			request.Manifest.Environment = []EnvironmentBinding{{
				Name: "TRAVERSE_BOARD_TREE_CHILD", Source: EnvironmentLiteral, Value: "root"}}
			// Both processes inherit Go race instrumentation in a -race run. The
			// shared 256 MiB fixture budget stalls nested process startup even with
			// the legacy HOME layout; memory-limit behavior has its own tests.
			request.Manifest.Resources.MemoryBytes = 1024 * 1024 * 1024
			request.Manifest.TimeoutSeconds = 1
			type runResponse struct {
				result LocalExecutionResult
				err    error
			}
			ctx := context.Background()
			var cancel context.CancelFunc
			if testCase.cancel {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				request.Manifest.TimeoutSeconds = 20
			}
			response := make(chan runResponse, 1)
			go func() {
				result, runErr := backend.Run(ctx, request)
				response <- runResponse{result: result, err: runErr}
			}()
			if testCase.cancel {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, statErr := os.Stat(pidPath); statErr == nil {
						break
					}
					if time.Now().After(deadline) {
						cancel()
						failed := <-response
						t.Fatalf("Local Sandbox grandchild did not start before cancellation: err=%v stdout=%s stderr=%s", failed.err, failed.result.Stdout.Data, failed.result.Stderr.Data)
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
			}
			var completed runResponse
			select {
			case completed = <-response:
			case <-time.After(10 * time.Second):
				t.Fatal("Local Sandbox Run did not terminate")
			}
			if completed.err == nil || !completed.result.TreeReaped ||
				!completed.result.ProfileDeleted || !completed.result.ACLsRestored {
				t.Fatalf("unexpected terminal result: %#v err=%v", completed.result, completed.err)
			}
			if testCase.cancel {
				if completed.result.Status != LocalExecutionCancelled ||
					!completed.result.Cancelled || completed.result.TimedOut {
					t.Fatalf("unexpected cancellation result: %#v", completed.result)
				}
			} else if completed.result.Status != LocalExecutionTimedOut ||
				!completed.result.TimedOut || completed.result.Cancelled {
				t.Fatalf("unexpected timeout result: %#v", completed.result)
			}
			payload, err := os.ReadFile(pidPath)
			if err != nil {
				t.Fatalf("%v; stdout=%s stderr=%s", err, completed.result.Stdout.Data, completed.result.Stderr.Data)
			}
			pid, err := strconv.ParseUint(strings.TrimSpace(string(payload)), 10, 32)
			if err != nil {
				t.Fatal(err)
			}
			assertWindowsProcessExited(t, uint32(pid))
		})
	}
}

func TestWindowsLocalSandboxEnforcesOutputAndDiskWriteLimits(t *testing.T) {
	if os.Getenv("TRAVERSE_BOARD_LIMIT_CHILD") != "" {
		t.Fatal("parent test unexpectedly entered limit child mode")
	}
	for _, testCase := range []struct {
		name string
		mode string
	}{
		{name: "output", mode: "output"},
		{name: "disk-write", mode: "disk"},
		{name: "private-temp-write", mode: "scratch"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			base := windowsTestTempDir(t)
			ownerRoot := filepath.Clean(filepath.Join(base, "owners"))
			drydock := filepath.Clean(filepath.Join(base, "drydock"))
			if err := os.MkdirAll(drydock, 0o700); err != nil {
				t.Fatal(err)
			}
			backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(ownerRoot))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = backend.Close() })
			readiness, err := backend.Readiness(context.Background(),
				LocalRuntimeCapabilities{Enabled: true})
			if err != nil || !readiness.Ready {
				t.Fatalf("Local Sandbox not ready: %#v err=%v", readiness, err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			toolchainRoot := windowsTestCanonicalRoot(t, filepath.Dir(executable))
			executable = filepath.Join(toolchainRoot, filepath.Base(executable))
			request := localWindowsTestRequest(t, backend, drydock,
				toolchainRoot, "/test-toolchain",
				"/test-toolchain/"+filepath.ToSlash(filepath.Base(executable)),
				[]string{"-test.run=^TestWindowsLocalSandboxLimitChild$"})
			request.Manifest.Environment = []EnvironmentBinding{{
				Name: "TRAVERSE_BOARD_LIMIT_CHILD", Source: EnvironmentLiteral,
				Value: testCase.mode}}
			request.Manifest.Resources.MaxOutputBytes = 32 * 1024
			request.MaxDiskWriteBytes = 64 * 1024
			if testCase.mode == "output" {
				request.MaxDiskWriteBytes = 8 * 1024 * 1024
			}
			result, runErr := backend.Run(context.Background(), request)
			if runErr == nil || result.Status != LocalExecutionFailed ||
				!result.TreeReaped || !result.ProfileDeleted || !result.ACLsRestored {
				t.Fatalf("limit did not fail closed: %#v err=%v", result, runErr)
			}
			if testCase.mode == "output" {
				if !errors.Is(runErr, ErrLocalSandboxOutputLimit) ||
					!result.OutputLimitExceeded || result.WriteLimitExceeded {
					t.Fatalf("output limit evidence mismatch: %#v err=%v", result, runErr)
				}
			} else if !errors.Is(runErr, ErrLocalSandboxWriteLimit) ||
				!result.WriteLimitExceeded {
				t.Fatalf("disk write limit evidence mismatch: %#v err=%v", result, runErr)
			}
		})
	}
}

func TestWindowsLocalSandboxLimitChild(t *testing.T) {
	switch os.Getenv("TRAVERSE_BOARD_LIMIT_CHILD") {
	case "":
		t.Skip("Local Sandbox resource limit child helper")
	case "output":
		payload := []byte(strings.Repeat("x", 4096))
		for index := 0; index < 512; index++ {
			_, _ = os.Stdout.Write(payload)
		}
	case "disk", "scratch":
		path := "large-artifact.bin"
		if os.Getenv("TRAVERSE_BOARD_LIMIT_CHILD") == "scratch" {
			path = filepath.Join(os.Getenv("TEMP"), path)
		}
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte(strings.Repeat("d", 4096))
		for index := 0; index < 512; index++ {
			if _, err := file.Write(payload); err != nil {
				file.Close()
				t.Fatal(err)
			}
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown Local Sandbox limit child mode")
	}
}

func TestWindowsLocalSandboxRecoversOwnerAfterSimulatedAppCrashWithoutPIDAuthority(t *testing.T) {
	testWindowsLocalSandboxOwnerRecovery(t, false, false)
}

func testWindowsLocalSandboxOwnerRecovery(t *testing.T, instrumentation, legacy bool) {
	t.Helper()
	base := windowsTestTempDir(t)
	ownerRoot := filepath.Clean(filepath.Join(base, "owners"))
	drydock := filepath.Clean(filepath.Join(base, "drydock"))
	if err := os.MkdirAll(drydock, 0o700); err != nil {
		t.Fatal(err)
	}
	existingDirectory := filepath.Clean(filepath.Join(drydock, "existing"))
	if err := os.MkdirAll(existingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	firstBackend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(ownerRoot))
	if err != nil {
		t.Fatal(err)
	}
	first := firstBackend.(*windowsLocalBackend)
	first.mu.Lock()
	root, err := pinLocalRoot(drydock)
	if err != nil {
		first.mu.Unlock()
		t.Fatal(err)
	}
	profile, err := prepareLocalProfileWithInstrumentation(localFingerprint(t.Name()), instrumentation)
	if err != nil {
		root.close()
		first.mu.Unlock()
		t.Fatal(err)
	}
	if err := createLocalAppContainerDirectories(drydock, profile.name); err != nil {
		root.close()
		first.mu.Unlock()
		t.Fatal(err)
	}
	snapshot, err := captureLocalSecurity(root)
	if err != nil {
		root.close()
		first.mu.Unlock()
		t.Fatal(err)
	}
	existingRoot, err := pinLocalRoot(existingDirectory)
	if err != nil {
		root.close()
		first.mu.Unlock()
		t.Fatal(err)
	}
	existingSnapshot, err := captureLocalSecurity(existingRoot)
	existingRoot.close()
	if err != nil {
		root.close()
		first.mu.Unlock()
		t.Fatal(err)
	}
	owner := localOwnerRecord{ProtocolVersion: localPreviousOwnerProtocolVersion,
		PolicyVersion:      localPreviousPolicyVersion,
		Instrumentation:    instrumentation,
		OwnerID:            localFingerprint("crash-owner", profile.name),
		BindingFingerprint: localFingerprint("crash-binding", t.Name()),
		ProfileName:        profile.name, ProfileSID: profile.sid.String(),
		Snapshots: []localSecuritySnapshot{snapshot}, CreatedAt: time.Now().UTC()}
	owner.seal()
	if legacy {
		owner.ProtocolVersion, owner.PolicyVersion = localLegacyOwnerProtocolVersion, ""
		owner.Instrumentation = false
		// Construct the old v1 fingerprint independently. This record models a
		// crash before the binary upgrade, not a new v2 writer downgrading itself.
		parts := []string{"local_sandbox_owner.v1", owner.OwnerID, owner.BindingFingerprint,
			owner.ProfileName, owner.ProfileSID, owner.CreatedAt.UTC().Format(time.RFC3339Nano)}
		for _, previous := range owner.Snapshots {
			parts = append(parts, previous.PathSHA256, previous.RootIdentity, previous.DACLSDDL,
				fmt.Sprint(previous.DACLProtected), previous.LabelSDDL, fmt.Sprint(previous.SACLProtected))
		}
		owner.Fingerprint = localFingerprint(parts...)
		payload, marshalErr := json.Marshal(owner)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		err = os.WriteFile(filepath.Join(ownerRoot, owner.OwnerID+".json"), payload, 0o600)
	} else {
		// The v2 byte contract predates private scratch. Construct its digest
		// independently instead of asking the current writer to downgrade.
		parts := []string{"local_sandbox_owner.v2", owner.OwnerID, owner.BindingFingerprint,
			owner.ProfileName, owner.ProfileSID, owner.CreatedAt.UTC().Format(time.RFC3339Nano),
			"windows_appcontainer_policy.v2", fmt.Sprint(owner.Instrumentation)}
		for _, previous := range owner.Snapshots {
			parts = append(parts, previous.PathSHA256, previous.RootIdentity, previous.DACLSDDL,
				fmt.Sprint(previous.DACLProtected), previous.LabelSDDL, fmt.Sprint(previous.SACLProtected))
		}
		owner.Fingerprint = localFingerprint(parts...)
		payload, marshalErr := json.Marshal(owner)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		err = os.WriteFile(filepath.Join(ownerRoot, owner.OwnerID+".json"), payload, 0o600)
	}
	if err != nil {
		root.close()
		first.mu.Unlock()
		t.Fatal(err)
	}
	if err := materializeLocalProfile(profile); err != nil {
		root.close()
		first.mu.Unlock()
		t.Fatal(err)
	}
	if err := grantLocalRoot(snapshot, profile.sid, true, true); err != nil {
		root.close()
		first.mu.Unlock()
		t.Fatal(err)
	}
	root.close()
	// Simulate abrupt app exit: only the kernel owner lock disappears. No PID is
	// persisted or consulted by the next backend instance.
	if err := windows.CloseHandle(first.lock); err != nil {
		first.mu.Unlock()
		t.Fatal(err)
	}
	first.lock = 0
	first.closed = true
	first.mu.Unlock()

	secondBackend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(ownerRoot))
	if err != nil {
		t.Fatal(err)
	}
	second := secondBackend.(*windowsLocalBackend)
	if second.initErr != nil {
		t.Fatalf("startup owner recovery failed: %v", second.initErr)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("close recovered backend: %v", err)
		}
	})
	entries, err := os.ReadDir(ownerRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("stale owner journal survived recovery: %s", entry.Name())
		}
	}
	profileDirectory, err := localAppContainerDirectory(drydock, profile.name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profileDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale AppContainer directory survived recovery: %v", err)
	}
	restoredRoot, err := pinLocalRoot(drydock)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := captureLocalSecurity(restoredRoot)
	restoredRoot.close()
	if err != nil {
		t.Fatal(err)
	}
	if !windowsTestSameDACL(restored.DACLSDDL, snapshot.DACLSDDL) ||
		restored.DACLProtected != snapshot.DACLProtected ||
		restored.LabelSDDL != snapshot.LabelSDDL {
		t.Fatalf("startup recovery mismatch: dacl=%t protected=%t label_before=%q label_after=%q",
			windowsTestSameDACL(restored.DACLSDDL, snapshot.DACLSDDL),
			restored.DACLProtected == snapshot.DACLProtected,
			snapshot.LabelSDDL, restored.LabelSDDL)
	}
	restoredExistingRoot, err := pinLocalRoot(existingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	restoredExisting, err := captureLocalSecurity(restoredExistingRoot)
	restoredExistingRoot.close()
	if err != nil {
		t.Fatal(err)
	}
	if !windowsTestSameDACL(restoredExisting.DACLSDDL, existingSnapshot.DACLSDDL) ||
		restoredExisting.DACLProtected != existingSnapshot.DACLProtected ||
		restoredExisting.LabelSDDL != existingSnapshot.LabelSDDL {
		t.Fatal("startup recovery left inherited AppContainer authority below Drydock")
	}
	if err := materializeLocalProfile(profile); err != nil {
		t.Fatalf("recovered AppContainer profile still exists: %v", err)
	}
	if err := deleteLocalProfile(profile.name); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsLocalSandboxRejectsPreexistingReparseEscapeBeforeStartingProcess(t *testing.T) {
	base := windowsTestTempDir(t)
	ownerRoot := filepath.Clean(filepath.Join(base, "owners"))
	drydock := filepath.Clean(filepath.Join(base, "drydock"))
	outside := filepath.Clean(filepath.Join(base, "outside"))
	if err := os.MkdirAll(drydock, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(drydock, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		command := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", escape, outside)
		if output, junctionErr := command.CombinedOutput(); junctionErr != nil {
			t.Skipf("reparse points are unavailable on this Windows host: symlink=%v junction=%v output=%s",
				err, junctionErr, output)
		}
	}
	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(ownerRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	readiness, err := backend.Readiness(context.Background(),
		LocalRuntimeCapabilities{Enabled: true})
	if err != nil || !readiness.Ready {
		t.Fatalf("Local Sandbox not ready: %#v err=%v", readiness, err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable = filepath.Clean(executable)
	request := localWindowsTestRequest(t, backend, drydock, filepath.Dir(executable),
		"/test-toolchain", "/test-toolchain/"+filepath.ToSlash(filepath.Base(executable)),
		[]string{"-test.run=^TestWindowsLocalSandboxChildProbe$"})
	result, runErr := backend.Run(context.Background(), request)
	if runErr == nil || !errors.Is(runErr, ErrLocalSandboxBoundary) ||
		!result.StartedAt.IsZero() || !result.TreeReaped || result.ExitCode != 125 {
		t.Fatalf("reparse escape was not rejected before process start: %#v err=%v",
			result, runErr)
	}
}

func TestWindowsLocalSandboxRejectsPreexistingHardlinkEscapeBeforeACLGrant(t *testing.T) {
	base := windowsTestTempDir(t)
	ownerRoot := filepath.Clean(filepath.Join(base, "owners"))
	drydock := filepath.Clean(filepath.Join(base, "drydock"))
	outside := filepath.Clean(filepath.Join(base, "outside", "sentinel.txt"))
	if err := os.MkdirAll(drydock, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(outside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("host-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(drydock, "outside-hardlink.txt")); err != nil {
		t.Fatal(err)
	}
	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(ownerRoot))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable = filepath.Clean(executable)
	request := localWindowsTestRequest(t, backend, drydock, filepath.Dir(executable),
		"/test-toolchain", "/test-toolchain/"+filepath.ToSlash(filepath.Base(executable)),
		[]string{"-test.run=^TestWindowsLocalSandboxChildProbe$"})
	if result, err := backend.Run(context.Background(), request); !errors.Is(err,
		ErrLocalSandboxBoundary) || !result.TreeReaped || result.ExitCode != 125 || !result.StartedAt.IsZero() {
		t.Fatalf("preexisting hardlink was not rejected before dispatch: result=%+v error=%v", result, err)
	}
	payload, err := os.ReadFile(outside)
	if err != nil || string(payload) != "host-secret" {
		t.Fatalf("outside hardlink target changed: %q err=%v", payload, err)
	}
}

func TestWindowsLocalSandboxRequestRejectsUNCDeviceAndBindingDrift(t *testing.T) {
	base := windowsTestTempDir(t)
	drydock := filepath.Clean(filepath.Join(base, "drydock"))
	toolchain := filepath.Clean(filepath.Join(base, "toolchain"))
	otherToolchain := filepath.Clean(filepath.Join(base, "other-toolchain"))
	for _, root := range []string{drydock, toolchain, otherToolchain} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	drydockDigest, _ := LocalHostPathDigest(drydock)
	toolchainDigest, _ := LocalHostPathDigest(toolchain)
	otherToolchainDigest, _ := LocalHostPathDigest(otherToolchain)
	digest := localFingerprint(t.Name())
	valid := LocalRunRequest{Manifest: Manifest{ProtocolVersion: ManifestProtocolVersion,
		Backend: BackendLocal,
		Command: CommandSpec{Executable: "/tools/tool.exe", WorkingDirectory: "/workspace"},
		Mounts:  []Mount{{Source: ".", Target: "/workspace", Access: MountReadWrite}},
		Network: NetworkScope{Mode: "disabled"}, Resources: ResourceLimits{
			CPUQuotaMillis: 1000, MemoryBytes: 64 * 1024 * 1024, PIDs: 2,
			MaxOutputBytes: 1024}, Output: OutputSpec{CaptureStdout: true},
		TimeoutSeconds: 10, Cancellation: CancellationSpec{}},
		Binding: LocalExecutionBinding{RunID: "run", MissionID: "mission", SessionID: "session",
			WorkspaceID: "workspace", DrydockID: "drydock", DrydockRoot: drydock,
			DrydockPathSHA256: drydockDigest, DrydockRootFingerprint: digest,
			DrydockBindingFingerprint: digest, DrydockGeneration: 1,
			PermissionSnapshotID: "permission", PermissionRevision: 1,
			ProfileSnapshotID: "profile", ProfileRevision: 1,
			InteractionSnapshotID: "interaction", InteractionRevision: 1,
			CapabilityGeneration: digest, LeaseID: "lease", LeaseGeneration: 1,
			OperationKeySHA256: digest, RuntimeGeneration: digest},
		ToolchainInputs: []LocalToolchainInput{{ID: "tools", Root: toolchain,
			VirtualRoot: "/tools", RootSHA256: toolchainDigest}}, MaxDiskWriteBytes: 4096}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid strict request rejected: %v", err)
	}
	spaced := valid
	spaced.Manifest.Command.WorkingDirectory = "/workspace/ project"
	if err := spaced.Validate(); err != nil {
		t.Fatalf("leading space in a working directory was refused: %v", err)
	}
	for name, mutate := range map[string]func(*LocalRunRequest){
		"working directory alias": func(value *LocalRunRequest) {
			value.Manifest.Command.WorkingDirectory = "/workspace/project "
		},
		"working directory ancestor alias": func(value *LocalRunRequest) {
			value.Manifest.Command.WorkingDirectory = "/workspace/project./nested"
		},
		"UNC root": func(value *LocalRunRequest) {
			value.Binding.DrydockRoot = `\\server\share\drydock`
			value.Binding.DrydockPathSHA256 = localHostPathDigest(value.Binding.DrydockRoot)
		},
		"device root": func(value *LocalRunRequest) {
			value.Binding.DrydockRoot = `\\.\C:\drydock`
			value.Binding.DrydockPathSHA256 = localHostPathDigest(value.Binding.DrydockRoot)
		},
		"extended root": func(value *LocalRunRequest) {
			value.Binding.DrydockRoot = `\\?\C:\drydock`
			value.Binding.DrydockPathSHA256 = localHostPathDigest(value.Binding.DrydockRoot)
		},
		"permission revision drift": func(value *LocalRunRequest) {
			value.Binding.PermissionRevision = 0
		},
		"runtime generation drift": func(value *LocalRunRequest) {
			value.Binding.RuntimeGeneration = "changed"
		},
		"network enabled": func(value *LocalRunRequest) {
			value.Manifest.Network.Mode = "allowlist"
			value.Manifest.Network.AllowedTargets = []string{"127.0.0.1"}
		},
		"credential environment": func(value *LocalRunRequest) {
			value.Manifest.Environment = []EnvironmentBinding{{Name: "AWS_ACCESS_KEY_ID",
				Source: EnvironmentLiteral, Value: "blocked"}}
		},
		"nested virtual toolchain root": func(value *LocalRunRequest) {
			value.ToolchainInputs = append(value.ToolchainInputs, LocalToolchainInput{
				ID: "nested-tools", Root: otherToolchain, VirtualRoot: "/tools/sub",
				RootSHA256: otherToolchainDigest})
		},
		"ancestor virtual toolchain root": func(value *LocalRunRequest) {
			value.ToolchainInputs[0].VirtualRoot = "/tools/sub"
			value.ToolchainInputs = append(value.ToolchainInputs, LocalToolchainInput{
				ID: "ancestor-tools", Root: otherToolchain, VirtualRoot: "/tools",
				RootSHA256: otherToolchainDigest})
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			changed.Manifest = valid.Manifest
			changed.Manifest.Mounts = append([]Mount(nil), valid.Manifest.Mounts...)
			changed.ToolchainInputs = append([]LocalToolchainInput(nil), valid.ToolchainInputs...)
			mutate(&changed)
			if err := changed.Validate(); err == nil {
				t.Fatalf("unsafe Local Sandbox request was accepted: %#v", changed)
			}
		})
	}
}

func TestWindowsLocalSandboxRejectsUserAndCredentialBoundaryRoots(t *testing.T) {
	profile, err := windows.KnownFolderPath(windows.FOLDERID_Profile,
		windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	localAppData, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData,
		windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	if !localRootContainsUserBoundary(filepath.Clean(profile)) ||
		!localRootContainsUserBoundary(filepath.Clean(filepath.Dir(profile))) ||
		!localRootOverlapsCredentialDirectory(filepath.Clean(filepath.Join(profile, ".ssh"))) ||
		!localRootCrossesSensitiveBoundary(filepath.Clean(filepath.Join(profile, ".ssh"))) ||
		!localRootOverlapsCredentialDirectory(filepath.Clean(filepath.Join(localAppData,
			"Microsoft", "Edge", "User Data"))) {
		t.Fatal("user home or credential-bearing root was accepted as a Local Sandbox boundary")
	}
	toolchainChild := filepath.Clean(filepath.Join(profile, "reviewed-toolchains", "go"))
	if localRootContainsUserBoundary(toolchainChild) ||
		localRootOverlapsCredentialDirectory(toolchainChild) ||
		localRootCrossesSensitiveBoundary(toolchainChild) {
		t.Fatal("an exact non-sensitive toolchain subtree was conflated with the user home")
	}
}

func TestWindowsLocalSandboxTreeChild(t *testing.T) {
	mode := os.Getenv("TRAVERSE_BOARD_TREE_CHILD")
	if mode == "" {
		t.Skip("Local Sandbox tree child helper")
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || len(os.Args) != separator+2 {
		t.Fatalf("invalid tree child arguments: %#v", os.Args)
	}
	pidPath := os.Args[separator+1]
	if mode == "grandchild" {
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWindowsLocalSandboxTreeChild$",
		"--", pidPath)
	command.Env = append(os.Environ(), "TRAVERSE_BOARD_TREE_CHILD=grandchild")
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Second)
}

func assertWindowsProcessExited(t *testing.T, pid uint32) {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatalf("open former Local Sandbox process %d: %v", pid, err)
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("Local Sandbox process %d leaked: status=%d err=%v", pid, status, err)
	}
}

func TestWindowsLocalSandboxChildProbe(t *testing.T) {
	if os.Getenv("TRAVERSE_BOARD_LOCAL_CHILD") != "1" {
		t.Skip("Local Sandbox child helper")
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || len(os.Args) != separator+8 {
		t.Fatalf("invalid child arguments: %#v", os.Args)
	}
	outside, loopback, udpLoopback := os.Args[separator+1], os.Args[separator+2],
		os.Args[separator+3]
	toolchainSentinel, credentialTarget := os.Args[separator+4], os.Args[separator+5]
	crossDrive := os.Args[separator+6]
	allPackagesTarget := os.Args[separator+7]
	if err := os.WriteFile("child-proof.txt", []byte("sandboxed\n"), 0o600); err != nil {
		t.Fatalf("Drydock write failed: %v", err)
	}
	profileFolder, err := windowsTestCurrentAppContainerFolder()
	if err != nil {
		t.Fatalf("resolve AppContainer profile folder: %v", err)
	}
	if err := os.MkdirAll(profileFolder, 0o700); err == nil {
		t.Fatal("recreated undeclared AppContainer profile folder")
	}
	profileProbe := filepath.Join(profileFolder, "undeclared-write-probe.txt")
	if err := os.WriteFile(profileProbe, []byte("blocked"), 0o600); err == nil {
		_ = os.Remove(profileProbe)
		t.Fatal("wrote to undeclared AppContainer profile folder")
	}
	stdin := make([]byte, 1)
	if count, err := os.Stdin.Read(stdin); count != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("stdin was not closed: count=%d err=%v", count, err)
	}
	if payload, err := os.ReadFile(outside); err == nil {
		t.Fatalf("read host sentinel outside Drydock: %q", payload)
	}
	if crossDrive != "" {
		if payload, err := os.ReadFile(crossDrive); err == nil {
			t.Fatalf("read host sentinel on another drive: %q", payload)
		}
	}
	if payload, err := os.ReadFile(allPackagesTarget); err == nil {
		t.Fatalf("read global ALL APPLICATION PACKAGES sentinel: %q", payload)
	}
	if payload, err := os.ReadFile(toolchainSentinel); err != nil ||
		string(payload) != "toolchain-read-only" {
		t.Fatalf("read-only toolchain input is unavailable: %q err=%v", payload, err)
	}
	if file, err := os.OpenFile(toolchainSentinel, os.O_WRONLY|os.O_TRUNC, 0); err == nil {
		file.Close()
		t.Fatal("wrote to read-only toolchain input")
	}
	if payload, err := os.ReadFile(`\\.\PhysicalDrive0`); err == nil {
		t.Fatalf("read Windows device path: %q", payload)
	}
	volume := filepath.VolumeName(outside)
	if len(volume) == 2 {
		unc := `\\localhost\` + strings.TrimSuffix(volume, ":") + `$\` +
			strings.TrimPrefix(strings.TrimPrefix(outside, volume), `\`)
		if payload, err := os.ReadFile(unc); err == nil {
			t.Fatalf("read host sentinel through UNC path: %q", payload)
		}
	}
	dialer := net.Dialer{Timeout: 750 * time.Millisecond}
	if connection, err := dialer.Dial("tcp4", loopback); err == nil {
		connection.Close()
		t.Fatal("connected to host loopback")
	}
	if _, err := net.DefaultResolver.LookupHost(context.Background(), "example.com"); err == nil {
		t.Fatal("resolved public DNS without network capability")
	}
	if connection, err := net.DialTimeout("udp4", udpLoopback, 500*time.Millisecond); err == nil {
		_, _ = connection.Write([]byte("blocked"))
		connection.Close()
	}
	if windowsTestCredentialReadable(credentialTarget) {
		t.Fatal("read host Credential Manager material")
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "SSH_AGENT_PID",
		"NPM_TOKEN"} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("inherited credential/proxy environment %s", name)
		}
	}
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA",
		"TEMP", "TMP"} {
		if value := os.Getenv(name); value == "" || localRootsOverlap(value, working) || !localHostPathWithin(value, filepath.Dir(os.Getenv("HOME"))) {
			t.Fatalf("%s is not isolated in private runtime storage: value=%q working=%q",
				name, value, working)
		}
	}
	for _, name := range []string{"HOME", "TEMP"} {
		if err := os.WriteFile(filepath.Join(os.Getenv(name), "runtime-state.txt"), []byte("scratch"), 0o600); err != nil {
			t.Fatalf("private %s is not writable: %v", name, err)
		}
	}
	journalRoot := filepath.Dir(filepath.Dir(os.Getenv("HOME")))
	if _, err := os.ReadDir(journalRoot); err == nil {
		t.Fatal("sandbox could enumerate the private owner journal")
	}
}

func TestWindowsLocalSandboxStdinChild(t *testing.T) {
	if os.Getenv("TRAVERSE_BOARD_LOCAL_CHILD") != "stdin" {
		t.Skip("Local Sandbox stdin child helper")
	}
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.Write(payload)
}

func windowsTestCurrentAppContainerFolder() (string, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY,
		&token); err != nil {
		return "", err
	}
	defer token.Close()
	containerInfo, err := localTokenInformation(token, localTokenAppContainerSID)
	if err != nil || len(containerInfo) == 0 {
		return "", errors.Join(err, ErrLocalSandboxBoundary)
	}
	sid := *(**windows.SID)(unsafe.Pointer(&containerInfo[0]))
	value, err := localAppContainerFolderPath(sid)
	runtime.KeepAlive(containerInfo)
	return value, err
}

func windowsTestTempDir(t *testing.T) string {
	t.Helper()
	return windowsTestCanonicalRoot(t, t.TempDir())
}

func windowsTestCanonicalRoot(t *testing.T, value string) string {
	t.Helper()
	raw := filepath.Clean(value)
	pointer, err := windows.UTF16PtrFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	resolved, err := localFinalPath(handle)
	if err != nil || !validLocalHostRoot(resolved) {
		t.Fatalf("resolve Windows test root %q: %v", raw, err)
	}
	return resolved
}

func windowsTestSameDACL(first, second string) bool {
	firstACEs, firstOK := windowsTestDACLACEs(first)
	secondACEs, secondOK := windowsTestDACLACEs(second)
	if !firstOK || !secondOK || len(firstACEs) != len(secondACEs) {
		return false
	}
	for ace, count := range firstACEs {
		if secondACEs[ace] != count {
			return false
		}
	}
	return true
}

func windowsTestDACLACEs(value string) (map[string]int, bool) {
	descriptor, err := windows.SecurityDescriptorFromString(value)
	if err != nil {
		return nil, false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return nil, false
	}
	aces := make(map[string]int, dacl.AceCount)
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil ||
			ace.Header.AceSize < uint16(unsafe.Sizeof(ace.Header)) {
			return nil, false
		}
		bytes := unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize))
		aces[string(bytes)]++
	}
	return aces, true
}

func windowsTestCrossDriveSentinel(t *testing.T, excludedVolume string) string {
	t.Helper()
	for drive := 'C'; drive <= 'Z'; drive++ {
		volume := string(drive) + ":"
		if strings.EqualFold(volume, excludedVolume) {
			continue
		}
		root := volume + `\`
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		directory, err := os.MkdirTemp(root, "traverse-local-cross-drive-")
		if err != nil {
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(directory) })
		pathValue := filepath.Join(directory, "sentinel.txt")
		if err := os.WriteFile(pathValue, []byte("cross-drive-secret"), 0o600); err != nil {
			continue
		}
		return pathValue
	}
	return ""
}

func windowsTestAllApplicationPackagesSentinel(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	pinned, err := pinLocalRoot(filepath.Clean(root))
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.close()
	snapshot, err := captureLocalSecurity(pinned)
	if err != nil {
		t.Fatal(err)
	}
	allPackagesSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAnyPackageSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := grantLocalRoot(snapshot, allPackagesSID, false, false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "global-appcontainer-readable.txt")
	if err := os.WriteFile(target, []byte("global-appcontainer-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	security, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	sddl := ""
	if security != nil {
		sddl = security.String()
	}
	if err != nil || (!strings.Contains(sddl, allPackagesSID.String()) &&
		!strings.Contains(sddl, ";;;AC)")) {
		t.Fatalf("test sentinel is not readable by ALL APPLICATION PACKAGES: %v", err)
	}
	return target
}

type windowsTestCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	windowsCredWriteProc  = windows.NewLazySystemDLL("advapi32.dll").NewProc("CredWriteW")
	windowsCredReadProc   = windows.NewLazySystemDLL("advapi32.dll").NewProc("CredReadW")
	windowsCredDeleteProc = windows.NewLazySystemDLL("advapi32.dll").NewProc("CredDeleteW")
	windowsCredFreeProc   = windows.NewLazySystemDLL("advapi32.dll").NewProc("CredFree")
)

func writeWindowsTestCredential(target string) error {
	targetPointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	userPointer, err := windows.UTF16PtrFromString("TraverseBoardLocalSandboxTest")
	if err != nil {
		return err
	}
	blob := []byte("credential-material-never-exposed")
	credential := windowsTestCredential{Type: 1, TargetName: targetPointer,
		CredentialBlobSize: uint32(len(blob)), CredentialBlob: &blob[0],
		Persist: 1, UserName: userPointer}
	success, _, callErr := windowsCredWriteProc.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(targetPointer)
	runtime.KeepAlive(userPointer)
	runtime.KeepAlive(blob)
	if success == 0 {
		return callErr
	}
	return nil
}

func deleteWindowsTestCredential(target string) {
	pointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return
	}
	_, _, _ = windowsCredDeleteProc.Call(uintptr(unsafe.Pointer(pointer)), 1, 0)
}

func windowsTestCredentialReadable(target string) bool {
	pointer, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return false
	}
	var credential *windowsTestCredential
	success, _, _ := windowsCredReadProc.Call(uintptr(unsafe.Pointer(pointer)), 1, 0,
		uintptr(unsafe.Pointer(&credential)))
	if success == 0 || credential == nil {
		return false
	}
	windowsCredFreeProc.Call(uintptr(unsafe.Pointer(credential)))
	return true
}

func localWindowsTestRequest(t *testing.T, backend LocalBackend, drydock,
	toolchainRoot, virtualRoot, executable string, arguments []string,
) LocalRunRequest {
	t.Helper()
	drydockDigest, err := LocalHostPathDigest(drydock)
	if err != nil {
		t.Fatal(err)
	}
	toolchainDigest, err := LocalHostPathDigest(toolchainRoot)
	if err != nil {
		t.Fatal(err)
	}
	unique := localFingerprint(t.Name())
	return LocalRunRequest{Manifest: Manifest{ProtocolVersion: ManifestProtocolVersion,
		Backend: BackendLocal, Command: CommandSpec{Executable: executable,
			Arguments: append([]string(nil), arguments...), WorkingDirectory: "/workspace"},
		Mounts:  []Mount{{Source: ".", Target: "/workspace", Access: MountReadWrite}},
		Network: NetworkScope{Mode: "disabled"},
		Resources: ResourceLimits{CPUQuotaMillis: 1000, MemoryBytes: 256 * 1024 * 1024,
			PIDs: 4, MaxOutputBytes: 1024 * 1024},
		Output:         OutputSpec{CaptureStdout: true, CaptureStderr: true},
		TimeoutSeconds: 20, Cancellation: CancellationSpec{GracePeriodMillis: 100}},
		Binding: LocalExecutionBinding{RunID: "run-" + unique[:16],
			MissionID: "mission-" + unique[:16], SessionID: "session-" + unique[:16],
			WorkspaceID: "workspace-" + unique[:16], DrydockID: "drydock-" + unique[:16],
			DrydockRoot: drydock, DrydockPathSHA256: drydockDigest,
			DrydockRootFingerprint:    localFingerprint("root", unique),
			DrydockBindingFingerprint: localFingerprint("binding", unique),
			DrydockGeneration:         1, PermissionSnapshotID: "permission-" + unique[:16],
			PermissionRevision: 1, ProfileSnapshotID: "profile-" + unique[:16],
			ProfileRevision: 1, InteractionSnapshotID: "interaction-" + unique[:16],
			InteractionRevision: 1, CapabilityGeneration: localFingerprint("capability", unique),
			LeaseID: "lease-" + unique[:16], LeaseGeneration: 1,
			OperationKeySHA256: localFingerprint("operation", unique),
			RuntimeGeneration:  backend.Generation()},
		ToolchainInputs: []LocalToolchainInput{{ID: "toolchain-" + unique[:16],
			Root: toolchainRoot, VirtualRoot: virtualRoot, RootSHA256: toolchainDigest}},
		MaxDiskWriteBytes: 16 * 1024 * 1024}
}
