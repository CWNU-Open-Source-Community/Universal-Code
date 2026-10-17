package toolgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/tools"
)

// ChildTaskProposeTool lets the root model propose a bounded child task set.
// The Go control plane validates scope, surface, budgets, duplicates, and
// dependencies; the tool never creates Agents or fan-out plans itself.

// ChildTaskProposalContext is the fenced root-call scope for one proposal.
type ChildTaskProposalContext struct {
	RunID           string
	RootAgentID     string
	SessionID       string
	WorkspaceID     string
	InvocationID    string
	OperationKey    string
	LeaseID         string
	LeaseGeneration int64
	RequestedBy     string
	PolicyDecision  Decision
}

func (c ChildTaskProposalContext) Validate() error {
	for _, value := range []string{c.RunID, c.RootAgentID, c.SessionID,
		c.InvocationID, c.LeaseID, c.RequestedBy} {
		if strings.TrimSpace(value) == "" || len([]byte(value)) > MaxToolIdentityRunes {
			return fmt.Errorf("child task proposal scope identity is invalid")
		}
	}
	if strings.TrimSpace(c.WorkspaceID) == "" || len([]byte(c.WorkspaceID)) > MaxToolIdentityRunes {
		return fmt.Errorf("child task proposal workspace is invalid")
	}
	if c.LeaseGeneration <= 0 || strings.TrimSpace(c.OperationKey) == "" {
		return fmt.Errorf("child task proposal lease and operation key are required")
	}
	return nil
}

// ChildTaskProposalResult is the redacted tool result.
type ChildTaskProposalResult struct {
	ProposalID string
	Status     string
	Surface    domain.ChildTaskSurface
	FanoutTier domain.ReadOnlyFanoutTier
	TaskCount  int
	Replayed   bool
}

// Validate bounds the redacted tool result.
func (r ChildTaskProposalResult) Validate() error {
	if strings.TrimSpace(r.ProposalID) == "" || r.Status != domain.ChildTaskProposalProposed ||
		!domain.ValidChildTaskSurface(r.Surface) || r.TaskCount <= 0 ||
		r.TaskCount > domain.MaxChildTaskTasks {
		return fmt.Errorf("child task proposal result is invalid")
	}
	return nil
}

// ChildTaskProposalExecutor persists one validated proposal.
type ChildTaskProposalExecutor interface {
	ProposeChildTasks(ctx context.Context, scope ChildTaskProposalContext,
		spec domain.ChildTaskProposalSpec) (ChildTaskProposalResult, error)
}

var childTaskProposeDefinition = ToolDefinition{
	Name: ChildTaskProposeTool, Class: ClassAgentProposal, Approval: ApprovalAutomatic,
	Description: "Record one review-required proposal for a bounded child task set; Go picks core (max 2) or read-only fan-out (1/2/4/6).",
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["version","tasks"],"properties":{"version":{"const":"child_task_proposal.v1"},"tasks":{"type":"array","minItems":1,"maxItems":6,"items":{"type":"object","additionalProperties":false,"required":["title","goal","skills","turn_limit","token_limit","timeout_millis"],"properties":{"title":{"type":"string","minLength":1,"maxLength":256},"goal":{"type":"string","minLength":1,"maxLength":1200},"skills":{"type":"array","minItems":1,"maxItems":16,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":96,"pattern":"^[A-Za-z0-9._-]+$"}},"input_refs":{"type":"array","maxItems":16,"items":{"type":"string","minLength":1,"maxLength":2048}},"dependency_ordinals":{"type":"array","maxItems":6,"uniqueItems":true,"items":{"type":"integer","minimum":1,"maximum":6}},"surface_hint":{"enum":["auto","core","readonly_fanout"]},"turn_limit":{"type":"integer","minimum":1},"worker":{"type":"object","additionalProperties":false,"description":"Execution worker: omit worker to retain legacy read-only behavior; an empty object inherits currently available parent execution tools. Explicit tools narrows that set.","properties":{"tools":{"type":["array","null"],"maxItems":128,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":128}}}},"allowed_read_paths":{"type":"array","maxItems":32,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":1024}},"tool_call_limit":{"type":"integer","minimum":0},"cost_limit_micros":{"type":"integer","minimum":0},"token_limit":{"type":"integer","minimum":1},"timeout_millis":{"type":"integer","minimum":1,"maximum":1800000},"expected_artifacts":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"required":["path_hint","kind"],"properties":{"path_hint":{"type":"string","minLength":1,"maxLength":2048},"kind":{"type":"string","minLength":1,"maxLength":64}}}}}}}}}`),
}

func normalizeChildTaskProposalPayload(payload json.RawMessage) (domain.ChildTaskProposalSpec,
	json.RawMessage, error,
) {
	spec, err := domain.DecodeChildTaskProposalSpec(payload)
	if err != nil {
		return domain.ChildTaskProposalSpec{}, nil, err
	}
	for index := range spec.Tasks {
		spec.Tasks[index].Title = redact.String(spec.Tasks[index].Title)
		spec.Tasks[index].Goal = redact.String(spec.Tasks[index].Goal)
	}
	spec, err = domain.NormalizeChildTaskProposalSpec(spec)
	if err != nil {
		return domain.ChildTaskProposalSpec{}, nil,
			fmt.Errorf("redacted child task payload is invalid: %w", err)
	}
	canonical, err := json.Marshal(spec)
	if err != nil {
		return domain.ChildTaskProposalSpec{}, nil, err
	}
	return spec, canonical, nil
}

// WithChildTaskProposalExecutor installs the proposal executor.
func (g *Gateway) WithChildTaskProposalExecutor(executor ChildTaskProposalExecutor) *Gateway {
	if g != nil {
		g.childTaskProposals = executor
	}
	return g
}

func (g *Gateway) invokeChildTaskProposal(ctx context.Context, call ToolCall) (Outcome, error) {
	spec, canonical, err := normalizeChildTaskProposalPayload(call.Payload)
	if err != nil {
		return Outcome{}, err
	}
	call.Payload = canonical
	policyDecision := g.checker.CheckToolCall(tools.Call{
		Name: string(call.Name), Args: map[string]string{"payload": string(canonical)},
	})
	if !policyDecision.Allowed {
		if err := g.recordChildTaskPolicyDecision(ctx, call, policyDecision); err != nil {
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
	scope := ChildTaskProposalContext{
		InvocationID: call.InvocationID, OperationKey: call.OperationKey, RunID: call.RunID,
		RootAgentID: call.AgentID, SessionID: call.SessionID, WorkspaceID: call.WorkspaceID,
		LeaseID: call.LeaseID, LeaseGeneration: call.LeaseGeneration,
		RequestedBy: call.RequestedBy, PolicyDecision: decision,
	}
	if err := scope.Validate(); err != nil {
		return Outcome{}, err
	}
	started := time.Now().UTC()
	result, err := g.childTaskProposals.ProposeChildTasks(ctx, scope, spec)
	if err != nil {
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
				"proposal_id": result.ProposalID, "status": result.Status,
				"surface": string(result.Surface), "fanout_tier": string(result.FanoutTier),
				"task_count":           strconv.Itoa(result.TaskCount),
				"admission_authorized": "false", "replayed": strconv.FormatBool(result.Replayed),
			},
		},
	}
	return validateOutcome(outcome, nil)
}

func (g *Gateway) recordChildTaskPolicyDecision(ctx context.Context, call ToolCall,
	decision policy.Decision,
) error {
	if g == nil || g.policyRecorder == nil {
		return errors.New("child task policy decision recorder is required")
	}
	return g.policyRecorder.RecordPolicyDecision(ctx, policy.DecisionRecord{
		SessionID: call.SessionID, SubjectID: call.InvocationID,
		Context: "tool_run." + string(call.Name), Decision: decision,
	})
}
