package application

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/pricing"
)

// MonetaryBudgetStore is the durable monetary boundary. Only the Go control
// plane computes prices; a Provider response, README, Skill, or repository
// file can never touch this interface.
type MonetaryBudgetStore interface {
	ActivePriceSnapshot(context.Context) (pricing.Snapshot, bool, error)
	ReserveModelCost(context.Context, domain.MonetaryReserveRequest) (domain.MonetaryUsage, bool, error)
	SettleModelCost(context.Context, domain.MonetarySettleRequest) (domain.MonetaryUsage, bool, error)
	ReleaseModelCost(context.Context, domain.MonetaryReleaseRequest) (domain.MonetaryUsage, bool, error)
	GetMonetaryUsage(context.Context, string) (domain.MonetaryUsage, error)
	ReleaseOpenMonetaryReservations(context.Context, string) (int, error)
}

type monetaryReservationPriceStore interface {
	ModelReservationPrice(context.Context, string, string, int64) (pricing.Entry, bool, error)
}

// MonetaryBudgetService composes the operator price snapshot with the
// durable run aggregate. Reserve computes the upper-bound cost before a
// model call; settle closes it with actual usage; release returns the
// unused portion. Runs without MaxCostUSD stay untracked and skip every
// gate.
type MonetaryBudgetService struct {
	store MonetaryBudgetStore
	now   func() time.Time
}

func NewMonetaryBudgetService(store MonetaryBudgetStore) *MonetaryBudgetService {
	return &MonetaryBudgetService{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// ReserveModelCall reserves the upper-bound cost for one model attempt.
// For tracked runs the active price snapshot must carry an exact
// provider/model entry; unknown or expired prices fail closed.
func (s *MonetaryBudgetService) ReserveModelCall(ctx context.Context, run domain.Run,
	scope string, attempt llm.ModelAttempt, request llm.ChatRequest,
) (domain.MonetaryUsage, error) {
	if s == nil || s.store == nil {
		return domain.MonetaryUsage{}, nil
	}
	if run.Budget.MaxCostUSD <= 0 {
		return domain.MonetaryUsage{}, nil
	}
	snapshot, found, err := s.store.ActivePriceSnapshot(ctx)
	if err != nil {
		return domain.MonetaryUsage{}, apperror.Normalize(err)
	}
	if !found || !snapshot.Active(s.now()) {
		return domain.MonetaryUsage{}, apperror.New(apperror.CodeFailedPrecondition,
			"no active price snapshot is configured for the run monetary budget")
	}
	entry, ok := snapshot.Lookup(attempt.Provider, attempt.Model)
	if !ok {
		return domain.MonetaryUsage{}, apperror.New(apperror.CodeFailedPrecondition,
			"the model has no price entry in the active price snapshot")
	}
	inputBytes := estimateModelRequestBytes(request)
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		return domain.MonetaryUsage{}, apperror.New(apperror.CodeFailedPrecondition,
			"monetary budget requires an explicit model output limit before reservation")
	}
	reserve := entry.ReserveCost(inputBytes, int64(maxTokens), int64(len(request.Tools)))
	if reserve <= 0 {
		return domain.MonetaryUsage{}, apperror.New(apperror.CodeFailedPrecondition,
			"the model price entry yields no positive cost bound")
	}
	usage, _, err := s.store.ReserveModelCost(ctx, domain.MonetaryReserveRequest{
		RunID: run.ID, Scope: scope, Provider: attempt.Provider, Model: attempt.Model,
		AttemptNumber: attempt.MonetaryAttemptNumber(), ReservedMicros: reserve,
		PriceFingerprint: snapshot.Fingerprint,
		EstimateSource:   pricing.ProtocolVersion + "/" + snapshot.ID,
	})
	if err != nil {
		return domain.MonetaryUsage{}, apperror.Normalize(err)
	}
	return usage, nil
}

// SettleModelCall prices received usage against the immutable snapshot used
// for this reservation. Older embedders can use their active price table; a
// missing price conservatively retains the full reserved estimate.
func (s *MonetaryBudgetService) SettleModelCall(ctx context.Context, runID, scope string,
	attempt llm.ModelAttempt, usage llm.Usage, toolCallCount int,
) (domain.MonetaryUsage, error) {
	if s == nil || s.store == nil {
		return domain.MonetaryUsage{}, nil
	}
	if usage.Validate() != nil || (usage.InputTokens == 0 && usage.OutputTokens == 0) ||
		(usage.TotalTokens > usage.InputTokens && usage.TotalTokens-usage.InputTokens > usage.OutputTokens) {
		return s.SettleUnknownModelCall(ctx, runID, scope, attempt)
	}
	actual := int64(math.MaxInt64)
	pricingStatus := domain.MonetaryPricingPriceMissing
	var entry pricing.Entry
	var priced bool
	if pinned, ok := s.store.(monetaryReservationPriceStore); ok {
		var err error
		entry, priced, err = pinned.ModelReservationPrice(ctx, runID, scope, attempt.MonetaryAttemptNumber())
		if err != nil {
			return domain.MonetaryUsage{}, apperror.Normalize(err)
		}
	} else if snapshot, found, err := s.store.ActivePriceSnapshot(ctx); err == nil && found && snapshot.Active(s.now()) {
		entry, priced = snapshot.Lookup(attempt.Provider, attempt.Model)
	}
	if priced && (entry.Provider != attempt.Provider || entry.Model != attempt.Model) {
		return domain.MonetaryUsage{}, apperror.New(apperror.CodeConflict,
			"model identity differs from its monetary reservation")
	}
	if priced && toolCallCount >= 0 {
		cost, costErr := entry.UsageCost(pricing.TokenUsage{
			InputTokens: int64(usage.InputTokens), OutputTokens: int64(usage.OutputTokens),
			CacheReadTokens: int64(usage.CacheReadTokens), CacheCreationTokens: int64(usage.CacheCreationTokens),
			CacheCreation5mTokens: int64(usage.CacheCreation5mTokens), CacheCreation1hTokens: int64(usage.CacheCreation1hTokens),
			CacheCreationBreakdownKnown: usage.CacheCreationBreakdownKnown,
		}, int64(toolCallCount))
		if costErr == nil {
			actual, pricingStatus = cost, domain.MonetaryPricingPriced
		} else {
			pricingStatus = domain.MonetaryPricingCachePriceMissing
		}
	}
	return s.settle(ctx, domain.MonetarySettleRequest{
		RunID: runID, Scope: scope, AttemptNumber: attempt.MonetaryAttemptNumber(), ActualMicros: actual,
		PricingStatus: pricingStatus,
	})
}

// A sent request with unknown usage is not known to be free. Keep its durable
// ceiling as a conservative local estimate, not a provider billing receipt.
func (s *MonetaryBudgetService) SettleUnknownModelCall(ctx context.Context, runID, scope string, attempt llm.ModelAttempt) (domain.MonetaryUsage, error) {
	if s == nil || s.store == nil {
		return domain.MonetaryUsage{}, nil
	}
	return s.settle(ctx, domain.MonetarySettleRequest{RunID: runID, Scope: scope,
		AttemptNumber: attempt.MonetaryAttemptNumber(), ActualMicros: math.MaxInt64,
		PricingStatus: domain.MonetaryPricingUsageUnknown})
}

func (s *MonetaryBudgetService) settle(ctx context.Context,
	request domain.MonetarySettleRequest,
) (domain.MonetaryUsage, error) {
	usage, _, err := s.store.SettleModelCost(ctx, request)
	if err != nil {
		return domain.MonetaryUsage{}, apperror.Normalize(err)
	}
	return usage, nil
}

// ReleaseModelCall releases an open reservation without settling (failed
// attempts and terminal runs).
func (s *MonetaryBudgetService) ReleaseModelCall(ctx context.Context, runID, scope string,
	attempt llm.ModelAttempt,
) (domain.MonetaryUsage, error) {
	if s == nil || s.store == nil {
		return domain.MonetaryUsage{}, nil
	}
	usage, _, err := s.store.ReleaseModelCost(ctx, domain.MonetaryReleaseRequest{
		RunID: runID, Scope: scope, AttemptNumber: attempt.MonetaryAttemptNumber(),
	})
	if err != nil {
		return domain.MonetaryUsage{}, apperror.Normalize(err)
	}
	return usage, nil
}

// Usage projects the run aggregate (with reconciliation of terminal
// attempts).
func (s *MonetaryBudgetService) Usage(ctx context.Context, runID string) (domain.MonetaryUsage, error) {
	if s == nil || s.store == nil {
		return domain.MonetaryUsage{}, nil
	}
	usage, err := s.store.GetMonetaryUsage(ctx, runID)
	if err != nil {
		return domain.MonetaryUsage{}, apperror.Normalize(err)
	}
	return usage, nil
}

// ReleaseOpenReservations releases every open reservation for a terminal
// run.
func (s *MonetaryBudgetService) ReleaseOpenReservations(ctx context.Context, runID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	_, err := s.store.ReleaseOpenMonetaryReservations(ctx, runID)
	return apperror.Normalize(err)
}

// estimateModelRequestBytes uses serialized text and complete tool schemas as
// a conservative local token allowance. Images use the same explicit planning
// estimate as the model window; this is not a provider billing calculator.
func estimateModelRequestBytes(request llm.ChatRequest) int64 {
	total := int64(0)
	if raw, err := json.Marshal(request.Messages); err == nil {
		total = int64(len(raw))
	} else {
		for _, message := range request.Messages {
			total += int64(len(message.Content) + len(message.Role) + 64)
			for _, call := range message.ToolCalls {
				total += int64(len(call.Name) + len(call.Arguments) + len(call.ID) + 64)
			}
			for _, result := range message.ToolResults {
				total += int64(len(result.Content) + 128)
			}
		}
	}
	for _, message := range request.Messages {
		for _, part := range message.Images {
			total += int64(llm.EstimateImageTokens(part))
		}
	}
	if raw, err := json.Marshal(request.Tools); err == nil {
		total += int64(len(raw))
	} else {
		for _, tool := range request.Tools {
			total += int64(len(tool.Name) + len(tool.Description) + len(tool.Parameters) + 128)
		}
	}
	return total + int64(len(request.Tools))*128
}
