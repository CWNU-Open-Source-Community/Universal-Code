package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/workspace"
)

const batchValidationActor = "native-batch-validator"

const batchValidationDockerRequesterPrefix = batchValidationActor + "/v1/"

// The immutable requester carries only a native ledger reference and directory
// identity. It can reconstruct cleanup ownership after restart, never a grant.
func batchValidationDockerRequester(t *batchValidationTarget) string {
	queue := t.ref.QueueID
	if queue == "" {
		queue = "-"
	}
	return batchValidationDockerRequesterPrefix + strings.Join([]string{t.ref.PlanID, queue,
		strconv.Itoa(t.ref.Ordinal), strconv.FormatInt(t.ref.Generation, 10), t.rootIdentity}, "/")
}

func isBatchValidationDockerRequester(value string) bool {
	return value == batchValidationActor || strings.HasPrefix(value, batchValidationActor+"/")
}

func parseBatchValidationDockerRequester(value string) (batchValidationTargetRef, string, error) {
	invalid := errors.New("batch Docker cleanup target reference is invalid")
	if !validDockerSandboxIdentity(value) || !strings.HasPrefix(value, batchValidationDockerRequesterPrefix) {
		return batchValidationTargetRef{}, "", invalid
	}
	parts := strings.Split(strings.TrimPrefix(value, batchValidationDockerRequesterPrefix), "/")
	if len(parts) != 5 {
		return batchValidationTargetRef{}, "", invalid
	}
	validID := func(id string) bool {
		if len(id) == 0 || len(id) > 96 || !domain.ValidAgentID(id) {
			return false
		}
		for _, r := range id {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
		return true
	}
	ordinal, ordinalErr := strconv.Atoi(parts[2])
	generation, generationErr := strconv.ParseInt(parts[3], 10, 64)
	identity, identityErr := hex.DecodeString(parts[4])
	if !validID(parts[0]) || parts[1] != "-" && !validID(parts[1]) || ordinalErr != nil || generationErr != nil ||
		strconv.Itoa(ordinal) != parts[2] || strconv.FormatInt(generation, 10) != parts[3] ||
		identityErr != nil || len(identity) != sha256.Size || hex.EncodeToString(identity) != parts[4] {
		return batchValidationTargetRef{}, "", invalid
	}
	ref := batchValidationTargetRef{PlanID: parts[0], Ordinal: ordinal, Generation: generation}
	if parts[1] == "-" {
		if ordinal < 1 || generation < 1 {
			return batchValidationTargetRef{}, "", invalid
		}
	} else {
		if ordinal != 0 || generation != 0 {
			return batchValidationTargetRef{}, "", invalid
		}
		ref.QueueID = parts[1]
	}
	return ref, parts[4], nil
}

func (s *DockerSandboxService) batchValidationCleanupWorkspace(ctx context.Context, admission domain.DockerSandboxAdmission, owned sandbox.WorkspaceBinding) (sandbox.WorkspaceBinding, error) {
	ref, identity, err := parseBatchValidationDockerRequester(admission.RequestedBy)
	if err != nil {
		return sandbox.WorkspaceBinding{}, err
	}
	store, ok := s.store.(interface {
		GetBatchDeliveryPlan(context.Context, string) (domain.BatchDeliveryPlan, bool, error)
		GetBatchDeliveryWorkspace(context.Context, string, int) (domain.BatchDeliveryWorkspace, bool, error)
		GetBatchDeliveryMergeQueue(context.Context, string) (domain.BatchDeliveryMergeQueue, bool, error)
	})
	if !ok {
		return sandbox.WorkspaceBinding{}, errors.New("batch Docker cleanup ledger is unavailable")
	}
	plan, found, err := store.GetBatchDeliveryPlan(ctx, ref.PlanID)
	if err != nil || !found || plan.RunID != admission.RunID || plan.WorkspaceID != admission.WorkspaceID {
		return sandbox.WorkspaceBinding{}, errors.New("batch Docker cleanup plan changed")
	}
	var root string
	if ref.QueueID != "" {
		queue, found, err := store.GetBatchDeliveryMergeQueue(ctx, ref.QueueID)
		if err != nil || !found || queue.PlanID != plan.ID {
			return sandbox.WorkspaceBinding{}, errors.New("batch Docker cleanup integration changed")
		}
		root = queue.IntegrationRoot
	} else {
		child, found, err := store.GetBatchDeliveryWorkspace(ctx, ref.PlanID, ref.Ordinal)
		if err != nil || !found || child.Generation != ref.Generation {
			return sandbox.WorkspaceBinding{}, errors.New("batch Docker cleanup child generation changed")
		}
		root = child.WorktreeRoot
	}
	actual, err := workspace.AgentCodeRootFingerprint(root)
	if err != nil || actual != identity {
		return sandbox.WorkspaceBinding{}, errors.New("batch Docker cleanup directory identity changed")
	}
	owned.RootPath = root
	return owned, nil
}

func (s *BatchDeliveryService) requireBatchLocalHostValidation(ctx context.Context, runID string) error {
	reader, ok := s.store.(interface {
		GetRunExecutionProfile(context.Context, string) (domain.RunExecutionProfileSnapshot, error)
	})
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "batch validation execution profile is unavailable")
	}
	profile, err := reader.GetRunExecutionProfile(ctx, runID)
	if err != nil {
		return err
	}
	owned, err := runHasOwnedFileWorkspace(ctx, s.store, runID)
	if err != nil {
		return err
	}
	if profile.Profile != domain.RunExecutionProfileLocal || owned {
		return apperror.New(apperror.CodeFailedPrecondition, "selected batch validation backend is unavailable; host fallback is forbidden")
	}
	return nil
}

// These references are supplied by native Submit/Review/Merge/verify callers.
// The executable root is always reread from the corresponding durable record.
type batchValidationTargetRef struct {
	PlanID, QueueID, OperationKey string
	Ordinal                       int
	Generation                    int64
	parentLeaseID                 string
	parentLeaseGeneration         int64
}

type batchValidationTarget struct {
	service              *BatchDeliveryService
	ref                  batchValidationTargetRef
	runID, root, base    string
	head, sourceRoot     string
	branch, rootIdentity string
}

func (s *BatchDeliveryService) bindBatchValidationTarget(ctx context.Context, runID, root, base string, ref batchValidationTargetRef) (*batchValidationTarget, error) {
	target := &batchValidationTarget{service: s, ref: ref, runID: runID, root: root, base: base}
	if ref.PlanID == "" || ref.OperationKey == "" {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "batch validation requires a recorded execution target")
	}
	if ref.QueueID != "" {
		queue, found, err := s.store.GetBatchDeliveryMergeQueue(ctx, ref.QueueID)
		if err != nil || !found || queue.PlanID != ref.PlanID || queue.IntegrationRoot != root || queue.LatestBaseCommit != base {
			return nil, apperror.New(apperror.CodeConflict, "batch integration validation target changed")
		}
		target.branch = queue.IntegrationBranch
	} else {
		child, found, err := s.store.GetBatchDeliveryWorkspace(ctx, ref.PlanID, ref.Ordinal)
		if err != nil || !found || child.Generation != ref.Generation || child.WorktreeRoot != root || child.BaseCommit != base {
			return nil, apperror.New(apperror.CodeConflict, "batch child validation target changed")
		}
		target.branch = child.Branch
	}
	var err error
	target.rootIdentity, err = workspace.AgentCodeRootFingerprint(root)
	if err != nil {
		return nil, err
	}
	plan, found, err := s.store.GetBatchDeliveryPlan(ctx, ref.PlanID)
	if err != nil || !found {
		return nil, errors.New("batch validation plan is unavailable")
	}
	source, err := s.store.GetWorkspaceInfo(ctx, plan.WorkspaceID)
	if err != nil {
		return nil, err
	}
	target.sourceRoot = source.RootPath
	inspection, err := repository.InspectBatchDelivery(ctx, root, target.branch, base, plan.Spec.Contract.MaxChangedFiles, plan.Spec.Contract.MaxDiffBytes)
	if err != nil {
		return nil, err
	}
	target.head = inspection.HeadCommit
	if err := target.check(ctx); err != nil {
		return nil, err
	}
	return target, target.checkExact(ctx)
}

func (t *batchValidationTarget) checkExact(ctx context.Context) error {
	return repository.VerifyBatchWorktreeBinding(ctx, t.sourceRoot, t.root, t.branch, t.head, true)
}

func (t *batchValidationTarget) check(ctx context.Context) error {
	plan, found, err := t.service.store.GetBatchDeliveryPlan(ctx, t.ref.PlanID)
	if err != nil || !found || plan.RunID != t.runID || plan.Status.Terminal() || plan.Status == domain.BatchDeliveryBlocked {
		return apperror.New(apperror.CodeConflict, "batch validation plan is no longer active")
	}
	if t.ref.QueueID != "" {
		queue, found, err := t.service.store.GetBatchDeliveryMergeQueue(ctx, t.ref.QueueID)
		if err != nil || !found || queue.PlanID != plan.ID || queue.IntegrationRoot != t.root || queue.LatestBaseCommit != t.base || queue.IntegrationBranch != t.branch || queue.Status != domain.BatchMergeQueueRunning {
			return apperror.New(apperror.CodeConflict, "batch validation integration ownership changed")
		}
	} else {
		child, found, err := t.service.store.GetBatchDeliveryWorkspace(ctx, plan.ID, t.ref.Ordinal)
		if err != nil || !found || child.Generation != t.ref.Generation || child.WorktreeRoot != t.root || child.BaseCommit != t.base || child.Branch != t.branch || child.Status.Terminal() || !t.service.now().UTC().Before(child.LeaseExpiresAt) {
			return apperror.New(apperror.CodePolicyDenied, "batch validation child ownership or lease changed")
		}
	}
	identity, err := workspace.AgentCodeRootFingerprint(t.root)
	if err != nil || identity != t.rootIdentity {
		return apperror.New(apperror.CodeConflict, "batch validation worktree identity changed")
	}
	return nil
}

type batchValidationRuntimeKey struct{}
type batchValidationRuntimeProof struct {
	target        *batchValidationTarget
	commandSHA256 string
	check         func(context.Context) error
}

type batchValidationJobBinding struct {
	proof                            *batchValidationRuntimeProof
	operationDigest, specFingerprint string
}

func batchValidationRuntimeFromContext(ctx context.Context) *batchValidationRuntimeProof {
	proof, _ := ctx.Value(batchValidationRuntimeKey{}).(*batchValidationRuntimeProof)
	return proof
}

// Preserve the existing Run/Drydock checks. Only an independently authorized
// native Batch invocation may substitute its recorded child/integration root.
func batchValidationExecutionWorkspace(ctx context.Context, owned runworktree.Workspace, scope runner.CommandRuntimeScope, spec runner.CommandRuntimeResolvedSpec) (runworktree.Workspace, error) {
	proof := batchValidationRuntimeFromContext(ctx)
	if proof == nil {
		return owned, nil
	}
	if proof.target.runID != scope.RunID || proof.target.root != spec.WorkspaceRoot || proof.check(ctx) != nil {
		return runworktree.Workspace{}, runner.ErrCommandRuntimeBoundary
	}
	owned.Path = proof.target.root
	owned.RootFingerprint = proof.target.rootIdentity
	return owned, nil
}

func batchValidationDockerWorkspace(ctx context.Context, binding sandbox.DockerStandardCodeRunnerBinding, owned sandbox.WorkspaceBinding) (sandbox.WorkspaceBinding, error) {
	proof := batchValidationRuntimeFromContext(ctx)
	if proof == nil {
		return owned, nil
	}
	if proof.target.runID != binding.RunID || proof.commandSHA256 == "" || proof.commandSHA256 != binding.CommandSHA256 || proof.check(ctx) != nil {
		return sandbox.WorkspaceBinding{}, runner.ErrCommandRuntimeBoundary
	}
	owned.RootPath = proof.target.root
	return owned, nil
}

type batchRuntimeValidationStarter struct {
	service            *BatchDeliveryService
	runtime            *CommandRuntimeService
	target             *batchValidationTarget
	requirement        domain.BatchDeliveryValidationRequirement
	execution          *domain.BatchValidationExecutionReceipt
	readinessCheckedAt time.Time
}

func (s *batchRuntimeValidationStarter) Name() string { return s.runtime.adapter.Backend }
func (s *batchRuntimeValidationStarter) Available() bool {
	return s.runtime.commandRuntimeAdapterCurrent()
}

func (s *batchRuntimeValidationStarter) BatchValidationCommand(root string, requirement domain.BatchDeliveryValidationRequirement) (string, []string, error) {
	if root != s.target.root || requirement != s.requirement {
		return "", nil, runner.ErrCommandRuntimeBoundary
	}
	pattern := "./..."
	if requirement.Scope != "." {
		pattern = "./" + strings.TrimSuffix(requirement.Scope, "/") + "/..."
	}
	id := "test.npm"
	if requirement.Kind == domain.BatchValidationGoTest {
		id = "test.go"
	}
	executable, launcher, err := resolveProjectCommandToolchain(id, s.runtime.adapter, root)
	if err != nil {
		return "", nil, err
	}
	if requirement.Kind == domain.BatchValidationGoTest {
		return executable, []string{"test", "-count=1", pattern}, nil
	}
	return executable, append(launcher, "test", "--", "--runInBand"), nil
}

func (s *batchRuntimeValidationStarter) BatchValidationGuestExecutable() bool {
	return s.runtime.adapter.Backend == CommandRuntimeDockerSandboxBackend || s.runtime.adapter.Backend == CommandRuntimeSBXBackend
}

func (s *batchRuntimeValidationStarter) Start(ctx context.Context, once runner.OnceStartSpec) (runner.OnceStartResult, error) {
	lease, found, err := s.runtime.store.GetRunExecutionLease(ctx, s.target.runID)
	if err != nil {
		return runner.OnceStartResult{}, err
	}
	if found && lease.ActiveAt(time.Now().UTC()) {
		if lease.LeaseID != s.target.ref.parentLeaseID || lease.Generation != s.target.ref.parentLeaseGeneration {
			return runner.OnceStartResult{}, apperror.New(apperror.CodeConflict, "batch validation Run is owned by another operation")
		}
		return s.start(ctx, once, lease)
	}
	if s.target.ref.parentLeaseID != "" || s.target.ref.parentLeaseGeneration != 0 {
		return runner.OnceStartResult{}, apperror.New(apperror.CodeConflict, "batch validation parent Run lease is no longer active")
	}
	leaseStore, ok := s.runtime.store.(RunExecutionLeaseStore)
	if !ok {
		return runner.OnceStartResult{}, apperror.New(apperror.CodeFailedPrecondition, "batch validation lease service is unavailable")
	}
	var result runner.OnceStartResult
	err = withRunExecutionLease(ctx, leaseStore, s.target.runID, idgen.New("batch-validation"), DefaultRunExecutionLeasePolicy(), func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
		var runErr error
		result, runErr = s.start(leaseCtx, once, lease)
		return runErr
	})
	return result, err
}

func (s *batchRuntimeValidationStarter) start(ctx context.Context, once runner.OnceStartSpec, expectedLease domain.RunExecutionLease) (runner.OnceStartResult, error) {
	empty := runner.OnceStartResult{}
	if err := s.target.check(ctx); err != nil {
		return empty, err
	}
	relative, err := filepath.Rel(s.target.root, once.WorkingDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return empty, runner.ErrCommandRuntimeBoundary
	}
	// Go's package pattern is relative to the repository root; npm's fixed
	// launcher runs in the declared package directory.
	if s.requirement.Kind == domain.BatchValidationGoTest {
		relative = "."
	}
	intent := runner.CommandRuntimeSpec{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
		Executable: once.ExecutablePath, Arguments: append([]string{}, once.Argv...), WorkingDirectory: filepath.ToSlash(relative),
		Environment: []runner.CommandRuntimeEnvironment{}, StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
		TimeoutMilliseconds: repository.MaxBatchValidationDuration.Milliseconds(),
		Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: runner.MinCommandRuntimeInlineBytes, ArtifactBytes: runner.MaxCommandRuntimeArtifactBytes},
		Network:             runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
		Purpose: "batch-validation:" + runmutation.Fingerprint("batch-validation-target.v1", s.target.ref.PlanID, s.target.ref.QueueID,
			fmt.Sprint(s.target.ref.Ordinal), fmt.Sprint(s.target.ref.Generation), s.target.rootIdentity, s.target.base, s.target.head)}
	intent.Environment = []runner.CommandRuntimeEnvironment{{Name: "npm_config_audit", Value: "false"}, {Name: "npm_config_fund", Value: "false"}}
	resolved, err := s.runtime.normalizeCommandRuntimeSpec(intent, s.target.root)
	if err != nil {
		return empty, fmt.Errorf("resolve Batch validation command: %w", err)
	}
	if s.runtime.adapter.Kind == commandruntimeadapter.KindHostUnsandboxed {
		// This native family retains its exact replaced environment (including
		// temporary HOME/cache). No request can supply or alter these bindings.
		resolved.Environment = append([]string{}, once.Environment...)
		raw, _ := json.Marshal(resolved.Environment)
		digest := sha256.Sum256(raw)
		resolved.EnvironmentSHA256 = hex.EncodeToString(digest[:])
	}
	run, err := s.runtime.store.GetRun(ctx, s.target.runID)
	if err != nil {
		return empty, err
	}
	mission, err := s.runtime.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return empty, err
	}
	root, found, err := s.runtime.store.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		return empty, apperror.New(apperror.CodeFailedPrecondition, "batch validation Root anchor is unavailable")
	}
	mode, err := s.runtime.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return empty, err
	}
	profile, err := s.runtime.store.GetRunExecutionProfile(ctx, run.ID)
	if err != nil {
		return empty, err
	}
	permission, err := s.runtime.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return empty, err
	}
	lease, found, err := s.runtime.store.GetRunExecutionLease(ctx, run.ID)
	if err != nil || !found || !lease.ActiveAt(time.Now().UTC()) || lease.LeaseID != expectedLease.LeaseID || lease.Generation != expectedLease.Generation || lease.OwnerID != expectedLease.OwnerID {
		return empty, apperror.New(apperror.CodePolicyDenied, "batch validation requires the current Run execution lease")
	}
	_, generation, epoch, fence, live := bindAgentCodeRuntime(s.service.executionPermissionCapabilities, permission)
	if !live || permission.Mode != domain.RunExecutionPermissionFull || epoch == "" || fence == 0 {
		return empty, apperror.New(apperror.CodePolicyDenied, "batch validation requires live Full authority")
	}
	key := "batch-validation-" + runmutation.Fingerprint("batch-validation-job.v1", s.target.ref.OperationKey, once.RequestFingerprint)
	scope := runner.CommandRuntimeScope{InvocationID: key, OperationKey: key, RunID: run.ID, MissionID: mission.ID,
		RootAgentID: root.ID, AgentID: root.ID, AttributionSource: domain.AgentAttributionOperatorRoot,
		SessionID: run.SessionID, WorkspaceID: mission.WorkspaceID, WorkspaceRootSHA256: resolved.WorkspaceRootSHA256,
		ModeSnapshotID: mode.ID, ModeRevision: mode.Revision, ProfileSnapshotID: profile.ID, ProfileRevision: profile.Revision,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionMode: permission.Mode,
		PermissionRuntimeEpoch: epoch, PermissionGeneration: generation, RunAuthorizationFence: fence,
		LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, LeaseOwnerID: lease.OwnerID, Adapter: s.runtime.adapter}
	operation, err := batchValidationOperation(key, struct {
		Scope runner.CommandRuntimeScope
		Spec  runner.CommandRuntimeResolvedSpec
	}{scope, resolved},
		[]toolcontract.Target{{Kind: "process", Locator: resolved.ExecutablePath}, {Kind: "directory", Locator: resolved.AbsoluteDirectory}})
	if err != nil {
		return empty, err
	}
	decision, err := s.service.batchValidationAuthorization(ctx, run.ID, resolved.AbsoluteDirectory, operation)
	if err != nil {
		return empty, err
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return empty, err
	}
	check := func(current context.Context) error {
		if err := decision.BeforeDispatch(current, fingerprint); err != nil {
			return err
		}
		if err := s.target.check(current); err != nil {
			return err
		}
		currentProfile, err := s.runtime.store.GetRunExecutionProfile(current, run.ID)
		if err != nil || currentProfile != profile || !s.runtime.commandRuntimeAdapterCurrent() || currentProfile.Profile != commandRuntimeExecutionProfile(scope.Adapter) {
			return apperror.New(apperror.CodePolicyDenied, "batch validation execution backend changed")
		}
		currentLease, found, err := s.runtime.store.GetRunExecutionLease(current, run.ID)
		if err != nil || !found || !currentLease.ActiveAt(time.Now().UTC()) || currentLease.LeaseID != lease.LeaseID || currentLease.Generation != lease.Generation || currentLease.OwnerID != lease.OwnerID {
			return apperror.New(apperror.CodePolicyDenied, "batch validation Run lease changed")
		}
		return nil
	}
	proof := &batchValidationRuntimeProof{target: s.target, check: check}
	if s.runtime.adapter.Backend == CommandRuntimeDockerSandboxBackend {
		command, err := commandRuntimeDockerCommand(resolved)
		if err != nil {
			return empty, err
		}
		proof.commandSHA256, err = command.Fingerprint()
		if err != nil {
			return empty, err
		}
	}
	ctx = context.WithValue(ctx, batchValidationRuntimeKey{}, proof)
	operationDigest, jobID := runner.CommandRuntimeOperationIdentity(run.ID, key)
	request := runner.CommandRuntimeStartRequest{Scope: scope, Spec: resolved, DiscardOutput: true,
		OwnershipCheck: check, DispatchCheck: func(current context.Context, actual runner.CommandRuntimeResolvedSpec) error {
			if runner.CommandRuntimeSpecFingerprint(actual) != runner.CommandRuntimeSpecFingerprint(resolved) {
				return runner.ErrCommandRuntimeBoundary
			}
			if err := check(current); err != nil {
				return err
			}
			return s.target.checkExact(current)
		}}
	// Publication is serialized with the durable cancellation fence. A Cancel
	// that starts after this section can observe every owned Job before cleanup.
	s.service.validationMu.Lock()
	if err := check(ctx); err != nil {
		s.service.validationMu.Unlock()
		return empty, err
	}
	binding := &batchValidationJobBinding{proof: proof, operationDigest: operationDigest,
		specFingerprint: runner.CommandRuntimeSpecFingerprint(resolved)}
	s.runtime.batchValidationJobs.LoadOrStore(jobID, binding)
	defer func() {
		stored, err := s.runtime.store.GetCommandRuntimeJob(context.WithoutCancel(ctx), jobID)
		if err == nil && stored.State.Terminal() && stored.TreeReaped {
			s.runtime.batchValidationJobs.CompareAndDelete(jobID, binding)
		}
	}()
	job, _, err := s.runtime.manager.Start(ctx, request)
	s.service.validationMu.Unlock()
	if err != nil {
		if job.ID == "" {
			s.runtime.batchValidationJobs.CompareAndDelete(jobID, binding)
		}
		return empty, fmt.Errorf("start Batch validation Job: %w", err)
	}
	if err = s.runtime.waitForOperatorCommand(ctx, job.ID); err != nil {
		return empty, err
	}
	stored, err := s.runtime.store.GetCommandRuntimeJob(context.WithoutCancel(ctx), job.ID)
	if err != nil {
		return empty, err
	}
	if stored.ExitCode == nil || stored.StartedAt == nil || stored.CompletedAt == nil || !stored.TreeReaped || !stored.StdinClosed {
		return empty, runner.ErrCommandRuntimeUncertain
	}
	s.execution = batchValidationExecutionReceipt(s.runtime.adapter, resolved.WorkspaceRootSHA256)
	s.execution.ReadinessCheckedAt = s.readinessCheckedAt
	s.execution.JobID = stored.ID
	s.execution.TreeReaped, s.execution.StdinClosed = stored.TreeReaped, stored.StdinClosed
	if docker, ok := s.runtime.sandbox.(*DockerSandboxCommandRuntimeExecutor); ok {
		s.execution.ImageDigest = docker.service.imageDigest
	}
	if sbx, ok := s.runtime.sandbox.(*SBXCommandRuntimeExecutor); ok {
		s.execution.TemplateReference = sbx.backend.TemplateReference()
	}
	result := runner.OnceStartResult{StartedAt: *stored.StartedAt, CompletedAt: *stored.CompletedAt,
		ExitCode: *stored.ExitCode, TreeReaped: stored.TreeReaped, StdinClosed: stored.StdinClosed,
		Stdout: batchValidationJobCapture(stored.StdoutObservedBytes, stored.StdoutSHA256),
		Stderr: batchValidationJobCapture(stored.StderrObservedBytes, stored.StderrSHA256)}
	if stored.State != runner.CommandRuntimeJobCompleted {
		return result, errors.New("batch validation Job did not complete successfully")
	}
	return result, check(ctx)
}

// Cancellation operates only on process-local native proofs. An uncertain Job
// retains its proof and mount for backend recovery instead of losing cleanup
// identity when the foreground request returns.
func (s *BatchDeliveryService) stopBatchValidationJobs(ctx context.Context, planID string) error {
	var services []*CommandRuntimeService
	switch runtime := s.validationRuntime.(type) {
	case *CommandRuntimeService:
		services = []*CommandRuntimeService{runtime}
	case *CommandRuntimeMultiplexer:
		services = runtime.adapters
	}
	var joined error
	for _, runtime := range services {
		runtime.batchValidationJobs.Range(func(key, value any) bool {
			binding := value.(*batchValidationJobBinding)
			if binding.proof.target.ref.PlanID != planID {
				return true
			}
			jobID := key.(string)
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runner.MaxCommandRuntimeCancelGrace+2*time.Second)
			defer cancel()
			if err := runtime.cancelForegroundJob(jobID); err != nil {
				joined = errors.Join(joined, err)
				return true
			}
			job, err := runtime.store.GetCommandRuntimeJob(cleanupCtx, jobID)
			if err != nil || !job.State.Terminal() || !job.TreeReaped || job.OperationDigest != binding.operationDigest || job.SpecFingerprint != binding.specFingerprint {
				joined = errors.Join(joined, errors.New("batch validation resource cleanup is unconfirmed"), err)
				return true
			}
			runtime.batchValidationJobs.CompareAndDelete(jobID, binding)
			return true
		})
	}
	return joined
}

// A newly constructed service has no live proofs for another process's Jobs.
// The durable plan fence prevents new authority; an active Run lease also
// covers a foreign operation still between its final check and Job publish.
func (s *BatchDeliveryService) requireBatchValidationCleanupQuiescent(ctx context.Context, plan domain.BatchDeliveryPlan, children []domain.BatchDeliveryWorkspace) error {
	jobs, err := s.store.ListCommandRuntimeJobs(ctx, runner.CommandRuntimeListFilter{RunID: plan.RunID, ActiveOnly: true, Limit: 500})
	if err != nil {
		return err
	}
	if len(jobs) == 500 {
		return errors.New("batch validation cleanup Job inventory is incomplete")
	}
	roots := make(map[string]bool)
	for _, child := range children {
		if sha, err := runner.CommandRuntimeWorkspaceRootSHA256(child.WorktreeRoot); err == nil {
			roots[sha] = true
		}
	}
	if queue, found, err := s.store.GetBatchDeliveryMergeQueueByPlan(ctx, plan.ID); err != nil {
		return err
	} else if found {
		if sha, err := runner.CommandRuntimeWorkspaceRootSHA256(queue.IntegrationRoot); err == nil {
			roots[sha] = true
		}
	}
	for _, job := range jobs {
		var intent struct {
			Purpose string `json:"purpose"`
		}
		_ = json.Unmarshal([]byte(job.IntentJSON), &intent)
		if roots[job.WorkspaceRootSHA256] || strings.HasPrefix(intent.Purpose, "batch-validation:") {
			return apperror.New(apperror.CodeConflict, "batch validation resource is still owned; recover or cancel its Job before retrying cleanup")
		}
	}
	needsProcess := false
	for _, task := range plan.Spec.Tasks {
		for _, requirement := range task.Validations {
			needsProcess = needsProcess || requirement.Kind != domain.BatchValidationGitDiffCheck
		}
	}
	if !needsProcess {
		return nil
	}
	deadline := time.Now().Add(runner.MaxCommandRuntimeCancelGrace + 2*time.Second)
	for {
		lease, found, err := s.store.GetRunExecutionLease(ctx, plan.RunID)
		if err != nil {
			return err
		}
		if !found || !lease.ActiveAt(time.Now().UTC()) {
			return nil
		}
		if !strings.HasPrefix(lease.OwnerID, "batch-validation-") || !time.Now().Before(deadline) {
			return apperror.New(apperror.CodeConflict, "Run operation still owns its lease; retry Batch cleanup after it ends")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func batchValidationJobCapture(count int64, digest string) runner.OnceOutputCapture {
	empty := sha256.Sum256(nil)
	return runner.OnceOutputCapture{ObservedBytes: int(count), ObservedSHA256: digest, CapturedPrefixSHA256: hex.EncodeToString(empty[:]), Truncated: count > 0}
}

func batchValidationExecutionReceipt(adapter commandruntimeadapter.Identity, rootSHA string) *domain.BatchValidationExecutionReceipt {
	readiness := "ready"
	if adapter.Kind == commandruntimeadapter.KindHostUnsandboxed {
		readiness = "explicit_host_execution"
	}
	return &domain.BatchValidationExecutionReceipt{Backend: adapter.Backend, BackendIdentity: adapter.BackendIdentity,
		BackendGeneration: adapter.Generation, IsolationGrade: string(adapter.IsolationGrade), NetworkPolicy: string(adapter.NetworkPolicy),
		CredentialPolicy: string(adapter.CredentialPolicy), Readiness: readiness, ReadinessCheckedAt: time.Now().UTC(), WorkspaceSHA256: rootSHA}
}
