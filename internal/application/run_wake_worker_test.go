package application

import (
	"context"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
)

type wakeWorkerDueFake struct {
	mu       sync.Mutex
	values   []domain.RunWakeIntent
	limits   []int
	maxSteps []int
}

func (f *wakeWorkerDueFake) Due(_ context.Context, limit, maxSteps int) ([]domain.RunWakeIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limits = append(f.limits, limit)
	f.maxSteps = append(f.maxSteps, maxSteps)
	if len(f.values) == 0 {
		return nil, nil
	}
	value := f.values[0]
	f.values = f.values[1:]
	return []domain.RunWakeIntent{value}, nil
}

type wakeWorkerConsumerFake struct {
	mu           sync.Mutex
	requests     []ConsumeRunWakeRequest
	active       int
	maximum      int
	block        chan struct{}
	ignoreCancel bool
}

func (f *wakeWorkerConsumerFake) Consume(ctx context.Context,
	request ConsumeRunWakeRequest,
) (ConsumeRunWakeResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.active++
	if f.active > f.maximum {
		f.maximum = f.active
	}
	f.mu.Unlock()
	if f.block != nil {
		if f.ignoreCancel {
			<-f.block
		} else {
			select {
			case <-ctx.Done():
			case <-f.block:
			}
		}
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return ConsumeRunWakeResult{}, nil
}

func TestRunWakeWorkerHardBoundsDueAndSupervisorHandoff(t *testing.T) {
	due := &wakeWorkerDueFake{values: []domain.RunWakeIntent{{RunID: "run-worker"}}}
	consumer := &wakeWorkerConsumerFake{}
	worker, err := NewRunWakeWorker(due, consumer, RunWakeWorkerConfig{
		PollInterval: MinRunWakeWorkerPollInterval, OwnerID: "wake-worker-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	worked, err := worker.RunOnce(t.Context())
	if err != nil || !worked {
		t.Fatalf("worker did not consume due intent: worked=%t err=%v", worked, err)
	}
	if len(due.limits) != 1 || due.limits[0] != RunWakeWorkerConcurrency ||
		len(due.maxSteps) != 1 || due.maxSteps[0] != RunWakeWorkerMaxSteps ||
		len(consumer.requests) != 1 ||
		consumer.requests[0].Version != domain.RunWakeConsumerProtocolVersion ||
		consumer.requests[0].MaxSteps != RunWakeWorkerMaxSteps ||
		consumer.maximum != 1 {
		t.Fatalf("worker widened a hard bound: due=%v requests=%#v max=%d",
			due.limits, consumer.requests, consumer.maximum)
	}
}

func TestRunWakeWorkerCancellationStopsSerialLoop(t *testing.T) {
	due := &wakeWorkerDueFake{values: []domain.RunWakeIntent{{RunID: "run-worker-cancel"}}}
	consumer := &wakeWorkerConsumerFake{block: make(chan struct{})}
	worker, err := NewRunWakeWorker(due, consumer, RunWakeWorkerConfig{
		PollInterval: MinRunWakeWorkerPollInterval, OwnerID: "wake-worker-cancel",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for {
		consumer.mu.Lock()
		started := len(consumer.requests) == 1
		consumer.mu.Unlock()
		if started {
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker did not start")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestRunWakeWorkerRunOnceRejectsNilContextAndSerializesCallers(t *testing.T) {
	due := &wakeWorkerDueFake{values: []domain.RunWakeIntent{{RunID: "run-worker-serial"}}}
	consumer := &wakeWorkerConsumerFake{block: make(chan struct{})}
	worker, err := NewRunWakeWorker(due, consumer, RunWakeWorkerConfig{
		OwnerID: "wake-worker-serial",
	})
	if err != nil {
		t.Fatal(err)
	}
	//lint:ignore SA1012 Verifies the public boundary rejects a nil context.
	if _, err := worker.RunOnce(nil); err == nil ||
		apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("nil RunOnce context error=%v", err)
	}
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, runErr := worker.RunOnce(t.Context())
			done <- runErr
		}()
	}
	deadline := time.After(2 * time.Second)
	for {
		consumer.mu.Lock()
		active := consumer.active
		consumer.mu.Unlock()
		if active == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("RunOnce caller did not become active")
		case <-time.After(time.Millisecond):
		}
	}
	close(consumer.block)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	consumer.mu.Lock()
	maximum := consumer.maximum
	consumer.mu.Unlock()
	if maximum != RunWakeWorkerConcurrency {
		t.Fatalf("RunOnce maximum concurrency=%d", maximum)
	}
}

func TestRunWakeWorkerProjectsBoundedDrainHealthAndCannotRestart(t *testing.T) {
	due := &wakeWorkerDueFake{values: []domain.RunWakeIntent{{RunID: "run-worker-health"}}}
	consumer := &wakeWorkerConsumerFake{block: make(chan struct{}), ignoreCancel: true}
	worker, err := NewRunWakeWorker(due, consumer, RunWakeWorkerConfig{
		PollInterval: MinRunWakeWorkerPollInterval, OwnerID: "wake-worker-health",
	})
	if err != nil {
		t.Fatal(err)
	}
	ready := worker.Health()
	if ready.ProtocolVersion != RunWakeWorkerHealthProtocolVersion ||
		ready.State != RunWakeWorkerReady || ready.Active ||
		ready.Concurrency != 1 || ready.MaxSteps != 1 ||
		ready.PollIntervalMillis != MinRunWakeWorkerPollInterval.Milliseconds() {
		t.Fatalf("unexpected ready health: %#v", ready)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for {
		health := worker.Health()
		if health.State == RunWakeWorkerRunning && health.Active {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("worker did not become active: %#v", health)
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	deadline = time.After(2 * time.Second)
	for {
		health := worker.Health()
		if health.State == RunWakeWorkerDraining && health.Active {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("worker did not project drain state: %#v", health)
		case <-time.After(time.Millisecond):
		}
	}
	close(consumer.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stopped := worker.Health()
	if stopped.State != RunWakeWorkerStopped || stopped.Active {
		t.Fatalf("worker did not reach stopped health: %#v", stopped)
	}
	if err := worker.Run(context.Background()); err == nil {
		t.Fatal("stopped worker was restarted")
	}
}
