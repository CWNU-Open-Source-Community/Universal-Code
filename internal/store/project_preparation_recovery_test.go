package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func newPreparationRecoveryStore(t *testing.T) (*SQLiteStore, domain.ProjectPreparationOperation) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "preparation-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, run, err := application.NewRunService(s).Create(t.Context(), application.CreateRunRequest{Goal: "recover dependency preparation", Profile: "code", Surface: "code", Phase: "deliver", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute)
	o := domain.ProjectPreparationOperation{ID: "prep-abandoned-web", RunID: run.ID, KeyDigest: strings.Repeat("a", 64), RequestFingerprint: strings.Repeat("b", 64), PlanFingerprint: strings.Repeat("c", 64), Target: "web", Backend: "docker_sandboxes", RequestedBy: "operator", OwnerID: "previous-process", RequestJSON: `{"allowed_sources":[]}`, PermissionRevision: 1, State: "pending", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if _, _, err := s.BeginProjectPreparation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	return s, o
}

func TestProjectPreparationRecoveryRechecksLeaseAndExactRevision(t *testing.T) {
	for _, protection := range []string{"new lease", "new progress", "new cancellation", "terminal", "recent"} {
		t.Run(protection, func(t *testing.T) {
			s, expected := newPreparationRecoveryStore(t)
			current := expected
			switch protection {
			case "new lease":
				if _, err := s.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: expected.RunID, OwnerID: "new-preparation-owner", TTL: time.Minute}); err != nil {
					t.Fatal(err)
				}
			case "new progress", "new cancellation", "terminal", "recent":
				current.Revision++
				if protection == "new progress" {
					current.State = "downloading"
					current.UpdatedAt = time.Now().UTC()
				} else if protection == "new cancellation" {
					current.CancelRequested = true
				} else if protection == "terminal" {
					current.State = "interrupted"
				} else {
					current.UpdatedAt = time.Now().UTC()
				}
				var err error
				current, err = s.UpdateProjectPreparation(t.Context(), current, expected.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if protection == "recent" {
					expected = current
				}
			}
			got, err := s.InterruptStaleProjectPreparation(t.Context(), expected)
			if err != nil || got != current {
				t.Fatalf("recovery changed the current owner: got=%+v want=%+v err=%v", got, current, err)
			}
			stored, err := s.GetProjectPreparation(t.Context(), current.RunID, current.ID)
			if err != nil || stored != current {
				t.Fatalf("recovery persisted a stale update: got=%+v want=%+v err=%v", stored, current, err)
			}
		})
	}
}

func TestProjectPreparationRecoveryRetainsCancellationAndFreesRunAdmission(t *testing.T) {
	s, old := newPreparationRecoveryStore(t)
	active, found, err := s.ActiveProjectPreparation(t.Context(), old.RunID)
	if err != nil || !found || active.ID != old.ID || active.Target != "web" {
		t.Fatalf("Run lookup missed another target: %+v found=%t err=%v", active, found, err)
	}
	if _, found, err := s.ActiveProjectPreparation(t.Context(), "another-run"); err != nil || found {
		t.Fatalf("Run lookup crossed ownership: found=%t err=%v", found, err)
	}
	old.CancelRequested = true
	old.Revision++
	old, err = s.UpdateProjectPreparation(t.Context(), old, old.Revision-1)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := s.InterruptStaleProjectPreparation(t.Context(), old)
	if err != nil || recovered.State != "interrupted" || !recovered.CancelRequested || recovered.Revision != old.Revision+1 || recovered.Error == "" {
		t.Fatalf("recovery discarded cancellation: %+v %v", recovered, err)
	}
	if _, found, err := s.ActiveProjectPreparation(t.Context(), old.RunID); err != nil || found {
		t.Fatalf("recovery left active admission: found=%t err=%v", found, err)
	}
	next := old
	next.ID, next.KeyDigest, next.RequestFingerprint, next.Target = "prep-new-root", strings.Repeat("d", 64), strings.Repeat("e", 64), "."
	next.State, next.CancelRequested, next.Error, next.Revision = "pending", false, "", 1
	next.CreatedAt = time.Now().UTC()
	next.UpdatedAt = next.CreatedAt
	if _, replayed, err := s.BeginProjectPreparation(t.Context(), next); err != nil || replayed {
		t.Fatalf("new target remains blocked: replayed=%t err=%v", replayed, err)
	}
	if replay, replayed, err := s.BeginProjectPreparation(t.Context(), old); err != nil || !replayed || replay != recovered {
		t.Fatalf("old key replay restarted recovery: %+v replayed=%t err=%v", replay, replayed, err)
	}
}
