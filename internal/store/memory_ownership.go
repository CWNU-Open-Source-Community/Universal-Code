package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

func requireAssignableAgentOwnerTx(ctx context.Context, tx *sql.Tx, runID string,
	agentID string,
) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}
	node, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return apperror.New(apperror.CodeInvalidArgument,
			"memory owner Agent does not exist")
	}
	if err != nil {
		return err
	}
	if node.RunID != strings.TrimSpace(runID) {
		return apperror.New(apperror.CodeInvalidArgument,
			"memory owner Agent must belong to the same Run")
	}
	if node.Terminal() {
		return apperror.New(apperror.CodeFailedPrecondition,
			"terminal Agent cannot receive new memory ownership")
	}
	return nil
}

func (s *SQLiteStore) noteViewerAgent(ctx context.Context, runID string,
	agentID string,
) (domain.AgentNode, error) {
	node, err := scanAgentNode(s.db.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`, strings.TrimSpace(agentID)))
	if err != nil {
		return domain.AgentNode{}, err
	}
	if node.RunID != strings.TrimSpace(runID) {
		return domain.AgentNode{}, apperror.New(apperror.CodeInvalidArgument,
			"note viewer Agent must belong to the same Run")
	}
	return node, nil
}
