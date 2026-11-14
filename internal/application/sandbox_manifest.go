package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/tools"
)

const (
	sandboxApprovalToolName    = "sandbox.manifest"
	sandboxApprovalActionClass = "sandbox_execute"
)

type SandboxManifestStore interface {
	GetRun(ctx context.Context, id string) (domain.Run, error)
	GetMission(ctx context.Context, id string) (domain.Mission, error)
	GetRunMode(ctx context.Context, runID string) (domain.RunModeSnapshot, error)
	GetSandboxWorkspace(ctx context.Context, id string) (sandbox.WorkspaceBinding, error)
	GetApproval(ctx context.Context, id string) (approval.Record, error)
	GetSandboxManifestOperation(ctx context.Context, keyDigest string) (sandbox.Operation, bool, error)
	CreateSandboxManifestIntent(ctx context.Context, preparation sandbox.Preparation,
		validation sandbox.Validation, operation sandbox.Operation) (sandbox.PreparedIntent, bool, error)
	GetSandboxManifestIntent(ctx context.Context, id string) (sandbox.PreparedIntent, error)
	ListSandboxManifestIntents(ctx context.Context, runID string, limit int) ([]sandbox.PreparedIntent, error)
	EnsureApproval(ctx context.Context, proposal approval.Proposal) (approval.Record, error)
	DecideApproval(ctx context.Context, request approval.DecisionRequest) (approval.DecisionResult, error)
	GetRunAgentUsage(ctx context.Context, runID string) (domain.RunAgentUsage, error)
	GetToolCallUsage(ctx context.Context, runID string) (toolbudget.Usage, error)
	GetRunExecutionLease(ctx context.Context, runID string) (domain.RunExecutionLease, bool, error)
	GetSandboxExecutionCandidateOperation(ctx context.Context, keyDigest string) (sandbox.CandidateOperation, bool, error)
	CreateSandboxExecutionCandidate(ctx context.Context, candidate sandbox.ExecutionCandidate,
		operation sandbox.CandidateOperation) (sandbox.ValidatedExecutionCandidate, bool, error)
	GetSandboxExecutionCandidate(ctx context.Context, id string) (sandbox.ValidatedExecutionCandidate, error)
	ListSandboxExecutionCandidates(ctx context.Context, runID string, limit int) ([]sandbox.ValidatedExecutionCandidate, error)
	GetRunArtifact(ctx context.Context, id string) (artifact.Blob, error)
	GetSandboxExecutionOperation(ctx context.Context, keyDigest string) (sandbox.ExecutionOperation, bool, error)
	CreateSandboxDisabledExecution(ctx context.Context, execution sandbox.DisabledExecution,
		inputs []sandbox.InputArtifactBinding, operation sandbox.ExecutionOperation,
		ownerID string, ttl time.Duration) (sandbox.Lifecycle, bool, error)
	GetSandboxDisabledExecution(ctx context.Context, id string) (sandbox.Lifecycle, error)
	ListSandboxDisabledExecutions(ctx context.Context, runID string, limit int) ([]sandbox.Lifecycle, error)
	AcquireSandboxExecutionLease(ctx context.Context, executionID, ownerID, leaseID string,
		ttl time.Duration) (sandbox.LeaseAcquisition, error)
	ReleaseSandboxExecutionLease(ctx context.Context, expected sandbox.ExecutionLease) (sandbox.ExecutionLease, bool, error)
	GetSandboxExecutionLease(ctx context.Context, executionID string) (sandbox.ExecutionLease, bool, error)
	GetSandboxCancellationOperation(ctx context.Context, keyDigest string) (sandbox.CancellationOperation, bool, error)
	CreateSandboxCancellation(ctx context.Context, request sandbox.CancellationRequest,
		operation sandbox.CancellationOperation) (sandbox.CancellationRequest, bool, error)
	GetSandboxCleanupOperation(ctx context.Context, keyDigest string) (sandbox.CleanupOperation, bool, error)
	CompleteSandboxCleanup(ctx context.Context, result sandbox.CleanupResult,
		operation sandbox.CleanupOperation, expectedLease sandbox.ExecutionLease) (sandbox.CleanupResult, bool, error)
	GetSandboxPreflightOperation(ctx context.Context, keyDigest string) (sandbox.PreflightOperation, bool, error)
	CreateSandboxDisabledPreflight(ctx context.Context, preflight sandbox.DisabledPreflight,
		operation sandbox.PreflightOperation) (sandbox.DisabledPreflight, bool, error)
	GetSandboxDisabledPreflight(ctx context.Context, id string) (sandbox.DisabledPreflight, error)
	ListSandboxDisabledPreflights(ctx context.Context, runID string, limit int) ([]sandbox.DisabledPreflight, error)
	GetSandboxBackendEvidenceOperation(ctx context.Context, keyDigest string) (sandbox.BackendEvidenceOperation, bool, error)
	CreateSandboxBackendEvidence(ctx context.Context, evidence sandbox.BackendEvidence,
		operation sandbox.BackendEvidenceOperation) (sandbox.BackendEvidence, bool, error)
	GetSandboxBackendEvidence(ctx context.Context, id string) (sandbox.BackendEvidence, error)
	ListSandboxBackendEvidence(ctx context.Context, runID string, limit int) ([]sandbox.BackendEvidence, error)
	GetSandboxOutputSimulationOperation(ctx context.Context, keyDigest string) (sandbox.OutputSimulationOperation, bool, error)
	CreateSandboxOutputSimulation(ctx context.Context, simulation sandbox.OutputSimulation,
		operation sandbox.OutputSimulationOperation) (sandbox.OutputSimulation, bool, error)
	GetSandboxOutputSimulation(ctx context.Context, id string) (sandbox.OutputSimulation, error)
	ListSandboxOutputSimulations(ctx context.Context, runID string, limit int) ([]sandbox.OutputSimulation, error)
	GetDockerObservationOperation(ctx context.Context, keyDigest string) (sandbox.DockerObservationOperation, bool, error)
	CreateDockerObservation(ctx context.Context, observation sandbox.DockerObservation,
		operation sandbox.DockerObservationOperation) (sandbox.DockerObservation, bool, error)
	GetDockerObservation(ctx context.Context, id string) (sandbox.DockerObservation, error)
	ListDockerObservations(ctx context.Context, runID string, limit int) ([]sandbox.DockerObservation, error)
	GetDockerContainerPlanOperation(ctx context.Context, keyDigest string) (sandbox.DockerContainerPlanOperation, bool, error)
	CreateDockerContainerPlan(ctx context.Context, plan sandbox.DockerContainerPlan,
		operation sandbox.DockerContainerPlanOperation) (sandbox.DockerContainerPlan, bool, error)
	GetDockerContainerPlan(ctx context.Context, id string) (sandbox.DockerContainerPlan, error)
	ListDockerContainerPlans(ctx context.Context, runID string, limit int) ([]sandbox.DockerContainerPlan, error)
	GetDockerContainerRehearsalOperation(ctx context.Context, keyDigest string) (sandbox.DockerContainerRehearsalOperation, bool, error)
	CreateDockerContainerRehearsal(ctx context.Context, rehearsal sandbox.DockerContainerRehearsal,
		operation sandbox.DockerContainerRehearsalOperation) (sandbox.DockerContainerRehearsal, bool, error)
	GetDockerContainerRehearsal(ctx context.Context, id string) (sandbox.DockerContainerRehearsal, error)
	ListDockerContainerRehearsals(ctx context.Context, runID string, limit int) ([]sandbox.DockerContainerRehearsal, error)
	BeginDockerContainerRehearsalAttempt(ctx context.Context,
		intent sandbox.DockerContainerAttemptIntent,
		requirement sandbox.DockerHostInputRequirement, ownerID string,
		ttl time.Duration,
		handoffRequirements ...sandbox.DockerHostInputHandoffRequirement) (sandbox.DockerContainerAttemptAcquisition, error)
	AcquireDockerContainerRehearsalAttempt(ctx context.Context, attemptID, requestedBy,
		ownerID string, ttl time.Duration) (sandbox.DockerContainerAttemptAcquisition, error)
	RecordDockerContainerAttemptStage(ctx context.Context,
		stage sandbox.DockerContainerAttemptStage,
		expected sandbox.DockerContainerAttemptLease) (sandbox.DockerContainerRehearsalAttempt, bool, error)
	RecordDockerContainerAttemptCleanup(ctx context.Context,
		cleanup sandbox.DockerContainerAttemptCleanup,
		expected sandbox.DockerContainerAttemptLease) (sandbox.DockerContainerRehearsalAttempt, bool, error)
	FailDockerContainerRehearsalAttempt(ctx context.Context,
		failure sandbox.DockerContainerAttemptFailure,
		expected sandbox.DockerContainerAttemptLease) (sandbox.DockerContainerRehearsalAttempt, error)
	CompleteDockerContainerRehearsalAttempt(ctx context.Context,
		completion sandbox.DockerContainerAttemptCompletion,
		rehearsal sandbox.DockerContainerRehearsal,
		operation sandbox.DockerContainerRehearsalOperation,
		expected sandbox.DockerContainerAttemptLease) (sandbox.DockerContainerRehearsal, bool, error)
	GetDockerContainerRehearsalAttempt(ctx context.Context,
		id string) (sandbox.DockerContainerRehearsalAttempt, error)
	ListDockerContainerRehearsalAttempts(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerContainerRehearsalAttempt, error)
	PrepareDockerHostInputStagingIntent(ctx context.Context,
		intent sandbox.DockerHostInputStagingIntent,
		expected sandbox.DockerContainerAttemptLease) (sandbox.DockerHostInputStagingRecord, bool, error)
	RecordDockerHostInputStaging(ctx context.Context,
		value sandbox.DockerHostInputStaging,
		expected sandbox.DockerContainerAttemptLease) (sandbox.DockerHostInputStagingRecord, bool, error)
	GetDockerHostInputStaging(ctx context.Context,
		id string) (sandbox.DockerHostInputStagingRecord, error)
	GetDockerHostInputStagingByAttempt(ctx context.Context,
		attemptID string) (sandbox.DockerHostInputStagingRecord, bool, error)
	GetDockerHostInputStagingByOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerHostInputStagingRecord, bool, error)
	GetDockerHostInputStagingByPlan(ctx context.Context,
		planID string) (sandbox.DockerHostInputStagingRecord, bool, error)
	ListDockerHostInputStagings(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerHostInputStagingRecord, error)
	GetDockerHostInputRequirement(ctx context.Context,
		attemptID string) (sandbox.DockerHostInputRequirement, bool, error)
	GetDockerHostInputRequirementByOperation(ctx context.Context,
		operationKeyDigest string) (sandbox.DockerHostInputRequirement, bool, error)
	GetDockerHostInputHandoffRequirement(ctx context.Context,
		attemptID string) (sandbox.DockerHostInputHandoffRequirement, bool, error)
	GetDockerHostInputHandoffRequirementByOperation(ctx context.Context,
		operationKeyDigest string) (sandbox.DockerHostInputHandoffRequirement, bool, error)
	PrepareDockerHostInputHandoffIntent(ctx context.Context,
		intent sandbox.DockerHostInputHandoffIntent,
		expected sandbox.DockerContainerAttemptLease) (sandbox.DockerHostInputHandoffRecord, bool, error)
	RecordDockerHostInputHandoff(ctx context.Context,
		value sandbox.DockerHostInputHandoff,
		expected sandbox.DockerContainerAttemptLease) (sandbox.DockerHostInputHandoffRecord, bool, error)
	GetDockerHostInputHandoff(ctx context.Context,
		id string) (sandbox.DockerHostInputHandoffRecord, error)
	GetDockerHostInputHandoffByAttempt(ctx context.Context,
		attemptID string) (sandbox.DockerHostInputHandoffRecord, bool, error)
	GetDockerHostInputHandoffByOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerHostInputHandoffRecord, bool, error)
	ListDockerHostInputHandoffs(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerHostInputHandoffRecord, error)
	GetDockerRuntimeInputProjectionOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerRuntimeInputProjectionOperation, bool, error)
	CreateDockerRuntimeInputProjectionPlan(ctx context.Context,
		plan sandbox.DockerRuntimeInputProjectionPlan,
		operation sandbox.DockerRuntimeInputProjectionOperation) (sandbox.DockerRuntimeInputProjectionPlan, bool, error)
	GetDockerRuntimeInputProjectionPlan(ctx context.Context,
		id string) (sandbox.DockerRuntimeInputProjectionPlan, error)
	GetDockerRuntimeInputProjectionPlanByHandoff(ctx context.Context,
		handoffID string) (sandbox.DockerRuntimeInputProjectionPlan, bool, error)
	ListDockerRuntimeInputProjectionPlans(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerRuntimeInputProjectionPlan, error)
	BeginDockerRuntimeInputApplication(ctx context.Context,
		intent sandbox.DockerRuntimeInputApplicationIntent, ownerID string,
		ttl time.Duration) (sandbox.DockerRuntimeInputApplicationAcquisition, error)
	AcquireDockerRuntimeInputApplication(ctx context.Context, intentID, requestedBy,
		ownerID string, ttl time.Duration) (sandbox.DockerRuntimeInputApplicationAcquisition, error)
	CompleteDockerRuntimeInputApplication(ctx context.Context,
		result sandbox.DockerRuntimeInputApplicationResult,
		expected sandbox.DockerRuntimeInputApplicationLease) (sandbox.DockerRuntimeInputApplicationRecord, bool, error)
	RecordDockerRuntimeInputApplicationFailure(ctx context.Context, intentID string,
		expected sandbox.DockerRuntimeInputApplicationLease, code string,
		createdAt time.Time) (sandbox.DockerRuntimeInputApplicationRecord, error)
	GetDockerRuntimeInputApplication(ctx context.Context,
		id string) (sandbox.DockerRuntimeInputApplicationRecord, error)
	GetDockerRuntimeInputApplicationByProjection(ctx context.Context,
		projectionID string) (sandbox.DockerRuntimeInputApplicationRecord, bool, error)
	GetDockerRuntimeInputApplicationByOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerRuntimeInputApplicationRecord, bool, error)
	ListDockerRuntimeInputApplications(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerRuntimeInputApplicationRecord, error)
	RecordDockerRuntimeInputResourceInspection(ctx context.Context,
		value sandbox.DockerRuntimeInputResourceInspection) (sandbox.DockerRuntimeInputResourceInspection, bool, error)
	GetDockerRuntimeInputResourceInspection(ctx context.Context,
		id string) (sandbox.DockerRuntimeInputResourceInspection, error)
	GetDockerRuntimeInputResourceInspectionByOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerRuntimeInputResourceInspection, bool, error)
	ListDockerRuntimeInputResourceInspections(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerRuntimeInputResourceInspection, error)
	BeginDockerRuntimeInputResourceCleanup(ctx context.Context,
		intent sandbox.DockerRuntimeInputResourceCleanupIntent, ownerID string,
		ttl time.Duration) (sandbox.DockerRuntimeInputResourceCleanupAcquisition, error)
	AcquireDockerRuntimeInputResourceCleanup(ctx context.Context, intentID, requestedBy,
		ownerID string, ttl time.Duration) (sandbox.DockerRuntimeInputResourceCleanupAcquisition, error)
	CompleteDockerRuntimeInputResourceCleanup(ctx context.Context,
		result sandbox.DockerRuntimeInputResourceCleanupResult,
		expected sandbox.DockerRuntimeInputResourceCleanupLease) (sandbox.DockerRuntimeInputResourceCleanupRecord, bool, error)
	RecordDockerRuntimeInputResourceCleanupFailure(ctx context.Context, intentID string,
		expected sandbox.DockerRuntimeInputResourceCleanupLease, code string,
		createdAt time.Time) (sandbox.DockerRuntimeInputResourceCleanupRecord, error)
	GetDockerRuntimeInputResourceCleanup(ctx context.Context,
		id string) (sandbox.DockerRuntimeInputResourceCleanupRecord, error)
	GetDockerRuntimeInputResourceCleanupByOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerRuntimeInputResourceCleanupRecord, bool, error)
	ListDockerRuntimeInputResourceCleanups(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerRuntimeInputResourceCleanupRecord, error)
	GetDockerStartGateReviewOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerStartGateReviewOperation, bool, error)
	CreateDockerStartGateReview(ctx context.Context, review sandbox.DockerStartGateReview,
		operation sandbox.DockerStartGateReviewOperation) (sandbox.DockerStartGateReview, bool, error)
	GetDockerStartGateReview(ctx context.Context, id string) (sandbox.DockerStartGateReview, error)
	GetDockerStartGateReviewByCleanup(ctx context.Context,
		cleanupIntentID string) (sandbox.DockerStartGateReview, bool, error)
	ListDockerStartGateReviews(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerStartGateReview, error)
	GetDockerProductionEvidenceOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerProductionEvidenceOperation, bool, error)
	CreateDockerProductionEvidence(ctx context.Context, value sandbox.DockerProductionEvidence,
		operation sandbox.DockerProductionEvidenceOperation) (sandbox.DockerProductionEvidence, bool, error)
	GetDockerProductionEvidence(ctx context.Context,
		id string) (sandbox.DockerProductionEvidence, error)
	ListDockerProductionEvidence(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerProductionEvidence, error)
	BeginDockerProductionEvidenceAttempt(ctx context.Context,
		attempt sandbox.DockerProductionEvidenceAttempt, ownerID string,
		ttl time.Duration) (sandbox.DockerProductionEvidenceAttemptAcquisition, error)
	AcquireDockerProductionEvidenceAttempt(ctx context.Context, attemptID, requestedBy,
		ownerID string, ttl time.Duration) (sandbox.DockerProductionEvidenceAttemptAcquisition, error)
	RecordDockerProductionEvidenceReconciliation(ctx context.Context,
		value sandbox.DockerProductionEvidenceReconciliation,
		expected sandbox.DockerProductionEvidenceAttemptLease) (sandbox.DockerProductionEvidenceAttemptRecord, bool, error)
	PrepareDockerProductionEvidenceHarnessIntent(ctx context.Context,
		value sandbox.DockerProductionEvidenceHarnessIntent,
		expected sandbox.DockerProductionEvidenceAttemptLease) (sandbox.DockerProductionEvidenceAttemptRecord, bool, error)
	RecordDockerProductionEvidenceHarnessReconciliation(ctx context.Context,
		value sandbox.DockerProductionEvidenceHarnessReconciliation,
		expected sandbox.DockerProductionEvidenceAttemptLease) (sandbox.DockerProductionEvidenceAttemptRecord, bool, error)
	CompleteDockerProductionEvidenceAttempt(ctx context.Context,
		value sandbox.DockerProductionEvidence, operation sandbox.DockerProductionEvidenceOperation,
		result sandbox.DockerProductionEvidenceAttemptResult,
		expected sandbox.DockerProductionEvidenceAttemptLease) (sandbox.DockerProductionEvidenceAttemptRecord,
		sandbox.DockerProductionEvidence, bool, error)
	CompleteDockerProductionEvidenceHarnessAttempt(ctx context.Context,
		value sandbox.DockerProductionEvidence, operation sandbox.DockerProductionEvidenceOperation,
		result sandbox.DockerProductionEvidenceHarnessResult,
		expected sandbox.DockerProductionEvidenceAttemptLease) (sandbox.DockerProductionEvidenceAttemptRecord,
		sandbox.DockerProductionEvidence, bool, error)
	RecordDockerProductionEvidenceAttemptFailure(ctx context.Context, attemptID string,
		expected sandbox.DockerProductionEvidenceAttemptLease, code string,
		createdAt time.Time) (sandbox.DockerProductionEvidenceAttemptRecord, error)
	GetDockerProductionEvidenceAttempt(ctx context.Context,
		id string) (sandbox.DockerProductionEvidenceAttemptRecord, error)
	GetDockerProductionEvidenceAttemptByOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerProductionEvidenceAttemptRecord, bool, error)
	GetDockerProductionEvidenceAttemptByEvidence(ctx context.Context,
		evidenceID string) (sandbox.DockerProductionEvidenceAttemptRecord, bool, error)
	ListDockerProductionEvidenceAttempts(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerProductionEvidenceAttemptRecord, error)
	GetDockerProductionEvidenceReviewOperation(ctx context.Context,
		keyDigest string) (sandbox.DockerProductionEvidenceReviewOperation, bool, error)
	CreateDockerProductionEvidenceReview(ctx context.Context,
		value sandbox.DockerProductionEvidenceReview,
		operation sandbox.DockerProductionEvidenceReviewOperation) (
		sandbox.DockerProductionEvidenceReview, bool, error)
	GetDockerProductionEvidenceReview(ctx context.Context,
		id string) (sandbox.DockerProductionEvidenceReview, error)
	GetDockerProductionEvidenceReviewByEvidence(ctx context.Context,
		evidenceID string) (sandbox.DockerProductionEvidenceReview, bool, error)
	ListDockerProductionEvidenceReviews(ctx context.Context, runID string,
		limit int) ([]sandbox.DockerProductionEvidenceReview, error)
}

type SandboxManifestService struct {
	store                SandboxManifestStore
	checker              policy.Checker
	inspector            sandbox.BackendInspector
	evidenceClient       sandbox.BackendEvidenceClient
	outputHarness        sandbox.OutputSimulationHarness
	dockerObserver       sandbox.DockerProductionObserver
	dockerWriter         sandbox.DockerContainerTransactionHarness
	dockerWriteTransport sandbox.DockerContainerWriteTransport
	hostInputStager      sandbox.DockerHostInputStager
	hostInputHandoff     sandbox.DockerHostInputHandoffTransport
	runtimeInputApply    sandbox.DockerRuntimeInputApplicationTransport
	runtimeResourceRead  sandbox.DockerRuntimeInputResourceInspector
	runtimeResourceClean sandbox.DockerRuntimeInputResourceCleanupTransport
	productionEvidence   sandbox.DockerProductionEvidenceCollector
	standardCodeDrydock  *RunWorktreeService
}

// WithStandardCodeDrydock projects only the exact, fully bound Standard Code
// manifest onto its product-owned Drydock root. Other sandbox manifests retain
// the registered source Workspace binding.
func (s *SandboxManifestService) WithStandardCodeDrydock(
	drydockService *RunWorktreeService,
) *SandboxManifestService {
	if s != nil {
		s.standardCodeDrydock = drydockService
	}
	return s
}

type PrepareSandboxManifestRequest struct {
	RunID        string
	Manifest     sandbox.Manifest
	ApprovalID   string
	OperationKey string
	RequestedBy  string
}

func NewSandboxManifestService(store SandboxManifestStore,
	checker policy.Checker,
) *SandboxManifestService {
	return &SandboxManifestService{
		store: store, checker: checker, inspector: sandbox.NewDisabledBackendInspector(),
		evidenceClient: sandbox.NewSimulationBackendClient(),
		outputHarness:  sandbox.NewInMemoryOutputHarness(),
		dockerObserver: sandbox.NewReadOnlyDockerProductionObserver(
			sandbox.NewLocalDockerReadOnlyTransport()),
		dockerWriter:         sandbox.NewInMemoryDockerWriteTransaction(),
		dockerWriteTransport: sandbox.NewUnavailableDockerContainerWriteTransport(),
		hostInputStager:      sandbox.NewUnavailableDockerHostInputStager(),
		hostInputHandoff:     sandbox.NewUnavailableDockerHostInputHandoffTransport(),
		runtimeInputApply:    sandbox.NewUnavailableDockerRuntimeInputApplicationTransport(),
		runtimeResourceRead:  sandbox.NewUnavailableDockerRuntimeInputResourceTransport(),
		runtimeResourceClean: sandbox.NewUnavailableDockerRuntimeInputResourceTransport(),
		productionEvidence:   sandbox.NewLocalDockerProductionEvidenceCollector(),
	}
}

func (s *SandboxManifestService) WithDockerProductionEvidenceCollector(
	collector sandbox.DockerProductionEvidenceCollector,
) *SandboxManifestService {
	if s != nil && collector != nil {
		s.productionEvidence = collector
	}
	return s
}

func (s *SandboxManifestService) WithDockerRuntimeInputResourceInspector(
	inspector sandbox.DockerRuntimeInputResourceInspector,
) *SandboxManifestService {
	if s != nil && inspector != nil {
		s.runtimeResourceRead = inspector
	}
	return s
}

func (s *SandboxManifestService) WithDockerRuntimeInputResourceCleanupTransport(
	transport sandbox.DockerRuntimeInputResourceCleanupTransport,
) *SandboxManifestService {
	if s != nil && transport != nil {
		s.runtimeResourceClean = transport
	}
	return s
}

func (s *SandboxManifestService) WithDockerRuntimeInputApplicationTransport(
	transport sandbox.DockerRuntimeInputApplicationTransport,
) *SandboxManifestService {
	if s != nil && transport != nil {
		s.runtimeInputApply = transport
	}
	return s
}

func (s *SandboxManifestService) WithDockerHostInputHandoffTransport(
	transport sandbox.DockerHostInputHandoffTransport,
) *SandboxManifestService {
	if s != nil && transport != nil {
		s.hostInputHandoff = transport
	}
	return s
}

func (s *SandboxManifestService) WithDockerContainerWriteTransport(
	transport sandbox.DockerContainerWriteTransport,
) *SandboxManifestService {
	if s != nil && transport != nil {
		s.dockerWriteTransport = transport
	}
	return s
}

func (s *SandboxManifestService) WithDockerHostInputStager(
	stager sandbox.DockerHostInputStager,
) *SandboxManifestService {
	if s != nil && stager != nil {
		s.hostInputStager = stager
	}
	return s
}

func (s *SandboxManifestService) WithDockerContainerTransactionHarness(
	harness sandbox.DockerContainerTransactionHarness,
) *SandboxManifestService {
	if s != nil && harness != nil {
		s.dockerWriter = harness
	}
	return s
}

func (s *SandboxManifestService) WithDockerProductionObserver(
	observer sandbox.DockerProductionObserver,
) *SandboxManifestService {
	if s != nil && observer != nil {
		s.dockerObserver = observer
	}
	return s
}

func (s *SandboxManifestService) Prepare(ctx context.Context,
	request PrepareSandboxManifestRequest,
) (sandbox.PreparedIntent, error) {
	if s == nil || s.store == nil || s.checker == nil {
		return sandbox.PreparedIntent{}, apperror.New(apperror.CodeFailedPrecondition,
			"sandbox manifest store and policy checker are required")
	}
	normalizedRequest, err := normalizeSandboxManifestRequest(request)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"sandbox manifest request is invalid", err)
	}
	manifest, err := sandbox.NewNoopRunner().ValidateManifest(ctx, normalizedRequest.Manifest)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"sandbox manifest Noop validation failed", err)
	}
	manifestFingerprint, err := manifest.Fingerprint()
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"sandbox manifest fingerprint failed", err)
	}
	operationKeyDigest := runmutation.Fingerprint("sandbox_manifest_operation.v1",
		normalizedRequest.RunID, normalizedRequest.OperationKey)
	if existing, found, err := s.store.GetSandboxManifestOperation(ctx,
		operationKeyDigest); err != nil {
		return sandbox.PreparedIntent{}, apperror.Normalize(err)
	} else if found {
		return s.replay(ctx, normalizedRequest, manifestFingerprint, existing)
	}
	run, err := s.store.GetRun(ctx, normalizedRequest.RunID)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Normalize(err)
	}
	if run.Terminal() {
		return sandbox.PreparedIntent{}, apperror.New(apperror.CodeFailedPrecondition,
			"sandbox manifest cannot be prepared for a terminal Run")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Normalize(err)
	}
	if mission.WorkspaceID == "" || mode.Scope.WorkspaceID != mission.WorkspaceID {
		return sandbox.PreparedIntent{}, apperror.New(apperror.CodeFailedPrecondition,
			"sandbox manifest requires an exact non-empty current Run workspace scope")
	}
	workspace, err := s.resolveSandboxWorkspace(ctx, run.ID, mission.WorkspaceID,
		manifest, true)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Normalize(err)
	}
	rootPath, err := validateSandboxWorkspaceBinding(workspace)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"sandbox workspace binding is invalid", err)
	}
	normalizedScope, err := normalizeSandboxMissionScope(mode.Scope)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"current Run network scope is invalid", err)
	}
	if err := requireSandboxScopeSubset(manifest.Network, normalizedScope); err != nil {
		return sandbox.PreparedIntent{}, apperror.Wrap(apperror.CodePolicyDenied,
			"sandbox manifest attempted to widen the current Run network scope", err)
	}
	canonicalScope, err := json.Marshal(normalizedScope)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Wrap(apperror.CodeInternal,
			"encode sandbox scope snapshot", err)
	}
	workspaceFingerprint := runmutation.Fingerprint("sandbox_workspace_binding.v1",
		workspace.ID, rootPath)
	scopeFingerprint := runmutation.Fingerprint("sandbox_scope_binding.v1",
		string(canonicalScope))
	authorizationFingerprint := runmutation.Fingerprint("sandbox_authorization.v1",
		run.ID, mission.ID, workspace.ID, manifestFingerprint, workspaceFingerprint,
		scopeFingerprint)
	canonicalManifest, err := manifest.CanonicalJSON()
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Wrap(apperror.CodeInternal,
			"encode normalized sandbox manifest", err)
	}
	decision := s.checker.CheckToolCall(tools.Call{
		Name: sandboxApprovalToolName,
		Args: map[string]string{"intent": string(canonicalManifest)},
	})
	decision = hardenSandboxDecision(decision, manifest)
	policyFingerprint := runmutation.Fingerprint("sandbox_policy_decision.v1",
		authorizationFingerprint, fmt.Sprintf("%t", decision.Allowed),
		fmt.Sprintf("%t", decision.NeedsApproval), decision.Risk, decision.Reason)
	approvalID, approvalStatus, err := s.bindSandboxApproval(ctx, run, mission,
		authorizationFingerprint, decision, normalizedRequest.ApprovalID)
	if err != nil {
		return sandbox.PreparedIntent{}, err
	}
	now := time.Now().UTC()
	preparation := sandbox.Preparation{
		ID: idgen.New("sandbox-manifest"), RunID: run.ID, MissionID: mission.ID,
		WorkspaceID: workspace.ID, CancellationID: idgen.New("sandbox-cancel"),
		ProtocolVersion: sandbox.ManifestProtocolVersion, Backend: manifest.Backend,
		ManifestFingerprint:      manifestFingerprint,
		AuthorizationFingerprint: authorizationFingerprint,
		WorkspaceFingerprint:     workspaceFingerprint, ScopeFingerprint: scopeFingerprint,
		CommandArgumentCount: len(manifest.Command.Arguments), MountCount: len(manifest.Mounts),
		WritableMountCount: manifest.WritableMountCount(),
		EnvironmentCount:   len(manifest.Environment), SecretReferenceCount: manifest.SecretReferenceCount(),
		NetworkMode: manifest.Network.Mode, AllowedTargetCount: len(manifest.Network.AllowedTargets),
		InputArtifactCount: len(manifest.InputArtifactIDs), OutputCount: manifest.OutputCount(),
		TimeoutSeconds:    manifest.TimeoutSeconds,
		GracePeriodMillis: manifest.Cancellation.GracePeriodMillis,
		CPUQuotaMillis:    manifest.Resources.CPUQuotaMillis,
		MemoryBytes:       manifest.Resources.MemoryBytes, PIDs: manifest.Resources.PIDs,
		MaxOutputBytes: manifest.Resources.MaxOutputBytes,
		RequestedBy:    normalizedRequest.RequestedBy, PreparedAt: now,
	}
	validation := sandbox.Validation{
		PreparationID: preparation.ID, RunID: run.ID,
		ProtocolVersion: sandbox.ValidationProtocolVersion,
		PolicyAllowed:   decision.Allowed, NeedsApproval: decision.NeedsApproval,
		Risk: decision.Risk, PolicyFingerprint: policyFingerprint,
		ApprovalID: approvalID, ApprovalStatus: approvalStatus, ValidatorName: "noop",
		BackendEnabled: false, ExecutionAuthorized: false, ValidatedAt: now,
	}
	operation := sandbox.Operation{
		KeyDigest:     operationKeyDigest,
		PreparationID: preparation.ID, RunID: run.ID,
		RequestedBy: normalizedRequest.RequestedBy, CreatedAt: now,
	}
	operation.RequestFingerprint = sandbox.IntentRequestFingerprint(preparation, validation)
	stored, replayed, err := s.store.CreateSandboxManifestIntent(ctx, preparation,
		validation, operation)
	stored.Replayed = replayed
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Normalize(err)
	}
	if !stored.Validation.PolicyAllowed {
		return stored, apperror.New(apperror.CodePolicyDenied,
			"sandbox manifest was recorded but permanently denied by policy")
	}
	return stored, nil
}

func (s *SandboxManifestService) resolveSandboxWorkspace(ctx context.Context,
	runID, workspaceID string, manifest sandbox.Manifest, requireUnchanged bool,
) (sandbox.WorkspaceBinding, error) {
	if binding, standardCode := sandbox.ParseDockerStandardCodeManifest(manifest); standardCode {
		if s.standardCodeDrydock == nil || binding.RunID != runID ||
			binding.WorkspaceID != workspaceID {
			return sandbox.WorkspaceBinding{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"Standard Code manifest requires its exact Drydock authority")
		}
		return s.standardCodeDrydock.ResolveDrydockExecutionBinding(ctx, binding,
			requireUnchanged)
	}
	return s.store.GetSandboxWorkspace(ctx, workspaceID)
}

func (s *SandboxManifestService) replay(ctx context.Context,
	request PrepareSandboxManifestRequest, manifestFingerprint string,
	operation sandbox.Operation,
) (sandbox.PreparedIntent, error) {
	if operation.RunID != request.RunID || operation.RequestedBy != request.RequestedBy {
		return sandbox.PreparedIntent{}, apperror.New(apperror.CodeConflict,
			"sandbox manifest operation key was already used for different intent")
	}
	stored, err := s.store.GetSandboxManifestIntent(ctx, operation.PreparationID)
	if err != nil {
		return sandbox.PreparedIntent{}, apperror.Normalize(err)
	}
	if operation.PreparationID != stored.Preparation.ID ||
		operation.RequestFingerprint != sandbox.IntentRequestFingerprint(stored.Preparation,
			stored.Validation) || operation.RunID != stored.Preparation.RunID ||
		operation.RequestedBy != stored.Preparation.RequestedBy {
		return sandbox.PreparedIntent{}, apperror.New(apperror.CodeInternal,
			"stored sandbox manifest replay binding is invalid")
	}
	if stored.Preparation.ManifestFingerprint != manifestFingerprint ||
		stored.Validation.ApprovalID != request.ApprovalID {
		return sandbox.PreparedIntent{}, apperror.New(apperror.CodeConflict,
			"sandbox manifest operation key was already used for different intent")
	}
	stored.Replayed = true
	if !stored.Validation.PolicyAllowed {
		return stored, apperror.New(apperror.CodePolicyDenied,
			"sandbox manifest was recorded but permanently denied by policy")
	}
	return stored, nil
}

func (s *SandboxManifestService) Get(ctx context.Context,
	id string,
) (sandbox.PreparedIntent, error) {
	if s == nil || s.store == nil {
		return sandbox.PreparedIntent{}, apperror.New(apperror.CodeFailedPrecondition,
			"sandbox manifest store is required")
	}
	value, err := s.store.GetSandboxManifestIntent(ctx, strings.TrimSpace(id))
	return value, apperror.Normalize(err)
}

func (s *SandboxManifestService) List(ctx context.Context, runID string,
	limit int,
) ([]sandbox.PreparedIntent, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"sandbox manifest store is required")
	}
	values, err := s.store.ListSandboxManifestIntents(ctx, strings.TrimSpace(runID), limit)
	return values, apperror.Normalize(err)
}

func normalizeSandboxManifestRequest(request PrepareSandboxManifestRequest,
) (PrepareSandboxManifestRequest, error) {
	originalOperationKey := request.OperationKey
	request.RunID = strings.TrimSpace(request.RunID)
	request.ApprovalID = strings.TrimSpace(request.ApprovalID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = strings.TrimSpace(redact.String(request.RequestedBy))
	if request.RequestedBy == "" {
		request.RequestedBy = "cli_operator"
	}
	if !domain.ValidAgentID(request.RunID) || !domain.ValidAgentID(request.RequestedBy) ||
		strings.ContainsRune(request.RunID, 0) || strings.ContainsRune(request.RequestedBy, 0) {
		return PrepareSandboxManifestRequest{}, errors.New("bounded Run and operator identities are required")
	}
	if request.ApprovalID != "" && (!domain.ValidAgentID(request.ApprovalID) ||
		strings.ContainsRune(request.ApprovalID, 0)) {
		return PrepareSandboxManifestRequest{}, errors.New("sandbox approval identity is invalid")
	}
	if request.OperationKey != originalOperationKey || !utf8.ValidString(request.OperationKey) {
		return PrepareSandboxManifestRequest{}, errors.New("sandbox operation key must be normalized UTF-8")
	}
	if _, err := domain.NormalizeAgentOperationKey(request.OperationKey); err != nil {
		return PrepareSandboxManifestRequest{}, err
	}
	for _, current := range request.OperationKey {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return PrepareSandboxManifestRequest{}, errors.New("sandbox operation key cannot contain whitespace or control characters")
		}
	}
	return request, nil
}

func validateSandboxWorkspaceBinding(workspace sandbox.WorkspaceBinding) (string, error) {
	if !domain.ValidAgentID(workspace.ID) || strings.ContainsRune(workspace.ID, 0) {
		return "", errors.New("workspace identity is invalid")
	}
	if workspace.RootPath == "" || !utf8.ValidString(workspace.RootPath) || strings.ContainsRune(workspace.RootPath, 0) {
		return "", errors.New("workspace root must be normalized UTF-8")
	}
	rootPath, err := rootfs.EvalExactSymlinks(workspace.RootPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(rootPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("workspace root is not a directory")
	}
	if err := rootfs.CheckExternalDirectory(rootPath); err != nil {
		return "", err
	}
	return rootPath, nil
}

func normalizeSandboxMissionScope(scope domain.Scope) (domain.Scope, error) {
	normalized := domain.Scope{WorkspaceID: strings.TrimSpace(scope.WorkspaceID),
		NetworkMode: strings.TrimSpace(scope.NetworkMode)}
	if normalized.WorkspaceID != scope.WorkspaceID || normalized.NetworkMode != scope.NetworkMode {
		return domain.Scope{}, errors.New("mission scope must be normalized before sandbox binding")
	}
	if normalized.WorkspaceID == "" {
		return domain.Scope{}, errors.New("mission scope workspace is required")
	}
	if normalized.NetworkMode != "disabled" && normalized.NetworkMode != "allowlist" {
		return domain.Scope{}, fmt.Errorf("unsupported mission network mode %q", normalized.NetworkMode)
	}
	if len(scope.AllowedTargets) > sandbox.MaxNetworkTargets {
		return domain.Scope{}, errors.New("mission network allowlist exceeds sandbox protocol bounds")
	}
	for _, target := range scope.AllowedTargets {
		value, err := sandbox.NormalizeAllowedTarget(target)
		if err != nil {
			return domain.Scope{}, err
		}
		normalized.AllowedTargets = append(normalized.AllowedTargets, value)
	}
	sort.Strings(normalized.AllowedTargets)
	for index := 1; index < len(normalized.AllowedTargets); index++ {
		if normalized.AllowedTargets[index] == normalized.AllowedTargets[index-1] {
			return domain.Scope{}, errors.New("mission network allowlist contains duplicate targets")
		}
	}
	if normalized.NetworkMode == "disabled" && len(normalized.AllowedTargets) != 0 {
		return domain.Scope{}, errors.New("disabled Mission network scope cannot contain targets")
	}
	if normalized.NetworkMode == "allowlist" && len(normalized.AllowedTargets) == 0 {
		return domain.Scope{}, errors.New("allowlist Mission network scope requires targets")
	}
	return normalized, nil
}

func requireSandboxScopeSubset(request sandbox.NetworkScope, mission domain.Scope) error {
	if request.Mode == "disabled" {
		return nil
	}
	if request.Mode != "allowlist" || mission.NetworkMode != "allowlist" {
		return errors.New("mission scope does not permit sandbox network access")
	}
	allowed := make(map[string]struct{}, len(mission.AllowedTargets))
	for _, target := range mission.AllowedTargets {
		allowed[target] = struct{}{}
	}
	for _, target := range request.AllowedTargets {
		if _, ok := allowed[target]; !ok {
			return fmt.Errorf("target %q is outside the mission allowlist", target)
		}
	}
	return nil
}

func hardenSandboxDecision(decision policy.Decision, manifest sandbox.Manifest) policy.Decision {
	decision.Reason = strings.TrimSpace(decision.Reason)
	decision.Risk = strings.ToLower(strings.TrimSpace(decision.Risk))
	if decision.Reason == "" {
		decision.Reason = "sandbox policy checker returned no reason"
	}
	if !decision.Allowed {
		decision.NeedsApproval = false
		if riskRank(decision.Risk) < riskRank("high") {
			decision.Risk = "high"
		}
		return decision
	}
	requiresApproval := decision.NeedsApproval || riskRank(decision.Risk) >= riskRank("medium") ||
		manifest.Backend != sandbox.BackendNoop ||
		manifest.Network.Mode == "allowlist" || manifest.HasWritableMount() ||
		manifest.SecretReferenceCount() > 0
	if requiresApproval {
		decision.NeedsApproval = true
		if riskRank(decision.Risk) < riskRank("medium") {
			decision.Risk = "medium"
		}
		decision.Reason += "; sandbox backend, write, network, or secret capability requires approval"
	} else if riskRank(decision.Risk) == 0 {
		decision.Risk = "low"
	}
	return decision
}

func riskRank(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	default:
		return 0
	}
}

func (s *SandboxManifestService) bindSandboxApproval(ctx context.Context, run domain.Run,
	mission domain.Mission, authorizationFingerprint string, decision policy.Decision,
	requestedApprovalID string,
) (string, sandbox.ApprovalStatus, error) {
	if !decision.Allowed {
		if requestedApprovalID != "" {
			return "", "", apperror.New(apperror.CodePolicyDenied,
				"policy-denied sandbox manifest cannot be overridden by approval")
		}
		return "", sandbox.ApprovalNotApplicable, nil
	}
	if !decision.NeedsApproval {
		if requestedApprovalID != "" {
			return "", "", apperror.New(apperror.CodeInvalidArgument,
				"sandbox manifest does not require an approval binding")
		}
		return "", sandbox.ApprovalNotRequired, nil
	}
	if requestedApprovalID == "" {
		return "", sandbox.ApprovalRequired, nil
	}
	record, err := s.store.GetApproval(ctx, requestedApprovalID)
	if err != nil {
		return "", "", apperror.Normalize(err)
	}
	if err := record.Validate(); err != nil {
		return "", "", apperror.Wrap(apperror.CodeInternal,
			"stored sandbox approval is invalid", err)
	}
	if record.RunID != run.ID || record.SessionID != run.SessionID ||
		record.WorkspaceID != mission.WorkspaceID || record.ToolName != sandboxApprovalToolName ||
		record.ActionClass != sandboxApprovalActionClass ||
		record.RequestFingerprint != authorizationFingerprint {
		return "", "", apperror.New(apperror.CodeConflict,
			"sandbox approval does not match the exact Run, workspace, action, and manifest intent")
	}
	switch record.Status {
	case approval.StatusPending:
		return record.ID, sandbox.ApprovalPending, nil
	case approval.StatusApproved:
		return record.ID, sandbox.ApprovalApproved, nil
	case approval.StatusDenied:
		return record.ID, sandbox.ApprovalDenied, nil
	default:
		return "", "", apperror.New(apperror.CodeInternal,
			"stored sandbox approval status is unsupported")
	}
}
