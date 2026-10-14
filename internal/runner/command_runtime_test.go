package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
)

func TestNormalizeCommandRuntimeProcessPinsAbsoluteExecutableAndEnvironment(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := NormalizeCommandRuntimeSpec(CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
		Executable: executable, Arguments: []string{"--help"}, WorkingDirectory: ".",
		Environment: []CommandRuntimeEnvironment{{Name: "COMMAND_RUNTIME_TEST", Value: "safe"}},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 1000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "inspect the current executable",
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.ExecutablePinned || resolved.ProfileStartupFiles ||
		resolved.EnvironmentInherited || len(resolved.ExecutableSHA256) != 64 ||
		len(resolved.EnvironmentSHA256) != 64 || len(resolved.WorkspaceRootSHA256) != 64 ||
		resolved.Spec.WorkingDirectory != "." {
		t.Fatalf("resolved contract widened authority: %#v", resolved)
	}
	joined := strings.Join(resolved.Environment, "\n")
	for _, boundary := range []string{"SSH_AUTH_SOCK=", "HOME=", "GOPROXY=off",
		"GIT_ALLOW_PROTOCOL=file", "CARGO_NET_OFFLINE=true"} {
		if !strings.Contains(joined, boundary) {
			t.Fatalf("restricted environment lacks %q: %q", boundary, joined)
		}
	}
	if _, err := NormalizeCommandRuntimeSpec(CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
		Executable: "go", WorkingDirectory: ".", StdinPolicy: CommandRuntimeStdinClosed,
		CloseInitialStdin: true, TimeoutMilliseconds: 1000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "reject PATH-selected executable",
	}, root); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("bare executable was accepted: %v", err)
	}
}

func TestNormalizeCommandRuntimeHostNetworkKeepsCredentialBoundary(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
		Executable: executable, Arguments: []string{"--help"}, WorkingDirectory: ".",
		Environment: []CommandRuntimeEnvironment{},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 1000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkHost, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "fetch public build dependencies",
	}
	host, err := NormalizeCommandRuntimeSpec(spec, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range host.Environment {
		name, _, _ := strings.Cut(entry, "=")
		if commandRuntimeOfflineEnvironmentName(name) {
			t.Fatalf("host intent retained forced offline setting %q", entry)
		}
	}
	for _, name := range []string{"HOME", "SSH_AUTH_SOCK", "GIT_TERMINAL_PROMPT"} {
		if !strings.Contains(strings.Join(host.Environment, "\n"), name+"=") {
			t.Fatalf("host intent lost credential boundary %s", name)
		}
	}
	spec.Network = CommandRuntimeNetworkDisabled
	disabled, err := NormalizeCommandRuntimeSpec(spec, root)
	if err != nil {
		t.Fatal(err)
	}
	if host.EnvironmentSHA256 == disabled.EnvironmentSHA256 ||
		CommandRuntimeSpecFingerprint(host) == CommandRuntimeSpecFingerprint(disabled) {
		t.Fatal("host and disabled launches have the same fingerprint")
	}
	if !strings.Contains(strings.Join(disabled.Environment, "\n"), "GOPROXY=off") {
		t.Fatal("disabled intent lost its offline environment")
	}
}

func TestCommandRuntimeHostOnlyExplicitCredentialFreeProxy(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
		Executable: executable, Arguments: []string{}, WorkingDirectory: ".",
		Environment: []CommandRuntimeEnvironment{
			{Name: "HTTPS_PROXY", Value: "http://127.0.0.1:7890"},
			{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
		},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 1000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkHost, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "fetch public resources through local proxy",
	}
	root := t.TempDir()
	first, err := NormalizeCommandRuntimeSpec(spec, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(first.Environment, "\n"),
		"HTTPS_PROXY=http://127.0.0.1:7890") {
		t.Fatal("explicit host proxy was not passed to child")
	}
	spec.Environment[0].Value = "http://127.0.0.1:7891"
	second, err := NormalizeCommandRuntimeSpec(spec, root)
	if err != nil || first.EnvironmentSHA256 == second.EnvironmentSHA256 ||
		CommandRuntimeSpecFingerprint(first) == CommandRuntimeSpecFingerprint(second) {
		t.Fatalf("proxy change did not alter launch fingerprint: %v", err)
	}
	spec.Network = CommandRuntimeNetworkDisabled
	if _, err := NormalizeCommandRuntimeIntent(spec); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("disabled network accepted explicit proxy: %v", err)
	}
	spec.Network = CommandRuntimeNetworkHost
	for _, bad := range []string{
		"http://user:pass@127.0.0.1:7890", "http://127.0.0.1:7890/path",
		"http://127.0.0.1:7890?token=secret", "127.0.0.1:7890",
	} {
		spec.Environment[0].Value = bad
		if _, err := NormalizeCommandRuntimeIntent(spec); !errors.Is(err, ErrCommandRuntimeBoundary) {
			t.Fatalf("unsafe proxy %q accepted: %v", bad, err)
		}
	}
	spec.Environment[0] = CommandRuntimeEnvironment{Name: "MY_PROXY",
		Value: "http://127.0.0.1:7890"}
	if _, err := NormalizeCommandRuntimeIntent(spec); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("non-allowlisted proxy variable accepted: %v", err)
	}
}

func TestCommandRuntimeHostNetworkBindsAdapterAndPermissionGeneration(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-host-network")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 2000)
	request.Spec.Spec.Network = CommandRuntimeNetworkHost
	request.Scope.PermissionRuntimeEpoch = "permission-runtime-epoch-1"
	request.Scope.PermissionGeneration = 1
	if _, replayed, err := manager.Start(context.Background(), request); err != nil || replayed {
		t.Fatalf("start host intent replayed=%t err=%v", replayed, err)
	}
	request.Scope.PermissionGeneration = 2
	if _, replayed, err := manager.Start(context.Background(), request); !replayed || !errors.Is(err, ErrCommandRuntimeUncertain) {
		t.Fatalf("re-grant replayed=%t err=%v", replayed, err)
	}
	if starter.starts != 1 {
		t.Fatalf("stale grant launched %d processes", starter.starts)
	}
	request.Scope.PermissionRuntimeEpoch = ""
	if _, _, err := manager.Start(context.Background(), request); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("partial grant tuple accepted: %v", err)
	}
	request.Scope.PermissionGeneration = 0
	request.Scope.PermissionMode = domain.RunExecutionPermissionWorkspaceAccess
	if _, _, err := manager.Start(context.Background(), request); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("Workspace Access host intent accepted: %v", err)
	}
}

func TestNormalizeCommandRuntimeProcessRejectsExecutableTextDisguisedAsNative(t *testing.T) {
	root := t.TempDir()
	name := "not-native"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\necho unsafe\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := NormalizeCommandRuntimeSpec(CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
		Executable: executable, Arguments: []string{}, WorkingDirectory: ".",
		Environment: []CommandRuntimeEnvironment{},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 1000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "reject a script disguised as a native process",
	}, root)
	if !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("text executable was accepted as native: %v", err)
	}
}

func TestNormalizeCommandRuntimeSpecRejectsImplicitBoundaryDefaults(t *testing.T) {
	valid := CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeBash,
		Script: "printf safe", WorkingDirectory: ".",
		Environment: []CommandRuntimeEnvironment{},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 1000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "reject implicit command boundary defaults",
	}
	cases := map[string]func(*CommandRuntimeSpec){
		"working directory": func(spec *CommandRuntimeSpec) { spec.WorkingDirectory = "" },
		"oversized working directory": func(spec *CommandRuntimeSpec) {
			spec.WorkingDirectory = strings.Repeat("x", MaxCommandRuntimePathBytes+1)
		},
		"environment":     func(spec *CommandRuntimeSpec) { spec.Environment = nil },
		"stdin policy":    func(spec *CommandRuntimeSpec) { spec.StdinPolicy = "" },
		"closed stdin":    func(spec *CommandRuntimeSpec) { spec.CloseInitialStdin = false },
		"output policy":   func(spec *CommandRuntimeSpec) { spec.Output = CommandRuntimeOutputPolicy{} },
		"script controls": func(spec *CommandRuntimeSpec) { spec.Script += "\x1b[31m" },
		"environment controls": func(spec *CommandRuntimeSpec) {
			spec.Environment = []CommandRuntimeEnvironment{{Name: "SAFE", Value: "bad\u009b"}}
		},
		"environment secrets": func(spec *CommandRuntimeSpec) {
			spec.Environment = []CommandRuntimeEnvironment{{Name: "SAFE",
				Value: "sk-123456789012345678901234"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := valid
			mutate(&spec)
			if _, err := NormalizeCommandRuntimeSpec(spec, t.TempDir()); !errors.Is(err, ErrCommandRuntimeBoundary) {
				t.Fatalf("implicit %s default was accepted: %v", name, err)
			}
		})
	}
}

func TestCommandRuntimeLaunchRejectsWorkingDirectoryDrift(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "build")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := NormalizeCommandRuntimeSpec(CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
		Executable: executable, Arguments: []string{}, WorkingDirectory: "build",
		Environment: []CommandRuntimeEnvironment{},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 1000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "reject working directory drift before launch",
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := validateCommandRuntimeLaunchDirectory(resolved); !errors.Is(err, ErrCommandRuntimeBoundary) {
		t.Fatalf("missing launch directory was accepted: %v", err)
	}
}

func TestCommandRuntimeProcessProfileSeparatesNativeRuntimesFromShellsAndBrokers(t *testing.T) {
	blocked := []string{"powershell", "bash", "sh", "busybox"}
	allowed := []string{"go", "node", "deno", "bun", "python", "python2", "python3", "python3.14", "pypy", "pypy3.11", "perl", "ruby", "php", "lua", "java", "dotnet", "mono"}
	if runtime.GOOS == "windows" {
		for index := range blocked {
			blocked[index] += ".exe"
		}
		blocked = append(blocked, "cmd.exe", "pwsh.exe", "wscript.exe", "cscript.exe", "mshta.exe", "rundll32.exe", "regsvr32.exe", "py.exe", "wsl.exe", "runas.exe", "test.js", "test.py", "test.cmd", "test.ps1")
		for index := range allowed {
			allowed[index] += ".exe"
		}
	} else {
		blocked = append(blocked, "env", "xargs", "sudo", "su", "doas", "pkexec", "dash", "zsh", "fish", "ksh", "csh", "tcsh", "pwsh")
	}
	for _, executable := range blocked {
		if commandRuntimeNativeExecutableAllowed(filepath.Join(string(filepath.Separator), executable)) {
			t.Fatalf("process profile accepted a shell, script host or broker %q", executable)
		}
	}
	for _, executable := range allowed {
		if !commandRuntimeNativeExecutableAllowed(filepath.Join(string(filepath.Separator), executable)) {
			t.Fatalf("process profile rejected native development runtime %q", executable)
		}
	}
}

func TestCommandRuntimeShellRejectionExplainsSupportedProfileForm(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"powershell", "bash"} {
		t.Run(name, func(t *testing.T) {
			executable := name
			if runtime.GOOS == "windows" {
				executable += ".exe"
			}
			spec := CommandRuntimeSpec{
				Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
				Executable: filepath.Join(root, executable), Arguments: []string{"--version"},
				WorkingDirectory: ".", Environment: []CommandRuntimeEnvironment{},
				StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
				TimeoutMilliseconds: 1000,
				Output:              CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes, ArtifactBytes: MinCommandRuntimeInlineBytes},
				Network:             CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
				Purpose: "inspect a shell using its supported profile",
			}
			_, intentErr := NormalizeCommandRuntimeIntent(spec)
			_, resolvedErr := NormalizeCommandRuntimeSpec(spec, root)
			for _, err := range []error{intentErr, resolvedErr} {
				if !errors.Is(err, ErrCommandRuntimeBoundary) {
					t.Fatalf("shell process changed its denial boundary: %v", err)
				}
				for _, guidance := range []string{"process profile", "system script hosts", "Node/Python", "powershell", "bash", "supported by the current adapter", "omit executable and arguments"} {
					if !strings.Contains(err.Error(), guidance) {
						t.Fatalf("rejected shell lacks actionable %q guidance: %v", guidance, err)
					}
				}
			}
			// Intent validation accepts the existing shell form, without claiming
			// that a resolver, adapter, permission or runtime is available here.
			for _, profile := range []CommandRuntimeProfile{CommandRuntimePowerShell, CommandRuntimeBash} {
				shell := spec
				shell.Profile, shell.Executable, shell.Arguments = profile, "", nil
				shell.Script = "echo profile-form"
				if _, err := NormalizeCommandRuntimeIntent(shell); err != nil {
					t.Fatalf("supported shell argument form was rejected: %v", err)
				}
				shell.Arguments = []string{}
				if _, err := NormalizeCommandRuntimeIntent(shell); !errors.Is(err, ErrCommandRuntimeBoundary) {
					t.Fatalf("shell form unexpectedly accepted process arguments: %v", err)
				}
			}
		})
	}
}

func TestCommandRuntimeEnvironmentRejectsCredentialAndNetworkBootstrapNames(t *testing.T) {
	for _, name := range []string{"HTTPS_PROXY", "AWS_ACCESS_KEY_ID", "AWS_PROFILE",
		"KUBECONFIG", "GIT_ASKPASS", "NPM_TOKEN", "CARGO_HOME",
		"GIT_SSH_COMMAND", "GIT_TERMINAL_PROMPT", "GOPROXY"} {
		if validCommandRuntimeEnvironmentName(name, CommandRuntimeNetworkDisabled) {
			t.Fatalf("credential or network environment name %q was accepted", name)
		}
	}
	if !validCommandRuntimeEnvironmentName("SAFE_BUILD_FLAG", CommandRuntimeNetworkDisabled) {
		t.Fatal("ordinary bounded environment name was rejected")
	}
}

func TestCommandRuntimeRingBoundsFramesAndReportsDroppedCursor(t *testing.T) {
	ring := commandRuntimeRing{capacity: MaxCommandRuntimeInlineBytes}
	for index := 0; index < MaxCommandRuntimeFrames+8; index++ {
		ring.append(CommandRuntimeStdout, "x", time.Unix(0, int64(index)).UTC())
	}
	if len(ring.frames) != MaxCommandRuntimeFrames || ring.base != 8 ||
		ring.next != MaxCommandRuntimeFrames+8 {
		t.Fatalf("ring bounds are wrong: frames=%d base=%d next=%d",
			len(ring.frames), ring.base, ring.next)
	}
	page := ring.read(0, 32)
	if !page.Dropped || page.BaseCursor != 8 || page.NextCursor != 40 ||
		len(page.Frames) != 32 {
		t.Fatalf("dropped cursor projection is wrong: %#v", page)
	}
}

func TestCommandRuntimeRingDurablyEncodesWorstCaseInlineWindow(t *testing.T) {
	ring := commandRuntimeRing{capacity: MaxCommandRuntimeInlineBytes}
	for index := 0; index < MaxCommandRuntimeFrames; index++ {
		ring.append(CommandRuntimeStdout, strings.Repeat(`"`, 128),
			time.Unix(int64(index), 0).UTC())
	}
	encoded := ring.json()
	if encoded == "[]" || len([]byte(encoded)) > MaxCommandRuntimeStoredFrames {
		t.Fatalf("bounded inline window was not durably encoded: bytes=%d", len(encoded))
	}
	var frames []CommandRuntimeFrame
	if err := json.Unmarshal([]byte(encoded), &frames); err != nil ||
		len(frames) != MaxCommandRuntimeFrames {
		t.Fatalf("durable ring frames=%d err=%v", len(frames), err)
	}
}

func TestCommandRuntimeRingMinimumPageAlwaysAdvancesUTF8(t *testing.T) {
	ring := commandRuntimeRing{capacity: MinCommandRuntimeInlineBytes}
	ring.append(CommandRuntimeStdout, "😀x", time.Now().UTC())
	page := ring.read(0, MinCommandRuntimeOutputRead)
	if len(page.Frames) != 1 || page.Frames[0].Text != "😀" ||
		page.NextCursor != uint64(len([]byte("😀"))) {
		t.Fatalf("minimum UTF-8 page did not make cursor progress: %#v", page)
	}
}

func TestCommandRuntimeManagerStreamsSanitizedOutputAndPersistsTerminalState(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, replayed, err := manager.Start(context.Background(), commandRuntimeTestRequest(manager, 2000))
	if err != nil || replayed || snapshot.State != CommandRuntimeJobRunning {
		t.Fatalf("start snapshot=%#v replayed=%t err=%v", snapshot, replayed, err)
	}
	process := starter.last()
	if _, _, _, err := manager.WriteStdin(context.Background(), snapshot.ID,
		"stdin-operation-1", []byte("hello\n"), false); err != nil {
		t.Fatal(err)
	}
	_, written, replayed, err := manager.WriteStdin(context.Background(), snapshot.ID,
		"stdin-operation-1", []byte("hello\n"), false)
	if err != nil || !replayed || written != 6 || process.input.String() != "hello\n" {
		t.Fatalf("stdin replay written=%d replayed=%t input=%q err=%v",
			written, replayed, process.input.String(), err)
	}
	_, _ = io.WriteString(process.stdoutWriter, "\x1b[31mok\x1b[0m token=secret-value-1234567890\n")
	_, _ = io.WriteString(process.stderrWriter, "warning\n")
	process.finish(0)
	terminal, page := waitCommandRuntimeTerminal(t, manager, snapshot.ID)
	if terminal.State != CommandRuntimeJobCompleted || terminal.ExitCode == nil ||
		*terminal.ExitCode != 0 || !terminal.TreeReaped || len(terminal.StdoutSHA256) != 64 ||
		len(terminal.StderrSHA256) != 64 {
		t.Fatalf("terminal snapshot is incomplete: %#v", terminal)
	}
	var combined strings.Builder
	for _, frame := range page.Frames {
		combined.WriteString(frame.Text)
	}
	if strings.Contains(combined.String(), "\x1b") ||
		strings.Contains(combined.String(), "secret-value") ||
		!strings.Contains(combined.String(), "ok") || !strings.Contains(combined.String(), "warning") {
		t.Fatalf("output was not sanitized and retained: %q", combined.String())
	}
	stored, err := store.GetCommandRuntimeJob(context.Background(), snapshot.ID)
	if err != nil || stored.State != CommandRuntimeJobCompleted ||
		stored.OutputFramesJSON == "[]" {
		t.Fatalf("terminal job was not persisted: %#v err=%v", stored, err)
	}
	replay, wasReplay, err := manager.Start(context.Background(), commandRuntimeTestRequest(manager, 2000))
	if err != nil || !wasReplay || replay.ID != snapshot.ID ||
		replay.State != CommandRuntimeJobCompleted || starter.starts != 1 {
		t.Fatalf("start replay duplicated execution: %#v replay=%t starts=%d err=%v",
			replay, wasReplay, starter.starts, err)
	}
}

func TestCommandRuntimeManagerRejectsProcessWithoutCreationTimeTreeOwnership(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	invalid := CommandRuntimeProcessOwnership{PID: 4242, ProcessGroup: 4242,
		JobAssignedAtCreation: true, KillOnClose: false}
	starter := &commandRuntimeFakeStarter{ownership: &invalid}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-invalid-tree")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := manager.Start(context.Background(), commandRuntimeTestRequest(manager, 2000))
	if !errors.Is(err, ErrCommandRuntimeUnavailable) ||
		snapshot.State != CommandRuntimeJobFailed || !snapshot.TreeReaped {
		t.Fatalf("unowned process was accepted: %#v err=%v", snapshot, err)
	}
}

func TestCommandRuntimeManagerShutdownClosesLaunchGate(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-closed")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 2000)
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.Available() {
		t.Fatal("closed manager still advertises availability")
	}
	if _, _, err := manager.Start(context.Background(), request); !errors.Is(err, ErrCommandRuntimeUnavailable) {
		t.Fatalf("closed manager accepted a launch: %v", err)
	}
	if starter.starts != 0 {
		t.Fatalf("closed manager started %d processes", starter.starts)
	}
}

func TestCommandRuntimeManagerConcurrentStartReplaysOneOwnedProcess(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-start-race")
	if err != nil {
		t.Fatal(err)
	}
	type startResult struct {
		snapshot CommandRuntimeJobSnapshot
		replayed bool
		err      error
	}
	results := make(chan startResult, 8)
	var wait sync.WaitGroup
	for index := 0; index < cap(results); index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			snapshot, replayed, err := manager.Start(context.Background(),
				commandRuntimeTestRequest(manager, 2000))
			results <- startResult{snapshot: snapshot, replayed: replayed, err: err}
		}()
	}
	wait.Wait()
	close(results)
	replays := 0
	jobID := ""
	for result := range results {
		if result.err != nil || result.snapshot.State != CommandRuntimeJobRunning {
			t.Fatalf("concurrent start=%#v err=%v", result.snapshot, result.err)
		}
		if jobID == "" {
			jobID = result.snapshot.ID
		} else if result.snapshot.ID != jobID {
			t.Fatalf("concurrent start returned a second Job %q", result.snapshot.ID)
		}
		if result.replayed {
			replays++
		}
	}
	if starter.starts != 1 || replays != cap(results)-1 {
		t.Fatalf("starts=%d replays=%d", starter.starts, replays)
	}
	starter.last().finish(0)
	waitCommandRuntimeTerminal(t, manager, jobID)
}

func TestCommandRuntimeManagerBoundsGlobalActiveProcesses(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "owner-global-limit")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxCommandRuntimeActiveJobs; index++ {
		request := commandRuntimeTestRequest(manager, 2000)
		request.Scope.OperationKey = fmt.Sprintf("operation-%d", index)
		if _, _, err := manager.Start(context.Background(), request); err != nil {
			t.Fatalf("start %d before global limit: %v", index, err)
		}
	}
	overflow := commandRuntimeTestRequest(manager, 2000)
	overflow.Scope.OperationKey = "operation-overflow"
	if _, _, err := manager.Start(context.Background(), overflow); !errors.Is(
		err, ErrCommandRuntimeUnavailable) {
		t.Fatalf("global active-process limit was not enforced: %v", err)
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
}

func TestCommandRuntimeManagerStartsTimeoutBeforeInitialStdinCanBlock(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	writeStarted := make(chan struct{})
	writeRelease := make(chan struct{})
	starter := &commandRuntimeFakeStarter{
		writeStarted: writeStarted,
		writeRelease: writeRelease,
	}
	manager, err := NewCommandRuntimeManager(store, starter, "owner-initial-stdin")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 2000)
	request.Spec.Spec.InitialStdin = "initial input\n"
	request.Spec.Spec.CloseInitialStdin = true
	type startResult struct {
		snapshot CommandRuntimeJobSnapshot
		err      error
	}
	started := make(chan startResult, 1)
	go func() {
		snapshot, _, startErr := manager.Start(context.Background(), request)
		started <- startResult{snapshot: snapshot, err: startErr}
	}()
	var result startResult
	select {
	case result = <-started:
	case <-time.After(500 * time.Millisecond):
		close(writeRelease)
		t.Fatal("Start blocked on initial stdin before lifecycle monitors could run")
	}
	if result.err != nil {
		close(writeRelease)
		t.Fatal(result.err)
	}
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		close(writeRelease)
		t.Fatal("initial stdin writer did not start")
	}
	writeContext, cancelWrite := context.WithTimeout(context.Background(), 25*time.Millisecond)
	_, _, _, writeErr := manager.WriteStdin(writeContext, result.snapshot.ID,
		"later-write", []byte("later\n"), false)
	cancelWrite()
	if !errors.Is(writeErr, context.DeadlineExceeded) {
		close(writeRelease)
		t.Fatalf("blocked stdin gate ignored request cancellation: %v", writeErr)
	}
	close(writeRelease)
	process := starter.last()
	inputDeadline := time.Now().Add(time.Second)
	for {
		process.mu.Lock()
		stdinComplete := process.stdinClosed && process.input.String() == "initial input\n"
		process.mu.Unlock()
		if stdinComplete {
			break
		}
		if time.Now().After(inputDeadline) {
			t.Fatal("initial stdin was not written and closed")
		}
		time.Sleep(time.Millisecond)
	}
	process.finish(0)
	snapshot, _ := waitCommandRuntimeTerminal(t, manager, result.snapshot.ID)
	if snapshot.State != CommandRuntimeJobCompleted || !snapshot.StdinClosed {
		t.Fatalf("initial stdin lifecycle was not completed: %#v", snapshot)
	}
}

func TestCommandRuntimeManagerTreatsPartialStdinWriteAsUncertainReplay(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-stdin-fault")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := manager.Start(context.Background(), commandRuntimeTestRequest(manager, 2000))
	if err != nil {
		t.Fatal(err)
	}
	process := starter.last()
	process.mu.Lock()
	process.writeFailures = 1
	process.mu.Unlock()
	_, written, replayed, err := manager.WriteStdin(context.Background(), snapshot.ID,
		"stdin-partial-operation", []byte("hello"), false)
	if !errors.Is(err, ErrCommandRuntimeUncertain) || replayed || written != 2 {
		t.Fatalf("partial stdin write=%d replayed=%t err=%v", written, replayed, err)
	}
	_, replayWritten, replayed, err := manager.WriteStdin(context.Background(), snapshot.ID,
		"stdin-partial-operation", []byte("hello"), false)
	if !errors.Is(err, ErrCommandRuntimeUncertain) || !replayed || replayWritten != written ||
		process.input.String() != "he" {
		t.Fatalf("partial stdin replay=%d replayed=%t input=%q err=%v",
			replayWritten, replayed, process.input.String(), err)
	}
	process.finish(0)
	waitCommandRuntimeTerminal(t, manager, snapshot.ID)
}

func TestCommandRuntimeManagerTimeoutOwnsFinalReason(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-timeout")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := manager.Start(context.Background(), commandRuntimeTestRequest(manager, 25))
	if err != nil {
		t.Fatal(err)
	}
	terminal, _ := waitCommandRuntimeTerminal(t, manager, snapshot.ID)
	if terminal.State != CommandRuntimeJobTimedOut || terminal.ExitCode == nil ||
		*terminal.ExitCode != 125 {
		t.Fatalf("timeout terminal state is wrong: %#v", terminal)
	}
	if stopped, err := manager.Stop(context.Background(), snapshot.ID, true, 0); err != nil ||
		stopped.State != CommandRuntimeJobTimedOut {
		t.Fatalf("terminal stop was not idempotent: %#v err=%v", stopped, err)
	}
}

func TestCommandRuntimeManagerRetriesStoppingProcessWithoutChangingReason(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-stop-retry")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := manager.Start(context.Background(), commandRuntimeTestRequest(manager, 2000))
	if err != nil {
		t.Fatal(err)
	}
	process := starter.last()
	process.mu.Lock()
	process.killFailures = 1
	process.mu.Unlock()
	stopping, err := manager.Stop(context.Background(), snapshot.ID, true, 0)
	if err == nil || stopping.State != CommandRuntimeJobStopping {
		t.Fatalf("first kill did not surface the injected failure: %#v err=%v", stopping, err)
	}
	if _, err := manager.Stop(context.Background(), snapshot.ID, true, 0); err != nil {
		t.Fatalf("stopping-process retry failed: %v", err)
	}
	terminal, _ := waitCommandRuntimeTerminal(t, manager, snapshot.ID)
	if terminal.State != CommandRuntimeJobKilled || !terminal.TreeReaped {
		t.Fatalf("retried kill changed terminal ownership: %#v", terminal)
	}
}

func TestCommandRuntimeManagerRenewsOwnerLeaseAndFailsClosed(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	manager.ownerLeaseTTL = 150 * time.Millisecond
	manager.ownerRenewEvery = 25 * time.Millisecond
	manager.ownerRenewTimeout = 50 * time.Millisecond
	snapshot, _, err := manager.Start(context.Background(), commandRuntimeTestRequest(manager, 2000))
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.GetCommandRuntimeJob(context.Background(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(starter.last().stdoutWriter, "heartbeat output\n")
	deadline := time.Now().Add(time.Second)
	for {
		renewed, getErr := store.GetCommandRuntimeJob(context.Background(), snapshot.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if renewed.Version > initial.Version &&
			renewed.OwnerExpiresAt.After(initial.OwnerExpiresAt) &&
			renewed.OutputFramesJSON != "[]" && renewed.OutputCursor > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owner lease was not renewed: initial=%#v renewed=%#v", initial, renewed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	store.mu.Lock()
	store.failActive = true
	store.mu.Unlock()
	terminal, _ := waitCommandRuntimeTerminal(t, manager, snapshot.ID)
	if terminal.State != CommandRuntimeJobInterrupted || terminal.ExitCode == nil ||
		*terminal.ExitCode != 125 {
		t.Fatalf("owner-renewal failure did not interrupt the job: %#v", terminal)
	}
}

func TestCommandRuntimeManagerRestartWaitsForOwnerExpiryAndNeverRelaunches(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	starter := &commandRuntimeFakeStarter{}
	first, err := NewCommandRuntimeManager(store, starter, "runtime-owner-before-restart")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := first.Start(context.Background(), commandRuntimeTestRequest(first, 2000))
	if err != nil {
		t.Fatal(err)
	}
	entry := first.entry(snapshot.ID)
	store.mu.Lock()
	job := store.jobs[snapshot.ID]
	job.OwnerExpiresAt = time.Now().UTC().Add(20 * time.Millisecond)
	store.jobs[snapshot.ID] = job
	store.mu.Unlock()

	secondStarter := &commandRuntimeFakeStarter{}
	second, err := NewCommandRuntimeManager(store, secondStarter,
		"runtime-owner-after-restart")
	if err != nil {
		t.Fatal(err)
	}
	if reconciled, err := second.ReconcileStartup(context.Background()); err != nil ||
		reconciled != 0 {
		t.Fatalf("live owner was reconciled: count=%d err=%v", reconciled, err)
	}
	time.Sleep(30 * time.Millisecond)
	if reconciled, err := second.ReconcileStartup(context.Background()); err != nil ||
		reconciled != 1 {
		t.Fatalf("expired owner was not reconciled: count=%d err=%v", reconciled, err)
	}
	stored, err := store.GetCommandRuntimeJob(context.Background(), snapshot.ID)
	if err != nil || stored.State != CommandRuntimeJobInterrupted || !stored.TreeReaped ||
		secondStarter.starts != 0 {
		t.Fatalf("restart replayed launch or lost terminal state: %#v starts=%d err=%v",
			stored, secondStarter.starts, err)
	}
	starter.last().finish(125)
	select {
	case <-entry.done:
	case <-time.After(time.Second):
		t.Fatal("original fake process did not finish")
	}
}

func TestCommandRuntimeManagerSurfacesTerminalPersistenceFailure(t *testing.T) {
	store := newCommandRuntimeMemoryStore()
	store.failTerminal = true
	starter := &commandRuntimeFakeStarter{}
	manager, err := NewCommandRuntimeManager(store, starter, "runtime-owner-fault")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := manager.Start(context.Background(), commandRuntimeTestRequest(manager, 2000))
	if err != nil {
		t.Fatal(err)
	}
	starter.last().finish(0)
	deadline := time.Now().Add(2 * time.Second)
	for {
		terminal, _, waitErr := manager.Wait(context.Background(), snapshot.ID,
			50*time.Millisecond, 0, MaxCommandRuntimeOutputRead)
		if errors.Is(waitErr, ErrCommandRuntimeUncertain) && terminal.State.Terminal() {
			return
		}
		if waitErr != nil {
			t.Fatalf("unexpected wait error: %v", waitErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal persistence failure was hidden")
		}
	}
}

func TestSandboxCommandRuntimeManagerUsesSameDurableJobWithoutHostPID(t *testing.T) {
	identity := commandruntimeadapter.SandboxedWorkspace("local_windows_lpac",
		"windows_appcontainer.v1", strings.Repeat("d", 64))
	executor := &commandRuntimeSandboxExecutorFake{identity: identity}
	manager, err := NewSandboxCommandRuntimeManager(newCommandRuntimeMemoryStore(),
		executor, "sandbox-runtime-owner")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 2000)
	request.Scope.PermissionMode = domain.RunExecutionPermissionWorkspaceAccess
	request.Spec.Spec.StdinPolicy = CommandRuntimeStdinClosed
	request.Spec.Spec.CloseInitialStdin = true
	snapshot, replayed, err := manager.Start(t.Context(), request)
	if err != nil || replayed || snapshot.State != CommandRuntimeJobRunning ||
		snapshot.PID != 0 || snapshot.ProcessGroup != 0 || snapshot.Adapter != identity {
		t.Fatalf("sandbox start=%#v replayed=%t err=%v", snapshot, replayed, err)
	}
	terminal, page := waitCommandRuntimeTerminal(t, manager, snapshot.ID)
	if terminal.State != CommandRuntimeJobCompleted || terminal.ExitCode == nil ||
		*terminal.ExitCode != 0 || !terminal.TreeReaped ||
		page.State != terminal.State || page.ExitCode == nil || *page.ExitCode != 0 ||
		page.EndCursor != terminal.OutputCursor || len(page.Frames) != 2 ||
		executor.calls != 1 {
		t.Fatalf("sandbox terminal=%#v page=%#v calls=%d", terminal, page, executor.calls)
	}

	wrong := request
	wrong.Scope.OperationKey = "sandbox-wrong-adapter"
	wrong.Scope.Adapter = commandruntimeadapter.SandboxedWorkspace("docker_standard_code",
		"docker-standard-code.v1", strings.Repeat("e", 64))
	if _, _, err := manager.Start(t.Context(), wrong); !errors.Is(
		err, ErrCommandRuntimeBoundary) {
		t.Fatalf("cross-adapter launch error=%v", err)
	}
}

func TestSandboxCommandRuntimeManagerStreamsInitialAndInteractiveStdin(t *testing.T) {
	identity := commandruntimeadapter.SandboxedWorkspace("docker_standard_code",
		"docker-standard-code.v2", strings.Repeat("f", 64))
	executor := &commandRuntimeSandboxExecutorFake{identity: identity, stdinEcho: true}
	manager, err := NewSandboxCommandRuntimeManager(newCommandRuntimeMemoryStore(),
		executor, "sandbox-stdin-owner")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 2000)
	request.Scope.PermissionMode = domain.RunExecutionPermissionWorkspaceAccess
	request.Spec.Spec.InitialStdin = "initial\n"
	request.Spec.Spec.CloseInitialStdin = false
	snapshot, replayed, err := manager.Start(t.Context(), request)
	if err != nil || replayed {
		t.Fatalf("sandbox stdin start=%#v replayed=%t err=%v", snapshot, replayed, err)
	}
	updated, written, replayed, err := manager.WriteStdin(t.Context(), snapshot.ID,
		"sandbox-stdin-second-write", []byte("interactive\n"), true)
	if err != nil || replayed || written != len("interactive\n") || !updated.StdinClosed {
		t.Fatalf("sandbox stdin write=%#v written=%d replayed=%t err=%v",
			updated, written, replayed, err)
	}
	terminal, page := waitCommandRuntimeTerminal(t, manager, snapshot.ID)
	var stdout strings.Builder
	for _, frame := range page.Frames {
		if frame.Stream == CommandRuntimeStdout {
			stdout.WriteString(frame.Text)
		}
	}
	if terminal.State != CommandRuntimeJobCompleted || !terminal.StdinClosed ||
		stdout.String() != "initial\ninteractive\n" {
		t.Fatalf("sandbox stdin terminal=%#v stdout=%q page=%#v",
			terminal, stdout.String(), page)
	}
}

func TestSandboxCommandRuntimeManagerCancellationClosesStdinPipe(t *testing.T) {
	identity := commandruntimeadapter.SandboxedWorkspace("docker_standard_code",
		"docker-standard-code.v2", strings.Repeat("9", 64))
	executor := &commandRuntimeSandboxExecutorFake{identity: identity, stdinEcho: true}
	manager, err := NewSandboxCommandRuntimeManager(newCommandRuntimeMemoryStore(),
		executor, "sandbox-stdin-cancel-owner")
	if err != nil {
		t.Fatal(err)
	}
	request := commandRuntimeTestRequest(manager, 2000)
	request.Scope.PermissionMode = domain.RunExecutionPermissionWorkspaceAccess
	snapshot, _, err := manager.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stop(t.Context(), snapshot.ID, false, time.Second); err != nil {
		t.Fatalf("cancel sandbox stdin Job: %v", err)
	}
	terminal, _ := waitCommandRuntimeTerminal(t, manager, snapshot.ID)
	if terminal.State != CommandRuntimeJobCancelled || !terminal.StdinClosed ||
		!terminal.TreeReaped {
		t.Fatalf("cancelled sandbox stdin Job=%#v", terminal)
	}
}

type commandRuntimeSandboxExecutorFake struct {
	identity  commandruntimeadapter.Identity
	calls     int
	stdinEcho bool
}

func (e *commandRuntimeSandboxExecutorFake) Identity() commandruntimeadapter.Identity {
	return e.identity
}

func (*commandRuntimeSandboxExecutorFake) Available() bool { return true }

func (e *commandRuntimeSandboxExecutorFake) ExecuteSandboxCommand(
	_ context.Context, _ CommandRuntimeScope, _ CommandRuntimeResolvedSpec, stdin io.ReadCloser,
) (CommandRuntimeSandboxResult, error) {
	e.calls++
	if e.stdinEcho {
		data, err := io.ReadAll(stdin)
		return CommandRuntimeSandboxResult{ExitCode: 0, Stdout: data,
			TreeReaped: true}, err
	}
	return CommandRuntimeSandboxResult{ExitCode: 0, Stdout: []byte("sandbox-out\n"),
		Stderr: []byte("sandbox-err\n"), TreeReaped: true}, nil
}

func TestCommandRuntimePlatformShellSmoke(t *testing.T) {
	root := t.TempDir()
	profile := CommandRuntimeBash
	script := "printf 'command-runtime-smoke\\n'"
	if runtime.GOOS == "windows" {
		profile = CommandRuntimePowerShell
		script = "[Console]::Out.WriteLine('command-runtime-smoke')"
	}
	resolved, err := NormalizeCommandRuntimeSpec(CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: profile, Script: script,
		WorkingDirectory: ".", Environment: []CommandRuntimeEnvironment{},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 5000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "platform shell smoke",
	}, root)
	if errors.Is(err, ErrCommandRuntimeUnavailable) {
		t.Skipf("%s is unavailable: %v", profile, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	process, err := newPlatformCommandRuntimeStarter().Start(
		context.Background(), CommandRuntimeScope{}, resolved)
	if err != nil {
		t.Fatal(err)
	}
	stdout, readErr := io.ReadAll(process.Stdout())
	exitCode, waitErr := process.Wait()
	_ = process.Close()
	if readErr != nil || waitErr != nil || exitCode != 0 ||
		!strings.Contains(string(stdout), "command-runtime-smoke") {
		t.Fatalf("shell smoke stdout=%q exit=%d read=%v wait=%v",
			stdout, exitCode, readErr, waitErr)
	}
}

func TestCommandRuntimePlatformCancelReapsOwnedProcessTree(t *testing.T) {
	root := t.TempDir()
	profile := CommandRuntimeBash
	script := "(sleep 1; printf leaked > command-runtime-leak-marker) & sleep 30"
	if runtime.GOOS == "windows" {
		profile = CommandRuntimePowerShell
		script = `$child = "Start-Sleep -Milliseconds 1200; [IO.File]::WriteAllText('command-runtime-leak-marker','leaked')"; ` +
			`[IO.File]::WriteAllText('command-runtime-child.ps1',$child); ` +
			`$self = (Get-Process -Id $PID).Path; ` +
			`Start-Process -WindowStyle Hidden -FilePath $self -ArgumentList '-NoLogo','-NoProfile','-NonInteractive','-File','command-runtime-child.ps1'; ` +
			`Start-Sleep -Seconds 30`
	}
	resolved, err := NormalizeCommandRuntimeSpec(CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: profile, Script: script,
		WorkingDirectory: ".", Environment: []CommandRuntimeEnvironment{},
		StdinPolicy: CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: 35_000,
		Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
			ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "process tree cancellation smoke",
	}, root)
	if errors.Is(err, ErrCommandRuntimeUnavailable) {
		t.Skipf("%s is unavailable: %v", profile, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	process, err := newPlatformCommandRuntimeStarter().Start(
		context.Background(), CommandRuntimeScope{}, resolved)
	if err != nil {
		t.Fatal(err)
	}
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, process.Stdout()); close(stdoutDone) }()
	go func() { _, _ = io.Copy(io.Discard, process.Stderr()); close(stderrDone) }()
	if runtime.GOOS == "windows" {
		childPath := filepath.Join(root, "command-runtime-child.ps1")
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, statErr := os.Stat(childPath); statErr == nil {
				break
			}
			if time.Now().After(deadline) {
				_ = process.Kill()
				t.Fatal("PowerShell child process was not prepared")
			}
			time.Sleep(20 * time.Millisecond)
		}
	} else {
		time.Sleep(100 * time.Millisecond)
	}
	if err := process.Cancel(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	<-stdoutDone
	<-stderrDone
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "command-runtime-leak-marker")); !os.IsNotExist(err) {
		t.Fatalf("owned child outlived cancellation: %v", err)
	}
}

func commandRuntimeTestRequest(manager *CommandRuntimeManager,
	timeoutMilliseconds int64,
) CommandRuntimeStartRequest {
	adapter, _ := manager.AdapterIdentity()
	return CommandRuntimeStartRequest{
		OwnershipCheck: func(ctx context.Context) error { return ctx.Err() },
		Scope: CommandRuntimeScope{InvocationID: "invocation-1", OperationKey: "operation-1",
			RunID: "run-1", MissionID: "mission-1", RootAgentID: "agent-1",
			AgentID: "agent-1", AgentAttemptID: "attempt-1",
			AttributionSource: domain.AgentAttributionRecorded,
			SessionID:         "session-1", WorkspaceID: "workspace-1",
			WorkspaceRootSHA256: strings.Repeat("a", 64),
			ModeSnapshotID:      "mode-1", ModeRevision: 1,
			ProfileSnapshotID: "profile-1", ProfileRevision: 1,
			PermissionSnapshotID: "permission-1", PermissionRevision: 1,
			PermissionMode: domain.RunExecutionPermissionFullAccess,
			LeaseID:        "lease-1", LeaseGeneration: 1, LeaseOwnerID: "lease-owner-1",
			Adapter: adapter},
		Spec: CommandRuntimeResolvedSpec{Spec: CommandRuntimeSpec{
			Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
			WorkingDirectory: ".", StdinPolicy: CommandRuntimeStdinPipe,
			TimeoutMilliseconds: timeoutMilliseconds,
			Output: CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes,
				ArtifactBytes: MinCommandRuntimeInlineBytes},
			Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
			Purpose: "test command runtime"},
			ExecutablePath: "test-command-runtime", ExecutableSHA256: strings.Repeat("b", 64),
			EnvironmentSHA256:   strings.Repeat("c", 64),
			WorkspaceRootSHA256: strings.Repeat("a", 64), CanonicalArgv: []string{},
			AbsoluteDirectory: ".", Environment: []string{}, ExecutablePinned: true},
	}
}

func waitCommandRuntimeTerminal(t *testing.T, manager *CommandRuntimeManager,
	jobID string,
) (CommandRuntimeJobSnapshot, CommandRuntimeOutputPage) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, page, err := manager.Wait(context.Background(), jobID,
			50*time.Millisecond, 0, MaxCommandRuntimeOutputRead)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.State.Terminal() {
			return snapshot, page
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not reach a terminal state", jobID)
		}
	}
}

type commandRuntimeMemoryStore struct {
	mu           sync.Mutex
	jobs         map[string]CommandRuntimeJob
	operations   map[string]string
	failActive   bool
	failTerminal bool
}

func newCommandRuntimeMemoryStore() *commandRuntimeMemoryStore {
	return &commandRuntimeMemoryStore{jobs: make(map[string]CommandRuntimeJob),
		operations: make(map[string]string)}
}

func (s *commandRuntimeMemoryStore) PrepareCommandRuntimeJob(_ context.Context,
	job CommandRuntimeJob,
) (CommandRuntimeJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, found := s.operations[job.OperationDigest]; found {
		return s.jobs[id], true, nil
	}
	if err := job.Validate(); err != nil {
		return CommandRuntimeJob{}, false, err
	}
	s.jobs[job.ID] = job
	s.operations[job.OperationDigest] = job.ID
	return job, false, nil
}

func (s *commandRuntimeMemoryStore) UpdateCommandRuntimeJob(_ context.Context,
	job CommandRuntimeJob, expectedVersion int64,
) (CommandRuntimeJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.jobs[job.ID]
	if !found {
		return CommandRuntimeJob{}, ErrCommandRuntimeJobNotFound
	}
	if current.Version != expectedVersion || job.Version != expectedVersion+1 {
		return CommandRuntimeJob{}, ErrCommandRuntimeUncertain
	}
	if s.failTerminal && job.State.Terminal() {
		return CommandRuntimeJob{}, errors.New("injected terminal persistence failure")
	}
	if s.failActive && !job.State.Terminal() {
		return CommandRuntimeJob{}, errors.New("injected owner-renewal persistence failure")
	}
	if err := job.Validate(); err != nil {
		return CommandRuntimeJob{}, err
	}
	s.jobs[job.ID] = job
	return job, nil
}

func (s *commandRuntimeMemoryStore) GetCommandRuntimeJob(_ context.Context,
	jobID string,
) (CommandRuntimeJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, found := s.jobs[jobID]
	if !found {
		return CommandRuntimeJob{}, ErrCommandRuntimeJobNotFound
	}
	return job, nil
}

func (s *commandRuntimeMemoryStore) ListCommandRuntimeJobs(_ context.Context,
	filter CommandRuntimeListFilter,
) ([]CommandRuntimeJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]CommandRuntimeJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		if filter.RunID != "" && job.RunID != filter.RunID ||
			filter.ActiveOnly && job.State.Terminal() {
			continue
		}
		result = append(result, job)
	}
	return result, nil
}

func (s *commandRuntimeMemoryStore) CommandRuntimeJobOwnershipActive(_ context.Context,
	job CommandRuntimeJob,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.jobs[job.ID]
	return found && current.OwnerID == job.OwnerID &&
		current.OwnerGeneration == job.OwnerGeneration &&
		!current.State.Terminal() && current.OwnerExpiresAt.After(time.Now().UTC()), nil
}

type commandRuntimeFakeStarter struct {
	mu           sync.Mutex
	processes    []*commandRuntimeFakeProcess
	starts       int
	ownership    *CommandRuntimeProcessOwnership
	writeStarted chan struct{}
	writeRelease chan struct{}
}

func (*commandRuntimeFakeStarter) Name() string    { return "fake-command-runtime" }
func (*commandRuntimeFakeStarter) Available() bool { return true }
func (s *commandRuntimeFakeStarter) Start(context.Context, CommandRuntimeScope,
	CommandRuntimeResolvedSpec,
) (commandRuntimeProcess, error) {
	process := newCommandRuntimeFakeProcess()
	if s.ownership != nil {
		process.ownership = *s.ownership
	}
	process.writeStarted = s.writeStarted
	process.writeRelease = s.writeRelease
	s.mu.Lock()
	s.starts++
	s.processes = append(s.processes, process)
	s.mu.Unlock()
	return process, nil
}
func (s *commandRuntimeFakeStarter) last() *commandRuntimeFakeProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processes[len(s.processes)-1]
}

type commandRuntimeFakeProcess struct {
	mu             sync.Mutex
	stdoutReader   *io.PipeReader
	stdoutWriter   *io.PipeWriter
	stderrReader   *io.PipeReader
	stderrWriter   *io.PipeWriter
	input          strings.Builder
	wait           chan int
	finishOnce     sync.Once
	stdinClosed    bool
	killFailures   int
	writeFailures  int
	ownership      CommandRuntimeProcessOwnership
	writeStarted   chan struct{}
	writeRelease   chan struct{}
	writeStartOnce sync.Once
}

func newCommandRuntimeFakeProcess() *commandRuntimeFakeProcess {
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	return &commandRuntimeFakeProcess{stdoutReader: stdoutReader, stdoutWriter: stdoutWriter,
		stderrReader: stderrReader, stderrWriter: stderrWriter, wait: make(chan int, 1),
		ownership: CommandRuntimeProcessOwnership{PID: 4242, ProcessGroup: 4242,
			JobAssignedAtCreation: true, KillOnClose: true}}
}
func (p *commandRuntimeFakeProcess) Ownership() CommandRuntimeProcessOwnership {
	return p.ownership
}
func (p *commandRuntimeFakeProcess) Stdout() io.ReadCloser { return p.stdoutReader }
func (p *commandRuntimeFakeProcess) Stderr() io.ReadCloser { return p.stderrReader }
func (p *commandRuntimeFakeProcess) WriteStdin(data []byte) (int, error) {
	if p.writeStarted != nil {
		p.writeStartOnce.Do(func() { close(p.writeStarted) })
	}
	if p.writeRelease != nil {
		<-p.writeRelease
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdinClosed {
		return 0, ErrCommandRuntimeJobClosed
	}
	if p.writeFailures > 0 {
		p.writeFailures--
		written := min(2, len(data))
		_, _ = p.input.Write(data[:written])
		return written, errors.New("injected partial stdin failure")
	}
	return p.input.Write(data)
}
func (p *commandRuntimeFakeProcess) CloseStdin() error {
	p.mu.Lock()
	p.stdinClosed = true
	p.mu.Unlock()
	return nil
}
func (p *commandRuntimeFakeProcess) Wait() (int, error) { return <-p.wait, nil }
func (p *commandRuntimeFakeProcess) Cancel(time.Duration) error {
	p.finish(125)
	return nil
}
func (p *commandRuntimeFakeProcess) Kill() error {
	p.mu.Lock()
	if p.killFailures > 0 {
		p.killFailures--
		p.mu.Unlock()
		return errors.New("injected command runtime kill failure")
	}
	p.mu.Unlock()
	p.finish(125)
	return nil
}
func (p *commandRuntimeFakeProcess) Close() error { return nil }
func (p *commandRuntimeFakeProcess) finish(exitCode int) {
	p.finishOnce.Do(func() {
		_ = p.stdoutWriter.Close()
		_ = p.stderrWriter.Close()
		p.wait <- exitCode
	})
}
