package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
)

const runWakeConsumerActor = "run_wake_consumer"

type RunWakeConsumerStore interface {
	RunWakeCoordinatorStore
	GetRunExecutionHandoff(context.Context, string) (domain.RunExecutionHandoff, bool, error)
	GetLatestRunWakeIntent(context.Context, string) (domain.RunWakeIntent, bool, error)
	GetRunWakeIntent(context.Context, string) (domain.RunWakeIntent, error)
	GetRunWakeConsumption(context.Context, string, int) (
		domain.RunWakeConsumption, bool, error)
	PrepareRunWakeConsumption(context.Context, domain.RunWakeConsumption) (
		domain.RunWakeConsumption, bool, error)
	CompleteRunWakeConsumption(context.Context, string, domain.RunExecutionHandoff,
		time.Time) (domain.RunWakeConsumption, domain.RunWakeIntent, bool, error)
	FailRunWakeConsumption(context.Context, string, string, string, string, time.Time) (
		domain.RunWakeConsumption, domain.RunWakeIntent, bool, error)
}

type RunWakeExecutionHandoff interface {
	Execute(context.Context, ExecuteRunHandoffRequest) (ExecuteRunHandoffResult, error)
}

type ForegroundRunWakeConsumer struct {
	store       RunWakeConsumerStore
	coordinator *RunWakeCoordinator
	handoff     RunWakeExecutionHandoff
	now         func() time.Time
}

type ConsumeRunWakeRequest struct {
	Version  string
	RunID    string
	IntentID string
	OwnerID  string
	MaxSteps int
}

type ConsumeRunWakeResult struct {
	Intent      domain.RunWakeIntent
	Consumption domain.RunWakeConsumption
	Handoff     domain.RunExecutionHandoff
	Replayed    bool
}

func NewForegroundRunWakeConsumer(store RunWakeConsumerStore,
	handoff RunWakeExecutionHandoff,
) *ForegroundRunWakeConsumer {
	return &ForegroundRunWakeConsumer{store: store,
		coordinator: NewRunWakeCoordinator(store), handoff: handoff,
		now: func() time.Time { return time.Now().UTC() }}
}

func (c *ForegroundRunWakeConsumer) Consume(ctx context.Context,
	request ConsumeRunWakeRequest,
) (ConsumeRunWakeResult, error) {
	if c == nil || c.store == nil || c.coordinator == nil || c.handoff == nil || c.now == nil {
		return ConsumeRunWakeResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"foreground Run wake consumer dependencies are required")
	}
	if request.Version != domain.RunWakeConsumerProtocolVersion ||
		!validControlIdentity(request.RunID) || !validControlIdentity(request.OwnerID) ||
		request.MaxSteps < 1 || request.MaxSteps > domain.MaxRunExecutionHandoffSteps {
		return ConsumeRunWakeResult{}, apperror.New(apperror.CodeInvalidArgument,
			"foreground Run wake request is invalid")
	}
	intent, found, err := c.store.GetLatestRunWakeIntent(ctx, request.RunID)
	if request.IntentID != "" {
		if !validControlIdentity(request.IntentID) {
			return ConsumeRunWakeResult{}, apperror.New(apperror.CodeInvalidArgument, "Run wake intent identity is invalid")
		}
		intent, err = c.store.GetRunWakeIntent(ctx, request.IntentID)
		found = err == nil
		if err == nil && intent.RunID != request.RunID {
			return ConsumeRunWakeResult{}, apperror.New(apperror.CodeFailedPrecondition, "Run wake intent belongs to another Run")
		}
	}
	if err != nil {
		return ConsumeRunWakeResult{}, apperror.Normalize(err)
	}
	if !found {
		return ConsumeRunWakeResult{}, apperror.New(apperror.CodeNotFound,
			"Run wake intent was not found")
	}
	if intent.Status == domain.RunWakeCompleted {
		consumption, found, lookupErr := c.store.GetRunWakeConsumption(ctx,
			intent.ID, intent.AttemptCount)
		if lookupErr != nil || !found || consumption.Status != domain.RunWakeConsumptionCompleted {
			if lookupErr == nil {
				lookupErr = apperror.New(apperror.CodeInternal,
					"completed Run wake consumption was not found")
			}
			return ConsumeRunWakeResult{}, apperror.Normalize(lookupErr)
		}
		handoff, handoffFound, handoffErr := c.store.GetRunExecutionHandoff(ctx,
			consumption.HandoffOperationKeyDigest)
		if handoffErr != nil || !handoffFound || handoff.Result == nil ||
			handoff.Operation.ID != consumption.HandoffOperationID ||
			handoff.Result.Status != domain.RunExecutionHandoffCompleted {
			if handoffErr == nil {
				handoffErr = apperror.New(apperror.CodeInternal,
					"completed Run wake handoff was not found")
			}
			return ConsumeRunWakeResult{}, apperror.Normalize(handoffErr)
		}
		return ConsumeRunWakeResult{Intent: intent, Consumption: consumption,
			Handoff: handoff, Replayed: true}, nil
	}
	if intent.Status == domain.RunWakeCancelled || intent.Status == domain.RunWakeExhausted {
		return ConsumeRunWakeResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Run wake intent is terminal")
	}
	if intent.Status == domain.RunWakeQueued && c.now().UTC().Before(intent.NextWakeAt) {
		return ConsumeRunWakeResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Run wake intent is not due")
	}

	var consumption domain.RunWakeConsumption
	preparedReplay := false
	if intent.Status == domain.RunWakeLeased {
		consumption, found, err = c.store.GetRunWakeConsumption(ctx, intent.ID,
			intent.AttemptCount)
		if err != nil {
			return ConsumeRunWakeResult{}, apperror.Normalize(err)
		}
		if found && consumption.Status != domain.RunWakeConsumptionPrepared {
			return ConsumeRunWakeResult{}, apperror.New(apperror.CodeConflict,
				"leased Run wake generation already has a terminal consumption")
		}
	}
	if consumption.ID == "" {
		claimed, lease, acquired, claimErr := c.coordinator.Claim(ctx, intent.ID,
			request.OwnerID)
		if claimErr != nil {
			return ConsumeRunWakeResult{}, apperror.Normalize(claimErr)
		}
		if !acquired {
			return ConsumeRunWakeResult{}, apperror.New(apperror.CodeConflict,
				"Run wake intent could not be claimed for foreground execution")
		}
		intent = claimed
		operationKey := runmutation.RunWakeConsumptionOperationKey(intent.ID,
			lease.Generation)
		consumption = domain.RunWakeConsumption{
			ID:              idgen.New("wake-consume"),
			ProtocolVersion: domain.RunWakeConsumptionProtocolVersion,
			IntentID:        intent.ID, RunID: intent.RunID, SessionID: intent.SessionID,
			LeaseID: lease.ID, Generation: lease.Generation, OwnerID: lease.OwnerID,
			HandoffOperationKeyDigest: runmutation.RunExecutionHandoffOperationDigest(
				intent.RunID, operationKey),
			MaxSteps: request.MaxSteps, Status: domain.RunWakeConsumptionPrepared,
			CreatedAt: c.now().UTC(),
		}
		consumption, preparedReplay, err = c.store.PrepareRunWakeConsumption(ctx,
			consumption)
		if err != nil {
			return ConsumeRunWakeResult{}, apperror.Normalize(err)
		}
	} else if consumption.MaxSteps != request.MaxSteps {
		return ConsumeRunWakeResult{}, apperror.New(apperror.CodeConflict,
			"Run wake generation was prepared with a different step limit")
	}

	operationKey := runmutation.RunWakeConsumptionOperationKey(consumption.IntentID,
		consumption.Generation)
	handoff, handoffErr := c.handoff.Execute(ctx, ExecuteRunHandoffRequest{
		Version: domain.RunExecutionHandoffProtocolVersion, RunID: consumption.RunID,
		MaxSteps: consumption.MaxSteps, OperationKey: operationKey,
		RequestedBy: runWakeConsumerActor,
	})
	if handoff.Handoff.Operation.ID != "" && handoff.Handoff.Result == nil {
		if handoffErr == nil {
			handoffErr = apperror.New(apperror.CodeConflict,
				"Run wake execution handoff is still pending")
		}
		return ConsumeRunWakeResult{Intent: intent, Consumption: consumption,
			Handoff:  handoff.Handoff,
			Replayed: preparedReplay || handoff.Replayed}, apperror.Normalize(handoffErr)
	}
	if handoffErr != nil || handoff.Handoff.Result == nil ||
		handoff.Handoff.Result.Status != domain.RunExecutionHandoffCompleted {
		code := "handoff_failed"
		stopReason := code
		handoffOperationID := handoff.Handoff.Operation.ID
		if handoffErr != nil {
			code = strings.ToLower(string(apperror.CodeOf(apperror.Normalize(handoffErr))))
			stopReason = code
		} else if handoff.Handoff.Result != nil {
			code = handoff.Handoff.Result.ErrorCode
			stopReason = handoff.Handoff.Result.StopReason
		}
		if code == "" {
			code = "handoff_failed"
		}
		if stopReason == "" {
			stopReason = code
		}
		completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		failed, storedIntent, replayed, failErr := c.store.FailRunWakeConsumption(
			completeCtx, consumption.ID, handoffOperationID, stopReason, code, c.now().UTC())
		if failErr != nil {
			if handoffErr != nil {
				return ConsumeRunWakeResult{}, apperror.Normalize(
					errors.Join(handoffErr, failErr))
			}
			return ConsumeRunWakeResult{}, apperror.Normalize(failErr)
		}
		result := ConsumeRunWakeResult{Intent: storedIntent, Consumption: failed,
			Handoff: handoff.Handoff, Replayed: preparedReplay || handoff.Replayed || replayed}
		if handoffErr != nil {
			return result, apperror.Normalize(handoffErr)
		}
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"Run wake execution handoff failed")
	}
	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	stored, storedIntent, completionReplay, err := c.store.CompleteRunWakeConsumption(
		completeCtx, consumption.ID, handoff.Handoff, c.now().UTC())
	if err != nil {
		return ConsumeRunWakeResult{}, apperror.Normalize(err)
	}
	return ConsumeRunWakeResult{Intent: storedIntent, Consumption: stored,
		Handoff:  handoff.Handoff,
		Replayed: preparedReplay || handoff.Replayed || completionReplay}, nil
}
