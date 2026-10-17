package toolgateway

import (
	"bytes"
	"context"
	"cyberagent-workbench/internal/domain"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/toolrun"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/waitgraph"
)

const maxWorkspaceListDepth = 32

type Store interface {
	toolrun.Store
	fileedit.Store
	approval.Store
	approval.GrantStore
	toolbudget.Store
	artifact.Store
	policy.DecisionRecorder
}

type ScriptRunStore interface {
	CreateScriptProcessRun(ctx context.Context, request ScriptRunStoreRequest) (ScriptRunStoreResult, error)
}

type Gateway struct {
	store                      Store
	grantStore                 approval.GrantStore
	budgetStore                toolbudget.Store
	artifactStore              artifact.Store
	policyRecorder             policy.DecisionRecorder
	checker                    policy.Checker
	workspaceRootResolver      WorkspaceRootResolver
	agentCodeWorkspaceResolver AgentCodeWorkspaceResolver
	legacyTools                *toolrun.Manager
	legacyEdits                *fileedit.Manager
	scriptStore                scriptprocess.Store
	scriptRunStore             ScriptRunStore
	scriptProcesses            *scriptprocess.Manager
	artifacts                  *artifact.Manager
	structuredMemory           StructuredMemoryExecutor
	historyRecall              HistoryRecallExecutor
	skillRead                  SkillReadExecutor
	delegationProposals        SpecialistDelegationExecutor
	childTaskProposals         ChildTaskProposalExecutor
	planDeliveryProposals      PlanDeliveryExecutor
	dockerSandboxProposals     DockerSandboxProposalExecutor
	skillCandidates            SkillCandidateExecutor
	debugTerminal              DebugTerminalExecutor
	commandRuntime             CommandRuntimeExecutor
	agentCode                  AgentCodeExecutor
	specialist                 SpecialistToolExecutor
	codeIntel                  CodeIntelExecutor
	mcp                        MCPExecutor
	mcpSearch                  MCPSearchExecutor
	webEvidence                WebEvidenceExecutor
	browserActions             BrowserActionExecutor
	agentBrowser               AgentBrowserExecutor
	lifecycleHooks             *hooks.Engine
	waitGraph                  *waitgraph.Graph
}

type WorkspaceRootResolver func(ctx context.Context, workspaceID string) (string, error)

func New(store Store, checker policy.Checker) *Gateway {
	if checker == nil {
		fallback := policy.NewDefaultChecker()
		checker = fallback
	}
	var scriptStore scriptprocess.Store
	var scriptRunStore ScriptRunStore
	if store != nil {
		if candidate, ok := store.(scriptprocess.Store); ok {
			scriptStore = candidate
		}
		if candidate, ok := store.(ScriptRunStore); ok {
			scriptRunStore = candidate
		}
	}
	gateway := &Gateway{
		store: store, grantStore: store, budgetStore: store, artifactStore: store, policyRecorder: store, checker: checker,
		legacyTools: toolrun.NewManager(store, checker), legacyEdits: fileedit.NewManager(store),
		scriptStore: scriptStore, scriptRunStore: scriptRunStore,
		scriptProcesses: scriptprocess.NewManager(scriptStore),
		artifacts:       artifact.NewManager(store),
		waitGraph:       waitgraph.Default(),
	}
	return gateway
}

// ScriptProcessCapabilities describes optional storage installed by New.
// A gateway used only for other tools need not implement either contract.
type ScriptProcessCapabilities struct {
	ProposalsAvailable   bool
	RunCreationAvailable bool
	UnavailableReasons   []string
}

func (g *Gateway) ScriptProcessCapabilities() ScriptProcessCapabilities {
	value := ScriptProcessCapabilities{UnavailableReasons: []string{}}
	if g != nil && g.scriptStore != nil {
		value.ProposalsAvailable = true
	} else {
		value.UnavailableReasons = append(value.UnavailableReasons, "script_process requires scriptprocess.Store")
	}
	if g != nil && g.scriptRunStore != nil {
		value.RunCreationAvailable = true
	} else {
		value.UnavailableReasons = append(value.UnavailableReasons, "script Run creation requires toolgateway.ScriptRunStore")
	}
	return value
}

func (g *Gateway) WithWaitGraph(graph *waitgraph.Graph) *Gateway {
	if g != nil && graph != nil {
		g.waitGraph = graph
	}
	return g
}

func (g *Gateway) WithWorkspaceRootResolver(resolver WorkspaceRootResolver) *Gateway {
	if g != nil {
		g.workspaceRootResolver = resolver
	}
	return g
}

func (g *Gateway) WithLifecycleHooks(engine *hooks.Engine) *Gateway {
	if g != nil {
		g.lifecycleHooks = engine
	}
	return g
}

func (g *Gateway) Invoke(ctx context.Context, call ToolCall) (outcome Outcome, returnedErr error) {
	if g == nil {
		return Outcome{}, errors.New("tool gateway is required")
	}
	normalized, err := NormalizeToolCall(call)
	if err != nil {
		return Outcome{}, err
	}
	if normalized.RequestedBy == "run_supervisor" && normalized.LeaseID == "" {
		return Outcome{}, errors.New("run supervisor tool call requires a Run execution lease")
	}
	if err := validateToolArguments(normalized); err != nil {
		return Outcome{}, err
	}
	hookAnnotations := ""
	if g.lifecycleHooks != nil {
		payload := normalized.Payload
		if len(payload) == 0 {
			payload, _ = json.Marshal(normalized.Arguments)
		}
		result, err := g.lifecycleHooks.Execute(ctx, hooks.Input{Event: hooks.PreTool,
			RunID: normalized.RunID, WorkspaceID: normalized.WorkspaceID,
			ToolName: string(normalized.Name), Payload: payload})
		if err != nil {
			return Outcome{}, fmt.Errorf("run pre-tool lifecycle hooks: %w", err)
		}
		if result.Denied {
			return deniedOutcome(normalized, policy.Decision{Allowed: false,
				Reason: "restricted lifecycle hook denied the call",
				Risk:   "high"})
		}
		if len(normalized.Payload) > 0 && !bytes.Equal(result.Payload, normalized.Payload) {
			normalized.Payload = append(json.RawMessage(nil), result.Payload...)
		} else if len(normalized.Payload) == 0 && !bytes.Equal(result.Payload, payload) {
			decoder := json.NewDecoder(bytes.NewReader(result.Payload))
			decoder.DisallowUnknownFields()
			var narrowed map[string]string
			if err := decoder.Decode(&narrowed); err != nil {
				return Outcome{}, fmt.Errorf("pre-tool hook returned invalid narrowed arguments: %w", err)
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				return Outcome{}, errors.New("pre-tool hook returned trailing narrowed arguments")
			}
			normalized.Arguments = narrowed
		}
		if err := validateToolArguments(normalized); err != nil {
			return Outcome{}, fmt.Errorf("pre-tool hook produced an invalid narrowed call: %w", err)
		}
		if len(result.Annotations) > 0 {
			encoded, _ := json.Marshal(result.Annotations)
			hookAnnotations = redact.String(string(encoded))
		}
		defer func() {
			status := "failed"
			if returnedErr == nil {
				status = "completed"
			}
			if outcome.Result != nil && hookAnnotations != "" {
				if outcome.Result.Metadata == nil {
					outcome.Result.Metadata = make(map[string]string)
				}
				outcome.Result.Metadata["hook_annotations"] = hookAnnotations
				outcome.Result.Metadata["hook_annotations_untrusted"] = "true"
			}
			postPayload, _ := json.Marshal(map[string]any{"status": status,
				"allowed": outcome.Decision.Allowed, "has_result": outcome.Result != nil})
			_, _ = g.lifecycleHooks.Execute(context.WithoutCancel(ctx), hooks.Input{
				Event: hooks.PostTool, RunID: normalized.RunID,
				WorkspaceID: normalized.WorkspaceID, ToolName: string(normalized.Name),
				Payload: postPayload})
		}()
	}
	if normalized.InvocationID != "" {
		return Outcome{}, errors.New("tool invocation id is generated by the gateway")
	}
	registration, found := lookupTool(normalized.Name)
	if !found || registration.invoke == nil {
		return Outcome{}, fmt.Errorf("unsupported tool %q", normalized.Name)
	}
	if registration.preflight != nil {
		if err := registration.preflight(g, normalized); err != nil {
			return Outcome{}, err
		}
	}
	fallback := waitgraph.External(normalized.RequestedBy)
	if normalized.AgentID != "" {
		fallback = waitgraph.Agent(normalized.AgentID)
	} else if normalized.RequestedBy == "" {
		fallback = waitgraph.External("operator")
	}
	ctx, releaseWait, err := waitgraph.Enter(ctx, g.waitGraph, fallback,
		waitgraph.Tool(idgen.New("tool-wait")))
	if err != nil {
		return Outcome{}, fmt.Errorf("register synchronous tool dependency: %w", err)
	}
	defer releaseWait()
	if g.budgetStore != nil {
		usage, err := g.budgetStore.ChargeToolCall(ctx, toolBudgetRequest(normalized, registration.definition.Class))
		if err != nil {
			return Outcome{}, err
		}
		if usage.Tracked {
			normalized.RunID = usage.RunID
			normalized.InvocationID = usage.LastCharge
		}
	}
	if normalized.RequestedBy == "specialist_runner" && normalized.Name != SpecialistTool {
		if err := g.markWorkerDispatchStarted(ctx, normalized); err != nil {
			return Outcome{}, err
		}
	}
	return registration.invoke(g, ctx, normalized)
}

func (g *Gateway) Review(ctx context.Context, request ReviewRequest) (Outcome, error) {
	if g == nil || g.store == nil {
		return Outcome{}, errors.New("tool gateway store is required")
	}
	normalized, err := NormalizeReviewRequest(request)
	if err != nil {
		return Outcome{}, err
	}
	switch normalized.Tool {
	case ShellTool:
		return g.reviewShell(ctx, normalized)
	case ReplaceFileTool:
		return g.reviewFileEdit(ctx, normalized)
	case ScriptProcessTool:
		return g.reviewScriptProcess(ctx, normalized)
	default:
		return Outcome{}, fmt.Errorf("tool %q does not support review", normalized.Tool)
	}
}

func (g *Gateway) invokeWorkspaceRead(ctx context.Context, call ToolCall) (Outcome, error) {
	if call.WorkspaceID == "" {
		return Outcome{}, errors.New("workspace read requires a workspace id")
	}
	root, err := g.bindWorkspaceRoot(ctx, call.WorkspaceID, call.WorkspaceRoot)
	if err != nil {
		return Outcome{}, err
	}
	call.WorkspaceRoot = root
	toolCall := tools.Call{Name: string(call.Name), Args: cloneArguments(call.Arguments), WorkingDir: call.WorkspaceRoot}
	policyDecision := g.checker.CheckToolCall(toolCall)
	if !policyDecision.Allowed {
		return deniedOutcome(call, policyDecision)
	}
	if policyDecision.NeedsApproval {
		policyDecision.Allowed = false
		policyDecision.Risk = defaultRisk(policyDecision.Risk, "medium")
		policyDecision.Reason = "scoped read required approval and was not executed: " + policyDecision.Reason
		return deniedOutcome(call, policyDecision)
	}
	decision, err := gatewayDecision(policyDecision, ApprovalAutomatic, "low")
	if err != nil {
		return Outcome{}, err
	}
	fs, err := g.projectWorkspaceFS(ctx, call)
	if err != nil {
		return Outcome{}, err
	}
	registry := tools.NewRegistry()
	registry.Register(tools.ReadFileTool{FS: fs})
	registry.Register(tools.ListWorkspaceTool{FS: fs})
	started := time.Now().UTC()
	legacyResult, runErr := registry.Run(ctx, toolCall)
	completed := time.Now().UTC()
	status := StatusCompleted
	if runErr != nil {
		status = StatusFailed
		if strings.TrimSpace(legacyResult.Stderr) == "" {
			legacyResult.Stderr = runErr.Error()
		}
	}
	mime := strings.TrimSpace(legacyResult.MIME)
	if mime == "" {
		mime = "text/plain; charset=utf-8"
	}
	safeStdout := redact.String(strings.ToValidUTF8(legacyResult.Stdout, "?"))
	safeStderr := redact.String(strings.ToValidUTF8(legacyResult.Stderr, "?"))
	artifactMetadata := map[string]string(nil)
	var captureErr error
	if call.InvocationID != "" {
		artifactMetadata, captureErr = g.captureTerminalArtifacts(ctx, call, call.InvocationID,
			safeStdout, safeStderr, mime)
	}
	stdout, stdoutTruncated := boundResultText(safeStdout, MaxResultStdoutBytes)
	stderr, stderrTruncated := boundResultText(safeStderr, MaxResultStderrBytes)
	outcome := Outcome{
		Call: safeToolCall(call), Decision: decision,
		Execution: &Execution{Backend: "workspace_fs", Status: status, StartedAt: started, CompletedAt: &completed},
		Result: &Result{
			Status: status, Stdout: stdout, Stderr: stderr, ExitCode: legacyResult.ExitCode, MIME: mime,
			Truncated: legacyResult.Truncated || stdoutTruncated || stderrTruncated,
			Metadata:  artifactMetadata, CompletedAt: completed,
		},
	}
	return validateOutcome(outcome, errors.Join(runErr, captureErr))
}

func (g *Gateway) invokeShellProposal(ctx context.Context, call ToolCall) (Outcome, error) {
	if g.store == nil {
		return Outcome{}, errors.New("tool gateway store is required")
	}
	if call.SessionID == "" {
		return Outcome{}, errors.New("shell proposal requires a session id")
	}
	run, err := g.legacyTools.ProposeShell(ctx, call.SessionID, call.WorkspaceID, call.Arguments["command"])
	if err != nil {
		return Outcome{}, err
	}
	if run.Status == toolrun.StatusProposed {
		grant, found, grantErr := g.activeSessionGrant(ctx, call, ClassShell)
		if grantErr != nil {
			return g.outcomeFromToolRun(ctx, call, run, grantErr)
		}
		if found {
			if _, grantErr = g.grantStore.AuthorizeApprovalWithSessionGrant(ctx, run.ID, grant.ID); grantErr != nil {
				return g.outcomeFromToolRun(ctx, call, run, grantErr)
			}
			run, err = g.legacyTools.Approve(ctx, run.ID)
			outcome, mappedErr := g.outcomeFromToolRun(ctx, call, run, err)
			return outcomeAuthorizedBySessionGrant(outcome, mappedErr)
		}
	}
	return g.outcomeFromToolRun(ctx, call, run, nil)
}

func (g *Gateway) invokeFileEditProposal(ctx context.Context, call ToolCall) (Outcome, error) {
	if g.store == nil {
		return Outcome{}, errors.New("tool gateway store is required")
	}
	if call.WorkspaceID == "" {
		return Outcome{}, errors.New("file edit proposal requires a workspace id")
	}
	root, err := g.bindWorkspaceRoot(ctx, call.WorkspaceID, call.WorkspaceRoot)
	if err != nil {
		return Outcome{}, err
	}
	call.WorkspaceRoot = root
	policyDecision := g.checker.CheckToolCall(tools.Call{
		Name: string(call.Name), Args: cloneArguments(call.Arguments), WorkingDir: call.WorkspaceRoot,
	})
	if !policyDecision.Allowed {
		return deniedOutcome(call, policyDecision)
	}
	edit, err := g.legacyEdits.Propose(ctx, fileedit.Proposal{
		SessionID: call.SessionID, WorkspaceID: call.WorkspaceID, WorkspaceRoot: call.WorkspaceRoot,
		Path: call.Arguments["path"], ProposedText: call.Arguments["content"],
	})
	if err != nil {
		return Outcome{}, err
	}
	grant, found, grantErr := g.activeSessionGrant(ctx, call, ClassWorkspaceWrite)
	if grantErr != nil {
		return g.outcomeFromFileEdit(ctx, call, edit, grantErr)
	}
	if found {
		if _, grantErr = g.grantStore.AuthorizeApprovalWithSessionGrant(ctx, edit.ID, grant.ID); grantErr != nil {
			return g.outcomeFromFileEdit(ctx, call, edit, grantErr)
		}
		edit, err = g.legacyEdits.Approve(ctx, edit.ID, root)
		outcome, mappedErr := g.outcomeFromFileEdit(ctx, call, edit, err)
		return outcomeAuthorizedBySessionGrant(outcome, mappedErr)
	}
	return g.outcomeFromFileEdit(ctx, call, edit, nil)
}

func (g *Gateway) activeSessionGrant(ctx context.Context, call ToolCall, class ActionClass) (approval.SessionGrant, bool, error) {
	if g.grantStore == nil || call.SessionID == "" {
		return approval.SessionGrant{}, false, nil
	}
	return g.grantStore.FindActiveSessionGrant(ctx, approval.GrantQuery{
		RunID: call.RunID, SessionID: call.SessionID, WorkspaceID: call.WorkspaceID,
		ToolName: string(call.Name), ActionClass: string(class),
	})
}

func outcomeAuthorizedBySessionGrant(outcome Outcome, operationErr error) (Outcome, error) {
	if outcome.Decision.Allowed {
		outcome.Decision.Approval = ApprovalSession
		outcome.Decision.Reason = "authorized by active session grant"
	}
	return validateOutcome(outcome, operationErr)
}

func (g *Gateway) reviewShell(ctx context.Context, request ReviewRequest) (Outcome, error) {
	before, err := g.legacyTools.Get(ctx, request.ProposalID)
	if err != nil {
		return Outcome{}, err
	}
	reason := request.Reason
	if request.Action == ReviewDeny && reason == "" {
		reason = "denied by operator"
	}
	request.Reason = reason
	record, err := g.persistReviewDecision(ctx, request, approvalProposalFromToolRun(before))
	if err != nil {
		return Outcome{}, err
	}
	var after toolrun.ToolRun
	if request.Action == ReviewApprove {
		after, err = g.legacyTools.Approve(ctx, request.ProposalID)
	} else {
		after, err = g.legacyTools.Deny(ctx, request.ProposalID, reason)
	}
	if after.ID == "" {
		after = before
	}
	call := ToolCall{
		Name: ShellTool, Arguments: map[string]string{"command": before.Command},
		RunID: record.RunID, SessionID: before.SessionID, WorkspaceID: before.WorkspaceID, RequestedBy: "approval_service",
	}
	return g.outcomeFromToolRun(ctx, call, after, err)
}

func (g *Gateway) reviewFileEdit(ctx context.Context, request ReviewRequest) (Outcome, error) {
	before, err := g.legacyEdits.Get(ctx, request.ProposalID)
	if err != nil {
		return Outcome{}, err
	}
	root := ""
	if request.Action == ReviewApprove {
		root, err = g.bindWorkspaceRoot(ctx, before.WorkspaceID, request.WorkspaceRoot)
		if err != nil {
			return Outcome{}, err
		}
	}
	reason := request.Reason
	if request.Action == ReviewDeny && reason == "" {
		reason = "denied by operator"
	}
	request.Reason = reason
	record, err := g.persistReviewDecision(ctx, request, approvalProposalFromFileEdit(before))
	if err != nil {
		return Outcome{}, err
	}
	var after fileedit.Edit
	if request.Action == ReviewApprove {
		after, err = g.legacyEdits.Approve(ctx, request.ProposalID, root)
	} else {
		after, err = g.legacyEdits.Deny(ctx, request.ProposalID, reason)
	}
	if after.ID == "" {
		after = before
	}
	call := ToolCall{
		Name: ReplaceFileTool, Arguments: map[string]string{"path": before.Path, "content": before.ProposedText},
		RunID: record.RunID, SessionID: before.SessionID, WorkspaceID: before.WorkspaceID, RequestedBy: "approval_service",
	}
	return g.outcomeFromFileEdit(ctx, call, after, err)
}

func (g *Gateway) reviewScriptProcess(ctx context.Context, request ReviewRequest) (Outcome, error) {
	before, err := g.scriptProcesses.Get(ctx, request.ProposalID)
	if err != nil {
		return Outcome{}, err
	}
	reason := request.Reason
	if request.Action == ReviewDeny && reason == "" {
		reason = "denied by operator"
	}
	request.Reason = reason
	if _, err := g.persistReviewDecision(ctx, request, approvalProposalFromScriptProcess(before)); err != nil {
		return Outcome{}, err
	}
	var after scriptprocess.Process
	if request.Action == ReviewApprove {
		after, err = g.scriptProcesses.Approve(ctx, request.ProposalID)
	} else {
		after, err = g.scriptProcesses.Deny(ctx, request.ProposalID, reason)
	}
	if after.ID == "" {
		after = before
	}
	call := ToolCall{
		Name: ScriptProcessTool, RunID: before.RunID, SessionID: before.SessionID,
		WorkspaceID: before.WorkspaceID, RequestedBy: "approval_service",
	}
	return g.outcomeFromScriptProcess(ctx, call, after, err)
}

func (g *Gateway) persistReviewDecision(ctx context.Context, request ReviewRequest, proposal approval.Proposal) (approval.Record, error) {
	if _, err := g.store.EnsureApproval(ctx, proposal); err != nil {
		return approval.Record{}, err
	}
	action := approval.ActionApprove
	if request.Action == ReviewDeny {
		action = approval.ActionDeny
	}
	result, err := g.store.DecideApproval(ctx, approval.DecisionRequest{
		ProposalID: proposal.ProposalID, IdempotencyKey: request.IdempotencyKey,
		Action: action, Reason: request.Reason, ReviewedBy: request.ReviewedBy,
	})
	return result.Approval, err
}

func approvalProposalFromToolRun(run toolrun.ToolRun) approval.Proposal {
	status := approval.StatusPending
	mode := string(ApprovalPerCall)
	reviewer := ""
	var decidedAt *time.Time
	switch run.Status {
	case toolrun.StatusDenied:
		status = approval.StatusDenied
		mode = string(ApprovalNever)
		reviewer = "legacy_recovery"
		decided := run.UpdatedAt
		decidedAt = &decided
	case toolrun.StatusApproved, toolrun.StatusRunning, toolrun.StatusCompleted, toolrun.StatusFailed:
		status = approval.StatusApproved
		reviewer = "legacy_recovery"
		decided := run.UpdatedAt
		decidedAt = &decided
	}
	return approval.Proposal{
		IdempotencyKey: approval.ProposalIdempotencyKey(string(ShellTool), run.ID), ProposalID: run.ID,
		SessionID: run.SessionID, WorkspaceID: run.WorkspaceID, ToolName: string(ShellTool), ActionClass: string(ClassShell),
		Mode: mode, Status: status, RequestFingerprint: approval.ShellFingerprint(run.SessionID, run.WorkspaceID, run.Command),
		DecisionReason: run.PolicyReason, RequestedBy: "tool_gateway", ReviewedBy: reviewer,
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, DecidedAt: decidedAt,
	}
}

func approvalProposalFromFileEdit(edit fileedit.Edit) approval.Proposal {
	status := approval.StatusPending
	reviewer := ""
	var decidedAt *time.Time
	switch edit.Status {
	case fileedit.StatusDenied:
		status = approval.StatusDenied
		reviewer = "legacy_recovery"
		decided := edit.UpdatedAt
		decidedAt = &decided
	case fileedit.StatusApproved, fileedit.StatusApplied, fileedit.StatusFailed:
		status = approval.StatusApproved
		reviewer = "legacy_recovery"
		decided := edit.UpdatedAt
		decidedAt = &decided
	}
	toolName := fileedit.ApprovalToolName(edit)
	fingerprint := approval.FileEditFingerprint(edit.SessionID, edit.WorkspaceID,
		edit.Path, edit.ProposedHash)
	if edit.Operation != "" && edit.Operation != fileedit.OperationReplace {
		fingerprint = approval.FileMutationFingerprint(toolName, edit.SessionID,
			edit.WorkspaceID, edit.Operation, edit.Path, edit.DestinationPath,
			edit.OriginalHash, edit.ProposedHash, edit.DestinationOriginalHash,
			edit.DestinationProposedHash)
	}
	return approval.Proposal{
		IdempotencyKey: approval.ProposalIdempotencyKey(toolName, edit.ID), ProposalID: edit.ID,
		SessionID: edit.SessionID, WorkspaceID: edit.WorkspaceID, ToolName: toolName,
		ActionClass: string(ClassWorkspaceWrite), Mode: string(ApprovalPerCall), Status: status,
		RequestFingerprint: fingerprint,
		DecisionReason:     edit.Reason, RequestedBy: "tool_gateway", ReviewedBy: reviewer,
		CreatedAt: edit.CreatedAt, UpdatedAt: edit.UpdatedAt, DecidedAt: decidedAt,
	}
}

func approvalProposalFromScriptProcess(process scriptprocess.Process) approval.Proposal {
	status := approval.StatusPending
	mode := string(ApprovalPerCall)
	reviewer := ""
	var decidedAt *time.Time
	switch process.Status {
	case scriptprocess.StatusDenied:
		status = approval.StatusDenied
		mode = string(ApprovalNever)
		reviewer = "policy"
		decided := process.UpdatedAt
		decidedAt = &decided
	case scriptprocess.StatusApproved, scriptprocess.StatusCompleted, scriptprocess.StatusFailed:
		status = approval.StatusApproved
		reviewer = "legacy_recovery"
		decided := process.UpdatedAt
		decidedAt = &decided
	}
	return approval.Proposal{
		IdempotencyKey: approval.ProposalIdempotencyKey(string(ScriptProcessTool), process.ID),
		ProposalID:     process.ID, SessionID: process.SessionID, WorkspaceID: process.WorkspaceID,
		ToolName: string(ScriptProcessTool), ActionClass: string(ClassProcess), Mode: mode, Status: status,
		RequestFingerprint: process.ApprovalFingerprint, DecisionReason: process.PolicyReason,
		RequestedBy: process.RequestedBy, ReviewedBy: reviewer, CreatedAt: process.CreatedAt,
		UpdatedAt: process.UpdatedAt, DecidedAt: decidedAt,
	}
}

func (g *Gateway) outcomeFromToolRun(ctx context.Context, call ToolCall, run toolrun.ToolRun, operationErr error) (Outcome, error) {
	if run.ID == "" {
		return Outcome{}, errors.Join(operationErr, errors.New("tool run record is required"))
	}
	call.Name = ShellTool
	call.SessionID = run.SessionID
	call.WorkspaceID = run.WorkspaceID
	call.Arguments = map[string]string{"command": run.Command}
	call.WorkspaceRoot = ""
	call, err := NormalizeToolCall(call)
	if err != nil {
		return Outcome{}, errors.Join(operationErr, err)
	}
	allowed := run.Status != toolrun.StatusDenied
	mode := ApprovalPerCall
	if run.Status == toolrun.StatusDenied {
		mode = ApprovalNever
	}
	risk := defaultRisk(run.Risk, "medium")
	reason := strings.TrimSpace(run.PolicyReason)
	if run.Status == toolrun.StatusDenied && (reason == "" || strings.HasPrefix(strings.ToLower(reason), "allowed")) {
		reason = "denied by operator"
	}
	if reason == "" {
		reason = "shell commands require explicit per-call approval"
	}
	decision, err := NormalizeDecision(Decision{Allowed: allowed, Approval: mode, Risk: risk, Reason: reason})
	if err != nil {
		return Outcome{}, errors.Join(operationErr, err)
	}
	status, ok := gatewayToolRunStatus(run.Status)
	if !ok {
		return Outcome{}, errors.Join(operationErr, fmt.Errorf("unsupported tool run status %q", run.Status))
	}
	proposalStatus := status
	if status == StatusRunning {
		proposalStatus = StatusApproved
	}
	proposal := &Proposal{
		ID: run.ID, Tool: ShellTool, Class: ClassShell, Status: proposalStatus,
		Preview: boundedPreview(run.Command), CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
	}
	outcome := Outcome{Call: safeToolCall(call), Decision: decision, Proposal: proposal}
	if status == StatusCompleted || status == StatusFailed {
		completed := run.UpdatedAt
		started := completed
		safeStdout := redact.String(strings.ToValidUTF8(run.Stdout, "?"))
		safeStderr := redact.String(strings.ToValidUTF8(run.Stderr, "?"))
		artifactMetadata, captureErr := g.captureTerminalArtifacts(ctx, call, run.ID, safeStdout, safeStderr,
			"text/plain; charset=utf-8")
		operationErr = errors.Join(operationErr, captureErr)
		stdout, stdoutTruncated := boundResultText(safeStdout, MaxResultStdoutBytes)
		stderr, stderrTruncated := boundResultText(safeStderr, MaxResultStderrBytes)
		outcome.Execution = &Execution{Backend: "dry_run", Status: status, StartedAt: started, CompletedAt: &completed}
		outcome.Result = &Result{
			Status: status, Stdout: stdout, Stderr: stderr, ExitCode: run.ExitCode,
			MIME: "text/plain; charset=utf-8", Truncated: stdoutTruncated || stderrTruncated,
			Metadata: artifactMetadata, CompletedAt: completed,
		}
	} else if status == StatusRunning {
		outcome.Execution = &Execution{Backend: "sandbox_pending", Status: StatusRunning, StartedAt: run.UpdatedAt}
	} else if status == StatusDenied {
		outcome.Result = deniedResult(decision.Reason, run.UpdatedAt)
	}
	return validateOutcome(outcome, operationErr)
}

func (g *Gateway) outcomeFromFileEdit(ctx context.Context, call ToolCall, edit fileedit.Edit, operationErr error) (Outcome, error) {
	if edit.ID == "" {
		return Outcome{}, errors.Join(operationErr, errors.New("file edit record is required"))
	}
	call.Name = ReplaceFileTool
	call.SessionID = edit.SessionID
	call.WorkspaceID = edit.WorkspaceID
	call.Arguments = map[string]string{"path": edit.Path, "content": edit.ProposedText}
	call.WorkspaceRoot = ""
	call, err := NormalizeToolCall(call)
	if err != nil {
		return Outcome{}, errors.Join(operationErr, err)
	}
	status, ok := gatewayFileEditStatus(edit.Status)
	if !ok {
		return Outcome{}, errors.Join(operationErr, fmt.Errorf("unsupported file edit status %q", edit.Status))
	}
	allowed := status != StatusDenied
	approval := ApprovalPerCall
	reason := "workspace file replacements require explicit per-call approval"
	if !allowed {
		approval = ApprovalNever
		if strings.TrimSpace(edit.Reason) != "" {
			reason = edit.Reason
		}
	}
	decision, err := NormalizeDecision(Decision{Allowed: allowed, Approval: approval, Risk: "medium", Reason: reason})
	if err != nil {
		return Outcome{}, errors.Join(operationErr, err)
	}
	proposal := &Proposal{
		ID: edit.ID, Tool: ReplaceFileTool, Class: ClassWorkspaceWrite, Status: status,
		Preview: boundedPreview(edit.Diff), CreatedAt: edit.CreatedAt, UpdatedAt: edit.UpdatedAt,
	}
	outcome := Outcome{Call: safeToolCall(call), Decision: decision, Proposal: proposal}
	if status == StatusCompleted || status == StatusFailed {
		completed := edit.UpdatedAt
		started := completed
		outcome.Execution = &Execution{Backend: "workspace_write", Status: status, StartedAt: started, CompletedAt: &completed}
		stderr := ""
		exitCode := 0
		if status == StatusFailed {
			stderr = edit.Reason
			exitCode = 1
		}
		stderr = redact.String(strings.ToValidUTF8(stderr, "?"))
		artifactMetadata, captureErr := g.captureTerminalArtifacts(ctx, call, edit.ID, "", stderr,
			"text/plain; charset=utf-8")
		operationErr = errors.Join(operationErr, captureErr)
		stderr, truncated := boundResultText(stderr, MaxResultStderrBytes)
		metadata := map[string]string{"path": redact.String(edit.Path)}
		for key, value := range artifactMetadata {
			metadata[key] = value
		}
		outcome.Result = &Result{
			Status: status, Stderr: stderr, ExitCode: exitCode, MIME: "text/plain; charset=utf-8", Truncated: truncated,
			Metadata: metadata, CompletedAt: completed,
		}
	} else if status == StatusDenied {
		outcome.Result = deniedResult(edit.Reason, edit.UpdatedAt)
	}
	return validateOutcome(outcome, operationErr)
}

func deniedOutcome(call ToolCall, source policy.Decision) (Outcome, error) {
	decision, err := gatewayDecision(source, ApprovalNever, "high")
	if err != nil {
		return Outcome{}, err
	}
	outcome := Outcome{Call: safeToolCall(call), Decision: decision, Result: deniedResult(decision.Reason, time.Now().UTC())}
	return validateOutcome(outcome, nil)
}

func deniedResult(reason string, at time.Time) *Result {
	stderr, truncated := boundResultText(reason, MaxResultStderrBytes)
	return &Result{
		Status: StatusDenied, Stderr: stderr, ExitCode: 126,
		MIME: "text/plain; charset=utf-8", Truncated: truncated, CompletedAt: at,
	}
}

func gatewayDecision(source policy.Decision, mode ApprovalMode, fallbackRisk string) (Decision, error) {
	if !source.Allowed {
		mode = ApprovalNever
	}
	reason := strings.TrimSpace(source.Reason)
	if reason == "" {
		reason = "tool call evaluated by policy"
	}
	return NormalizeDecision(Decision{
		Allowed: source.Allowed, Approval: mode, Risk: defaultRisk(source.Risk, fallbackRisk), Reason: reason,
	})
}

func validateToolArguments(call ToolCall) error {
	if call.Name == SpecialistTool {
		return validateSpecialistCall(call)
	}
	if call.Name == SkillReadTool {
		return validateSkillReadCall(call)
	}
	if IsHistoryRecallTool(call.Name) {
		return validateHistoryRecallCall(call)
	}
	if IsCodeIntelTool(call.Name) {
		if len(call.Arguments) != 0 || call.RunID == "" || call.MissionID == "" ||
			call.AgentID == "" || call.SessionID == "" || call.WorkspaceID == "" ||
			call.RequestedBy != "run_supervisor" || call.OperationKey == "" ||
			call.LeaseID == "" {
			return errors.New("code-intel tools require a structured fenced root Supervisor scope")
		}
		_, err := NormalizeSupervisorToolPayload(call.Name, call.Payload)
		return err
	}
	if isAgentCodeTool(call.Name) {
		worker := call.RequestedBy == "specialist_runner" && call.Role == domain.AgentRoleSpecialist && call.RootAgentID != "" && call.AgentAttemptID != "" && call.SpecialistToolCallID != ""
		if len(call.Arguments) != 0 || call.RunID == "" || call.MissionID == "" ||
			call.AgentID == "" || call.SessionID == "" || call.WorkspaceID == "" ||
			(!worker && call.RequestedBy != "run_supervisor") || call.OperationKey == "" ||
			call.LeaseID == "" {
			return errors.New("agent code tools require a structured fenced root Supervisor scope")
		}
		_, err := NormalizeSupervisorToolPayload(call.Name, call.Payload)
		return err
	}
	if IsSupervisorTool(call.Name) {
		if len(call.Arguments) != 0 {
			return fmt.Errorf("tool %s accepts a JSON payload instead of string arguments", call.Name)
		}
		if call.RunID == "" || call.SessionID == "" || call.RequestedBy == "" {
			return fmt.Errorf("tool %s requires Run, Session, and requester identities", call.Name)
		}
		if call.OperationKey == "" {
			return fmt.Errorf("tool %s requires an operation key", call.Name)
		}
		switch call.Name {
		case WorkItemCreateTool:
			// Memory admission validates the original fields; its catalog normalizer
			// also redacts and revalidates them, which can expand bounded text.
			_, _, err := decodeWorkItemCreateInput(call.Payload)
			return err
		case NoteCreateTool:
			_, _, err := decodeNoteCreateInput(call.Payload)
			return err
		case SpecialistDelegationProposeTool:
			if call.RequestedBy != "run_supervisor" || call.AgentID == "" || call.LeaseID == "" {
				return errors.New("specialist delegation proposals require a fenced root Supervisor")
			}
		case ChildTaskProposeTool:
			if call.RequestedBy != "run_supervisor" || call.AgentID == "" || call.LeaseID == "" {
				return errors.New("child task proposals require a fenced root Supervisor")
			}
		case PlanDeliveryProposeTool:
			if call.RequestedBy != "run_supervisor" || call.AgentID == "" || call.LeaseID == "" {
				return errors.New("Plan/Delivery proposals require a fenced root Supervisor")
			}
		case DockerSandboxRunProposeTool:
			if call.RequestedBy != "run_supervisor" || call.AgentID == "" ||
				call.WorkspaceID == "" || call.LeaseID == "" {
				return errors.New("Docker Sandbox proposals require a fenced root Supervisor")
			}
		case SkillCandidateProposeTool:
			if call.RequestedBy != "run_supervisor" || call.AgentID == "" ||
				call.WorkspaceID == "" || call.LeaseID == "" {
				return errors.New("Skill candidate proposals require a fenced root Supervisor")
			}
		case DebugTerminalTool:
			if call.RequestedBy != "run_supervisor" || call.AgentID == "" ||
				call.WorkspaceID == "" || call.LeaseID == "" {
				return errors.New("debug terminal calls require a fenced root Supervisor")
			}
		case CommandRuntimeTool, RunTestsTool:
			if !recordedExecutionActor(call) || call.AgentID == "" ||
				call.WorkspaceID == "" || call.LeaseID == "" {
				return errors.New("command runtime calls require a fenced root Supervisor")
			}
		case MCPToolCallTool, MCPToolSearchTool:
			if !recordedExecutionActor(call) || call.AgentID == "" ||
				call.WorkspaceID == "" || call.LeaseID == "" {
				return errors.New("MCP tool calls require a fenced host actor")
			}
		case BrowserStatusTool, BrowserNavigateTool, BrowserSnapshotTool,
			BrowserClickTool, BrowserTypeTool, BrowserScreenshotTool, BrowserScrollTool, BrowserKeyTool:
			if !recordedExecutionActor(call) || call.AgentID == "" ||
				call.LeaseID == "" {
				return errors.New("browser actions require a fenced root Supervisor")
			}
		case WebSearchTool, SourceSearchTool, WebFetchTool, WebCitationTool:
			if call.RequestedBy != "run_supervisor" || call.AgentID == "" ||
				call.MissionID == "" || call.LeaseID == "" {
				return errors.New("web evidence calls require a fenced root Supervisor")
			}
		}
		_, err := NormalizeSupervisorToolPayload(call.Name, call.Payload)
		return err
	}
	if len(call.Payload) != 0 || call.OperationKey != "" {
		return fmt.Errorf("tool %s does not accept a structured payload or operation key", call.Name)
	}
	var required []string
	var optional []string
	switch call.Name {
	case ReadFileTool:
		required = []string{"path"}
		optional = []string{"max_bytes"}
	case ListWorkspaceTool:
		optional = []string{"path", "max_depth"}
	case ShellTool:
		required = []string{"command"}
	case ReplaceFileTool:
		required = []string{"path", "content"}
	default:
		return fmt.Errorf("unsupported tool %q", call.Name)
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range append(required, optional...) {
		allowed[name] = struct{}{}
	}
	for name := range call.Arguments {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("tool %s does not accept argument %q", call.Name, name)
		}
	}
	for _, name := range required {
		value, ok := call.Arguments[name]
		if !ok || (name != "content" && strings.TrimSpace(value) == "") {
			return fmt.Errorf("tool %s requires argument %q", call.Name, name)
		}
	}
	if call.Name == ShellTool && len([]byte(strings.TrimSpace(call.Arguments["command"]))) > MaxCommandBytes {
		return fmt.Errorf("shell command exceeds %d bytes", MaxCommandBytes)
	}
	if value := strings.TrimSpace(call.Arguments["max_bytes"]); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > MaxResultStdoutBytes {
			return fmt.Errorf("read_file max_bytes must be between 1 and %d", MaxResultStdoutBytes)
		}
	}
	if value := strings.TrimSpace(call.Arguments["max_depth"]); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > maxWorkspaceListDepth {
			return fmt.Errorf("list_workspace max_depth must be between 0 and %d", maxWorkspaceListDepth)
		}
	}
	return nil
}

func gatewayToolRunStatus(status string) (Status, bool) {
	switch status {
	case toolrun.StatusProposed:
		return StatusProposed, true
	case toolrun.StatusApproved:
		return StatusApproved, true
	case toolrun.StatusDenied:
		return StatusDenied, true
	case toolrun.StatusRunning:
		return StatusRunning, true
	case toolrun.StatusCompleted:
		return StatusCompleted, true
	case toolrun.StatusFailed:
		return StatusFailed, true
	default:
		return "", false
	}
}

func gatewayFileEditStatus(status string) (Status, bool) {
	switch status {
	case fileedit.StatusProposed:
		return StatusProposed, true
	case fileedit.StatusApproved:
		return StatusApproved, true
	case fileedit.StatusApplied:
		return StatusCompleted, true
	case fileedit.StatusDenied:
		return StatusDenied, true
	case fileedit.StatusFailed:
		return StatusFailed, true
	default:
		return "", false
	}
}

func safeToolCall(call ToolCall) ToolCall {
	call.WorkspaceRoot = ""
	call.OperationKey = ""
	call.LeaseID = ""
	call.LeaseGeneration = 0
	call.ProviderFingerprint = ""
	call.ConnectorFingerprint = ""
	call.BrowserActionSessionID = ""
	call.BrowserPermissionSnapshotID = ""
	call.BrowserPermissionRevision = 0
	call.AgentBrowserAuthority = nil
	if len(call.Payload) > 0 {
		call.Payload = redactRunMutationPayload(call.Name, call.Payload)
	}
	call.Arguments = cloneArguments(call.Arguments)
	for name, value := range call.Arguments {
		call.Arguments[name] = redact.String(value)
	}
	call.RequestedBy = redact.String(call.RequestedBy)
	return call
}

func cloneArguments(arguments map[string]string) map[string]string {
	out := make(map[string]string, len(arguments))
	for name, value := range arguments {
		out[name] = value
	}
	return out
}

func boundResultText(value string, limit int) (string, bool) {
	value = redact.String(strings.ToValidUTF8(value, "?"))
	if limit <= 0 {
		return "", value != ""
	}
	if len([]byte(value)) <= limit {
		return value, false
	}
	const marker = "\n[truncated by tool gateway]\n"
	if limit <= len(marker) {
		return marker[:limit], true
	}
	contentLimit := max(0, limit-len(marker))
	data := []byte(value)
	data = data[:contentLimit]
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data) + marker, true
}

func boundedPreview(value string) string {
	value, _ = boundResultText(value, MaxProposalPreviewBytes)
	return value
}

func defaultRisk(value string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

func validateOutcome(outcome Outcome, operationErr error) (Outcome, error) {
	if err := outcome.Validate(); err != nil {
		return Outcome{}, errors.Join(operationErr, err)
	}
	return outcome, operationErr
}

func (g *Gateway) bindWorkspaceRoot(ctx context.Context, workspaceID string, provided string) (string, error) {
	provided = strings.TrimSpace(provided)
	if g.workspaceRootResolver == nil {
		if provided == "" {
			return "", errors.New("workspace root is required")
		}
		return provided, nil
	}
	expected, err := g.workspaceRootResolver(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return "", errors.New("resolved workspace root is empty")
	}
	if provided == "" {
		return expected, nil
	}
	expectedInfo, err := os.Stat(expected)
	if err != nil {
		return "", errors.New("resolved workspace root is unavailable")
	}
	providedInfo, err := os.Stat(provided)
	if err != nil {
		return "", errors.New("provided workspace root is unavailable")
	}
	if !expectedInfo.IsDir() || !providedInfo.IsDir() || !os.SameFile(expectedInfo, providedInfo) {
		return "", errors.New("provided workspace root does not match the workspace record")
	}
	return expected, nil
}
