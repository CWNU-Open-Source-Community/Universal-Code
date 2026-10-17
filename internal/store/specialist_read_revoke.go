package store

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func (s *SQLiteStore) RevokeSpecialistReadTools(ctx context.Context, runID, agentID string) (domain.AgentNode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.AgentNode{}, err
	}
	defer tx.Rollback()
	node, err := scanAgentNode(tx.QueryRowContext(ctx, agentNodeSelect+` WHERE id=?`, agentID))
	if err != nil {
		return domain.AgentNode{}, err
	}
	if node.RunID != runID || node.Role != domain.AgentRoleSpecialist {
		return domain.AgentNode{}, apperror.New(apperror.CodeInvalidArgument, "read revocation requires a child in this Run")
	}
	if len(node.AllowedReadPaths) > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_nodes SET allowed_read_paths_json='[]',version=version+1,updated_at=? WHERE id=?`, ts(time.Now().UTC()), agentID); err != nil {
			return domain.AgentNode{}, err
		}
		run, _, err := getCoordinatorRunTx(ctx, tx, runID)
		if err != nil {
			return domain.AgentNode{}, err
		}
		if err = appendSupervisorEventTx(ctx, tx, run, "agent.read_tools.revoked", "operator", agentID, map[string]any{"agent_id": agentID}); err != nil {
			return domain.AgentNode{}, err
		}
		if _, err = createAgentGraphSnapshotTx(ctx, tx, run); err != nil {
			return domain.AgentNode{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return domain.AgentNode{}, err
	}
	return s.GetAgentNode(ctx, agentID)
}
