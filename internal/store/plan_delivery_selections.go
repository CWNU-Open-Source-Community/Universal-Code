package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/redact"
)

func hasPlanDeliveryManualAcceptance(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	var present bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('plan_delivery_selections') WHERE name='manual_acceptance')`).Scan(&present)
	return present, err
}

// alias is a source-code SQL alias, never request input. Historical schemas
// without an explicit selection policy retain required manual acceptance.
func planDeliveryManualAcceptanceSQL(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, alias string) (string, error) {
	present, err := hasPlanDeliveryManualAcceptance(ctx, queryer)
	if err != nil {
		return "", err
	}
	if !present {
		return "'required'", nil
	}
	if alias != "" {
		return alias + ".manual_acceptance", nil
	}
	return "manual_acceptance", nil
}

func planDeliverySelectionSelectSQL(ctx context.Context, queryer planDeliveryQueryer) (string, error) {
	acceptance, err := planDeliveryManualAcceptanceSQL(ctx, queryer, "")
	return `SELECT id, proposal_id, run_id, root_agent_id,
	direction_ordinal, note_id, module_count, requested_by, version, created_at, ` + acceptance + ` FROM plan_delivery_selections`, err
}

func (s *SQLiteStore) SelectPlanDeliveryDirection(ctx context.Context,
	operation domain.PlanDeliverySelectionOperation,
	selection domain.PlanDeliverySelection, items []domain.WorkItem,
	note domain.Note, selectionEvent events.Event,
	itemEvents []events.Event, noteEvent events.Event,
) (domain.PlanDeliverySelection, bool, error) {
	return s.selectPlanDeliveryDirection(ctx, "", operation, selection, items, note, selectionEvent, itemEvents, noteEvent)
}

func (s *SQLiteStore) SelectThreadPlanDeliveryDirection(ctx context.Context, threadID string,
	operation domain.PlanDeliverySelectionOperation, selection domain.PlanDeliverySelection, items []domain.WorkItem,
	note domain.Note, selectionEvent events.Event, itemEvents []events.Event, noteEvent events.Event,
) (domain.PlanDeliverySelection, bool, error) {
	return s.selectPlanDeliveryDirection(ctx, threadID, operation, selection, items, note, selectionEvent, itemEvents, noteEvent)
}

func (s *SQLiteStore) selectPlanDeliveryDirection(ctx context.Context, threadID string,
	operation domain.PlanDeliverySelectionOperation, selection domain.PlanDeliverySelection, items []domain.WorkItem,
	note domain.Note, selectionEvent events.Event, itemEvents []events.Event, noteEvent events.Event,
) (domain.PlanDeliverySelection, bool, error) {
	operation = normalizePlanDeliverySelectionOperation(operation)
	selection = normalizePlanDeliverySelection(selection)
	items = slices.Clone(items)
	for index := range items {
		items[index] = redactAndNormalizeWorkItem(items[index])
	}
	note = redactAndNormalizeNote(note)
	if err := validatePlanDeliverySelectionMutation(operation, selection, items,
		note, selectionEvent, itemEvents, noteEvent); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at=updated_at WHERE id=?`, selection.RunID); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	if existing, found, err := getPlanDeliverySelectionOperation(ctx, tx,
		operation.KeyDigest); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	} else if found {
		if err := validatePlanDeliverySelectionReplay(existing, operation); err != nil {
			return domain.PlanDeliverySelection{}, false, err
		}
		stored, err := getPlanDeliverySelection(ctx, tx, existing.SelectionID)
		if err != nil {
			return domain.PlanDeliverySelection{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.PlanDeliverySelection{}, false, err
		}
		return stored, true, nil
	}
	if err := acquireStructuredMutationWriteLockTx(ctx, tx, selection.RunID); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	if _, found, err := getPlanDeliverySelectionByRun(ctx, tx,
		selection.RunID); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	} else if found {
		return domain.PlanDeliverySelection{}, false, apperror.New(
			apperror.CodeConflict,
			"Run already has an immutable Plan/Delivery direction selection")
	}
	proposal, err := getPlanDeliveryProposal(ctx, tx, selection.ProposalID)
	if err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	if threadID != "" {
		if _, found, err := getRunModeOperation(ctx, tx, operation.KeyDigest); err != nil {
			return domain.PlanDeliverySelection{}, false, err
		} else if found {
			return domain.PlanDeliverySelection{}, false, apperror.New(apperror.CodeConflict, "Thread Plan key already changed a mode")
		}
		if err := requireUnusedThreadPlanSuccessorKeyTx(ctx, tx, threadID, operation.KeyDigest); err != nil {
			return domain.PlanDeliverySelection{}, false, err
		}
		if err := requireThreadPlanPreparationTx(ctx, tx, threadID, selection.RunID, proposal.ID); err != nil {
			return domain.PlanDeliverySelection{}, false, err
		}
	}
	run, mission, direction, err := requirePlanDeliverySelectionBindingTx(ctx,
		tx, proposal, selection)
	if err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	if err := validatePlanDeliverySelectionProjection(proposal, direction,
		selection, items, note); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	for _, event := range append(append([]events.Event{selectionEvent},
		itemEvents...), noteEvent) {
		if event.RunID != run.ID || event.MissionID != mission.ID ||
			!event.CreatedAt.Equal(selection.CreatedAt) {
			return domain.PlanDeliverySelection{}, false, apperror.New(
				apperror.CodeInvalidArgument,
				"Plan/Delivery selection event scope or timestamp does not match")
		}
	}
	for _, item := range items {
		if err := insertNewWorkItemTx(ctx, tx, item); err != nil {
			return domain.PlanDeliverySelection{}, false, err
		}
	}
	if err := insertNewNoteTx(ctx, tx, note); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	insert := `INSERT INTO plan_delivery_selections
		(id, proposal_id, run_id, root_agent_id, direction_ordinal, note_id,
		module_count, requested_by, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	arguments := []any{selection.ID,
		selection.ProposalID, selection.RunID, selection.RootAgentID,
		selection.DirectionOrdinal, selection.NoteID, len(selection.Items),
		selection.RequestedBy, selection.Version, ts(selection.CreatedAt)}
	if present, err := hasPlanDeliveryManualAcceptance(ctx, tx); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	} else if present {
		insert = `INSERT INTO plan_delivery_selections
		(id, proposal_id, run_id, root_agent_id, direction_ordinal, note_id,
		module_count, requested_by, version, created_at, manual_acceptance)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		arguments = append(arguments, selection.EffectiveManualAcceptance())
	} else if selection.EffectiveManualAcceptance() != domain.PlanDeliveryManualAcceptanceRequired {
		return domain.PlanDeliverySelection{}, false, apperror.New(apperror.CodeFailedPrecondition, "manual acceptance selection requires the current database schema")
	}
	if _, err := tx.ExecContext(ctx, insert, arguments...); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	for _, item := range selection.Items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO plan_delivery_selection_items
			(selection_id, ordinal, module_ordinal, work_item_id)
			VALUES (?, ?, ?, ?)`, selection.ID, item.Ordinal,
			item.ModuleOrdinal, item.WorkItemID); err != nil {
			return domain.PlanDeliverySelection{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plan_delivery_selection_operations
		(operation_key_digest, request_fingerprint, selection_id, proposal_id,
		run_id, requested_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, operation.KeyDigest,
		operation.RequestFingerprint, selection.ID, selection.ProposalID,
		selection.RunID, selection.RequestedBy,
		ts(selection.CreatedAt)); err != nil {
		_ = tx.Rollback()
		return s.recoverPlanDeliverySelection(ctx, operation, err)
	}
	for _, event := range append(append([]events.Event{selectionEvent},
		itemEvents...), noteEvent) {
		if _, err := insertRunEventTx(ctx, tx, event); err != nil {
			return domain.PlanDeliverySelection{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	return domain.ClonePlanDeliverySelection(selection), false, nil
}

func (s *SQLiteStore) GetPlanDeliverySelection(ctx context.Context,
	id string,
) (domain.PlanDeliverySelection, error) {
	id = strings.TrimSpace(id)
	if !domain.ValidAgentID(id) || strings.ContainsRune(id, 0) {
		return domain.PlanDeliverySelection{}, apperror.New(
			apperror.CodeInvalidArgument, "Plan/Delivery selection id is invalid")
	}
	return getPlanDeliverySelection(ctx, s.db, id)
}

func (s *SQLiteStore) GetPlanDeliverySelectionByRun(ctx context.Context,
	runID string,
) (domain.PlanDeliverySelection, bool, error) {
	runID = strings.TrimSpace(runID)
	if !domain.ValidAgentID(runID) || strings.ContainsRune(runID, 0) {
		return domain.PlanDeliverySelection{}, false, apperror.New(
			apperror.CodeInvalidArgument,
			"Plan/Delivery selection Run id is invalid")
	}
	return getPlanDeliverySelectionByRun(ctx, s.db, runID)
}

func (s *SQLiteStore) GetPlanDeliverySelectionOperation(ctx context.Context, keyDigest string) (domain.PlanDeliverySelectionOperation, bool, error) {
	return getPlanDeliverySelectionOperation(ctx, s.db, keyDigest)
}

func normalizePlanDeliverySelectionOperation(
	operation domain.PlanDeliverySelectionOperation,
) domain.PlanDeliverySelectionOperation {
	operation.KeyDigest = strings.TrimSpace(operation.KeyDigest)
	operation.RequestFingerprint = strings.TrimSpace(operation.RequestFingerprint)
	operation.SelectionID = strings.TrimSpace(operation.SelectionID)
	operation.ProposalID = strings.TrimSpace(operation.ProposalID)
	operation.RunID = strings.TrimSpace(operation.RunID)
	operation.RequestedBy = strings.TrimSpace(redact.String(operation.RequestedBy))
	operation.CreatedAt = operation.CreatedAt.UTC()
	return operation
}

func normalizePlanDeliverySelection(
	selection domain.PlanDeliverySelection,
) domain.PlanDeliverySelection {
	selection = domain.ClonePlanDeliverySelection(selection)
	selection.ID = strings.TrimSpace(selection.ID)
	selection.ProposalID = strings.TrimSpace(selection.ProposalID)
	selection.RunID = strings.TrimSpace(selection.RunID)
	selection.RootAgentID = strings.TrimSpace(selection.RootAgentID)
	selection.NoteID = strings.TrimSpace(selection.NoteID)
	selection.RequestedBy = strings.TrimSpace(redact.String(selection.RequestedBy))
	selection.ManualAcceptance = selection.EffectiveManualAcceptance()
	for index := range selection.Items {
		selection.Items[index].WorkItemID = strings.TrimSpace(
			selection.Items[index].WorkItemID)
	}
	selection.CreatedAt = selection.CreatedAt.UTC()
	return selection
}

func validatePlanDeliverySelectionMutation(
	operation domain.PlanDeliverySelectionOperation,
	selection domain.PlanDeliverySelection, items []domain.WorkItem,
	note domain.Note, selectionEvent events.Event,
	itemEvents []events.Event, noteEvent events.Event,
) error {
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Plan/Delivery selection operation is invalid", err)
	}
	if err := selection.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Plan/Delivery selection is invalid", err)
	}
	if operation.SelectionID != selection.ID ||
		operation.ProposalID != selection.ProposalID ||
		operation.RunID != selection.RunID ||
		operation.RequestedBy != selection.RequestedBy ||
		!operation.CreatedAt.Equal(selection.CreatedAt) ||
		operation.RequestFingerprint != domain.PlanDeliverySelectionRequestFingerprintForAcceptance(
			selection.ProposalID, selection.RunID, selection.DirectionOrdinal,
			selection.RequestedBy, selection.ManualAcceptance) {
		return apperror.New(apperror.CodeInvalidArgument,
			"Plan/Delivery selection operation does not match its selection")
	}
	if len(items) != len(selection.Items) || len(itemEvents) != len(items) {
		return apperror.New(apperror.CodeInvalidArgument,
			"Plan/Delivery selection item projection count is inconsistent")
	}
	for index, item := range items {
		if selection.Items[index].WorkItemID != item.ID ||
			item.RunID != selection.RunID {
			return apperror.New(apperror.CodeInvalidArgument,
				"Plan/Delivery selection WorkItem identity is inconsistent")
		}
		if err := validateNewWorkItem(item, itemEvents[index]); err != nil {
			return err
		}
	}
	if selection.NoteID != note.ID || note.RunID != selection.RunID {
		return apperror.New(apperror.CodeInvalidArgument,
			"Plan/Delivery selection Note identity is inconsistent")
	}
	if err := validateNewNote(note, noteEvent); err != nil {
		return err
	}
	if selectionEvent.Type != events.PlanDeliveryDirectionSelectedEvent ||
		selectionEvent.Source != "plan_delivery" ||
		selectionEvent.SubjectID != selection.ID ||
		selectionEvent.RunID != selection.RunID ||
		!selectionEvent.CreatedAt.Equal(selection.CreatedAt) {
		return apperror.New(apperror.CodeInvalidArgument,
			"Plan/Delivery selection event identity is invalid")
	}
	if err := selectionEvent.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"Plan/Delivery selection event is invalid", err)
	}
	return nil
}

func requirePlanDeliverySelectionBindingTx(ctx context.Context, tx *sql.Tx,
	proposal domain.PlanDeliveryProposal, selection domain.PlanDeliverySelection,
) (domain.Run, domain.Mission, domain.PlanDeliveryDirection, error) {
	run, mission, err := getCoordinatorRunTx(ctx, tx, selection.RunID)
	if err != nil {
		return domain.Run{}, domain.Mission{}, domain.PlanDeliveryDirection{}, err
	}
	mode, err := getCurrentRunModeSnapshot(ctx, tx, run.ID)
	if err != nil {
		return domain.Run{}, domain.Mission{}, domain.PlanDeliveryDirection{}, err
	}
	if run.Status != domain.RunPaused || mode.Phase != domain.ExecutionPhasePlan ||
		mode.Revision != proposal.ModeRevision || proposal.RunID != run.ID ||
		selection.ProposalID != proposal.ID ||
		selection.RootAgentID != proposal.RootAgentID ||
		selection.CreatedAt.Before(proposal.CreatedAt) {
		return domain.Run{}, domain.Mission{}, domain.PlanDeliveryDirection{},
			apperror.New(apperror.CodeFailedPrecondition,
				"Plan/Delivery selection requires the paused Run in the proposal's Plan revision")
	}
	var activeLeaseCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_execution_leases
		WHERE run_id = ? AND status = 'active'
			AND julianday(expires_at) > julianday('now')`, run.ID).
		Scan(&activeLeaseCount); err != nil {
		return domain.Run{}, domain.Mission{}, domain.PlanDeliveryDirection{}, err
	}
	if activeLeaseCount != 0 {
		return domain.Run{}, domain.Mission{}, domain.PlanDeliveryDirection{},
			apperror.New(apperror.CodeFailedPrecondition,
				"Plan/Delivery direction cannot be selected while an execution lease is active")
	}
	root, err := scanAgentNode(tx.QueryRowContext(ctx, agentLifecycleSelect+` WHERE id = ?`,
		selection.RootAgentID))
	if err != nil {
		return domain.Run{}, domain.Mission{}, domain.PlanDeliveryDirection{}, err
	}
	if root.RunID != run.ID || root.Role != domain.AgentRoleRoot ||
		root.ParentID != "" || root.Terminal() {
		return domain.Run{}, domain.Mission{}, domain.PlanDeliveryDirection{},
			apperror.New(apperror.CodeFailedPrecondition,
				"Plan/Delivery selection requires the non-terminal root Agent")
	}
	if selection.DirectionOrdinal < 1 ||
		selection.DirectionOrdinal > len(proposal.Spec.Directions) {
		return domain.Run{}, domain.Mission{}, domain.PlanDeliveryDirection{},
			apperror.New(apperror.CodeInvalidArgument,
				"selected Plan/Delivery direction is unavailable")
	}
	return run, mission, proposal.Spec.Directions[selection.DirectionOrdinal-1], nil
}

func validatePlanDeliverySelectionProjection(proposal domain.PlanDeliveryProposal,
	direction domain.PlanDeliveryDirection, selection domain.PlanDeliverySelection,
	items []domain.WorkItem, note domain.Note,
) error {
	if len(direction.Modules) != len(items) || len(items) != len(selection.Items) {
		return apperror.New(apperror.CodeInvalidArgument,
			"selected Plan/Delivery modules do not match the projection")
	}
	for index, module := range direction.Modules {
		item := items[index]
		dependencies := make([]string, len(module.Dependencies))
		for dependencyIndex, dependency := range module.Dependencies {
			if dependency <= 0 || dependency > index {
				return apperror.New(apperror.CodeInvalidArgument,
					"Plan/Delivery module dependency is not backward-only")
			}
			dependencies[dependencyIndex] = selection.Items[dependency-1].WorkItemID
		}
		details, err := domain.NormalizeWorkItemDetails(item.ID,
			domain.WorkItemDetails{
				Title: module.Title, Description: module.Objective,
				Priority:           domain.WorkItemPriorityNormal,
				OwnerAgentID:       proposal.RootAgentID,
				AcceptanceCriteria: module.AcceptanceCriteria,
				Dependencies:       dependencies,
			})
		if err != nil {
			return err
		}
		expected := item
		expected.Title = details.Title
		expected.Description = details.Description
		expected.Priority = details.Priority
		expected.Owner = details.Owner
		expected.OwnerAgentID = details.OwnerAgentID
		expected.AcceptanceCriteria = details.AcceptanceCriteria
		expected.Dependencies = details.Dependencies
		if item.Status != domain.WorkItemPending || item.Version != 1 ||
			item.RunID != proposal.RunID ||
			!item.CreatedAt.Equal(selection.CreatedAt) ||
			!item.UpdatedAt.Equal(selection.CreatedAt) ||
			!sameWorkItemDetails(item, expected) {
			return apperror.New(apperror.CodeInvalidArgument,
				"selected Plan/Delivery WorkItem does not match its module")
		}
	}
	noteDetails, err := domain.NormalizeNoteDetails(domain.NoteDetails{
		Title:    domain.PlanDeliveryHandoffTitle(direction),
		Content:  domain.PlanDeliveryHandoffContent(proposal, direction),
		Category: domain.NoteDecision, Visibility: domain.NoteVisibilityRun,
		OwnerAgentID: proposal.RootAgentID,
		Tags:         []string{"plan-delivery", "selected-direction"},
		SourceRefs:   []string{"plan_delivery:" + proposal.ID}, Pinned: true,
	})
	if err != nil {
		return err
	}
	expectedNote := note
	expectedNote.Title = noteDetails.Title
	expectedNote.Content = noteDetails.Content
	expectedNote.Category = noteDetails.Category
	expectedNote.Visibility = noteDetails.Visibility
	expectedNote.Owner = noteDetails.Owner
	expectedNote.OwnerAgentID = noteDetails.OwnerAgentID
	expectedNote.Tags = noteDetails.Tags
	expectedNote.SourceRefs = noteDetails.SourceRefs
	expectedNote.EvidenceIDs = noteDetails.EvidenceIDs
	expectedNote.Pinned = noteDetails.Pinned
	if note.Status != domain.NoteActive || note.Version != 1 ||
		note.RunID != proposal.RunID ||
		!note.CreatedAt.Equal(selection.CreatedAt) ||
		!note.UpdatedAt.Equal(selection.CreatedAt) ||
		!sameNoteDetails(note, expectedNote) {
		return apperror.New(apperror.CodeInvalidArgument,
			"selected Plan/Delivery handoff Note does not match its direction")
	}
	return nil
}

func validatePlanDeliverySelectionReplay(existing,
	request domain.PlanDeliverySelectionOperation,
) error {
	if existing.KeyDigest != request.KeyDigest ||
		existing.RequestFingerprint != request.RequestFingerprint ||
		existing.ProposalID != request.ProposalID ||
		existing.RunID != request.RunID ||
		existing.RequestedBy != request.RequestedBy {
		return apperror.New(apperror.CodeConflict,
			"Plan/Delivery selection idempotency key was already used for different intent")
	}
	return nil
}

func (s *SQLiteStore) recoverPlanDeliverySelection(ctx context.Context,
	operation domain.PlanDeliverySelectionOperation, original error,
) (domain.PlanDeliverySelection, bool, error) {
	existing, found, err := getPlanDeliverySelectionOperation(ctx, s.db,
		operation.KeyDigest)
	if err != nil || !found {
		if err == nil {
			return domain.PlanDeliverySelection{}, false, original
		}
		return domain.PlanDeliverySelection{}, false, errors.Join(original, err)
	}
	if err := validatePlanDeliverySelectionReplay(existing, operation); err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	selection, err := s.GetPlanDeliverySelection(ctx, existing.SelectionID)
	return selection, true, err
}

func getPlanDeliverySelectionOperation(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, keyDigest string) (domain.PlanDeliverySelectionOperation, bool, error) {
	var operation domain.PlanDeliverySelectionOperation
	var createdAt string
	err := queryer.QueryRowContext(ctx, `SELECT operation_key_digest,
		request_fingerprint, selection_id, proposal_id, run_id, requested_by,
		created_at FROM plan_delivery_selection_operations
		WHERE operation_key_digest = ?`, keyDigest).
		Scan(&operation.KeyDigest, &operation.RequestFingerprint,
			&operation.SelectionID, &operation.ProposalID, &operation.RunID,
			&operation.RequestedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PlanDeliverySelectionOperation{}, false, nil
	}
	if err != nil {
		return domain.PlanDeliverySelectionOperation{}, false, err
	}
	operation.CreatedAt = parseTS(createdAt)
	return operation, true, operation.Validate()
}

func getPlanDeliverySelection(ctx context.Context,
	queryer planDeliveryQueryer, id string,
) (domain.PlanDeliverySelection, error) {
	query, err := planDeliverySelectionSelectSQL(ctx, queryer)
	if err != nil {
		return domain.PlanDeliverySelection{}, err
	}
	selection, expected, err := scanPlanDeliverySelection(
		queryer.QueryRowContext(ctx, query+` WHERE id = ?`, id))
	if err != nil {
		return domain.PlanDeliverySelection{}, err
	}
	selection.Items, err = listPlanDeliverySelectionItems(ctx, queryer,
		selection.ID, expected)
	if err != nil {
		return domain.PlanDeliverySelection{}, err
	}
	return selection, selection.Validate()
}

func getPlanDeliverySelectionByRun(ctx context.Context,
	queryer planDeliveryQueryer, runID string,
) (domain.PlanDeliverySelection, bool, error) {
	query, err := planDeliverySelectionSelectSQL(ctx, queryer)
	if err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	selection, expected, err := scanPlanDeliverySelection(
		queryer.QueryRowContext(ctx, query+
			` WHERE run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PlanDeliverySelection{}, false, nil
	}
	if err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	selection.Items, err = listPlanDeliverySelectionItems(ctx, queryer,
		selection.ID, expected)
	if err != nil {
		return domain.PlanDeliverySelection{}, false, err
	}
	return selection, true, selection.Validate()
}

func scanPlanDeliverySelection(row scanner) (domain.PlanDeliverySelection, int, error) {
	var selection domain.PlanDeliverySelection
	var moduleCount int
	var createdAt string
	if err := row.Scan(&selection.ID, &selection.ProposalID, &selection.RunID,
		&selection.RootAgentID, &selection.DirectionOrdinal, &selection.NoteID,
		&moduleCount, &selection.RequestedBy, &selection.Version,
		&createdAt, &selection.ManualAcceptance); err != nil {
		return domain.PlanDeliverySelection{}, 0, err
	}
	selection.CreatedAt = parseTS(createdAt)
	return selection, moduleCount, nil
}

func listPlanDeliverySelectionItems(ctx context.Context,
	queryer planDeliveryQueryer, selectionID string, expected int,
) ([]domain.PlanDeliverySelectionItem, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT ordinal, module_ordinal,
		work_item_id FROM plan_delivery_selection_items
		WHERE selection_id = ? ORDER BY ordinal`, selectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.PlanDeliverySelectionItem, 0, expected)
	for rows.Next() {
		var item domain.PlanDeliverySelectionItem
		if err := rows.Scan(&item.Ordinal, &item.ModuleOrdinal,
			&item.WorkItemID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) != expected {
		return nil, fmt.Errorf(
			"Plan/Delivery selection item count is inconsistent: got %d want %d",
			len(items), expected)
	}
	return items, nil
}
