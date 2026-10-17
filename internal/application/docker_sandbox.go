package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/tools"
)

const (
	DockerSandboxRecoveryLimit = 64

	dockerSandboxLifecycleKeyProtocol = "docker_sandbox_lifecycle_key.v1"
	dockerSandboxRuntimeEpochProtocol = "docker_sandbox_runtime_epoch.v1"
)

// DockerSandboxStore is the single durable boundary used by every product
// adapter. The model, CLI, HTTP, and Desktop layers cannot substitute a
// daemon endpoint, plan, mount, image, or runtime capability.
type DockerSandboxStore interface {
	DockerContainerLifecycleStore
	dockerContainerIOStore

	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetSandboxWorkspace(context.Context, string) (sandbox.WorkspaceBinding, error)
	GetSandboxManifestIntent(context.Context, string) (sandbox.PreparedIntent, error)
	GetSandboxExecutionCandidate(context.Context, string) (
		sandbox.ValidatedExecutionCandidate, error)
	GetDockerContainerPlan(context.Context, string) (sandbox.DockerContainerPlan, error)
	GetDockerObservation(context.Context, string) (sandbox.DockerObservation, error)
	GetRunExecutionProfile(context.Context, string) (
		domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionProfileSnapshot(context.Context, string) (
		domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionPermissionSnapshot(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
	GetApproval(context.Context, string) (approval.Record, error)
	GetRunAgentUsage(context.Context, string) (domain.RunAgentUsage, error)
	GetToolCallUsage(context.Context, string) (toolbudget.Usage, error)

	CreateDockerSandboxAdmission(context.Context, domain.DockerSandboxAdmission) (
		domain.DockerSandboxRecord, bool, error)
	GetDockerSandboxAdmissionByOperation(context.Context, string) (
		domain.DockerSandboxAdmission, bool, error)
	RecordDockerSandboxDenial(context.Context, domain.DockerSandboxDenial) (bool, error)
	GetDockerSandboxDenialByOperation(context.Context, string) (string, string, bool, error)
	GetDockerSandboxRecord(context.Context, string) (domain.DockerSandboxRecord, error)
	BeginDockerSandboxStart(context.Context, domain.DockerSandboxStartIntent) (
		domain.DockerSandboxStartIntent, bool, error)
	GetDockerSandboxStart(context.Context, string) (
		domain.DockerSandboxStartIntent, bool, error)
	GetDockerSandboxAdmissionByLifecycleOperation(context.Context, string) (
		domain.DockerSandboxAdmission, bool, error)
	GetDockerSandboxRecordByLifecycleIntent(context.Context, string) (
		domain.DockerSandboxRecord, bool, error)
	BindDockerSandboxLaunch(context.Context, domain.DockerSandboxLaunch) (
		domain.DockerSandboxRecord, bool, error)
	CompleteDockerSandbox(context.Context, domain.DockerSandboxReceipt) (
		domain.DockerSandboxRecord, bool, error)
	ListRecoverableDockerSandboxes(context.Context, int) ([]domain.DockerSandboxRecord, error)
	RequestDockerSandboxCancellation(context.Context, domain.DockerSandboxCancellation) (
		domain.DockerSandboxCancellation, bool, error)
	GetDockerSandboxCancellation(context.Context, string) (
		domain.DockerSandboxCancellation, bool, error)
}

type DockerSandboxService struct {
	store                   DockerSandboxStore
	readiness               sandbox.ReadinessProbe
	checker                 policy.Checker
	dockerCapabilities      sandbox.DockerRuntimeCapabilities
	permissionCapabilities  domain.ExecutionPermissionRuntimeCapabilities
	runtimeEpochFingerprint string
	lifecycleTransport      sandbox.DockerContainerLifecycleTransport
	stdinTransport          sandbox.DockerContainerStdinTransport
	ioService               *DockerContainerIOService
	stagingRoot             string
	leaseTTL                time.Duration
	now                     func() time.Time
	standardCodeDrydock     *RunWorktreeService
	standardCodeImageDigest string
	standardCodeCapability  string

	standardCodeMaskMu sync.Mutex
	activeMu           sync.Mutex
	active             map[string]context.CancelFunc
	stdinMu            sync.Mutex
	activeStdin        map[string]io.ReadCloser
}

type DockerSandboxServiceOption func(*DockerSandboxService) error

// WithDockerStandardCode binds the fixed Standard Code manifest to one exact,
// pre-existing image digest and to read-only Drydock ownership validation. It
// never pulls an image and exposes no image or Docker setting to a command.
func WithDockerStandardCode(drydockService *RunWorktreeService,
	imageDigest string,
) DockerSandboxServiceOption {
	return func(service *DockerSandboxService) error {
		imageDigest = strings.TrimSpace(imageDigest)
		if drydockService == nil || !sandbox.ValidOCIImageDigest(imageDigest) {
			return errors.New("Standard Code Docker authority is invalid")
		}
		service.standardCodeDrydock = drydockService
		service.standardCodeImageDigest = imageDigest
		service.standardCodeCapability = runmutation.Fingerprint(
			"standard_code_docker_capability.v1", imageDigest,
			fmt.Sprintf("%t", service.dockerCapabilities.Enabled),
			fmt.Sprintf("%t", service.permissionCapabilities.WorkspaceSandboxEnabled))
		return nil
	}
}

func (s *DockerSandboxService) StandardCodeCapabilityGeneration() (string, error) {
	if s == nil || s.standardCodeDrydock == nil ||
		!sandbox.ValidOCIImageDigest(s.standardCodeImageDigest) ||
		s.standardCodeCapability == "" {
		return "", apperror.New(apperror.CodeFailedPrecondition,
			"Standard Code Docker capability is not configured")
	}
	return s.standardCodeCapability, nil
}

// WithDockerSandboxExecution installs the only product execution transports.
// Both must use the same fixed local endpoint. stagingRoot is trusted host
// configuration and is never accepted from a model, HTTP request, or Manifest.
func WithDockerSandboxExecution(lifecycle sandbox.DockerContainerLifecycleTransport,
	ioTransport sandbox.DockerContainerIOTransport, stagingRoot string,
	leaseTTL time.Duration,
) DockerSandboxServiceOption {
	return func(service *DockerSandboxService) error {
		if lifecycle == nil || ioTransport == nil ||
			lifecycle.Endpoint().Validate() != nil || ioTransport.Endpoint().Validate() != nil ||
			lifecycle.Endpoint() != ioTransport.Endpoint() ||
			sandbox.ValidateDockerContainerLifecycleLeaseTTL(leaseTTL) != nil {
			return errors.New("Docker Sandbox execution transports are invalid")
		}
		root, err := canonicalDockerSandboxStagingRoot(stagingRoot)
		if err != nil {
			return err
		}
		service.lifecycleTransport = lifecycle
		service.stdinTransport, _ = ioTransport.(sandbox.DockerContainerStdinTransport)
		service.ioService = NewDockerContainerIOService(service.store, ioTransport)
		service.stagingRoot = root
		service.leaseTTL = leaseTTL
		return nil
	}
}

// NewDockerSandboxService creates a process-local admission service. A fresh
// epoch is generated on every process start and only its digest may enter the
// durable ledger; reopening SQLite can therefore never recreate start power.
func NewDockerSandboxService(store DockerSandboxStore, readiness sandbox.ReadinessProbe,
	checker policy.Checker, dockerCapabilities sandbox.DockerRuntimeCapabilities,
	permissionCapabilities domain.ExecutionPermissionRuntimeCapabilities,
	options ...DockerSandboxServiceOption,
) (*DockerSandboxService, error) {
	if store == nil || readiness == nil || checker == nil ||
		dockerCapabilities.Validate() != nil || permissionCapabilities.Validate() != nil {
		return nil, errors.New("Docker Sandbox service dependencies are invalid")
	}
	epoch := idgen.New("docker-sandbox-runtime-epoch")
	service := &DockerSandboxService{
		store: store, readiness: readiness, checker: checker,
		dockerCapabilities:     dockerCapabilities,
		permissionCapabilities: permissionCapabilities,
		runtimeEpochFingerprint: dockerSandboxRuntimeEpochFingerprint(epoch,
			dockerCapabilities, permissionCapabilities),
		leaseTTL:    sandbox.DefaultDockerContainerLifecycleLeaseTTL,
		now:         func() time.Time { return time.Now().UTC() },
		active:      make(map[string]context.CancelFunc),
		activeStdin: make(map[string]io.ReadCloser),
	}
	for _, option := range options {
		if option == nil || option(service) != nil {
			return nil, errors.New("Docker Sandbox service option is invalid")
		}
	}
	if dockerCapabilities.Enabled && (service.lifecycleTransport == nil ||
		service.ioService == nil || service.stagingRoot == "") {
		return nil, errors.New("enabled Docker Sandbox requires fixed execution transports")
	}
	return service, nil
}

type DockerSandboxReadinessRequest struct {
	PlanID   string
	Manifest sandbox.Manifest
}

type DockerSandboxAdmissionRequest struct {
	PlanID       string
	Manifest     sandbox.Manifest
	OperationKey string
	RequestedBy  string
}

type DockerSandboxAdmissionResult struct {
	Readiness       sandbox.DockerReadiness
	Admission       *domain.DockerSandboxAdmission
	Allowed         bool
	ReasonCode      string
	RemediationCode string
	Replayed        bool
}

func (s *DockerSandboxService) RuntimeCapabilities() (
	sandbox.DockerRuntimeCapabilities, string, error,
) {
	if s == nil || s.dockerCapabilities.Validate() != nil ||
		s.runtimeEpochFingerprint == "" {
		return sandbox.DockerRuntimeCapabilities{}, "", apperror.New(
			apperror.CodeFailedPrecondition, "Docker Sandbox runtime capability is unavailable")
	}
	return s.dockerCapabilities, s.runtimeEpochFingerprint, nil
}

func (s *DockerSandboxService) Readiness(ctx context.Context,
	request DockerSandboxReadinessRequest,
) (sandbox.DockerReadiness, error) {
	if s == nil || s.store == nil || s.readiness == nil {
		return sandbox.DockerReadiness{}, apperror.New(
			apperror.CodeFailedPrecondition, "Docker Sandbox service is unavailable")
	}
	planID := strings.TrimSpace(request.PlanID)
	manifest, err := sandbox.NormalizeManifest(request.Manifest)
	if err != nil || !validDockerSandboxIdentity(planID) {
		return sandbox.DockerReadiness{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"Docker Sandbox readiness request is invalid", err)
	}
	plan, err := s.store.GetDockerContainerPlan(ctx, planID)
	if err != nil {
		return sandbox.DockerReadiness{}, apperror.Normalize(err)
	}
	if err := requireDockerSandboxManifestPlan(manifest, plan); err != nil {
		return sandbox.DockerReadiness{}, err
	}
	value, err := s.readiness.Check(ctx, s.dockerCapabilities, manifest, plan.ImageDigest)
	if err != nil {
		return sandbox.DockerReadiness{}, apperror.Normalize(err)
	}
	if value.Validate() != nil {
		return sandbox.DockerReadiness{}, apperror.New(
			apperror.CodeInternal, "Docker Sandbox readiness result is invalid")
	}
	if value.Ready && s.lifecycleTransport != nil &&
		(value.EndpointClass != s.lifecycleTransport.Endpoint().Class ||
			value.EndpointFingerprint != s.lifecycleTransport.Endpoint().Fingerprint) {
		return sandbox.DockerReadiness{}, apperror.New(apperror.CodeConflict,
			"Docker Sandbox readiness endpoint changed from execution endpoint")
	}
	return value, nil
}

// StandardCodeReadiness probes the fixed image before an approval/plan exists.
// The request carries no endpoint or image selector and the probe never pulls.
func (s *DockerSandboxService) StandardCodeReadiness(ctx context.Context,
	manifest sandbox.Manifest,
) (sandbox.DockerReadiness, error) {
	if s == nil || s.readiness == nil || s.standardCodeDrydock == nil ||
		!sandbox.ValidOCIImageDigest(s.standardCodeImageDigest) {
		return sandbox.DockerReadiness{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Standard Code Docker readiness is not configured")
	}
	if _, ok := sandbox.ParseDockerStandardCodeManifest(manifest); !ok {
		return sandbox.DockerReadiness{}, apperror.New(apperror.CodeInvalidArgument,
			"Standard Code Docker manifest is invalid")
	}
	value, err := s.readiness.Check(ctx, s.dockerCapabilities, manifest,
		s.standardCodeImageDigest)
	if err != nil {
		return sandbox.DockerReadiness{}, apperror.Normalize(err)
	}
	if value.Validate() != nil {
		return sandbox.DockerReadiness{}, apperror.New(apperror.CodeInternal,
			"Standard Code Docker readiness result is invalid")
	}
	if value.Ready && s.lifecycleTransport != nil &&
		(value.EndpointClass != s.lifecycleTransport.Endpoint().Class ||
			value.EndpointFingerprint != s.lifecycleTransport.Endpoint().Fingerprint) {
		return sandbox.DockerReadiness{}, apperror.New(apperror.CodeConflict,
			"Standard Code Docker readiness endpoint changed")
	}
	return value, nil
}

func (s *DockerSandboxService) Admit(ctx context.Context,
	request DockerSandboxAdmissionRequest,
) (DockerSandboxAdmissionResult, error) {
	if s == nil || s.store == nil || s.readiness == nil || s.checker == nil {
		return DockerSandboxAdmissionResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "Docker Sandbox service is unavailable")
	}
	normalized, err := normalizeDockerSandboxAdmissionRequest(request)
	if err != nil {
		return DockerSandboxAdmissionResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Docker Sandbox admission request is invalid", err)
	}
	authority, err := s.loadCurrentDockerSandboxAuthority(ctx, normalized.PlanID,
		normalized.Manifest, normalized.RequestedBy)
	if err != nil {
		return DockerSandboxAdmissionResult{}, err
	}
	operationDigest := runmutation.Fingerprint("docker_sandbox_admission_operation.v1",
		authority.Run.ID, normalized.OperationKey)
	readiness, err := s.readiness.Check(ctx, s.dockerCapabilities,
		authority.Manifest, authority.Plan.ImageDigest)
	if err != nil {
		return DockerSandboxAdmissionResult{}, apperror.Normalize(err)
	}
	if readiness.Validate() != nil {
		return DockerSandboxAdmissionResult{}, apperror.New(
			apperror.CodeInternal, "Docker Sandbox readiness result is invalid")
	}
	if reason, remediation, found, lookupErr :=
		s.store.GetDockerSandboxDenialByOperation(ctx, operationDigest); lookupErr != nil {
		return DockerSandboxAdmissionResult{}, apperror.Normalize(lookupErr)
	} else if found {
		result := deniedDockerSandboxAdmission(readiness, reason, remediation)
		result.Replayed = true
		return result, nil
	}
	deny := func(reason, remediation string) (DockerSandboxAdmissionResult, error) {
		return s.recordDockerSandboxDenial(ctx, authority, operationDigest,
			readiness, reason, remediation)
	}
	if readiness.Ready && (s.lifecycleTransport == nil ||
		readiness.EndpointClass != s.lifecycleTransport.Endpoint().Class ||
		readiness.EndpointFingerprint != s.lifecycleTransport.Endpoint().Fingerprint) {
		return deny(
			domain.DockerSandboxReasonAuthorityChanged,
			domain.DockerSandboxRemediationRetryFreshRequest)
	}
	if !readiness.Ready {
		reason, remediation := dockerSandboxReadinessProductDisposition(readiness)
		return deny(reason, remediation)
	}
	if result, denied := s.evaluateCurrentDockerSandboxGates(authority, readiness); denied {
		return deny(result.ReasonCode, result.RemediationCode)
	}
	now := s.now().UTC()
	if !readiness.ReadyAt(now) {
		return deny(
			domain.DockerSandboxReasonAuthorityChanged,
			domain.DockerSandboxRemediationRetryFreshRequest)
	}
	canonical, err := authority.Manifest.CanonicalJSON()
	if err != nil || redact.String(string(canonical)) != string(canonical) {
		return DockerSandboxAdmissionResult{}, apperror.New(
			apperror.CodePolicyDenied,
			"Docker Sandbox Manifest may not persist secret-bearing command material")
	}
	lifecycleKey := dockerSandboxLifecycleKey(operationDigest)
	lifecycleDigest := runmutation.Fingerprint("sandbox_docker_lifecycle_operation.v1",
		authority.Plan.ID, lifecycleKey)
	if existing, found, lookupErr := s.store.GetDockerSandboxAdmissionByOperation(ctx,
		operationDigest); lookupErr != nil {
		return DockerSandboxAdmissionResult{}, apperror.Normalize(lookupErr)
	} else if found {
		if existing.Validate() != nil || existing.PlanID != authority.Plan.ID ||
			existing.RunID != authority.Run.ID ||
			existing.ManifestJSON != string(canonical) ||
			existing.LifecycleOperationDigest != lifecycleDigest ||
			existing.RequestedBy != normalized.RequestedBy {
			return DockerSandboxAdmissionResult{}, apperror.New(apperror.CodeConflict,
				"Docker Sandbox operation was already used for a different request")
		}
		if existing.RuntimeEpochFingerprint != s.runtimeEpochFingerprint {
			return deniedDockerSandboxAdmission(readiness,
				domain.DockerSandboxReasonAuthorityChanged,
				domain.DockerSandboxRemediationRetryFreshRequest), nil
		}
		return DockerSandboxAdmissionResult{Readiness: readiness,
			Admission: &existing, Allowed: true,
			ReasonCode:      domain.DockerSandboxReasonReady,
			RemediationCode: domain.DockerSandboxRemediationNone,
			Replayed:        true}, nil
	}
	wallSeconds, toolCallsRemaining, ok := dockerSandboxRemainingBudgets(authority)
	if !ok {
		return deny(
			domain.DockerSandboxReasonBudgetExhausted,
			domain.DockerSandboxRemediationRestoreBudget)
	}
	requestFingerprint := runmutation.Fingerprint("docker_sandbox_admission_request.v1",
		authority.Run.ID, authority.Plan.ID, authority.Plan.PlanFingerprint,
		authority.Plan.SpecFingerprint, string(canonical), readiness.ReadinessFingerprint,
		s.runtimeEpochFingerprint, authority.Profile.ID,
		fmt.Sprint(authority.Profile.Revision), authority.Permission.ID,
		fmt.Sprint(authority.Permission.Revision), authority.Approval.ID,
		fmt.Sprint(authority.Approval.Version), normalized.RequestedBy)
	admission := domain.DockerSandboxAdmission{
		ID:                 idgen.New("docker-sandbox-admission"),
		ProtocolVersion:    domain.DockerSandboxAdmissionProtocolVersion,
		OperationKeyDigest: operationDigest, RequestFingerprint: requestFingerprint,
		LifecycleOperationDigest: lifecycleDigest,
		RunID:                    authority.Run.ID, MissionID: authority.Mission.ID,
		WorkspaceID: authority.Workspace.ID, PlanID: authority.Plan.ID,
		CandidateID:   authority.Candidate.Candidate.ID,
		PreparationID: authority.Intent.Preparation.ID,
		ManifestJSON:  string(canonical), ManifestFingerprint: authority.Plan.ManifestFingerprint,
		PlanFingerprint:         authority.Plan.PlanFingerprint,
		SpecFingerprint:         authority.Plan.SpecFingerprint,
		AuthorityFingerprint:    authority.Plan.AuthorityFingerprint,
		ReadinessFingerprint:    readiness.ReadinessFingerprint,
		ReadinessExpiresAt:      readiness.ExpiresAt,
		RuntimeEpochFingerprint: s.runtimeEpochFingerprint,
		ProfileSnapshotID:       authority.Profile.ID, ProfileRevision: authority.Profile.Revision,
		PermissionSnapshotID: authority.Permission.ID,
		PermissionRevision:   authority.Permission.Revision,
		PermissionMode:       authority.Permission.Mode,
		ApprovalID:           authority.Approval.ID, ApprovalVersion: authority.Approval.Version,
		PolicyFingerprint:  authority.Plan.PolicyFingerprint,
		NetworkMode:        authority.Plan.NetworkMode,
		NetworkTargetCount: authority.Plan.NetworkTargetCount,
		CPUQuotaMillis:     int(authority.Plan.NanoCPUs / 1_000_000),
		MemoryBytes:        authority.Plan.MemoryBytes, PIDs: authority.Plan.PIDs,
		DiskBytes:           min(authority.Plan.MaxOutputBytes, domain.MaxDockerSandboxDiskBytes),
		WallClockSeconds:    wallSeconds,
		LogBytes:            min(authority.Plan.MaxOutputBytes, domain.MaxDockerSandboxLogBytes),
		LogLines:            domain.MaxDockerSandboxLogLines,
		ToolCallsRemaining:  toolCallsRemaining,
		Decision:            domain.DockerSandboxAdmissionAuthorized,
		ReasonCode:          domain.DockerSandboxReasonReady,
		RemediationCode:     domain.DockerSandboxRemediationNone,
		ProductEntryEnabled: true, ExecutionAuthorized: true,
		ArtifactCommitAuthorized: true,
		RequestedBy:              normalized.RequestedBy, CreatedAt: now,
	}
	admission.AdmissionFingerprint = domain.DockerSandboxAdmissionFingerprint(admission)
	if err := admission.Validate(); err != nil {
		return DockerSandboxAdmissionResult{}, apperror.Wrap(
			apperror.CodeInternal, "Docker Sandbox admission assembly failed", err)
	}
	stored, replayed, err := s.store.CreateDockerSandboxAdmission(ctx, admission)
	if err != nil {
		return DockerSandboxAdmissionResult{}, apperror.Normalize(err)
	}
	stored.Replayed = replayed
	return DockerSandboxAdmissionResult{Readiness: readiness,
		Admission: &stored.Admission, Allowed: true,
		ReasonCode:      domain.DockerSandboxReasonReady,
		RemediationCode: domain.DockerSandboxRemediationNone,
		Replayed:        replayed}, nil
}

func (s *DockerSandboxService) Get(ctx context.Context,
	admissionID string,
) (domain.DockerSandboxRecord, error) {
	if s == nil || s.store == nil || !validDockerSandboxIdentity(strings.TrimSpace(admissionID)) {
		return domain.DockerSandboxRecord{}, apperror.New(
			apperror.CodeInvalidArgument, "Docker Sandbox admission id is invalid")
	}
	value, err := s.store.GetDockerSandboxRecord(ctx, strings.TrimSpace(admissionID))
	return value, apperror.Normalize(err)
}

type dockerSandboxAuthority struct {
	Run         domain.Run
	Mission     domain.Mission
	Workspace   sandbox.WorkspaceBinding
	Manifest    sandbox.Manifest
	Intent      sandbox.PreparedIntent
	Candidate   sandbox.ValidatedExecutionCandidate
	Plan        sandbox.DockerContainerPlan
	Observation sandbox.DockerObservation
	Profile     domain.RunExecutionProfileSnapshot
	Permission  domain.RunExecutionPermissionSnapshot
	Approval    approval.Record
	AgentUsage  domain.RunAgentUsage
	ToolUsage   toolbudget.Usage
	RootPath    string
}

func (s *DockerSandboxService) loadCurrentDockerSandboxAuthority(ctx context.Context,
	planID string, manifest sandbox.Manifest, requestedBy string,
) (dockerSandboxAuthority, error) {
	plan, err := s.store.GetDockerContainerPlan(ctx, planID)
	if err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if requestedBy != plan.RequestedBy {
		return dockerSandboxAuthority{}, apperror.New(apperror.CodeConflict,
			"Docker Sandbox requester does not own the exact compiled plan")
	}
	if isWorkerCommandDockerRequester(plan.RequestedBy) {
		proof := workerCommandRuntimeFromContext(ctx)
		if proof == nil || proof.scope.RunID != plan.RunID || workerCommandDockerRequester(proof) != plan.RequestedBy || proof.check(ctx, proof.nativeScope, proof.binding.rootPath) != nil {
			return dockerSandboxAuthority{}, apperror.New(apperror.CodePolicyDenied, "worker Docker execution requires its live child authority")
		}
	}
	if isBatchValidationDockerRequester(plan.RequestedBy) {
		proof := batchValidationRuntimeFromContext(ctx)
		if proof == nil || proof.target.runID != plan.RunID || batchValidationDockerRequester(proof.target) != plan.RequestedBy || proof.check(ctx) != nil {
			return dockerSandboxAuthority{}, apperror.New(apperror.CodePolicyDenied, "batch Docker execution requires live target authority")
		}
	}
	if isGitBisectDockerRequester(plan.RequestedBy) {
		proof := gitBisectRuntimeFromContext(ctx)
		if proof == nil || proof.runID != plan.RunID || gitBisectDockerRequester(proof) != plan.RequestedBy || proof.check == nil || proof.check(ctx) != nil {
			return dockerSandboxAuthority{}, apperror.New(apperror.CodePolicyDenied, "Git bisect Docker execution requires live source authority")
		}
	}
	if err := requireDockerSandboxManifestPlan(manifest, plan); err != nil {
		return dockerSandboxAuthority{}, err
	}
	value := dockerSandboxAuthority{Plan: plan, Manifest: manifest}
	if value.Run, err = s.store.GetRun(ctx, plan.RunID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if value.Mission, err = s.store.GetMission(ctx, plan.MissionID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if value.Workspace, err = s.resolveDockerSandboxWorkspace(ctx, plan.RunID,
		plan.WorkspaceID, manifest, true); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if value.Intent, err = s.store.GetSandboxManifestIntent(ctx, plan.PreparationID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if value.Candidate, err = s.store.GetSandboxExecutionCandidate(ctx, plan.CandidateID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if value.Observation, err = s.store.GetDockerObservation(ctx, plan.ObservationID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if value.Profile, err = s.store.GetRunExecutionProfile(ctx, plan.RunID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if value.Permission, err = s.store.GetRunExecutionPermission(ctx, plan.RunID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	approvalID := value.Candidate.Candidate.ApprovalID
	if approvalID == "" {
		return dockerSandboxAuthority{}, apperror.New(apperror.CodeFailedPrecondition,
			"Docker Sandbox always requires an exact per-call approval")
	}
	if value.Approval, err = s.store.GetApproval(ctx, approvalID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if value.AgentUsage, err = s.store.GetRunAgentUsage(ctx, plan.RunID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if value.ToolUsage, err = s.store.GetToolCallUsage(ctx, plan.RunID); err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	if err := validateDockerSandboxAuthorityBindings(value); err != nil {
		return dockerSandboxAuthority{}, err
	}
	currentLease, leaseFound, err := s.store.GetRunExecutionLease(ctx, plan.RunID)
	if err != nil {
		return dockerSandboxAuthority{}, apperror.Normalize(err)
	}
	now := s.now().UTC()
	if value.Candidate.Candidate.LeaseQuiescent {
		if leaseFound && currentLease.ActiveAt(now) {
			return dockerSandboxAuthority{}, apperror.New(apperror.CodeConflict,
				"Docker Sandbox quiescent candidate gained a Run lease")
		}
	} else if !leaseFound || !currentLease.ActiveAt(now) ||
		!executionCandidateMatchesRunLease(value.Candidate.Candidate, currentLease) {
		return dockerSandboxAuthority{}, apperror.New(apperror.CodeConflict,
			"Docker Sandbox Run lease authority changed")
	}
	if binding, standardCode := sandbox.ParseDockerStandardCodeManifest(manifest); standardCode {
		if err := s.validateDockerStandardCodeAuthority(value, binding); err != nil {
			return dockerSandboxAuthority{}, err
		}
	}
	if value.RootPath, err = validateSandboxWorkspaceBinding(value.Workspace); err != nil {
		return dockerSandboxAuthority{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"Docker Sandbox workspace binding is invalid", err)
	}
	return value, nil
}

func (s *DockerSandboxService) resolveDockerSandboxWorkspace(ctx context.Context,
	runID, workspaceID string, manifest sandbox.Manifest, requireUnchanged bool,
) (sandbox.WorkspaceBinding, error) {
	if binding, standardCode := sandbox.ParseDockerStandardCodeManifest(manifest); standardCode {
		if s.standardCodeDrydock == nil || binding.RunID != runID ||
			binding.WorkspaceID != workspaceID {
			return sandbox.WorkspaceBinding{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"Standard Code Docker requires its exact Drydock authority")
		}
		return s.standardCodeDrydock.ResolveDrydockExecutionBinding(ctx, binding,
			requireUnchanged)
	}
	return s.store.GetSandboxWorkspace(ctx, workspaceID)
}

func (s *DockerSandboxService) validateDockerStandardCodeAuthority(
	authority dockerSandboxAuthority, binding sandbox.DockerStandardCodeRunnerBinding,
) error {
	if s.standardCodeDrydock == nil ||
		authority.Plan.ImageDigest != s.standardCodeImageDigest ||
		binding.RunID != authority.Run.ID || binding.MissionID != authority.Mission.ID ||
		binding.SessionID != authority.Run.SessionID ||
		binding.WorkspaceID != authority.Mission.WorkspaceID ||
		binding.ProfileSnapshotID != authority.Profile.ID ||
		binding.ProfileRevision != authority.Profile.Revision ||
		binding.PermissionSnapshotID != authority.Permission.ID ||
		binding.PermissionRevision != authority.Permission.Revision ||
		binding.CapabilityGeneration != s.standardCodeCapability ||
		authority.Profile.Profile != domain.RunExecutionProfileDocker ||
		!authority.Permission.Mode.IsApprovalMode() ||
		!s.permissionCapabilities.WorkspaceSandboxEnabled ||
		!s.permissionCapabilities.AllowsSnapshot(authority.Permission) {
		return apperror.New(apperror.CodeConflict,
			"Standard Code Docker authority changed")
	}
	return nil
}

func (s *DockerSandboxService) evaluateCurrentDockerSandboxGates(
	authority dockerSandboxAuthority, readiness sandbox.DockerReadiness,
) (DockerSandboxAdmissionResult, bool) {
	deny := func(reason, remediation string) (DockerSandboxAdmissionResult, bool) {
		return deniedDockerSandboxAdmission(readiness, reason, remediation), true
	}
	if authority.Run.Terminal() {
		return deny(domain.DockerSandboxReasonAuthorityChanged,
			domain.DockerSandboxRemediationRetryFreshRequest)
	}
	if authority.Profile.Profile != domain.RunExecutionProfileDocker {
		return deny(domain.DockerSandboxReasonPermissionDenied,
			domain.DockerSandboxRemediationSelectDockerProfile)
	}
	if !s.permissionCapabilities.AllowsSnapshot(authority.Permission) {
		return deny(domain.DockerSandboxReasonPermissionDenied,
			domain.DockerSandboxRemediationRetryFreshRequest)
	}
	if authority.Approval.Status != approval.StatusApproved ||
		authority.Approval.Mode != "per_call" {
		return deny(domain.DockerSandboxReasonApprovalRequired,
			domain.DockerSandboxRemediationApproveExactRequest)
	}
	decision := hardenSandboxDecision(s.checker.CheckToolCall(tools.Call{
		Name: sandboxApprovalToolName,
		Args: map[string]string{"intent": mustDockerSandboxCanonical(authority.Manifest)},
	}), authority.Manifest)
	policyFingerprint := runmutation.Fingerprint("sandbox_policy_decision.v1",
		authority.Plan.AuthorizationFingerprint, fmt.Sprintf("%t", decision.Allowed),
		fmt.Sprintf("%t", decision.NeedsApproval), decision.Risk, decision.Reason)
	if !decision.Allowed {
		return deny(domain.DockerSandboxReasonPolicyDenied,
			domain.DockerSandboxRemediationReviewPolicy)
	}
	if policyFingerprint != authority.Plan.PolicyFingerprint {
		return deny(domain.DockerSandboxReasonAuthorityChanged,
			domain.DockerSandboxRemediationRetryFreshRequest)
	}
	if _, _, ok := dockerSandboxRemainingBudgets(authority); !ok {
		return deny(domain.DockerSandboxReasonBudgetExhausted,
			domain.DockerSandboxRemediationRestoreBudget)
	}
	return DockerSandboxAdmissionResult{}, false
}

func validateDockerSandboxAuthorityBindings(value dockerSandboxAuthority) error {
	candidate := value.Candidate.Candidate
	preparation := value.Intent.Preparation
	validation := value.Intent.Validation
	plan := value.Plan
	if value.Run.Validate() != nil || value.Mission.Validate() != nil ||
		candidate.Validate() != nil || preparation.Validate() != nil ||
		validation.Validate() != nil || plan.Validate() != nil ||
		value.Observation.Validate() != nil ||
		value.Profile.Validate() != nil || value.Permission.Validate() != nil ||
		value.Approval.Validate() != nil || value.AgentUsage.Validate() != nil ||
		!value.ToolUsage.Tracked || value.ToolUsage.RunID != plan.RunID ||
		value.Run.MissionID != value.Mission.ID ||
		value.Mission.WorkspaceID != value.Workspace.ID ||
		plan.RunID != value.Run.ID || plan.MissionID != value.Mission.ID ||
		plan.WorkspaceID != value.Workspace.ID ||
		candidate.ID != plan.CandidateID || candidate.PreparationID != plan.PreparationID ||
		candidate.RunID != plan.RunID || candidate.MissionID != plan.MissionID ||
		candidate.WorkspaceID != plan.WorkspaceID ||
		candidate.ManifestFingerprint != plan.ManifestFingerprint ||
		candidate.AuthorizationFingerprint != plan.AuthorizationFingerprint ||
		candidate.PolicyFingerprint != plan.PolicyFingerprint ||
		candidate.ApprovalID != value.Approval.ID ||
		value.Observation.ID != plan.ObservationID ||
		value.Observation.RunID != plan.RunID ||
		value.Observation.MissionID != plan.MissionID ||
		value.Observation.WorkspaceID != plan.WorkspaceID ||
		value.Observation.CandidateID != plan.CandidateID ||
		value.Observation.PreparationID != plan.PreparationID ||
		value.Observation.ManifestFingerprint != plan.ManifestFingerprint ||
		value.Observation.AuthorizationFingerprint != plan.AuthorizationFingerprint ||
		value.Observation.PolicyFingerprint != plan.PolicyFingerprint ||
		value.Observation.Report.ObservationFingerprint != plan.ObservationFingerprint ||
		value.Observation.Report.ImageDigest != plan.ImageDigest ||
		candidate.ApprovalStatus != sandbox.ApprovalApproved ||
		preparation.ID != plan.PreparationID || preparation.RunID != plan.RunID ||
		preparation.MissionID != plan.MissionID || preparation.WorkspaceID != plan.WorkspaceID ||
		preparation.Backend != sandbox.BackendDocker ||
		preparation.ManifestFingerprint != plan.ManifestFingerprint ||
		preparation.AuthorizationFingerprint != plan.AuthorizationFingerprint ||
		preparation.NetworkMode != "disabled" || preparation.AllowedTargetCount != 0 ||
		preparation.EnvironmentCount != 0 || preparation.SecretReferenceCount != 0 ||
		!validation.PolicyAllowed || !validation.NeedsApproval ||
		validation.PolicyFingerprint != plan.PolicyFingerprint ||
		value.Profile.RunID != plan.RunID || value.Profile.MissionID != plan.MissionID ||
		value.Permission.RunID != plan.RunID || value.Permission.MissionID != plan.MissionID ||
		value.Approval.ProposalID != preparation.ID ||
		value.Approval.RunID != plan.RunID || value.Approval.SessionID != value.Run.SessionID ||
		value.Approval.WorkspaceID != plan.WorkspaceID ||
		value.Approval.ToolName != sandboxApprovalToolName ||
		value.Approval.ActionClass != sandboxApprovalActionClass ||
		value.Approval.Mode != "per_call" ||
		value.Approval.RequestFingerprint != plan.AuthorizationFingerprint ||
		plan.NetworkMode != "disabled" || plan.NetworkTargetCount != 0 ||
		plan.EnvironmentCount != 0 || plan.SecretReferenceCount != 0 ||
		!plan.SimulationOnly || plan.BackendEnabled || plan.ExecutionAuthorized ||
		plan.ArtifactCommitAuthorized {
		return apperror.New(apperror.CodeConflict,
			"Docker Sandbox authority does not exactly bind the compiled request")
	}
	return nil
}

func requireDockerSandboxManifestPlan(manifest sandbox.Manifest,
	plan sandbox.DockerContainerPlan,
) error {
	normalized, err := sandbox.NormalizeManifest(manifest)
	if err != nil || plan.Validate() != nil || normalized.Backend != sandbox.BackendDocker ||
		normalized.Network.Mode != "disabled" ||
		len(normalized.Network.AllowedTargets) != 0 || len(normalized.Environment) != 0 ||
		normalized.SecretReferenceCount() != 0 {
		return apperror.New(apperror.CodePolicyDenied,
			"Docker Sandbox product admission only supports environment-free network-none manifests")
	}
	fingerprint, err := normalized.Fingerprint()
	if err != nil || fingerprint != plan.ManifestFingerprint ||
		int64(normalized.Resources.CPUQuotaMillis)*1_000_000 != plan.NanoCPUs ||
		normalized.Resources.MemoryBytes != plan.MemoryBytes ||
		normalized.Resources.PIDs != plan.PIDs ||
		normalized.Resources.MaxOutputBytes != plan.MaxOutputBytes ||
		normalized.TimeoutSeconds != plan.TimeoutSeconds ||
		normalized.Cancellation.GracePeriodMillis != plan.GracePeriodMillis {
		return apperror.New(apperror.CodeConflict,
			"Docker Sandbox Manifest does not match the exact compiled plan")
	}
	return nil
}

func dockerSandboxRemainingBudgets(value dockerSandboxAuthority) (int, int64, bool) {
	remainingSeconds := int64(value.Plan.TimeoutSeconds)
	if value.Run.Budget.TimeoutSeconds > 0 {
		remainingMillis := value.Run.Budget.TimeoutSeconds*1000 -
			value.AgentUsage.TotalExecutionMillis
		if remainingMillis < 1000 {
			return 0, 0, false
		}
		remainingSeconds = min(remainingSeconds, remainingMillis/1000)
	}
	remainingTools := int64(domain.MaxDockerSandboxToolCalls)
	if value.ToolUsage.Limit > 0 {
		if value.ToolUsage.Remaining < 1 {
			return 0, 0, false
		}
		remainingTools = min(remainingTools, value.ToolUsage.Remaining)
	}
	if remainingSeconds < 1 || remainingSeconds > int64(sandbox.MaxTimeoutSeconds) ||
		remainingTools < 1 {
		return 0, 0, false
	}
	return int(remainingSeconds), remainingTools, true
}

func normalizeDockerSandboxAdmissionRequest(request DockerSandboxAdmissionRequest) (
	DockerSandboxAdmissionRequest, error,
) {
	request.PlanID = strings.TrimSpace(request.PlanID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	manifest, err := sandbox.NormalizeManifest(request.Manifest)
	if err != nil {
		return DockerSandboxAdmissionRequest{}, err
	}
	request.Manifest = manifest
	if !validDockerSandboxIdentity(request.PlanID) ||
		!validDockerSandboxIdentity(request.RequestedBy) ||
		!validDockerSandboxOperationKey(request.OperationKey) {
		return DockerSandboxAdmissionRequest{}, errors.New(
			"Docker Sandbox plan, operation, and requester are required")
	}
	return request, nil
}

func validDockerSandboxIdentity(value string) bool {
	if !domain.ValidAgentID(value) || strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validDockerSandboxOperationKey(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 256 ||
		strings.TrimSpace(value) != value || strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return redact.String(value) == value
}

func deniedDockerSandboxAdmission(readiness sandbox.DockerReadiness, reason,
	remediation string,
) DockerSandboxAdmissionResult {
	return DockerSandboxAdmissionResult{Readiness: readiness, Allowed: false,
		ReasonCode: reason, RemediationCode: remediation}
}

func dockerSandboxReadinessProductDisposition(readiness sandbox.DockerReadiness) (
	string, string,
) {
	switch readiness.ReasonCode {
	case sandbox.DockerReadinessReasonFeatureDisabled:
		return domain.DockerSandboxReasonFeatureDisabled,
			domain.DockerSandboxRemediationEnableFeature
	case sandbox.DockerReadinessReasonDaemonUnreachable:
		return domain.DockerSandboxReasonDaemonUnreachable,
			domain.DockerSandboxRemediationStartDocker
	case sandbox.DockerReadinessReasonAPIUnsupported:
		return domain.DockerSandboxReasonAPIUnsupported,
			domain.DockerSandboxRemediationUpdateDocker
	case sandbox.DockerReadinessReasonPlatformUnsupported:
		return domain.DockerSandboxReasonPlatformUnsupported,
			domain.DockerSandboxRemediationUseLinuxContainers
	case sandbox.DockerReadinessReasonPIDsLimitUnavailable:
		return domain.DockerSandboxReasonResourceUnavailable,
			domain.DockerSandboxRemediationEnablePIDsLimit
	case sandbox.DockerReadinessReasonResourceCapacityInsufficient:
		return domain.DockerSandboxReasonResourceUnavailable,
			domain.DockerSandboxRemediationReduceResources
	case sandbox.DockerReadinessReasonImageUnavailable:
		return domain.DockerSandboxReasonResourceUnavailable,
			domain.DockerSandboxRemediationProvideImage
	case sandbox.DockerReadinessReasonManagedEgressUnavailable:
		return domain.DockerSandboxReasonManagedEgressUnavailable,
			domain.DockerSandboxRemediationDisableNetwork
	default:
		return domain.DockerSandboxReasonPolicyDenied,
			domain.DockerSandboxRemediationCorrectRequest
	}
}

func (s *DockerSandboxService) recordDockerSandboxDenial(ctx context.Context,
	authority dockerSandboxAuthority, operationDigest string,
	readiness sandbox.DockerReadiness, reason, remediation string,
) (DockerSandboxAdmissionResult, error) {
	denial := domain.DockerSandboxDenial{
		ProtocolVersion:    domain.DockerSandboxDenialProtocolVersion,
		OperationKeyDigest: operationDigest, RunID: authority.Run.ID,
		MissionID: authority.Mission.ID, WorkspaceID: authority.Workspace.ID,
		PlanID: authority.Plan.ID, RequestedBy: authority.Plan.RequestedBy,
		ReasonCode: reason, RemediationCode: remediation,
		NetworkMode: authority.Plan.NetworkMode, CreatedAt: s.now().UTC(),
	}
	denial.DenialFingerprint = domain.DockerSandboxDenialFingerprint(denial)
	if denial.Validate() != nil {
		return DockerSandboxAdmissionResult{}, apperror.New(apperror.CodeInternal,
			"Docker Sandbox denial assembly failed")
	}
	inserted, err := s.store.RecordDockerSandboxDenial(ctx, denial)
	if err != nil {
		return DockerSandboxAdmissionResult{}, apperror.Normalize(err)
	}
	result := deniedDockerSandboxAdmission(readiness, reason, remediation)
	result.Replayed = !inserted
	return result, nil
}

func mustDockerSandboxCanonical(manifest sandbox.Manifest) string {
	value, _ := manifest.CanonicalJSON()
	return string(value)
}

func dockerSandboxLifecycleKey(operationDigest string) string {
	return runmutation.Fingerprint(dockerSandboxLifecycleKeyProtocol, operationDigest)
}

func dockerSandboxRuntimeEpochFingerprint(epoch string,
	dockerCapabilities sandbox.DockerRuntimeCapabilities,
	permissionCapabilities domain.ExecutionPermissionRuntimeCapabilities,
) string {
	return runmutation.Fingerprint(dockerSandboxRuntimeEpochProtocol, epoch,
		fmt.Sprint(dockerCapabilities.Enabled),
		fmt.Sprint(dockerCapabilities.ManagedEgressEnabled),
		fmt.Sprint(permissionCapabilities.OperatorApprovalEnabled),
		fmt.Sprint(permissionCapabilities.DangerFullAccessEnabled))
}

func canonicalDockerSandboxStagingRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, 0) {
		return "", errors.New("Docker Sandbox staging root is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("Docker Sandbox staging root is not a directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}
