package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestProjectPreparationLedgerReplayCancellationAndImmutability(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "preparation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, run, err := application.NewRunService(s).Create(t.Context(), application.CreateRunRequest{Goal: "prepare dependencies", Profile: "code", Surface: "code", Phase: "deliver", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o := domain.ProjectPreparationOperation{ID: "prep-one", RunID: run.ID, KeyDigest: strings.Repeat("a", 64), RequestFingerprint: strings.Repeat("b", 64), PlanFingerprint: strings.Repeat("c", 64), Target: ".", Backend: "local_windows_lpac", RequestedBy: "operator", OwnerID: "preparation-owner", RequestJSON: `{"allowed_sources":[]}`, PermissionRevision: 1, State: "pending", TotalPackages: 2, Revision: 1, CreatedAt: now, UpdatedAt: now}
	got, replay, err := s.BeginProjectPreparation(t.Context(), o)
	if err != nil || replay {
		t.Fatalf("begin %+v %v", got, err)
	}
	if got, replay, err = s.BeginProjectPreparation(t.Context(), o); err != nil || !replay || got.ID != o.ID {
		t.Fatalf("replay %+v %v", got, err)
	}
	other := o
	other.RequestFingerprint = strings.Repeat("d", 64)
	if _, _, err = s.BeginProjectPreparation(t.Context(), other); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed intent: %v", err)
	}
	other = o
	other.ID = "prep-two"
	other.KeyDigest = strings.Repeat("e", 64)
	if _, _, err = s.BeginProjectPreparation(t.Context(), other); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("concurrent admission: %v", err)
	}
	got.State = "downloading"
	got.Revision++
	got.UpdatedAt = now.Add(time.Second)
	got, err = s.UpdateProjectPreparation(t.Context(), got, 1)
	if err != nil {
		t.Fatal(err)
	}
	stale := got
	// A progress label cannot manufacture readiness without a successful Job.
	if _, err = s.db.ExecContext(t.Context(), `UPDATE project_preparations SET state='installing',completed_packages=total_packages,revision=revision+1,updated_at=? WHERE id='prep-one'`, ts(now.Add(1500*time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE project_preparations SET state='ready',revision=revision+1,updated_at=? WHERE id='prep-one'`, ts(now.Add(1700*time.Millisecond))); err == nil {
		t.Fatal("SQL fabricated readiness without an installed Job")
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE project_preparations SET job_id='unrelated-job',revision=revision+1,updated_at=? WHERE id='prep-one'`, ts(now.Add(1800*time.Millisecond))); err == nil {
		t.Fatal("SQL attached an unrelated Job to preparation")
	}
	got, err = s.GetProjectPreparation(t.Context(), run.ID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale = got
	got.CancelRequested = true
	got.Revision++
	got.UpdatedAt = now.Add(2 * time.Second)
	got, err = s.UpdateProjectPreparation(t.Context(), got, 3)
	if err != nil {
		t.Fatal(err)
	}
	stale.CompletedPackages = 1
	stale.Revision++
	stale.UpdatedAt = now.Add(3 * time.Second)
	if _, err = s.UpdateProjectPreparation(t.Context(), stale, 3); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("lost cancellation via stale worker: %v", err)
	}
	for _, query := range []string{`UPDATE project_preparations SET request_json='{"allowed_sources":["https://evil.example"]}',revision=revision+1 WHERE id='prep-one'`, `UPDATE project_preparations SET cancel_requested=0,revision=revision+1 WHERE id='prep-one'`, `DELETE FROM project_preparations WHERE id='prep-one'`} {
		if _, err = s.db.ExecContext(t.Context(), query); err == nil {
			t.Fatalf("SQL accepted %s", query)
		}
	}
	got.State = "cancelled"
	got.Revision++
	got.UpdatedAt = now.Add(4 * time.Second)
	got, err = s.UpdateProjectPreparation(t.Context(), got, 4)
	if err != nil {
		t.Fatal(err)
	}
	got.State = "installing"
	got.Revision++
	got.UpdatedAt = now.Add(5 * time.Second)
	if _, err = s.UpdateProjectPreparation(t.Context(), got, 5); err == nil {
		t.Fatal("terminal operation restarted")
	}
	if _, _, err = s.BeginProjectPreparation(t.Context(), other); err != nil {
		t.Fatalf("fresh retry rejected: %v", err)
	}
	var integrity string
	if err = s.db.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity %s %v", integrity, err)
	}
}
