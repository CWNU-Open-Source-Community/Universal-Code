package store

import (
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
)

func TestNPMWorkspacePreparationLedgerBindsInstallToSharedRoot(t *testing.T) {
	for _, scenario := range []string{"success", "wrong-directory"} {
		t.Run(scenario, func(t *testing.T) {
			state, operation, jobs := pnpmPreparationLedgerFixture(t)
			operation.SeedDirectory = ""
			stored, _, err := state.BeginProjectPreparation(t.Context(), operation)
			if err != nil {
				t.Fatal(err)
			}
			advance := func(change func(*domain.ProjectPreparationOperation)) error {
				next := stored
				change(&next)
				next.Revision++
				next.UpdatedAt = time.Now().UTC()
				updated, err := state.UpdateProjectPreparation(t.Context(), next, stored.Revision)
				if err == nil {
					stored = updated
				}
				return err
			}
			if err := advance(func(o *domain.ProjectPreparationOperation) { o.State = "downloading" }); err != nil {
				t.Fatal(err)
			}
			if err := advance(func(o *domain.ProjectPreparationOperation) { o.State = "installing" }); err != nil {
				t.Fatal(err)
			}
			job := jobs["install"]
			if scenario == "wrong-directory" {
				job = jobs["install-cwd"]
			}
			err = advance(func(o *domain.ProjectPreparationOperation) { o.JobID = job.ID })
			if scenario == "wrong-directory" {
				if err == nil {
					t.Fatal("member-directory install receipt accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := advance(func(o *domain.ProjectPreparationOperation) { o.State = "ready" }); err != nil {
				t.Fatal(err)
			}
			read, err := state.GetProjectPreparation(t.Context(), operation.RunID, operation.ID)
			if err != nil || read.WorkspaceRoot != "." || read.Target != "apps/web" || read.FetchJobID != "" || read.SeedDirectory != "" {
				t.Fatalf("workspace receipt %+v %v", read, err)
			}
			if _, err := state.db.ExecContext(t.Context(), "UPDATE project_preparations SET workspace_root='apps',revision=revision+1 WHERE id=?", operation.ID); err == nil {
				t.Fatal("immutable workspace root changed")
			}
		})
	}
}

func TestNPMWorkspacePreparationMigrationPreservesExistingTriggers(t *testing.T) {
	state := openUnmigratedSQLiteStore(t, filepath.Join(t.TempDir(), "npm-workspace-v218.db"))
	if err := applyMigrationPrefixForTest(t.Context(), state, migrationPlan(), 218); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := state.db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_project_preparation_update'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := state.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := state.db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_master WHERE type='trigger' AND name='trg_project_preparation_update'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("migration changed existing receipt and authority checks")
	}
}
