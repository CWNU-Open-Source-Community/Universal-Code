package application

import (
	"context"

	"cyberagent-workbench/internal/mcp"
)

func (s *ExtensionControlService) ListMCPInputs(ctx context.Context, runID string) ([]mcp.InputView, error) {
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	return s.mcp.InputBroker().List(ctx, runID)
}
func (s *ExtensionControlService) RespondMCPInput(ctx context.Context, runID, id string, response mcp.InputResponse) error {
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		return err
	}
	return s.mcp.InputBroker().Respond(ctx, runID, id, response)
}
