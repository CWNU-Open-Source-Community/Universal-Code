package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
)

const (
	RunWakeWorkerProtocolVersion       = "run_wake_worker.v1"
	MinRunWakeWorkerPollInterval       = 250 * time.Millisecond
	MaxRunWakeWorkerPollInterval       = 60 * time.Second
	DefaultRunWakeWorkerInterval       = 2 * time.Second
	RunWakeWorkerConcurrency           = 1
	RunWakeWorkerMaxSteps              = 1
	RunWakeWorkerHealthProtocolVersion = "run_wake_worker_health.v1"
)

type RunWakeWorkerState string

const (
	RunWakeWorkerReady    RunWakeWorkerState = "ready"
	RunWakeWorkerRunning  RunWakeWorkerState = "running"
	RunWakeWorkerDraining RunWakeWorkerState = "draining"
	RunWakeWorkerStopped  RunWakeWorkerState = "stopped"
)

type RunWakeWorkerHealth struct {
	ProtocolVersion    string
	State              RunWakeWorkerState
	Active             bool
	PollIntervalMillis int64
	Concurrency        int
	MaxSteps           int
}

type RunWakeDueSource interface {
	Due(context.Context, int, int) ([]domain.RunWakeIntent, error)
}

type RunWakeIntentConsumer interface {
	Consume(context.Context, ConsumeRunWakeRequest) (ConsumeRunWakeResult, error)
}

type RunWakeWorkerConfig struct {
	PollInterval time.Duration
	OwnerID      string
	OnError      func(error)
}

// RunWakeWorker is process-lifetime orchestration only. It is serial by
// construction, consumes at most one due intent per tick, hands off exactly
// one Supervisor step, and owns no Shell, LocalRunner, Docker, or Tool Runner.
// Existing durable intent attempts, deadlines, leases, and Supervisor budgets
// remain the authority after restart.
type RunWakeWorker struct {
	due          RunWakeDueSource
	consumer     RunWakeIntentConsumer
	pollInterval time.Duration
	ownerID      string
	onError      func(error)
	healthMu     sync.RWMutex
	runMu        sync.Mutex
	state        RunWakeWorkerState
	active       bool
	started      bool
}

func NewRunWakeWorker(due RunWakeDueSource, consumer RunWakeIntentConsumer,
	config RunWakeWorkerConfig,
) (*RunWakeWorker, error) {
	if due == nil || consumer == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"Run wake worker dependencies are required")
	}
	interval := config.PollInterval
	if interval == 0 {
		interval = DefaultRunWakeWorkerInterval
	}
	if interval < MinRunWakeWorkerPollInterval || interval > MaxRunWakeWorkerPollInterval {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"Run wake worker poll interval is outside its hard bounds")
	}
	ownerID := config.OwnerID
	if ownerID == "" {
		ownerID = idgen.New("wake-worker")
	}
	if !validControlIdentity(ownerID) {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"Run wake worker owner identity is invalid")
	}
	return &RunWakeWorker{due: due, consumer: consumer, pollInterval: interval,
		ownerID: ownerID, onError: config.OnError, state: RunWakeWorkerReady}, nil
}

func (w *RunWakeWorker) Run(ctx context.Context) error {
	if w == nil || w.due == nil || w.consumer == nil {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Run wake worker is unavailable")
	}
	if ctx == nil {
		return apperror.New(apperror.CodeInvalidArgument,
			"Run wake worker context is required")
	}
	w.healthMu.Lock()
	if w.started {
		w.healthMu.Unlock()
		return apperror.New(apperror.CodeFailedPrecondition,
			"Run wake worker cannot be restarted")
	}
	w.started = true
	w.state = RunWakeWorkerRunning
	w.healthMu.Unlock()
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			w.markDraining()
		case <-watchDone:
		}
	}()
	defer func() {
		close(watchDone)
		w.healthMu.Lock()
		w.active = false
		w.state = RunWakeWorkerStopped
		w.healthMu.Unlock()
	}()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			_, err := w.RunOnce(ctx)
			if err != nil && !errors.Is(err, context.Canceled) && w.onError != nil {
				w.onError(apperror.Normalize(err))
			}
			timer.Reset(w.pollInterval)
		}
	}
}

func (w *RunWakeWorker) RunOnce(ctx context.Context) (bool, error) {
	if w == nil || w.due == nil || w.consumer == nil {
		return false, apperror.New(apperror.CodeFailedPrecondition,
			"Run wake worker is unavailable")
	}
	if ctx == nil {
		return false, apperror.New(apperror.CodeInvalidArgument,
			"Run wake worker context is required")
	}
	w.runMu.Lock()
	defer w.runMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	due, err := w.due.Due(ctx, RunWakeWorkerConcurrency, RunWakeWorkerMaxSteps)
	if err != nil {
		return false, err
	}
	if len(due) == 0 {
		return false, nil
	}
	if len(due) != 1 || !validControlIdentity(due[0].RunID) {
		return false, apperror.New(apperror.CodeInternal,
			"Run wake worker due projection violated its hard concurrency bound")
	}
	w.healthMu.Lock()
	w.active = true
	w.healthMu.Unlock()
	defer func() {
		w.healthMu.Lock()
		w.active = false
		w.healthMu.Unlock()
	}()
	_, err = w.consumer.Consume(ctx, ConsumeRunWakeRequest{
		Version: domain.RunWakeConsumerProtocolVersion, RunID: due[0].RunID,
		OwnerID: w.ownerID, MaxSteps: RunWakeWorkerMaxSteps,
	})
	return true, err
}

func (w *RunWakeWorker) Health() RunWakeWorkerHealth {
	if w == nil {
		return RunWakeWorkerHealth{ProtocolVersion: RunWakeWorkerHealthProtocolVersion,
			State: RunWakeWorkerStopped, Concurrency: RunWakeWorkerConcurrency,
			MaxSteps: RunWakeWorkerMaxSteps}
	}
	w.healthMu.RLock()
	state, active := w.state, w.active
	w.healthMu.RUnlock()
	return RunWakeWorkerHealth{ProtocolVersion: RunWakeWorkerHealthProtocolVersion,
		State: state, Active: active, PollIntervalMillis: w.pollInterval.Milliseconds(),
		Concurrency: RunWakeWorkerConcurrency, MaxSteps: RunWakeWorkerMaxSteps}
}

func (w *RunWakeWorker) markDraining() {
	w.healthMu.Lock()
	if w.state == RunWakeWorkerRunning {
		w.state = RunWakeWorkerDraining
	}
	w.healthMu.Unlock()
}
