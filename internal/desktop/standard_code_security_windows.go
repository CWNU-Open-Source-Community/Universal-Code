//go:build windows

package desktop

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/packagede2e"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/standardcode"
	"cyberagent-workbench/internal/toolgateway"
)

//go:embed testdata/standard-code-security/probe.go.txt
var standardCodeSecurityProbe []byte

const standardCodeSecurityProbeProtocol = "standard_code_security_probe.v1"

type standardCodeSecurityDriver struct {
	dockerDigest         string
	config               packagede2e.SecurityDriverConfig
	capabilities         domain.ExecutionPermissionRuntimeCapabilities
	local                sandbox.LocalBackend
	plane                *ControlPlane
	gateway              *toolgateway.Gateway
	readToken            string
	goExecutable         string
	localProbeExecutable string
	outsideSentinel      string
	available            map[string]bool
	runs                 map[string]*standardCodeSecurityRun
	recoveryJobsStarted  int
	recoveryJobsReaped   int
	opened               bool
	closed               bool
}

type standardCodeSecurityRun struct {
	backend     string
	fixtureID   string
	workspaceID string
	run         domain.Run
	root        domain.AgentNode
	lease       domain.RunExecutionLease
	adapter     commandruntimeadapter.Identity
	turn        domain.SupervisorTurn
}

type standardCodeSecurityProbeReceipt struct {
	Protocol string `json:"protocol"`
	Case     string `json:"case"`
	Blocked  bool   `json:"blocked"`
	Detail   string `json:"detail"`
}

type standardCodeSecurityCommandRuntimeReceipt struct {
	Version string                             `json:"version"`
	Action  string                             `json:"action"`
	Jobs    []runner.CommandRuntimeJobSnapshot `json:"jobs"`
	Pages   []runner.CommandRuntimeOutputPage  `json:"pages,omitempty"`
}

type standardCodeSecurityObservation struct {
	outcome      toolgateway.Outcome
	job          runner.CommandRuntimeJob
	stdout       *artifact.Blob
	stderr       *artifact.Blob
	receipt      standardCodeSecurityProbeReceipt
	dockerResult *standardcode.Result
	dockerRecord *domain.DockerSandboxRecord
	dockerLogs   *sandbox.DockerLogCaptureReceipt
	recovery     *standardCodeSecurityRecoveryReceipt
	observed     bool
	detail       string
}

// NewStandardCodeSecurityDriver returns the fixed packaged-only #181 driver.
// It accepts no command, environment, workspace, permission, or host path from
// the caller. The Docker value is an immutable digest already admitted by the
// existing Standard Code runtime.
func NewStandardCodeSecurityDriver(dockerDigest string) packagede2e.SecurityMatrixDriver {
	return &standardCodeSecurityDriver{dockerDigest: strings.TrimSpace(dockerDigest),
		available: make(map[string]bool), runs: make(map[string]*standardCodeSecurityRun)}
}

func (d *standardCodeSecurityDriver) Open(ctx context.Context,
	config packagede2e.SecurityDriverConfig,
) ([]packagede2e.SecurityBackendEvidence, error) {
	if d == nil || d.opened || d.closed || ctx == nil || ctx.Err() != nil {
		return nil, errors.New("packaged security driver lifecycle is invalid")
	}
	runtimeRoot, err := filepath.Abs(config.OwnedRuntimeRoot)
	if err != nil || filepath.Clean(runtimeRoot) != runtimeRoot ||
		!pathInside(runtimeRoot, config.FixtureRoot) {
		return nil, errors.New("packaged security driver roots are invalid")
	}
	d.config = config
	d.goExecutable, err = exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("resolve fixed Go probe toolchain: %w", err)
	}
	d.goExecutable, err = filepath.Abs(d.goExecutable)
	if err != nil {
		return nil, err
	}
	d.localProbeExecutable, err = prepareStandardCodeSecurityLocalProbe(ctx,
		runtimeRoot, d.goExecutable)
	if err != nil {
		return nil, fmt.Errorf("build fixed Local Sandbox probe: %w", err)
	}
	d.outsideSentinel, err = prepareStandardCodeSecurityOutsideSentinel(runtimeRoot)
	if err != nil {
		return nil, fmt.Errorf("create fixed outside sentinel: %w", err)
	}
	d.local, err = sandbox.NewPlatformLocalBackend(sandbox.WithLocalOwnerRoot(
		filepath.Join(runtimeRoot, "local-owners")))
	if err != nil {
		return nil, fmt.Errorf("open packaged Local Sandbox: %w", err)
	}
	readiness, err := d.local.Readiness(ctx, sandbox.LocalRuntimeCapabilities{Enabled: true})
	if err != nil || !readiness.Ready {
		_ = d.local.Close()
		d.local = nil
		return nil, fmt.Errorf("prove packaged Local Sandbox readiness: %w", err)
	}
	d.readToken, err = randomDesktopSecurityToken()
	if err != nil {
		_ = d.local.Close()
		return nil, err
	}
	controlToken, err := randomDesktopSecurityToken()
	if err != nil {
		_ = d.local.Close()
		return nil, err
	}
	home := filepath.Join(runtimeRoot, "desktop-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		_ = d.local.Close()
		return nil, err
	}
	d.capabilities = domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	d.plane, err = OpenControlPlane(ControlPlaneConfig{
		DatabasePath: filepath.Join(home, "security-matrix.db"), HomePath: home,
		ReadToken: d.readToken, ControlToken: controlToken,
		RunControlEnabled: true, RunCreationEnabled: true,
		ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities:   d.capabilities,
		LocalSandboxReadiness:             &readiness, LocalSandboxBackend: d.local,
		RunLifecycleEnabled: true, RunExecutionEnabled: true,
		PlanDeliveryControlEnabled: true, ApprovalControlEnabled: true,
		DockerExecutionEnabled:        d.dockerDigest != "",
		StandardCodeDockerImageDigest: d.dockerDigest,
		CredentialStore:               credential.NewMemoryStore(), AppVersion: "packaged-security-181",
	})
	if err != nil {
		_ = d.local.Close()
		d.local = nil
		return nil, fmt.Errorf("open packaged Desktop control plane: %w", err)
	}
	if d.plane.commandRuntime == nil || d.plane.standardCodeDrydocks == nil ||
		d.plane.standardCodePreset == nil || d.plane.policyChecker == nil {
		_ = d.plane.Close()
		_ = d.local.Close()
		return nil, errors.New("packaged Standard Code product path is incomplete")
	}
	d.gateway = toolgateway.New(d.plane.stateStore, d.plane.policyChecker).
		WithCommandRuntimeExecutor(d.plane.commandRuntime)

	adapters := d.plane.commandRuntime.InstalledCommandRuntimeAdapters()
	byMatrixBackend := make(map[string]commandruntimeadapter.Identity)
	for _, adapter := range adapters {
		switch adapter.Backend {
		case application.CommandRuntimeLocalSandboxBackend:
			byMatrixBackend["local"] = adapter
		case application.CommandRuntimeDockerSandboxBackend:
			byMatrixBackend["docker"] = adapter
		}
	}
	backends := make([]packagede2e.SecurityBackendEvidence, 0, 2)
	for _, name := range []string{"local", "docker"} {
		adapter, ready := byMatrixBackend[name]
		d.available[name] = ready
		identityHash, generationHash := hashSecurityValue(struct {
			Backend string                         `json:"backend"`
			Adapter commandruntimeadapter.Identity `json:"adapter"`
		}{Backend: name, Adapter: adapter}), hashSecurityValue(adapter.Generation)
		if !ready {
			identityHash = hashSecurityValue(struct {
				Backend string `json:"backend"`
				State   string `json:"state"`
			}{name, "unavailable"})
			generationHash = hashSecurityValue(struct {
				Backend string `json:"backend"`
				State   string `json:"state"`
			}{name, "no-generation"})
		}
		evidence := packagede2e.SecurityBackendEvidence{Backend: name,
			Availability:   packagede2e.SecurityBackendReady,
			IdentitySHA256: identityHash, GenerationSHA256: generationHash,
			Network: "disabled", Credentials: "none", FullAccessEnabled: false}
		if !ready {
			evidence.Availability = packagede2e.SecurityBackendUnavailable
			evidence.UnavailableSignal = "backend_unavailable"
		}
		backends = append(backends, evidence)
	}
	d.opened = true
	return backends, nil
}

func (d *standardCodeSecurityDriver) Execute(ctx context.Context,
	request packagede2e.SecurityDriverCase,
) (packagede2e.SecurityCaseBackendEvidence, error) {
	started := time.Now().UTC()
	base := packagede2e.SecurityCaseBackendEvidence{Status: packagede2e.SecurityEvidenceFailed,
		ActualOutcome: "deny", ActualSignal: "failed_precondition",
		OperatorCode:   "standard_code.attack.failed_precondition",
		DiagnosticCode: "product.execution_failed", ActualExecution: true,
		StartedAt: started}
	if d == nil || !d.opened || d.closed || ctx == nil || ctx.Err() != nil ||
		!d.available[request.Backend] {
		base.ActualExecution = false
		base.CompletedAt = time.Now().UTC()
		return base, errors.New("packaged security backend is unavailable")
	}
	if request.Attack.ID == "recovery_renderer_close" {
		base.ActualExecution = false
		base.ActualSignal = "unsupported"
		base.OperatorCode = "standard_code.attack.unsupported"
		base.DiagnosticCode = "product.renderer_file_edit_recovery_unimplemented"
		base.CompletedAt = time.Now().UTC()
		return base, errStandardCodeRendererRecoveryUnsupported
	}
	// Each independent attack owns its Run and ledger. Reusing a Run for a
	// fixed matrix would correctly trip the product's no-progress guard.
	run, err := d.securityRun(ctx, request.Backend, request.FixtureID, request.Attack.ID)
	if err != nil {
		base.CompletedAt = time.Now().UTC()
		return base, err
	}
	observation, err := d.runProbe(ctx, run, request)
	if err == nil {
		err = d.verifyCaseSpecificBoundary(ctx, run, request, &observation)
	}
	if err == nil {
		err = run.completeTurn(ctx, d.plane)
	}
	refs, evidenceErr := d.securityEvidence(ctx, run, request, observation)
	base.Evidence = refs
	if err == nil {
		err = evidenceErr
	}
	if err == nil && observation.observed {
		base.Status = packagede2e.SecurityEvidencePassed
		base.ActualOutcome = request.Attack.ExpectedOutcome
		base.ActualSignal = request.Attack.ExpectedSignal
		base.OperatorCode = "standard_code.attack." + request.Attack.ExpectedSignal
		base.DiagnosticCode = "matrix." + request.Attack.ID
	}
	base.CompletedAt = time.Now().UTC()
	return base, err
}

func (d *standardCodeSecurityDriver) securityRun(ctx context.Context, backend,
	fixtureID, instance string,
) (*standardCodeSecurityRun, error) {
	key := backend + "/" + fixtureID
	operationSuffix := backend + "-" + fixtureID
	if instance != "" {
		key += "/" + instance
		operationSuffix += "-" + instance
	}
	if cached := d.runs[key]; cached != nil {
		return cached, nil
	}
	fixtureRoot := filepath.Join(d.config.FixtureRoot, fixtureID)
	workspace, err := d.plane.RegisterWorkspaceDirectory(ctx, fixtureRoot)
	if err != nil {
		return nil, err
	}
	goal := "execute the fixed packaged Standard Code security matrix"
	previewKey := "issue181-preview-" + operationSuffix
	preview, err := d.plane.standardCodePreset.Configure(ctx,
		application.ConfigureStandardCodeRequest{
			Version: domain.StandardCodePresetProtocolVersion, WorkspaceID: workspace.ID,
			Goal: goal, BackendIntent: backend, Action: "configure",
			OperationKey: previewKey, RequestedBy: "operator",
		})
	if err != nil || !preview.TrustRequired || preview.TrustDigest == "" {
		return nil, fmt.Errorf("preview fixed Standard Code workspace trust: %w", err)
	}
	configured, err := d.plane.standardCodePreset.Configure(ctx,
		application.ConfigureStandardCodeRequest{
			Version: domain.StandardCodePresetProtocolVersion, WorkspaceID: workspace.ID,
			Goal: goal, BackendIntent: backend, Action: "configure",
			OperationKey: "issue181-configure-" + operationSuffix,
			RequestedBy:  "operator", ConfirmWorkspaceTrust: true,
			ExpectedTrustDigest: preview.TrustDigest,
		})
	if err != nil || configured.Status != application.StandardCodeResultConfigured ||
		configured.Run == nil || configured.Permission == nil ||
		configured.Permission.Mode != domain.RunExecutionPermissionAsk ||
		configured.CapabilityGrant {
		return nil, fmt.Errorf("configure fixed Standard Code workspace: %w", err)
	}
	workspaceState, found, err := d.plane.stateStore.GetDrydockByRun(ctx, configured.RunID)
	if err != nil || !found || workspaceState.State != runworktree.StateReady {
		return nil, fmt.Errorf("load configured Standard Code Drydock: %w", err)
	}
	initialCheckpoint := workspaceState.LastCheckpointID
	probeDirectory := filepath.Join(workspaceState.Path, ".standard-code-security")
	if err := os.Mkdir(probeDirectory, 0o700); err != nil {
		return nil, err
	}
	probePath := filepath.Join(probeDirectory, "probe.go")
	if err := writeExclusiveSecurityFile(probePath, standardCodeSecurityProbe); err != nil {
		return nil, err
	}
	_ = os.Symlink(d.outsideSentinel, filepath.Join(probeDirectory, "outside-link"))
	checkpoint, err := d.plane.standardCodeDrydocks.Checkpoint(ctx,
		application.DrydockCheckpointRequest{RunID: configured.RunID,
			ExpectedGeneration: workspaceState.Generation,
			OperationKey:       "issue181-probe-checkpoint-" + operationSuffix,
			RequestedBy:        "operator", Title: "Fixed packaged security probe",
			ConfirmObservedChanges: true})
	if err != nil {
		return nil, err
	}
	runs := application.NewRunService(d.plane.stateStore)
	if _, err := runs.ChangePhase(ctx, application.ChangeRunPhaseRequest{
		RunID: configured.RunID, Phase: string(domain.ExecutionPhaseDeliver),
		OperationKey: "issue181-deliver-" + operationSuffix,
		RequestedBy:  "operator", Reason: "execute frozen packaged security matrix",
	}); err != nil {
		return nil, err
	}
	runRecord, err := runs.Start(ctx, configured.RunID)
	if err != nil {
		return nil, err
	}
	root, found, err := d.plane.stateStore.GetRootAgent(ctx, runRecord.ID)
	if err != nil || !found {
		return nil, fmt.Errorf("load Standard Code root Agent: %w", err)
	}
	acquired, err := d.plane.stateStore.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: runRecord.ID,
			OwnerID: "packaged-security-181-" + operationSuffix,
			TTL:     2 * time.Hour})
	if err != nil {
		return nil, err
	}
	adapter, available, err := d.plane.commandRuntime.AdvertisedCommandRuntimeAdapter(
		ctx, runRecord.ID, configured.Permission.Mode)
	if err != nil || !available {
		return nil, fmt.Errorf("advertise exact Standard Code adapter: %w", err)
	}
	result := &standardCodeSecurityRun{backend: backend, fixtureID: fixtureID,
		workspaceID: workspace.ID, run: runRecord, root: root,
		lease: acquired.Lease, adapter: adapter}
	if checkpoint.Workspace.LastCheckpointID == initialCheckpoint {
		return nil, errors.New("fixed security probe checkpoint did not advance")
	}
	d.runs[key] = result
	return result, nil
}

func (d *standardCodeSecurityDriver) runProbe(ctx context.Context,
	run *standardCodeSecurityRun, request packagede2e.SecurityDriverCase,
) (observation standardCodeSecurityObservation, returnErr error) {
	probeArgument, cleanup, verifyHostBoundary, err :=
		d.prepareStandardCodeSecurityProbeBoundary(request)
	if err != nil {
		return standardCodeSecurityObservation{}, err
	}
	defer func() {
		if cleanup != nil {
			returnErr = errors.Join(returnErr, cleanup())
		}
	}()
	artifactBytes := 128 * 1024
	if strings.HasPrefix(request.Attack.ID, "output_") {
		artifactBytes = 64 * 1024
	}
	maxBytes := 32 * 1024
	executable := d.goExecutable
	arguments := []string{"run", ".standard-code-security/probe.go",
		request.Attack.ID, probeArgument}
	if request.Backend == "local" {
		executable = d.localProbeExecutable
		arguments = []string{request.Attack.ID, probeArgument}
	}
	action := toolgateway.CommandRuntimeActionRun
	timeoutMilliseconds := int64(24_000)
	if request.Backend == "docker" {
		// Docker admission, container lifecycle, bounded log capture, cleanup,
		// and the final Drydock checkpoint are intentionally asynchronous. Use
		// the product's background Job path instead of exceeding the 25-second
		// foreground budget or retrying a timed-out execution.
		action = toolgateway.CommandRuntimeActionStart
		timeoutMilliseconds = int64((60 * time.Second).Milliseconds())
	}
	input := toolgateway.CommandRuntimeInput{
		Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action:  action,
		Commands: []runner.CommandRuntimeSpec{{
			Version: runner.CommandRuntimeProtocolVersion,
			Profile: runner.CommandRuntimeProcess, Executable: executable,
			Arguments:        arguments,
			WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: timeoutMilliseconds,
			Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 32 * 1024,
				ArtifactBytes: artifactBytes},
			Network:     runner.CommandRuntimeNetworkDisabled,
			Credentials: runner.CommandRuntimeCredentialsNone,
			Purpose:     "execute one fixed packaged Standard Code matrix probe",
		}},
	}
	if action == toolgateway.CommandRuntimeActionRun {
		input.FailurePolicy = toolgateway.CommandRuntimeFailFast
		input.MaxBytes = &maxBytes
	}
	outcome, jobID, err := d.invokeStandardCodeSecurityCommand(ctx, run,
		input)
	if err != nil {
		return standardCodeSecurityObservation{}, err
	}
	observation = standardCodeSecurityObservation{outcome: outcome}
	if outcome.Result == nil || outcome.Result.Status != toolgateway.StatusCompleted {
		return observation, errors.New("fixed probe did not produce a completed tool receipt")
	}
	if jobID == "" {
		return observation, errors.New("fixed probe omitted its durable Job identity")
	}
	observation.job, err = d.plane.stateStore.GetCommandRuntimeJob(ctx, jobID)
	if err != nil {
		return observation, err
	}
	if id := outcome.Result.Metadata["job_1_artifact_stdout_id"]; id != "" {
		blob, blobErr := d.plane.stateStore.GetRunArtifact(ctx, id)
		if blobErr != nil {
			return observation, blobErr
		}
		observation.stdout = &blob
	}
	if id := outcome.Result.Metadata["job_1_artifact_stderr_id"]; id != "" {
		blob, blobErr := d.plane.stateStore.GetRunArtifact(ctx, id)
		if blobErr != nil {
			return observation, blobErr
		}
		observation.stderr = &blob
	}
	if !observation.job.State.Terminal() || !observation.job.TreeReaped ||
		observation.job.Network != runner.CommandRuntimeNetworkDisabled ||
		observation.job.Credentials != runner.CommandRuntimeCredentialsNone {
		return observation, errors.New("fixed probe process boundary is incomplete")
	}
	if request.Backend == "docker" {
		if err := d.observeStandardCodeSecurityDocker(ctx, run, request,
			arguments, &observation); err != nil {
			return observation, err
		}
	} else {
		observation.receipt = parseSecurityProbeReceipt(observation.stdout,
			request.Attack.ID)
		observation.observed = observation.receipt.Protocol == standardCodeSecurityProbeProtocol &&
			observation.receipt.Case == request.Attack.ID && observation.receipt.Blocked
	}
	observation.detail = observation.receipt.Detail
	if verifyHostBoundary != nil {
		if err := verifyHostBoundary(); err != nil {
			return observation, err
		}
	}
	return observation, nil
}

func (d *standardCodeSecurityDriver) invokeStandardCodeSecurityCommand(ctx context.Context,
	run *standardCodeSecurityRun, input toolgateway.CommandRuntimeInput,
) (toolgateway.Outcome, string, error) {
	call, err := run.prepareCommand(ctx, d.plane, d.capabilities, input)
	if err != nil {
		return toolgateway.Outcome{}, "", err
	}
	outcome, err := run.invokeCommand(ctx, d.plane, d.gateway, call)
	if err != nil {
		return toolgateway.Outcome{}, "", err
	}
	if input.Action != toolgateway.CommandRuntimeActionStart {
		if outcome.Result == nil {
			return outcome, "", errors.New("fixed foreground probe omitted its result")
		}
		return outcome, outcome.Result.Metadata["job_1_id"], nil
	}
	receipt, err := decodeStandardCodeSecurityCommandRuntimeReceipt(outcome,
		toolgateway.CommandRuntimeActionStart)
	if err != nil || len(receipt.Jobs) != 1 || receipt.Jobs[0].ID == "" {
		return outcome, "", errors.Join(err,
			errors.New("fixed background probe omitted its durable Job identity"))
	}
	jobID := receipt.Jobs[0].ID
	waitCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	for !receipt.Jobs[0].State.Terminal() {
		job, loadErr := d.plane.stateStore.GetCommandRuntimeJob(waitCtx, jobID)
		if loadErr != nil {
			return outcome, jobID, loadErr
		}
		if job.State.Terminal() {
			break
		}
		select {
		case <-waitCtx.Done():
			return outcome, jobID, waitCtx.Err()
		case <-poll.C:
		}
	}
	// A Wait call charges the Run's tool-call budget. Issuing it while Docker
	// admission is in flight would intentionally invalidate that candidate's
	// frozen budget snapshot. Read the durable Job until terminal, then make one
	// public Wait projection so Artifacts and cursor evidence are still produced.
	cursor := uint64(0)
	// The public wait protocol requires a positive wait budget. Because the Job
	// is already terminal this returns immediately; one millisecond is only the
	// smallest valid projection value, not an execution-time relaxation.
	waitMilliseconds := 1
	maxBytes := 32 * 1024
	waitInput := toolgateway.CommandRuntimeInput{
		Version:          toolgateway.CommandRuntimeToolProtocolVersion,
		Action:           toolgateway.CommandRuntimeActionWait,
		JobID:            jobID,
		Cursor:           &cursor,
		MaxBytes:         &maxBytes,
		WaitMilliseconds: &waitMilliseconds,
	}
	waitCall, err := run.prepareCommand(waitCtx, d.plane, d.capabilities, waitInput)
	if err != nil {
		return outcome, jobID, err
	}
	outcome, err = run.invokeCommand(waitCtx, d.plane, d.gateway, waitCall)
	if err != nil {
		return outcome, jobID, err
	}
	receipt, err = decodeStandardCodeSecurityCommandRuntimeReceipt(outcome,
		toolgateway.CommandRuntimeActionWait)
	if err != nil || len(receipt.Jobs) != 1 || receipt.Jobs[0].ID != jobID ||
		!receipt.Jobs[0].State.Terminal() {
		return outcome, jobID, errors.Join(err,
			errors.New("fixed background probe wait receipt changed terminal Job identity"))
	}
	return outcome, jobID, nil
}

func decodeStandardCodeSecurityCommandRuntimeReceipt(outcome toolgateway.Outcome,
	expectedAction string,
) (standardCodeSecurityCommandRuntimeReceipt, error) {
	var receipt standardCodeSecurityCommandRuntimeReceipt
	if outcome.Result == nil || outcome.Result.Status != toolgateway.StatusCompleted ||
		outcome.Result.Truncated || json.Unmarshal([]byte(outcome.Result.Stdout), &receipt) != nil ||
		receipt.Version != runner.CommandRuntimeResultVersion ||
		receipt.Action != expectedAction {
		return receipt, errors.New("fixed background probe receipt is invalid")
	}
	return receipt, nil
}

func (d *standardCodeSecurityDriver) observeStandardCodeSecurityDocker(
	ctx context.Context, run *standardCodeSecurityRun,
	request packagede2e.SecurityDriverCase, expectedArguments []string,
	observation *standardCodeSecurityObservation,
) error {
	if d == nil || d.plane == nil || run == nil || observation == nil ||
		request.Backend != "docker" || len(expectedArguments) == 0 {
		return errors.New("fixed Docker security observation is invalid")
	}
	var result standardcode.Result
	hasResult := json.Unmarshal([]byte(strings.TrimSpace(observation.job.Stdout)),
		&result) == nil && result.Validate() == nil
	var record domain.DockerSandboxRecord
	var err error
	if hasResult {
		if result.Backend != standardcode.BackendDocker ||
			result.RunID != run.run.ID || result.Status != standardcode.StatusSucceeded ||
			result.ExitCode == nil || *result.ExitCode != 0 ||
			result.Network != standardcode.NetworkDisabled ||
			result.Credentials != standardcode.CredentialsNone {
			return errors.New("fixed Docker probe returned an unexpected Standard Code result")
		}
		var found bool
		record, found, err = d.plane.stateStore.GetDockerSandboxRecordByLifecycleIntent(
			ctx, result.ExecutionID)
		if err != nil || !found {
			return errors.Join(err,
				errors.New("fixed Docker probe omitted its product lifecycle record"))
		}
	} else {
		if request.Attack.ID != "output_artifact_limit" {
			return errors.New("fixed Docker probe omitted its Standard Code result")
		}
		record, err = d.findStandardCodeSecurityDockerRecord(ctx, run,
			expectedArguments)
		if err != nil {
			return err
		}
	}
	if err := validateStandardCodeSecurityDockerRecord(record, run,
		observation.job, expectedArguments); err != nil {
		return err
	}
	if hasResult {
		manifest, decodeErr := sandbox.DecodeManifest(
			[]byte(record.Admission.ManifestJSON))
		binding, exact := sandbox.ParseDockerStandardCodeManifest(manifest)
		if decodeErr != nil || !exact || result.ExecutionID != record.Receipt.LifecycleIntentID ||
			result.DrydockID != binding.DrydockID ||
			result.Checkpoint.DrydockID != binding.DrydockID ||
			result.Checkpoint.GenerationBefore != binding.DrydockGeneration ||
			result.Checkpoint.BeforeID != binding.CheckpointID {
			return errors.Join(decodeErr,
				errors.New("fixed Docker result changed its Drydock or lifecycle binding"))
		}
	}
	observation.dockerRecord = &record

	if request.Attack.ID == "output_artifact_limit" {
		if hasResult || record.Receipt == nil || record.Receipt.ExitCode == nil ||
			*record.Receipt.ExitCode != 125 ||
			record.Receipt.Outcome != domain.DockerSandboxOutcomeFailed ||
			record.Receipt.ReasonCode != domain.DockerSandboxReasonIOFailed ||
			record.Receipt.LogReceiptID != "" ||
			observation.job.State != runner.CommandRuntimeJobFailed ||
			observation.job.ExitCode == nil || *observation.job.ExitCode != 125 ||
			!strings.Contains(observation.job.Stderr,
				"untracked worktree state exceeds safe capture bounds") {
			return errors.New("fixed Docker workspace growth did not fail closed")
		}
		if err := d.verifyStandardCodeSecurityDockerEntryGrowth(ctx, run); err != nil {
			return err
		}
		observation.receipt = standardCodeSecurityProbeReceipt{
			Protocol: standardCodeSecurityProbeProtocol, Case: request.Attack.ID,
			Blocked: true, Detail: "workspace-growth-denied",
		}
		observation.observed = true
		return nil
	}

	if record.Receipt == nil || record.Receipt.ExitCode == nil ||
		*record.Receipt.ExitCode != 0 ||
		record.Receipt.Outcome != domain.DockerSandboxOutcomeSucceeded ||
		record.Receipt.ReasonCode != domain.DockerSandboxReasonCompleted ||
		record.Receipt.LogReceiptID == "" {
		return errors.New("fixed Docker probe did not complete with bounded logs")
	}
	logs, found, err := d.plane.stateStore.GetDockerLogCaptureReceiptByAttempt(ctx,
		record.Receipt.AttemptID)
	if err != nil || !found || logs.Validate() != nil ||
		logs.ID != record.Receipt.LogReceiptID || logs.RunID != run.run.ID {
		return errors.Join(err,
			errors.New("fixed Docker probe log receipt is unavailable or unbound"))
	}
	receipt, err := matchStandardCodeSecurityDockerProbeLogs(request.Attack.ID, logs)
	if err != nil {
		return err
	}
	if !standardCodeSecurityDockerResultBindsLogs(result, logs) {
		return errors.New("fixed Docker Standard Code result did not bind its log receipt")
	}
	observation.dockerResult = &result
	observation.dockerLogs = &logs
	observation.receipt = receipt
	observation.observed = receipt.Blocked
	return nil
}

func (d *standardCodeSecurityDriver) findStandardCodeSecurityDockerRecord(
	ctx context.Context, run *standardCodeSecurityRun, expectedArguments []string,
) (domain.DockerSandboxRecord, error) {
	records, err := d.plane.stateStore.ListCompletedStandardCodeDockerSandboxes(ctx, 100)
	if err != nil {
		return domain.DockerSandboxRecord{}, err
	}
	var matched *domain.DockerSandboxRecord
	for index := range records {
		record := records[index]
		if record.Admission.RunID != run.run.ID ||
			!standardCodeSecurityDockerBindingMatches(record, run,
				expectedArguments) {
			continue
		}
		if matched != nil {
			return domain.DockerSandboxRecord{},
				errors.New("fixed Docker probe resolved more than one product lifecycle")
		}
		copy := record
		matched = &copy
	}
	if matched == nil {
		return domain.DockerSandboxRecord{},
			errors.New("fixed Docker probe product lifecycle is unavailable")
	}
	return *matched, nil
}

func validateStandardCodeSecurityDockerRecord(record domain.DockerSandboxRecord,
	run *standardCodeSecurityRun, job runner.CommandRuntimeJob,
	expectedArguments []string,
) error {
	if run == nil || record.Validate() != nil || record.Launch == nil ||
		record.Receipt == nil || !record.Receipt.CleanupComplete ||
		record.Admission.RunID != run.run.ID ||
		record.Admission.WorkspaceID != run.workspaceID ||
		record.Admission.NetworkMode != "disabled" ||
		record.Admission.NetworkTargetCount != 0 ||
		record.Admission.PermissionMode != domain.RunExecutionPermissionAsk ||
		record.Admission.PermissionMode != job.PermissionMode ||
		record.Admission.PermissionSnapshotID != job.PermissionSnapshotID ||
		record.Admission.PermissionRevision != job.PermissionRevision ||
		!record.Admission.ProductEntryEnabled || !record.Admission.ExecutionAuthorized ||
		record.Receipt.LifecycleIntentID != record.Launch.LifecycleIntentID ||
		record.Receipt.AttemptID != record.Launch.AttemptID ||
		record.Receipt.RunID != run.run.ID ||
		record.Receipt.WorkspaceID != run.workspaceID ||
		job.RunID != run.run.ID || !job.Adapter.SameBackend(run.adapter) ||
		!standardCodeSecurityDockerBindingMatches(record, run, expectedArguments) {
		return errors.New("fixed Docker probe lifecycle is not bound to exact authority")
	}
	return nil
}

func standardCodeSecurityDockerBindingMatches(record domain.DockerSandboxRecord,
	run *standardCodeSecurityRun, expectedArguments []string,
) bool {
	if run == nil {
		return false
	}
	manifest, err := sandbox.DecodeManifest([]byte(record.Admission.ManifestJSON))
	if err != nil {
		return false
	}
	binding, exact := sandbox.ParseDockerStandardCodeManifest(manifest)
	if !exact || binding.RunID != run.run.ID ||
		binding.MissionID != run.run.MissionID ||
		binding.SessionID != run.run.SessionID ||
		binding.WorkspaceID != run.workspaceID ||
		binding.CapabilityGeneration != run.adapter.Generation ||
		binding.Toolchain != sandbox.DockerStandardCodeToolchainGo ||
		binding.WorkingDirectory != "." ||
		len(binding.Arguments) != len(expectedArguments) {
		return false
	}
	for index := range expectedArguments {
		if binding.Arguments[index] != expectedArguments[index] {
			return false
		}
	}
	return true
}

func standardCodeSecurityDockerResultBindsLogs(result standardcode.Result,
	logs sandbox.DockerLogCaptureReceipt,
) bool {
	if result.Validate() != nil || logs.Validate() != nil ||
		len(result.Artifacts) != 1 {
		return false
	}
	value := result.Artifacts[0]
	return value.ID == logs.ID && value.Kind == "logs" &&
		value.SHA256 == logs.ReceiptFingerprint &&
		value.SizeBytes == logs.TotalBytes && value.FileCount == logs.StreamCount &&
		value.Redacted == (logs.RedactedSegments > 0)
}

func (d *standardCodeSecurityDriver) verifyStandardCodeSecurityDockerEntryGrowth(
	ctx context.Context, run *standardCodeSecurityRun,
) error {
	workspace, found, err := d.plane.stateStore.GetDrydockByRun(ctx, run.run.ID)
	if err != nil || !found {
		return errors.Join(err,
			errors.New("reload fixed Docker resource-limit Drydock"))
	}
	entries, err := os.ReadDir(filepath.Join(workspace.Path,
		".standard-code-security", "output-artifact-limit"))
	if err != nil || len(entries) < sandbox.DockerStandardCodeWorkspaceGrowthEntries {
		return errors.Join(err,
			errors.New("fixed Docker entry growth did not reach the product limit"))
	}
	return nil
}

func matchStandardCodeSecurityDockerProbeLogs(caseID string,
	logs sandbox.DockerLogCaptureReceipt,
) (standardCodeSecurityProbeReceipt, error) {
	if len(logs.Streams) != 2 {
		return standardCodeSecurityProbeReceipt{},
			errors.New("fixed Docker log receipt omitted a stream digest")
	}
	for _, detail := range standardCodeSecurityExpectedProbeDetails(caseID) {
		receipt := standardCodeSecurityProbeReceipt{Protocol: standardCodeSecurityProbeProtocol,
			Case: caseID, Blocked: true, Detail: detail}
		stdout, stderr := standardCodeSecurityExpectedProbeStreams(receipt)
		if standardCodeSecurityDockerStreamMatches(logs.Streams[0], stdout) &&
			standardCodeSecurityDockerStreamMatches(logs.Streams[1], stderr) &&
			logs.Status == sandbox.DockerLogCaptureStatusCompleted {
			return receipt, nil
		}
	}
	return standardCodeSecurityProbeReceipt{},
		errors.New("fixed Docker log digests did not prove the expected probe receipt")
}

func standardCodeSecurityDockerStreamMatches(record sandbox.DockerLogStreamRecord,
	raw string,
) bool {
	safe := redact.String(raw)
	digest := sha256.Sum256([]byte(safe))
	return record.ByteCount == int64(len([]byte(raw))) &&
		record.LineCount == strings.Count(raw, "\n") &&
		!record.TruncatedBytes && !record.TruncatedLines &&
		!record.TruncatedDeadline && record.UTF8Violations == 0 &&
		record.RedactedSegments == strings.Count(safe, "[REDACTED:") &&
		record.ContentDigest == hex.EncodeToString(digest[:])
}

func standardCodeSecurityExpectedProbeStreams(
	receipt standardCodeSecurityProbeReceipt,
) (string, string) {
	encoded, _ := json.Marshal(receipt)
	line := string(encoded) + "\n"
	switch receipt.Case {
	case "output_secret_redaction":
		return "sk-Issue181SyntheticSentinel000000\n" + line,
			"GITHUB_TOKEN=Issue181SyntheticSentinel000000\n"
	case "output_control_ansi":
		return "visible\x1b[2J\x1b[Hhidden\rrewrite\u202e\n" + line, ""
	case "output_stream_limit":
		var value strings.Builder
		for index := 0; index < 1000; index++ {
			fmt.Fprintf(&value,
				"bounded-stream-%05d-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n", index)
		}
		value.WriteString(line)
		return value.String(), ""
	default:
		return line, ""
	}
}

func standardCodeSecurityExpectedProbeDetails(caseID string) []string {
	switch caseID {
	case "filesystem_parent_traversal", "credential_home_profile":
		return []string{"outside-read"}
	case "filesystem_drive_root":
		return []string{"volume-root"}
	case "filesystem_unc_root":
		return []string{"unc-root"}
	case "filesystem_device_path":
		return []string{"device-root"}
	case "filesystem_symlink_reparse_escape":
		return []string{"indirect-root"}
	case "filesystem_long_path_overflow":
		return []string{"path-bound"}
	case "credential_manager":
		return []string{"credential-manager-delete-denied"}
	case "credential_git_helper":
		return []string{"git-helper"}
	case "credential_ssh_agent":
		return []string{"ssh-agent"}
	case "credential_cloud_environment":
		return []string{"host-sentinels-absent"}
	case "network_dns":
		return []string{"dns-disabled"}
	case "network_tcp", "network_loopback":
		return []string{"tcp-disabled"}
	case "network_udp":
		return []string{"udp-disabled", "udp-no-response"}
	case "network_proxy_environment":
		return []string{"proxy-empty"}
	case "process_detached_child", "process_background_job", "process_group_escape":
		return []string{"child-start-denied", "child-owned-by-runtime"}
	case "process_inherited_handle":
		return []string{"host-named-object-denied"}
	case "output_secret_redaction":
		return []string{"secret-shaped-output"}
	case "output_control_ansi":
		return []string{"control-output"}
	case "output_stream_limit":
		return []string{"stream-bound"}
	case "output_artifact_limit":
		return []string{"workspace-growth-submitted", "workspace-growth-denied"}
	default:
		for _, prefix := range []string{"prompt_", "authority_", "approval_", "recovery_"} {
			if strings.HasPrefix(caseID, prefix) {
				return []string{"application-boundary"}
			}
		}
	}
	return nil
}

// prepareCommand uses the same durable call identity and native input binder as
// AgentRunner. The fixed packaged driver owns its lease so recovery cases can
// expire or terminate that exact lease instead of Step releasing it early.
func (run *standardCodeSecurityRun) prepareCommand(ctx context.Context,
	plane *ControlPlane, capabilities domain.ExecutionPermissionRuntimeCapabilities,
	input toolgateway.CommandRuntimeInput,
) (toolgateway.ToolCall, error) {
	if run.turn.Checkpoint.Phase != domain.SupervisorTurnStarted {
		turn, err := plane.stateStore.BeginSupervisorTurn(ctx, run.lease, "")
		if err != nil {
			return toolgateway.ToolCall{}, err
		}
		run.turn = turn
		run.root = turn.Agent
	}
	turn := &run.turn
	if !turn.Checkpoint.HasPendingInput() {
		checkpoint, err := plane.stateStore.BindSupervisorTurnInput(ctx, turn.Checkpoint, turn.Mission.Goal)
		if err != nil {
			return toolgateway.ToolCall{}, err
		}
		turn.Checkpoint = checkpoint
	}
	permission, err := plane.stateStore.GetRunExecutionPermission(ctx, run.run.ID)
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	// A packaged Standard Code probe is a verified sandbox operation under Ask.
	// It must never gain Full, use a different adapter, or create a host fallback.
	if permission.Mode != domain.RunExecutionPermissionAsk || !capabilities.AllowsSnapshot(permission) || capabilities.RuntimeAuthority == nil {
		return toolgateway.ToolCall{}, errors.New("fixed probe requires current Ask runtime authority")
	}
	adapter, available, err := plane.commandRuntime.AdvertisedCommandRuntimeAdapter(ctx, run.run.ID, permission.Mode)
	if err != nil || !available || !adapter.SameBackend(run.adapter) {
		return toolgateway.ToolCall{}, errors.Join(err, errors.New("fixed probe adapter changed"))
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	_, payload, err := toolgateway.NormalizeCommandRuntimePayload(raw)
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	fence, err := capabilities.RuntimeAuthority.IssueRunAuthorizationFence(run.run.ID)
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	authority := commandruntimeadapter.Authority{ProtocolVersion: commandruntimeadapter.OperationAuthorityVersion,
		RunID: run.run.ID, Adapter: adapter, PermissionSnapshotID: permission.ID,
		PermissionMode: permission.Mode, PermissionRevision: permission.Revision,
		PermissionRuntimeEpoch: capabilities.RuntimeAuthority.RuntimeEpoch(), RunAuthorizationFence: fence}
	encoded, err := commandruntimeadapter.EncodeAuthority(authority)
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	binder, ok := plane.commandRuntime.(interface {
		BindCommandRuntimeAuthority(context.Context, json.RawMessage, json.RawMessage) (json.RawMessage, error)
	})
	if !ok {
		return toolgateway.ToolCall{}, errors.New("fixed probe native input binder is unavailable")
	}
	encoded, err = binder.BindCommandRuntimeAuthority(ctx, encoded, payload)
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	rounds, err := plane.stateStore.ListSupervisorToolRounds(ctx, turn.Checkpoint)
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	round := len(rounds) + 1
	key := runmutation.SupervisorToolOperationKey(run.run.ID, turn.Checkpoint.NextTurn, string(toolgateway.CommandRuntimeTool), string(payload))
	callID, err := runmutation.SupervisorToolCallID(key, round)
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	attempt := llm.ModelAttempt{Number: round, ToolRound: round - 1, TransportAttempt: 1,
		MaxAttempts: 1, Provider: "packaged-fixed-probe", Model: "frozen-security-matrix"}
	if _, err = plane.stateStore.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil {
		return toolgateway.ToolCall{}, err
	}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, err := plane.stateStore.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, attempt,
		llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model,
			ToolCalls: []llm.ToolCall{{ID: callID, Name: string(toolgateway.CommandRuntimeTool), Arguments: payload, Authority: encoded}}})
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	turn.Checkpoint = checkpoint
	rounds, err = plane.stateStore.ListSupervisorToolRounds(ctx, turn.Checkpoint)
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	if len(rounds) != round || len(rounds[round-1].Calls) != 1 {
		return toolgateway.ToolCall{}, errors.New("fixed probe durable call is missing")
	}
	source := rounds[round-1].Calls[0]
	authority, err = commandruntimeadapter.DecodeAuthority(json.RawMessage(source.AuthorityJSON))
	if err != nil {
		return toolgateway.ToolCall{}, err
	}
	key = runmutation.SupervisorToolExecutionKey(source.RunID, source.Turn, source.ToolName, source.PayloadJSON, source.CallID)
	return toolgateway.ToolCall{Name: toolgateway.CommandRuntimeTool,
		Arguments: map[string]string{}, Payload: json.RawMessage(source.PayloadJSON), OperationKey: key,
		RunID: source.RunID, MissionID: turn.Mission.ID, AgentID: source.AgentID,
		AgentAttemptID: source.AgentAttemptID, SessionID: turn.Run.SessionID, WorkspaceID: turn.Mission.WorkspaceID,
		Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Role: turn.Agent.Role, Profile: turn.Mode.Profile,
		PermissionMode: authority.PermissionMode, ModeRevision: turn.Mode.Revision, PermissionRevision: authority.PermissionRevision,
		PermissionSnapshotID: authority.PermissionSnapshotID, PermissionGeneration: authority.PermissionGeneration,
		PermissionRuntimeEpoch: authority.PermissionRuntimeEpoch,
		RunAuthorizationFence:  authority.RunAuthorizationFence, CapabilityGeneration: authority.Adapter.Generation,
		LeaseID: turn.Checkpoint.LeaseID, LeaseGeneration: turn.Checkpoint.LeaseGeneration,
		RequestedBy: "run_supervisor", CommandRuntimeAdapter: authority.Adapter,
		SupervisorTurn: source.Turn, SupervisorToolCallID: source.CallID}, nil
}

func (run *standardCodeSecurityRun) invokeCommand(ctx context.Context,
	plane *ControlPlane, gateway *toolgateway.Gateway, call toolgateway.ToolCall,
) (toolgateway.Outcome, error) {
	fresh, superseded, err := plane.stateStore.RecordSupervisorToolExecutionStartedWithSteering(ctx, run.turn.Checkpoint, call.SupervisorToolCallID)
	if err != nil {
		return toolgateway.Outcome{}, err
	}
	if superseded {
		return toolgateway.Outcome{}, errors.New("fixed probe was superseded before dispatch")
	}
	if !fresh {
		return toolgateway.Outcome{}, errors.New("fixed probe dispatch was already started")
	}
	outcome, invokeErr := gateway.Invoke(ctx, call)
	status, code := domain.SupervisorToolCompleted, ""
	if invokeErr != nil {
		category := apperror.CodeOf(apperror.Normalize(invokeErr))
		switch category {
		case apperror.CodeConflict, apperror.CodeInvalidArgument, apperror.CodeFailedPrecondition, apperror.CodePolicyDenied, apperror.CodeNotFound, apperror.CodeResourceExhausted:
			status, code = domain.SupervisorToolFailed, string(category)
		default:
			// Cancellation or an uncertain dispatch keeps started/no-result. The
			// packaged harness must fail without manufacturing a settled receipt.
			return outcome, invokeErr
		}
	} else if outcome.Result == nil {
		return outcome, errors.New("fixed probe omitted its dispatch result")
	} else if !outcome.Decision.Allowed || outcome.Result.Status == toolgateway.StatusDenied {
		status, code = domain.SupervisorToolDenied, string(apperror.CodePolicyDenied)
		invokeErr = errors.New("fixed probe was denied by current execution policy")
	} else if outcome.Result.Status != toolgateway.StatusCompleted {
		return outcome, errors.New("fixed probe did not settle its dispatch result")
	}
	result := map[string]any{"version": "supervisor_tool_result.v1", "tool": string(toolgateway.CommandRuntimeTool), "status": string(status)}
	if code != "" {
		result["code"] = code
	}
	if outcome.Result != nil {
		result["stdout"], result["stderr"] = redact.String(outcome.Result.Stdout), redact.String(outcome.Result.Stderr)
		metadata := make(map[string]string, len(outcome.Result.Metadata))
		for key, value := range outcome.Result.Metadata {
			if key != "replayed" {
				metadata[key] = redact.String(value)
			}
		}
		result["truncated"], result["metadata"] = outcome.Result.Truncated, metadata
	}
	raw, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		return outcome, errors.Join(invokeErr, encodeErr)
	}
	_, _, recordErr := plane.stateStore.RecordSupervisorToolResult(ctx, run.turn.Checkpoint,
		domain.SupervisorToolResult{CallID: call.SupervisorToolCallID, Status: status,
			ErrorCode: code, ResultJSON: string(raw), CompletedAt: time.Now().UTC()})
	return outcome, errors.Join(invokeErr, recordErr)
}

func (run *standardCodeSecurityRun) completeTurn(ctx context.Context, plane *ControlPlane) error {
	if run.turn.Checkpoint.Phase != domain.SupervisorTurnStarted {
		return nil
	}
	rounds, err := plane.stateStore.ListSupervisorToolRounds(ctx, run.turn.Checkpoint)
	if err != nil {
		return err
	}
	for _, round := range rounds {
		for _, call := range round.Calls {
			if !call.Status.Terminal() {
				return errors.New("fixed probe has an unfinished durable call")
			}
		}
	}
	action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue,
		Message: "Fixed packaged security observation recorded."}
	raw, err := json.Marshal(action)
	if err != nil {
		return err
	}
	attempt := llm.ModelAttempt{Number: len(rounds) + 1, ToolRound: len(rounds), TransportAttempt: 1,
		MaxAttempts: 1, Provider: "packaged-fixed-probe", Model: "frozen-security-matrix"}
	if _, err = plane.stateStore.RecordSupervisorModelStarted(ctx, run.turn.Checkpoint, attempt); err != nil {
		return err
	}
	attempt.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model, Text: string(raw)}
	checkpoint, err := plane.stateStore.RecordSupervisorModelCompleted(ctx, run.turn.Checkpoint, attempt, response)
	if err != nil {
		return err
	}
	run.turn.Checkpoint = checkpoint
	updated, completed, _, err := plane.stateStore.CompleteSupervisorTurn(ctx, run.turn.Checkpoint,
		response, action, policy.Decision{Allowed: true, Reason: "fixed packaged observation is complete"}, 0)
	if err == nil {
		run.run, run.turn.Checkpoint = updated, completed
	}
	return err
}
func (d *standardCodeSecurityDriver) verifyCaseSpecificBoundary(ctx context.Context,
	run *standardCodeSecurityRun, request packagede2e.SecurityDriverCase,
	observation *standardCodeSecurityObservation,
) error {
	if observation == nil {
		return errors.New("security observation is required")
	}
	switch request.Attack.Category {
	case "process_escape":
		if request.Attack.ID != "process_inherited_handle" {
			time.Sleep(1700 * time.Millisecond)
			workspace, found, err := d.plane.stateStore.GetDrydockByRun(ctx, run.run.ID)
			if err != nil || !found {
				return errors.New("reload process-escape Drydock")
			}
			marker := filepath.Join(workspace.Path, ".standard-code-security",
				request.Attack.ID+".marker")
			if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
				return errors.New("owned child escaped the command runtime tree")
			}
		}
	case "output_safety":
		switch request.Attack.ID {
		case "output_secret_redaction":
			if request.Backend == "docker" {
				observation.observed = observation.observed &&
					observation.dockerResult != nil && observation.dockerLogs != nil &&
					observation.dockerLogs.RedactedSegments >= 2 &&
					observation.dockerResult.Artifacts[0].Redacted &&
					(observation.stdout == nil || !strings.Contains(
						observation.stdout.Content, "Issue181SyntheticSentinel")) &&
					(observation.stderr == nil || !strings.Contains(
						observation.stderr.Content, "Issue181SyntheticSentinel"))
			} else {
				observation.observed = observation.stdout != nil && observation.stderr != nil &&
					observation.stdout.Redacted && observation.stderr.Redacted &&
					!strings.Contains(observation.stdout.Content, "Issue181SyntheticSentinel") &&
					!strings.Contains(observation.stderr.Content, "Issue181SyntheticSentinel")
			}
		case "output_control_ansi":
			observation.observed = observation.observed && observation.stdout != nil &&
				!strings.ContainsAny(observation.stdout.Content, "\x1b\r") &&
				!strings.ContainsRune(observation.stdout.Content, '\u202e')
		case "output_stream_limit", "output_artifact_limit":
			if request.Attack.ID == "output_stream_limit" {
				if request.Backend == "docker" {
					observation.observed = observation.observed &&
						observation.dockerLogs != nil &&
						observation.dockerLogs.Streams[0].ByteCount >
							int64(observation.job.InlineLimitBytes) &&
						observation.dockerLogs.Streams[0].ByteCount <=
							int64(observation.job.ArtifactLimitBytes) &&
						observation.dockerLogs.Status == sandbox.DockerLogCaptureStatusCompleted
				} else {
					observation.observed = observation.observed &&
						observation.job.TruncationReason == "inline_window" &&
						observation.job.StdoutObservedBytes > int64(observation.job.InlineLimitBytes) &&
						observation.job.StdoutObservedBytes <= int64(observation.job.ArtifactLimitBytes)
				}
			} else if request.Backend == "docker" {
				observation.observed = observation.observed &&
					observation.receipt.Detail == "workspace-growth-denied" &&
					observation.dockerRecord != nil &&
					observation.job.State == runner.CommandRuntimeJobFailed &&
					observation.job.ExitCode != nil && *observation.job.ExitCode == 125
			} else {
				observation.observed = observation.observed &&
					observation.receipt.Detail == "workspace-growth-submitted" &&
					observation.job.State == runner.CommandRuntimeJobFailed &&
					observation.job.ExitCode != nil && *observation.job.ExitCode == 0
			}
		}
	case "authority_replay":
		return d.verifyStaleAuthority(ctx, run, request)
	case "prompt_injection":
		if request.Attack.ID == "prompt_forged_receipt" {
			return d.verifyStaleAuthority(ctx, run, request)
		}
		if err := d.verifyPromptInjectionDenial(ctx, run, request); err != nil {
			return err
		}
		observation.observed = observation.observed &&
			!observation.outcome.Call.CommandRuntimeAdapter.IsZero()
	case "recovery":
		receipt, err := d.verifyPackagedRecovery(ctx, request)
		if err != nil {
			return err
		}
		observation.recovery = &receipt
		observation.observed = observation.observed && receipt.Status == "passed" &&
			receipt.Signal == request.Attack.ExpectedSignal && receipt.TreeReaped &&
			receipt.Preserved && receipt.RecoveryCycles == 2
	}
	if !observation.observed {
		return errors.New("fixed product observation did not prove the expected boundary")
	}
	return nil
}

func (d *standardCodeSecurityDriver) verifyPromptInjectionDenial(ctx context.Context,
	run *standardCodeSecurityRun, request packagede2e.SecurityDriverCase,
) error {
	before, err := d.plane.stateStore.ListCommandRuntimeJobs(ctx,
		runner.CommandRuntimeListFilter{Limit: 500})
	if err != nil {
		return err
	}
	outcome, err := d.gateway.Invoke(ctx, toolgateway.ToolCall{
		Name:      toolgateway.ShellTool,
		Arguments: map[string]string{"command": "masscan 0.0.0.0/0"},
		RunID:     run.run.ID, MissionID: run.run.MissionID, AgentID: run.root.ID,
		SessionID: run.run.SessionID, WorkspaceID: run.workspaceID,
		LeaseID: run.lease.LeaseID, LeaseGeneration: run.lease.Generation,
		RequestedBy: "run_supervisor",
	})
	after, listErr := d.plane.stateStore.ListCommandRuntimeJobs(ctx,
		runner.CommandRuntimeListFilter{Limit: 500})
	if err != nil || listErr != nil || len(after) != len(before) ||
		outcome.Decision.Allowed || outcome.Decision.Approval != toolgateway.ApprovalNever ||
		outcome.Result == nil || outcome.Result.Status != toolgateway.StatusDenied ||
		outcome.Execution != nil {
		return errors.Join(err, listErr,
			errors.New("untrusted repository prompt did not remain a non-executing policy denial"))
	}
	return nil
}

func (d *standardCodeSecurityDriver) verifyStaleAuthority(ctx context.Context,
	run *standardCodeSecurityRun, request packagede2e.SecurityDriverCase,
) error {
	maxBytes := 4096
	input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action:        toolgateway.CommandRuntimeActionRun,
		FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes,
		Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion,
			Profile: runner.CommandRuntimeProcess, Executable: d.goExecutable,
			Arguments: []string{"version"}, WorkingDirectory: ".",
			Environment: []runner.CommandRuntimeEnvironment{},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 5000,
			Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
			Network:             runner.CommandRuntimeNetworkDisabled,
			Credentials:         runner.CommandRuntimeCredentialsNone,
			Purpose:             "prove stale packaged authority fails closed"}}}
	call, err := run.prepareCommand(ctx, d.plane, d.capabilities, input)
	if err != nil {
		return err
	}
	switch request.Attack.ID {
	case "authority_stale_permission":
		call.PermissionRevision++
	case "authority_stale_profile":
		call.ModeRevision++
	case "authority_stale_root":
		call.LeaseGeneration++
	case "authority_stale_backend":
		call.CapabilityGeneration = strings.Repeat("0", 64)
	case "authority_cross_run_replay", "prompt_forged_receipt":
		other := d.otherSecurityRun(run)
		if other == nil {
			return errors.New("cross-Run security replay has no second real Run")
		}
		call.MissionID = other.run.MissionID
	default:
		call.PermissionRevision++
	}
	before, err := d.plane.stateStore.ListCommandRuntimeJobs(ctx,
		runner.CommandRuntimeListFilter{Limit: 500})
	if err != nil {
		return err
	}
	_, invokeErr := run.invokeCommand(ctx, d.plane, d.gateway, call)
	after, listErr := d.plane.stateStore.ListCommandRuntimeJobs(ctx,
		runner.CommandRuntimeListFilter{Limit: 500})
	if listErr != nil {
		return listErr
	}
	if invokeErr == nil || len(after) != len(before) {
		return errors.New("stale packaged authority reached process execution")
	}
	code := apperror.CodeOf(apperror.Normalize(invokeErr))
	if code != apperror.CodeConflict {
		return fmt.Errorf("stale packaged authority returned unstable category %q", code)
	}
	return nil
}

func (d *standardCodeSecurityDriver) otherSecurityRun(
	current *standardCodeSecurityRun,
) *standardCodeSecurityRun {
	if d == nil || current == nil {
		return nil
	}
	keys := make([]string, 0, len(d.runs))
	for key := range d.runs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		candidate := d.runs[key]
		if candidate != nil && candidate.backend == current.backend &&
			candidate.run.ID != current.run.ID {
			return candidate
		}
	}
	return nil
}

func (d *standardCodeSecurityDriver) securityEvidence(ctx context.Context,
	run *standardCodeSecurityRun, request packagede2e.SecurityDriverCase,
	observation standardCodeSecurityObservation,
) ([]packagede2e.SecurityEvidenceRef, error) {
	events, err := d.plane.stateStore.ListRunEvents(ctx, run.run.ID)
	if err != nil {
		return nil, err
	}
	projection, err := d.plane.standardCodeDrydocks.Projection(ctx, run.run.ID, 50)
	if err != nil {
		return nil, err
	}
	workspace, found, err := d.plane.stateStore.GetDrydockByRun(ctx, run.run.ID)
	if err != nil || !found {
		return nil, errors.New("security evidence Drydock is unavailable")
	}
	checkpoint, err := d.plane.stateStore.GetWorkspaceCheckpointSnapshot(ctx,
		workspace.LastCheckpointID)
	if err != nil {
		return nil, err
	}
	uiSource := "desktop.projection"
	uiHash := ""
	if request.Attack.ID == "output_artifact_limit" && request.Backend == "docker" {
		// This case intentionally leaves the Drydock beyond the product's safe
		// capture bound. Capability-readiness is a post-execution selector and
		// cannot project that recovery-required workspace. Bind operator evidence
		// to the same public terminal Command Runtime result instead of rescanning
		// the rejected tree or weakening the resource limit.
		if observation.outcome.Result == nil ||
			observation.outcome.Result.Status != toolgateway.StatusCompleted ||
			observation.job.ID == "" ||
			observation.job.State != runner.CommandRuntimeJobFailed {
			return nil, errors.New("resource-limit operator result is unavailable")
		}
		uiSource = "command_runtime.public_result"
		uiHash = hashSecurityValue(struct {
			Outcome toolgateway.Outcome `json:"outcome"`
			JobID   string              `json:"job_id"`
		}{observation.outcome, observation.job.ID})
	} else {
		uiHash, err = d.operatorProjectionHash(run.run.ID)
		if err != nil {
			return nil, err
		}
	}
	values := map[string]struct {
		Source string
		Hash   string
	}{
		"operator_ui":      {uiSource, uiHash},
		"immutable_event":  {"run.event", hashSecurityValue(events)},
		"workspace_digest": {"drydock.observation", hashSecurityValue(projection)},
		"process_receipt": {"command_runtime.receipt", hashSecurityValue(struct {
			Job          runner.CommandRuntimeJob         `json:"job"`
			DockerResult *standardcode.Result             `json:"docker_result,omitempty"`
			DockerRecord *domain.DockerSandboxRecord      `json:"docker_record,omitempty"`
			DockerLogs   *sandbox.DockerLogCaptureReceipt `json:"docker_logs,omitempty"`
		}{observation.job, observation.dockerResult,
			observation.dockerRecord, observation.dockerLogs})},
		"network_observation": {"sandbox.network", hashSecurityValue(struct {
			Adapter      commandruntimeadapter.Identity   `json:"adapter"`
			Job          runner.CommandRuntimeJob         `json:"job"`
			DockerRecord *domain.DockerSandboxRecord      `json:"docker_record,omitempty"`
			DockerLogs   *sandbox.DockerLogCaptureReceipt `json:"docker_logs,omitempty"`
		}{run.adapter, observation.job, observation.dockerRecord,
			observation.dockerLogs})},
		"artifact_digest": {"artifact.store", hashSecurityValue(struct {
			Stdout     *artifact.Blob                   `json:"stdout,omitempty"`
			Stderr     *artifact.Blob                   `json:"stderr,omitempty"`
			DockerLogs *sandbox.DockerLogCaptureReceipt `json:"docker_logs,omitempty"`
		}{observation.stdout, observation.stderr, observation.dockerLogs})},
		"thread_transcript": {"thread.projection", hashSecurityValue(events)},
		"checkpoint":        {"workspace.checkpoint", hashSecurityValue(checkpoint)},
	}
	if observation.recovery != nil {
		values["immutable_event"] = struct {
			Source string
			Hash   string
		}{"recovery.event", observation.recovery.EventSHA256}
		values["process_receipt"] = struct {
			Source string
			Hash   string
		}{"recovery.command_runtime", observation.recovery.JobSHA256}
		values["checkpoint"] = struct {
			Source string
			Hash   string
		}{"recovery.checkpoint", observation.recovery.CheckpointSHA256}
		values["workspace_digest"] = struct {
			Source string
			Hash   string
		}{"recovery.drydock", observation.recovery.WorkspaceSHA256}
		values["thread_transcript"] = struct {
			Source string
			Hash   string
		}{"recovery.transcript", observation.recovery.TranscriptSHA256}
	}
	refs := make([]packagede2e.SecurityEvidenceRef, 0,
		len(request.Attack.RequiredEvidence))
	for _, kind := range request.Attack.RequiredEvidence {
		value, ok := values[kind]
		if !ok || value.Hash == "" {
			return nil, fmt.Errorf("security evidence kind %q is unavailable", kind)
		}
		refs = append(refs, packagede2e.SecurityEvidenceRef{Kind: kind,
			Source: value.Source, SHA256: value.Hash})
	}
	return refs, nil
}

func (d *standardCodeSecurityDriver) operatorProjectionHash(runID string) (string, error) {
	request := httptest.NewRequest(http.MethodGet,
		"http://127.0.0.1/api/v1/runs/"+runID+"/capability-readiness", nil)
	request.Host = "127.0.0.1:8765"
	request.RemoteAddr = "127.0.0.1:45678"
	request.Header.Set("Authorization", "Bearer "+d.readToken)
	response := httptest.NewRecorder()
	d.plane.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		return "", fmt.Errorf("operator projection status %d", response.Code)
	}
	return hashSecurityValue(json.RawMessage(response.Body.Bytes())), nil
}

func (d *standardCodeSecurityDriver) Close(ctx context.Context) (
	packagede2e.SecurityCleanupEvidence, error,
) {
	cleanup := packagede2e.SecurityCleanupEvidence{OwnedRootSHA256: hashSecurityValue(
		struct {
			Owner string `json:"owner"`
		}{"standard-code-attack-181"}),
		OwnedProcessesStarted: d.recoveryJobsStarted,
		OwnedProcessesReaped:  d.recoveryJobsReaped, OwnedDirectoriesOnly: true}
	if d == nil || !d.opened || d.closed || ctx == nil {
		return cleanup, errors.New("packaged security driver close lifecycle is invalid")
	}
	var closeErr error
	jobs, err := d.plane.stateStore.ListCommandRuntimeJobs(ctx,
		runner.CommandRuntimeListFilter{Limit: 500})
	if err != nil {
		closeErr = errors.Join(closeErr, errors.New("list harness-owned command runtime Jobs"))
	}
	cleanup.OwnedProcessesStarted += len(jobs)
	for _, job := range jobs {
		if job.State.Terminal() && job.TreeReaped {
			cleanup.OwnedProcessesReaped++
		} else {
			closeErr = errors.Join(closeErr, fmt.Errorf("owned Job cleanup %s", job.ID))
		}
	}
	for _, run := range d.runs {
		_, _, _ = d.plane.stateStore.ReleaseRunExecutionLease(ctx, run.lease)
		_, _ = application.NewRunService(d.plane.stateStore).Pause(ctx, run.run.ID)
	}
	closeErr = errors.Join(closeErr, d.plane.Close())
	closeErr = errors.Join(closeErr, d.local.Close())
	d.closed = true
	return cleanup, closeErr
}

func prepareStandardCodeSecurityLocalProbe(ctx context.Context, runtimeRoot,
	goExecutable string,
) (string, error) {
	buildRoot := filepath.Join(runtimeRoot, "local-probe-build")
	toolchainRoot := filepath.Join(runtimeRoot, "local-probe-toolchain")
	if err := os.Mkdir(buildRoot, 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(toolchainRoot, 0o700); err != nil {
		return "", err
	}
	source := filepath.Join(buildRoot, "probe.go")
	if err := writeExclusiveSecurityFile(source, standardCodeSecurityProbe); err != nil {
		return "", err
	}
	output := filepath.Join(toolchainRoot, "standard-code-security-probe.exe")
	command := exec.CommandContext(ctx, goExecutable, "build", "-trimpath",
		"-buildvcs=false", "-o", output, source)
	configureStandardCodeSecuritySubprocess(command)
	command.Dir = buildRoot
	command.Env = append(standardCodeSecurityWorkerEnvironment(""),
		"GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	combined, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(combined))
		if len(detail) > 4096 {
			detail = detail[:4096]
		}
		return "", fmt.Errorf("compile fixed probe: %w: %s", err, detail)
	}
	info, err := os.Lstat(output)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > 64<<20 {
		return "", errors.New("compiled fixed probe is unavailable, indirect, or oversized")
	}
	return output, nil
}

func prepareStandardCodeSecurityOutsideSentinel(runtimeRoot string) (string, error) {
	directory := filepath.Join(runtimeRoot, "outside")
	if err := os.Mkdir(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "sentinel.txt")
	if err := writeExclusiveSecurityFile(path, []byte("issue181-owned-sentinel\n")); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != int64(len("issue181-owned-sentinel\n")) {
		return "", errors.New("fixed outside sentinel is unavailable or indirect")
	}
	return path, nil
}

func parseSecurityProbeReceipt(blob *artifact.Blob,
	caseID string,
) standardCodeSecurityProbeReceipt {
	if blob == nil {
		return standardCodeSecurityProbeReceipt{}
	}
	lines := strings.Split(blob.Content, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		var value standardCodeSecurityProbeReceipt
		if json.Unmarshal([]byte(lines[index]), &value) == nil &&
			value.Protocol == standardCodeSecurityProbeProtocol && value.Case == caseID {
			return value
		}
	}
	return standardCodeSecurityProbeReceipt{}
}

func hashSecurityValue(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func randomDesktopSecurityToken() (string, error) {
	// The HTTP package owns the exact token entropy and formatting contract.
	return httpapi.GenerateAccessToken()
}

func writeExclusiveSecurityFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func pathInside(parent, child string) bool {
	parent, parentErr := filepath.Abs(parent)
	child, childErr := filepath.Abs(child)
	if parentErr != nil || childErr != nil {
		return false
	}
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
