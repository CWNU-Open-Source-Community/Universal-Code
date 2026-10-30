package application

import (
	"context"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/waitgraph"
)

// DependencyWaitStore is the durable dependency boundary. Models may only
// propose intent; this interface and the store behind it validate the
// graph, budget, scope, and ownership before anything is persisted.
type DependencyWaitStore interface {
	RecordDependencyWait(context.Context, domain.DependencyEdge, string) (domain.DependencyEdge, bool, error)
	SettleDependencyTarget(context.Context, string, waitgraph.Kind, string,
		domain.AgentDependencyState, string) ([]domain.DependencyWake, error)
	CancelDependencySource(context.Context, string, waitgraph.Kind, string,
		string) ([]domain.DependencyWake, error)
	ExpireOverdueDependencyEdges(context.Context, string, time.Time) ([]domain.DependencyWake, error)
	ReconcileDependencyEdges(context.Context, string) ([]domain.DependencyWake, error)
	DetectDependencyStalls(context.Context, string, time.Time) (domain.DependencyStallDiagnosis, error)
	ListDependencyEdges(context.Context, string, int) ([]domain.DependencyEdge, error)
	GetDependencyEdge(context.Context, string) (domain.DependencyEdge, bool, error)
}

// DependencyWaitService is the control-plane surface for structured
// dependency waiting. It composes the durable ledger with the waitgraph
// cycle contract and the exactly-once wake receipts.
type DependencyWaitService struct {
	store DependencyWaitStore
}

func NewDependencyWaitService(store DependencyWaitStore) *DependencyWaitService {
	return &DependencyWaitService{store: store}
}

// Wait records one structured wait edge. Cycles, self-loops, reverse
// runtime→Agent waits, unknown or cross-Run endpoints, capacity, and
// polling livelock are rejected before the edge is persisted.
func (s *DependencyWaitService) Wait(ctx context.Context, edge domain.DependencyEdge,
	operationKey string,
) (domain.DependencyEdge, bool, error) {
	if s == nil || s.store == nil {
		return domain.DependencyEdge{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"dependency wait store is required")
	}
	stored, replayed, err := s.store.RecordDependencyWait(ctx, edge, operationKey)
	return stored, replayed, apperror.Normalize(err)
}

// SatisfyTarget closes every open wait on the target with the satisfied
// outcome and wakes each source exactly once.
func (s *DependencyWaitService) SatisfyTarget(ctx context.Context, runID string,
	targetKind waitgraph.Kind, targetID, reason string,
) ([]domain.DependencyWake, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "dependency wait store is required")
	}
	wakes, err := s.store.SettleDependencyTarget(ctx, runID, targetKind, targetID,
		domain.AgentDependencySatisfied, reason)
	return wakes, apperror.Normalize(err)
}

// FailTarget closes every open wait on the target with the failed outcome;
// each edge's declared failure policy decides how the source is woken.
func (s *DependencyWaitService) FailTarget(ctx context.Context, runID string,
	targetKind waitgraph.Kind, targetID, reason string,
) ([]domain.DependencyWake, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "dependency wait store is required")
	}
	wakes, err := s.store.SettleDependencyTarget(ctx, runID, targetKind, targetID,
		domain.AgentDependencyFailed, reason)
	return wakes, apperror.Normalize(err)
}

// CancelSource fans a parent cancellation down over every open outgoing
// wait of the source.
func (s *DependencyWaitService) CancelSource(ctx context.Context, runID string,
	sourceKind waitgraph.Kind, sourceID, reason string,
) ([]domain.DependencyWake, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "dependency wait store is required")
	}
	wakes, err := s.store.CancelDependencySource(ctx, runID, sourceKind, sourceID, reason)
	return wakes, apperror.Normalize(err)
}

// Expire closes open waits whose no-progress deadline has passed and emits
// the deadlock diagnosis event.
func (s *DependencyWaitService) Expire(ctx context.Context, runID string,
) ([]domain.DependencyWake, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "dependency wait store is required")
	}
	wakes, err := s.store.ExpireOverdueDependencyEdges(ctx, runID, time.Now().UTC())
	return wakes, apperror.Normalize(err)
}

// Reconcile idempotently settles open waits whose target, deadline, or Run
// already reached a terminal state (crash recovery).
func (s *DependencyWaitService) Reconcile(ctx context.Context, runID string,
) ([]domain.DependencyWake, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "dependency wait store is required")
	}
	wakes, err := s.store.ReconcileDependencyEdges(ctx, runID)
	return wakes, apperror.Normalize(err)
}

// Stalls projects the read-only deadlock/livelock diagnosis for one Run.
func (s *DependencyWaitService) Stalls(ctx context.Context, runID string,
) (domain.DependencyStallDiagnosis, error) {
	if s == nil || s.store == nil {
		return domain.DependencyStallDiagnosis{}, apperror.New(apperror.CodeFailedPrecondition,
			"dependency wait store is required")
	}
	diagnosis, err := s.store.DetectDependencyStalls(ctx, runID, time.Now().UTC())
	return diagnosis, apperror.Normalize(err)
}

