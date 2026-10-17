package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/operationreceipt"
	"cyberagent-workbench/internal/operatoraction"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/threadtranscript"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/verification"
	"cyberagent-workbench/internal/webevidence"
)

const (
	Version               = "api.v1"
	OpenAPIPath           = "/api/v1/openapi.json"
	DefaultListenAddress  = "127.0.0.1:8765"
	MaxRequestTargetBytes = 8 * 1024
	MaxQueryBytes         = 4 * 1024
	MaxResponseBytes      = 8 * 1024 * 1024
	MinAccessTokenBytes   = 32
	MaxAccessTokenBytes   = 512
)

type Store interface {
	application.RunWebAccessStore
	application.ControlledCommandHistoryStore
	application.HostCommandHistoryStore
	SchemaVersion(ctx context.Context) (int, error)
	GetThread(context.Context, string) (domain.Thread, error)
	GetThreadRunRecovery(context.Context, string) (domain.ThreadRunRecovery, bool, error)
	RecoverThreadRunFromFailedHandoff(context.Context, string, string, string, string, string) (
		domain.Thread, domain.Run, bool, error)
	GetThreadByRun(context.Context, string) (domain.Thread, error)
	GetThreadBySession(context.Context, string) (domain.Thread, error)
	ListThreadsByCreationPage(context.Context, domain.ThreadFilter, time.Time,
		string) ([]domain.Thread, error)
	GetThreadExecutionFacts(context.Context, []string) (map[string]domain.ThreadExecutionFacts, error)
	ListThreadRuns(context.Context, string) ([]domain.ThreadRun, error)
	ListThreadMessagesPage(context.Context, string, bool, int,
		int) ([]domain.ThreadMessage, error)
	ListThreadTranscriptSourceBefore(context.Context, string, int64, int64,
		int) ([]threadtranscript.Source, error)
	GetThreadSupervisorToolCall(context.Context, string, string) (
		domain.SupervisorToolCall, error)
	GetThreadCommandRuntimeJob(context.Context, string, string) (
		runner.CommandRuntimeJob, error)
	GetThreadCommandRuntimeJobMetadata(context.Context, string, string) (
		runner.CommandRuntimeJobMetadata, error)
	GetThreadCommandRuntimeJobAgentAttribution(context.Context, string, string) (
		domain.AgentAttribution, error)
	ListWebSources(context.Context, string, int) ([]webevidence.Source, error)
	ListWebSnapshots(context.Context, string, int) ([]webevidence.Snapshot, error)
	ListWebCitations(context.Context, string, int) ([]webevidence.Citation, error)
	EnsureThreadSuccessor(context.Context, string, string, domain.Mission, domain.Run,
		domain.RunModeSnapshot, session.Session, []events.Event) (domain.Thread, domain.Run, bool, error)
	TransitionThreadWithOperationKey(context.Context, string, domain.ThreadLifecycleAction,
		int64, string, string, time.Time) (domain.Thread, error)
	ExportThread(context.Context, string) (domain.ThreadExport, error)
	GetMission(ctx context.Context, id string) (domain.Mission, error)
	GetRun(ctx context.Context, id string) (domain.Run, error)
	GetAgentNode(ctx context.Context, id string) (domain.AgentNode, error)
	GetRootAgent(ctx context.Context, runID string) (domain.AgentNode, bool, error)
	GetRunMode(ctx context.Context, runID string) (domain.RunModeSnapshot, error)
	GetRunModeSnapshot(ctx context.Context, id string) (domain.RunModeSnapshot, error)
	GetRunNetworkAuthorityOperation(ctx context.Context, keyDigest string) (
		domain.RunNetworkAuthorityOperation, bool, error)
	TransitionRunNetworkAuthority(ctx context.Context, snapshot domain.RunModeSnapshot,
		operation domain.RunNetworkAuthorityOperation, event events.Event) (
		domain.RunModeSnapshot, bool, error)
	GetRunCreationOperation(ctx context.Context,
		keyDigest string) (domain.RunCreationOperation, bool, error)
	CreateMissionRunWithOperation(ctx context.Context, mission domain.Mission, run domain.Run,
		mode domain.RunModeSnapshot, linkedSession session.Session, initialEvents []events.Event,
		operation domain.RunCreationOperation, pin domain.InitialThreadModelRoutePin) (
		domain.RunCreationOperation, bool, error)
	GetRunExecutionProfile(ctx context.Context,
		runID string) (domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionProfileSnapshot(ctx context.Context,
		id string) (domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionProfileOperation(ctx context.Context,
		keyDigest string) (domain.RunExecutionProfileOperation, bool, error)
	TransitionRunExecutionProfile(ctx context.Context,
		snapshot domain.RunExecutionProfileSnapshot,
		operation domain.RunExecutionProfileOperation,
		event events.Event) (domain.RunExecutionProfileSnapshot, bool, error)
	GetRunExecutionPermission(ctx context.Context,
		runID string) (domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionPermissionSnapshot(ctx context.Context,
		id string) (domain.RunExecutionPermissionSnapshot, error)
	GetRunExecutionPermissionOperation(ctx context.Context,
		keyDigest string) (domain.RunExecutionPermissionOperation, bool, error)
	TransitionRunExecutionPermission(ctx context.Context,
		snapshot domain.RunExecutionPermissionSnapshot,
		operation domain.RunExecutionPermissionOperation,
		event events.Event) (domain.RunExecutionPermissionSnapshot, bool, error)
	GetThreadExecutionPermission(ctx context.Context,
		threadID string) (domain.ThreadExecutionPermissionSnapshot, error)
	GetThreadExecutionPermissionSnapshot(ctx context.Context,
		id string) (domain.ThreadExecutionPermissionSnapshot, error)
	GetThreadExecutionPermissionOperation(ctx context.Context,
		keyDigest string) (domain.ThreadExecutionPermissionOperation, bool, error)
	TransitionThreadExecutionPermission(ctx context.Context,
		snapshot domain.ThreadExecutionPermissionSnapshot,
		operation domain.ThreadExecutionPermissionOperation) (
		domain.ThreadExecutionPermissionSnapshot,
		domain.ThreadExecutionPermissionOperation, bool, error)
	GetRunBrowserCDPPermission(ctx context.Context,
		runID string) (domain.RunBrowserCDPPermissionSnapshot, error)
	GetRunBrowserCDPPermissionSnapshot(ctx context.Context,
		id string) (domain.RunBrowserCDPPermissionSnapshot, error)
	GetRunBrowserCDPPermissionOperation(ctx context.Context,
		keyDigest string) (domain.RunBrowserCDPPermissionOperation, bool, error)
	TransitionRunBrowserCDPPermission(ctx context.Context,
		snapshot domain.RunBrowserCDPPermissionSnapshot,
		operation domain.RunBrowserCDPPermissionOperation,
		event events.Event,
		expectedExecution domain.RunExecutionPermissionSnapshot) (
		domain.RunBrowserCDPPermissionSnapshot, bool, error)
	GetRunExecutionInteraction(ctx context.Context,
		runID string) (domain.RunExecutionInteractionSnapshot, error)
	GetRunExecutionInteractionSnapshot(ctx context.Context,
		id string) (domain.RunExecutionInteractionSnapshot, error)
	GetRunExecutionInteractionOperation(ctx context.Context,
		keyDigest string) (domain.RunExecutionInteractionOperation, bool, error)
	TransitionRunExecutionInteraction(ctx context.Context,
		snapshot domain.RunExecutionInteractionSnapshot,
		operation domain.RunExecutionInteractionOperation,
		event events.Event) (domain.RunExecutionInteractionSnapshot, bool, error)
	GetRunBySession(ctx context.Context, sessionID string) (domain.Run, bool, error)
	EnqueueOperatorSteering(ctx context.Context,
		request domain.EnqueueOperatorSteeringRequest) (domain.OperatorSteeringEnqueueResult, error)
	GetOperatorSteering(ctx context.Context, id string) (domain.OperatorSteeringMessage, error)
	CancelOperatorSteering(ctx context.Context,
		request domain.CancelOperatorSteeringRequest) (domain.OperatorSteeringCancellationResult, error)
	ListRuns(ctx context.Context, filter domain.RunFilter) ([]domain.Run, error)
	ListRunsByCreationPage(ctx context.Context, filter domain.RunFilter,
		beforeCreatedAt time.Time, beforeID string) ([]domain.Run, error)
	ListRunEventsPage(ctx context.Context, runID string, offset int, limit int) ([]events.Event, error)
	ListRunEventsAfterSequence(ctx context.Context, runID string, afterSequence int64, limit int) ([]events.Event, error)
	LatestRunEventSequence(ctx context.Context, runID string) (int64, error)
	GetScheduledJob(context.Context, string) (domain.ScheduledJob, error)
	ListScheduledJobs(context.Context, string, int) ([]domain.ScheduledJob, error)
	GetScheduledJobOperation(context.Context, string) (domain.ScheduledJobOperation, bool, error)
	GetScheduledJobAuthorization(context.Context, string) (domain.ScheduledJobAuthorization, bool, error)
	CreateScheduledJob(context.Context, domain.ScheduledJob, *domain.ScheduledJobAuthorization,
		domain.ScheduledJobOperation) (domain.ScheduledJob, bool, error)
	TransitionScheduledJob(context.Context, string, domain.ScheduledJobAction, int64,
		time.Time, domain.ScheduledJobOperation) (domain.ScheduledJob, bool, error)
	ListScheduledJobRounds(context.Context, string, int) ([]domain.ScheduledJobRound, error)
	ListScheduledJobNotifications(context.Context, string, int) ([]domain.ScheduledJobNotification, error)
	ClaimDueScheduledJob(context.Context, string, time.Time) (domain.ScheduledJob,
		domain.ScheduledJobLease, bool, error)
	CompleteScheduledJobRound(context.Context, domain.ScheduledJobLease,
		domain.ScheduledJobRoundOutcome, time.Time) (domain.ScheduledJob,
		domain.ScheduledJobRound, *domain.ScheduledJobNotification, error)
	FailScheduledJobRound(context.Context, domain.ScheduledJobLease,
		domain.ScheduledJobRoundFailure, time.Time) (domain.ScheduledJob,
		domain.ScheduledJobRound, *domain.ScheduledJobNotification, error)
	ReconcileScheduledJobs(context.Context, time.Time, int) (int, error)
	GetSupervisorCheckpoint(ctx context.Context, runID string) (domain.SupervisorCheckpoint, bool, error)
	GetRunExecutionLease(ctx context.Context, runID string) (domain.RunExecutionLease, bool, error)
	GetDrydockByRun(ctx context.Context, runID string) (runworktree.Workspace, bool, error)
	ListOperatorSteering(ctx context.Context, runID string,
		limit int) ([]domain.OperatorSteeringMessage, error)
	GetOperatorSteeringQueueSummary(ctx context.Context,
		runID string) (domain.OperatorSteeringQueueSummary, error)
	RequestSupervisorModelCancellation(ctx context.Context, request domain.RequestModelCancellation) (domain.ModelCancellationResult, error)
	RequestSpecialistModelCancellation(ctx context.Context,
		request domain.RequestSpecialistModelCancellation) (domain.SpecialistModelCancellationResult, error)
	GetToolCallUsage(ctx context.Context, runID string) (toolbudget.Usage, error)
	ListRunSupervisorToolRoundsPage(ctx context.Context, runID string, offset int, limit int) ([]domain.SupervisorToolRound, error)
	ListPlanDeliveryProposals(ctx context.Context, runID string, limit int) ([]domain.PlanDeliveryProposal, error)
	GetPlanDeliveryProposal(ctx context.Context, id string) (domain.PlanDeliveryProposal, error)
	GetPlanDeliverySelectionByRun(ctx context.Context, runID string) (domain.PlanDeliverySelection, bool, error)
	GetExternalSkillProjectionByRun(ctx context.Context, runID string) (skills.ExternalSkillProjection, bool, error)
	ListDeliveryCheckpoints(ctx context.Context, runID string, limit int) ([]domain.DeliveryCheckpoint, error)
	DeliveryGateEnforced(ctx context.Context, runID string) (bool, error)
	ListAgentNodes(ctx context.Context, runID string) ([]domain.AgentNode, error)
	GetAgentCompletion(ctx context.Context, agentID string) (domain.AgentCompletion, bool, error)
	ListSpecialistDelegationProposalsPage(ctx context.Context, runID string,
		offset int, limit int) ([]domain.SpecialistDelegationProposal, error)
	GetSpecialistDelegationReviewByProposal(ctx context.Context,
		proposalID string) (domain.SpecialistDelegationReview, bool, error)
	GetSpecialistDelegationApplicationByProposal(ctx context.Context,
		proposalID string) (domain.SpecialistDelegationApplication, bool, error)
	GetLatestSpecialistOperatorScheduleRequestByApplication(ctx context.Context,
		applicationID string) (domain.SpecialistOperatorScheduleRequest, bool, error)
	GetLatestSpecialistOperatorScheduleAttempt(ctx context.Context,
		requestID string) (domain.SpecialistSchedule, domain.SpecialistOperatorScheduleAttempt, bool, error)
	ListReadOnlyFanoutPlanSummariesPage(ctx context.Context, runID string,
		offset int, limit int) ([]domain.ReadOnlyFanoutPlanSummary, error)
	GetLatestReadOnlyFanoutExecutionSummary(ctx context.Context,
		planID string) (domain.ReadOnlyFanoutExecutionSummary, bool, error)
	ListFindingReportSummariesPage(ctx context.Context, runID string,
		offset int, limit int) ([]domain.FindingReportSummary, error)
	GetFindingReport(ctx context.Context, id string) (domain.FindingReport, error)
	GetApproval(ctx context.Context, id string) (approval.Record, error)
	ListApprovals(ctx context.Context, filter approval.ListFilter) ([]approval.Record, error)
	GetFileEditPreview(ctx context.Context, id string) (fileedit.Preview, error)
	ListFileEditPreviewsPage(ctx context.Context, filter fileedit.ListFilter,
		offset int, limit int) ([]fileedit.Preview, error)
	ListRunFileEditPreviewsPage(ctx context.Context, runID string, offset int, limit int) ([]fileedit.Preview, error)
	FileEditWorkspaceBelongsToRun(ctx context.Context, runID string, sessionID string, workspaceID string) (bool, error)

	GetSession(ctx context.Context, id string) (session.Session, error)
	SaveSession(ctx context.Context, record session.Session) error
	GetWorkspaceInfo(ctx context.Context, id string) (session.WorkspaceInfo, error)
	ListWorkspacesPage(ctx context.Context, offset int, limit int) ([]session.WorkspaceRecord, error)
	ListSessionsPage(ctx context.Context, offset int, limit int) ([]session.Session, error)
	ListSessionsByCreationPage(ctx context.Context, beforeCreatedAt time.Time,
		beforeID string, limit int) ([]session.Session, error)
	ListSessionMessagesPage(ctx context.Context, sessionID string, includeCompacted bool,
		offset int, limit int) ([]session.Message, error)
	ListTerminalOperationRecords(context.Context, string, int) (
		[]operationreceipt.TerminalRecord, error)
	GetEvidenceAttachment(context.Context, string) (session.EvidenceAttachment,
		session.Message, bool, error)
	AttachEvidence(context.Context, session.EvidenceAttachment, session.Message) (
		session.EvidenceAttachment, session.Message, bool, error)
	GetVerificationEvidenceByOperation(context.Context, string) (
		verification.Evidence, bool, error)
	ListVerificationEvidence(context.Context, string, int) ([]verification.Evidence, error)
	RecordVerificationEvidence(context.Context, verification.Evidence) (
		verification.Evidence, bool, error)
	GetVerificationPlanByOperation(context.Context, string) (verification.Plan, bool, error)
	ListVerificationPlans(context.Context, string, int) ([]verification.Plan, error)
	RecordVerificationPlan(context.Context, verification.Plan) (verification.Plan, bool, error)
	GetVerificationPlan(context.Context, string) (verification.Plan, error)
	GetVerificationEvidence(context.Context, string) (verification.Evidence, error)
	GetVerificationPlanEvidenceAssociationByOperation(context.Context, string) (
		verification.PlanEvidenceAssociation, bool, error)
	ListVerificationPlanEvidenceAssociations(context.Context, string, int) (
		[]verification.PlanEvidenceAssociation, error)
	GetVerificationPlanItemCoverageSnapshot(context.Context, string, string, int, int64) (
		verification.PlanItemCoverageCount, bool, error)
	CountVerificationPlanItemAssociationsThroughAnchor(context.Context,
		string, string, int, int64, int64, string) (int, bool, error)
	ListVerificationPlanItemEvidenceAssociations(context.Context,
		string, string, int, int, int64, int64, string) (
		[]verification.PlanEvidenceAssociation, error)
	ListVerificationPlanCoverageCounts(context.Context, string, []string) (
		[]verification.PlanItemCoverageCount, error)
	RecordVerificationPlanEvidenceAssociation(context.Context,
		verification.PlanEvidenceAssociation) (verification.PlanEvidenceAssociation, bool, error)
	GetVerificationSnapshotReceiptByOperation(context.Context, string) (
		verification.SnapshotReceipt, bool, error)
	ListVerificationSnapshotReceipts(context.Context, string, int) (
		[]verification.SnapshotReceipt, error)
	RecordVerificationSnapshotReceipt(context.Context, verification.SnapshotReceipt) (
		verification.SnapshotReceipt, bool, error)
	GetVerificationSnapshotReceipt(context.Context, string) (verification.SnapshotReceipt, error)
	GetVerificationSnapshotReceiptReviewByOperation(context.Context, string) (
		verification.SnapshotReceiptReview, bool, error)
	GetVerificationSnapshotReceiptReviewByReceipt(context.Context, string) (
		verification.SnapshotReceiptReview, bool, error)
	ListVerificationSnapshotReceiptReviews(context.Context, string, int) (
		[]verification.SnapshotReceiptReview, error)
	RecordVerificationSnapshotReceiptReview(context.Context, verification.SnapshotReceiptReview) (
		verification.SnapshotReceiptReview, bool, error)
	ListOperatorActionRecords(context.Context, string, string, string, time.Time, int) (
		[]operatoraction.Record, error)

	GetWorkItem(ctx context.Context, id string) (domain.WorkItem, error)
	ListWorkItems(ctx context.Context, filter domain.WorkItemFilter) ([]domain.WorkItem, error)
	GetNote(ctx context.Context, id string) (domain.Note, error)
	ListNotes(ctx context.Context, filter domain.NoteFilter) ([]domain.Note, error)

	GetRunArtifactDescriptor(ctx context.Context, id string) (artifact.Descriptor, error)
	ListRunArtifacts(ctx context.Context, filter artifact.ListFilter) ([]artifact.Descriptor, error)

	LoadLatestBrowserNetworkEvidence(ctx context.Context,
		executableIdentityFingerprint string) (browserruntime.BrowserNetworkContainmentEvidence, error)
	LoadBrowserNetworkReview(ctx context.Context,
		evidenceFingerprint string) (browserruntime.BrowserNetworkContainmentReview, error)
}

type Config struct {
	AccessToken                           string
	ControlToken                          string
	RunControlEnabled                     bool
	ExecutionPermissionControlEnabled     bool
	BrowserCDPPermissionControlEnabled    bool
	FullCDPSessionControlEnabled          bool
	RunCreationEnabled                    bool
	WorkspaceImportEnabled                bool
	WorkspaceImporter                     WorkspaceImporter
	FileWorkspaceDrydocks                 *application.RunWorktreeService
	StandardCodePresetEnabled             bool
	SessionMessageEnabled                 bool
	SessionSteeringControlEnabled         bool
	RunLifecycleEnabled                   bool
	RunExecutionEnabled                   bool
	PlanDeliveryControlEnabled            bool
	ApprovalControlEnabled                bool
	WebFetchAuthorizationSchedulerEnabled bool
	ModelControlEnabled                   bool
	ProviderDefinitionEnabled             bool
	ProviderCredentialEnabled             bool
	ChatGPTAuthEnabled                    bool
	FileEditReviewEnabled                 bool
	FileEditProposalEnabled               bool
	RunWakeControlEnabled                 bool
	FileEditApplyEnabled                  bool
	RunWakeExecutionEnabled               bool
	RunWakeWorkerEnabled                  bool
	ScheduledJobControlEnabled            bool
	ScheduledJobWorkerEnabled             bool
	SkillInstallationEnabled              bool
	SkillCatalogControlEnabled            bool
	EvidenceAttachmentEnabled             bool
	VerificationEvidenceEnabled           bool
	EmbeddedAnalyzerExecutionEnabled      bool
	WorkspaceCheckpointControlEnabled     bool
	GitAdvancedControlEnabled             bool
	GitHubReviewControlEnabled            bool
	BatchDeliveryControlEnabled           bool
	BatchDeliveryHostValidationEnabled    bool
	ExtensionControlEnabled               bool
	LifecycleHooks                        *hooks.Engine
	UIEvidenceControlEnabled              bool
	ExecutionPermissionCapabilities       domain.ExecutionPermissionRuntimeCapabilities
	BrowserCDPPermissionCapabilities      domain.BrowserCDPPermissionRuntimeCapabilities
	FullCDPSessionController              application.FullCDPSessionController
	AgentBrowserController                AgentBrowserController
	CapabilityReadinessRuntime            *application.CapabilityReadinessRuntime
	CommandRuntimeAdapters                []commandruntimeadapter.Identity
	CommandRuntimeAdvertiser              toolgateway.CommandRuntimeAdvertiser
	RunLifecycleController                RunLifecycleController
	ThreadTurnController                  ThreadTurnController
	ThreadReviewReader                    ThreadReviewReader
	ThreadGitController                   ThreadGitController
	ThreadApplicationServiceController    ThreadApplicationServiceController
	ProjectActionsController              ProjectActionsController
	ProjectPreparationController          ProjectPreparationController
	ThreadPullRequestController           ThreadPullRequestController
	PRFollowupController                  PRFollowupController
	StandardCodePresetController          StandardCodePresetController
	StandardCodeDeliveryController        StandardCodeDeliveryController
	RunExecutionController                RunExecutionController
	PublicModelStreamSource               PublicModelStreamSource
	PlanDeliveryController                PlanDeliveryController
	ApprovalController                    ApprovalController
	ControlledCommandProposalController   ControlledCommandProposalController
	HostCommandProposalController         HostCommandProposalController
	ModelControlController                ModelControlController
	ThreadModelRouteController            ThreadModelRouteController
	ProviderSearchReadinessController     ProviderSearchReadinessController
	ProviderDefinitionController          ProviderDefinitionController
	PriceSnapshotController               PriceSnapshotController
	FanoutExecutionController             FanoutExecutionController
	ChildTaskControlController            ChildTaskControlController
	SpecialistExecutionController         SpecialistExecutionController
	ProviderCredentialController          ProviderCredentialController
	ChatGPTAuthController                 ChatGPTAuthController
	FileEditReviewController              FileEditReviewController
	FileEditProposalController            FileEditProposalController
	RunWakeController                     RunWakeController
	FileEditApplyController               FileEditApplyController
	RunWakeExecutionController            RunWakeExecutionController
	RunWakeWorkerHealthSource             RunWakeWorkerHealthSource
	ScheduledJobController                ScheduledJobController
	ScheduledJobWorkerHealthSource        ScheduledJobWorkerHealthSource
	SkillInstallationController           SkillInstallationController
	SkillCatalogController                SkillCatalogController
	EmbeddedAnalyzerExecutionController   EmbeddedAnalyzerExecutionController
	WorkspaceCheckpointController         WorkspaceCheckpointController
	GitAdvancedController                 GitAdvancedController
	GitHubReviewController                GitHubReviewController
	BatchDeliveryController               BatchDeliveryController
	ExtensionController                   ExtensionController
	CodeIntelSource                       CodeIntelSource
	CodeIntelController                   CodeIntelController
	UIEvidenceController                  UIEvidenceController
	DockerSandboxController               DockerSandboxController
	DockerEnvironmentController           DockerEnvironmentController
	SandboxEnvironmentController          SandboxEnvironmentController
	SandboxEnvironmentControlEnabled      bool
	ModelRegistry                         *modelregistry.Registry
	AppVersion                            string
	EventStream                           EventStreamConfig
	UIHandler                             http.Handler
}

type API struct {
	store                                 Store
	tokenHash                             [sha256.Size]byte
	controlTokenHash                      [sha256.Size]byte
	controlEnabled                        bool
	executionPermissionControlEnabled     bool
	browserCDPPermissionControlEnabled    bool
	fullCDPSessionControlEnabled          bool
	runCreationEnabled                    bool
	workspaceImportEnabled                bool
	workspaceImporter                     WorkspaceImporter
	standardCodePresetEnabled             bool
	sessionMessageEnabled                 bool
	sessionSteeringControlEnabled         bool
	runLifecycleEnabled                   bool
	runExecutionEnabled                   bool
	planDeliveryControlEnabled            bool
	approvalControlEnabled                bool
	webFetchAuthorizationSchedulerEnabled bool
	modelControlEnabled                   bool
	providerDefinitionEnabled             bool
	providerCredentialEnabled             bool
	chatGPTAuthEnabled                    bool
	fileEditReviewEnabled                 bool
	fileWorkspaceDrydocks                 *application.RunWorktreeService
	fileEditProposalEnabled               bool
	runWakeControlEnabled                 bool
	fileEditApplyEnabled                  bool
	runWakeExecutionEnabled               bool
	runWakeWorkerEnabled                  bool
	scheduledJobControlEnabled            bool
	scheduledJobWorkerEnabled             bool
	skillInstallationEnabled              bool
	skillCatalogControlEnabled            bool
	evidenceAttachmentEnabled             bool
	verificationEvidenceEnabled           bool
	embeddedAnalyzerExecutionEnabled      bool
	workspaceCheckpointControlEnabled     bool
	gitAdvancedControlEnabled             bool
	githubReviewControlEnabled            bool
	batchDeliveryControlEnabled           bool
	batchDeliveryHostValidationEnabled    bool
	extensionControlEnabled               bool
	lifecycleHooks                        *hooks.Engine
	uiEvidenceControlEnabled              bool
	dockerSandboxControlEnabled           bool
	dockerExecutionEnabled                bool
	executionPermissionCapabilities       domain.ExecutionPermissionRuntimeCapabilities
	browserCDPPermissionCapabilities      domain.BrowserCDPPermissionRuntimeCapabilities
	fullCDPSessionController              application.FullCDPSessionController
	agentBrowserController                AgentBrowserController
	capabilityReadiness                   *application.RunCapabilityReadinessService
	commandRuntimeAdapters                []commandruntimeadapter.Identity
	commandActivitySource                 application.ThreadActivityCommandRuntimeSource
	capabilityReadinessRuntime            application.CapabilityReadinessRuntime
	runLifecycleController                RunLifecycleController
	threadTurnController                  ThreadTurnController
	threadReview                          ThreadReviewReader
	threadGitController                   ThreadGitController
	threadApplicationServiceController    ThreadApplicationServiceController
	projectActionsController              ProjectActionsController
	projectPreparationController          ProjectPreparationController
	threadPullRequestController           ThreadPullRequestController
	prFollowupController                  PRFollowupController
	standardCodePresetController          StandardCodePresetController
	standardCodeDeliveryController        StandardCodeDeliveryController
	runExecutionController                RunExecutionController
	publicModelStreamSource               PublicModelStreamSource
	planDeliveryController                PlanDeliveryController
	approvalController                    ApprovalController
	controlledCommandProposalController   ControlledCommandProposalController
	hostCommandProposalController         HostCommandProposalController
	modelControlController                ModelControlController
	threadModelRouteController            ThreadModelRouteController
	providerSearchReadinessController     ProviderSearchReadinessController
	providerDefinitionController          ProviderDefinitionController
	priceSnapshotController               PriceSnapshotController
	fanoutExecutionController             FanoutExecutionController
	childTaskControlController            ChildTaskControlController
	specialistExecutionController         SpecialistExecutionController
	providerCredentialController          ProviderCredentialController
	chatGPTAuthController                 ChatGPTAuthController
	fileEditReviewController              FileEditReviewController
	fileEditProposalController            FileEditProposalController
	runWakeController                     RunWakeController
	fileEditApplyController               FileEditApplyController
	runWakeExecutionController            RunWakeExecutionController
	runWakeWorkerHealthSource             RunWakeWorkerHealthSource
	scheduledJobController                ScheduledJobController
	scheduledJobWorkerHealthSource        ScheduledJobWorkerHealthSource
	diagnostics                           *application.DiagnosticsService
	skillInstallationController           SkillInstallationController
	skillCatalogController                SkillCatalogController
	embeddedAnalyzerExecutionController   EmbeddedAnalyzerExecutionController
	workspaceCheckpointController         WorkspaceCheckpointController
	gitAdvancedController                 GitAdvancedController
	githubReviewController                GitHubReviewController
	batchDeliveryController               BatchDeliveryController
	extensionController                   ExtensionController
	codeIntelSource                       CodeIntelSource
	codeIntelController                   CodeIntelController
	uiEvidenceController                  UIEvidenceController
	dockerSandboxController               DockerSandboxController
	dockerEnvironmentController           DockerEnvironmentController
	sandboxEnvironmentController          SandboxEnvironmentController
	sandboxEnvironmentControlEnabled      bool
	modelRegistry                         *modelregistry.Registry
	appVersion                            string
	openAPI                               []byte
	eventStream                           EventStreamConfig
	eventStreamSlots                      chan struct{}
	uiHandler                             http.Handler
}

func New(store Store, config Config) (*API, error) {
	if store == nil {
		return nil, errors.New("HTTP API store is required")
	}
	token := config.AccessToken
	if err := validateAccessToken(token); err != nil {
		return nil, err
	}
	controlToken := config.ControlToken
	controlTokenPresent := controlToken != ""
	var controlTokenHash [sha256.Size]byte
	if controlTokenPresent {
		if err := validateAccessToken(controlToken); err != nil {
			return nil, apperror.Wrap(apperror.CodeInvalidArgument, "invalid HTTP API control token", err)
		}
		controlTokenHash = sha256.Sum256([]byte(controlToken))
		accessTokenHash := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(accessTokenHash[:], controlTokenHash[:]) == 1 {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"HTTP API read and control tokens must be distinct")
		}
	}
	if (config.AgentBrowserController != nil || config.RunControlEnabled || config.ExecutionPermissionControlEnabled ||
		config.BrowserCDPPermissionControlEnabled ||
		config.FullCDPSessionControlEnabled ||
		config.RunCreationEnabled || config.WorkspaceImportEnabled || config.StandardCodePresetEnabled ||
		config.SessionMessageEnabled ||
		config.SessionSteeringControlEnabled || config.RunLifecycleEnabled ||
		config.RunExecutionEnabled || config.PlanDeliveryControlEnabled ||
		config.ApprovalControlEnabled || config.WebFetchAuthorizationSchedulerEnabled ||
		config.ModelControlEnabled ||
		config.ProviderDefinitionEnabled ||
		config.ProviderCredentialEnabled ||
		config.ChatGPTAuthEnabled ||
		config.FileEditReviewEnabled || config.FileEditProposalEnabled ||
		config.RunWakeControlEnabled ||
		config.FileEditApplyEnabled || config.RunWakeExecutionEnabled ||
		config.RunWakeWorkerEnabled ||
		config.ScheduledJobControlEnabled || config.ScheduledJobWorkerEnabled ||
		config.SkillInstallationEnabled || config.SkillCatalogControlEnabled || config.EvidenceAttachmentEnabled ||
		config.VerificationEvidenceEnabled || config.EmbeddedAnalyzerExecutionEnabled ||
		config.WorkspaceCheckpointControlEnabled || config.BatchDeliveryControlEnabled ||
		config.GitAdvancedControlEnabled || config.GitHubReviewControlEnabled ||
		config.ExtensionControlEnabled || config.UIEvidenceControlEnabled || config.SandboxEnvironmentControlEnabled) &&
		!controlTokenPresent {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API control capabilities require a control token")
	}
	if config.SandboxEnvironmentControlEnabled && config.SandboxEnvironmentController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "HTTP API sandbox environment controller is required when enabled")
	}
	if config.RunLifecycleEnabled && config.RunLifecycleController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Run lifecycle controller is required when enabled")
	}
	if config.WorkspaceImportEnabled && config.WorkspaceImporter == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API workspace importer is required when enabled")
	}
	if config.FullCDPSessionControlEnabled && config.FullCDPSessionController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Full CDP session controller is required when enabled")
	}
	if config.FullCDPSessionControlEnabled &&
		(!config.BrowserCDPPermissionControlEnabled ||
			!config.BrowserCDPPermissionCapabilities.ControlEnabled ||
			!config.BrowserCDPPermissionCapabilities.FullDebugEnabled ||
			!config.ExecutionPermissionCapabilities.DangerFullAccessEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Full CDP session control requires the production Full CDP permission stack")
	}
	if config.StandardCodePresetEnabled && config.StandardCodePresetController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Standard Code preset controller is required when enabled")
	}
	if config.RunExecutionEnabled && config.RunExecutionController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Run execution controller is required when enabled")
	}
	if config.RunCreationEnabled && config.SessionMessageEnabled &&
		config.RunLifecycleEnabled && config.RunExecutionEnabled &&
		config.ThreadTurnController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Thread turn controller is required when its prerequisite capabilities are enabled")
	}
	if config.PlanDeliveryControlEnabled && config.PlanDeliveryController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Plan/Delivery controller is required when enabled")
	}
	if config.ApprovalControlEnabled && config.ApprovalController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API approval controller is required when enabled")
	}
	if config.WebFetchAuthorizationSchedulerEnabled {
		if !config.RunExecutionEnabled || !config.ApprovalControlEnabled ||
			!config.ExecutionPermissionCapabilities.OperatorApprovalEnabled {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"HTTP API Web fetch authorization scheduler requires Run execution, approval control, and operator approval")
		}
		if _, ok := config.RunExecutionController.(WebFetchAuthorizationResumeController); !ok {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"HTTP API Web fetch authorization scheduler requires a resume controller")
		}
	}
	if config.ControlledCommandProposalController == nil {
		config.ControlledCommandProposalController = application.NewControlledCommandHistory(store)
	}
	if config.HostCommandProposalController == nil {
		config.HostCommandProposalController = application.NewHostCommandHistory(store)
	}

	if config.ModelControlEnabled && config.ModelControlController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API model controller is required when enabled")
	}
	if config.ModelControlEnabled && config.ThreadModelRouteController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Thread model route controller is required when model control is enabled")
	}
	if config.ProviderDefinitionEnabled && config.ProviderDefinitionController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API custom Provider definition controller is required when enabled")
	}
	if config.ProviderCredentialEnabled && config.ProviderCredentialController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Provider credential controller is required when enabled")
	}
	if config.ChatGPTAuthEnabled && config.ChatGPTAuthController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API ChatGPT sign-in controller is required when enabled")
	}
	if config.FileEditReviewEnabled && config.FileEditReviewController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API file edit review controller is required when enabled")
	}
	if config.FileEditProposalEnabled && config.FileEditProposalController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API file edit proposal controller is required when enabled")
	}
	if config.RunWakeControlEnabled && config.RunWakeController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Run wake controller is required when enabled")
	}
	if config.FileEditApplyEnabled && config.FileEditApplyController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API FileEdit apply controller is required when enabled")
	}
	if config.RunWakeExecutionEnabled && config.RunWakeExecutionController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API foreground Run wake controller is required when enabled")
	}
	if config.RunWakeWorkerEnabled != (config.RunWakeWorkerHealthSource != nil) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Run wake worker health source must exactly match enablement")
	}
	if config.ScheduledJobControlEnabled && config.ScheduledJobController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API scheduled job controller is required when enabled")
	}
	if config.ScheduledJobWorkerEnabled != (config.ScheduledJobWorkerHealthSource != nil) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API scheduled job worker health source must exactly match enablement")
	}
	if config.SkillCatalogControlEnabled && config.SkillCatalogController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "HTTP API Skill catalog control requires a controller")
	}
	if config.SkillInstallationEnabled && config.SkillInstallationController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Skill installation controller is required when enabled")
	}
	if config.EmbeddedAnalyzerExecutionEnabled &&
		config.EmbeddedAnalyzerExecutionController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API embedded analyzer execution controller is required when enabled")
	}
	if config.WorkspaceCheckpointControlEnabled &&
		config.WorkspaceCheckpointController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Workspace checkpoint controller is required when enabled")
	}
	if config.GitAdvancedControlEnabled && config.GitAdvancedController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Git advanced controller is required when enabled")
	}
	if config.GitHubReviewControlEnabled && config.GitHubReviewController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API GitHub review controller is required when enabled")
	}
	if config.GitHubReviewControlEnabled &&
		(!config.ExecutionPermissionControlEnabled ||
			!config.ExecutionPermissionCapabilities.OperatorApprovalEnabled ||
			!config.ApprovalControlEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API GitHub review control requires permission control, operator approval, and approval control")
	}
	if config.GitAdvancedControlEnabled &&
		(!config.ExecutionPermissionControlEnabled ||
			!config.ExecutionPermissionCapabilities.OperatorApprovalEnabled ||
			!config.ApprovalControlEnabled ||
			!config.WorkspaceCheckpointControlEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API Git advanced control requires permission control, operator approval, approval control, and Workspace checkpoint control")
	}
	if config.BatchDeliveryControlEnabled && config.BatchDeliveryController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API batch delivery controller is required when enabled")
	}
	if config.ExtensionControlEnabled && config.ExtensionController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API extension controller is required when enabled")
	}
	if config.BatchDeliveryHostValidationEnabled &&
		(!config.BatchDeliveryControlEnabled || !config.ExecutionPermissionControlEnabled ||
			!config.ExecutionPermissionCapabilities.OperatorApprovalEnabled ||
			!config.ExecutionPermissionCapabilities.DangerFullAccessEnabled) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API batch delivery host validation requires batch control, permission control, operator approval, and danger-full-access")
	}
	if config.UIEvidenceControlEnabled && config.UIEvidenceController == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API UI evidence controller is required when enabled")
	}
	dockerExecutionEnabled := false
	if config.DockerSandboxController != nil {
		capabilities, epochFingerprint, err :=
			config.DockerSandboxController.RuntimeCapabilities()
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeInvalidArgument,
				"HTTP API Docker Sandbox controller capabilities are invalid", err)
		}
		if capabilities.Validate() != nil || strings.TrimSpace(epochFingerprint) == "" {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"HTTP API Docker Sandbox controller capabilities are invalid")
		}
		dockerExecutionEnabled = capabilities.Enabled
		if dockerExecutionEnabled && (!controlTokenPresent ||
			!config.ExecutionPermissionControlEnabled) {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"HTTP API Docker execution requires permission control and a control token")
		}
	}
	if err := config.ExecutionPermissionCapabilities.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument,
			"HTTP API execution permission capabilities are invalid", err)
	}
	if (config.ExecutionPermissionCapabilities.WorkspaceSandboxEnabled ||
		config.ExecutionPermissionCapabilities.OperatorApprovalEnabled ||
		config.ExecutionPermissionCapabilities.DangerFullAccessEnabled) &&
		!config.ExecutionPermissionControlEnabled {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API execution permission capabilities require execution permission control")
	}
	if err := config.BrowserCDPPermissionCapabilities.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument,
			"HTTP API browser CDP permission capabilities are invalid", err)
	}
	if config.BrowserCDPPermissionCapabilities.ControlEnabled !=
		config.BrowserCDPPermissionControlEnabled {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API browser CDP capability must exactly match browser CDP control")
	}
	if config.BrowserCDPPermissionCapabilities.FullDebugEnabled &&
		!config.ExecutionPermissionCapabilities.DangerFullAccessEnabled {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API full CDP requires the Full Access execution adapter")
	}
	if err := validateControllerContracts(store, config); err != nil {
		return nil, err
	}
	readinessRuntime := application.CapabilityReadinessRuntime{
		RunControlEnabled:                  controlTokenPresent && config.RunControlEnabled,
		RunExecutionEnabled:                controlTokenPresent && config.RunExecutionEnabled,
		ExecutionPermissionControlEnabled:  controlTokenPresent && config.ExecutionPermissionControlEnabled,
		BrowserCDPPermissionControlEnabled: controlTokenPresent && config.BrowserCDPPermissionControlEnabled,
		ExecutionPermissionCapabilities:    config.ExecutionPermissionCapabilities,
		BrowserCDPPermissionCapabilities:   config.BrowserCDPPermissionCapabilities,
		StandardCodePresetEnabled:          controlTokenPresent && config.StandardCodePresetEnabled,
		LocalSandboxInstalled:              config.ExecutionPermissionCapabilities.WorkspaceSandboxEnabled,
		DockerStartupGateEnabled:           dockerExecutionEnabled,
		DockerAvailable:                    dockerExecutionEnabled,
		CommandRuntimeAdapters:             append([]commandruntimeadapter.Identity(nil), config.CommandRuntimeAdapters...),
	}
	if config.CapabilityReadinessRuntime != nil {
		readinessRuntime = *config.CapabilityReadinessRuntime
		if readinessRuntime.RunControlEnabled !=
			(controlTokenPresent && config.RunControlEnabled) ||
			readinessRuntime.RunExecutionEnabled !=
				(controlTokenPresent && config.RunExecutionEnabled) ||
			readinessRuntime.ExecutionPermissionControlEnabled !=
				(controlTokenPresent && config.ExecutionPermissionControlEnabled) ||
			readinessRuntime.BrowserCDPPermissionControlEnabled !=
				(controlTokenPresent && config.BrowserCDPPermissionControlEnabled) ||
			readinessRuntime.StandardCodePresetEnabled !=
				(controlTokenPresent && config.StandardCodePresetEnabled) ||
			readinessRuntime.ExecutionPermissionCapabilities !=
				config.ExecutionPermissionCapabilities ||
			readinessRuntime.BrowserCDPPermissionCapabilities !=
				config.BrowserCDPPermissionCapabilities ||
			readinessRuntime.DockerStartupGateEnabled != dockerExecutionEnabled {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"HTTP API capability readiness runtime does not match process startup gates")
		}
		if !sameCommandRuntimeAdapterIdentities(readinessRuntime.CommandRuntimeAdapters,
			config.CommandRuntimeAdapters) {
			return nil, apperror.New(apperror.CodeInvalidArgument,
				"HTTP API Command Runtime adapter readiness does not match process installation")
		}
	}
	if err := readinessRuntime.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument,
			"HTTP API capability readiness runtime is invalid", err)
	}
	capabilityReadiness := application.NewRunCapabilityReadinessService(store,
		readinessRuntime, config.CommandRuntimeAdvertiser)
	var commandActivitySource application.ThreadActivityCommandRuntimeSource
	if source, ok := config.CommandRuntimeAdvertiser.(application.ThreadActivityCommandRuntimeSource); ok {
		commandActivitySource = source
	}
	threadApplicationServices := config.ThreadApplicationServiceController
	if threadApplicationServices == nil {
		if state, ok := store.(application.ThreadApplicationServiceStore); ok {
			service := application.NewThreadApplicationService(state)
			if runtime, ok := config.CommandRuntimeAdvertiser.(application.ThreadApplicationServiceCommandRuntime); ok {
				service.WithCommandRuntime(runtime)
			}
			threadApplicationServices = service
		}
	}
	version := strings.TrimSpace(config.AppVersion)
	if version == "" {
		version = "unknown"
	}
	modelRegistry := config.ModelRegistry
	if modelRegistry == nil {
		modelRegistry = modelregistry.New(nil)
	}
	document, err := GenerateOpenAPI()
	if err != nil {
		return nil, fmt.Errorf("generate OpenAPI document: %w", err)
	}
	eventStream, err := normalizeEventStreamConfig(config.EventStream)
	if err != nil {
		return nil, err
	}
	return &API{store: store, tokenHash: sha256.Sum256([]byte(token)),
		controlTokenHash: controlTokenHash,
		controlEnabled:   controlTokenPresent && config.RunControlEnabled,
		executionPermissionControlEnabled: controlTokenPresent &&
			config.ExecutionPermissionControlEnabled,
		browserCDPPermissionControlEnabled: controlTokenPresent &&
			config.BrowserCDPPermissionControlEnabled,
		fullCDPSessionControlEnabled: controlTokenPresent &&
			config.FullCDPSessionControlEnabled,
		runCreationEnabled:            controlTokenPresent && config.RunCreationEnabled,
		workspaceImportEnabled:        controlTokenPresent && config.WorkspaceImportEnabled,
		workspaceImporter:             config.WorkspaceImporter,
		fileWorkspaceDrydocks:         config.FileWorkspaceDrydocks,
		standardCodePresetEnabled:     controlTokenPresent && config.StandardCodePresetEnabled,
		appVersion:                    version,
		sessionMessageEnabled:         controlTokenPresent && config.SessionMessageEnabled,
		sessionSteeringControlEnabled: controlTokenPresent && config.SessionSteeringControlEnabled,
		runLifecycleEnabled:           controlTokenPresent && config.RunLifecycleEnabled,
		runExecutionEnabled:           controlTokenPresent && config.RunExecutionEnabled,
		planDeliveryControlEnabled:    controlTokenPresent && config.PlanDeliveryControlEnabled,
		approvalControlEnabled:        controlTokenPresent && config.ApprovalControlEnabled,
		webFetchAuthorizationSchedulerEnabled: controlTokenPresent &&
			config.WebFetchAuthorizationSchedulerEnabled,
		modelControlEnabled:               controlTokenPresent && config.ModelControlEnabled,
		providerDefinitionEnabled:         controlTokenPresent && config.ProviderDefinitionEnabled,
		providerCredentialEnabled:         controlTokenPresent && config.ProviderCredentialEnabled,
		chatGPTAuthEnabled:                controlTokenPresent && config.ChatGPTAuthEnabled,
		fileEditReviewEnabled:             controlTokenPresent && config.FileEditReviewEnabled,
		fileEditProposalEnabled:           controlTokenPresent && config.FileEditProposalEnabled,
		runWakeControlEnabled:             controlTokenPresent && config.RunWakeControlEnabled,
		fileEditApplyEnabled:              controlTokenPresent && config.FileEditApplyEnabled,
		runWakeExecutionEnabled:           controlTokenPresent && config.RunWakeExecutionEnabled,
		runWakeWorkerEnabled:              controlTokenPresent && config.RunWakeWorkerEnabled,
		scheduledJobControlEnabled:        controlTokenPresent && config.ScheduledJobControlEnabled,
		scheduledJobWorkerEnabled:         controlTokenPresent && config.ScheduledJobWorkerEnabled,
		skillInstallationEnabled:          controlTokenPresent && config.SkillInstallationEnabled,
		skillCatalogControlEnabled:        controlTokenPresent && config.SkillCatalogControlEnabled,
		evidenceAttachmentEnabled:         controlTokenPresent && config.EvidenceAttachmentEnabled,
		verificationEvidenceEnabled:       controlTokenPresent && config.VerificationEvidenceEnabled,
		embeddedAnalyzerExecutionEnabled:  controlTokenPresent && config.EmbeddedAnalyzerExecutionEnabled,
		workspaceCheckpointControlEnabled: controlTokenPresent && config.WorkspaceCheckpointControlEnabled,
		gitAdvancedControlEnabled:         controlTokenPresent && config.GitAdvancedControlEnabled,
		githubReviewControlEnabled:        controlTokenPresent && config.GitHubReviewControlEnabled,
		batchDeliveryControlEnabled:       controlTokenPresent && config.BatchDeliveryControlEnabled,
		batchDeliveryHostValidationEnabled: controlTokenPresent &&
			config.BatchDeliveryHostValidationEnabled,
		extensionControlEnabled:  controlTokenPresent && config.ExtensionControlEnabled,
		lifecycleHooks:           config.LifecycleHooks,
		uiEvidenceControlEnabled: controlTokenPresent && config.UIEvidenceControlEnabled,
		dockerSandboxControlEnabled: config.DockerSandboxController != nil &&
			controlTokenPresent && config.ExecutionPermissionControlEnabled,
		dockerExecutionEnabled:              dockerExecutionEnabled,
		executionPermissionCapabilities:     config.ExecutionPermissionCapabilities,
		browserCDPPermissionCapabilities:    config.BrowserCDPPermissionCapabilities,
		fullCDPSessionController:            config.FullCDPSessionController,
		agentBrowserController:              config.AgentBrowserController,
		capabilityReadiness:                 capabilityReadiness,
		commandRuntimeAdapters:              append([]commandruntimeadapter.Identity(nil), config.CommandRuntimeAdapters...),
		projectActionsController:            config.ProjectActionsController,
		projectPreparationController:        config.ProjectPreparationController,
		commandActivitySource:               commandActivitySource,
		capabilityReadinessRuntime:          readinessRuntime,
		runLifecycleController:              config.RunLifecycleController,
		threadTurnController:                config.ThreadTurnController,
		threadReview:                        config.ThreadReviewReader,
		threadGitController:                 config.ThreadGitController,
		threadApplicationServiceController:  threadApplicationServices,
		threadPullRequestController:         config.ThreadPullRequestController,
		prFollowupController:                config.PRFollowupController,
		standardCodePresetController:        config.StandardCodePresetController,
		standardCodeDeliveryController:      config.StandardCodeDeliveryController,
		runExecutionController:              config.RunExecutionController,
		publicModelStreamSource:             config.PublicModelStreamSource,
		planDeliveryController:              config.PlanDeliveryController,
		approvalController:                  config.ApprovalController,
		controlledCommandProposalController: config.ControlledCommandProposalController,
		hostCommandProposalController:       config.HostCommandProposalController,
		modelControlController:              config.ModelControlController,
		threadModelRouteController:          config.ThreadModelRouteController,
		providerSearchReadinessController:   config.ProviderSearchReadinessController,
		providerDefinitionController:        config.ProviderDefinitionController,
		priceSnapshotController:             config.PriceSnapshotController,
		fanoutExecutionController:           config.FanoutExecutionController,
		childTaskControlController:          config.ChildTaskControlController,
		specialistExecutionController:       config.SpecialistExecutionController,
		providerCredentialController:        config.ProviderCredentialController,
		chatGPTAuthController:               config.ChatGPTAuthController,
		fileEditReviewController:            config.FileEditReviewController,
		fileEditProposalController:          config.FileEditProposalController,
		runWakeController:                   config.RunWakeController,
		fileEditApplyController:             config.FileEditApplyController,
		runWakeExecutionController:          config.RunWakeExecutionController,
		runWakeWorkerHealthSource:           config.RunWakeWorkerHealthSource,
		scheduledJobController:              config.ScheduledJobController,
		scheduledJobWorkerHealthSource:      config.ScheduledJobWorkerHealthSource,
		diagnostics:                         application.NewDiagnosticsService(store, modelRegistry),
		skillInstallationController:         config.SkillInstallationController,
		skillCatalogController:              config.SkillCatalogController,
		embeddedAnalyzerExecutionController: config.EmbeddedAnalyzerExecutionController,
		workspaceCheckpointController:       config.WorkspaceCheckpointController,
		gitAdvancedController:               config.GitAdvancedController,
		githubReviewController:              config.GitHubReviewController,
		batchDeliveryController:             config.BatchDeliveryController,
		extensionController:                 config.ExtensionController,
		codeIntelSource:                     config.CodeIntelSource,
		codeIntelController:                 config.CodeIntelController,
		uiEvidenceController:                config.UIEvidenceController,
		dockerSandboxController:             config.DockerSandboxController,
		dockerEnvironmentController:         config.DockerEnvironmentController,
		sandboxEnvironmentController:        config.SandboxEnvironmentController,
		sandboxEnvironmentControlEnabled:    controlTokenPresent && config.SandboxEnvironmentControlEnabled,
		modelRegistry:                       modelRegistry,
		openAPI:                             document, eventStream: eventStream,
		eventStreamSlots: make(chan struct{}, eventStream.MaxConnections),
		uiHandler:        config.UIHandler}, nil
}

func GenerateAccessToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate HTTP API access token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func validateAccessToken(token string) error {
	if token != strings.TrimSpace(token) || !utf8.ValidString(token) ||
		len([]byte(token)) < MinAccessTokenBytes || len([]byte(token)) > MaxAccessTokenBytes {
		return apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("HTTP API access token must be normalized UTF-8 between %d and %d bytes",
				MinAccessTokenBytes, MaxAccessTokenBytes))
	}
	for _, current := range token {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return apperror.New(apperror.CodeInvalidArgument,
				"HTTP API access token cannot contain whitespace or control characters")
		}
	}
	return nil
}

func sameCommandRuntimeAdapterIdentities(left, right []commandruntimeadapter.Identity) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func ListenLoopback(ctx context.Context, address string) (net.Listener, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	address = strings.TrimSpace(address)
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"HTTP API listen address must use loopback-host:port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "HTTP API listen port is invalid")
	}
	if !loopbackHost(host) {
		return nil, apperror.New(apperror.CodePolicyDenied,
			"HTTP API listen host must be localhost or a loopback IP address")
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, apperror.Wrap(apperror.CodeUnavailable, "HTTP API listen failed", err)
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcpAddress.IP == nil || !tcpAddress.IP.IsLoopback() {
		_ = listener.Close()
		return nil, apperror.New(apperror.CodePolicyDenied,
			"HTTP API listener resolved outside the loopback interface")
	}
	return listener, nil
}

func loopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a *API) Handler() http.Handler {
	return a
}

func (a *API) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	requestID := idgen.New("req")
	tracked := &responseWriter{ResponseWriter: writer}
	uiRequest := a.uiHandler != nil && request != nil && request.URL != nil &&
		!reservedAPIPath(request.URL.Path)
	if uiRequest {
		setUISecurityHeaders(tracked.Header(), requestID)
	} else {
		setSecurityHeaders(tracked.Header(), requestID)
	}
	defer func() {
		if recover() != nil && !tracked.wrote {
			if uiRequest {
				writeUIError(tracked, request,
					apperror.New(apperror.CodeInternal, "internal server error"), 0)
			} else {
				a.writeError(tracked, requestID,
					apperror.New(apperror.CodeInternal, "internal server error"), 0)
			}
		}
	}()

	if status, err := validateRequestBoundary(request); err != nil {
		if uiRequest {
			writeUIError(tracked, request, err, status)
		} else {
			a.writeError(tracked, requestID, err, status)
		}
		return
	}
	if uiRequest {
		a.serveUI(tracked, request)
		return
	}
	if request.URL.Path == TaskSkillCandidatesPath {
		a.serveTaskSkillCandidates(tracked, request, requestID)
		return
	}
	if request.URL.Path == TaskConfigurationPreviewPath {
		a.serveTaskConfiguration(tracked, request, requestID, "")
		return
	}
	if runID, matched := matchRunTaskConfigurationPath(request.URL.Path); matched {
		a.serveTaskConfiguration(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchProjectSkillSelectionPath(request.URL.Path); matched {
		a.serveProjectSkillSelection(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchInstalledSkillSelectionPath(request.URL.Path); matched {
		a.serveInstalledSkillSelection(tracked, request, requestID, runID)
		return
	}
	if request.URL.Path == DockerEnvironmentPath {
		a.serveDockerEnvironment(tracked, request, requestID)
		return
	}
	if request.URL.Path == SandboxEnvironmentPath {
		a.serveSandboxEnvironment(tracked, request, requestID)
		return
	}
	if isDockerSandboxPath(request.URL.Path) {
		a.serveDockerSandbox(tracked, request, requestID)
		return
	}
	if runID, operationID, cancel, matched := matchProjectPreparationPath(request.URL.Path); matched {
		a.serveProjectPreparation(tracked, request, requestID, runID, operationID, cancel)
		return
	}
	if runID, slot, jobID, matched := matchProjectActionsPath(request.URL.Path); matched {
		a.serveProjectActions(tracked, request, requestID, runID, slot, jobID)
		return
	}
	if threadID, jobID, stop, matched := matchThreadApplicationServicesPath(request.URL.Path); matched {
		a.serveThreadApplicationServices(tracked, request, requestID, threadID, jobID, stop)
		return
	}
	if route, matched := matchUIEvidencePath(request.URL.Path); matched {
		a.serveUIEvidence(tracked, request, requestID, route)
		return
	}
	if runID, action, matched := matchWorkspaceCheckpointPath(request.URL.Path); matched {
		a.serveWorkspaceCheckpoint(tracked, request, requestID, runID, action)
		return
	}
	if runID, agentID, attemptID, matched := matchSpecialistExecutionPath(request.URL.Path); matched {
		a.serveSpecialistExecution(tracked, request, requestID, runID, agentID, attemptID)
		return
	}
	if runID, matched := matchStandardCodeDeliveryPath(request.URL.Path); matched {
		a.serveStandardCodeDelivery(tracked, request, requestID, runID)
		return
	}
	if runID, action, matched := matchGitAdvancedPath(request.URL.Path); matched {
		a.serveGitAdvanced(tracked, request, requestID, runID, action)
		return
	}
	if threadID, remaining, matched := matchThreadGitPath(request.URL.Path); matched {
		a.routeThreadGit(tracked, request, requestID, threadID, remaining)
		return
	}
	if route, matched := matchGitHubReviewPath(request.URL.Path); matched {
		a.serveGitHubReview(tracked, request, requestID, route)
		return
	}
	if route, matched := matchThreadPullRequestPath(request.URL.Path); matched {
		a.serveThreadPullRequest(tracked, request, requestID, route)
		return
	}
	if route, matched := matchPRFollowupPath(request.URL.Path); matched {
		a.servePRFollowup(tracked, request, requestID, route)
		return
	}
	if request.Method != http.MethodGet && isContextContinuityMutationPath(request.URL.Path) {
		a.serveContextContinuityMutation(tracked, request, requestID)
		return
	}
	if request.URL.Path == ExtensionHookDiagnosticsPath {
		a.servePluginLifecycle(tracked, request, requestID, "", "hooks")
		return
	}
	if id, action, matched := matchPluginLifecyclePath(request.URL.Path); matched {
		a.servePluginLifecycle(tracked, request, requestID, id, action)
		return
	}
	if request.URL.Path == ExtensionMCPRegistrationPath || request.URL.Path == ExtensionPluginImportPath {
		a.serveExtensionOnboarding(tracked, request, requestID)
		return
	}
	if identity, matched := matchMCPCredentialPath(request.URL.Path); matched {
		a.serveMCPCredential(tracked, request, requestID, identity)
		return
	}
	if identity, kind, matched := matchCodeIntelConfigurationMutationPath(request.URL.Path); matched {
		a.serveCodeIntelConfigurationMutation(tracked, request, requestID, identity, kind)
		return
	}
	if runID, inputID, matched := matchMCPInputPath(request.URL.Path); matched {
		a.serveMCPInput(tracked, request, requestID, runID, inputID)
		return
	}
	if identity, kind, matched := matchExtensionMutationPath(request.URL.Path); matched {
		a.serveExtensionMutation(tracked, request, requestID, identity, kind)
		return
	}
	if request.URL.Path == WorkspaceImportPath {
		a.serveWorkspaceImport(tracked, request, requestID)
		return
	}
	if workspaceID, attachmentID, content, matched := matchWorkspaceFileAttachmentPath(request.URL.Path); matched {
		a.serveWorkspaceFileAttachment(tracked, request, requestID, workspaceID, attachmentID, content)
		return
	}
	if workspaceID, imageID, content, matched := matchWorkspaceImagePath(request.URL.Path); matched {
		a.serveWorkspaceImage(tracked, request, requestID, workspaceID, imageID, content)
		return
	}
	if request.URL.Path == "/api/v1/runs" && request.Method != http.MethodGet {
		a.serveRunCreationControl(tracked, request, requestID)
		return
	}
	if request.URL.Path == ThreadCollectionPath && request.Method != http.MethodGet {
		a.serveThreadCreationControl(tracked, request, requestID)
		return
	}
	if threadID, matched := matchProviderSearchReadinessPath(request.URL.Path); matched {
		a.serveProviderSearchReadiness(tracked, request, requestID, threadID)
		return
	}
	if threadID, matched := matchSearchDiagnosticsPath(request.URL.Path); matched {
		a.serveSearchDiagnostics(tracked, request, requestID, threadID)
		return
	}
	if threadID, matched := matchThreadModelRoutePath(request.URL.Path); matched {
		a.serveThreadModelRoute(tracked, request, requestID, threadID)
		return
	}
	if threadID, matched := matchThreadPlanControlPath(request.URL.Path); matched && request.Method != http.MethodGet {
		a.serveThreadPlanControl(tracked, request, requestID, threadID)
		return
	}
	if threadID, action, matched := matchThreadMutationPath(request.URL.Path); matched &&
		request.Method != http.MethodGet {
		a.serveThreadMutationControl(tracked, request, requestID, threadID, action)
		return
	}
	if sessionID, matched := matchSessionArchiveControlPath(request.URL.Path); matched {
		a.serveSessionArchiveControl(tracked, request, requestID, sessionID)
		return
	}
	if sessionID, matched := matchSessionMessageControlPath(request.URL.Path); matched &&
		request.Method != http.MethodGet {
		a.serveSessionMessageControl(tracked, request, requestID, sessionID)
		return
	}
	if sessionID, operationKey, matched := matchSessionMessageOperationObservationPath(request.URL.Path); matched {
		a.serveSessionMessageOperationObservation(tracked, request, requestID, sessionID, operationKey)
		return
	}
	if sessionID, messageID, matched := matchSessionSteeringCancellationPath(request.URL.Path); matched {
		a.serveSessionSteeringCancellation(tracked, request, requestID, sessionID, messageID)
		return
	}
	if sessionID, messageID, matched := matchSessionSteeringPromotionPath(request.URL.Path); matched {
		a.serveSessionSteeringPromotion(tracked, request, requestID, sessionID, messageID)
		return
	}
	if sessionID, messageID, matched := matchSessionSteeringRevisionPath(request.URL.Path); matched {
		a.serveSessionSteeringRevision(tracked, request, requestID, sessionID, messageID)
		return
	}
	if runID, action, matched := matchStandardCodePresetControlPath(request.URL.Path); matched {
		a.serveStandardCodePresetControl(tracked, request, requestID, runID, action)
		return
	}
	if runID, matched := matchRunLifecycleControlPath(request.URL.Path); matched {
		a.serveRunLifecycleControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchRunWebAccessControlPath(request.URL.Path); matched {
		a.serveRunWebAccessControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchRunNetworkAuthorityControlPath(request.URL.Path); matched {
		a.serveRunNetworkAuthorityControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchPlanDirectionControlPath(request.URL.Path); matched {
		a.servePlanDirectionControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchPlanModeControlPath(request.URL.Path); matched {
		a.servePlanModeControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchPlanDeliveryControlPath(request.URL.Path); matched {
		a.servePlanDeliveryControl(tracked, request, requestID, runID)
		return
	}
	if runID, itemID, action, matched := matchPlanDeliveryWorkItemControlPath(request.URL.Path); matched {
		a.servePlanDeliveryWorkItemControl(tracked, request, requestID, runID, itemID, action)
		return
	}
	if runID, approvalID, matched := matchApprovalDecisionControlPath(request.URL.Path); matched {
		a.serveApprovalDecisionControl(tracked, request, requestID, runID, approvalID)
		return
	}
	if runID, proposalID, route, matched :=
		matchControlledCommandProposalPath(request.URL.Path); matched {
		a.serveControlledCommandProposal(
			tracked, request, requestID, runID, proposalID, route)
		return
	}
	if runID, proposalID, route, matched :=
		matchHostCommandProposalPath(request.URL.Path); matched {
		a.serveHostCommandProposal(
			tracked, request, requestID, runID, proposalID, route)
		return
	}
	if request.URL.Path != AvailableModelRoutesPath {
		if action, matched := matchChatGPTAuthControlPath(request.URL.Path); matched {
			a.serveChatGPTAuthControl(tracked, request, requestID, action)
			return
		}
		if route, kind, matched := matchModelControlPath(request.URL.Path); matched {
			a.serveModelControl(tracked, request, requestID, route, kind)
			return
		}
	}
	if request.URL.Path == ProviderModelDiscoveryPath {
		a.serveProviderModelDiscovery(tracked, request, requestID)
		return
	}
	if provider, remove, matched := matchProviderDefinitionControlPath(request.URL.Path); matched {
		a.serveProviderDefinitionControl(tracked, request, requestID, provider, remove)
		return
	}
	if request.URL.Path == PriceSnapshotsPath {
		a.servePriceSnapshots(tracked, request, requestID)
		return
	}
	if provider, matched := matchProviderCredentialControlPath(request.URL.Path); matched {
		a.serveProviderCredentialControl(tracked, request, requestID, provider)
		return
	}
	if runID, matched := matchFileEditProposalControlPath(request.URL.Path); matched {
		a.serveFileEditProposalControl(tracked, request, requestID, runID)
		return
	}
	if runID, sourceEditID, matched := matchFileEditRevertProposalControlPath(request.URL.Path); matched {
		a.serveFileEditRevertProposalControl(tracked, request, requestID, runID, sourceEditID)
		return
	}
	if runID, editID, matched := matchFileEditReviewControlPath(request.URL.Path); matched {
		a.serveFileEditReviewControl(tracked, request, requestID, runID, editID)
		return
	}
	if runID, editID, matched := matchFileEditApplyControlPath(request.URL.Path); matched {
		a.serveFileEditApplyControl(tracked, request, requestID, runID, editID)
		return
	}
	if runID, cancel, matched := matchRunWakeControlPath(request.URL.Path); matched &&
		request.Method != http.MethodGet {
		a.serveRunWakeControl(tracked, request, requestID, runID, cancel)
		return
	}
	if request.Method != http.MethodGet {
		if runID, jobID, action, matched := matchScheduledJobMutationPath(request.URL.Path); matched {
			a.serveScheduledJobControl(tracked, request, requestID, runID, jobID, action)
			return
		}
	}
	if runID, matched := matchRunWakeExecutionPath(request.URL.Path); matched {
		a.serveRunWakeExecutionControl(tracked, request, requestID, runID)
		return
	}
	if request.URL.Path == SkillCatalogPublishersPath {
		a.serveSkillCatalogPublishers(tracked, request, requestID)
		return
	}
	if request.URL.Path == SkillPackageInstallPath {
		a.serveSkillPackageInstallControl(tracked, request, requestID)
		return
	}
	if request.URL.Path == SkillPackageRemovePath {
		a.serveSkillPackageRemoveControl(tracked, request, requestID)
		return
	}
	if runID, matched := matchEvidenceAttachmentPath(request.URL.Path); matched &&
		request.Method != http.MethodGet {
		a.serveEvidenceAttachmentControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchVerificationEvidencePath(request.URL.Path); matched &&
		request.Method != http.MethodGet {
		a.serveVerificationEvidenceControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchVerificationPlanPath(request.URL.Path); matched &&
		request.Method != http.MethodGet {
		a.serveVerificationPlanControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchVerificationAssociationPath(request.URL.Path); matched {
		a.serveVerificationAssociationControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchVerificationSnapshotReceiptPath(request.URL.Path); matched &&
		request.Method != http.MethodGet {
		a.serveVerificationSnapshotReceiptControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchVerificationSnapshotReceiptReviewPath(request.URL.Path); matched &&
		request.Method != http.MethodGet {
		a.serveVerificationSnapshotReceiptReviewControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchEmbeddedAnalyzerExecutionPath(request.URL.Path); matched {
		a.serveEmbeddedAnalyzerExecutionControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchRunExecutionControlPath(request.URL.Path); matched {
		a.serveRunExecutionControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchRunExecutionProfileControlPath(request.URL.Path); matched {
		a.serveRunExecutionProfileControl(tracked, request, requestID, runID)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/agent-browser/screenshot") {
		if runID, _, matched := matchAgentBrowserPath(strings.TrimSuffix(request.URL.Path, "/screenshot")); matched {
			a.serveAgentBrowserScreenshot(tracked, request, requestID, runID)
			return
		}
	}
	if runID, closeSession, matched := matchAgentBrowserPath(request.URL.Path); matched {
		a.serveAgentBrowser(tracked, request, requestID, runID, closeSession)
		return
	}
	if runID, image, matched := matchFullCDPPreviewPath(request.URL.Path); matched {
		a.serveFullCDPPreview(tracked, request, requestID, runID, image)
		return
	}
	if runID, closeSession, matched :=
		matchFullCDPSessionControlPath(request.URL.Path); matched {
		a.serveFullCDPSessionControl(
			tracked, request, requestID, runID, closeSession)
		return
	}
	if runID, matched := matchRunExecutionPermissionControlPath(request.URL.Path); matched {
		a.serveRunExecutionPermissionControl(tracked, request, requestID, runID)
		return
	}
	if threadID, matched := matchThreadExecutionPermissionControlPath(request.URL.Path); matched {
		a.serveThreadExecutionPermissionControl(tracked, request, requestID, threadID)
		return
	}
	if runID, matched := matchRunBrowserCDPPermissionControlPath(request.URL.Path); matched {
		a.serveRunBrowserCDPPermissionControl(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchRunExecutionInteractionControlPath(request.URL.Path); matched {
		a.serveRunExecutionInteractionControl(tracked, request, requestID, runID)
		return
	}
	if runID, agentID, matched := matchSpecialistModelCancellationPath(request.URL.Path); matched {
		a.serveSpecialistModelCancellation(tracked, request, requestID, runID, agentID)
		return
	}
	if runID, matched := matchModelCancellationPath(request.URL.Path); matched {
		a.serveModelCancellation(tracked, request, requestID, runID)
		return
	}
	if runID, executionID, matched := matchFanoutExecutionCancelPath(request.URL.Path); matched {
		a.serveFanoutExecutionCancel(tracked, request, requestID, runID, executionID)
		return
	}
	if runID, proposalID, matched := matchChildTaskProposalReviewPath(request.URL.Path); matched {
		a.serveChildTaskProposalReview(tracked, request, requestID, runID, proposalID)
		return
	}
	if runID, proposalID, matched := matchChildTaskProposalAdmitPath(request.URL.Path); matched {
		a.serveChildTaskProposalAdmit(tracked, request, requestID, runID, proposalID)
		return
	}
	if request.Method != http.MethodGet {
		if runID, planID, action, ordinal, matched :=
			matchBatchDeliveryMutationPath(request.URL.Path); matched {
			a.serveBatchDeliveryControl(tracked, request, requestID,
				runID, planID, action, ordinal)
			return
		}
	}
	if !a.authorized(request, a.tokenHash) {
		tracked.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
		a.writeError(tracked, requestID,
			apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"),
			http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodGet {
		tracked.Header().Set("Allow", http.MethodGet)
		a.writeError(tracked, requestID,
			apperror.New(apperror.CodeInvalidArgument, "HTTP API endpoint only supports GET"),
			http.StatusMethodNotAllowed)
		return
	}
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
		a.writeError(tracked, requestID,
			apperror.New(apperror.CodeInvalidArgument, "read-only HTTP API requests cannot contain a body"), 0)
		return
	}
	if request.URL.Path == OpenAPIPath {
		if err := rejectQuery(request.URL.Query()); err != nil {
			a.writeError(tracked, requestID, err, 0)
			return
		}
		a.writeOpenAPI(tracked, requestID)
		return
	}
	if runID, matched := matchRunEventStreamPath(request.URL.Path); matched {
		if err := validatePathIdentity(runID); err != nil {
			a.writeError(tracked, requestID, err, 0)
			return
		}
		a.serveRunEventStream(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchRunEventPollPath(request.URL.Path); matched {
		if err := validatePathIdentity(runID); err != nil {
			a.writeError(tracked, requestID, err, 0)
			return
		}
		a.serveRunEventPoll(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchPublicModelStreamPollPath(request.URL.Path); matched {
		a.servePublicModelStreamPoll(tracked, request, requestID, runID)
		return
	}
	if runID, matched := matchPublicModelStreamPath(request.URL.Path); matched {
		a.servePublicModelStream(tracked, request, requestID, runID)
		return
	}
	data, page, err := a.route(request)
	if err != nil {
		a.writeError(tracked, requestID, err, 0)
		return
	}
	a.writeSuccess(tracked, requestID, data, page)
}

func reservedAPIPath(value string) bool {
	return value == "/api" || strings.HasPrefix(value, "/api/")
}

func (a *API) serveUI(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		writeUIError(writer, request,
			apperror.New(apperror.CodeInvalidArgument, "Web UI only supports GET and HEAD"),
			http.StatusMethodNotAllowed)
		return
	}
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
		writeUIError(writer, request,
			apperror.New(apperror.CodeInvalidArgument, "Web UI requests cannot contain a body"), 0)
		return
	}
	if request.URL.RawQuery != "" {
		writeUIError(writer, request,
			apperror.New(apperror.CodeInvalidArgument, "Web UI requests cannot contain query parameters"), 0)
		return
	}
	if len(request.Header.Values("Authorization")) != 0 {
		writeUIError(writer, request,
			apperror.New(apperror.CodeInvalidArgument, "Web UI asset requests cannot contain authorization"), 0)
		return
	}
	a.uiHandler.ServeHTTP(writer, request)
}

func validateRequestBoundary(request *http.Request) (int, error) {
	if request == nil || request.URL == nil {
		return 0, apperror.New(apperror.CodeInvalidArgument, "HTTP request is required")
	}
	if len(request.RequestURI) > MaxRequestTargetBytes {
		return http.StatusRequestURITooLong,
			apperror.New(apperror.CodeResourceExhausted, "HTTP request target exceeds its limit")
	}
	if len(request.URL.RawQuery) > MaxQueryBytes {
		return http.StatusRequestURITooLong,
			apperror.New(apperror.CodeResourceExhausted, "HTTP query exceeds its limit")
	}
	if request.URL.Path == "" || request.URL.Path != path.Clean(request.URL.Path) ||
		strings.Contains(request.URL.Path, `\`) {
		return 0, apperror.New(apperror.CodeInvalidArgument, "HTTP path is not canonical")
	}
	if !loopbackRequestHost(request.Host) {
		return 0, apperror.New(apperror.CodePolicyDenied, "HTTP Host must identify a loopback address")
	}
	remoteHost, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil || !loopbackHost(remoteHost) {
		return 0, apperror.New(apperror.CodePolicyDenied, "HTTP client must connect from loopback")
	}
	return 0, nil
}

func loopbackRequestHost(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		host = value
		if strings.Contains(strings.Trim(host, "[]"), ":") && net.ParseIP(strings.Trim(host, "[]")) == nil {
			return false
		}
	}
	return loopbackHost(host)
}

func (a *API) authorized(request *http.Request, tokenHash [sha256.Size]byte) bool {
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	parts := strings.SplitN(strings.TrimSpace(values[0]), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return false
	}
	candidate := sha256.Sum256([]byte(parts[1]))
	return subtle.ConstantTimeCompare(candidate[:], tokenHash[:]) == 1
}

func setSecurityHeaders(header http.Header, requestID string) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-CyberAgent-API-Version", Version)
	header.Set("X-Request-ID", requestID)
}

func setUISecurityHeaders(header http.Header, requestID string) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; connect-src 'self'; "+
		"font-src 'self'; form-action 'none'; frame-ancestors 'none'; img-src 'self' data: blob:; "+
		"manifest-src 'self'; media-src data:; object-src 'none'; script-src 'self'; style-src 'self'; worker-src 'self'")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-CyberAgent-UI-Version", "web-ui.v1")
	header.Set("X-Request-ID", requestID)
}

func writeUIError(writer http.ResponseWriter, request *http.Request, err error, statusOverride int) {
	status := statusOverride
	if status == 0 {
		status = apperror.HTTPStatus(apperror.Normalize(err))
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(status)
	if request == nil || request.Method != http.MethodHead {
		_, _ = io.WriteString(writer, http.StatusText(status)+"\n")
	}
}

type Server struct {
	httpServer     *http.Server
	cancelRequests context.CancelFunc
}

func NewServer(api *API, errorLog *log.Logger) (*Server, error) {
	if api == nil {
		return nil, errors.New("HTTP API is required")
	}
	if errorLog == nil {
		errorLog = log.New(io.Discard, "", 0)
	}
	requestContext, cancelRequests := context.WithCancel(context.Background())
	return &Server{httpServer: &http.Server{
		Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 * 1024,
		ErrorLog: errorLog, BaseContext: func(net.Listener) context.Context { return requestContext },
	}, cancelRequests: cancelRequests}, nil
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if s == nil || s.httpServer == nil || s.cancelRequests == nil || listener == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "HTTP API server and listener are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer s.cancelRequests()
	done := make(chan error, 1)
	go func() { done <- s.httpServer.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return apperror.Wrap(apperror.CodeUnavailable, "HTTP API server stopped", err)
	case <-ctx.Done():
		s.cancelRequests()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := s.httpServer.Shutdown(shutdownCtx)
		serveErr := <-done
		if shutdownErr != nil {
			return apperror.Wrap(apperror.CodeUnavailable, "HTTP API shutdown failed", shutdownErr)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return apperror.Wrap(apperror.CodeUnavailable, "HTTP API server stopped", serveErr)
		}
		return nil
	}
}

type responseWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *responseWriter) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
