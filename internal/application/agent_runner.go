package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/standardcodedelivery"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/waitgraph"
	"cyberagent-workbench/internal/webevidence"
)

const maxSupervisorHistoryMessages = 20

const maxSupervisorWorkItems = 20

const maxSupervisorWorkBoardRunes = 16 * 1024

const maxSupervisorNotes = 100

const maxSupervisorMemoryTokens = 8 * 1024

const maxModelRetryAttempts = 5

const maxProtocolRepairReasonChars = 1024

const maxModelCancellationPollInterval = 5 * time.Second

type AgentTurnStore interface {
	BeginSupervisorTurn(ctx context.Context, lease domain.RunExecutionLease, pendingInput string) (domain.SupervisorTurn, error)
	BeginSupervisorSteeringTurn(ctx context.Context,
		lease domain.RunExecutionLease) (domain.SupervisorTurn, error)
	BeginSupervisorSteeringTurnForMessage(ctx context.Context,
		lease domain.RunExecutionLease, messageID string) (domain.SupervisorTurn, error)
	BindSupervisorTurnInput(ctx context.Context, checkpoint domain.SupervisorCheckpoint, input string) (domain.SupervisorCheckpoint, error)
	NextSupervisorModelAttempt(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
		protocolRepair int, toolRound int) (int, int, error)
	RecordSupervisorModelStarted(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt) (bool, error)
	RecordSupervisorModelCancelRequested(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, reason string) (bool, error)
	ObserveSupervisorModelCancellation(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt) (domain.ModelCancellation, bool, error)
	RecordSupervisorModelDelta(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, delta llm.ModelDelta) (bool, error)
	RecordSupervisorModelPublicCommentary(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, commentary domain.ModelPublicCommentary) (bool, error)
	RecordSupervisorModelCompleted(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, response llm.ChatResponse) (domain.SupervisorCheckpoint, error)
	RecordSupervisorModelFailed(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt) (domain.SupervisorCheckpoint, error)
	RecordSupervisorProtocolFailure(ctx context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, response llm.ChatResponse, reason string, requestRepair bool) (domain.SupervisorCheckpoint, error)
	CompleteSupervisorTurn(ctx context.Context, checkpoint domain.SupervisorCheckpoint, response llm.ChatResponse, action domain.RootAction, decision policy.Decision, elapsed time.Duration) (domain.Run, domain.SupervisorCheckpoint, session.TurnMessages, error)
	FailSupervisorTurn(ctx context.Context, checkpoint domain.SupervisorCheckpoint, cause string, elapsed time.Duration) (domain.SupervisorCheckpoint, error)
	FinalizeSupervisorRun(ctx context.Context, lease domain.RunExecutionLease, target domain.RunStatus, summary string) (domain.Run, domain.SupervisorCheckpoint, error)
	GetSupervisorCheckpoint(ctx context.Context, runID string) (domain.SupervisorCheckpoint, bool, error)
	GetRun(ctx context.Context, id string) (domain.Run, error)
	GetRunProgressGuard(ctx context.Context, runID string) (domain.RunProgressGuard, bool, error)
	GetOperatorSteeringQueueSummary(ctx context.Context,
		runID string) (domain.OperatorSteeringQueueSummary, error)
	GetRunMode(ctx context.Context, runID string) (domain.RunModeSnapshot, error)
	ListSessionMessages(ctx context.Context, sessionID string, includeCompacted bool) ([]session.Message, error)
	LatestContextSummary(ctx context.Context, taskID string) (contextmgr.Summary, bool, error)
	ListWorkItems(ctx context.Context, filter domain.WorkItemFilter) ([]domain.WorkItem, error)
	ListNotes(ctx context.Context, filter domain.NoteFilter) ([]domain.Note, error)
	PrepareRootInboxContext(ctx context.Context,
		checkpoint domain.SupervisorCheckpoint) (domain.RootInboxContextBatch, error)
	GetSkillSelectionByRun(ctx context.Context, runID string) (skills.Selection, bool, error)
	PrepareRootSkillContext(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
		request skills.RootContextPreparationRequest) (skills.RootContextPreparation, error)
	ListSupervisorToolRounds(ctx context.Context, checkpoint domain.SupervisorCheckpoint) ([]domain.SupervisorToolRound, error)
	RecordSupervisorToolResult(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
		result domain.SupervisorToolResult) (domain.SupervisorToolCall, bool, error)
	RecordSupervisorToolExecutionStarted(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
		callID string) (bool, error)
}

// supervisorRootActionRecoveryStore is optional so alternate AgentTurnStore
// implementations keep their existing contract. The production store uses it
// to attach bounded compatibility metadata to the same durable model.completed
// event as the recovered response.
type supervisorRootActionRecoveryStore interface {
	RecordSupervisorModelCompletedWithRootActionRecovery(ctx context.Context,
		checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt,
		response llm.ChatResponse, discardedTrailingBytes int) (
		domain.SupervisorCheckpoint, error)
}

type supervisorAgentAttributionStore interface {
	RecordSupervisorModelCompletedForAgent(context.Context,
		domain.SupervisorCheckpoint, llm.ModelAttempt, llm.ChatResponse,
		domain.AgentAttribution) (domain.SupervisorCheckpoint, error)
}

type externalRootSkillContextStore interface {
	GetExternalSkillSelectionByRun(ctx context.Context, runID string) (
		skills.ExternalSelection, bool, error)
	PrepareExternalRootSkillContext(ctx context.Context,
		checkpoint domain.SupervisorCheckpoint,
		request skills.ExternalRootContextPreparationRequest) (
		skills.ExternalRootContextPreparation, error)
}

type RunExecutionLeaseStore interface {
	AcquireRunExecutionLease(ctx context.Context, request domain.AcquireRunExecutionLeaseRequest) (domain.RunExecutionLeaseAcquisition, error)
	RenewRunExecutionLease(ctx context.Context, expected domain.RunExecutionLease, ttl time.Duration) (domain.RunExecutionLease, error)
	ReleaseRunExecutionLease(ctx context.Context, expected domain.RunExecutionLease) (domain.RunExecutionLease, bool, error)
	GetRunExecutionLease(ctx context.Context, runID string) (domain.RunExecutionLease, bool, error)
}

type AgentRunnerStore interface {
	GetMission(context.Context, string) (domain.Mission, error)
	GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error)
	GetRunExecutionInteraction(context.Context, string) (domain.RunExecutionInteractionSnapshot, error)
	GetRunExecutionProfile(context.Context, string) (domain.RunExecutionProfileSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
	AgentTurnStore
	RunExecutionLeaseStore
	StructuredMemoryMutationStore
	SpecialistDelegationMutationStore
	PlanDeliveryProposalMutationStore
	SkillCandidateMutationStore
	toolgateway.Store
}

type RunHandle struct {
	RunID     string
	MissionID string
	SessionID string
}

type LifecycleStatus string

const (
	LifecycleTurnCompleted LifecycleStatus = "turn_completed"
	LifecycleTurnFailed    LifecycleStatus = "turn_failed"
)

type LifecycleResult struct {
	Handle                  RunHandle
	AgentID                 string
	Status                  LifecycleStatus
	Turn                    int
	AttemptID               string
	Recovered               bool
	Text                    string
	Provider                string
	Model                   string
	Usage                   llm.Usage
	RequestedAction         domain.RootActionKind
	Action                  domain.RootAction
	RunStatus               domain.RunStatus
	UserMessage             session.Message
	ReplyMessage            session.Message
	Checkpoint              domain.SupervisorCheckpoint
	ModelAttempts           int
	ProtocolRepairs         int
	StreamEvents            int
	StreamBytes             int
	ToolRounds              int
	ToolCalls               int
	ToolBoundary            bool
	InboxMessages           int
	InboxRecovered          bool
	SkillItems              int
	SkillTokens             int
	SkillBudget             int
	SkillRedactions         int
	SkillRecovered          bool
	ExternalSkillItems      int
	ExternalSkillTokens     int
	ExternalSkillBudget     int
	ExternalSkillRedactions int
	ExternalSkillRecovered  bool
	LongTermMemoryItems     int
	ContextCompacted        bool
	ContextSummaryID        int64
	ModelOutcome            llm.Outcome
}

type LifecycleOutcome string

const (
	LifecycleOutcomeCompleted LifecycleOutcome = "completed"
	LifecycleOutcomeFailed    LifecycleOutcome = "failed"
)

type FinalizationResult struct {
	Run        domain.Run
	Checkpoint domain.SupervisorCheckpoint
	Outcome    LifecycleOutcome
	Summary    string
}

type ExecutionResult struct {
	RunID      string
	Steps      []LifecycleResult
	StopReason string
	RunStatus  domain.RunStatus
}

type AgentRunner struct {
	generatedContextCompactionEnabled     bool
	historyRecallEnabled                  bool
	store                                 AgentRunnerStore
	router                                *llm.Router
	checker                               policy.Checker
	retryPolicy                           ModelRetryPolicy
	activeCalls                           *ActiveCallRegistry
	monetary                              *MonetaryBudgetService
	tools                                 *toolgateway.Gateway
	leaseOwner                            string
	leasePolicy                           RunExecutionLeasePolicy
	cancellationPollInterval              time.Duration
	skillRegistry                         *skills.Registry
	skillRegistryErr                      error
	waitGraph                             *waitgraph.Graph
	debugTerminalEnabled                  bool
	commandRuntime                        toolgateway.CommandRuntimeAdvertiser
	mcpClient                             AgentRunnerMCPClient
	executionCapabilities                 domain.ExecutionPermissionRuntimeCapabilities
	codeIntel                             *codeintel.Manager
	lifecycleHooks                        *hooks.Engine
	webEvidence                           *webevidence.Service
	webFetchAuthorizationSchedulerEnabled bool
	browserActions                        *FullCDPProductionService
	agentBrowser                          *AgentBrowserService
	standardCodeDelivery                  *StandardCodeDeliveryService
	drydocks                              *RunWorktreeService
}

func NewAgentRunner(store AgentRunnerStore, router *llm.Router, checker policy.Checker) *AgentRunner {
	skillRegistry, skillRegistryErr := skills.BuiltinRegistry()
	gateway := toolgateway.New(store, checker).
		WithStructuredMemoryExecutor(NewStructuredMemoryToolExecutor(store)).
		WithSpecialistDelegationExecutor(NewSpecialistDelegationToolExecutor(store)).
		WithSkillCandidateExecutor(NewSkillCandidateToolExecutor(store))
	if reader, ok := store.(builtinSkillReadStore); ok && skillRegistryErr == nil {
		gateway.WithSkillReadExecutor(&builtinSkillReader{reader, skillRegistry})
	}
	if childTaskStore, ok := store.(ChildTaskMutationStore); ok {
		gateway.WithChildTaskProposalExecutor(NewChildTaskToolExecutor(childTaskStore))
	}
	historyStore, historyRecallEnabled := store.(HistoryRecallToolStore)
	if historyRecallEnabled {
		gateway.WithHistoryRecallExecutor(NewHistoryRecallToolExecutor(historyStore))
	}
	if agentCodeStore, ok := store.(AgentCodeToolStore); ok {
		gateway.WithWorkspaceRootResolver(func(ctx context.Context, workspaceID string) (string, error) {
			registered, err := agentCodeStore.GetWorkspaceInfo(ctx, workspaceID)
			return registered.RootPath, err
		}).WithAgentCodeWorkspaceResolver(NewAgentCodeWorkspaceResolver(agentCodeStore, nil)).
			WithAgentCodeExecutor(NewAgentCodeToolExecutor(agentCodeStore, checker))
	}
	var webService *webevidence.Service
	if webStore, ok := any(store).(WebEvidenceToolStore); ok {
		webService = webEvidenceServiceForStore(webStore, "")
		if executor, err := NewWebEvidenceToolExecutor(webStore, webService); err == nil {
			gateway.WithWebEvidenceExecutor(executor)
		}
	}
	var monetary *MonetaryBudgetService
	if moneyStore, ok := store.(MonetaryBudgetStore); ok {
		monetary = NewMonetaryBudgetService(moneyStore)
	}
	return &AgentRunner{
		monetary:                          monetary,
		generatedContextCompactionEnabled: true,
		historyRecallEnabled:              historyRecallEnabled,
		store:                             store, router: router, checker: checker, retryPolicy: DefaultModelRetryPolicy(),
		activeCalls: NewActiveCallRegistry(), waitGraph: waitgraph.Default(),
		leaseOwner: idgen.New("worker"), leasePolicy: DefaultRunExecutionLeasePolicy(),
		cancellationPollInterval: 100 * time.Millisecond,
		tools: gateway.
			WithPlanDeliveryExecutor(NewPlanDeliveryToolExecutor(store)),
		skillRegistry: skillRegistry, skillRegistryErr: skillRegistryErr,
		webEvidence: webService,
	}
}

func (s *AgentRunner) WithStandardCodeDelivery(
	delivery *StandardCodeDeliveryService,
) *AgentRunner {
	if s != nil {
		s.standardCodeDelivery = delivery
		if delivery != nil {
			s.WithRunWorktree(delivery.drydocks)
		}
	}
	return s
}

func (s *AgentRunner) WithWebEvidence(service *webevidence.Service) *AgentRunner {
	if s == nil || s.tools == nil || service == nil {
		return s
	}
	store, ok := any(s.store).(WebEvidenceToolStore)
	if !ok {
		return s
	}
	executor, err := NewWebEvidenceToolExecutor(store, service)
	if err != nil {
		return s
	}
	executor.WithWebFetchAuthorizationScheduler(
		s.webFetchAuthorizationSchedulerEnabled)
	s.webEvidence = service
	s.tools.WithWebEvidenceExecutor(executor)
	return s
}

// WithWebFetchAuthorizationScheduler binds approval-mediated Web fetch
// advertisement and execution to the process-owned durable continuation
// worker. Directly authorized fetches remain available when this is false.
func (s *AgentRunner) WithWebFetchAuthorizationScheduler(
	enabled bool,
) *AgentRunner {
	if s == nil {
		return s
	}
	s.webFetchAuthorizationSchedulerEnabled = enabled
	if s.webEvidence != nil {
		s.WithWebEvidence(s.webEvidence)
	}
	return s
}

// WithBrowserActions installs only an already-owned Full CDP session service.
// The Supervisor can use a ready session but cannot open, close, or elevate it.
func (s *AgentRunner) WithBrowserActions(
	service *FullCDPProductionService,
) *AgentRunner {
	if s == nil || s.tools == nil || service == nil {
		return s
	}
	s.browserActions = service
	s.tools.WithBrowserActionExecutor(service)
	return s
}

func (s *AgentRunner) WithWaitGraph(graph *waitgraph.Graph) *AgentRunner {
	if s != nil && graph != nil {
		s.waitGraph = graph
		if s.tools != nil {
			s.tools.WithWaitGraph(graph)
		}
	}
	return s
}

// WithDockerSandboxProposalExecutor installs the model-facing adapter for the
// process-owned Docker Sandbox service. The executor can create an admission,
// but it cannot start or cancel a container.
func (s *AgentRunner) WithDockerSandboxProposalExecutor(
	executor toolgateway.DockerSandboxProposalExecutor,
) *AgentRunner {
	if s != nil && s.tools != nil && executor != nil {
		s.tools.WithDockerSandboxProposalExecutor(executor)
	}
	return s
}

// WithDebugTerminalAgentInput installs the model adapter for the existing
// user-owned terminal. Phase, permission, and short-lived operator lease
// checks remain independent gates.
func (s *AgentRunner) WithDebugTerminalAgentInput(
	controller DebugTerminalAgentInputController,
) *AgentRunner {
	if s == nil || s.tools == nil || controller == nil {
		return s
	}
	executor, err := NewDebugTerminalToolExecutor(controller)
	if err != nil {
		return s
	}
	s.tools.WithDebugTerminalExecutor(executor)
	s.debugTerminalEnabled = true
	return s
}

func (s *AgentRunner) WithCommandRuntime(
	executor toolgateway.CommandRuntimeExecutor,
) *AgentRunner {
	if s != nil && s.tools != nil && executor != nil {
		advertiser, ok := executor.(toolgateway.CommandRuntimeAdvertiser)
		if !ok {
			return s
		}
		s.tools.WithCommandRuntimeExecutor(executor)
		s.commandRuntime = advertiser
	}
	return s
}

func (s *AgentRunner) supervisorCommandRuntimeTools(ctx context.Context,
	runID string, permission domain.RunExecutionPermissionMode,
) (supervisorCommandRuntimeTools, error) {
	if s == nil || s.commandRuntime == nil {
		return supervisorCommandRuntimeTools{}, nil
	}
	adapter, available, err := s.commandRuntime.AdvertisedCommandRuntimeAdapter(
		ctx, runID, permission)
	if err != nil {
		return supervisorCommandRuntimeTools{}, apperror.Normalize(err)
	}
	if !available {
		return supervisorCommandRuntimeTools{}, nil
	}
	bound := commandruntimeadapter.NewAuthority(runID, adapter)
	if permission.IsApprovalMode() {
		snapshot, err := s.store.GetRunExecutionPermission(ctx, runID)
		if err != nil {
			return supervisorCommandRuntimeTools{}, err
		}
		snapshotID, generation, epoch, fence, live := bindAgentCodeRuntime(s.executionCapabilities, snapshot)
		if !live || snapshot.Mode != permission {
			return supervisorCommandRuntimeTools{}, nil
		}
		bound.ProtocolVersion = commandruntimeadapter.OperationAuthorityVersion
		bound.PermissionMode, bound.PermissionRevision = snapshot.Mode, snapshot.Revision
		bound.PermissionSnapshotID, bound.PermissionGeneration = snapshotID, generation
		bound.PermissionRuntimeEpoch, bound.RunAuthorizationFence = epoch, fence
	}
	authority, err := commandruntimeadapter.EncodeAuthority(bound)
	if err != nil {
		return supervisorCommandRuntimeTools{}, apperror.Wrap(apperror.CodeInternal,
			"command runtime adapter advertisement is invalid", err)
	}
	return supervisorCommandRuntimeTools{Adapter: adapter, Authority: authority}, nil
}

// WithMCPClient exposes operator-reviewed MCP tools within their host scope.
// Per-call consent and current execution authority are checked separately;
// registration staging and review remain operator control-plane operations.
func (s *AgentRunner) WithMCPClient(client AgentRunnerMCPClient) *AgentRunner {
	if s == nil || s.tools == nil || client == nil {
		return s
	}
	s.mcpClient = client
	s.installMCPExecutor()
	return s
}

func (s *AgentRunner) WithExecutionPermissionCapabilities(
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) *AgentRunner {
	if s == nil {
		return s
	}
	if capabilities.Validate() != nil {
		s.executionCapabilities = domain.ExecutionPermissionRuntimeCapabilities{}
	} else {
		s.executionCapabilities = capabilities
	}
	s.installMCPExecutor()
	if store, ok := s.store.(AgentCodeToolStore); ok && s.tools != nil {
		s.tools.WithAgentCodeExecutor(NewAgentCodeToolExecutor(store, s.checker).
			WithRunWorktree(s.drydocks).
			WithCodeIntel(s.codeIntel).
			WithExecutionPermissionCapabilities(s.executionCapabilities))
	}
	return s
}

func (s *AgentRunner) revokeRunRuntimeAuthority(runID string) {
	if s == nil {
		return
	}
	if s.executionCapabilities.RuntimeAuthority != nil {
		s.executionCapabilities.RuntimeAuthority.RevokeRun(runID)
	}
	if client, ok := s.mcpClient.(interface{ CloseRun(string) }); ok {
		client.CloseRun(runID)
	}
}

func (s *AgentRunner) installMCPExecutor() {
	if s == nil || s.tools == nil {
		return
	}
	if s.mcpClient == nil {
		s.tools.WithMCPExecutor(nil)
		return
	}
	executor, err := NewMCPClientToolExecutor(s.mcpClient, s.store,
		s.executionCapabilities)
	if err != nil {
		s.tools.WithMCPExecutor(nil)
		return
	}
	s.tools.WithMCPExecutor(executor)
}

// WithCodeIntel exposes only read-only semantic tools and reuses the Agent
// Code Root/Workspace/Phase authority. Server execution remains owned by the
// process-injected manager and cannot be configured by Workspace content.
func (s *AgentRunner) WithCodeIntel(manager *codeintel.Manager) *AgentRunner {
	if s == nil || s.tools == nil || manager == nil {
		return s
	}
	store, ok := s.store.(AgentCodeToolStore)
	if !ok {
		return s
	}
	s.codeIntel = manager
	s.tools.WithAgentCodeExecutor(NewAgentCodeToolExecutor(store, s.checker).
		WithRunWorktree(s.drydocks).WithCodeIntel(manager).
		WithExecutionPermissionCapabilities(s.executionCapabilities))
	s.tools.WithCodeIntelExecutor(NewCodeIntelToolExecutor(store, s.checker, manager).WithRunWorktree(s.drydocks))
	return s
}

func (s *AgentRunner) WithRunWorktree(drydocks *RunWorktreeService) *AgentRunner {
	if s == nil {
		return s
	}
	s.drydocks = drydocks
	if store, ok := s.store.(AgentCodeToolStore); ok && s.tools != nil {
		s.tools.WithAgentCodeWorkspaceResolver(NewAgentCodeWorkspaceResolver(store, drydocks)).
			WithAgentCodeExecutor(NewAgentCodeToolExecutor(store, s.checker).
				WithRunWorktree(drydocks).
				WithCodeIntel(s.codeIntel).
				WithExecutionPermissionCapabilities(s.executionCapabilities))
		if s.codeIntel != nil {
			s.tools.WithCodeIntelExecutor(NewCodeIntelToolExecutor(store, s.checker, s.codeIntel).WithRunWorktree(drydocks))
		}
	}
	return s
}

func (s *AgentRunner) WithLifecycleHooks(engine *hooks.Engine) *AgentRunner {
	if s != nil && s.tools != nil && engine != nil {
		s.lifecycleHooks = engine
		s.tools.WithLifecycleHooks(engine)
	}
	return s
}

type RunExecutionLeasePolicy struct {
	TTL           time.Duration
	RenewInterval time.Duration
}

func DefaultRunExecutionLeasePolicy() RunExecutionLeasePolicy {
	return RunExecutionLeasePolicy{TTL: 30 * time.Second, RenewInterval: 10 * time.Second}
}

func (p RunExecutionLeasePolicy) Validate() error {
	if err := domain.ValidateRunExecutionLeaseTTL(p.TTL); err != nil {
		return err
	}
	if p.RenewInterval <= 0 || p.RenewInterval >= p.TTL {
		return errors.New("run execution lease renewal interval must be positive and shorter than its TTL")
	}
	return nil
}

func (s *AgentRunner) WithRunExecutionLeasePolicy(policy RunExecutionLeasePolicy) *AgentRunner {
	if s != nil {
		s.leasePolicy = policy
	}
	return s
}

func (s *AgentRunner) WithRunExecutionLeaseOwner(ownerID string) *AgentRunner {
	if s != nil {
		s.leaseOwner = strings.TrimSpace(ownerID)
	}
	return s
}

type ModelRetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

func DefaultModelRetryPolicy() ModelRetryPolicy {
	return ModelRetryPolicy{MaxAttempts: 3, BaseDelay: 100 * time.Millisecond, MaxDelay: 2 * time.Second}
}

func (s *AgentRunner) WithModelRetryPolicy(policy ModelRetryPolicy) *AgentRunner {
	if s != nil {
		s.retryPolicy = normalizeModelRetryPolicy(policy)
	}
	return s
}

func (s *AgentRunner) WithActiveCalls(registry *ActiveCallRegistry) *AgentRunner {
	if s != nil && registry != nil {
		s.activeCalls = registry
	}
	return s
}

// WithMonetaryBudget overrides the gate installed from the durable store by
// default. A nil override leaves the existing gate intact.
func (s *AgentRunner) WithMonetaryBudget(service *MonetaryBudgetService) *AgentRunner {
	if s != nil && service != nil {
		s.monetary = service
	}
	return s
}

func (s *AgentRunner) WithSkillRegistry(registry *skills.Registry) *AgentRunner {
	if s != nil {
		s.skillRegistry = registry
		s.skillRegistryErr = nil
		if reader, ok := s.store.(builtinSkillReadStore); ok {
			if registry == nil {
				s.tools.WithSkillReadExecutor(nil)
			} else {
				s.tools.WithSkillReadExecutor(&builtinSkillReader{reader, registry})
			}
		}
		if registry == nil {
			s.skillRegistryErr = errors.New("skill registry is required")
		}
	}
	return s
}

func (s *AgentRunner) WithModelCancellationPollInterval(interval time.Duration) *AgentRunner {
	if s != nil {
		s.cancellationPollInterval = interval
	}
	return s
}

func (s *AgentRunner) Step(ctx context.Context, runID string) (LifecycleResult, error) {
	return s.step(ctx, runID, "")
}

func (s *AgentRunner) StepWithInput(ctx context.Context, runID string, input string) (LifecycleResult, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return LifecycleResult{}, apperror.New(apperror.CodeInvalidArgument, "supervisor input is required")
	}
	return s.step(ctx, runID, input)
}

func (s *AgentRunner) step(ctx context.Context, runID string, requestedInput string) (LifecycleResult, error) {
	if s == nil || s.store == nil || s.router == nil || s.checker == nil || s.activeCalls == nil || s.tools == nil {
		return LifecycleResult{}, apperror.New(apperror.CodeFailedPrecondition, "run supervisor dependencies are required")
	}
	if s.cancellationPollInterval <= 0 || s.cancellationPollInterval > maxModelCancellationPollInterval {
		return LifecycleResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"model cancellation poll interval must be positive and bounded")
	}
	runID = strings.TrimSpace(runID)
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return LifecycleResult{}, apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning {
		return LifecycleResult{}, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run %s is %s; supervisor requires running", run.ID, run.Status))
	}
	var result LifecycleResult
	err = s.withRunExecutionLease(ctx, run.ID, func(leaseCtx context.Context, lease domain.RunExecutionLease) error {
		var stepErr error
		result, stepErr = s.stepWithLease(leaseCtx, lease, requestedInput)
		return stepErr
	})
	return result, err
}

func (s *AgentRunner) stepWithLease(ctx context.Context, lease domain.RunExecutionLease,
	requestedInput string,
) (LifecycleResult, error) {
	return s.stepWithLeaseMode(ctx, lease, requestedInput, false, "")
}

func (s *AgentRunner) stepSteeringWithLease(ctx context.Context,
	lease domain.RunExecutionLease,
) (LifecycleResult, error) {
	return s.stepWithLeaseMode(ctx, lease, "", true, "")
}

func (s *AgentRunner) stepSteeringMessageWithLease(ctx context.Context,
	lease domain.RunExecutionLease, messageID string,
) (LifecycleResult, error) {
	return s.stepWithLeaseMode(ctx, lease, "", true, messageID)
}

func (s *AgentRunner) stepSegmentWithLeaseMode(ctx context.Context, lease domain.RunExecutionLease,
	requestedInput string, requireSteering bool, steeringMessageID string,
) (LifecycleResult, error) {
	var turn domain.SupervisorTurn
	var err error
	if requireSteering {
		if steeringMessageID == "" {
			turn, err = s.store.BeginSupervisorSteeringTurn(ctx, lease)
		} else {
			turn, err = s.store.BeginSupervisorSteeringTurnForMessage(ctx, lease,
				steeringMessageID)
		}
	} else {
		turn, err = s.store.BeginSupervisorTurn(ctx, lease, requestedInput)
	}
	if err != nil {
		return LifecycleResult{}, apperror.Normalize(err)
	}
	ctx = waitgraph.WithCurrent(ctx, waitgraph.Agent(turn.Agent.ID))
	result := LifecycleResult{
		Handle:  RunHandle{RunID: turn.Run.ID, MissionID: turn.Mission.ID, SessionID: turn.Run.SessionID},
		AgentID: turn.Agent.ID,
		Status:  LifecycleTurnFailed, Turn: turn.Checkpoint.NextTurn, AttemptID: turn.Checkpoint.AttemptID,
		Recovered: turn.Recovered, Checkpoint: turn.Checkpoint,
	}
	if err := ctx.Err(); err != nil {
		return result, apperror.Normalize(err)
	}
	input := turn.Checkpoint.PendingInput
	if !turn.Checkpoint.HasPendingInput() {
		input = supervisorTurnInput(turn.Mission.Goal, turn.Checkpoint.NextTurn)
		checkpoint, err := s.store.BindSupervisorTurnInput(ctx, turn.Checkpoint, input)
		if err != nil {
			failure := s.recordFailure(ctx, &result, err, 0)
			return result, failure
		}
		turn.Checkpoint = checkpoint
		result.Checkpoint = checkpoint
	}
	prepared, preparedResult, err := s.prepareAgentTurn(ctx, &turn, result, input)
	if err != nil {
		return preparedResult, err
	}
	result = preparedResult
	request := prepared.request
	baseRequest := request
	if len(prepared.toolRounds) > 0 {
		if err := requirePinnedProjectInstructionRequest(turn.Run.Config, baseRequest); err != nil {
			return result, s.recordFailure(ctx, &result, err, 0)
		}
		if err := s.requirePinnedProjectInstructionToolOrigins(ctx, turn, prepared.toolRounds); err != nil {
			return result, s.recordFailure(ctx, &result, err, 0)
		}
		var waitingApproval bool
		prepared.toolRounds, waitingApproval, err = s.resumeSupervisorTools(ctx, turn, prepared.toolRounds, prepared.tools.standardCode)
		if err != nil {
			return result, apperror.Normalize(err)
		}
		if waitingApproval {
			return supervisorApprovalWaitingResult(result, prepared.toolRounds), nil
		}
	}
	refreshStandardCodeSupervisorRequest(&baseRequest, prepared.tools.standardCode)
	request, err = s.requestWithSupervisorToolRounds(ctx, turn.Checkpoint, baseRequest, prepared.toolRounds)
	if err != nil {
		failure := s.recordFailure(ctx, &result, err, 0)
		return result, failure
	}
	result.ToolRounds, result.ToolCalls = supervisorToolStats(prepared.toolRounds)
	protocolRepair := 0
	repairReason := ""
	switch turn.Checkpoint.RepairPhase {
	case domain.ProtocolRepairPending:
		protocolRepair = 1
		repairReason = turn.Checkpoint.RepairReason
		result.ProtocolRepairs = 1
	case domain.ProtocolRepairExhausted:
		result.ProtocolRepairs = 1
		result.ModelOutcome = llm.OutcomeInvalidResponse
		failure := s.recordFailure(ctx, &result,
			apperror.New(apperror.CodeFailedPrecondition, "root lifecycle protocol repair was already exhausted: "+turn.Checkpoint.RepairReason), 0)
		return result, failure
	}

	// After durable history compaction cannot shrink the current segment's own
	// tool rounds enough, receipt the oldest completed rounds (one more each
	// retry) and rebuild from baseRequest so recent rounds stay native.
	receiptedRounds := 0
	var receiptPlan supervisorSegmentReceipt
	rebuildToolRequest := func() (llm.ChatRequest, error) {
		if receiptedRounds == 0 {
			return s.requestWithSupervisorToolRounds(ctx, turn.Checkpoint, baseRequest, prepared.toolRounds)
		}
		plan, err := minimalSupervisorSegmentReceiptPlan(prepared.toolRounds, receiptedRounds,
			turn.Run.SessionID, turn.Checkpoint.AttemptID)
		if err != nil {
			return llm.ChatRequest{}, err
		}
		rebuilt, err := supervisorRequestWithSegmentReceipt(baseRequest, plan,
			turn.Run.SessionID, turn.Checkpoint.AttemptID)
		if err != nil {
			return llm.ChatRequest{}, err
		}
		receiptPlan = plan
		return s.attachSupervisorProviderReplay(ctx, turn.Checkpoint, rebuilt, plan.NativeRounds)
	}

	rebuildHistoryRequest := func(compacted bool) error {
		prepared.context.history, prepared.context.summary, prepared.context.hasSummary, _, err = s.supervisorConversationContext(ctx, &turn)
		prepared.context.history = withoutCurrentMCPReferenceEvidence(prepared.context.history, prepared.input.mcpEvidenceIDs)
		result.Checkpoint = turn.Checkpoint
		if err == nil {
			prepared.context.memory, err = supervisorMemoryContextWithinBudget(prepared.context.memoryBudget, prepared.context.threadEndTurn, prepared.context.summary, prepared.context.hasSummary, prepared.context.workItems, prepared.context.notes, prepared.context.inbox.Messages,
				prepared.context.projectInstructionSections, prepared.context.longTermMemorySections, prepared.context.continuitySections, supervisorGoalContext(turn))
		}
		if err == nil {
			err = requireSupervisorContinuityContext(prepared.context.memory, prepared.context.summary, prepared.context.hasSummary, turn.Mission.ID, turn.Run.Config.ContinuityContextFingerprint)
		}
		if err == nil {
			err = requirePinnedProjectInstructionSelection(turn.Run.Config, prepared.context.memory)
		}
		if err != nil {
			return err
		}
		prepared.context.contextAudit = supervisorModelContextAudit(prepared.context.memory)
		if compacted {
			result.ContextCompacted, result.ContextSummaryID = true, prepared.context.summary.ID
		}
		baseRequest.Messages, prepared.layout = supervisorMessagesWithLayout(prepared.context.history, prepared.input.modelInput, prepared.context.memory,
			prepared.context.skillContext, prepared.context.externalSkillContext, turn.Mode, prepared.context.threadEndTurn, prepared.tools.guidance)
		baseRequest.Messages, err = s.supervisorMessagesWithImages(ctx, turn, prepared.context.history, baseRequest.Messages, prepared.layout)
		if err != nil {
			return err
		}
		baseRequest.Messages, err = s.supervisorMessagesWithOriginalFiles(ctx, turn, baseRequest.Messages, prepared.tools.options.CommandRuntime)
		if err != nil {
			return err
		}
		baseRequest.Messages, prepared.layout, err = s.supervisorMessagesWithPrivateHistory(ctx, turn, prepared.context.history, baseRequest.Messages, prepared.layout)
		if err != nil {
			return err
		}
		baseRequest.Messages = append(baseRequest.Messages, prepared.input.mcpMessages...)
		if prepared.input.boundaryContext != "" {
			baseRequest.Messages = append(baseRequest.Messages,
				toolBoundaryEvidenceMessage(turn.Run.SessionID, turn.Checkpoint.AttemptID, prepared.input.boundaryContext))
		}
		if prepared.input.fileEffects != "" {
			baseRequest.Messages = append(baseRequest.Messages,
				toolBoundaryEvidenceMessage(turn.Run.SessionID, turn.Checkpoint.AttemptID, prepared.input.fileEffects))
		}
		supervisorSummaryMetadata(&baseRequest, prepared.context.summary, prepared.context.hasSummary)
		baseRequest.Metadata["memory_sections"] = fmt.Sprint(len(prepared.context.memory.Sections))
		baseRequest.Metadata["memory_omitted"] = fmt.Sprint(len(prepared.context.memory.OmittedSources))
		baseRequest.Metadata["memory_tokens"] = fmt.Sprint(prepared.context.memory.EstimatedTokens)
		baseRequest.Metadata["selected_notes"] = fmt.Sprint(countContextSources(prepared.context.memory.IncludedSources, "note"))
		baseRequest.Metadata["long_term_memory_selected"] = fmt.Sprint(countContextSources(prepared.context.memory.IncludedSources, "long_term_memory"))
		baseRequest.Metadata["project_instruction_items"] = fmt.Sprint(countContextSources(prepared.context.memory.IncludedSources, "project_instruction"))
		baseRequest.Metadata["continuity_context_items"] = fmt.Sprint(countContextSources(prepared.context.memory.IncludedSources, "continuity_context"))
		baseRequest.Metadata["pending_user_instructions"] = fmt.Sprint(prepared.input.pendingInstructions)
		refreshStandardCodeSupervisorRequest(&baseRequest, prepared.tools.standardCode)
		request, err = rebuildToolRequest()
		if err != nil {
			return err
		}
		return nil
	}
	refreshCurrentSteering := func() error {
		refreshedInput, count, sequence, err := supervisorInputWithPendingInstructions(ctx, s.store, turn, prepared.input.visibleBaseInput)
		if err != nil {
			return err
		}
		if sequence == prepared.input.steeringSequence && refreshedInput == prepared.input.modelInput {
			return nil
		}
		prepared.input.modelInput, prepared.input.pendingInstructions, prepared.input.steeringSequence = refreshedInput, count, sequence
		return rebuildHistoryRequest(false)
	}
	refreshLongTermMemories := func() error {
		memories, err := loadSupervisorLongTermMemories(ctx, s.store, turn.Mission.WorkspaceID)
		if err != nil {
			return err
		}
		sections, err := longTermMemoryContextSections(memories)
		if err != nil {
			return err
		}
		if slices.Equal(sections, prepared.context.longTermMemorySections) {
			return nil
		}
		prepared.context.longTermMemorySections = sections
		result.LongTermMemoryItems = len(memories)
		baseRequest.Metadata["long_term_memory_items"] = fmt.Sprint(len(memories))
		return rebuildHistoryRequest(false)
	}
	trySegmentReceipt := func() bool {
		if s.router.RequiresPrivateAssistantHistory(prepared.ref) {
			return false
		}
		// A tiny first round can make a single-round receipt larger. Check all
		// bounded prefixes locally before deciding no useful reduction exists.
		for count := receiptedRounds + 1; count <= len(prepared.toolRounds); count++ {
			plan, planErr := minimalSupervisorSegmentReceiptPlan(prepared.toolRounds, count,
				turn.Run.SessionID, turn.Checkpoint.AttemptID)
			if planErr != nil {
				return false
			}
			rebuilt, rebuildErr := supervisorRequestWithSegmentReceipt(baseRequest, plan,
				turn.Run.SessionID, turn.Checkpoint.AttemptID)
			if rebuildErr != nil {
				return false
			}
			rebuilt, rebuildErr = s.attachSupervisorProviderReplay(ctx, turn.Checkpoint, rebuilt, plan.NativeRounds)
			if rebuildErr != nil {
				return false
			}
			if estimateModelRequestTokens(rebuilt) >= estimateModelRequestTokens(request) {
				continue
			}
			request, receiptedRounds, receiptPlan = rebuilt, count, plan
			return true
		}
		return false
	}

	for {
		// Each logical model request uses the current confirmed records. A
		// transport retry remains bound to the same prepared request and audit.
		if err := refreshLongTermMemories(); err != nil {
			return result, s.recordFailure(ctx, &result, err, 0)
		}
		if err := refreshCurrentSteering(); err != nil {
			return result, s.recordFailure(ctx, &result, err, 0)
		}
		if s.agentBrowser != nil || s.browserActions != nil {
			prepared.tools.options.BrowserActions.Capabilities, prepared.tools.options.BrowserActions.Authority, err = s.supervisorBrowserActionCapabilities(ctx, turn, prepared.tools.permission)
			if err != nil {
				return result, err
			}
			request.Tools = refreshBrowserModelTools(request.Tools, prepared.tools.options.BrowserActions.Capabilities)
		}
		modelRequest := request
		modelContextLayout := prepared.layout
		if len(prepared.toolRounds) == domain.MaxSupervisorToolRounds {
			modelRequest = supervisorToolBoundaryRequest(modelRequest)
		}
		if protocolRepair == 1 {
			modelRequest = supervisorProtocolRepairRequest(modelRequest, repairReason, supervisorRepairContext{ThreadEndTurn: prepared.context.threadEndTurn, ToolRounds: len(prepared.toolRounds)})
			modelContextLayout = modelContextLayout.shifted(1)
		}
		modelRequest, err = s.requestWithBuiltinSkillReads(ctx, turn, modelRequest)
		if err != nil {
			return result, s.recordFailure(ctx, &result, err, 0)
		}
		modelRequest, err = prepareModelHarnessRequest(s.router, prepared.ref,
			llm.HarnessWorkloadRoot, modelRequest)
		if err == nil {
			modelRequest, err = s.supervisorBrowserImages(ctx, turn.Checkpoint, prepared.ref, modelRequest, prepared.toolRounds)
		}
		if err == nil {
			modelRequest, err = supervisorMCPContent(modelRequest, prepared.toolRounds, s.router.DescribeVision(prepared.ref).State == llm.VisionSupported)
		}
		if err != nil {
			failure := s.recordFailure(ctx, &result, err, 0)
			return result, failure
		}
		modelWindow, _ := modelRequest.PreparedContextWindow()
		if modelRequest.MaxTokens <= 0 && (!modelRequest.AllowsDefaultOutput() || turn.Run.Budget.MaxTokens > 0 || turn.Run.Budget.MaxCostUSD > 0) {
			modelRequest.MaxTokens = modelRequest.PlannedOutputTokens(modelWindow)
		}
		modelRequest, err = supervisorRequestWithinBudget(modelRequest, turn.Run.Budget, turn.Checkpoint)
		if err != nil {
			failure := s.recordFailure(ctx, &result, err, 0)
			return result, failure
		}
		if len(modelRequest.Tools) > 0 {
			modelRequest = supervisorOutputBudgetGuidance(modelRequest, modelWindow)
			modelRequest = supervisorFileEditGuidance(modelRequest, prepared.tools.options.AgentCode.Capabilities)
			modelContextLayout = modelContextLayout.shifted(1)
		}
		modelRequest, err = s.workspaceReadFeedbackRequest(ctx, turn.Checkpoint, modelRequest, prepared.toolRounds)
		if err != nil {
			return result, s.recordFailure(ctx, &result, err, 0)
		}
		var recoveryLimit *int
		if reader, ok := s.store.(supervisorContextRecoveryLimitStore); ok {
			limit, found, readErr := reader.SupervisorContextRecoveryInputLimit(ctx, turn.Checkpoint, protocolRepair, len(prepared.toolRounds))
			if readErr != nil {
				return result, s.recordFailure(ctx, &result, readErr, 0)
			}
			if found {
				recoveryLimit = &limit
			}
		}
		// Receipt growth must not consume the Run's remaining output allowance.
		// Preserve the existing budget path for requests without either receipt.
		receiptBudget := domain.Budget{}
		if receiptedRounds > 0 || prepared.input.boundaryContext != "" {
			receiptBudget = turn.Run.Budget
		}
		inputLimit, limitErr := supervisorReceiptInputLimit(modelRequest, modelWindow, receiptBudget, turn.Checkpoint, recoveryLimit)
		if limitErr != nil {
			return result, s.recordFailure(ctx, &result, limitErr, 0)
		}
		if receiptedRounds > 0 || prepared.input.boundaryContext != "" {
			blockers := append([]domain.SupervisorToolCall(nil), prepared.input.fileEffectCalls...)
			for _, round := range prepared.toolRounds {
				blockers = append(blockers, round.Calls...)
			}
			modelRequest, err = fitSupervisorReceiptViews(modelRequest, receiptPlan, prepared.input.boundaryReceipt, turn.Run.SessionID, turn.Checkpoint.AttemptID, inputLimit, blockers)
			if err != nil {
				return result, s.recordFailure(ctx, &result, err, 0)
			}
		}
		boundedRequest, contextPlan, err := constrainRequestToModelWindow(modelRequest,
			modelWindow, modelContextLayout, inputLimit)
		if err != nil && contextPlan.HistoryOmitted == 0 {
			if trySegmentReceipt() {
				continue
			}
			failure := s.recordFailure(ctx, &result, supervisorContextWindowFailure(err), 0)
			return result, failure
		}
		if contextPlan.HistoryOmitted > 0 {
			// Never send the generic fitter's lossy history slice on the root
			// path. Compact durable history first, then reassemble the complete
			// request, including the still-current input and native tool pairs.
			compacted, compactErr := s.compactSupervisorHistory(ctx, &turn, 1)
			result.Checkpoint = turn.Checkpoint
			if compactErr == nil && !compacted {
				if trySegmentReceipt() {
					continue
				}
				compactErr = supervisorContextWindowFailure(apperror.New(apperror.CodeResourceExhausted,
					"model context remains too large after compaction; history is preserved, use a larger context window or shorten the current input"))
			}
			if compactErr != nil {
				failure := s.recordFailure(ctx, &result, compactErr, 0)
				return result, failure
			}
			if err := rebuildHistoryRequest(true); err != nil {
				return result, s.recordFailure(ctx, &result, err, 0)
			}
			continue
		}
		modelRequest = boundedRequest
		if err := requirePinnedProjectInstructionRequest(turn.Run.Config, modelRequest); err != nil {
			return result, s.recordFailure(ctx, &result, err, 0)
		}
		modelCall, err := s.callModelWithRetry(ctx, turn, prepared.ref, modelRequest, protocolRepair,
			len(prepared.toolRounds), prepared.input.steeringSequence, prepared.context.contextAudit)
		if modelCall.Checkpoint.RunID != "" {
			turn.Checkpoint = modelCall.Checkpoint
			result.Checkpoint = modelCall.Checkpoint
		}
		if modelCall.Attempt.Number > 0 {
			result.ModelAttempts = modelCall.Attempt.Number
		}
		if modelCall.Attempt.Outcome != "" {
			result.ModelOutcome = modelCall.Attempt.Outcome
		}
		result.StreamEvents += modelCall.StreamEvents
		result.StreamBytes += modelCall.StreamBytes
		if err != nil {
			if ctx.Err() != nil {
				return result, apperror.Normalize(ctx.Err())
			}
			if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeConflict {
				if current, readErr := supervisorCurrentSteeringSequence(ctx, s.store, turn.Checkpoint); readErr == nil && current != prepared.input.steeringSequence {
					continue
				}
				return result, apperror.Normalize(err)
			}
			if modelCall.Attempt.FailureReason == llm.ProviderFailureContextLimit && modelCall.FailureRecorded {
				if recovery, supported := s.store.(supervisorContextRecoveryStore); supported {
					claimed, claimErr := recovery.ClaimSupervisorContextRecovery(ctx, turn.Checkpoint, modelCall.Attempt)
					if claimErr != nil {
						return result, s.recordFailure(ctx, &result, claimErr, 0)
					}
					if claimed {
						before := estimateModelRequestTokens(request)
						compacted, compactErr := s.compactSupervisorHistory(ctx, &turn, 1)
						result.Checkpoint = turn.Checkpoint
						if compactErr == nil && compacted {
							compactErr = rebuildHistoryRequest(true)
						}
						if compactErr != nil {
							return result, s.recordFailure(ctx, &result, compactErr, 0)
						}
						if estimateModelRequestTokens(request) >= before {
							trySegmentReceipt()
						}
						if estimateModelRequestTokens(request) < before && ctx.Err() == nil {
							continue
						}
					}
				}
			}
			failure := s.recordFailure(ctx, &result, err, modelCall.UnpersistedElapsed)
			return result, failure
		}
		response := modelCall.Response
		if response == nil {
			updated, err := s.recordInvalidModelAttempt(ctx, turn.Checkpoint, &modelCall.Attempt,
				llm.NewProviderError(llm.OutcomeInvalidResponse, prepared.ref.Provider, "returned an empty response", nil))
			failureElapsed := modelCall.Attempt.Elapsed
			if updated.RunID != "" {
				turn.Checkpoint = updated
				result.Checkpoint = updated
				failureElapsed = 0
			}
			result.ModelOutcome = modelCall.Attempt.Outcome
			failure := s.recordFailure(ctx, &result, err, failureElapsed)
			return result, failure
		}
		if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 || response.Usage.TotalTokens < 0 {
			updated, err := s.recordInvalidModelAttempt(ctx, turn.Checkpoint, &modelCall.Attempt,
				llm.NewProviderError(llm.OutcomeInvalidResponse, prepared.ref.Provider, "returned negative token usage", nil))
			failureElapsed := modelCall.Attempt.Elapsed
			if updated.RunID != "" {
				turn.Checkpoint = updated
				result.Checkpoint = updated
				failureElapsed = 0
			}
			result.ModelOutcome = modelCall.Attempt.Outcome
			failure := s.recordFailure(ctx, &result, err, failureElapsed)
			return result, failure
		}
		if len(response.ToolCalls) == 0 {
			currentSteering, readErr := supervisorCurrentSteeringSequence(ctx, s.store, turn.Checkpoint)
			if readErr != nil {
				return result, apperror.Normalize(readErr)
			}
			if currentSteering != modelCall.Attempt.SteeringSequence {
				// Charge and retain the actual model response, but do not present an
				// answer prepared before the newly accepted correction as final.
				modelCall.Attempt.Outcome = llm.OutcomeSuccess
				eventCtx, eventCancel := supervisorModelEventContext(ctx)
				updated, storeErr := s.store.RecordSupervisorModelCompleted(eventCtx,
					turn.Checkpoint, modelCall.Attempt, *response)
				eventCancel()
				if storeErr != nil {
					return result, s.failReceivedModelOutput(ctx, &result, &turn,
						modelCall.Attempt, *response, storeErr)
				}
				turn.Checkpoint, result.Checkpoint = updated, updated
				if s.monetary != nil {
					if settleErr := s.settleModelAccounting(ctx, turn.Checkpoint.RunID,
						modelCall.Attempt, response.Usage, 0); settleErr != nil {
						return result, s.recordFailure(ctx, &result, settleErr, 0)
					}
				}
				continue
			}
		}
		var action domain.RootAction
		var parseErr error
		repairableRootResponse := false
		repairableToolRequest := false
		var rootActionRecovery rootActionTrailingCommentaryRecovery
		if len(response.ToolCalls) > 0 {
			_, continuationRepair := domain.SupervisorThreadContinueRepairRound(repairReason)
			_, textToolRepair := domain.SupervisorTextToolRepairRound(repairReason)
			switch {
			case protocolRepair != 0 && !domain.IsSupervisorToolRequestRepair(repairReason) && !continuationRepair && !textToolRepair:
				parseErr = errors.New("protocol repair response cannot request tools")
			case len(prepared.toolRounds) >= domain.MaxSupervisorToolRounds:
				parseErr = fmt.Errorf("supervisor tool round limit of %d was exhausted",
					domain.MaxSupervisorToolRounds)
			default:
				var preparedCalls []llm.ToolCall
				preparedCalls, parseErr = prepareSupervisorToolCalls(response.ToolCalls,
					turn.Run.ID, turn.Checkpoint.NextTurn, len(prepared.toolRounds)+1,
					turn.Mode.Surface, turn.Mode.Phase, prepared.tools.permission.Mode,
					prepared.tools.skillCandidateEnabled, s.debugTerminalEnabled,
					prepared.tools.options)
				if parseErr == nil {
					preparedCalls, parseErr = s.bindCommandRuntimeCalls(ctx, preparedCalls)
				}
				if parseErr == nil && response.Replay != nil {
					response.Replay, parseErr = response.Replay.BindToolCalls(preparedCalls)
					if parseErr != nil {
						break
					}
				}
				if parseErr == nil {
					response.ToolCalls = preparedCalls
				} else {
					// Preparation rejects the whole batch before any tool is
					// recorded or invoked. Only this known no-effect boundary
					// may reuse the single durable protocol correction.
					repairableToolRequest = true
				}
			}
			if parseErr == nil {
				if commentary, ok := prepareModelPublicCommentary(s.checker, turn.Checkpoint,
					modelCall.Attempt, response.Text); ok {
					eventCtx, eventCancel := supervisorModelEventContext(ctx)
					_, storeErr := s.store.RecordSupervisorModelPublicCommentary(eventCtx,
						turn.Checkpoint, modelCall.Attempt, commentary)
					eventCancel()
					if storeErr != nil {
						failure := s.failReceivedModelOutput(ctx, &result, &turn, modelCall.Attempt, *response, storeErr)
						return result, failure
					}
				}
				modelCall.Attempt.Outcome = llm.OutcomeSuccess
				eventCtx, eventCancel := supervisorModelEventContext(ctx)
				var updated domain.SupervisorCheckpoint
				var storeErr error
				if attributed, ok := s.store.(supervisorAgentAttributionStore); ok {
					updated, storeErr = attributed.RecordSupervisorModelCompletedForAgent(
						eventCtx, turn.Checkpoint, modelCall.Attempt, *response,
						domain.AgentAttribution{AgentID: turn.Agent.ID,
							AgentAttemptID: turn.Checkpoint.AttemptID,
							Source:         domain.AgentAttributionRecorded})
				} else {
					updated, storeErr = s.store.RecordSupervisorModelCompleted(eventCtx,
						turn.Checkpoint, modelCall.Attempt, *response)
				}
				eventCancel()
				if storeErr != nil {
					failure := s.failReceivedModelOutput(ctx, &result, &turn, modelCall.Attempt, *response, storeErr)
					return result, failure
				}
				turn.Checkpoint = updated
				result.Checkpoint = updated
				if s.monetary != nil {
					if settleErr := s.settleModelAccounting(ctx, turn.Checkpoint.RunID,
						modelCall.Attempt, response.Usage, len(response.ToolCalls)); settleErr != nil {
						failure := s.recordFailure(ctx, &result, settleErr, 0)
						return result, failure
					}
				}
				result.ModelOutcome = llm.OutcomeSuccess
				prepared.toolRounds, storeErr = s.store.ListSupervisorToolRounds(ctx, turn.Checkpoint)
				if storeErr != nil {
					return result, apperror.Normalize(storeErr)
				}
				if err := s.requirePinnedProjectInstructionToolOrigins(ctx, turn, prepared.toolRounds); err != nil {
					return result, s.recordFailure(ctx, &result, err, 0)
				}
				var waitingApproval bool
				prepared.toolRounds, waitingApproval, storeErr = s.resumeSupervisorTools(ctx, turn, prepared.toolRounds,
					prepared.tools.standardCode)
				if storeErr != nil {
					return result, apperror.Normalize(storeErr)
				}
				if waitingApproval {
					return supervisorApprovalWaitingResult(result, prepared.toolRounds), nil
				}
				prepared.context.workItems, storeErr = s.store.ListWorkItems(ctx, domain.WorkItemFilter{
					RunID: turn.Run.ID,
					Statuses: []domain.WorkItemStatus{
						domain.WorkItemInProgress, domain.WorkItemBlocked, domain.WorkItemPending,
					},
					Limit: maxSupervisorWorkItems,
				})
				if storeErr != nil {
					return result, apperror.Normalize(storeErr)
				}
				baseRequest.Metadata["active_work_items"] = fmt.Sprint(len(prepared.context.workItems))
				refreshStandardCodeSupervisorRequest(&baseRequest, prepared.tools.standardCode)
				request, storeErr = rebuildToolRequest()
				if storeErr != nil {
					return result, apperror.Normalize(storeErr)
				}
				result.ToolRounds, result.ToolCalls = supervisorToolStats(prepared.toolRounds)
				continue
			}
		} else {
			evaluated := s.evaluateAgentRootAction(ctx, turn, response, prepared, protocolRepair, repairReason)
			action, parseErr = evaluated.action, evaluated.err
			rootActionRecovery, repairableRootResponse = evaluated.recovery, evaluated.repairable
		}
		if parseErr != nil {
			reason := supervisorProtocolRepairReason(parseErr)
			if len(response.ToolCalls) > 0 {
				// Capture the original received batch and this exact offered request
				// before any failed call can become executable work. Provider-owned
				// metadata cannot issue this diagnostic or reconstruct authority.
				offered := append(make([]llm.ToolSpec, 0, len(modelRequest.Tools)), modelRequest.Tools...)
				response.ToolRequestRejection = llm.NewToolRequestRejection(response.ToolCalls, offered)
				if feedback := response.ToolRequestRejection.VersionRepairFeedback(); feedback != "" {
					reason = sanitizeProtocolRepairReason(reason + " " + feedback)
				}
			}
			if repairableToolRequest {
				if toolReason, reasonErr := domain.NewSupervisorToolRequestRepairReason(len(prepared.toolRounds), reason); reasonErr == nil {
					reason = toolReason
				} else {
					repairableToolRequest = false
				}
			}
			providerErr := llm.NewProviderError(llm.OutcomeInvalidResponse, prepared.ref.Provider, reason, parseErr)
			modelCall.Attempt.Outcome = llm.OutcomeInvalidResponse
			modelCall.Attempt.ErrorText = reason
			modelCall.Attempt.RetryAfter = 0
			modelCall.Attempt.RetryPlanned = false
			requestRepair := protocolRepair == 0 && (repairableRootResponse || repairableToolRequest)
			eventCtx, eventCancel := supervisorModelEventContext(ctx)
			updated, storeErr := s.store.RecordSupervisorProtocolFailure(eventCtx, turn.Checkpoint, modelCall.Attempt, *response, reason, requestRepair)
			eventCancel()
			if updated.RunID != "" {
				turn.Checkpoint = updated
				result.Checkpoint = updated
			}
			result.ModelOutcome = llm.OutcomeInvalidResponse
			if storeErr != nil {
				failure := s.failReceivedModelOutput(ctx, &result, &turn, modelCall.Attempt, *response,
					errors.Join(providerApplicationError(providerErr), storeErr))
				return result, failure
			}
			if settleErr := s.settleModelAccounting(ctx, turn.Checkpoint.RunID,
				modelCall.Attempt, response.Usage, len(response.ToolCalls)); settleErr != nil {
				return result, s.recordFailure(ctx, &result, settleErr, 0)
			}
			if requestRepair {
				protocolRepair = 1
				repairReason = reason
				result.ProtocolRepairs = 1
				continue
			}
			failure := s.recordFailure(ctx, &result, providerApplicationError(providerErr), 0)
			return result, failure
		}
		completed, retry, err := s.commitAgentRootAction(ctx, &turn, result, modelCall,
			action, rootActionRecovery, len(prepared.toolRounds))
		if retry {
			result = completed
			continue
		}
		return completed, err
	}
}

func (s *AgentRunner) Execute(ctx context.Context, runID string, maxSteps int) (ExecutionResult, error) {
	if s == nil || s.store == nil || s.router == nil || s.checker == nil {
		return ExecutionResult{}, apperror.New(apperror.CodeFailedPrecondition, "run supervisor dependencies are required")
	}
	if maxSteps <= 0 {
		return ExecutionResult{}, apperror.New(apperror.CodeInvalidArgument, "max steps must be positive")
	}
	result := ExecutionResult{RunID: strings.TrimSpace(runID), Steps: make([]LifecycleResult, 0)}
	run, err := s.store.GetRun(ctx, result.RunID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	result.RunStatus = run.Status
	if run.Terminal() {
		s.revokeRunRuntimeAuthority(run.ID)
		result.StopReason = "run_terminal"
		return result, nil
	}
	if run.Status == domain.RunPaused {
		result.StopReason = "run_paused"
		return result, nil
	}
	if run.Status == domain.RunWaitingApproval {
		result.StopReason = "waiting_approval"
		return result, nil
	}
	err = s.withRunExecutionLease(ctx, result.RunID, func(leaseCtx context.Context,
		lease domain.RunExecutionLease,
	) error {
		return s.executeWithLease(leaseCtx, lease, maxSteps, &result)
	})
	return result, err
}

func (s *AgentRunner) DrainOperatorSteering(ctx context.Context, runID string,
	maxSteps int,
) (ExecutionResult, error) {
	if s == nil || s.store == nil || s.router == nil || s.checker == nil {
		return ExecutionResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"run supervisor dependencies are required")
	}
	if maxSteps <= 0 || maxSteps > domain.MaxPendingOperatorSteering {
		return ExecutionResult{}, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("steering drain steps must be between 1 and %d",
				domain.MaxPendingOperatorSteering))
	}
	result := ExecutionResult{RunID: strings.TrimSpace(runID), Steps: make([]LifecycleResult, 0)}
	run, err := s.store.GetRun(ctx, result.RunID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	result.RunStatus = run.Status
	if run.Status != domain.RunRunning {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			fmt.Sprintf("run %s is %s; steering drain requires running", run.ID, run.Status))
	}
	summary, err := s.store.GetOperatorSteeringQueueSummary(ctx, run.ID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if summary.Pending+summary.Prepared == 0 {
		result.StopReason = "queue_empty"
		return result, nil
	}
	err = s.withRunExecutionLease(ctx, result.RunID, func(leaseCtx context.Context,
		lease domain.RunExecutionLease,
	) error {
		return s.drainOperatorSteeringWithLease(leaseCtx, lease, maxSteps, &result)
	})
	return result, err
}

func (s *AgentRunner) drainOperatorSteeringWithLease(ctx context.Context,
	lease domain.RunExecutionLease, maxSteps int, result *ExecutionResult,
) error {
	for range maxSteps {
		run, err := s.store.GetRun(ctx, result.RunID)
		if err != nil {
			return apperror.Normalize(err)
		}
		result.RunStatus = run.Status
		if run.Terminal() {
			s.revokeRunRuntimeAuthority(run.ID)
			result.StopReason = "run_terminal"
			return nil
		}
		if run.Status == domain.RunPaused {
			result.StopReason = "run_paused"
			return nil
		}
		if run.Status == domain.RunWaitingApproval {
			result.StopReason = "waiting_approval"
			return nil
		}
		summary, err := s.store.GetOperatorSteeringQueueSummary(ctx, result.RunID)
		if err != nil {
			return apperror.Normalize(err)
		}
		if summary.Pending+summary.Prepared == 0 {
			result.StopReason = "steering_drained"
			return nil
		}
		step, err := s.stepSteeringWithLease(ctx, lease)
		if step.Turn > 0 {
			result.Steps = append(result.Steps, step)
		}
		if err != nil {
			if apperror.CodeOf(err) == apperror.CodeFailedPrecondition {
				after, summaryErr := s.store.GetOperatorSteeringQueueSummary(ctx, result.RunID)
				if summaryErr == nil && after.Pending+after.Prepared == 0 {
					result.StopReason = "steering_drained"
					return nil
				}
			}
			result.StopReason = strings.ToLower(string(apperror.CodeOf(err)))
			return err
		}
		result.RunStatus = step.RunStatus
		if step.RunStatus == domain.RunWaitingApproval {
			result.StopReason = "waiting_approval"
			return nil
		}
		if step.Action.Kind == domain.RootActionFinish {
			result.StopReason = "root_finish"
			return nil
		}
		if step.Action.Kind == domain.RootActionWait {
			result.StopReason = supervisorWaitStopReason(step.Action)
			return nil
		}
	}
	run, err := s.store.GetRun(ctx, result.RunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	result.RunStatus = run.Status
	summary, err := s.store.GetOperatorSteeringQueueSummary(ctx, result.RunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if summary.Pending+summary.Prepared == 0 {
		result.StopReason = "steering_drained"
	} else {
		result.StopReason = "step_limit"
	}
	return nil
}

func supervisorApprovalWaitingResult(result LifecycleResult,
	rounds []domain.SupervisorToolRound,
) LifecycleResult {
	result.Status = LifecycleTurnCompleted
	result.RunStatus = domain.RunWaitingApproval
	result.Action = domain.RootAction{Version: domain.RootLifecycleVersion,
		Kind:    domain.RootActionWait,
		Message: "Waiting for operator review of the exact high-risk action.",
		Reason:  "waiting_approval:risk_escalation"}
	result.Text = result.Action.Message
	result.ToolRounds, result.ToolCalls = supervisorToolStats(rounds)
	return result
}

func (s *AgentRunner) executeWithLease(ctx context.Context, lease domain.RunExecutionLease,
	maxSteps int, result *ExecutionResult,
) error {
	for range maxSteps {
		run, err := s.store.GetRun(ctx, result.RunID)
		if err != nil {
			return apperror.Normalize(err)
		}
		result.RunStatus = run.Status
		if run.Terminal() {
			s.revokeRunRuntimeAuthority(run.ID)
			result.StopReason = "run_terminal"
			return nil
		}
		if run.Status == domain.RunPaused {
			result.StopReason = "run_paused"
			return nil
		}
		if run.Status == domain.RunWaitingApproval {
			result.StopReason = "waiting_approval"
			return nil
		}
		step, err := s.stepWithLease(ctx, lease, "")
		if step.Turn > 0 {
			result.Steps = append(result.Steps, step)
		}
		if err != nil {
			result.StopReason = strings.ToLower(string(apperror.CodeOf(err)))
			return err
		}
		result.RunStatus = step.RunStatus
		switch step.Action.Kind {
		case domain.RootActionFinish:
			result.StopReason = "root_finish"
			return nil
		case domain.RootActionWait:
			result.StopReason = supervisorWaitStopReason(step.Action)
			return nil
		}
	}
	run, err := s.store.GetRun(ctx, result.RunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	result.RunStatus = run.Status
	result.StopReason = "step_limit"
	return nil
}

func supervisorWaitStopReason(action domain.RootAction) string {
	if strings.HasPrefix(strings.TrimSpace(action.Reason), "livelock_detected:") {
		return "livelock_detected"
	}
	return "root_wait"
}

func (s *AgentRunner) Finalize(ctx context.Context, runID string, outcome LifecycleOutcome, summary string) (FinalizationResult, error) {
	if s == nil || s.store == nil {
		return FinalizationResult{}, apperror.New(apperror.CodeFailedPrecondition, "run supervisor store is required")
	}
	var target domain.RunStatus
	switch outcome {
	case LifecycleOutcomeCompleted:
		target = domain.RunCompleted
	case LifecycleOutcomeFailed:
		target = domain.RunFailed
	default:
		return FinalizationResult{}, apperror.New(apperror.CodeInvalidArgument, "lifecycle outcome must be completed or failed")
	}
	runID = strings.TrimSpace(runID)
	current, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return FinalizationResult{}, apperror.Normalize(err)
	}
	if current.Status == target {
		s.revokeRunRuntimeAuthority(current.ID)
		return s.finalizationResult(ctx, current, outcome, summary)
	}
	if current.Terminal() {
		s.revokeRunRuntimeAuthority(current.ID)
		return FinalizationResult{}, apperror.New(apperror.CodeConflict,
			fmt.Sprintf("run %s is already terminal as %s", current.ID, current.Status))
	}
	if outcome == LifecycleOutcomeCompleted {
		mode, modeErr := s.store.GetRunMode(ctx, current.ID)
		if modeErr != nil {
			return FinalizationResult{}, apperror.Normalize(modeErr)
		}
		if mode.Phase == domain.ExecutionPhasePlan {
			return FinalizationResult{}, apperror.New(apperror.CodeFailedPrecondition,
				"plan-phase Run cannot be completed; switch to deliver or cancel it")
		}
		if presetStore, ok := s.store.(standardCodeSupervisorStore); ok {
			if _, configured, presetErr := presetStore.GetConfiguredStandardCodePresetOperation(
				ctx, current.ID); presetErr != nil {
				return FinalizationResult{}, apperror.Normalize(presetErr)
			} else if configured {
				if s.standardCodeDelivery == nil {
					return FinalizationResult{}, apperror.New(apperror.CodeFailedPrecondition,
						"Standard Code completion requires the delivery truth gate")
				}
				report, found, deliveryErr := s.standardCodeDelivery.Current(ctx, current.ID)
				if deliveryErr != nil {
					return FinalizationResult{}, deliveryErr
				}
				if !found || report.Status != standardcodedelivery.StatusPassed || !report.Verified {
					return FinalizationResult{}, apperror.New(apperror.CodeFailedPrecondition,
						"Standard Code completion requires a current passed delivery receipt")
				}
			}
		}
	}
	mission, err := s.store.GetMission(ctx, current.MissionID)
	if err != nil {
		return FinalizationResult{}, apperror.Normalize(err)
	}
	var finalized FinalizationResult
	err = s.withRunExecutionLease(ctx, current.ID, func(leaseCtx context.Context,
		lease domain.RunExecutionLease,
	) error {
		if err := executeLifecycleBoundary(leaseCtx, s.lifecycleHooks, hooks.RunCompleted,
			current.ID, mission.WorkspaceID, map[string]any{
				"session_id": current.SessionID, "from": current.Status, "to": target,
				"outcome": outcome, "summary_present": strings.TrimSpace(summary) != "",
				"source": "run_supervisor",
			}); err != nil {
			return err
		}
		// Terminal persistence and every process-local child capability share
		// this boundary. Revoke first so a failed store commit is fail-closed.
		s.revokeRunRuntimeAuthority(current.ID)
		run, checkpoint, finalizeErr := s.store.FinalizeSupervisorRun(leaseCtx, lease, target, summary)
		if finalizeErr == nil {
			finalized = FinalizationResult{Run: run, Checkpoint: checkpoint, Outcome: outcome,
				Summary: redact.String(strings.TrimSpace(summary))}
		}
		return finalizeErr
	})
	if err != nil && finalized.Run.ID == "" {
		latest, getErr := s.store.GetRun(ctx, current.ID)
		if getErr == nil {
			switch {
			case latest.Status == target:
				s.revokeRunRuntimeAuthority(latest.ID)
				return s.finalizationResult(ctx, latest, outcome, summary)
			case latest.Terminal():
				s.revokeRunRuntimeAuthority(latest.ID)
				return FinalizationResult{}, apperror.New(apperror.CodeConflict,
					fmt.Sprintf("run %s is already terminal as %s", latest.ID, latest.Status))
			}
		}
	}
	return finalized, apperror.Normalize(err)
}

func (s *AgentRunner) finalizationResult(ctx context.Context, run domain.Run,
	outcome LifecycleOutcome, summary string,
) (FinalizationResult, error) {
	checkpoint, ok, err := s.store.GetSupervisorCheckpoint(ctx, run.ID)
	if err != nil {
		return FinalizationResult{}, apperror.Normalize(err)
	}
	if !ok {
		return FinalizationResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"finalized run has no supervisor checkpoint")
	}
	return FinalizationResult{Run: run, Checkpoint: checkpoint, Outcome: outcome,
		Summary: redact.String(strings.TrimSpace(summary))}, nil
}

func (s *AgentRunner) withRunExecutionLease(ctx context.Context, runID string,
	operation func(context.Context, domain.RunExecutionLease) error,
) error {
	return withRunExecutionLease(ctx, s.store, runID, s.leaseOwner, s.leasePolicy, operation)
}

func (s *AgentRunner) Checkpoint(ctx context.Context, runID string) (domain.SupervisorCheckpoint, bool, error) {
	if s == nil || s.store == nil {
		return domain.SupervisorCheckpoint{}, false, apperror.New(apperror.CodeFailedPrecondition, "run supervisor store is required")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.SupervisorCheckpoint{}, false, apperror.New(apperror.CodeInvalidArgument, "run id is required")
	}
	checkpoint, ok, err := s.store.GetSupervisorCheckpoint(ctx, runID)
	if err != nil || ok {
		return checkpoint, ok, apperror.Normalize(err)
	}
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		return domain.SupervisorCheckpoint{}, false, apperror.Normalize(err)
	}
	return domain.SupervisorCheckpoint{}, false, nil
}

func (s *AgentRunner) recordFailure(ctx context.Context, result *LifecycleResult, cause error, elapsed time.Duration) error {
	classified := apperror.Normalize(cause)
	safeCause := apperror.Wrap(apperror.CodeOf(classified), redact.String(classified.Error()), classified)
	checkpoint, err := s.store.FailSupervisorTurn(ctx, result.Checkpoint, safeCause.Error(), elapsed)
	if err != nil {
		return errors.Join(safeCause, err)
	}
	result.Checkpoint = checkpoint
	return safeCause
}
