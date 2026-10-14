//go:build windows

package desktop

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/outputsafe"
	"cyberagent-workbench/internal/packagede2e"
	"cyberagent-workbench/internal/sandbox"

	"golang.org/x/sys/windows"
)

func TestConfigureStandardCodeSecuritySubprocessHidesWindowsConsole(t *testing.T) {
	command := exec.Command("cmd.exe", "/c", "exit", "0")
	configureStandardCodeSecuritySubprocess(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow ||
		command.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("security subprocess can create a visible console: %+v", command.SysProcAttr)
	}
}

func TestStandardCodeSecurityRecoveryTerminationPolicy(t *testing.T) {
	forced := map[string]bool{
		"recovery_force_termination":               true,
		"recovery_reboot_equivalent":               true,
		"recovery_lease_expiry":                    true,
		"recovery_dirty_untracked_concurrent_edit": true,
	}
	for caseID := range standardCodeRecoveryCases {
		if got := standardCodeSecurityRecoveryForceTermination(caseID); got != forced[caseID] {
			t.Fatalf("force termination for %s = %t, want %t", caseID, got, forced[caseID])
		}
	}
}

func TestStandardCodeRendererCloseRecoveryCannotReportSyntheticPass(t *testing.T) {
	request := packagede2e.SecurityDriverCase{
		Attack: packagede2e.AttackCase{ID: "recovery_renderer_close", Category: "recovery",
			ExpectedSignal: "recovery_required"}, Backend: "local",
	}
	driver := &standardCodeSecurityDriver{opened: true, available: map[string]bool{"local": true}}
	result, err := driver.Execute(context.Background(), request)
	if !errors.Is(err, errStandardCodeRendererRecoveryUnsupported) ||
		result.Status != packagede2e.SecurityEvidenceFailed || result.ActualExecution ||
		result.ActualSignal != "unsupported" ||
		result.DiagnosticCode != "product.renderer_file_edit_recovery_unimplemented" {
		t.Fatalf("unimplemented renderer/FileEdit case = %#v, err = %v", result, err)
	}
	if receipt, err := driver.verifyPackagedRecovery(context.Background(), request); !errors.Is(err, errStandardCodeRendererRecoveryUnsupported) || receipt.Status == "passed" {
		t.Fatalf("command worker manufactured renderer recovery: %#v, err = %v", receipt, err)
	}
	for _, phase := range []string{recoveryWorkerPreparePhase, recoveryWorkerRecoverPhase} {
		err := RunStandardCodeSecurityRecoveryWorker(context.Background(),
			StandardCodeSecurityRecoveryWorkerConfig{CaseID: request.Attack.ID, Backend: "local", Phase: phase})
		if !errors.Is(err, errStandardCodeRendererRecoveryUnsupported) {
			t.Fatalf("renderer recovery worker %s error = %v", phase, err)
		}
	}
}

func TestStandardCodeSecurityRecoveryDirectoryNamesAreBoundAndDistinct(t *testing.T) {
	seen := make(map[string]string)
	for caseID := range standardCodeRecoveryCases {
		for _, backend := range []string{"local", "docker"} {
			name := standardCodeSecurityRecoveryDirectoryName(caseID, backend)
			if len(name) != len("recovery-")+24 || filepath.Base(name) != name {
				t.Fatalf("recovery directory for %s/%s is not bounded: %q",
					caseID, backend, name)
			}
			if previous := seen[name]; previous != "" {
				t.Fatalf("recovery directory collision: %s and %s", previous,
					caseID+"/"+backend)
			}
			seen[name] = caseID + "/" + backend
			if repeated := standardCodeSecurityRecoveryDirectoryName(caseID, backend); repeated != name {
				t.Fatalf("recovery directory for %s/%s drifted", caseID, backend)
			}
		}
	}
}

func TestSetStandardCodeSecurityEnvironmentRestoresHostState(t *testing.T) {
	const name = "TRAVERSE_BOARD_ISSUE181_ENVIRONMENT_TEST"
	t.Cleanup(func() { _ = os.Unsetenv(name) })
	if err := os.Setenv(name, "original"); err != nil {
		t.Fatal(err)
	}
	restore, err := setStandardCodeSecurityEnvironment(name, "synthetic")
	if err != nil || os.Getenv(name) != "synthetic" {
		t.Fatalf("set synthetic environment: value=%q err=%v", os.Getenv(name), err)
	}
	if err := restore(); err != nil || os.Getenv(name) != "original" {
		t.Fatalf("restore original environment: value=%q err=%v", os.Getenv(name), err)
	}
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	restore, err = setStandardCodeSecurityEnvironment(name, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if _, found := os.LookupEnv(name); found {
		t.Fatal("previously absent host environment was not removed")
	}
}

func TestCreateStandardCodeSecurityNamedPipeIsExclusive(t *testing.T) {
	name, handle, err := createStandardCodeSecurityNamedPipe(t.Name())
	if err != nil {
		t.Fatal(err)
	}
	if name == "" || handle == 0 {
		t.Fatalf("named pipe boundary is incomplete: name=%q handle=%v", name, handle)
	}
	if _, duplicate, duplicateErr := createStandardCodeSecurityNamedPipe(t.Name()); duplicateErr == nil {
		_ = windows.CloseHandle(duplicate)
		t.Fatal("duplicate harness-owned named pipe unexpectedly opened")
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	_, reopened, err := createStandardCodeSecurityNamedPipe(t.Name())
	if err != nil {
		t.Fatalf("named pipe boundary was not released: %v", err)
	}
	if err := windows.CloseHandle(reopened); err != nil {
		t.Fatal(err)
	}
}

func TestSeedStandardCodeSecurityConcurrentEdit(t *testing.T) {
	root := t.TempDir()
	drydockRoot := filepath.Join(root, "drydock")
	if err := os.Mkdir(drydockRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("fixed fixture\n")
	if err := os.WriteFile(filepath.Join(drydockRoot, "README.md"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	state := standardCodeSecurityRecoveryState{DrydockRelativePath: "drydock"}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, recoveryWorkerStateName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seedStandardCodeSecurityConcurrentEdit(root); err != nil {
		t.Fatal(err)
	}
	tracked, err := os.ReadFile(filepath.Join(drydockRoot, "README.md"))
	if err != nil || !bytes.HasPrefix(tracked, original) ||
		!bytes.Contains(tracked, []byte("issue181-concurrent-edit\r\n")) {
		t.Fatalf("tracked concurrent edit was not preserved: %q err=%v", tracked, err)
	}
	untracked, err := os.ReadFile(filepath.Join(drydockRoot, "issue181-concurrent.bin"))
	if err != nil || !bytes.Equal(untracked, []byte{0x00, 0x18, 0x01, 0xff}) {
		t.Fatalf("untracked concurrent edit was not preserved: %v err=%v", untracked, err)
	}
}

func TestStandardCodeSecurityExpectedProbeDetailsCoverFrozenMatrix(t *testing.T) {
	definition, err := packagede2e.LoadDefinition()
	if err != nil {
		t.Fatal(err)
	}
	for _, attack := range definition.AttackMatrix.Cases {
		if details := standardCodeSecurityExpectedProbeDetails(attack.ID); len(details) == 0 {
			t.Fatalf("frozen attack %q has no exact probe detail", attack.ID)
		}
	}
	if details := standardCodeSecurityExpectedProbeDetails("not-in-frozen-matrix"); len(details) != 0 {
		t.Fatalf("unknown probe acquired expected details: %v", details)
	}
}

func TestStandardCodeSecurityControlOutputRetainsFramedReceiptAfterSanitizing(t *testing.T) {
	receipt := standardCodeSecurityProbeReceipt{
		Protocol: standardCodeSecurityProbeProtocol,
		Case:     "output_control_ansi",
		Blocked:  true,
		Detail:   "control-output",
	}
	stdout, _ := standardCodeSecurityExpectedProbeStreams(receipt)
	sanitizer := &outputsafe.RedactingStream{}
	safe := sanitizer.Feed([]byte(stdout)) + sanitizer.Flush()
	if strings.ContainsAny(safe, "\x1b\r") || strings.ContainsRune(safe, '\u202e') {
		t.Fatalf("control output remained unsafe: %q", safe)
	}
	if got := parseSecurityProbeReceipt(&artifact.Blob{Content: safe}, receipt.Case); got != receipt {
		t.Fatalf("framed receipt=%+v, want %+v; output=%q", got, receipt, safe)
	}
}

func TestMatchStandardCodeSecurityDockerProbeLogsUsesContentFreeDigests(t *testing.T) {
	for _, caseID := range []string{
		"filesystem_parent_traversal",
		"output_secret_redaction",
		"output_control_ansi",
		"output_stream_limit",
	} {
		detail := standardCodeSecurityExpectedProbeDetails(caseID)[0]
		expected := standardCodeSecurityProbeReceipt{
			Protocol: standardCodeSecurityProbeProtocol, Case: caseID,
			Blocked: true, Detail: detail,
		}
		stdout, stderr := standardCodeSecurityExpectedProbeStreams(expected)
		plan, err := sandbox.NewDockerLogCapturePlan(
			"sandbox-docker-attempt-issue181", 1, "run-issue181-digest-test",
			strings.Repeat("a", 64), sandbox.MaxDockerLogCaptureBytes,
			sandbox.MaxDockerLogCaptureLines, 30)
		if err != nil {
			t.Fatal(err)
		}
		streams, status, err := sandbox.DecodeDockerLogFrames(context.Background(),
			plan, bytes.NewReader(standardCodeSecurityDockerFrames(stdout, stderr)))
		if err != nil {
			t.Fatal(err)
		}
		logs := sandbox.DockerLogCaptureReceipt{Status: status, Streams: streams}
		receipt, err := matchStandardCodeSecurityDockerProbeLogs(caseID, logs)
		if err != nil || receipt != expected {
			t.Fatalf("match %s=%+v err=%v streams=%+v", caseID, receipt, err, streams)
		}
		if caseID == "output_secret_redaction" &&
			(streams[0].RedactedSegments == 0 || streams[1].RedactedSegments == 0) {
			t.Fatalf("secret streams were not independently redacted: %+v", streams)
		}
		if caseID == "output_stream_limit" && streams[0].ByteCount <= 32*1024 {
			t.Fatalf("stream-limit probe did not cross public window: %+v", streams[0])
		}
	}
}

func standardCodeSecurityDockerFrames(stdout, stderr string) []byte {
	var result bytes.Buffer
	for index, value := range []string{stdout, stderr} {
		if value == "" {
			continue
		}
		header := make([]byte, 8)
		header[0] = byte(index + 1)
		binary.BigEndian.PutUint32(header[4:], uint32(len([]byte(value))))
		result.Write(header)
		result.WriteString(value)
	}
	return result.Bytes()
}
