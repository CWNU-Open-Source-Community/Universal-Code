package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/tools"
)

const (
	ScriptProcessSchema             = scriptprocess.Schema
	ScriptProcessExecutionDisabled  = scriptprocess.ExecutionDisabled
	ScriptProcessBackendSandbox     = scriptprocess.BackendSandbox
	ScriptProcessBackendLocal       = scriptprocess.BackendLocal
	MaxScriptProcessArguments       = scriptprocess.MaxArguments
	MaxScriptProcessExecutableBytes = scriptprocess.MaxExecutableBytes
	MaxScriptProcessArgumentBytes   = scriptprocess.MaxArgumentBytes
)

type ScriptProcessProposal = scriptprocess.Proposal

type ScriptRunSession struct {
	ID          string
	WorkspaceID string
	Title       string
	Route       string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (s ScriptRunSession) Validate() error {
	for label, value := range map[string]string{
		"id": s.ID, "workspace id": s.WorkspaceID, "title": s.Title, "route": s.Route, "status": s.Status,
	} {
		if strings.TrimSpace(value) != value || !utf8.ValidString(value) || len([]rune(value)) > MaxToolIdentityRunes {
			return fmt.Errorf("script Run session %s must be normalized and bounded UTF-8", label)
		}
	}
	if s.ID == "" || s.Title == "" || s.Route == "" || s.Status == "" || s.CreatedAt.IsZero() ||
		s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return errors.New("script Run session identity, metadata, and timestamps are required")
	}
	return nil
}

type ScriptRunStoreRequest struct {
	Mission       domain.Mission
	Run           domain.Run
	Mode          domain.RunModeSnapshot
	Session       ScriptRunSession
	CreateSession bool
	InitialEvents []events.Event
	Process       scriptprocess.Process
}

type ScriptRunStoreResult struct {
	Mission  domain.Mission
	Run      domain.Run
	Process  scriptprocess.Process
	Replayed bool
}

type ScriptRunCreateRequest struct {
	OperationKey  string
	Mission       domain.Mission
	Run           domain.Run
	Mode          domain.RunModeSnapshot
	Session       ScriptRunSession
	CreateSession bool
	InitialEvents []events.Event
	Call          ToolCall
	Proposal      ScriptProcessProposal
}

type ScriptRunCreateResult struct {
	Mission  domain.Mission
	Run      domain.Run
	Process  scriptprocess.Process
	Outcome  Outcome
	Replayed bool
}

func NormalizeScriptProcessProposal(proposal ScriptProcessProposal) (ScriptProcessProposal, error) {
	return scriptprocess.NormalizeProposal(proposal)
}

func EncodeScriptProcessProposal(proposal ScriptProcessProposal) (string, error) {
	return scriptprocess.EncodeProposal(proposal)
}

func (g *Gateway) ScriptProcesses() *scriptprocess.Manager {
	if g == nil {
		return scriptprocess.NewManager(nil)
	}
	return g.scriptProcesses
}

func (g *Gateway) CreateScriptProcessRun(ctx context.Context, request ScriptRunCreateRequest) (ScriptRunCreateResult, error) {
	if g == nil || g.scriptRunStore == nil {
		return ScriptRunCreateResult{}, errors.New("script Run store is required")
	}
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	if request.OperationKey == "" || !utf8.ValidString(request.OperationKey) ||
		len([]rune(request.OperationKey)) > MaxToolIdentityRunes {
		return ScriptRunCreateResult{}, errors.New("script Run idempotency key is required and bounded")
	}
	if err := validateScriptRunIdentity(request); err != nil {
		return ScriptRunCreateResult{}, err
	}
	call, proposal, decision, err := g.prepareScriptProcessCall(ctx, request.Call, request.Proposal)
	if err != nil {
		return ScriptRunCreateResult{}, err
	}
	request.Call = call
	request.Proposal = proposal
	requestFingerprint, err := scriptRunRequestFingerprint(request)
	if err != nil {
		return ScriptRunCreateResult{}, err
	}
	process := newScriptProcess(call, proposal, decision, scriptprocess.OperationKeyDigest(request.OperationKey), requestFingerprint)
	stored, err := g.scriptRunStore.CreateScriptProcessRun(ctx, ScriptRunStoreRequest{
		Mission: request.Mission, Run: request.Run, Mode: request.Mode,
		Session: request.Session, CreateSession: request.CreateSession,
		InitialEvents: append([]events.Event(nil), request.InitialEvents...), Process: process,
	})
	if err != nil {
		return ScriptRunCreateResult{}, err
	}
	outcome, mapErr := g.outcomeFromScriptProcess(ctx, call, stored.Process, nil)
	if mapErr != nil {
		return ScriptRunCreateResult{}, mapErr
	}
	return ScriptRunCreateResult{
		Mission: stored.Mission, Run: stored.Run, Process: stored.Process, Outcome: outcome, Replayed: stored.Replayed,
	}, nil
}

func (g *Gateway) ProposeScriptProcess(ctx context.Context, call ToolCall, proposal ScriptProcessProposal) (Outcome, error) {
	if g == nil || g.scriptStore == nil {
		return Outcome{}, errors.New("script process store is required")
	}
	if strings.TrimSpace(call.RunID) == "" {
		return Outcome{}, errors.New("script process proposal requires a run id")
	}
	preparedCall, normalized, decision, err := g.prepareScriptProcessCall(ctx, call, proposal)
	if err != nil {
		return Outcome{}, err
	}
	usage, err := g.budgetStore.ChargeToolCall(ctx, toolBudgetRequest(preparedCall, ClassProcess))
	if err != nil {
		return Outcome{}, err
	}
	if usage.Tracked {
		preparedCall.RunID = usage.RunID
	}
	requestFingerprint, err := standaloneScriptRequestFingerprint(preparedCall, normalized)
	if err != nil {
		return Outcome{}, err
	}
	process := newScriptProcess(preparedCall, normalized, decision,
		scriptprocess.OperationKeyDigest(idgen.New("scriptop")), requestFingerprint)
	process, err = g.scriptStore.SaveScriptProcess(ctx, process)
	if err != nil {
		return Outcome{}, err
	}
	return g.outcomeFromScriptProcess(ctx, preparedCall, process, nil)
}

func (g *Gateway) prepareScriptProcessCall(ctx context.Context, call ToolCall, proposal ScriptProcessProposal) (ToolCall, ScriptProcessProposal, Decision, error) {
	if call.Name != "" && call.Name != ScriptProcessTool {
		return ToolCall{}, ScriptProcessProposal{}, Decision{}, fmt.Errorf("script process cannot use tool %q", call.Name)
	}
	if len(call.Arguments) != 0 {
		return ToolCall{}, ScriptProcessProposal{}, Decision{}, errors.New("script process call arguments are generated by the gateway")
	}
	call.Name = ScriptProcessTool
	call.Arguments = map[string]string{}
	if strings.TrimSpace(call.RequestedBy) == "" {
		call.RequestedBy = "script_process"
	}
	call, err := NormalizeToolCall(call)
	if err != nil {
		return ToolCall{}, ScriptProcessProposal{}, Decision{}, err
	}
	if call.SessionID == "" || call.WorkspaceID == "" {
		return ToolCall{}, ScriptProcessProposal{}, Decision{}, errors.New("script process requires session and workspace ids")
	}
	root, err := g.bindWorkspaceRoot(ctx, call.WorkspaceID, call.WorkspaceRoot)
	if err != nil {
		return ToolCall{}, ScriptProcessProposal{}, Decision{}, err
	}
	call.WorkspaceRoot = root
	normalized, err := scriptprocess.NormalizeProposal(proposal)
	if err != nil {
		return ToolCall{}, ScriptProcessProposal{}, Decision{}, err
	}
	payload, err := scriptprocess.EncodeProposal(normalized)
	if err != nil {
		return ToolCall{}, ScriptProcessProposal{}, Decision{}, err
	}
	policyDecision := g.checker.CheckToolCall(tools.Call{
		Name: string(ScriptProcessTool), Args: map[string]string{"proposal": payload}, WorkingDir: root,
	})
	mode := ApprovalPerCall
	fallbackRisk := "medium"
	if !policyDecision.Allowed {
		mode = ApprovalNever
		fallbackRisk = "high"
	}
	decision, err := gatewayDecision(policyDecision, mode, fallbackRisk)
	if err != nil {
		return ToolCall{}, ScriptProcessProposal{}, Decision{}, err
	}
	return call, normalized, decision, nil
}

func validateScriptRunIdentity(request ScriptRunCreateRequest) error {
	if err := request.Mission.Validate(); err != nil {
		return err
	}
	if err := request.Run.Validate(); err != nil {
		return err
	}
	if err := request.Mode.Validate(); err != nil {
		return err
	}
	if err := request.Session.Validate(); err != nil {
		return err
	}
	if request.Run.Status != domain.RunCreated || request.Run.MissionID != request.Mission.ID ||
		request.Run.SessionID != request.Session.ID || request.Mission.WorkspaceID != request.Session.WorkspaceID ||
		request.Call.RunID != request.Run.ID || request.Call.SessionID != request.Session.ID ||
		request.Call.WorkspaceID != request.Mission.WorkspaceID ||
		request.Mode.RunID != request.Run.ID || request.Mode.MissionID != request.Mission.ID ||
		request.Mode.Revision != 1 {
		return errors.New("script Run Mission, Run, Session, Workspace, and ToolCall identities do not match")
	}
	if len(request.InitialEvents) == 0 {
		return errors.New("script Run initial events are required")
	}
	return nil
}

func scriptRunRequestFingerprint(request ScriptRunCreateRequest) (string, error) {
	proposal, err := scriptprocess.EncodeProposal(request.Proposal)
	if err != nil {
		return "", err
	}
	intent := struct {
		Goal          string                  `json:"goal"`
		Profile       domain.Profile          `json:"profile"`
		ModeProtocol  string                  `json:"mode_protocol"`
		Surface       domain.ExecutionSurface `json:"surface"`
		Phase         domain.ExecutionPhase   `json:"phase"`
		ModePolicy    string                  `json:"mode_policy"`
		ModeScope     domain.Scope            `json:"mode_scope"`
		WorkspaceID   string                  `json:"workspace_id"`
		ModelRoute    string                  `json:"model_route"`
		Interactive   bool                    `json:"interactive"`
		Budget        domain.Budget           `json:"budget"`
		Existing      bool                    `json:"existing_session"`
		ExistingID    string                  `json:"existing_session_id,omitempty"`
		RequestedBy   string                  `json:"requested_by"`
		TypedProposal string                  `json:"typed_proposal"`
	}{
		Goal: request.Mission.Goal, Profile: request.Mission.Profile,
		ModeProtocol: request.Mode.ProtocolVersion, Surface: request.Mode.Surface,
		Phase: request.Mode.Phase, ModePolicy: request.Mode.PolicyVersion,
		ModeScope:   request.Mode.Scope,
		WorkspaceID: request.Mission.WorkspaceID,
		ModelRoute:  request.Run.Config.ModelRoute, Interactive: request.Run.Config.Interactive, Budget: request.Run.Budget,
		Existing: !request.CreateSession, RequestedBy: request.Call.RequestedBy, TypedProposal: proposal,
	}
	if !request.CreateSession {
		intent.ExistingID = request.Session.ID
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	return scriptprocess.Fingerprint("script_run_create.v1", string(encoded)), nil
}

func standaloneScriptRequestFingerprint(call ToolCall, proposal ScriptProcessProposal) (string, error) {
	payload, err := scriptprocess.EncodeProposal(proposal)
	if err != nil {
		return "", err
	}
	return scriptprocess.Fingerprint("script_process_proposal.v1", call.RunID, call.SessionID,
		call.WorkspaceID, call.RequestedBy, payload), nil
}

func newScriptProcess(call ToolCall, proposal ScriptProcessProposal, decision Decision, operationDigest string, requestFingerprint string) scriptprocess.Process {
	redactedProposal := proposal
	redactedProposal.Executable = redact.String(redactedProposal.Executable)
	redactedProposal.Arguments = append([]string(nil), redactedProposal.Arguments...)
	for index := range redactedProposal.Arguments {
		redactedProposal.Arguments[index] = redact.String(redactedProposal.Arguments[index])
	}
	status := scriptprocess.StatusProposed
	if !decision.Allowed {
		status = scriptprocess.StatusDenied
	}
	now := time.Now().UTC()
	approvalFingerprint := scriptprocess.Fingerprint("script_process_approval.v1", call.SessionID,
		call.WorkspaceID, requestFingerprint)
	return scriptprocess.Process{
		ID: idgen.New("process"), OperationKeyDigest: operationDigest, RunID: call.RunID,
		SessionID: call.SessionID, WorkspaceID: call.WorkspaceID, Executable: redactedProposal.Executable,
		Arguments: redactedProposal.Arguments, WorkingDirectory: redactedProposal.WorkingDirectory,
		RequestedBackend: redactedProposal.RequestedBackend, ExecutionMode: scriptprocess.ExecutionDisabled,
		Status: status, Risk: decision.Risk, PolicyReason: decision.Reason,
		RequestFingerprint: requestFingerprint, ApprovalFingerprint: approvalFingerprint,
		RequestedBy: redact.String(call.RequestedBy), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func (g *Gateway) outcomeFromScriptProcess(ctx context.Context, call ToolCall, process scriptprocess.Process, operationErr error) (Outcome, error) {
	call.Name = ScriptProcessTool
	call.RunID = process.RunID
	call.SessionID = process.SessionID
	call.WorkspaceID = process.WorkspaceID
	payload, encodeErr := scriptprocess.EncodeProposal(process.Proposal())
	if encodeErr != nil {
		return Outcome{}, errors.Join(operationErr, encodeErr)
	}
	call.Arguments = map[string]string{"proposal": payload}
	call.WorkspaceRoot = ""
	call, err := NormalizeToolCall(call)
	if err != nil {
		return Outcome{}, errors.Join(operationErr, err)
	}
	allowed := process.Status != scriptprocess.StatusDenied
	mode := ApprovalPerCall
	if !allowed {
		mode = ApprovalNever
	}
	decision, err := NormalizeDecision(Decision{
		Allowed: allowed, Approval: mode, Risk: process.Risk, Reason: process.PolicyReason,
	})
	if err != nil {
		return Outcome{}, errors.Join(operationErr, err)
	}
	status, ok := gatewayScriptProcessStatus(process.Status)
	if !ok {
		return Outcome{}, errors.Join(operationErr, fmt.Errorf("unsupported script process status %q", process.Status))
	}
	proposalStatus := status
	proposal := &Proposal{
		ID: process.ID, Tool: ScriptProcessTool, Class: ClassProcess, Status: proposalStatus,
		Preview: boundedPreview(payload), CreatedAt: process.CreatedAt, UpdatedAt: process.UpdatedAt,
	}
	outcome := Outcome{Call: safeToolCall(call), Decision: decision, Proposal: proposal}
	if status == StatusCompleted || status == StatusFailed {
		completed := process.UpdatedAt
		safeStdout := redact.String(strings.ToValidUTF8(process.Stdout, "?"))
		safeStderr := redact.String(strings.ToValidUTF8(process.Stderr, "?"))
		artifactMetadata, captureErr := g.captureTerminalArtifacts(ctx, call, process.ID,
			safeStdout, safeStderr, "text/plain; charset=utf-8")
		operationErr = errors.Join(operationErr, captureErr)
		stdout, stdoutTruncated := boundResultText(safeStdout, MaxResultStdoutBytes)
		stderr, stderrTruncated := boundResultText(safeStderr, MaxResultStderrBytes)
		outcome.Execution = &Execution{Backend: "dry_run", Status: status, StartedAt: completed, CompletedAt: &completed}
		outcome.Result = &Result{
			Status: status, Stdout: stdout, Stderr: stderr, ExitCode: process.ExitCode,
			MIME: "text/plain; charset=utf-8", Truncated: stdoutTruncated || stderrTruncated,
			Metadata: artifactMetadata, CompletedAt: completed,
		}
	} else if status == StatusDenied {
		outcome.Result = deniedResult(process.PolicyReason, process.UpdatedAt)
	}
	return validateOutcome(outcome, operationErr)
}

func gatewayScriptProcessStatus(status scriptprocess.Status) (Status, bool) {
	switch status {
	case scriptprocess.StatusProposed:
		return StatusProposed, true
	case scriptprocess.StatusApproved:
		return StatusApproved, true
	case scriptprocess.StatusDenied:
		return StatusDenied, true
	case scriptprocess.StatusCompleted:
		return StatusCompleted, true
	case scriptprocess.StatusFailed:
		return StatusFailed, true
	default:
		return "", false
	}
}

func toolBudgetRequest(call ToolCall, class ActionClass) toolbudget.ChargeRequest {
	name := string(call.Name)
	if call.Name == SpecialistTool {
		input, _, err := NormalizeSpecialistToolPayload(call.Payload)
		if err == nil {
			name = input.Tool
			switch ToolName(input.Tool) {
			case WorkspaceChangeTool, WorkspaceApplyTool, "git_commit":
				class = ClassWorkspaceWrite
			case "verify":
				class = ClassProcess
			}
		}
	}
	return toolbudget.ChargeRequest{
		RunID: call.RunID, SessionID: call.SessionID, WorkspaceID: call.WorkspaceID,
		ToolName: name, ActionClass: string(class), LeaseID: call.LeaseID,
		LeaseGeneration: call.LeaseGeneration, RequestedBy: call.RequestedBy,
		AgentID: call.AgentID, AgentAttemptID: call.AgentAttemptID, ToolCallID: call.SpecialistToolCallID,
	}
}
