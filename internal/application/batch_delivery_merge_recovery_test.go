package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
)

type batchMergeLostReceiptStore struct {
	*store.SQLiteStore
	lost bool
}

func (s *batchMergeLostReceiptStore) CompleteBatchDeliveryMergeStep(ctx context.Context,
	step domain.BatchDeliveryMergeStep, nextIndex int, head string,
	status domain.BatchDeliveryMergeQueueStatus, failureCode, failureSummary string,
) error {
	if !s.lost {
		s.lost = true
		return errors.New("injected merge receipt write failure")
	}
	return s.SQLiteStore.CompleteBatchDeliveryMergeStep(ctx, step, nextIndex, head, status, failureCode, failureSummary)
}

func TestBatchMergeRecoversCommitBeforeReceipt(t *testing.T) {
	for _, recovery := range []string{"replay", "reconcile", "unrelated_commit"} {
		t.Run(recovery, func(t *testing.T) {
			fixture := newBatchDeliveryApplicationFixture(t, false, domain.RunExecutionPermissionAsk)
			prepared, receipts := prepareAcceptedBatchTextDeliveries(t, fixture, "merge-recovery")
			request := MergeBatchDeliveryRequest{PlanID: prepared.Plan.ID,
				OperationKey: "merge-recovery-operation", RequestedBy: fixture.root.ID,
				WorktreeParent: t.TempDir(), Confirm: true}
			failing := NewBatchDeliveryService(&batchMergeLostReceiptStore{SQLiteStore: fixture.store})
			interrupted, err := failing.Merge(t.Context(), request)
			if err == nil {
				t.Fatal("merge receipt failure was not injected")
			}
			queue, found, err := fixture.store.GetBatchDeliveryMergeQueue(t.Context(), interrupted.Queue.ID)
			if err != nil || !found || queue.Status != domain.BatchMergeQueueRunning || queue.NextIndex != 0 {
				t.Fatalf("interrupted queue=%+v found=%t err=%v", queue, found, err)
			}
			mergedHead := fixtureGit(t, "-C", queue.IntegrationRoot, "rev-parse", "HEAD")
			if mergedHead == queue.IntegrationHead {
				t.Fatal("Git merge did not complete before receipt failure")
			}
			if recovery == "unrelated_commit" {
				fixtureGit(t, "-C", queue.IntegrationRoot, "-c", "user.name=test",
					"-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "external commit")
			}
			reopened, err := store.Open(fixture.database)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			restarted := NewBatchDeliveryService(reopened)
			if recovery == "reconcile" {
				result, err := restarted.Reconcile(t.Context(), prepared.Plan.ID)
				if err != nil || !result.MergeResumed || !result.MergeCompleted || result.NeedsOperatorAttention {
					t.Fatalf("reconcile=%+v err=%v", result, err)
				}
			} else {
				_, err = restarted.Merge(t.Context(), request)
				if (recovery == "unrelated_commit") != (err != nil) {
					t.Fatalf("merge recovery=%s err=%v", recovery, err)
				}
			}
			stored, _, err := reopened.GetBatchDeliveryMergeQueue(t.Context(), queue.ID)
			steps, stepsErr := reopened.ListBatchDeliveryMergeSteps(t.Context(), queue.ID)
			if err != nil || stepsErr != nil {
				t.Fatalf("queue err=%v steps err=%v", err, stepsErr)
			}
			if recovery == "unrelated_commit" {
				if stored.Status != domain.BatchMergeQueueBlocked || stored.FailureCode != "integration_drift" || len(steps) != 0 {
					t.Fatalf("unrelated commit was adopted: queue=%+v steps=%+v", stored, steps)
				}
				return
			}
			if stored.Status != domain.BatchMergeQueueCompleted || stored.NextIndex != 2 || len(steps) != 2 ||
				steps[0].PostMergeHead != mergedHead || steps[0].InputHead != receipts[0].HeadCommit {
				t.Fatalf("recovered queue=%+v steps=%+v", stored, steps)
			}
			var validation struct {
				Recovered bool `json:"recovered"`
			}
			if err := json.Unmarshal([]byte(steps[0].ValidationJSON), &validation); err != nil || !validation.Recovered {
				t.Fatalf("existing merge was not recovered: %+v err=%v", steps[0], err)
			}
			replayed, err := restarted.Merge(t.Context(), request)
			if err != nil || !replayed.Replayed || replayed.Queue.IntegrationHead != stored.IntegrationHead || len(replayed.Steps) != 2 {
				t.Fatalf("completed replay=%+v err=%v", replayed, err)
			}
			if head := fixtureGit(t, "-C", fixture.repository, "rev-parse", "HEAD"); head != prepared.Plan.BaseCommit {
				t.Fatalf("source changed: %s", head)
			}
		})
	}
}
