package toolgateway

import (
	"context"
	"errors"

	"cyberagent-workbench/internal/domain"
)

func recordedExecutionActor(call ToolCall) bool {
	if call.RequestedBy == "run_supervisor" {
		return call.SpecialistToolCallID == ""
	}
	return call.RequestedBy == "specialist_runner" && call.Role == domain.AgentRoleSpecialist && call.AgentID != "" && call.RootAgentID != "" && call.RootAgentID != call.AgentID && call.AgentAttemptID != "" && call.SpecialistToolCallID != "" && call.SupervisorToolCallID == ""
}

// Mark only after the gateway has charged the exact pending intent, and before
// entering the runtime. A started intent without a result is never dispatched
// again by resuming the worker.
func (g *Gateway) markWorkerDispatchStarted(ctx context.Context, call ToolCall) error {
	st, ok := g.store.(interface {
		GetSpecialistToolCall(context.Context, domain.AgentAttemptRef, string) (domain.SpecialistToolCall, error)
		MarkSpecialistToolStarted(context.Context, domain.AgentAttemptRef, string, string) (domain.SpecialistToolCall, error)
	})
	if !ok || !recordedExecutionActor(call) || call.InvocationID == "" {
		return errors.New("worker dispatch requires its charged durable intent")
	}
	ref := domain.AgentAttemptRef{RunID: call.RunID, AgentID: call.AgentID, AttemptID: call.AgentAttemptID}
	c, err := st.GetSpecialistToolCall(ctx, ref, call.SpecialistToolCallID)
	if err != nil {
		return err
	}
	if c.StartedAt != nil || c.Status != domain.SupervisorToolPending || c.ChargeID != call.InvocationID || c.Name != string(call.Name) || c.PayloadJSON != string(call.Payload) || c.AuthorityJSON == "" {
		return errors.New("worker intent changed or was already started; do not repeat an unknown outcome")
	}
	_, err = st.MarkSpecialistToolStarted(ctx, ref, c.CallID, c.AuthorityJSON)
	return err
}
