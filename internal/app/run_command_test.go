package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	reporting "cyberagent-workbench/internal/report"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

var runIDPattern = regexp.MustCompile(`run-[0-9]{14}-[a-f0-9]{12}`)
var agentIDPattern = regexp.MustCompile(`agent-[0-9]{14}-[a-f0-9]{12}`)
var sessionIDPattern = regexp.MustCompile(`sess-[0-9]{14}-[a-f0-9]{12}`)
var toolIDPattern = regexp.MustCompile(`tool-[0-9]{14}-[a-f0-9]{12}`)
var processIDPattern = regexp.MustCompile(`process-[0-9]{14}-[a-f0-9]{12}`)
var editIDPattern = regexp.MustCompile(`edit-[0-9]{14}-[a-f0-9]{12}`)
var approvalIDPattern = regexp.MustCompile(`approval-[0-9]{14}-[a-f0-9]{12}`)
var artifactIDPattern = regexp.MustCompile(`artifact-[0-9]{14}-[a-f0-9]{12}`)
var delegationReviewIDPattern = regexp.MustCompile(`delegation-review-[0-9]{14}-[a-f0-9]{12}`)
var fanoutPlanIDPattern = regexp.MustCompile(`fanout-plan-[0-9]{14}-[a-f0-9]{12}`)
var fanoutExecutionIDPattern = regexp.MustCompile(`fanout-execution-[0-9]{14}-[a-f0-9]{12}`)
var findingReportIDPattern = regexp.MustCompile(`report-[a-f0-9]{64}`)
var findingArtifactEvidenceIDPattern = regexp.MustCompile(`finding-artifact-evidence-[0-9]{14}-[a-f0-9]{12}`)
var findingValidationIDPattern = regexp.MustCompile(`finding-validation-[0-9]{14}-[a-f0-9]{12}`)
var findingAcceptanceIDPattern = regexp.MustCompile(`finding-acceptance-[0-9]{14}-[a-f0-9]{12}`)
var findingRemediationEvidenceIDPattern = regexp.MustCompile(`finding-remediation-evidence-[0-9]{14}-[a-f0-9]{12}`)
var findingFixIDPattern = regexp.MustCompile(`finding-fix-[0-9]{14}-[a-f0-9]{12}`)

func executeTestCommand(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out bytes.Buffer
	var errOut bytes.Buffer
	code := Execute(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestPlanDeliveryCLITextIsSingleLine(t *testing.T) {
	const input = "first line\r\nsecond\tline"
	if got := planDeliveryCLIText(input); got != "first line second line" {
		t.Fatalf("Plan/Delivery CLI text was not normalized: %q", got)
	}
}

func TestSpecialistCLIParsesToolsAfterPositionalIdentity(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	for _, args := range [][]string{
		{"run", "specialist-step", "missing-run-for-parser", "--agent", "admitted-child", "--operation-key", "cli-tools-parse-0001", "--tools"},
		{"run", "delegation", "schedule", "missing-proposal-for-parser", "--operation-key", "cli-tools-parse-0002", "--tools"},
	} {
		_, stderr, code := executeTestCommand(t, args...)
		lookupFailure := strings.Contains(stderr, "not found") || strings.Contains(stderr, "no rows in result set")
		if code == 0 || strings.Contains(stderr, "usage:") || strings.Contains(stderr, "flag provided but not defined") || !lookupFailure {
			t.Fatalf("tools flag after identity did not reach lookup: args=%v code=%d stderr=%s", args, code, stderr)
		}
	}
}

func TestCLIHelpListsRunGraphAndLease(t *testing.T) {
	stdout, stderr, code := executeTestCommand(t, "help")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "checkpoint|graph|lease|finish") ||
		!strings.Contains(stdout, "cyberagent report show") {
		t.Fatalf("run graph or lease is missing from help: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
}

func TestScheduledJobCLIStartsReadOnlyAndSupportsStructuredShow(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create",
		"schedule a bounded monitor", "--profile", "code", "--surface", "code",
		"--phase", "plan", "--max-turns", "4")
	if code != 0 || stderr != "" {
		t.Fatalf("create stdout=%s stderr=%s code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	if runID == "" {
		t.Fatalf("missing Run id in %s", created)
	}
	if _, stderr, code = executeTestCommand(t, "run", "start", runID); code != 0 {
		t.Fatalf("start stderr=%s code=%d", stderr, code)
	}
	anchor := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	deadline := anchor.Add(time.Hour)
	output, stderr, code := executeTestCommand(t, "run", "schedule", "create", runID,
		"--at", anchor.Format(time.RFC3339), "--every", "1m",
		"--deadline", deadline.Format(time.RFC3339),
		"--operation-key", "scheduled-cli-create-operation-0001")
	if code != 0 || stderr != "" ||
		!strings.Contains(output, `"version": "scheduled-job.v1"`) ||
		!strings.Contains(output, `"execution_mode": "read_only"`) ||
		!strings.Contains(output, `"replayed": false`) {
		t.Fatalf("schedule stdout=%s stderr=%s code=%d", output, stderr, code)
	}
}

func TestRunModeCLISelectsShowsAndChangesPhase(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create",
		"plan cyber work", "--profile", "review", "--surface", "cyber",
		"--phase", "plan", "--max-turns", "3")
	if code != 0 || stderr != "" || !strings.Contains(created, "surface: cyber") ||
		!strings.Contains(created, "phase: plan") ||
		!strings.Contains(created, "mode_revision: 1") {
		t.Fatalf("unexpected mode create output=%q stderr=%q code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	if runID == "" {
		t.Fatalf("run id missing: %s", created)
	}
	shown, stderr, code := executeTestCommand(t, "run", "mode", runID)
	if code != 0 || stderr != "" || !strings.Contains(shown, "protocol: run_mode.v1") ||
		!strings.Contains(shown, "surface: cyber") || !strings.Contains(shown, "phase: plan") ||
		!strings.Contains(shown, "capability_grant: false") {
		t.Fatalf("unexpected mode output=%q stderr=%q code=%d", shown, stderr, code)
	}
	changed, stderr, code := executeTestCommand(t, "run", "phase", runID, "deliver",
		"--operation-key", "cli-phase-change-0001", "--reason", "plan approved")
	if code != 0 || stderr != "" || !strings.Contains(changed, "phase: deliver") ||
		!strings.Contains(changed, "revision: 2") || !strings.Contains(changed, "replayed: false") ||
		!strings.Contains(changed, "capability_grant: false") {
		t.Fatalf("unexpected phase output=%q stderr=%q code=%d", changed, stderr, code)
	}
	replayed, stderr, code := executeTestCommand(t, "run", "phase", runID, "deliver",
		"--operation-key", "cli-phase-change-0001", "--reason", "plan approved")
	if code != 0 || stderr != "" || !strings.Contains(replayed, "replayed: true") {
		t.Fatalf("unexpected phase replay=%q stderr=%q code=%d", replayed, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run", "start", runID); code != 0 {
		t.Fatalf("run start failed: %s", stderr)
	}
	_, stderr, code = executeTestCommand(t, "run", "phase", runID, "plan",
		"--operation-key", "cli-phase-change-0002")
	if code != 4 || !strings.Contains(stderr, "created or paused") {
		t.Fatalf("running phase change stderr=%q code=%d", stderr, code)
	}
}

func TestRunCapabilityReadinessCLIEmitsStrictNonAuthorizingProjection(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create",
		"inspect capability readiness", "--profile", "code", "--surface", "code",
		"--phase", "deliver", "--max-turns", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("create stdout=%s stderr=%s code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	output, stderr, code := executeTestCommand(t, "run", "capability-readiness",
		runID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("readiness stdout=%s stderr=%s code=%d", output, stderr, code)
	}
	var projection application.RunCapabilityReadiness
	if err := json.Unmarshal([]byte(output), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.RunID != runID || projection.CapabilityGrant ||
		projection.Validate() != nil || len(projection.Permissions) != 3 ||
		len(projection.Presets) != 1 {
		t.Fatalf("invalid CLI readiness projection: %#v", projection)
	}
	for index, mode := range []string{"ask", "auto", "full"} {
		option := projection.Permissions[index]
		if option.Value != mode || option.Selected != (mode == "ask") ||
			option.Selectable || !option.RestartRequired ||
			option.RuntimeAvailable != (mode != "full") {
			t.Fatalf("CLI readiness widened current %s preference: %#v", mode, option)
		}
	}
	if strings.Contains(strings.ToLower(output), "root_path") ||
		strings.Contains(strings.ToLower(output), "lease_id") {
		t.Fatalf("CLI readiness exposed private runtime state: %s", output)
	}
}

func TestRunExecutionProfileCLISelectsWithoutGrantingExecution(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create",
		"choose a bounded execution environment", "--max-turns", "2")
	if code != 0 || stderr != "" {
		t.Fatalf("run create output=%q stderr=%q code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	shown, stderr, code := executeTestCommand(t, "run", "execution-profile", runID)
	if code != 0 || stderr != "" || !strings.Contains(shown, "profile: preview") ||
		!strings.Contains(shown, "backend: noop") ||
		!strings.Contains(shown, "process_enabled: false") ||
		!strings.Contains(shown, "execution_authorized: false") {
		t.Fatalf("unexpected initial profile output=%q stderr=%q code=%d", shown, stderr, code)
	}
	selected, stderr, code := executeTestCommand(t, "run", "execution-profile", "set",
		runID, "docker", "--operation-key", "cli-execution-profile-0001",
		"--reason", "prefer isolated execution")
	if code != 0 || stderr != "" || !strings.Contains(selected, "profile: docker") ||
		!strings.Contains(selected, "required_gate: docker_production_start_gate") ||
		!strings.Contains(selected, "revision: 2") ||
		!strings.Contains(selected, "replayed: false") ||
		!strings.Contains(selected, "capability_grant: false") {
		t.Fatalf("unexpected selected profile output=%q stderr=%q code=%d", selected, stderr, code)
	}
	replayed, stderr, code := executeTestCommand(t, "run", "execution-profile", "set",
		runID, "docker", "--operation-key", "cli-execution-profile-0001",
		"--reason", "prefer isolated execution")
	if code != 0 || stderr != "" || !strings.Contains(replayed, "replayed: true") {
		t.Fatalf("unexpected profile replay=%q stderr=%q code=%d", replayed, stderr, code)
	}
}

func TestRunExecutionInteractionCLIRequiresExplicitOperatorBoundaries(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create",
		"choose an execution interaction boundary", "--max-turns", "2")
	if code != 0 || stderr != "" {
		t.Fatalf("run create output=%q stderr=%q code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	shown, stderr, code := executeTestCommand(t, "run", "execution-interaction",
		runID)
	if code != 0 || stderr != "" ||
		!strings.Contains(shown, "mode: preview") ||
		!strings.Contains(shown, "workspace_trust: untrusted") ||
		!strings.Contains(shown, "agent_input_default: false") ||
		!strings.Contains(shown, "process_enabled: false") {
		t.Fatalf("unexpected initial interaction output=%q stderr=%q code=%d",
			shown, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run", "execution-interaction",
		"set", runID, "controlled", "--trust", "trusted",
		"--operation-key", "cli-interaction-missing-confirmation-0001"); code != 2 || !strings.Contains(stderr, "explicit Workspace trust") {
		t.Fatalf("missing confirmation stderr=%q code=%d", stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run", "execution-profile", "set",
		runID, "local", "--operation-key", "cli-interaction-profile-0001"); code != 0 {
		t.Fatalf("local profile selection failed: %s", stderr)
	}
	selected, stderr, code := executeTestCommand(t, "run",
		"execution-interaction", "set", runID, "controlled",
		"--trust", "trusted", "--confirm-workspace-trust",
		"--operation-key", "cli-interaction-controlled-0001",
		"--reason", "trusted code workspace")
	if code != 0 || stderr != "" ||
		!strings.Contains(selected, "mode: controlled") ||
		!strings.Contains(selected, "command_form: structured_argv") ||
		!strings.Contains(selected, "persistent_terminal: false") ||
		!strings.Contains(selected, "required_gate: local_os_sandbox_gate") ||
		!strings.Contains(selected, "execution_authorized: false") {
		t.Fatalf("unexpected controlled output=%q stderr=%q code=%d",
			selected, stderr, code)
	}
	debug, stderr, code := executeTestCommand(t, "run",
		"execution-interaction", "set", runID, "debug",
		"--trust", "trusted", "--confirm-workspace-trust",
		"--confirm-debug-boundary",
		"--operation-key", "cli-interaction-debug-0001")
	if code != 0 || stderr != "" ||
		!strings.Contains(debug, "mode: debug") ||
		!strings.Contains(debug, "command_form: user_conpty") ||
		!strings.Contains(debug, "persistent_terminal: true") ||
		!strings.Contains(debug, "user_input_available: true") ||
		!strings.Contains(debug, "agent_input_default: false") ||
		!strings.Contains(debug, "capability_grant: false") {
		t.Fatalf("unexpected debug output=%q stderr=%q code=%d",
			debug, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run",
		"execution-interaction", "set", runID, "preview",
		"--operation-key", "cli-interaction-agent-0001", "--operator", "model"); code != 2 || !strings.Contains(stderr, "cannot select") {
		t.Fatalf("model-selected interaction stderr=%q code=%d", stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run", "start", runID); code != 0 {
		t.Fatalf("run start failed before interaction replay: %s", stderr)
	}
	replayed, stderr, code := executeTestCommand(t, "run",
		"execution-interaction", "set", runID, "controlled",
		"--trust", "trusted", "--confirm-workspace-trust",
		"--operation-key", "cli-interaction-controlled-0001",
		"--reason", "trusted code workspace")
	if code != 0 || stderr != "" ||
		!strings.Contains(replayed, "mode: controlled") ||
		!strings.Contains(replayed, "replayed: true") {
		t.Fatalf("running Run replay output=%q stderr=%q code=%d",
			replayed, stderr, code)
	}
}

func TestRunExecutionPermissionCLIRequiresRuntimeGateAndExactConfirmation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	created, stderr, code := executeTestCommand(t, "run", "create",
		"choose an execution permission boundary", "--max-turns", "2")
	if code != 0 || stderr != "" {
		t.Fatalf("run create output=%q stderr=%q code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	assertPreference := func(shown, stderr string, code int, mode string, active bool) {
		t.Helper()
		if code != 0 || stderr != "" {
			t.Fatalf("permission output=%q stderr=%q code=%d", shown, stderr, code)
		}
		for _, want := range []string{"mode: " + mode,
			"approval_policy: per_operation", "command_scope: per_operation",
			"filesystem_scope: per_operation", "network_scope: per_operation",
			"persistent_terminal: false", "background_process: false", "agent_terminal_input: false",
			"sandboxed_command_runtime: true", "unsandboxed_host_process: true",
			fmt.Sprintf("runtime_gate_available: %t", active),
			"process_enabled: false", "execution_authorized: false", "capability_grant: false"} {
			if !strings.Contains(shown, want) {
				t.Fatalf("permission omitted %q: %s", want, shown)
			}
		}
	}
	shown, stderr, code := executeTestCommand(t, "run", "execution-permission", runID)
	assertPreference(shown, stderr, code, "ask", true)
	initialEvents, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || stderr != "" {
		t.Fatalf("initial events stderr=%q code=%d", stderr, code)
	}
	assertRejectedWithoutEffects := func(retired string) {
		t.Helper()
		current, stderr, code := executeTestCommand(t, "run", "execution-permission", runID)
		assertPreference(current, stderr, code, "ask", true)
		if current != shown {
			t.Fatalf("retired argument %s changed permission: before=%q after=%q", retired, shown, current)
		}
		currentEvents, stderr, code := executeTestCommand(t, "run", "events", runID)
		if code != 0 || stderr != "" || currentEvents != initialEvents {
			t.Fatalf("retired argument %s changed events: before=%q after=%q stderr=%q code=%d",
				retired, initialEvents, currentEvents, stderr, code)
		}
		st, err := store.Open(filepath.Join(home, "cyberagent.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		jobs, err := st.ListCommandRuntimeJobs(context.Background(), runner.CommandRuntimeListFilter{
			RunID: runID, Limit: 1,
		})
		if err != nil || len(jobs) != 0 {
			t.Fatalf("retired argument %s created command jobs: jobs=%+v err=%v", retired, jobs, err)
		}
		rounds, err := st.ListRunSupervisorToolRoundsPage(context.Background(), runID, 0, 1)
		if err != nil || len(rounds) != 0 {
			t.Fatalf("retired argument %s created tool calls: rounds=%+v err=%v", retired, rounds, err)
		}
	}
	assertRejectedWithoutEffects("initial state")
	for _, retired := range []string{"conservative", "workspace_access", "approval", "full_access", "debug"} {
		_, stderr, code := executeTestCommand(t, "run", "execution-permission", "set", runID,
			retired, "--operation-key", "cli-retired-"+retired)
		if code != 2 || !strings.Contains(stderr, "approval mode must be ask, auto, or full") {
			t.Fatalf("retired selector %s stderr=%q code=%d", retired, stderr, code)
		}
		assertRejectedWithoutEffects(retired)
	}
	for _, retired := range []string{"--confirm-workspace-access", "--confirm-user-approval",
		"--confirm-danger-full-access", "--confirm-debug-access"} {
		if stdout, stderr, code := executeTestCommand(t, "run", "execution-permission", "set",
			runID, "full", "--operation-key", "cli-retired-confirmation", retired); code != 2 || stdout != "" ||
			!strings.Contains(stderr, "usage: cyberagent run execution-permission ") {
			t.Fatalf("retired confirmation %s stdout=%q stderr=%q code=%d", retired, stdout, stderr, code)
		}
		assertRejectedWithoutEffects(retired)
	}
	for _, mode := range []string{"auto", "ask"} {
		shown, stderr, code := executeTestCommand(t, "run", "execution-permission", "set",
			runID, mode, "--operation-key", "cli-current-permission-"+mode+"-0001")
		assertPreference(shown, stderr, code, mode, true)
	}
	if _, stderr, code := executeTestCommand(t, "run", "execution-permission",
		"set", runID, "full", "--operation-key", "cli-permission-no-runtime-gate-0001",
		"--confirm-full"); code != 5 || !strings.Contains(stderr, "lacks gate") {
		t.Fatalf("closed Full runtime gate stderr=%q code=%d", stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run", "execution-permission",
		"set", runID, "full", "--operation-key", "cli-permission-no-confirmation-0001",
		"--enable-permission-control", "--enable-danger-full-access"); code != 2 ||
		!strings.Contains(stderr, "full requires explicit confirm_full=true") {
		t.Fatalf("missing Full confirmation stderr=%q code=%d", stderr, code)
	}
	full, stderr, code := executeTestCommand(t, "run", "execution-permission",
		"set", runID, "full", "--operation-key", "cli-permission-full-0001",
		"--enable-permission-control", "--enable-danger-full-access", "--confirm-full")
	assertPreference(full, stderr, code, "full", true)
	if !strings.Contains(full, "activation_lifetime: this CLI process only") {
		t.Fatalf("Full activation lifetime missing: %s", full)
	}
	cold, stderr, code := executeTestCommand(t, "run", "execution-permission", runID)
	assertPreference(cold, stderr, code, "full", false)

	cdp, stderr, code := executeTestCommand(t, "run", "browser-cdp-permission", runID)
	if code != 0 || stderr != "" ||
		!strings.Contains(cdp, "mode: full_debug") ||
		!strings.Contains(cdp, "navigate_allowed: true") ||
		!strings.Contains(cdp, "request_mutation_allowed: true") ||
		!strings.Contains(cdp, "transport_enabled: false") {
		t.Fatalf("Full did not default Full CDP on safely: output=%q stderr=%q code=%d",
			cdp, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run", "browser-cdp-permission",
		"set", runID, "full_debug", "--operation-key", "cli-browser-cdp-no-gate-0001",
		"--enable-browser-cdp-control", "--confirm-full-cdp-debug"); code != 5 ||
		!strings.Contains(stderr, "lacks gate") {
		t.Fatalf("closed browser CDP gate stderr=%q code=%d", stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run", "browser-cdp-permission",
		"set", runID, "full_debug", "--operation-key", "cli-browser-cdp-no-confirmation-0001",
		"--enable-browser-cdp-control", "--enable-full-cdp-debug",
		"--enable-permission-control", "--enable-danger-full-access", "--confirm-full"); code != 2 ||
		!strings.Contains(stderr, "exact highly-sensitive confirmation") {
		t.Fatalf("missing browser CDP confirmation stderr=%q code=%d", stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run", "browser-cdp-permission",
		"set", runID, "full_debug", "--operation-key", "cli-browser-cdp-cold-full-0001",
		"--enable-browser-cdp-control", "--enable-full-cdp-debug",
		"--enable-permission-control", "--enable-danger-full-access", "--confirm-full-cdp-debug"); code != 5 ||
		!strings.Contains(stderr, "live Full authority") {
		t.Fatalf("cold Full gained CDP authority stderr=%q code=%d", stderr, code)
	}
	restrictedCDP, stderr, code := executeTestCommand(t, "run", "browser-cdp-permission",
		"set", runID, "restricted", "--operation-key", "cli-browser-cdp-disable-0001",
		"--enable-browser-cdp-control")
	if code != 0 || stderr != "" ||
		!strings.Contains(restrictedCDP, "mode: restricted") ||
		!strings.Contains(restrictedCDP, "request_mutation_allowed: false") ||
		!strings.Contains(restrictedCDP, "cookie_access_allowed: false") {
		t.Fatalf("explicit Full CDP disable failed output=%q stderr=%q code=%d",
			restrictedCDP, stderr, code)
	}
	fullCDP, stderr, code := executeTestCommand(t, "run", "browser-cdp-permission",
		"set", runID, "full_debug", "--operation-key", "cli-browser-cdp-full-0001",
		"--enable-browser-cdp-control", "--enable-full-cdp-debug",
		"--enable-permission-control", "--enable-danger-full-access",
		"--confirm-full", "--confirm-full-cdp-debug")
	if code != 0 || stderr != "" ||
		!strings.Contains(fullCDP, "mode: full_debug") ||
		!strings.Contains(fullCDP, "request_mutation_allowed: true") ||
		!strings.Contains(fullCDP, "cookie_access_allowed: true") ||
		!strings.Contains(fullCDP, "arbitrary_method_allowed: true") ||
		!strings.Contains(fullCDP, "transport_enabled: false") ||
		!strings.Contains(fullCDP, "runtime_authorized: false") {
		t.Fatalf("unexpected full browser CDP output=%q stderr=%q code=%d",
			fullCDP, stderr, code)
	}
	if lowered, stderr, code := executeTestCommand(t, "run", "execution-permission",
		"set", runID, "ask", "--operation-key", "cli-permission-lower-forces-cdp-off-0001"); code != 0 || stderr != "" || !strings.Contains(lowered, "mode: ask") {
		t.Fatalf("execution downgrade failed output=%q stderr=%q code=%d",
			lowered, stderr, code)
	}
	forcedRestricted, stderr, code := executeTestCommand(t, "run", "browser-cdp-permission", runID)
	if code != 0 || stderr != "" ||
		!strings.Contains(forcedRestricted, "mode: restricted") ||
		!strings.Contains(forcedRestricted, "request_mutation_allowed: false") {
		t.Fatalf("execution downgrade did not force Full CDP off output=%q stderr=%q code=%d",
			forcedRestricted, stderr, code)
	}
}

func TestRunCommandPlanExposesOnlyClosedNonStartingEnvelope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init",
		"command-plan-demo"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	rawRoot := filepath.Join(home, "workspaces", "command-plan-demo")
	canonicalRoot, err := filepath.EvalSymlinks(rawRoot)
	if err != nil {
		t.Fatal(err)
	}
	created, stderr, code := executeTestCommand(t, "run", "create",
		"plan one controlled command", "--workspace", "command-plan-demo",
		"--max-turns", "2")
	if code != 0 || stderr != "" {
		t.Fatalf("run create output=%q stderr=%q code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	if _, stderr, code := executeTestCommand(t, "run", "execution-profile", "set",
		runID, "local", "--operation-key", "cli-command-plan-profile-0001"); code != 0 {
		t.Fatalf("local profile selection failed: %s", stderr)
	}
	if _, stderr, code := executeTestCommand(t, "run",
		"execution-interaction", "set", runID, "controlled",
		"--trust", "trusted", "--confirm-workspace-trust",
		"--operation-key", "cli-command-plan-interaction-0001"); code != 0 {
		t.Fatalf("controlled interaction selection failed: %s", stderr)
	}
	planned, stderr, code := executeTestCommand(t, "run", "command-plan",
		runID, "powershell-workspace-list", "--path", "src")
	if code != 0 || stderr != "" ||
		!strings.Contains(planned, "kind: powershell-workspace-list") ||
		!strings.Contains(planned, "executable_id: windows-powershell") ||
		!strings.Contains(planned, "relative_path: src") ||
		!strings.Contains(planned, "caller_shell_text_accepted: false") ||
		!strings.Contains(planned, "go_owned_powershell_script: true") ||
		!strings.Contains(planned, "os_sandbox_required: true") ||
		!strings.Contains(planned, "start_blocked: true") ||
		!strings.Contains(planned, "product_execution_enabled: false") ||
		strings.Contains(planned, "Get-ChildItem") ||
		strings.Contains(planned, rawRoot) || strings.Contains(planned, canonicalRoot) {
		t.Fatalf("unexpected command plan output=%q stderr=%q code=%d",
			planned, stderr, code)
	}
	if _, stderr, code := executeTestCommand(t, "run", "command-plan",
		runID, "powershell", "--path", "whoami"); code != 1 || !strings.Contains(stderr, "unsupported command kind") {
		t.Fatalf("arbitrary PowerShell kind stderr=%q code=%d", stderr, code)
	}
}

func TestRetiredProposalCLIRejectsNewReviewAndOnceExecution(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	for _, args := range [][]string{{"run", "command-proposal", "review", "old", "approve"}, {"run", "host-command-proposal", "review", "old", "approve"}, {"once-command", "run", "old"}} {
		if _, stderr, code := executeTestCommand(t, args...); code == 0 || strings.TrimSpace(stderr) == "" {
			t.Fatalf("retired execution accepted: %v %d %s", args, code, stderr)
		}
	}
}

func TestRunCommandExecuteIsConfirmedAuditedAndExactlyOnce(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fixed restricted native adapter is Windows-only; exercised in Windows CI")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init",
		"command-execute-demo"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	created, stderr, code := executeTestCommand(t, "run", "create",
		"execute one controlled command", "--workspace", "command-execute-demo",
		"--surface", "code", "--phase", "deliver",
		"--max-turns", "2")
	if code != 0 || stderr != "" {
		t.Fatalf("run create output=%q stderr=%q code=%d", created, stderr, code)
	}
	runID := runIDPattern.FindString(created)
	if _, stderr, code := executeTestCommand(t, "run", "execution-profile", "set",
		runID, "local", "--operation-key",
		"cli-command-execute-profile-0001"); code != 0 {
		t.Fatalf("local profile selection failed: %s", stderr)
	}
	if _, stderr, code := executeTestCommand(t, "run",
		"execution-interaction", "set", runID, "controlled",
		"--trust", "trusted", "--confirm-workspace-trust",
		"--operation-key",
		"cli-command-execute-interaction-0001"); code != 0 {
		t.Fatalf("controlled interaction selection failed: %s", stderr)
	}
	execute := func(args ...string) (string, string, int) { return executeTestCommand(t, args...) }
	if _, stderr, code := execute("run", "command-execute", runID,
		"go-version", "--operation-key", "controlled-execute-0001"); code != 2 || !strings.Contains(stderr, "--confirm-execution") {
		t.Fatalf("missing confirmation stderr=%q code=%d",
			stderr, code)
	}
	if plan, stderr, code := execute("run", "command-plan", runID, "go-version"); code != 0 || stderr != "" || !strings.Contains(plan, "go-version") {
		t.Fatalf("plan output=%q stderr=%q code=%d", plan, stderr, code)
	}
	first, stderr, code := execute("run", "command-execute", runID,
		"go-version", "--operation-key", "controlled-execute-0001",
		"--confirm-execution", "--enable-permission-control")
	if code != 0 || stderr != "" ||
		!strings.Contains(first, "command_job: command-job-") ||
		!strings.Contains(first, "state: completed") ||
		!strings.Contains(first, "replayed: false") ||
		!strings.Contains(first, "stdout_begin\ngo version ") {
		t.Fatalf("first execution output=%q stderr=%q code=%d",
			first, stderr, code)
	}
	replayed, stderr, code := execute("run", "command-execute", runID,
		"go-version", "--operation-key", "controlled-execute-0001",
		"--confirm-execution")
	if code != 0 || stderr != "" ||
		!strings.Contains(replayed, "replayed: true") ||
		!strings.Contains(replayed, "go version ") {
		t.Fatalf("replay output=%q stderr=%q code=%d",
			replayed, stderr, code)
	}
	if _, stderr, code := execute("run", "command-execute", runID,
		"git-status", "--operation-key", "controlled-execute-0001",
		"--confirm-execution"); code == 0 ||
		!strings.Contains(stderr, "another request") {
		t.Fatalf("conflict stderr=%q code=%d", stderr, code)
	}
	if output, stderr, code := execute("run", "show", runID); code != 0 || stderr != "" || !strings.Contains(output, "status: created") {
		t.Fatalf("command changed Run output=%q stderr=%q code=%d", output, stderr, code)
	}
}

func TestRunHostExecuteRequiresFullAccessAndIsExactlyOnce(t *testing.T) {
	home := newCanonicalCLIHome(t)
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "host-execute-demo"); code != 0 {
		t.Fatal(stderr)
	}
	created, stderr, code := executeTestCommand(t, "run", "create", "execute one host command", "--workspace", "host-execute-demo", "--surface", "code", "--phase", "deliver", "--max-turns", "2")
	if code != 0 {
		t.Fatal(stderr)
	}
	runID := runIDPattern.FindString(created)
	for _, args := range [][]string{
		{"run", "execution-profile", "set", runID, "local", "--operation-key", "host-profile-0001"},
		{"run", "execution-interaction", "set", runID, "controlled", "--trust", "trusted", "--confirm-workspace-trust", "--operation-key", "host-interaction-0001"},
		{"run", "execution-permission", "set", runID, "full", "--operation-key", "host-permission-0001", "--enable-permission-control", "--enable-danger-full-access", "--confirm-full"},
	} {
		if _, stderr, code := executeTestCommand(t, args...); code != 0 {
			t.Fatal(args, stderr)
		}
	}
	executable, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"run", "host-execute", runID, "--executable", executable, "--arg", "version", "--operation-key", "host-execute-0001", "--confirm-non-sandboxed-host-execution", "--enable-permission-control", "--enable-danger-full-access"}
	if _, stderr, code := executeTestCommand(t, append(base, "--confirm-full")...); code == 0 || !strings.Contains(stderr, "stopped Run require exact operator confirmation") {
		t.Fatal("Full bypassed exact execution confirmation", code, stderr)
	}
	base = append(base, "--confirm-execution")
	if _, stderr, code := executeTestCommand(t, base...); code == 0 || !strings.Contains(stderr, "runtime authority is unavailable") {
		t.Fatal("cold Full was not rejected by runtime authority", code, stderr)
	}
	first, stderr, code := executeTestCommand(t, append(base, "--confirm-full")...)
	if code != 0 || !strings.Contains(first, "state: completed") || !strings.Contains(first, "replayed: false") || !strings.Contains(first, "go version ") {
		t.Fatal(code, first, stderr)
	}
	replay, stderr, code := executeTestCommand(t, base...)
	if code != 0 || !strings.Contains(replay, "replayed: true") || !strings.Contains(replay, "go version ") {
		t.Fatal(code, replay, stderr)
	}
	if strings.Split(first, "\n")[0] != strings.Split(replay, "\n")[0] {
		t.Fatal("receipt changed identity")
	}
	if _, stderr, code := executeTestCommand(t, append(base, "--arg", "different")...); code == 0 || !strings.Contains(stderr, "another request") {
		t.Fatal("conflicting request accepted", code, stderr)
	}
	state, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	jobs, err := state.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: runID, Limit: 10})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("expected one completed host Job: %+v err=%v", jobs, err)
	}
	for _, flag := range []string{"--confirm-danger-full-access", "--enable-debug-maximum-access"} {
		if stdout, stderr, code := executeTestCommand(t, append(base, flag)...); code != 2 || stdout != "" ||
			!strings.Contains(stderr, "usage: cyberagent run host-execute ") {
			t.Fatal("retired flag was not rejected", flag, code, stdout, stderr)
		}
		current, err := state.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: runID, Limit: 10})
		if err != nil || !reflect.DeepEqual(current, jobs) {
			t.Fatalf("retired flag %s changed the Job ledger: %+v err=%v", flag, current, err)
		}
	}
}

func TestReportCheckRejectsOutputFormatBeforeLookup(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	_, stderr, code := executeTestCommand(t, "report", "check",
		"report-"+strings.Repeat("a", 64), "--format", "yaml")
	if code == 0 || !strings.Contains(stderr, "format must be text, json, or github") ||
		strings.Contains(stderr, "not found") {
		t.Fatalf("report format validation was not fail-fast: stderr=%s code=%d",
			stderr, code)
	}
}

func TestRunReadOnlyFanoutCLIPlansThenExecutesThroughReadOnlyGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "fanout-demo"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	root := filepath.Join(home, "workspaces", "fanout-demo")
	for index := range 8 {
		path := filepath.Join(root, "src", fmt.Sprintf("module-%d.go", index+1))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package source\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	created, stderr, code := executeTestCommand(t, "run", "create",
		"plan parallel read-only audit", "--workspace", "fanout-demo", "--profile", "review",
		"--max-turns", "20", "--max-tokens", "20000")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	sessionID := sessionIDPattern.FindString(created)
	if runID == "" || sessionID == "" {
		t.Fatalf("run or Session id missing: %s", created)
	}
	if _, stderr, code := executeTestCommand(t, "run", "start", runID); code != 0 {
		t.Fatalf("run start failed: %s", stderr)
	}
	planned, stderr, code := executeTestCommand(t, "run", "fanout", "plan", runID,
		"audit independent source modules", "--tier", "6", "--path", ".",
		"--operation-key", "fanout-cli-plan-0001")
	if code != 0 || stderr != "" || !strings.Contains(planned, "requested_tier: 6") ||
		!strings.Contains(planned, "effective_parallelism: 6") ||
		!strings.Contains(planned, "capability: workspace_readonly") ||
		!strings.Contains(planned, "shell: false") ||
		!strings.Contains(planned, "network: false") ||
		!strings.Contains(planned, "execution_authorized: false") ||
		!strings.Contains(planned, "replayed: false") {
		t.Fatalf("unexpected fan-out plan output=%s stderr=%s code=%d", planned, stderr, code)
	}
	planID := fanoutPlanIDPattern.FindString(planned)
	if planID == "" {
		t.Fatalf("fan-out plan id missing: %s", planned)
	}
	replayed, stderr, code := executeTestCommand(t, "run", "fanout", "plan", runID,
		"audit independent source modules", "--tier", "6", "--path", ".",
		"--operation-key", "fanout-cli-plan-0001")
	if code != 0 || stderr != "" || fanoutPlanIDPattern.FindString(replayed) != planID ||
		!strings.Contains(replayed, "replayed: true") {
		t.Fatalf("fan-out replay drifted output=%s stderr=%s code=%d", replayed, stderr, code)
	}
	shown, stderr, code := executeTestCommand(t, "run", "fanout", "show", planID)
	if code != 0 || stderr != "" || strings.Count(shown, "shard_") != 6 ||
		!strings.Contains(shown, "goal: audit independent source modules") ||
		!strings.Contains(shown, "execution_authorized: false") {
		t.Fatalf("fan-out show drifted output=%s stderr=%s code=%d", shown, stderr, code)
	}
	listed, stderr, code := executeTestCommand(t, "run", "fanouts", runID)
	if code != 0 || stderr != "" || !strings.Contains(listed, planID) ||
		!strings.Contains(listed, "execution_authorized=false") {
		t.Fatalf("fan-out list drifted output=%s stderr=%s code=%d", listed, stderr, code)
	}
	executed, stderr, code := executeTestCommand(t, "run", "fanout", "execute", planID,
		"--operation-key", "fanout-cli-execution-0001", "--max-output-tokens", "512")
	if code != 0 || stderr != "" || !strings.Contains(executed, "status: completed") ||
		strings.Count(executed, "summary: Mock read-only audit completed") != 6 ||
		!strings.Contains(executed, "external_tools: false") ||
		!strings.Contains(executed, "child_spawn: false") ||
		!strings.Contains(executed, "replayed: false") {
		t.Fatalf("fan-out execution failed output=%s stderr=%s code=%d",
			executed, stderr, code)
	}
	executionID := fanoutExecutionIDPattern.FindString(executed)
	if executionID == "" {
		t.Fatalf("fan-out execution id missing: %s", executed)
	}
	executionReplay, stderr, code := executeTestCommand(t, "run", "fanout", "execute",
		planID, "--operation-key", "fanout-cli-execution-0001",
		"--max-output-tokens", "512")
	if code != 0 || stderr != "" ||
		fanoutExecutionIDPattern.FindString(executionReplay) != executionID ||
		!strings.Contains(executionReplay, "replayed: true") {
		t.Fatalf("fan-out execution replay drifted output=%s stderr=%s code=%d",
			executionReplay, stderr, code)
	}
	executionShown, stderr, code := executeTestCommand(t, "run", "fanout", "execution",
		executionID)
	if code != 0 || stderr != "" || !strings.Contains(executionShown, "status: completed") ||
		strings.Count(executionShown, "shard_") != 6 {
		t.Fatalf("fan-out execution show drifted output=%s stderr=%s code=%d",
			executionShown, stderr, code)
	}
	reportJSON, stderr, code := executeTestCommand(t, "run", "fanout", "report",
		executionID, "--format", "json")
	var findingReport domain.FindingReport
	if code != 0 || stderr != "" || json.Unmarshal([]byte(reportJSON), &findingReport) != nil ||
		findingReport.Status != domain.FindingReportGenerated ||
		findingReport.SourceID != executionID || findingReport.FindingCount != 6 ||
		findingReport.EvidenceCount != 6 || len(findingReport.Findings) != 6 ||
		findingReport.Severity.Info != 6 || findingReportIDPattern.FindString(reportJSON) == "" {
		t.Fatalf("fan-out JSON report failed output=%s stderr=%s code=%d",
			reportJSON, stderr, code)
	}
	reportReplay, stderr, code := executeTestCommand(t, "run", "fanout", "report",
		executionID, "--format", "json")
	if code != 0 || stderr != "" || reportReplay != reportJSON {
		t.Fatalf("fan-out report replay drifted output=%s stderr=%s code=%d",
			reportReplay, stderr, code)
	}
	findingID := findingReport.Findings[0].ID
	artifactProposal, stderr, code := executeTestCommand(t, "session", "send", sessionID,
		"/run echo report-validation-evidence")
	toolID := toolIDPattern.FindString(artifactProposal)
	if code != 0 || stderr != "" || toolID == "" {
		t.Fatalf("validation Artifact proposal failed output=%s stderr=%s code=%d",
			artifactProposal, stderr, code)
	}
	artifactApproved, stderr, code := executeTestCommand(t, "tool", "approve", toolID)
	artifactID := artifactIDPattern.FindString(artifactApproved)
	if code != 0 || stderr != "" || artifactID == "" ||
		!strings.Contains(artifactApproved, "artifact_stdout_id:") {
		t.Fatalf("validation Artifact approval failed output=%s stderr=%s code=%d",
			artifactApproved, stderr, code)
	}
	attached, stderr, code := executeTestCommand(t, "report", "finding", "attach",
		findingID, artifactID, "--operation-key", "report-cli-evidence-0001",
		"--note", "reproduced with frozen mock output")
	evidenceID := findingArtifactEvidenceIDPattern.FindString(attached)
	if code != 0 || stderr != "" || evidenceID == "" ||
		!strings.Contains(attached, "attached") {
		t.Fatalf("Artifact Evidence attach failed output=%s stderr=%s code=%d",
			attached, stderr, code)
	}
	attachedReplay, stderr, code := executeTestCommand(t, "report", "finding", "attach",
		findingID, artifactID, "--operation-key", "report-cli-evidence-0001",
		"--note", "reproduced with frozen mock output")
	if code != 0 || stderr != "" ||
		findingArtifactEvidenceIDPattern.FindString(attachedReplay) != evidenceID ||
		!strings.Contains(attachedReplay, "reused") {
		t.Fatalf("Artifact Evidence replay drifted output=%s stderr=%s code=%d",
			attachedReplay, stderr, code)
	}
	validated, stderr, code := executeTestCommand(t, "report", "finding", "validate",
		findingID, "--operation-key", "report-cli-validation-0001",
		"--reason", "frozen Artifact confirms the mock workflow")
	validationID := findingValidationIDPattern.FindString(validated)
	if code != 0 || stderr != "" || validationID == "" ||
		!strings.Contains(validated, "status: validated") ||
		!strings.Contains(validated, "artifact_evidence_count: 1") {
		t.Fatalf("finding validation failed output=%s stderr=%s code=%d",
			validated, stderr, code)
	}
	validatedReplay, stderr, code := executeTestCommand(t, "report", "finding", "validate",
		findingID, "--operation-key", "report-cli-validation-0001",
		"--reason", "frozen Artifact confirms the mock workflow")
	if code != 0 || stderr != "" ||
		findingValidationIDPattern.FindString(validatedReplay) != validationID ||
		!strings.Contains(validatedReplay, "reused") {
		t.Fatalf("finding validation replay drifted output=%s stderr=%s code=%d",
			validatedReplay, stderr, code)
	}
	verified, stderr, code := executeTestCommand(t, "report", "finding", "verify", findingID)
	if code != 0 || stderr != "" || !strings.Contains(verified, "status: validated") ||
		!strings.Contains(verified, "validation_artifact_evidence_count: 1") ||
		!strings.Contains(verified, "remediation_evidence_count: 0") {
		t.Fatalf("finding verification failed output=%s stderr=%s code=%d",
			verified, stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "report", "finding", "reject", findingID,
		"--operation-key", "report-cli-rejection-0001", "--reason", "changed decision")
	if code != apperror.ExitCode(apperror.New(apperror.CodeConflict, "conflict")) ||
		!strings.Contains(stderr, "already validated") {
		t.Fatalf("second finding decision did not conflict stderr=%s code=%d", stderr, code)
	}
	validatedJSON, stderr, code := executeTestCommand(t, "report", "show",
		findingReport.ID, "--format", "json")
	var validatedReport domain.FindingReport
	if code != 0 || stderr != "" ||
		json.Unmarshal([]byte(validatedJSON), &validatedReport) != nil ||
		validatedReport.ProjectionDigest != findingReport.ProjectionDigest ||
		validatedReport.Findings[0].Validation == nil ||
		validatedReport.Findings[0].Validation.Status != domain.FindingStatusValidated ||
		len(validatedReport.Findings[0].ArtifactEvidence) != 1 {
		t.Fatalf("validated JSON report drifted output=%s stderr=%s code=%d",
			validatedJSON, stderr, code)
	}
	sarifJSON, stderr, code := executeTestCommand(t, "report", "show",
		findingReport.ID, "--format", "sarif")
	var sarifReport struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []struct {
				Kind       string         `json:"kind"`
				Level      string         `json:"level"`
				Properties map[string]any `json:"properties"`
			} `json:"results"`
		} `json:"runs"`
	}
	if code != 0 || stderr != "" ||
		json.Unmarshal([]byte(sarifJSON), &sarifReport) != nil ||
		sarifReport.Version != reporting.SARIFVersion || len(sarifReport.Runs) != 1 ||
		len(sarifReport.Runs[0].Results) != 1 ||
		strings.Contains(sarifJSON, "baselineState") ||
		strings.Contains(sarifJSON, "suppressions") {
		t.Fatalf("stored SARIF report failed output=%s stderr=%s code=%d",
			sarifJSON, stderr, code)
	}
	sarifResult := sarifReport.Runs[0].Results[0]
	if sarifResult.Properties["cyberagentValidationStatus"] != "validated" ||
		sarifResult.Kind != "fail" || sarifResult.Level != "note" {
		t.Fatalf("validated-only SARIF boundary drifted: %#v", sarifResult)
	}
	checkText, stderr, code := executeTestCommand(t, "report", "check", findingReport.ID)
	if code != 0 || stderr != "" || !strings.Contains(checkText, "matched: 0") ||
		!strings.Contains(checkText, "passed: true") {
		t.Fatalf("default report gate should ignore validated info output=%s stderr=%s code=%d",
			checkText, stderr, code)
	}
	checkJSON, stderr, code := executeTestCommand(t, "report", "check", findingReport.ID,
		"--min-severity", "info", "--format", "json")
	var validatedGate reporting.GateResult
	if code != apperror.ExitCode(apperror.New(apperror.CodeFailedPrecondition, "failed")) ||
		!strings.Contains(stderr, "report check matched 1 validated finding") ||
		json.Unmarshal([]byte(checkJSON), &validatedGate) != nil ||
		validatedGate.MatchedCount != 1 || validatedGate.Passed {
		t.Fatalf("validated report gate drifted output=%s stderr=%s code=%d",
			checkJSON, stderr, code)
	}
	githubAnnotations, stderr, code := executeTestCommand(t, "report", "check",
		findingReport.ID, "--min-severity", "info", "--format", "github")
	if code != apperror.ExitCode(apperror.New(apperror.CodeFailedPrecondition, "failed")) ||
		!strings.Contains(stderr, "report check matched 1 validated finding") ||
		strings.Count(githubAnnotations, "\n") != 1 ||
		!strings.HasPrefix(githubAnnotations, "::notice file=") ||
		!strings.Contains(githubAnnotations, "status=validated;") ||
		strings.Contains(githubAnnotations, "Artifact confirms the finding") ||
		strings.Contains(githubAnnotations, "reproduction output") {
		t.Fatalf("GitHub annotation gate drifted output=%s stderr=%s code=%d",
			githubAnnotations, stderr, code)
	}
	activeJSON, stderr, code := executeTestCommand(t, "report", "check", findingReport.ID,
		"--fail-status", "active", "--min-severity", "info", "--format", "json")
	var activeGate reporting.GateResult
	if code != apperror.ExitCode(apperror.New(apperror.CodeFailedPrecondition, "failed")) ||
		json.Unmarshal([]byte(activeJSON), &activeGate) != nil ||
		activeGate.MatchedCount != 6 || activeGate.Passed {
		t.Fatalf("active report gate did not admit drafts output=%s stderr=%s code=%d",
			activeJSON, stderr, code)
	}
	disabledText, stderr, code := executeTestCommand(t, "report", "check", findingReport.ID,
		"--fail-status", "none", "--min-severity", "info")
	if code != 0 || stderr != "" || !strings.Contains(disabledText, "matched: 0") ||
		!strings.Contains(disabledText, "passed: true") {
		t.Fatalf("disabled report gate failed output=%s stderr=%s code=%d",
			disabledText, stderr, code)
	}
	reportMarkdown, stderr, code := executeTestCommand(t, "report", "show",
		findingReport.ID, "--format", "markdown")
	if code != 0 || stderr != "" ||
		!strings.Contains(reportMarkdown, "# Read-only Fan-out Audit Report") ||
		!strings.Contains(reportMarkdown, findingReport.ID) ||
		!strings.Contains(reportMarkdown, "- Validated: 1") ||
		!strings.Contains(reportMarkdown, "Validation Artifact Evidence:") ||
		!strings.Contains(reportMarkdown, "Mock review observation") {
		t.Fatalf("stored Markdown report failed output=%s stderr=%s code=%d",
			reportMarkdown, stderr, code)
	}
	accepted, stderr, code := executeTestCommand(t, "report", "finding", "accept",
		findingID, "--operation-key", "report-cli-acceptance-0001",
		"--reason", "operator accepts the validated finding")
	acceptanceID := findingAcceptanceIDPattern.FindString(accepted)
	if code != 0 || stderr != "" || acceptanceID == "" ||
		!strings.Contains(accepted, "status: accepted") ||
		!strings.Contains(accepted, "validation: "+validationID) ||
		!strings.Contains(accepted, "validation_artifact_evidence_count: 1") {
		t.Fatalf("finding acceptance failed output=%s stderr=%s code=%d",
			accepted, stderr, code)
	}
	acceptedReplay, stderr, code := executeTestCommand(t, "report", "finding", "accept",
		findingID, "--operation-key", "report-cli-acceptance-0001",
		"--reason", "operator accepts the validated finding")
	if code != 0 || stderr != "" ||
		findingAcceptanceIDPattern.FindString(acceptedReplay) != acceptanceID ||
		!strings.Contains(acceptedReplay, "reused") {
		t.Fatalf("finding acceptance replay drifted output=%s stderr=%s code=%d",
			acceptedReplay, stderr, code)
	}
	verified, stderr, code = executeTestCommand(t, "report", "finding", "verify", findingID)
	if code != 0 || stderr != "" || !strings.Contains(verified, "status: accepted") ||
		!strings.Contains(verified, "acceptance: "+acceptanceID) ||
		!strings.Contains(verified, "remediation_evidence_count: 0") {
		t.Fatalf("accepted finding verification failed output=%s stderr=%s code=%d",
			verified, stderr, code)
	}
	acceptedSARIF, stderr, code := executeTestCommand(t, "report", "show",
		findingReport.ID, "--format", "sarif")
	if code != 0 || stderr != "" ||
		json.Unmarshal([]byte(acceptedSARIF), &sarifReport) != nil ||
		len(sarifReport.Runs) != 1 || len(sarifReport.Runs[0].Results) != 1 ||
		sarifReport.Runs[0].Results[0].Properties["cyberagentFindingStatus"] != "accepted" ||
		sarifReport.Runs[0].Results[0].Properties["cyberagentValidationStatus"] != "validated" {
		t.Fatalf("accepted SARIF projection failed output=%s stderr=%s code=%d",
			acceptedSARIF, stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "report", "finding", "fix", findingID,
		"--operation-key", "report-cli-fix-without-remediation-0001",
		"--reason", "must not fix without fresh evidence")
	if code != apperror.ExitCode(apperror.New(apperror.CodeFailedPrecondition, "failed")) ||
		!strings.Contains(stderr, "requires fresh remediation") {
		t.Fatalf("evidence-free fix was not rejected stderr=%s code=%d", stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "report", "finding", "remediation", "attach",
		findingID, artifactID, "--operation-key", "report-cli-remediation-reuse-0001",
		"--note", "must not reuse validation Artifact")
	if code != apperror.ExitCode(apperror.New(apperror.CodeFailedPrecondition, "failed")) ||
		!strings.Contains(stderr, "fresh Artifact") {
		t.Fatalf("validation Artifact reuse was not rejected stderr=%s code=%d", stderr, code)
	}
	remediationProposal, stderr, code := executeTestCommand(t, "session", "send", sessionID,
		"/run echo report-remediation-evidence")
	remediationToolID := toolIDPattern.FindString(remediationProposal)
	if code != 0 || stderr != "" || remediationToolID == "" {
		t.Fatalf("remediation Artifact proposal failed output=%s stderr=%s code=%d",
			remediationProposal, stderr, code)
	}
	remediationApproved, stderr, code := executeTestCommand(t, "tool", "approve",
		remediationToolID)
	remediationArtifactID := artifactIDPattern.FindString(remediationApproved)
	if code != 0 || stderr != "" || remediationArtifactID == "" {
		t.Fatalf("remediation Artifact approval failed output=%s stderr=%s code=%d",
			remediationApproved, stderr, code)
	}
	remediationAttached, stderr, code := executeTestCommand(t, "report", "finding",
		"remediation", "attach", findingID, remediationArtifactID,
		"--operation-key", "report-cli-remediation-evidence-0001",
		"--note", "fresh Artifact confirms remediation")
	remediationEvidenceID := findingRemediationEvidenceIDPattern.FindString(remediationAttached)
	if code != 0 || stderr != "" || remediationEvidenceID == "" ||
		!strings.Contains(remediationAttached, "attached") {
		t.Fatalf("remediation Evidence attach failed output=%s stderr=%s code=%d",
			remediationAttached, stderr, code)
	}
	remediationReplay, stderr, code := executeTestCommand(t, "report", "finding",
		"remediation", "attach", findingID, remediationArtifactID,
		"--operation-key", "report-cli-remediation-evidence-0001",
		"--note", "fresh Artifact confirms remediation")
	if code != 0 || stderr != "" ||
		findingRemediationEvidenceIDPattern.FindString(remediationReplay) != remediationEvidenceID ||
		!strings.Contains(remediationReplay, "reused") {
		t.Fatalf("remediation Evidence replay drifted output=%s stderr=%s code=%d",
			remediationReplay, stderr, code)
	}
	fixed, stderr, code := executeTestCommand(t, "report", "finding", "fix", findingID,
		"--operation-key", "report-cli-fix-0001",
		"--reason", "fresh remediation evidence confirms the correction")
	fixID := findingFixIDPattern.FindString(fixed)
	if code != 0 || stderr != "" || fixID == "" ||
		!strings.Contains(fixed, "status: fixed") ||
		!strings.Contains(fixed, "acceptance: "+acceptanceID) ||
		!strings.Contains(fixed, "remediation_evidence_count: 1") {
		t.Fatalf("finding fix failed output=%s stderr=%s code=%d", fixed, stderr, code)
	}
	fixedReplay, stderr, code := executeTestCommand(t, "report", "finding", "fix", findingID,
		"--operation-key", "report-cli-fix-0001",
		"--reason", "fresh remediation evidence confirms the correction")
	if code != 0 || stderr != "" || findingFixIDPattern.FindString(fixedReplay) != fixID ||
		!strings.Contains(fixedReplay, "reused") {
		t.Fatalf("finding fix replay drifted output=%s stderr=%s code=%d",
			fixedReplay, stderr, code)
	}
	verified, stderr, code = executeTestCommand(t, "report", "finding", "verify", findingID)
	if code != 0 || stderr != "" || !strings.Contains(verified, "status: fixed") ||
		!strings.Contains(verified, "fix: "+fixID) ||
		!strings.Contains(verified, "remediation_evidence_count: 1") {
		t.Fatalf("fixed finding verification failed output=%s stderr=%s code=%d",
			verified, stderr, code)
	}
	fixedJSON, stderr, code := executeTestCommand(t, "report", "show",
		findingReport.ID, "--format", "json")
	var fixedReport domain.FindingReport
	if code != 0 || stderr != "" || json.Unmarshal([]byte(fixedJSON), &fixedReport) != nil ||
		fixedReport.ProjectionDigest != findingReport.ProjectionDigest ||
		fixedReport.Findings[0].EffectiveStatus() != domain.FindingStatusFixed ||
		fixedReport.Findings[0].Acceptance == nil || fixedReport.Findings[0].Fix == nil ||
		len(fixedReport.Findings[0].RemediationEvidence) != 1 {
		t.Fatalf("fixed JSON report drifted output=%s stderr=%s code=%d",
			fixedJSON, stderr, code)
	}
	fixedSARIF, stderr, code := executeTestCommand(t, "report", "show",
		findingReport.ID, "--format", "sarif")
	if code != 0 || stderr != "" || json.Unmarshal([]byte(fixedSARIF), &sarifReport) != nil ||
		len(sarifReport.Runs) != 1 || len(sarifReport.Runs[0].Results) != 0 {
		t.Fatalf("fixed finding remained in SARIF output=%s stderr=%s code=%d",
			fixedSARIF, stderr, code)
	}
	fixedGateText, stderr, code := executeTestCommand(t, "report", "check",
		findingReport.ID, "--min-severity", "info")
	if code != 0 || stderr != "" || !strings.Contains(fixedGateText, "fixed: 1") ||
		!strings.Contains(fixedGateText, "matched: 0") ||
		!strings.Contains(fixedGateText, "passed: true") {
		t.Fatalf("fixed finding still blocked validated gate output=%s stderr=%s code=%d",
			fixedGateText, stderr, code)
	}
	fixedGitHub, stderr, code := executeTestCommand(t, "report", "check",
		findingReport.ID, "--min-severity", "info", "--format", "github")
	if code != 0 || stderr != "" || fixedGitHub != "" {
		t.Fatalf("fixed finding emitted GitHub annotation output=%s stderr=%s code=%d",
			fixedGitHub, stderr, code)
	}
	fixedActiveJSON, stderr, code := executeTestCommand(t, "report", "check",
		findingReport.ID, "--fail-status", "active", "--min-severity", "info",
		"--format", "json")
	var fixedActiveGate reporting.GateResult
	if code != apperror.ExitCode(apperror.New(apperror.CodeFailedPrecondition, "failed")) ||
		json.Unmarshal([]byte(fixedActiveJSON), &fixedActiveGate) != nil ||
		fixedActiveGate.MatchedCount != 5 || fixedActiveGate.FixedCount != 1 ||
		fixedActiveGate.Passed {
		t.Fatalf("fixed active gate drifted output=%s stderr=%s code=%d",
			fixedActiveJSON, stderr, code)
	}
	fixedMarkdown, stderr, code := executeTestCommand(t, "report", "show",
		findingReport.ID, "--format", "markdown")
	if code != 0 || stderr != "" || !strings.Contains(fixedMarkdown, "- Fixed: 1") ||
		!strings.Contains(fixedMarkdown, "Acceptance decision:") ||
		!strings.Contains(fixedMarkdown, "Remediation Artifact Evidence:") ||
		!strings.Contains(fixedMarkdown, "Fix decision:") {
		t.Fatalf("fixed Markdown lifecycle failed output=%s stderr=%s code=%d",
			fixedMarkdown, stderr, code)
	}
	usage, stderr, code := executeTestCommand(t, "run", "usage", runID)
	if code != 0 || stderr != "" ||
		!strings.Contains(usage, "agent_readonly_fanout_tokens:") {
		t.Fatalf("fan-out usage is missing output=%s stderr=%s code=%d",
			usage, stderr, code)
	}
	graph, stderr, code := executeTestCommand(t, "run", "graph", runID)
	if code != 0 || stderr != "" || !strings.Contains(graph, "nodes: 1") {
		t.Fatalf("fan-out plan changed Agent graph output=%s stderr=%s code=%d", graph, stderr, code)
	}
	timeline, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || stderr != "" ||
		strings.Count(timeline, events.ReadOnlyFanoutPlannedEvent) != 1 ||
		strings.Count(timeline, events.ReadOnlyFanoutExecutionStartedEvent) != 1 ||
		strings.Count(timeline, events.ReadOnlyFanoutShardCompletedEvent) != 6 ||
		strings.Count(timeline, events.ReadOnlyFanoutExecutionCompletedEvent) != 1 ||
		strings.Count(timeline, events.FindingReportGeneratedEvent) != 1 ||
		strings.Count(timeline, events.FindingArtifactEvidenceAttachedEvent) != 1 ||
		strings.Count(timeline, events.FindingValidationDecidedEvent) != 1 ||
		strings.Count(timeline, events.FindingAcceptedEvent) != 1 ||
		strings.Count(timeline, events.FindingRemediationEvidenceAttachedEvent) != 1 ||
		strings.Count(timeline, events.FindingFixedEvent) != 1 {
		t.Fatalf("fan-out timeline drifted output=%s stderr=%s code=%d", timeline, stderr, code)
	}
}

func TestRunGraphShowsDurableRootProjection(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	stdout, stderr, code := executeTestCommand(t, "run", "create", "graph root", "--profile", "code")
	if code != 0 {
		t.Fatalf("run create failed: stderr=%s", stderr)
	}
	runID := runIDPattern.FindString(stdout)
	if runID == "" {
		t.Fatalf("run id missing from create output: %s", stdout)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "graph", runID)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "nodes: 1") ||
		!strings.Contains(stdout, "role=root") || !strings.Contains(stdout, "status=ready") ||
		!strings.Contains(stdout, "snapshot_protocol: agent_graph.v1") {
		t.Fatalf("unexpected run graph output: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
}

func TestRunDelegationReviewCLIIsExplicitAndIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	stdout, stderr, code := executeTestCommand(t, "run", "create", "review delegation",
		"--profile", "code", "--max-turns", "8", "--max-tokens", "2000")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(stdout)
	if runID == "" {
		t.Fatalf("run id missing: %s", stdout)
	}
	if _, stderr, code = executeTestCommand(t, "run", "start", runID); code != 0 {
		t.Fatalf("run start failed: %s", stderr)
	}
	st, err := store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root, _, err := st.RegisterRootAgent(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	acquisition, err := st.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{
			RunID: runID, OwnerID: "delegation-cli-test", TTL: time.Minute,
		})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.BeginSupervisorTurn(ctx, acquisition.Lease, "review delegation")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(domain.SpecialistDelegationSpec{
		Version: domain.SpecialistDelegationVersion,
		Assignments: []domain.SpecialistDelegationAssignment{{
			Title: "Inspect parser", Goal: "Review parser input boundaries",
			Skills: []string{"model.chat"}, TurnLimit: 3, TokenLimit: 512,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := toolgateway.New(st, policy.NewDefaultChecker()).
		WithSpecialistDelegationExecutor(application.NewSpecialistDelegationToolExecutor(st)).
		Invoke(ctx, toolgateway.ToolCall{
			Name: toolgateway.SpecialistDelegationProposeTool, Payload: payload,
			OperationKey: "delegation-cli-proposal", RunID: runID, AgentID: root.ID,
			SessionID: turn.Agent.SessionID, RequestedBy: "run_supervisor",
			LeaseID: acquisition.Lease.LeaseID, LeaseGeneration: acquisition.Lease.Generation,
		})
	if err != nil || outcome.Result == nil {
		t.Fatalf("proposal creation failed: outcome=%#v err=%v", outcome, err)
	}
	proposalID := outcome.Result.Metadata["proposal_id"]
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	operationKey := "delegation-cli-review-0001"
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", "approve", proposalID,
		"--operation-key", operationKey, "--reason", "bounded review")
	if code != 0 || stderr != "" || !delegationReviewIDPattern.MatchString(stdout) ||
		!strings.Contains(stdout, "decision: approved") ||
		!strings.Contains(stdout, "admission_authorized: false") ||
		!strings.Contains(stdout, "replayed: false") {
		t.Fatalf("unexpected approval output: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", "approve", proposalID,
		"--operation-key", operationKey, "--reason", "bounded review")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "replayed: true") {
		t.Fatalf("review replay failed: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", proposalID)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "review: approved") ||
		!strings.Contains(stdout, "application_required: true") {
		t.Fatalf("review detail is incomplete: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "delegations", runID)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "review=approved") {
		t.Fatalf("review list is incomplete: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "run", "delegation", "reject", proposalID,
		"--operation-key", "delegation-cli-review-0002", "--reason", "changed decision")
	if code != apperror.ExitCode(apperror.New(apperror.CodeConflict, "conflict")) ||
		!strings.Contains(stderr, "already approved") {
		t.Fatalf("second decision did not conflict: stderr=%s code=%d", stderr, code)
	}
	st, err = store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	modelAttempt := llm.ModelAttempt{
		Number: 1, TransportAttempt: 1, MaxAttempts: 1,
		Provider: "test", Model: "test-model",
	}
	if inserted, err := st.RecordSupervisorModelStarted(ctx, turn.Checkpoint,
		modelAttempt); err != nil || !inserted {
		t.Fatalf("model start failed: inserted=%t err=%v", inserted, err)
	}
	modelAttempt.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{
		Text: "delegation reviewed", Provider: "test", Model: "test-model",
		Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}
	checkpoint, err := st.RecordSupervisorModelCompleted(ctx, turn.Checkpoint,
		modelAttempt, response)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.CompleteSupervisorTurn(ctx, checkpoint, response,
		domain.RootAction{
			Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue,
			Message: "delegation reviewed",
		}, policy.Decision{Allowed: true, Reason: "allowed by test Policy"},
		time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(ctx, acquisition.Lease); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	applicationKey := "delegation-cli-application-0001"
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", "apply", proposalID,
		"--operation-key", applicationKey)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "status: applied") ||
		!strings.Contains(stdout, "admission_authorized: true") ||
		!strings.Contains(stdout, "scheduling_started: false") ||
		!strings.Contains(stdout, "replayed: false") || !agentIDPattern.MatchString(stdout) {
		t.Fatalf("unexpected application output: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", "apply", proposalID,
		"--operation-key", applicationKey)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "replayed: true") {
		t.Fatalf("application replay failed: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", proposalID)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "application: applied") ||
		!strings.Contains(stdout, "scheduling_started: false") {
		t.Fatalf("application detail is incomplete: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
	scheduleKey := "delegation-cli-schedule-0001"
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", "schedule", proposalID,
		"--operation-key", scheduleKey)
	if code != 0 || stderr != "" ||
		!strings.Contains(stdout, "schedule_request: operator-schedule-") ||
		!strings.Contains(stdout, "operator_controlled: true") ||
		!strings.Contains(stdout, "status: completed") ||
		!strings.Contains(stdout, "turns_started: 1") ||
		!strings.Contains(stdout, "replayed: false") {
		t.Fatalf("unexpected operator schedule output: stdout=%s stderr=%s code=%d",
			stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", "schedule", proposalID,
		"--operation-key", scheduleKey)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "replayed: true") {
		t.Fatalf("operator schedule replay failed: stdout=%s stderr=%s code=%d",
			stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", "continue", proposalID,
		"--operation-key", "delegation-cli-continue-0002")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "status: completed") ||
		!strings.Contains(stdout, "turns_started: 1") ||
		!strings.Contains(stdout, "replayed: false") {
		t.Fatalf("operator continuation failed: stdout=%s stderr=%s code=%d",
			stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "delegation", proposalID)
	if code != 0 || stderr != "" ||
		!strings.Contains(stdout, "scheduling_requested: true") ||
		!strings.Contains(stdout, "scheduling_started: true") ||
		!strings.Contains(stdout, "schedule_status: completed") {
		t.Fatalf("operator schedule detail is incomplete: stdout=%s stderr=%s code=%d",
			stdout, stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "run", "delegation", "schedule", proposalID,
		"--operation-key", "delegation-cli-wrong-operator", "--operator", "other_operator")
	if code != apperror.ExitCode(apperror.New(apperror.CodeFailedPrecondition, "failed")) ||
		!strings.Contains(stderr, "application operator") {
		t.Fatalf("different schedule operator was accepted: stderr=%s code=%d", stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "tool", "invoke", "specialist_operator_schedule",
		"--run", runID, "--operation-key", "delegation-tool-bypass-0001", "--payload", `{}`)
	if code == 0 || !strings.Contains(stderr, "not an invocable structured memory tool") {
		t.Fatalf("ordinary tool bypass reached operator scheduling: stderr=%s code=%d",
			stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "delegations", runID)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "application=applied") {
		t.Fatalf("application list is incomplete: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
	stdout, stderr, code = executeTestCommand(t, "run", "graph", runID)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "nodes: 2") ||
		!strings.Contains(stdout, "role=specialist") || !strings.Contains(stdout, "status=ready") {
		t.Fatalf("application graph is incomplete: stdout=%s stderr=%s code=%d", stdout, stderr, code)
	}
}

func TestExecuteContextCancelsProviderAndPersistsFailure(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	entered := make(chan struct{})
	noncePattern := regexp.MustCompile(`[a-f0-9]{32}`)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read Provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test Provider does not support streaming")
			return
		}
		current := requestCount.Add(1)
		if current <= 2 {
			nonce := noncePattern.FindString(string(body))
			if nonce == "" {
				t.Errorf("qualification request %d omitted nonce: %s", current, body)
				return
			}
			var payloads []string
			if current == 1 {
				partial, _ := json.Marshal(`{"nonce":"` + nonce + `"}`)
				payloads = []string{
					`{"type":"message_start","message":{"model":"test-model","usage":{"input_tokens":1,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"probe-call","name":"prayu_harness_echo","input":{}}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + string(partial) + `}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"message_delta","usage":{"output_tokens":1}}`,
					`{"type":"message_stop"}`,
				}
			} else {
				text, _ := json.Marshal(`{"version":"model_harness_probe.v1","status":"ok","nonce":"` +
					nonce + `"}`)
				payloads = []string{
					`{"type":"message_start","message":{"model":"test-model","usage":{"input_tokens":1,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(text) + `}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"message_delta","usage":{"output_tokens":1}}`,
					`{"type":"message_stop"}`,
				}
			}
			for _, payload := range payloads {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
				flusher.Flush()
			}
			return
		}
		flusher.Flush()
		close(entered)
		<-request.Context().Done()
	}))
	defer server.Close()
	t.Setenv("MIMO_API_KEY", "test-provider-key")
	t.Setenv("MIMO_BASE_URL", server.URL)
	t.Setenv("MIMO_MODEL", "test-model")
	qualified, stderr, code := executeTestCommand(t, "provider", "qualify",
		"mimo/test-model")
	if code != 0 || stderr != "" || !strings.Contains(qualified, "root_eligible: true") {
		t.Fatalf("Harness qualification failed: output=%q stderr=%q code=%d",
			qualified, stderr, code)
	}

	created, stderr, code := executeTestCommand(t, "run", "create", "signal cancellation", "--profile", "review", "--route", "mimo/test-model")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	if runID == "" {
		t.Fatalf("missing run id: %s", created)
	}
	if _, stderr, code := executeTestCommand(t, "run", "start", runID); code != 0 {
		t.Fatalf("run start failed: %s", stderr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	type commandResult struct {
		stderr string
		code   int
	}
	done := make(chan commandResult, 1)
	go func() {
		var out bytes.Buffer
		var errOut bytes.Buffer
		code := ExecuteContext(ctx, []string{"run", "step", runID}, &out, &errOut)
		done <- commandResult{stderr: errOut.String(), code: code}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("streaming provider was not called")
	}
	cancel()
	var result commandResult
	select {
	case result = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled CLI context did not stop the provider call")
	}
	if result.code != 7 || !strings.Contains(result.stderr, "context canceled") {
		t.Fatalf("unexpected cancelled command result: code=%d stderr=%s", result.code, result.stderr)
	}
	timeline, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 {
		t.Fatalf("run events failed: %s", stderr)
	}
	if strings.Count(timeline, "model.failed") != 1 || !strings.Contains(timeline, `"outcome":"cancelled"`) {
		t.Fatalf("provider cancellation was not durably audited: %s", timeline)
	}
	checkpoint, stderr, code := executeTestCommand(t, "run", "checkpoint", runID)
	if code != 0 || !strings.Contains(checkpoint, "phase: turn_started") {
		t.Fatalf("cancelled turn was not recoverable: code=%d stderr=%s checkpoint=%s", code, stderr, checkpoint)
	}
}

func TestRunCLIEndToEndLifecycle(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	if _, stderr, code := executeTestCommand(t, "workspace", "init", "demo"); code != 0 {
		t.Fatalf("workspace init failed: %s", stderr)
	}
	created, stderr, code := executeTestCommand(t, "run", "create", "review this workspace", "--workspace", "demo", "--profile", "review", "--max-turns", "12")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	sessionID := sessionIDPattern.FindString(created)
	if runID == "" || sessionID == "" || !strings.Contains(created, "status: created") {
		t.Fatalf("unexpected create output: %s", created)
	}
	initialEvents, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || !strings.Contains(initialEvents, "run.created") || !strings.Contains(initialEvents, "session.attached") {
		t.Fatalf("unexpected initial events output=%s stderr=%s", initialEvents, stderr)
	}
	chatOutput, stderr, code := executeTestCommand(t, "session", "send", sessionID, "hello run timeline")
	if code != 0 {
		t.Fatalf("session send failed: %s", stderr)
	}
	if !strings.Contains(chatOutput, "[run "+runID+": action=continue status=running]") {
		t.Fatalf("session send did not expose supervised run state: %s", chatOutput)
	}
	toolOutput, stderr, code := executeTestCommand(t, "session", "send", sessionID, "/run echo hello")
	if code != 0 {
		t.Fatalf("tool proposal failed: %s", stderr)
	}
	toolID := toolIDPattern.FindString(toolOutput)
	if toolID == "" {
		t.Fatalf("missing tool id in output: %s", toolOutput)
	}
	if _, stderr, code := executeTestCommand(t, "tool", "approve", toolID); code != 0 {
		t.Fatalf("tool approval failed: %s", stderr)
	}
	editOutput, stderr, code := executeTestCommand(t, "edit", "propose", "--workspace", "demo", "--session", sessionID, "--path", "notes.txt", "--content", "timeline note")
	if code != 0 {
		t.Fatalf("file edit proposal failed: %s", stderr)
	}
	editID := editIDPattern.FindString(editOutput)
	if editID == "" {
		t.Fatalf("missing edit id in output: %s", editOutput)
	}
	if _, stderr, code := executeTestCommand(t, "edit", "review-approve", runID, editID); code != 0 {
		t.Fatalf("file edit review failed: %s", stderr)
	}
	applyOutput, stderr, code := executeTestCommand(t, "edit", "apply", runID, editID,
		"--operation-key", "run-cli-file-apply-0001")
	if code != 0 || !strings.Contains(applyOutput, "file_written: true") {
		t.Fatalf("file edit apply failed: output=%s stderr=%s", applyOutput, stderr)
	}
	for _, step := range []struct {
		action string
		status string
	}{
		{"start", "running"},
		{"pause", "paused"},
		{"resume", "running"},
		{"cancel", "cancelled"},
	} {
		stdout, stderr, code := executeTestCommand(t, "run", step.action, runID)
		if code != 0 || !strings.Contains(stdout, step.status) {
			t.Fatalf("run %s failed output=%s stderr=%s", step.action, stdout, stderr)
		}
	}
	shown, stderr, code := executeTestCommand(t, "run", "show", runID)
	if code != 0 || !strings.Contains(shown, "status: cancelled") || !strings.Contains(shown, `"max_turns":12`) ||
		!strings.Contains(shown, "agent_code_tools_protocol: "+toolgateway.AgentCodeRegistryVersion) ||
		!strings.Contains(shown, "agent_code_tool: workspace_read\tavailable=true") ||
		!strings.Contains(shown, "agent_code_tool: workspace_change\tavailable=false") {
		t.Fatalf("unexpected show output=%s stderr=%s", shown, stderr)
	}
	eventOutput, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || strings.Count(eventOutput, "run.status_changed") != 5 {
		t.Fatalf("unexpected event timeline output=%s stderr=%s", eventOutput, stderr)
	}
	for _, eventType := range []string{"session.message_created", "policy.decision", "tool.proposed", "tool.approved", "tool.completed", "file_edit.proposed", "file_edit.approved", "file_edit.applied"} {
		if !strings.Contains(eventOutput, eventType) {
			t.Fatalf("event timeline missing %s: %s", eventType, eventOutput)
		}
	}
	listed, stderr, code := executeTestCommand(t, "run", "list", "--status", "cancelled")
	if code != 0 || !strings.Contains(listed, runID) {
		t.Fatalf("unexpected list output=%s stderr=%s", listed, stderr)
	}
}

func TestRunCLIAdaptsLegacyTaskIdempotently(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	createdTask, stderr, code := executeTestCommand(t, "script", "new", "adapter smoke", "--workspace", "adapter-demo")
	if code != 0 {
		t.Fatalf("script new failed: %s", stderr)
	}
	taskID := regexp.MustCompile(`task-[0-9]{14}-[a-f0-9]{12}`).FindString(createdTask)
	if taskID == "" {
		t.Fatalf("missing task id: %s", createdTask)
	}
	first, stderr, code := executeTestCommand(t, "run", "adapt-task", taskID)
	if code != 0 || !strings.Contains(first, " adapted") {
		t.Fatalf("first adaptation failed output=%s stderr=%s code=%d", first, stderr, code)
	}
	runID := runIDPattern.FindString(first)
	if runID == "" || sessionIDPattern.FindString(first) == "" {
		t.Fatalf("missing adapted ids: %s", first)
	}
	second, stderr, code := executeTestCommand(t, "run", "adapt-task", taskID)
	if code != 0 || !strings.Contains(second, " reused") || runIDPattern.FindString(second) != runID {
		t.Fatalf("repeat adaptation was not idempotent output=%s stderr=%s code=%d", second, stderr, code)
	}
	timeline, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || strings.Count(timeline, "legacy.task_adapted") != 1 || strings.Count(timeline, "run.created") != 1 {
		t.Fatalf("unexpected adapted timeline output=%s stderr=%s", timeline, stderr)
	}
}

func TestCLIStableExitCodesPreserveErrorText(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	_, stderr, code := executeTestCommand(t, "run", "show")
	if code != 2 || stderr != "error: usage: cyberagent run show <run-id>\n" {
		t.Fatalf("unexpected invalid argument result code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = executeTestCommand(t, "run", "show", "run-missing")
	if code != 3 || stderr != "error: sql: no rows in result set\n" {
		t.Fatalf("unexpected not found result code=%d stderr=%q", code, stderr)
	}
}

func TestRunCLISupervisorStepAndCheckpoint(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	_, stderr, code := executeTestCommand(t, "run", "checkpoint", "run-missing")
	if code != 3 {
		t.Fatalf("unexpected missing checkpoint result code=%d stderr=%s", code, stderr)
	}
	created, stderr, code := executeTestCommand(t, "run", "create", "supervisor cli smoke", "--profile", "review", "--max-turns", "1")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	if runID == "" {
		t.Fatalf("missing run id: %s", created)
	}
	lease, stderr, code := executeTestCommand(t, "run", "lease", runID)
	if code != 0 || !strings.Contains(lease, "has no execution lease") {
		t.Fatalf("unexpected empty lease output=%s stderr=%s code=%d", lease, stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "run", "step", runID)
	if code != 4 || !strings.Contains(stderr, "supervisor requires running") {
		t.Fatalf("unexpected precondition result code=%d stderr=%s", code, stderr)
	}
	if _, stderr, code := executeTestCommand(t, "run", "start", runID); code != 0 {
		t.Fatalf("run start failed: %s", stderr)
	}
	stepped, stderr, code := executeTestCommand(t, "run", "step", runID)
	if code != 0 || !strings.Contains(stepped, "turn 1 completed") || !strings.Contains(stepped, "model_attempts: 1") || !strings.Contains(stepped, "protocol_repairs: 0") || !strings.Contains(stepped, "stream_events: 1") || !strings.Contains(stepped, "stream_bytes:") || !strings.Contains(stepped, "model_outcome: success") || !strings.Contains(stepped, "action: continue") || !strings.Contains(stepped, "run_status: running") || !strings.Contains(stepped, "next_turn: 2") {
		t.Fatalf("unexpected step output=%s stderr=%s code=%d", stepped, stderr, code)
	}
	checkpoint, stderr, code := executeTestCommand(t, "run", "checkpoint", runID)
	if code != 0 || !strings.Contains(checkpoint, "phase: idle") || !strings.Contains(checkpoint, "next_turn: 2") {
		t.Fatalf("unexpected checkpoint output=%s stderr=%s code=%d", checkpoint, stderr, code)
	}
	lease, stderr, code = executeTestCommand(t, "run", "lease", runID)
	if code != 0 || !strings.Contains(lease, "generation: 1") ||
		!strings.Contains(lease, "status: released") || !strings.Contains(lease, "active: false") ||
		strings.Contains(lease, "lease_id") {
		t.Fatalf("unexpected released lease output=%s stderr=%s code=%d", lease, stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "run", "step", runID)
	if code != 8 || !strings.Contains(stderr, "exhausted its 1 turn budget") {
		t.Fatalf("unexpected budget result code=%d stderr=%s", code, stderr)
	}
	timeline, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || strings.Count(timeline, "agent.turn_started") != 1 || strings.Count(timeline, "agent.turn_completed") != 1 || strings.Count(timeline, "model.started") != 1 || strings.Count(timeline, "model.completed") != 1 || strings.Count(timeline, "model.failed") != 0 {
		t.Fatalf("unexpected supervisor timeline output=%s stderr=%s", timeline, stderr)
	}
}

func TestRunCLIExecuteAndFinalize(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	created, stderr, code := executeTestCommand(t, "run", "create", "execute cli smoke", "--profile", "code", "--max-turns", "3", "--max-tokens", "1000", "--timeout", "30s")
	if code != 0 {
		t.Fatalf("run create failed: %s", stderr)
	}
	runID := runIDPattern.FindString(created)
	if runID == "" {
		t.Fatalf("missing run id: %s", created)
	}
	if _, stderr, code := executeTestCommand(t, "run", "start", runID); code != 0 {
		t.Fatalf("run start failed: %s", stderr)
	}
	executed, stderr, code := executeTestCommand(t, "run", "execute", runID, "--max-steps", "2", "--finish", "--summary", "operator verified")
	if code != 0 || strings.Count(executed, "turn ") != 2 || strings.Count(executed, "\tcontinue\t") != 2 || !strings.Contains(executed, "finalized: completed") {
		t.Fatalf("unexpected execute output=%s stderr=%s code=%d", executed, stderr, code)
	}
	shown, stderr, code := executeTestCommand(t, "run", "show", runID)
	if code != 0 || !strings.Contains(shown, "status: completed") {
		t.Fatalf("unexpected finalized run output=%s stderr=%s", shown, stderr)
	}
	checkpoint, stderr, code := executeTestCommand(t, "run", "checkpoint", runID)
	if code != 0 || !strings.Contains(checkpoint, "phase: run_completed") || !strings.Contains(checkpoint, "next_turn: 3") || !strings.Contains(checkpoint, "total_tokens:") {
		t.Fatalf("unexpected finalized checkpoint output=%s stderr=%s", checkpoint, stderr)
	}
	timeline, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || strings.Count(timeline, "supervisor.run_completed") != 1 {
		t.Fatalf("unexpected completion timeline output=%s stderr=%s", timeline, stderr)
	}
	if _, stderr, code := executeTestCommand(t, "run", "finish", runID, "--summary", "repeat"); code != 0 {
		t.Fatalf("repeat finish was not idempotent: %s", stderr)
	}
	after, stderr, code := executeTestCommand(t, "run", "events", runID)
	if code != 0 || strings.Count(after, "supervisor.run_completed") != 1 {
		t.Fatalf("repeat finish duplicated timeline output=%s stderr=%s", after, stderr)
	}

	failedCreated, stderr, code := executeTestCommand(t, "run", "create", "fail cli smoke", "--profile", "review")
	if code != 0 {
		t.Fatalf("failed-run create failed: %s", stderr)
	}
	failedRunID := runIDPattern.FindString(failedCreated)
	if _, stderr, code := executeTestCommand(t, "run", "start", failedRunID); code != 0 {
		t.Fatalf("failed-run start failed: %s", stderr)
	}
	failed, stderr, code := executeTestCommand(t, "run", "fail", failedRunID, "--reason", "operator stopped")
	if code != 0 || !strings.Contains(failed, "finalized: failed") {
		t.Fatalf("unexpected fail output=%s stderr=%s code=%d", failed, stderr, code)
	}
}
