//go:build windows

package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"cyberagent-workbench/internal/domain"

	"golang.org/x/sys/windows"
)

func TestWindowsFixedCommandRuntimeUsesRestrictedNativeProcess(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	for _, kind := range []ControlledCommandKind{ControlledCommandGoVersion, ControlledCommandPowerShellWorkspaceList} {
		t.Run(string(kind), func(t *testing.T) {
			request := controlledCommandTestRequest(t, kind)
			if kind == ControlledCommandPowerShellWorkspaceList {
				// Test native isolation independently of the default execution deadline.
				request.Timeout = time.Minute
			}
			if err := os.WriteFile(filepath.Join(request.WorkspaceRoot, "fixed-list-marker.txt"), []byte("marker"), 0o600); err != nil {
				t.Fatal(err)
			}
			plan, err := PlanControlledCommand(request)
			if err != nil {
				t.Fatal(err)
			}
			if kind == ControlledCommandPowerShellWorkspaceList && plan.TimeoutMilliseconds != 60_000 {
				t.Fatal("fixed native correctness budget changed")
			}
			manager, intent, err := NewFixedCommandRuntimeManager(newCommandRuntimeMemoryStore(), "fixed-native-test", plan, request.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := manager.NormalizeCommandRuntimeSpec(intent, request.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			if kind == ControlledCommandPowerShellWorkspaceList {
				profile := ""
				for _, entry := range resolved.Environment {
					name, value, _ := strings.Cut(entry, "=")
					if strings.EqualFold(name, "USERPROFILE") {
						profile = value
					}
				}
				if profile != resolved.WorkspaceRoot || commandRuntimePathEqual(profile, os.Getenv("USERPROFILE")) ||
					resolved.EnvironmentInherited || resolved.ProfileStartupFiles {
					t.Fatalf("fixed PowerShell did not bind the workspace profile: %+v", resolved)
				}
				encoded, _ := json.Marshal(resolved.Environment)
				if resolved.EnvironmentSHA256 != commandRuntimeStringSHA256(string(encoded)) {
					t.Fatal("fixed environment digest does not describe the launch")
				}
			}
			changed := intent
			changed.Environment = []CommandRuntimeEnvironment{{Name: "UNTRUSTED", Value: "1"}}
			if _, err := manager.NormalizeCommandRuntimeSpec(changed, request.WorkspaceRoot); err == nil {
				t.Fatal("fixed plan accepted external environment")
			}
			adapter, _ := manager.AdapterIdentity()
			scope := CommandRuntimeScope{AttributionSource: domain.AgentAttributionOperatorRoot, Adapter: adapter}
			ctx := withCommandRuntimeDispatchCheck(t.Context(), func(context.Context, CommandRuntimeResolvedSpec) error { return nil })
			forged := scope
			forged.AttributionSource = domain.AgentAttributionRecorded
			if _, err := manager.starter.Start(ctx, forged, resolved); err == nil {
				t.Fatal("fixed native starter accepted an agent source")
			}
			if kind == ControlledCommandPowerShellWorkspaceList {
				redirected := resolved
				redirected.Environment = replaceCommandRuntimeEnvironment(append([]string(nil), resolved.Environment...), "USERPROFILE", os.Getenv("USERPROFILE"))
				encoded, _ := json.Marshal(redirected.Environment)
				redirected.EnvironmentSHA256 = commandRuntimeStringSHA256(string(encoded))
				if _, err := manager.starter.Start(ctx, scope, redirected); err == nil {
					t.Fatal("fixed native starter accepted a redirected profile with a matching digest")
				}
			}
			process, err := manager.starter.Start(ctx, scope, resolved)
			if err != nil {
				t.Fatal(err)
			}
			defer process.Close()
			native := process.(*windowsCommandRuntimeProcess)
			var token windows.Token
			if err := windows.OpenProcessToken(native.process, windows.TOKEN_QUERY, &token); err != nil {
				t.Fatal(err)
			}
			defer token.Close()
			var restricted, size uint32
			if err := windows.GetTokenInformation(token, windows.TokenHasRestrictions, (*byte)(unsafe.Pointer(&restricted)), uint32(unsafe.Sizeof(restricted)), &size); err != nil || restricted != 1 {
				t.Fatal("native token is not restricted", restricted, err)
			}
			label := make([]byte, 1024)
			if err := windows.GetTokenInformation(token, windows.TokenIntegrityLevel, &label[0], uint32(len(label)), &size); err != nil {
				t.Fatal(err)
			}
			if got := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&label[0])).Label.Sid.String(); got != "S-1-16-4096" {
				t.Fatal("native integrity is not Low", got)
			}
			var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
			if err := windows.QueryInformationJobObject(native.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err != nil {
				t.Fatal(err)
			}
			if limits.BasicLimitInformation.ActiveProcessLimit != 1 || limits.ProcessMemoryLimit != MaxControlledProcessMemoryBytes || limits.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
				t.Fatalf("fixed Job limits changed %+v", limits)
			}
			result := waitWindowsProfileTestProcess(process, time.Duration(plan.TimeoutMilliseconds)*time.Millisecond)
			if result.watchdog || result.waitErr != nil || result.killErr != nil || result.exitCode != 0 ||
				result.stdout.err != nil || result.stderr.err != nil || len(result.stderr.value) != 0 {
				t.Fatalf("restricted process failed: %s", result)
			}
			want := "go version "
			if kind == ControlledCommandPowerShellWorkspaceList {
				want = "fixed-list-marker.txt"
			}
			if !bytes.Contains(result.stdout.value, []byte(want)) {
				t.Fatalf("fixed output missing %q: %q", want, result.stdout.value)
			}
			if kind == ControlledCommandPowerShellWorkspaceList {
				var listing struct {
					Name       string
					Length     int64
					Attributes uint32
				}
				if err := json.Unmarshal(result.stdout.value, &listing); err != nil || listing.Name != want || listing.Length != int64(len("marker")) {
					t.Fatalf("fixed listing changed: %q, error=%v", result.stdout.value, err)
				}
				markerPath, err := windows.UTF16PtrFromString(filepath.Join(request.WorkspaceRoot, want))
				if err != nil {
					t.Fatal(err)
				}
				attributes, err := windows.GetFileAttributes(markerPath)
				if err != nil || listing.Attributes != attributes {
					t.Fatalf("fixed listing attributes=%d want=%d error=%v", listing.Attributes, attributes, err)
				}
			}
			if reaped, err := waitControlledJobReaped(t.Context(), native.job, time.Second); err != nil || !reaped {
				t.Fatal("fixed process tree remained", reaped, err)
			}
			cancelled, err := manager.starter.Start(ctx, scope, resolved)
			if err != nil {
				t.Fatal(err)
			}
			defer cancelled.Close()
			if err := cancelled.Cancel(0); err != nil {
				t.Fatal(err)
			}
			if _, err := cancelled.Wait(); err != nil {
				t.Fatal(err)
			}
			if reaped, err := waitControlledJobReaped(t.Context(), cancelled.(*windowsCommandRuntimeProcess).job, time.Second); err != nil || !reaped {
				t.Fatal("cancelled fixed process tree remained", reaped, err)
			}
		})
	}
}

func TestControlledExecutableCandidatesIgnoreEnvironmentRedirects(t *testing.T) {
	redirect := t.TempDir()
	t.Setenv("SystemRoot", redirect)
	t.Setenv("ProgramFiles", redirect)
	t.Setenv("LocalAppData", redirect)

	systemRoot, err := controlledWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	powerShell := controlledExecutableCandidates("windows-powershell")
	if len(powerShell) != 1 ||
		!strings.EqualFold(filepath.Dir(filepath.Dir(filepath.Dir(
			filepath.Dir(powerShell[0])))), systemRoot) {
		t.Fatalf("unexpected PowerShell candidate: %v", powerShell)
	}
	redirectPrefix := strings.ToLower(filepath.Clean(redirect)) +
		string(filepath.Separator)
	for _, executableID := range []string{"go", "git"} {
		for _, candidate := range controlledExecutableCandidates(executableID) {
			if strings.HasPrefix(strings.ToLower(candidate), redirectPrefix) {
				t.Fatalf("%s candidate trusted redirected environment: %s",
					executableID, candidate)
			}
		}
	}
}

func TestControlledOutputReaderOwnsAndClosesTransferredHandle(t *testing.T) {
	pipe, err := newControlledPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.close()
	readHandle := pipe.read
	pipe.read = 0
	var written uint32
	if err := windows.WriteFile(pipe.write, []byte("owned"), &written, nil); err != nil {
		t.Fatal(err)
	}
	if written != uint32(len("owned")) {
		t.Fatalf("written bytes=%d", written)
	}
	if err := windows.CloseHandle(pipe.write); err != nil {
		t.Fatal(err)
	}
	pipe.write = 0

	resultChannel := make(chan controlledOutputResult, 1)
	errorChannel := make(chan error, 1)
	readControlledOutput(readHandle, resultChannel, errorChannel)
	result := <-resultChannel
	if result.err != nil || string(result.output.Data) != "owned" {
		t.Fatalf("output=%+v error=%v", result.output, result.err)
	}
	if err := windows.CloseHandle(readHandle); !errors.Is(err,
		windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("transferred read handle remained open: %v", err)
	}
}

func TestControlledWorkspaceRejectsEscapingRelativeDirectory(t *testing.T) {
	request := controlledCommandTestRequest(t, ControlledCommandPowerShellWorkspaceList)
	plan, err := PlanControlledCommand(request)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(request.WorkspaceRoot, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("directory symlink is unavailable: %v", err)
	}
	plan.RelativePath = "escape"
	plan.Argv[7] = encodeControlledRelativePath("escape")
	plan.Fingerprint = controlledCommandPlanFingerprint(plan)
	spec := ControlledStartSpec{
		RequestID: ControlledExecutionRequestID(plan),
		PlanID:    plan.ID, PlanFingerprint: plan.Fingerprint,
		ExecutableID:  plan.ExecutableID,
		Argv:          append([]string(nil), plan.Argv...),
		WorkspaceRoot: request.WorkspaceRoot,
		Timeout:       DefaultControlledCommandTimeout,
	}
	if _, err := openControlledWorkspace(spec); err == nil {
		t.Fatal("escaping relative directory was accepted")
	}
}
