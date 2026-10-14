package application

import (
	"context"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/operatoraction"
	"cyberagent-workbench/internal/runmutation"
)

type OperatorActionCenterStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	ListOperatorActionRecords(context.Context, string, string, string, time.Time, int) (
		[]operatoraction.Record, error)
}

type OperatorActionCenterService struct {
	store OperatorActionCenterStore
	now   func() time.Time
}

func NewOperatorActionCenterService(store OperatorActionCenterStore) *OperatorActionCenterService {
	return &OperatorActionCenterService{store: store,
		now: func() time.Time { return time.Now().UTC() }}
}

func (s *OperatorActionCenterService) List(ctx context.Context,
	runID string,
) (operatoraction.Center, error) {
	if s == nil || s.store == nil || s.now == nil {
		return operatoraction.Center{}, apperror.New(apperror.CodeFailedPrecondition,
			"operator action center store is required")
	}
	normalizedRunID := strings.TrimSpace(runID)
	if normalizedRunID != runID || !domain.ValidAgentID(runID) {
		return operatoraction.Center{}, apperror.New(apperror.CodeInvalidArgument,
			"operator action center Run identity is invalid")
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return operatoraction.Center{}, apperror.Normalize(err)
	}
	if run.ID != runID {
		return operatoraction.Center{}, apperror.New(apperror.CodeConflict,
			"operator action center Run lookup changed")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return operatoraction.Center{}, apperror.Normalize(err)
	}
	if mission.ID != run.MissionID {
		return operatoraction.Center{}, apperror.New(apperror.CodeConflict,
			"operator action center Mission lookup changed")
	}
	fileWorkspaceID, err := runFileProjectionWorkspaceID(ctx, s.store, run, mission)
	if err != nil {
		return operatoraction.Center{}, err
	}
	now := s.now().UTC()
	records, err := s.store.ListOperatorActionRecords(ctx, run.ID, run.SessionID,
		fileWorkspaceID, now, operatoraction.MaxItems+1)
	if err != nil {
		return operatoraction.Center{}, apperror.Normalize(err)
	}
	truncated := len(records) > operatoraction.MaxItems
	if truncated {
		records = records[:operatoraction.MaxItems]
	}
	items := make([]operatoraction.Item, 0, len(records))
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return operatoraction.Center{}, apperror.Wrap(apperror.CodeInternal,
				"stored operator action is invalid", err)
		}
		workspaceMatches := record.WorkspaceID == "" || record.WorkspaceID == mission.WorkspaceID
		switch record.Kind {
		case operatoraction.KindFileEditReview, operatoraction.KindFileEditApply:
			workspaceMatches = record.WorkspaceID == fileWorkspaceID
		case operatoraction.KindApprovalPending:
			workspaceMatches = workspaceMatches || record.WorkspaceID == fileWorkspaceID
		}
		if record.RunID != run.ID ||
			(record.SessionID != "" && record.SessionID != run.SessionID) ||
			!workspaceMatches ||
			(record.Kind == operatoraction.KindWakeDue && record.DueAt.After(now)) {
			return operatoraction.Center{}, apperror.New(apperror.CodeConflict,
				"operator action escaped its requested Run binding")
		}
		destination, ok := operatoraction.DestinationFor(record.Kind)
		if !ok {
			return operatoraction.Center{}, apperror.New(apperror.CodeInternal,
				"operator action has no closed destination")
		}
		digest := runmutation.Fingerprint("operator_action_item.v1", run.ID,
			string(record.Kind), record.SourceID)
		items = append(items, operatoraction.Item{ID: "action-" + digest[:24],
			Kind: record.Kind, State: record.State, Destination: destination,
			AvailableAt: record.AvailableAt, DueAt: record.DueAt})
	}
	center := operatoraction.Center{ProtocolVersion: operatoraction.ProtocolVersion,
		RunID: run.ID, GeneratedAt: now, Items: items, Truncated: truncated}
	if err := center.Validate(); err != nil {
		return operatoraction.Center{}, apperror.Wrap(apperror.CodeInternal,
			"operator action center projection is invalid", err)
	}
	return center, nil
}
