package application

import (
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

// RunRuntimeDependencies binds host-owned services to one Supervisor. Hosts
// retain startup validation, reconciliation, permission selection and shutdown;
// this assembly neither activates grants nor starts workers or browser sessions.
// All siblings must receive the same process-local ExecutionCapabilities.
type RunRuntimeDependencies struct {
	ActiveCalls                    *ActiveCallRegistry
	ExecutionCapabilities          domain.ExecutionPermissionRuntimeCapabilities
	Drydocks                       *RunWorktreeService
	StandardCodeDelivery           *StandardCodeDeliveryService
	CommandRuntime                 toolgateway.CommandRuntimeExecutor
	DockerSandbox                  toolgateway.DockerSandboxProposalExecutor
	MCPClient                      AgentRunnerMCPClient
	LifecycleHooks                 *hooks.Engine
	ProjectHooks                   ProjectHookAuthorizer
	CodeIntel                      *codeintel.Manager
	WebEvidence                    *webevidence.Service
	WebFetchAuthorizationScheduler bool
	BrowserActions                 *FullCDPProductionService
	AgentBrowser                   *AgentBrowserService
	DebugTerminal                  DebugTerminalAgentInputController
}

func NewAgentRunnerWithRuntime(store AgentRunnerStore, router *llm.Router,
	checker policy.Checker, dependencies RunRuntimeDependencies,
) *AgentRunner {
	s := NewAgentRunner(store, router, checker).
		WithExecutionPermissionCapabilities(dependencies.ExecutionCapabilities).
		WithActiveCalls(dependencies.ActiveCalls).
		WithRunWorktree(dependencies.Drydocks).
		WithStandardCodeDelivery(dependencies.StandardCodeDelivery).
		WithWebFetchAuthorizationScheduler(dependencies.WebFetchAuthorizationScheduler).
		WithWebEvidence(dependencies.WebEvidence).
		WithCommandRuntime(dependencies.CommandRuntime).
		WithDockerSandboxProposalExecutor(dependencies.DockerSandbox).
		WithMCPClient(dependencies.MCPClient).
		WithLifecycleHooks(dependencies.LifecycleHooks).
		WithProjectHooks(dependencies.ProjectHooks).
		WithCodeIntel(dependencies.CodeIntel).
		WithBrowserActions(dependencies.BrowserActions).
		WithDebugTerminalAgentInput(dependencies.DebugTerminal)
	if dependencies.AgentBrowser != nil {
		s.WithAgentBrowser(dependencies.AgentBrowser)
	}
	return s
}

func NewRunExecutionHandoffWithRuntime(store RunExecutionHandoffStore, router *llm.Router,
	checker policy.Checker, dependencies RunRuntimeDependencies,
) *RunExecutionHandoffService {
	return &RunExecutionHandoffService{store: store,
		supervisor: NewAgentRunnerWithRuntime(store, router, checker, dependencies)}
}
