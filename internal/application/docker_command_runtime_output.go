package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/standardcode"
)

// This key and collector exist only for one live Command Runtime request.
// Neither public Standard Code requests nor persisted/replayed JSON can set it.
type dockerCommandRuntimeOutputKey struct{}

type dockerCommandRuntimeOutput struct {
	mu                 sync.Mutex
	runID              string
	admissionID        string
	maximum            int
	captured           bool
	dispatchPossible   bool
	cleanupConfirmed   bool
	processFailureExit *int
	status             string
	stdout             string
	stderr             string
}

// This provenance is private to the live executor. A preparation or admission
// alone never proves dispatch. An uncertain Start WAL, an existing Start/Launch,
// or entering the lifecycle may own a container until a durable terminal receipt
// confirms cleanup.
func (output *dockerCommandRuntimeOutput) noteDispatchPossible() {
	output.mu.Lock()
	defer output.mu.Unlock()
	output.dispatchPossible = true
}

func noteDockerCommandRuntimeDispatchPossible(ctx context.Context) {
	if ctx == nil {
		return
	}
	if output := dockerCommandRuntimeOutputFromContext(ctx); output != nil {
		output.noteDispatchPossible()
	}
}

func (output *dockerCommandRuntimeOutput) noOwnedTree() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return !output.dispatchPossible || output.cleanupConfirmed
}

func (output *dockerCommandRuntimeOutput) confirmCleanup(record domain.DockerSandboxRecord) error {
	output.mu.Lock()
	defer output.mu.Unlock()
	if record.Validate() != nil || record.Receipt == nil || !record.Receipt.CleanupComplete ||
		record.Admission.RunID != output.runID || (output.admissionID != "" && output.admissionID != record.Admission.ID) {
		return errors.New("Docker Command Runtime cleanup receipt binding is invalid")
	}
	output.cleanupConfirmed = true
	if record.Receipt.Outcome == domain.DockerSandboxOutcomeFailed &&
		record.Receipt.ReasonCode == domain.DockerSandboxReasonProcessFailed &&
		record.Receipt.ExitCode != nil && *record.Receipt.ExitCode > 0 {
		exit := *record.Receipt.ExitCode
		output.processFailureExit = &exit
	}
	return nil
}

func withDockerCommandRuntimeOutput(ctx context.Context, runID string, maximum int) (
	context.Context, *dockerCommandRuntimeOutput,
) {
	output := &dockerCommandRuntimeOutput{runID: runID, maximum: maximum}
	return context.WithValue(ctx, dockerCommandRuntimeOutputKey{}, output), output
}

func dockerCommandRuntimeOutputFromContext(ctx context.Context) *dockerCommandRuntimeOutput {
	output, _ := ctx.Value(dockerCommandRuntimeOutputKey{}).(*dockerCommandRuntimeOutput)
	return output
}

func (output *dockerCommandRuntimeOutput) bind(runID, admissionID string) error {
	output.mu.Lock()
	defer output.mu.Unlock()
	if runID != output.runID || admissionID == "" || output.admissionID != "" {
		return errors.New("Docker Command Runtime output admission binding changed")
	}
	output.admissionID = admissionID
	return nil
}

func (output *dockerCommandRuntimeOutput) matches(runID, admissionID string) bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return runID == output.runID && admissionID != "" && admissionID == output.admissionID
}

// accept is called only after the first exact owned capture receipt has been
// persisted. A receipt replay never calls this method or opens another attach.
func (output *dockerCommandRuntimeOutput) accept(receipt sandbox.DockerLogCaptureReceipt,
	body sandbox.DockerLogOutput,
) error {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.admissionID == "" || output.runID != receipt.RunID ||
		receipt.Validate() != nil || output.captured ||
		!utf8.ValidString(body.Stdout) || !utf8.ValidString(body.Stderr) {
		return errors.New("Docker Command Runtime captured output binding is invalid")
	}
	for _, stream := range receipt.Streams {
		text := body.Stdout
		if stream.Stream == "stderr" {
			text = body.Stderr
		}
		digest := sha256.Sum256([]byte(text))
		if hex.EncodeToString(digest[:]) != stream.ContentDigest {
			return errors.New("Docker Command Runtime output differs from its durable log digest")
		}
	}
	output.stdout, output.stderr = body.Stdout, body.Stderr
	output.status, output.captured = receipt.Status, true
	return nil
}

func (output *dockerCommandRuntimeOutput) terminal(exitCode int) runner.CommandRuntimeSandboxResult {
	output.mu.Lock()
	defer output.mu.Unlock()
	stdout, stderr := output.stdout, output.stderr
	var notices []string
	if !output.captured {
		notices = append(notices, "Docker log output unavailable: only a terminal receipt is available; the command was not rerun.")
	} else if output.status != sandbox.DockerLogCaptureStatusCompleted {
		notices = append(notices, "Docker log capture status: "+output.status+"; output is incomplete.")
	}
	// The runner validates the combined stdout+stderr size. Diagnostics are
	// included in that bound; streams already within it stay byte-for-byte intact.
	diagnostic := dockerOutputDiagnostic(notices)
	if len(stdout)+len(stderr)+len(diagnostic) > output.maximum {
		notices = append(notices, "Docker output truncated to the Command Runtime combined artifact byte limit.")
		diagnostic = dockerOutputDiagnostic(notices)
		budget := max(0, output.maximum-len(diagnostic))
		stderrBudget := min(len(stderr), budget/2)
		stdoutBudget := min(len(stdout), budget-stderrBudget)
		stderrBudget = budget - stdoutBudget
		stdout = dockerOutputPrefix(stdout, stdoutBudget)
		stderr = dockerOutputPrefix(stderr, stderrBudget)
	}
	stderr += diagnostic
	return runner.CommandRuntimeSandboxResult{ExitCode: exitCode, Stdout: []byte(stdout),
		Stderr: []byte(stderr), TreeReaped: true}
}

func (output *dockerCommandRuntimeOutput) terminalResult(result standardcode.Result,
	cause error,
) (runner.CommandRuntimeSandboxResult, error) {
	if result.ExitCode == nil {
		return runner.CommandRuntimeSandboxResult{}, errors.Join(cause,
			errors.New("Docker Standard Code terminal exit code is unavailable"))
	}
	// A natural nonzero exit is a program result only when the exact validated
	// owned receipt says process_failed and confirms cleanup. Other failures,
	// including receipt-only I/O failures, retain their infrastructure error.
	output.mu.Lock()
	programFailure := output.processFailureExit != nil && *output.processFailureExit == *result.ExitCode
	output.mu.Unlock()
	if result.Status != standardcode.StatusSucceeded &&
		!(result.Status == standardcode.StatusFailed && programFailure && cause == nil) {
		cause = errors.Join(cause, fmt.Errorf("Docker Standard Code terminal status is %s", result.Status))
	}
	return output.terminal(*result.ExitCode), cause
}

func dockerOutputDiagnostic(notices []string) string {
	if len(notices) == 0 {
		return ""
	}
	return "\n[Traverse Docker output] " + strings.Join(notices, " ") + "\n"
}

func dockerOutputPrefix(value string, maximum int) string {
	if maximum >= len(value) {
		return value
	}
	maximum = max(0, maximum)
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return value[:maximum]
}
