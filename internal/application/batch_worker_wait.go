package application

import (
	"context"
	"errors"

	"cyberagent-workbench/internal/domain"
)

func batchWorkerApprovalWait(ctx context.Context, state any, child domain.AgentNode) (bool, bool, error) {
	if child.Worker == nil || child.Status != domain.AgentRunning || child.ActiveAttemptID == "" {
		return false, false, nil
	}
	reader, ok := state.(interface {
		SpecialistToolApprovalState(context.Context, domain.AgentAttemptRef) (bool, bool, error)
	})
	if !ok {
		return false, false, errors.New("isolated worker approval reader unavailable")
	}
	return reader.SpecialistToolApprovalState(ctx, domain.AgentAttemptRef{RunID: child.RunID, AgentID: child.ID, AttemptID: child.ActiveAttemptID})
}
