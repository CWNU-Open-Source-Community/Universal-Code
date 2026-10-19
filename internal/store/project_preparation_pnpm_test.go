package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runner"
)

func TestPNPMPreparationLedgerBindsEachPhaseToItsDirectoryAndPurpose(t *testing.T) {
	for _, scenario := range []string{"success", "fetch-cwd", "fetch-purpose", "install-cwd", "install-purpose", "failed-fetch", "missing-fetch"} {
		t.Run(scenario, func(t *testing.T) {
			state, operation, jobs := pnpmPreparationLedgerFixture(t)
			stored, _, err := state.BeginProjectPreparation(t.Context(), operation)
			if err != nil {
				t.Fatal(err)
			}
			advance := func(change func(*domain.ProjectPreparationOperation)) error {
				candidate := stored
				change(&candidate)
				candidate.Revision++
				candidate.UpdatedAt = time.Now().UTC()
				updated, err := state.UpdateProjectPreparation(t.Context(), candidate, stored.Revision)
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
			fetch := jobs["fetch"]
			if scenario == "fetch-cwd" || scenario == "fetch-purpose" || scenario == "failed-fetch" {
				fetch = jobs[scenario]
			}
			if scenario != "missing-fetch" {
				err = advance(func(o *domain.ProjectPreparationOperation) { o.FetchJobID = fetch.ID })
				if scenario == "fetch-cwd" || scenario == "fetch-purpose" {
					if err == nil {
						t.Fatal("ledger accepted an unrelated fetch Job")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			install := jobs["install"]
			if scenario == "install-cwd" || scenario == "install-purpose" {
				install = jobs[scenario]
			}
			err = advance(func(o *domain.ProjectPreparationOperation) { o.JobID = install.ID })
			if scenario == "install-cwd" || scenario == "install-purpose" {
				if err == nil {
					t.Fatal("ledger accepted an unrelated install Job")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "success" {
				for _, assignment := range []string{"workspace_root='apps'", "seed_directory='different-seed'", "fetch_job_id='different-job'"} {
					if _, err := state.db.ExecContext(t.Context(), "UPDATE project_preparations SET "+assignment+",revision=revision+1 WHERE id=?", operation.ID); err == nil {
						t.Fatal("SQL replaced immutable preparation binding", assignment)
					}
				}
			}
			err = advance(func(o *domain.ProjectPreparationOperation) { o.State = "ready" })
			if scenario == "failed-fetch" || scenario == "missing-fetch" {
				if err == nil {
					t.Fatal("readiness accepted an unsuccessful fetch")
				}
				if err := advance(func(o *domain.ProjectPreparationOperation) { o.State, o.Error = "failed", "fetch did not succeed" }); err != nil {
					t.Fatal("failed fetch could not terminate", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := state.GetProjectPreparation(t.Context(), operation.RunID, operation.ID)
			if err != nil || got.State != "ready" || got.JobID != install.ID || got.FetchJobID != fetch.ID || got.WorkspaceRoot != "." || got.SeedDirectory != operation.SeedDirectory {
				t.Fatalf("phase receipts lost: %+v %v", got, err)
			}
		})
	}
}

func pnpmPreparationLedgerFixture(t *testing.T) (*SQLiteStore, domain.ProjectPreparationOperation, map[string]runner.CommandRuntimeJob) {
	t.Helper()
	state, err := Open(filepath.Join(t.TempDir(), "pnpm-preparation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	adapter := commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeDockerSandboxBackend, "pnpm-test", strings.Repeat("a", 64))
	template, actor := commandScopeV189Fixture(t, state, "docker", adapter, "pnpm-preparation")
	at := time.Now().UTC()
	op := domain.ProjectPreparationOperation{ID: "prep-pnpm-ledger", RunID: template.RunID, KeyDigest: strings.Repeat("a", 64), RequestFingerprint: strings.Repeat("b", 64), PlanFingerprint: strings.Repeat("c", 64),
		Target: "apps/web", WorkspaceRoot: ".", SeedDirectory: projectprep.PNPMSeedDirectory("apps/web"), Backend: adapter.Backend, RequestedBy: "operator", OwnerID: "preparation-owner",
		RequestJSON: `{"run_install_scripts":false}`, PermissionRevision: template.PermissionRevision, State: "pending", Revision: 1, CreatedAt: at, UpdatedAt: at}
	jobs := map[string]runner.CommandRuntimeJob{}
	for _, name := range []string{"fetch", "install", "fetch-cwd", "fetch-purpose", "install-cwd", "install-purpose", "failed-fetch"} {
		job := template
		job.ID, job.OperationDigest, job.RequestFingerprint = "job-pnpm-"+name, testCommandRuntimeDigest("pnpm-operation-"+name), testCommandRuntimeDigest("pnpm-request-"+name)
		purpose := "Populate pnpm dependency store " + op.RequestFingerprint
		job.WorkingDirectory = op.SeedDirectory
		if strings.HasPrefix(name, "install") {
			job.WorkingDirectory, purpose = op.WorkspaceRoot, "Prepare project dependencies "+op.RequestFingerprint
		}
		if strings.HasSuffix(name, "-cwd") {
			job.WorkingDirectory = op.Target
		}
		if strings.HasSuffix(name, "-purpose") {
			purpose = "Unrelated operator command"
		}
		raw, _ := json.Marshal(map[string]string{"purpose": purpose})
		job.IntentJSON = string(raw)
		job, _, err = state.PrepareCommandRuntimeJobForAgent(t.Context(), job, actor)
		if err != nil {
			t.Fatalf("prepare %s: %v", name, err)
		}
		started := job.CreatedAt.Add(time.Millisecond)
		job.State, job.StartedAt, job.JobAssignedAtCreation, job.Version, job.UpdatedAt = runner.CommandRuntimeJobRunning, &started, true, job.Version+1, started
		job, err = state.UpdateCommandRuntimeJob(t.Context(), job, job.Version-1)
		if err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
		completed, exit := started.Add(time.Millisecond), 0
		if name == "failed-fetch" {
			exit = 1
		}
		job.State, job.CompletedAt, job.ExitCode, job.TreeReaped = runner.CommandRuntimeJobCompleted, &completed, &exit, true
		job.StdoutSHA256, job.StderrSHA256, job.Version, job.UpdatedAt = testCommandRuntimeDigest(""), testCommandRuntimeDigest(""), job.Version+1, completed
		job, err = state.UpdateCommandRuntimeJob(t.Context(), job, job.Version-1)
		if err != nil {
			t.Fatalf("complete %s: %v", name, err)
		}
		jobs[name] = job
	}
	lease, found, err := state.GetRunExecutionLease(t.Context(), op.RunID)
	if err != nil || !found {
		t.Fatal("fixture lease missing", err)
	}
	if _, _, err := state.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	return state, op, jobs
}

func TestProjectPreparationV214MigrationKeepsLegacyTargetAndReceipt(t *testing.T) {
	state := openUnmigratedSQLiteStore(t, filepath.Join(t.TempDir(), "preparation-v214.db"))
	if err := applyMigrationPrefixForTest(t.Context(), state, migrationPlan(), 214); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(state).Create(t.Context(), application.CreateRunRequest{Goal: "migrate legacy preparation", Profile: "code", Surface: "code", Phase: "deliver", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	at := ts(time.Now().UTC())
	_, err = state.db.ExecContext(t.Context(), `INSERT INTO project_preparations(id,run_id,key_digest,request_fingerprint,target,plan_fingerprint,backend,requested_by,request_json,owner_id,permission_revision,state,completed_packages,total_packages,cache_hits,downloaded_bytes,error,cancel_requested,revision,created_at,updated_at) VALUES ('legacy-preparation',?,?,?,?,?,'local_windows_lpac','operator','{}','legacy-owner',1,'pending',0,0,0,0,'',0,1,?,?)`, run.ID, strings.Repeat("a", 64), strings.Repeat("b", 64), "web", strings.Repeat("c", 64), at, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	op, err := state.GetProjectPreparation(t.Context(), run.ID, "legacy-preparation")
	if err != nil || op.Target != "web" || op.WorkspaceRoot != "" || op.SeedDirectory != "" || op.FetchJobID != "" {
		t.Fatalf("legacy receipt changed %+v %v", op, err)
	}
	op.State, op.Revision, op.UpdatedAt = "failed", op.Revision+1, time.Now().UTC()
	if _, err := state.UpdateProjectPreparation(t.Context(), op, op.Revision-1); err != nil {
		t.Fatal("legacy update rejected after migration", err)
	}
}

func TestPNPMPreparationDomainRejectsAnotherTargetsSeed(t *testing.T) {
	at := time.Now().UTC()
	op := domain.ProjectPreparationOperation{ID: "prep-domain", RunID: "run-domain", KeyDigest: strings.Repeat("a", 64), RequestFingerprint: strings.Repeat("b", 64), PlanFingerprint: strings.Repeat("c", 64), Target: "apps/web", WorkspaceRoot: ".", SeedDirectory: projectprep.PNPMSeedDirectory("apps/web"), Backend: application.CommandRuntimeDockerSandboxBackend, RequestedBy: "operator", OwnerID: "owner", RequestJSON: `{}`, PermissionRevision: 1, State: "pending", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if err := op.Validate(); err != nil {
		t.Fatal("valid workspace preparation rejected", err)
	}
	op.SeedDirectory = projectprep.PNPMSeedDirectory("another-target")
	if err := op.Validate(); err == nil {
		t.Fatal("seed for another target was accepted")
	}
}
