package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/tools"
)

type SpecialistDelegationContext struct {
	InvocationID    string
	OperationKey    string
	RunID           string
	RootAgentID     string
	SessionID       string
	WorkspaceID     string
	LeaseID         string
	LeaseGeneration int64
	RequestedBy     string
	PolicyDecision  Decision
}

func (c SpecialistDelegationContext) Validate() error {
	for label, value := range map[string]string{
		"invocation id": c.InvocationID, "operation key": c.OperationKey, "run id": c.RunID,
		"root agent id": c.RootAgentID, "session id": c.SessionID, "workspace id": c.WorkspaceID,
		"lease id": c.LeaseID, "requester": c.RequestedBy,
	} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
			len([]rune(value)) > MaxToolIdentityRunes {
			return fmt.Errorf("specialist delegation %s must be normalized and bounded UTF-8", label)
		}
	}
	if c.InvocationID == "" || c.OperationKey == "" || c.RunID == "" || c.RootAgentID == "" ||
		c.SessionID == "" || c.LeaseID == "" || c.RequestedBy != "run_supervisor" ||
		c.LeaseGeneration <= 0 {
		return errors.New("specialist delegation requires a fenced root Supervisor scope")
	}
	if !domain.ValidAgentID(c.RootAgentID) {
		return errors.New("specialist delegation root Agent identity is invalid")
	}
	if err := c.PolicyDecision.Validate(); err != nil {
		return err
	}
	if !c.PolicyDecision.Allowed || c.PolicyDecision.Approval != ApprovalAutomatic {
		return errors.New("specialist delegation proposal creation requires an automatic allowed decision")
	}
	return nil
}

type SpecialistDelegationResult struct {
	ProposalID      string
	Status          domain.SpecialistDelegationStatus
	AssignmentCount int
	Version         int64
	Replayed        bool
}

func (r SpecialistDelegationResult) Validate() error {
	if !domain.ValidAgentID(r.ProposalID) || r.Status != domain.SpecialistDelegationProposed ||
		r.AssignmentCount <= 0 || r.AssignmentCount > domain.MaxSpecialistDelegationAssignments ||
		r.Version <= 0 {
		return errors.New("specialist delegation result is invalid")
	}
	return nil
}

type SpecialistDelegationExecutor interface {
	ProposeSpecialists(ctx context.Context, scope SpecialistDelegationContext,
		spec domain.SpecialistDelegationSpec) (SpecialistDelegationResult, error)
}

var specialistDelegationDefinition = ToolDefinition{
	Name: SpecialistDelegationProposeTool, Class: ClassAgentProposal, Approval: ApprovalAutomatic,
	Description: "Record one review-required proposal for up to two bounded Specialist assignments without creating Agents.",
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["version","assignments"],"properties":{"version":{"const":"specialist_delegation.v1"},"assignments":{"type":"array","minItems":1,"maxItems":2,"items":{"type":"object","additionalProperties":false,"required":["title","goal","skills","turn_limit","token_limit"],"properties":{"title":{"type":"string","minLength":1,"maxLength":256},"goal":{"type":"string","minLength":1,"maxLength":1200},"skills":{"type":"array","minItems":1,"maxItems":16,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":96,"pattern":"^[A-Za-z0-9._-]+$"}},"turn_limit":{"type":"integer","minimum":1},"worker":{"type":"object","additionalProperties":false,"description":"Execution worker: omit worker to retain legacy read-only behavior; an empty object inherits currently available parent execution tools. Explicit tools narrows that set.","properties":{"tools":{"type":["array","null"],"maxItems":128,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}}}},"allowed_read_paths":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":1024}},"tool_call_limit":{"type":"integer","minimum":0},"cost_limit_micros":{"type":"integer","minimum":0},"token_limit":{"type":"integer","minimum":1}}}}}}`),
}

func normalizeSpecialistDelegationPayload(payload json.RawMessage) (domain.SpecialistDelegationSpec,
	json.RawMessage, error,
) {
	spec, err := domain.DecodeSpecialistDelegationSpec(payload)
	if err != nil {
		return domain.SpecialistDelegationSpec{}, nil, err
	}
	for index := range spec.Assignments {
		spec.Assignments[index].Title = redact.String(spec.Assignments[index].Title)
		spec.Assignments[index].Goal = redact.String(spec.Assignments[index].Goal)
	}
	spec, err = domain.NormalizeSpecialistDelegationSpec(spec)
	if err != nil {
		return domain.SpecialistDelegationSpec{}, nil,
			fmt.Errorf("redacted specialist delegation payload is invalid: %w", err)
	}
	canonical, err := json.Marshal(spec)
	if err != nil {
		return domain.SpecialistDelegationSpec{}, nil, err
	}
	return spec, canonical, nil
}

func (g *Gateway) WithSpecialistDelegationExecutor(executor SpecialistDelegationExecutor) *Gateway {
	if g != nil {
		g.delegationProposals = executor
	}
	return g
}

func (g *Gateway) invokeSpecialistDelegation(ctx context.Context, call ToolCall) (Outcome, error) {
	spec, canonical, err := normalizeSpecialistDelegationPayload(call.Payload)
	if err != nil {
		return Outcome{}, err
	}
	call.Payload = canonical
	policyDecision := g.checker.CheckToolCall(tools.Call{
		Name: string(call.Name), Args: map[string]string{"payload": string(canonical)},
	})
	if !policyDecision.Allowed {
		if err := g.recordSpecialistDelegationPolicyDecision(ctx, call, policyDecision); err != nil {
			return Outcome{}, err
		}
		return deniedOutcome(call, policyDecision)
	}
	if policyDecision.NeedsApproval {
		policyDecision.Reason = "proposal recorded for mandatory operator review: " + policyDecision.Reason
	}
	decision, err := gatewayDecision(policyDecision, ApprovalAutomatic, "low")
	if err != nil {
		return Outcome{}, err
	}
	scope := SpecialistDelegationContext{
		InvocationID: call.InvocationID, OperationKey: call.OperationKey, RunID: call.RunID,
		RootAgentID: call.AgentID, SessionID: call.SessionID, WorkspaceID: call.WorkspaceID,
		LeaseID: call.LeaseID, LeaseGeneration: call.LeaseGeneration,
		RequestedBy: call.RequestedBy, PolicyDecision: decision,
	}
	if err := scope.Validate(); err != nil {
		return Outcome{}, err
	}
	started := time.Now().UTC()
	result, err := g.delegationProposals.ProposeSpecialists(ctx, scope, spec)
	if err != nil {
		return Outcome{}, err
	}
	if err := result.Validate(); err != nil {
		return Outcome{}, err
	}
	completed := time.Now().UTC()
	outcome := Outcome{
		Call: safeToolCall(call), Decision: decision,
		Execution: &Execution{Backend: "agent_proposal", Status: StatusCompleted,
			StartedAt: started, CompletedAt: &completed},
		Result: &Result{
			Status: StatusCompleted, ExitCode: 0, MIME: "application/json", CompletedAt: completed,
			Metadata: map[string]string{
				"proposal_id": result.ProposalID, "status": string(result.Status),
				"assignment_count":     strconv.Itoa(result.AssignmentCount),
				"version":              strconv.FormatInt(result.Version, 10),
				"admission_authorized": "false", "replayed": strconv.FormatBool(result.Replayed),
			},
		},
	}
	return validateOutcome(outcome, nil)
}

func (g *Gateway) recordSpecialistDelegationPolicyDecision(ctx context.Context, call ToolCall,
	decision policy.Decision,
) error {
	if g == nil || g.policyRecorder == nil {
		return errors.New("specialist delegation policy decision recorder is required")
	}
	return g.policyRecorder.RecordPolicyDecision(ctx, policy.DecisionRecord{
		SessionID: call.SessionID, SubjectID: call.InvocationID,
		Context: "tool_run." + string(call.Name), Decision: decision,
	})
}
