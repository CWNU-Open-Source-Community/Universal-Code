package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
)

type workflowClaimExpiryProbe struct {
	WorkflowStore
	renewals, releases, fingerprints atomic.Int32
}

func (s *workflowClaimExpiryProbe) WorkflowWakeFingerprint(ctx context.Context, _ string, _ time.Time) (string, error) {
	s.fingerprints.Add(1)
	<-ctx.Done()
	return "", ctx.Err()
}
func (s *workflowClaimExpiryProbe) RenewWorkflowExecutionClaim(context.Context, string, string) (bool, error) {
	s.renewals.Add(1)
	return true, nil
}
func (s *workflowClaimExpiryProbe) ReleaseWorkflowExecution(context.Context, string, string) error {
	s.releases.Add(1)
	return nil
}

func TestWorkflowClaimInitialExpiryUsesAcquisitionStart(t *testing.T) {
	for _, beforeEntry := range []bool{false, true} {
		t.Run(map[bool]string{false: "expires_after_entry", true: "expired_before_entry"}[beforeEntry], func(t *testing.T) {
			st := &workflowClaimExpiryProbe{}
			svc := &WorkflowService{store: st}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			started := time.Now()
			claimStarted := started.Add(-workflowClaimTTL + 100*time.Millisecond)
			wantReads := int32(1)
			if beforeEntry {
				claimStarted = started.Add(-workflowClaimTTL - time.Second)
				wantReads = 0
			}
			_, err := svc.runClaimed(ctx, domain.WorkflowExecution{ID: "already-held-claim"}, "owner", 0, claimStarted)
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second || st.renewals.Load() != 0 || st.releases.Load() != 1 || st.fingerprints.Load() != wantReads {
				t.Fatalf("claim expiry was rebased on interpreter entry: elapsed=%s reads=%d renewals=%d releases=%d err=%v", time.Since(started), st.fingerprints.Load(), st.renewals.Load(), st.releases.Load(), err)
			}
		})
	}
}
