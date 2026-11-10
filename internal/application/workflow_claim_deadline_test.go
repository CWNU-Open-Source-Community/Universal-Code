package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cyberagent-workbench/internal/store"
)

type workflowDeadlineProbeStore struct {
	*store.SQLiteStore
	deadline time.Time
	bounded  bool
	failure  error
}

func (s *workflowDeadlineProbeStore) WorkflowWakeFingerprint(ctx context.Context, _ string, _ time.Time) (string, error) {
	s.deadline, s.bounded = ctx.Deadline()
	return "", s.failure
}

func TestWorkflowClaimOwnerHasInterpreterDeadlineBeforeReadingWake(t *testing.T) {
	for _, callerTimeout := range []time.Duration{0, time.Minute} {
		t.Run(callerTimeout.String(), func(t *testing.T) {
			provider := &specialistTestProvider{}
			raw, run, _, _ := newWorkflowFixture(t, provider)
			failure := errors.New("stop at the first claimed interpreter read")
			st := &workflowDeadlineProbeStore{SQLiteStore: raw, failure: failure}
			svc := workflowClaimService(st, provider)
			e := startHostWorkflow(t, svc, run.ID, "claim-owner-deadline", workflowSpec(workflowWorker("one")))
			ctx := t.Context()
			limit := 10 * time.Minute
			if callerTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, callerTimeout)
				defer cancel()
				limit = callerTimeout
			}
			started := time.Now()
			if _, err := svc.Advance(ctx, e.ID); !errors.Is(err, failure) {
				t.Fatalf("first read error changed: %v", err)
			}
			if !st.bounded || st.deadline.Before(started.Add(limit-time.Second)) || st.deadline.After(time.Now().Add(limit)) {
				t.Fatalf("claim owner does not share the interpreter limit: bounded=%t deadline=%s limit=%s", st.bounded, st.deadline, limit)
			}
			if provider.calls != 0 {
				t.Fatal("failed interpreter read started a model call")
			}
		})
	}
}
