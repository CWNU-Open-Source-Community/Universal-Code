//go:build windows

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/packagede2e"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/toolgateway"

	"golang.org/x/sys/windows"
)

func configureStandardCodeSecuritySubprocess(command *exec.Cmd) {
	if command != nil {
		command.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: windows.CREATE_NO_WINDOW,
			HideWindow:    true,
		}
	}
}

const (
	standardCodeSecurityRecoveryProtocol = "standard_code_security_recovery.v1"
	recoveryWorkerPreparePhase           = "prepare"
	recoveryWorkerRecoverPhase           = "recover"
	recoveryWorkerStateName              = "recovery-state.json"
	recoveryWorkerReadyName              = "prepare-ready"
	recoveryWorkerShutdownName           = "shutdown-requested"
	recoveryWorkerReceiptName            = "recovery-receipt.json"
	standardCodeSecurityDockerEnv        = "CYBERAGENT_STANDARD_CODE_DOCKER_IMAGE_DIGEST"
)

var errStandardCodeRendererRecoveryUnsupported = errors.New(
	"recovery_renderer_close is unsupported: real renderer closure at the FileEdit apply boundary has not been implemented")

var standardCodeRecoveryCases = map[string]bool{
	"recovery_renderer_close":                  true,
	"recovery_desktop_exit":                    true,
	"recovery_force_termination":               true,
	"recovery_reboot_equivalent":               true,
	"recovery_lease_expiry":                    true,
	"recovery_dirty_untracked_concurrent_edit": true,
}

// StandardCodeSecurityRecoveryWorkerConfig is deliberately not a general
// Desktop launch configuration. The packaged parent supplies one harness-owned
// root, one frozen case/backend pair, and one of two fixed lifecycle phases.
type StandardCodeSecurityRecoveryWorkerConfig struct {
	Root    string
	CaseID  string
	Backend string
	Phase   string
}

type standardCodeSecurityRecoveryOwner struct {
	Protocol string `json:"protocol"`
	CaseID   string `json:"case_id"`
	Backend  string `json:"backend"`
}

type standardCodeSecurityRecoveryState struct {
	Protocol             string                         `json:"protocol"`
	CaseID               string                         `json:"case_id"`
	Backend              string                         `json:"backend"`
	RunID                string                         `json:"run_id"`
	WorkspaceID          string                         `json:"workspace_id"`
	RootAgentID          string                         `json:"root_agent_id"`
	JobID                string                         `json:"job_id"`
	DrydockRelativePath  string                         `json:"drydock_relative_path"`
	InitialCheckpointID  string                         `json:"initial_checkpoint_id"`
	PreparedCheckpointID string                         `json:"prepared_checkpoint_id"`
	LeaseID              string                         `json:"lease_id"`
	LeaseGeneration      int64                          `json:"lease_generation"`
	Adapter              commandruntimeadapter.Identity `json:"adapter"`
	CreatedAt            time.Time                      `json:"created_at"`
}

type standardCodeSecurityRecoveryReceipt struct {
	Protocol         string `json:"protocol"`
	CaseID           string `json:"case_id"`
	Backend          string `json:"backend"`
	Status           string `json:"status"`
	Signal           string `json:"signal"`
	JobState         string `json:"job_state"`
	TreeReaped       bool   `json:"tree_reaped"`
	Preserved        bool   `json:"preserved"`
	RecoveryCycles   int    `json:"recovery_cycles"`
	EventSHA256      string `json:"event_sha256"`
	JobSHA256        string `json:"job_sha256"`
	CheckpointSHA256 string `json:"checkpoint_sha256"`
	WorkspaceSHA256  string `json:"workspace_sha256"`
	TranscriptSHA256 string `json:"transcript_sha256"`
}

type standardCodeSecurityRecoveryPlane struct {
	local        sandbox.LocalBackend
	plane        *ControlPlane
	gateway      *toolgateway.Gateway
	goPath       string
	readToken    string
	backend      string
	runtimeRoot  string
	capabilities domain.ExecutionPermissionRuntimeCapabilities
}

// RunStandardCodeSecurityRecoveryWorker executes only the fixed internal
// prepare/recover lifecycle. It never accepts a command, environment, host
// permission, arbitrary workspace, Docker flag, or output target.
func RunStandardCodeSecurityRecoveryWorker(ctx context.Context,
	config StandardCodeSecurityRecoveryWorkerConfig,
) error {
	if config.CaseID == "recovery_renderer_close" {
		return errStandardCodeRendererRecoveryUnsupported
	}
	root, owner, err := validateStandardCodeSecurityRecoveryWorker(config)
	if err != nil {
		return err
	}
	if config.Phase == recoveryWorkerPreparePhase {
		return prepareStandardCodeSecurityRecovery(ctx, root, owner)
	}
	return recoverStandardCodeSecurityRecovery(ctx, root, owner)
}

func validateStandardCodeSecurityRecoveryWorker(
	config StandardCodeSecurityRecoveryWorkerConfig,
) (string, standardCodeSecurityRecoveryOwner, error) {
	root, err := filepath.Abs(strings.TrimSpace(config.Root))
	if err != nil || strings.TrimSpace(config.Root) == "" || filepath.Clean(root) != root ||
		!standardCodeRecoveryCases[config.CaseID] ||
		(config.Backend != "local" && config.Backend != "docker") ||
		(config.Phase != recoveryWorkerPreparePhase && config.Phase != recoveryWorkerRecoverPhase) ||
		filepath.Base(root) != standardCodeSecurityRecoveryDirectoryName(
			config.CaseID, config.Backend) {
		return "", standardCodeSecurityRecoveryOwner{},
			errors.New("packaged recovery worker configuration is invalid")
	}
	foundHarness := false
	harnessRoot := ""
	for current := filepath.Dir(root); ; current = filepath.Dir(current) {
		if strings.HasPrefix(strings.ToLower(filepath.Base(current)), "standard-code-attack-") {
			foundHarness = true
			harnessRoot = current
			break
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
	}
	info, statErr := os.Lstat(root)
	if !foundHarness || statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		standardCodeSecurityPathHasReparse(harnessRoot, root) {
		return "", standardCodeSecurityRecoveryOwner{},
			errors.New("packaged recovery worker root is not a direct harness-owned directory")
	}
	owner := standardCodeSecurityRecoveryOwner{Protocol: standardCodeSecurityRecoveryProtocol,
		CaseID: config.CaseID, Backend: config.Backend}
	var stored standardCodeSecurityRecoveryOwner
	if err := readStrictSecurityJSON(filepath.Join(root, "owner.json"), &stored, 4096); err != nil ||
		stored != owner {
		return "", standardCodeSecurityRecoveryOwner{},
			errors.New("packaged recovery worker ownership marker is invalid")
	}
	repository := filepath.Join(root, "repository")
	if repositoryInfo, repoErr := os.Lstat(repository); repoErr != nil ||
		!repositoryInfo.IsDir() || repositoryInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInside(root, repository) || standardCodeSecurityPathHasReparse(root, repository) {
		return "", standardCodeSecurityRecoveryOwner{},
			errors.New("packaged recovery worker repository is unavailable or indirect")
	}
	return root, owner, nil
}

func standardCodeSecurityPathHasReparse(parent, child string) bool {
	if !pathInside(parent, child) {
		return true
	}
	for current := filepath.Clean(child); ; current = filepath.Dir(current) {
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return true
		}
		attributes, err := windows.GetFileAttributes(pointer)
		if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return true
		}
		if strings.EqualFold(current, filepath.Clean(parent)) {
			return false
		}
		next := filepath.Dir(current)
		if next == current {
			return true
		}
	}
}

func openStandardCodeSecurityRecoveryPlane(ctx context.Context, root,
	backend string,
) (*standardCodeSecurityRecoveryPlane, error) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("resolve fixed recovery Go toolchain: %w", err)
	}
	goPath, err = filepath.Abs(goPath)
	if err != nil {
		return nil, err
	}
	local, err := sandbox.NewPlatformLocalBackend(sandbox.WithLocalOwnerRoot(
		filepath.Join(root, "local-owners")))
	if err != nil {
		return nil, err
	}
	readiness, err := local.Readiness(ctx, sandbox.LocalRuntimeCapabilities{Enabled: true})
	if err != nil || !readiness.Ready {
		_ = local.Close()
		return nil, fmt.Errorf("prove recovery Local Sandbox readiness: %w", err)
	}
	readToken, err := randomDesktopSecurityToken()
	if err != nil {
		_ = local.Close()
		return nil, err
	}
	controlToken, err := randomDesktopSecurityToken()
	if err != nil {
		_ = local.Close()
		return nil, err
	}
	home := filepath.Join(root, "desktop-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		_ = local.Close()
		return nil, err
	}
	dockerDigest := ""
	if backend == "docker" {
		dockerDigest = strings.TrimSpace(os.Getenv(standardCodeSecurityDockerEnv))
		if dockerDigest == "" {
			_ = local.Close()
			return nil, errors.New("packaged recovery Docker digest is unavailable")
		}
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
	plane, err := OpenControlPlane(ControlPlaneConfig{
		DatabasePath: filepath.Join(home, "security-recovery.db"), HomePath: home,
		ReadToken: readToken, ControlToken: controlToken,
		RunControlEnabled: true, RunCreationEnabled: true,
		ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities:   capabilities,
		LocalSandboxReadiness:             &readiness, LocalSandboxBackend: local,
		RunLifecycleEnabled: true, RunExecutionEnabled: true,
		PlanDeliveryControlEnabled: true, ApprovalControlEnabled: true,
		DockerExecutionEnabled:        backend == "docker",
		StandardCodeDockerImageDigest: dockerDigest,
		CredentialStore:               credential.NewMemoryStore(),
		AppVersion:                    "packaged-security-recovery-181",
	})
	if err != nil {
		_ = local.Close()
		return nil, err
	}
	if plane.commandRuntime == nil || plane.standardCodeDrydocks == nil ||
		plane.standardCodePreset == nil || plane.policyChecker == nil {
		_ = plane.Close()
		_ = local.Close()
		return nil, errors.New("packaged recovery product path is incomplete")
	}
	return &standardCodeSecurityRecoveryPlane{local: local, plane: plane,
		gateway: toolgateway.New(plane.stateStore, plane.policyChecker).
			WithCommandRuntimeExecutor(plane.commandRuntime),
		goPath: goPath, readToken: readToken, backend: backend, runtimeRoot: root, capabilities: capabilities}, nil
}

func (p *standardCodeSecurityRecoveryPlane) close() error {
	if p == nil {
		return nil
	}
	var err error
	if p.plane != nil {
		err = errors.Join(err, p.plane.Close())
	}
	if p.local != nil {
		err = errors.Join(err, p.local.Close())
	}
	return err
}

func prepareStandardCodeSecurityRecovery(ctx context.Context, root string,
	owner standardCodeSecurityRecoveryOwner,
) (resultErr error) {
	statePath := filepath.Join(root, recoveryWorkerStateName)
	if _, err := os.Lstat(statePath); !errors.Is(err, os.ErrNotExist) {
		return errors.New("packaged recovery state already exists")
	}
	opened, err := openStandardCodeSecurityRecoveryPlane(ctx, root, owner.Backend)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, opened.close()) }()

	workspace, err := opened.plane.RegisterWorkspaceDirectory(ctx,
		filepath.Join(root, "repository"))
	if err != nil {
		return err
	}
	goal := "exercise one fixed packaged Standard Code recovery boundary"
	preview, err := opened.plane.standardCodePreset.Configure(ctx,
		application.ConfigureStandardCodeRequest{Version: domain.StandardCodePresetProtocolVersion,
			WorkspaceID: workspace.ID, Goal: goal, BackendIntent: owner.Backend,
			Action: "configure", OperationKey: "issue181-recovery-preview-" + owner.CaseID,
			RequestedBy: "operator"})
	if err != nil || !preview.TrustRequired || preview.TrustDigest == "" {
		return fmt.Errorf("preview recovery Standard Code workspace: %w", err)
	}
	configured, err := opened.plane.standardCodePreset.Configure(ctx,
		application.ConfigureStandardCodeRequest{Version: domain.StandardCodePresetProtocolVersion,
			WorkspaceID: workspace.ID, Goal: goal, BackendIntent: owner.Backend,
			Action: "configure", OperationKey: "issue181-recovery-configure-" + owner.CaseID,
			RequestedBy: "operator", ConfirmWorkspaceTrust: true,
			ExpectedTrustDigest: preview.TrustDigest})
	if err != nil || configured.Run == nil || configured.Permission == nil ||
		configured.Permission.Mode != domain.RunExecutionPermissionAsk ||
		configured.CapabilityGrant {
		return fmt.Errorf("configure recovery Standard Code workspace: %w", err)
	}
	workspaceState, found, err := opened.plane.stateStore.GetDrydockByRun(ctx,
		configured.RunID)
	if err != nil || !found || workspaceState.State != runworktree.StateReady {
		return fmt.Errorf("load recovery Standard Code Drydock: %w", err)
	}
	initialCheckpoint := workspaceState.LastCheckpointID
	probeDirectory := filepath.Join(workspaceState.Path, ".standard-code-security")
	if err := os.Mkdir(probeDirectory, 0o700); err != nil {
		return err
	}
	if err := writeExclusiveSecurityFile(filepath.Join(probeDirectory, "probe.go"),
		standardCodeSecurityProbe); err != nil {
		return err
	}
	checkpoint, err := opened.plane.standardCodeDrydocks.Checkpoint(ctx,
		application.DrydockCheckpointRequest{RunID: configured.RunID,
			ExpectedGeneration: workspaceState.Generation,
			OperationKey:       "issue181-recovery-probe-" + owner.CaseID,
			RequestedBy:        "operator", Title: "Fixed packaged recovery probe",
			ConfirmObservedChanges: true})
	if err != nil || checkpoint.Workspace.LastCheckpointID == initialCheckpoint {
		return fmt.Errorf("checkpoint recovery Standard Code probe: %w", err)
	}
	runs := application.NewRunService(opened.plane.stateStore)
	if _, err := runs.ChangePhase(ctx, application.ChangeRunPhaseRequest{
		RunID: configured.RunID, Phase: string(domain.ExecutionPhaseDeliver),
		OperationKey: "issue181-recovery-deliver-" + owner.CaseID,
		RequestedBy:  "operator", Reason: "exercise fixed packaged recovery"}); err != nil {
		return err
	}
	runRecord, err := runs.Start(ctx, configured.RunID)
	if err != nil {
		return err
	}
	rootAgent, found, err := opened.plane.stateStore.GetRootAgent(ctx, runRecord.ID)
	if err != nil || !found {
		return fmt.Errorf("load recovery root Agent: %w", err)
	}
	probeExecutable := opened.goPath
	probeArguments := []string{"run", ".standard-code-security/probe.go",
		"recovery_hold", "fixed"}
	if owner.Backend == "local" {
		probeExecutable, err = prepareStandardCodeSecurityLocalProbe(ctx, root,
			opened.goPath)
		if err != nil {
			return fmt.Errorf("build fixed recovery Local Sandbox probe: %w", err)
		}
		probeArguments = []string{"recovery_hold", "fixed"}
	}
	leaseTTL := runner.CommandRuntimeOwnerLeaseTTL
	if owner.CaseID == "recovery_lease_expiry" {
		leaseTTL = 5 * time.Second
	}
	acquired, err := opened.plane.stateStore.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: runRecord.ID,
			OwnerID: "packaged-recovery-181-" + owner.Backend, TTL: leaseTTL})
	if err != nil {
		return err
	}
	adapter, available, err := opened.plane.commandRuntime.AdvertisedCommandRuntimeAdapter(
		ctx, runRecord.ID, configured.Permission.Mode)
	if err != nil || !available {
		return fmt.Errorf("advertise recovery command adapter: %w", err)
	}
	input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionStart,
		Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion,
			Profile: runner.CommandRuntimeProcess, Executable: probeExecutable,
			Arguments: probeArguments, WorkingDirectory: ".",
			Environment: []runner.CommandRuntimeEnvironment{},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: int64((10 * time.Minute).Milliseconds()),
			Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
			Network:             runner.CommandRuntimeNetworkDisabled,
			Credentials:         runner.CommandRuntimeCredentialsNone,
			Purpose:             "hold one fixed packaged recovery boundary"}}}
	probe := &standardCodeSecurityRun{backend: owner.Backend, workspaceID: workspace.ID,
		run: runRecord, root: rootAgent, lease: acquired.Lease, adapter: adapter}
	call, err := probe.prepareCommand(ctx, opened.plane, opened.capabilities, input)
	if err != nil {
		return err
	}
	outcome, err := probe.invokeCommand(ctx, opened.plane, opened.gateway, call)
	if err != nil || outcome.Result == nil || outcome.Result.Status != toolgateway.StatusCompleted {
		return fmt.Errorf("start fixed recovery Job: %w", err)
	}
	var receipt struct {
		Version string                             `json:"version"`
		Action  string                             `json:"action"`
		Jobs    []runner.CommandRuntimeJobSnapshot `json:"jobs"`
	}
	if outcome.Result.Truncated || json.Unmarshal([]byte(outcome.Result.Stdout), &receipt) != nil ||
		receipt.Version != runner.CommandRuntimeResultVersion ||
		receipt.Action != toolgateway.CommandRuntimeActionStart || len(receipt.Jobs) != 1 ||
		receipt.Jobs[0].ID == "" {
		return errors.New("start fixed recovery Job omitted its durable Job identity")
	}
	jobID := receipt.Jobs[0].ID
	job, err := opened.plane.stateStore.GetCommandRuntimeJob(ctx, jobID)
	if err != nil || job.State != runner.CommandRuntimeJobRunning {
		return fmt.Errorf("observe running recovery Job: %w", err)
	}
	relativeDrydock, err := filepath.Rel(root, workspaceState.Path)
	if err != nil || relativeDrydock == ".." ||
		strings.HasPrefix(relativeDrydock, ".."+string(filepath.Separator)) {
		return errors.New("recovery Drydock escaped its owned root")
	}
	state := standardCodeSecurityRecoveryState{Protocol: standardCodeSecurityRecoveryProtocol,
		CaseID: owner.CaseID, Backend: owner.Backend, RunID: runRecord.ID,
		WorkspaceID: workspace.ID, RootAgentID: rootAgent.ID, JobID: jobID,
		DrydockRelativePath:  relativeDrydock,
		InitialCheckpointID:  initialCheckpoint,
		PreparedCheckpointID: checkpoint.Workspace.LastCheckpointID,
		LeaseID:              acquired.Lease.LeaseID, LeaseGeneration: acquired.Lease.Generation,
		Adapter: adapter, CreatedAt: time.Now().UTC()}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := writeExclusiveSecurityFile(statePath, append(encoded, '\n')); err != nil {
		return err
	}
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	if err := opened.plane.StartTerminalBoundaryMonitor(monitorCtx); err != nil {
		return err
	}
	if err := writeExclusiveSecurityFile(filepath.Join(root, recoveryWorkerReadyName),
		[]byte("ready\n")); err != nil {
		return err
	}
	if err := waitForStandardCodeSecurityRecoverySignal(ctx, root); err != nil {
		return err
	}
	if _, _, err := opened.plane.stateStore.ReleaseRunExecutionLease(ctx,
		acquired.Lease); err != nil {
		return fmt.Errorf("release fixed recovery execution lease: %w", err)
	}
	return nil
}

func waitForStandardCodeSecurityRecoverySignal(ctx context.Context, root string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Lstat(filepath.Join(root, recoveryWorkerShutdownName)); err == nil {
				return nil
			}
		}
	}
}

func recoverStandardCodeSecurityRecovery(ctx context.Context, root string,
	owner standardCodeSecurityRecoveryOwner,
) (resultErr error) {
	var state standardCodeSecurityRecoveryState
	if err := readStrictSecurityJSON(filepath.Join(root, recoveryWorkerStateName),
		&state, 64<<10); err != nil || state.Protocol != standardCodeSecurityRecoveryProtocol ||
		state.CaseID != owner.CaseID || state.Backend != owner.Backend ||
		state.RunID == "" || state.JobID == "" || state.PreparedCheckpointID == "" ||
		state.Adapter.Validate() != nil {
		return errors.New("packaged recovery durable state is invalid")
	}
	first, err := openStandardCodeSecurityRecoveryPlane(ctx, root, owner.Backend)
	if err != nil {
		return err
	}
	receipt, err := observeStandardCodeSecurityRecovery(ctx, first, root, owner, state)
	closeErr := first.close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	second, err := openStandardCodeSecurityRecoveryPlane(ctx, root, owner.Backend)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, second.close()) }()
	replayed, err := observeStandardCodeSecurityRecovery(ctx, second, root, owner, state)
	if err != nil || replayed.JobSHA256 != receipt.JobSHA256 ||
		replayed.CheckpointSHA256 != receipt.CheckpointSHA256 ||
		replayed.WorkspaceSHA256 != receipt.WorkspaceSHA256 ||
		replayed.Signal != receipt.Signal || replayed.Preserved != receipt.Preserved {
		return errors.Join(err, errors.New("packaged recovery replay drifted"))
	}
	receipt.RecoveryCycles = 2
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return writeExclusiveSecurityFile(filepath.Join(root, recoveryWorkerReceiptName),
		append(encoded, '\n'))
}

func observeStandardCodeSecurityRecovery(ctx context.Context,
	opened *standardCodeSecurityRecoveryPlane, root string,
	owner standardCodeSecurityRecoveryOwner, state standardCodeSecurityRecoveryState,
) (standardCodeSecurityRecoveryReceipt, error) {
	if err := opened.plane.reconcileCommandRuntime(ctx); err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	if _, err := opened.plane.standardCodeDrydocks.Reconcile(ctx); err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	job, err := opened.plane.stateStore.GetCommandRuntimeJob(ctx, state.JobID)
	if err != nil || !job.State.Terminal() || !job.TreeReaped ||
		job.Network != runner.CommandRuntimeNetworkDisabled ||
		job.Credentials != runner.CommandRuntimeCredentialsNone {
		return standardCodeSecurityRecoveryReceipt{},
			fmt.Errorf("recovered Job boundary is incomplete: %w", err)
	}
	workspace, found, err := opened.plane.stateStore.GetDrydockByRun(ctx, state.RunID)
	if err != nil || !found || workspace.LastCheckpointID == "" {
		return standardCodeSecurityRecoveryReceipt{},
			fmt.Errorf("recovered Drydock is unavailable: %w", err)
	}
	preserved := owner.CaseID != "recovery_dirty_untracked_concurrent_edit"
	if !preserved {
		preserved = workspace.State == runworktree.StateRecoveryRequired
		drydockRoot := filepath.Join(root, state.DrydockRelativePath)
		tracked, trackedErr := os.ReadFile(filepath.Join(drydockRoot, "README.md"))
		untracked, untrackedErr := os.ReadFile(filepath.Join(drydockRoot,
			"issue181-concurrent.bin"))
		preserved = preserved && trackedErr == nil && untrackedErr == nil &&
			bytes.Contains(tracked, []byte("issue181-concurrent-edit\r\n")) &&
			bytes.Equal(untracked, []byte{0x00, 0x18, 0x01, 0xff})
	}
	if !preserved {
		return standardCodeSecurityRecoveryReceipt{},
			errors.New("recovery did not preserve the concurrent Drydock edits")
	}
	checkpoint, err := opened.plane.stateStore.GetWorkspaceCheckpointSnapshot(ctx,
		workspace.LastCheckpointID)
	if err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	events, err := opened.plane.stateStore.ListRunEvents(ctx, state.RunID)
	if err != nil || len(events) == 0 {
		return standardCodeSecurityRecoveryReceipt{}, errors.Join(err,
			errors.New("recovery immutable events are unavailable"))
	}
	projection, err := opened.plane.standardCodeDrydocks.Projection(ctx, state.RunID, 50)
	if err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	signal := "interrupted"
	if owner.CaseID == "recovery_reboot_equivalent" ||
		owner.CaseID == "recovery_dirty_untracked_concurrent_edit" {
		signal = "recovery_required"
	}
	return standardCodeSecurityRecoveryReceipt{Protocol: standardCodeSecurityRecoveryProtocol,
		CaseID: owner.CaseID, Backend: owner.Backend, Status: "passed", Signal: signal,
		JobState: string(job.State), TreeReaped: job.TreeReaped, Preserved: preserved,
		RecoveryCycles: 1, EventSHA256: hashSecurityValue(events),
		JobSHA256: hashSecurityValue(job), CheckpointSHA256: hashSecurityValue(checkpoint),
		WorkspaceSHA256: hashSecurityValue(projection),
		TranscriptSHA256: hashSecurityValue(struct {
			Events any                      `json:"events"`
			Job    runner.CommandRuntimeJob `json:"job"`
		}{Events: events, Job: job})}, nil
}

func (d *standardCodeSecurityDriver) verifyPackagedRecovery(ctx context.Context,
	request packagede2e.SecurityDriverCase,
) (standardCodeSecurityRecoveryReceipt, error) {
	if request.Attack.ID == "recovery_renderer_close" {
		return standardCodeSecurityRecoveryReceipt{}, errStandardCodeRendererRecoveryUnsupported
	}
	root := filepath.Join(d.config.OwnedRuntimeRoot, "recovery",
		standardCodeSecurityRecoveryDirectoryName(request.Attack.ID, request.Backend))
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	owner := standardCodeSecurityRecoveryOwner{Protocol: standardCodeSecurityRecoveryProtocol,
		CaseID: request.Attack.ID, Backend: request.Backend}
	ownerRaw, _ := json.Marshal(owner)
	if err := writeExclusiveSecurityFile(filepath.Join(root, "owner.json"),
		append(ownerRaw, '\n')); err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	if err := cloneStandardCodeSecurityFixture(ctx,
		filepath.Join(d.config.FixtureRoot, request.FixtureID),
		filepath.Join(root, "repository")); err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	prepare := exec.Command(executable,
		"--standard-code-attack-recovery-worker",
		"--recovery-worker-root", root,
		"--recovery-worker-case", request.Attack.ID,
		"--recovery-worker-backend", request.Backend,
		"--recovery-worker-phase", recoveryWorkerPreparePhase)
	configureStandardCodeSecuritySubprocess(prepare)
	prepare.Env = standardCodeSecurityWorkerEnvironment(d.dockerDigest)
	var prepareOutput bytes.Buffer
	prepare.Stdout, prepare.Stderr = &prepareOutput, &prepareOutput
	if err := prepare.Start(); err != nil {
		return standardCodeSecurityRecoveryReceipt{}, err
	}
	prepareSettled := false
	defer func() {
		if !prepareSettled && prepare.Process != nil {
			_ = prepare.Process.Kill()
			_ = prepare.Wait()
		}
	}()
	if err := waitForSecurityWorkerFile(ctx, prepare,
		filepath.Join(root, recoveryWorkerReadyName), 90*time.Second); err != nil {
		return standardCodeSecurityRecoveryReceipt{}, fmt.Errorf(
			"prepare packaged recovery worker: %w: %s", err,
			strings.TrimSpace(prepareOutput.String()))
	}
	forceTermination := standardCodeSecurityRecoveryForceTermination(request.Attack.ID)
	if request.Attack.ID == "recovery_dirty_untracked_concurrent_edit" {
		if err := seedStandardCodeSecurityConcurrentEdit(root); err != nil {
			return standardCodeSecurityRecoveryReceipt{}, err
		}
	}
	if forceTermination {
		if err := prepare.Process.Kill(); err != nil {
			return standardCodeSecurityRecoveryReceipt{}, err
		}
		_ = prepare.Wait()
		prepareSettled = true
		// The durable command owner lease is the restart adoption fence. A new
		// process must wait for its exact expiry instead of assuming that a PID
		// or container belongs to it.
		select {
		case <-ctx.Done():
			return standardCodeSecurityRecoveryReceipt{}, ctx.Err()
		case <-time.After(runner.CommandRuntimeOwnerLeaseTTL + time.Second):
		}
	} else {
		if err := writeExclusiveSecurityFile(filepath.Join(root,
			recoveryWorkerShutdownName), []byte("shutdown\n")); err != nil {
			return standardCodeSecurityRecoveryReceipt{}, err
		}
		if err := prepare.Wait(); err != nil {
			return standardCodeSecurityRecoveryReceipt{}, fmt.Errorf(
				"clean packaged recovery worker exit: %w: %s", err,
				strings.TrimSpace(prepareOutput.String()))
		}
		prepareSettled = true
	}
	recoverCommand := exec.Command(executable,
		"--standard-code-attack-recovery-worker",
		"--recovery-worker-root", root,
		"--recovery-worker-case", request.Attack.ID,
		"--recovery-worker-backend", request.Backend,
		"--recovery-worker-phase", recoveryWorkerRecoverPhase)
	configureStandardCodeSecuritySubprocess(recoverCommand)
	recoverCommand.Env = standardCodeSecurityWorkerEnvironment(d.dockerDigest)
	recoverOutput, err := recoverCommand.CombinedOutput()
	if err != nil {
		return standardCodeSecurityRecoveryReceipt{}, fmt.Errorf(
			"recover packaged worker: %w: %s", err, strings.TrimSpace(string(recoverOutput)))
	}
	var receipt standardCodeSecurityRecoveryReceipt
	if err := readStrictSecurityJSON(filepath.Join(root, recoveryWorkerReceiptName),
		&receipt, 64<<10); err != nil || receipt.Protocol != standardCodeSecurityRecoveryProtocol ||
		receipt.CaseID != request.Attack.ID || receipt.Backend != request.Backend ||
		receipt.Status != "passed" || receipt.Signal != request.Attack.ExpectedSignal ||
		!receipt.TreeReaped || !receipt.Preserved || receipt.RecoveryCycles != 2 ||
		receipt.EventSHA256 == "" || receipt.JobSHA256 == "" ||
		receipt.CheckpointSHA256 == "" || receipt.WorkspaceSHA256 == "" ||
		receipt.TranscriptSHA256 == "" {
		return standardCodeSecurityRecoveryReceipt{},
			errors.New("packaged recovery receipt is invalid or mismatched")
	}
	d.recoveryJobsStarted++
	d.recoveryJobsReaped++
	return receipt, nil
}

func standardCodeSecurityRecoveryForceTermination(caseID string) bool {
	switch caseID {
	case "recovery_force_termination", "recovery_reboot_equivalent",
		"recovery_lease_expiry", "recovery_dirty_untracked_concurrent_edit":
		return true
	default:
		return false
	}
}

func standardCodeSecurityRecoveryDirectoryName(caseID, backend string) string {
	digest := hashSecurityValue(struct {
		Protocol string `json:"protocol"`
		CaseID   string `json:"case_id"`
		Backend  string `json:"backend"`
	}{Protocol: standardCodeSecurityRecoveryProtocol, CaseID: caseID, Backend: backend})
	return "recovery-" + digest[:24]
}

func waitForSecurityWorkerFile(ctx context.Context, command *exec.Cmd, path string,
	timeout time.Duration,
) error {
	deadline := time.Now().Add(timeout)
	for {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() &&
			info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		if command == nil || command.Process == nil {
			return errors.New("packaged recovery worker process is unavailable")
		}
		if exited, err := processExited(command.Process); err != nil || exited {
			_ = command.Wait()
			return errors.Join(err, errors.New("packaged recovery worker exited before its marker"))
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_ = command.Wait()
			return errors.New("packaged recovery worker marker timed out")
		}
		select {
		case <-ctx.Done():
			_ = command.Process.Kill()
			_ = command.Wait()
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func cloneStandardCodeSecurityFixture(ctx context.Context, source, destination string) error {
	if !pathInside(filepath.Dir(source), source) || !pathInside(filepath.Dir(destination), destination) {
		return errors.New("fixed recovery fixture paths are invalid")
	}
	command := exec.CommandContext(ctx, "git", "-c", "core.autocrlf=false",
		"-c", "core.filemode=false", "-c", "core.symlinks=false",
		"clone", "--quiet", "--no-hardlinks", "--local", source, destination)
	configureStandardCodeSecuritySubprocess(command)
	command.Env = standardCodeSecurityWorkerEnvironment("")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone fixed recovery fixture: %w: %s", err,
			strings.TrimSpace(string(output)))
	}
	return nil
}

func seedStandardCodeSecurityConcurrentEdit(root string) error {
	var state standardCodeSecurityRecoveryState
	if err := readStrictSecurityJSON(filepath.Join(root, recoveryWorkerStateName),
		&state, 64<<10); err != nil {
		return err
	}
	drydockRoot := filepath.Join(root, state.DrydockRelativePath)
	if !pathInside(root, drydockRoot) {
		return errors.New("concurrent edit Drydock escaped its owned root")
	}
	trackedPath := filepath.Join(drydockRoot, "README.md")
	tracked, err := os.ReadFile(trackedPath)
	if err != nil {
		return err
	}
	tracked = append(tracked, []byte("\r\nissue181-concurrent-edit\r\n")...)
	file, err := os.OpenFile(trackedPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(tracked); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return writeExclusiveSecurityFile(filepath.Join(drydockRoot,
		"issue181-concurrent.bin"), []byte{0x00, 0x18, 0x01, 0xff})
}

func standardCodeSecurityWorkerEnvironment(dockerDigest string) []string {
	allowed := map[string]bool{"PATH": true, "PATHEXT": true, "SYSTEMROOT": true,
		"WINDIR": true, "COMSPEC": true, "TEMP": true, "TMP": true,
		"PROGRAMFILES": true, "PROGRAMFILES(X86)": true, "PROGRAMDATA": true,
		"LOCALAPPDATA": true, "APPDATA": true, "USERPROFILE": true,
		"HOMEDRIVE": true, "HOMEPATH": true, "GOCACHE": true, "GOMODCACHE": true,
		"GOTOOLCHAIN": true, "GOPROXY": true, "GOSUMDB": true,
		"DOCKER_HOST": true, "DOCKER_CONTEXT": true}
	values := make([]string, 0, len(allowed)+1)
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if ok && allowed[strings.ToUpper(key)] {
			values = append(values, value)
		}
	}
	if strings.TrimSpace(dockerDigest) != "" {
		values = append(values, standardCodeSecurityDockerEnv+"="+
			strings.TrimSpace(dockerDigest))
	}
	return values
}

func readStrictSecurityJSON(path string, target any, maximum int64) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > maximum {
		return errors.New("fixed security JSON is unavailable or indirect")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("fixed security JSON contains trailing data")
	}
	return nil
}

func processExited(process *os.Process) (bool, error) {
	if process == nil || process.Pid <= 0 {
		return true, nil
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false,
		uint32(process.Pid))
	if err != nil {
		// The child is ours and runs at the same integrity level. Failure to
		// reopen its PID therefore means the process has already disappeared.
		return true, nil
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, err
	}
	return status == windows.WAIT_OBJECT_0, nil
}
