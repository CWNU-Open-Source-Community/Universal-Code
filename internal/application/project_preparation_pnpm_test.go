package application

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

// Uses the production application, broker and job/approval ledgers. Only the
// Docker transport is doubled; native pnpm installation has a separate test.
type pnpmPreparationReadiness struct{ sandbox.ReadinessProbe }

func (pnpmPreparationReadiness) ProjectPreparationAvailable(context.Context, string) (bool, error) {
	return true, nil
}
func (pnpmPreparationReadiness) PNPMPreparationAvailable(context.Context, string) (bool, error) {
	return true, nil
}

type pnpmPreparationTransport struct {
	*dockerLifecycleSupervisorTestTransport
	afterFetch func() error
}

func (p *pnpmPreparationTransport) Cleanup(ctx context.Context, request sandbox.DockerContainerLifecycleRequest, fence sandbox.DockerContainerLifecycleFence) (sandbox.DockerContainerLifecycleCleanupResult, error) {
	result, err := p.dockerLifecycleSupervisorTestTransport.Cleanup(ctx, request, fence)
	if err == nil && p.deletes == 1 && p.afterFetch != nil {
		err = p.afterFetch()
	}
	return result, err
}

func pnpmPreparationFixture(t *testing.T) (*commandApprovalSandboxFixture, *ProjectPreparationService, *pnpmPreparationTransport) {
	t.Helper()
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, domain.RunExecutionProfileDocker, true, true)
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("fixture lease: %t %v", found, err)
	}
	if _, _, err = f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"package.json":                    `{"name":"fixture","private":true,"packageManager":"pnpm@10.28.1"}`,
		"pnpm-workspace.yaml":             "packages:\n  - apps/*\n  - packages/*\n",
		"apps/web/package.json":           `{"name":"web","scripts":{"test":"node --test"},"dependencies":{"shared-ui":"workspace:*"}}`,
		"packages/shared-ui/package.json": `{"name":"shared-ui","version":"1.0.0"}`,
		"pnpm-lock.yaml":                  "lockfileVersion: '9.0'\nimporters:\n  .: {}\n  apps/web:\n    dependencies:\n      shared-ui:\n        specifier: workspace:*\n        version: link:../../packages/shared-ui\n  packages/shared-ui: {}\npackages: {}\nsnapshots: {}\n",
	}
	for name, body := range files {
		file := filepath.Join(f.owned.Path, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := f.drydockFixture.service.Checkpoint(t.Context(), DrydockCheckpointRequest{RunID: f.turn.Run.ID, ExpectedGeneration: f.owned.Generation, OperationKey: "pnpm-fixture-manifests", RequestedBy: "operator", Title: "pnpm workspace", ConfirmObservedChanges: true})
	if err != nil {
		t.Fatal(err)
	}
	f.owned = checkpoint.Workspace
	executor := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	executor.service.docker.readiness = pnpmPreparationReadiness{executor.service.docker.readiness}
	transport := &pnpmPreparationTransport{dockerLifecycleSupervisorTestTransport: executor.service.docker.lifecycleTransport.(*dockerLifecycleSupervisorTestTransport)}
	executor.service.docker.lifecycleTransport = transport
	s, err := NewProjectPreparationService(f.st, f.mux, f.drydockFixture.service, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return f, s, transport
}

func TestPNPMPreparationKeepsFetchInstallAndScriptAuthoritySeparate(t *testing.T) {
	for _, scenario := range []string{"success", "scripts", "fetch-fails", "manifest-changes", "cancel-between-phases"} {
		t.Run(scenario, func(t *testing.T) {
			f, service, transport := pnpmPreparationFixture(t)
			if scenario == "fetch-fails" {
				transport.naturalExitCode = 1
			}
			if scenario == "manifest-changes" {
				transport.afterFetch = func() error {
					return os.WriteFile(filepath.Join(f.owned.Path, "packages", "shared-ui", "package.json"), []byte(`{"name":"shared-ui","version":"2.0.0"}`), 0600)
				}
			}
			if scenario == "cancel-between-phases" {
				transport.afterFetch = func() error {
					op, found, err := f.st.ActiveProjectPreparation(t.Context(), f.turn.Run.ID)
					if err != nil || !found {
						t.Errorf("active preparation: %t %v", found, err)
						return err
					}
					_, err = service.change(t.Context(), op.RunID, op.ID, func(v *domain.ProjectPreparationOperation) { v.CancelRequested = true })
					return err
				}
			}
			view, err := service.Inspect(t.Context(), f.turn.Run.ID, "apps/web")
			if err != nil || !view.Available || view.Target != "apps/web" || view.Plan.WorkspaceRoot != "." {
				t.Fatalf("inspect: %+v %#v", view, err)
			}
			request := PrepareProjectRequest{RunID: f.turn.Run.ID, Target: "apps/web", ExpectedFingerprint: view.Plan.Fingerprint, AllowedSources: []string{}, OperationKey: "project-preparation-pnpm-" + scenario, RequestedBy: "operator", ConfirmExecution: true, RunInstallScripts: scenario == "scripts"}
			view, err = service.Prepare(t.Context(), request)
			if err != nil || view.Latest == nil {
				t.Fatalf("prepare: %+v %v", view, err)
			}
			var result ProjectPreparationOperationView
			deadline := time.Now().Add(time.Minute)
			for time.Now().Before(deadline) {
				result, err = service.GetOperation(t.Context(), f.turn.Run.ID, view.Latest.ID)
				if err != nil {
					t.Fatal(err)
				}
				if result.State != "pending" && result.State != "downloading" && result.State != "installing" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			wantJobs, wantState := 2, "ready"
			if scenario == "scripts" {
				wantJobs = 3
			} else if scenario == "fetch-fails" || scenario == "manifest-changes" {
				wantJobs, wantState = 1, "failed"
			} else if scenario == "cancel-between-phases" {
				wantJobs, wantState = 1, "cancelled"
			}
			if result.State != wantState {
				t.Fatalf("result: %+v; want %s", result, wantState)
			}
			jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.turn.Run.ID, Limit: 10})
			if err != nil || len(jobs) != wantJobs {
				t.Fatalf("jobs: %+v %v; want %d", jobs, err, wantJobs)
			}
			var fetch, install bool
			for _, job := range jobs {
				if !job.Adapter.SameBackend(f.service.adapter) {
					t.Fatalf("job escaped Docker: %+v", job)
				}
				fetch = fetch || strings.Contains(job.IntentJSON, "Populate pnpm dependency store")
				install = install || strings.Contains(job.IntentJSON, "Prepare project dependencies")
			}
			if !fetch || (wantState == "ready" && !install) {
				t.Fatalf("missing separate job receipts: %+v", jobs)
			}
			op, err := f.st.GetProjectPreparation(t.Context(), f.turn.Run.ID, result.ID)
			if err != nil || op.FetchJobID == "" || (wantState == "ready" && (op.JobID == "" || op.JobID == op.FetchJobID)) {
				t.Fatalf("phase receipts: %+v %v", op, err)
			}
			// Replaying the same request reads its result without another install.
			replayed, err := service.Prepare(t.Context(), request)
			if err != nil || replayed.Latest == nil || replayed.Latest.ID != result.ID {
				t.Fatalf("replay: %+v %v", replayed, err)
			}
			if scenario == "success" {
				// The next user action must run and read the selected member's
				// test through the same project-action service used by the UI.
				actions, err := service.jobs.List(t.Context(), f.turn.Run.ID, "apps/web")
				if err != nil || len(actions.Actions) == 0 || !actions.Actions[0].Available || actions.Actions[0].CommandID != "test.pnpm" {
					t.Fatalf("pnpm test action: %+v %v", actions, err)
				}
				testRequest := ProjectActionRequest{RunID: f.turn.Run.ID, Slot: "test", Target: "apps/web", OperationKey: "project-pnpm-member-test", RequestedBy: "operator", ConfirmExecution: true}
				started, err := service.jobs.Run(t.Context(), testRequest)
				if err != nil {
					t.Fatal(err)
				}
				completed := waitProjectAction(t, service.jobs, f.turn.Run.ID, started.Job.ID, time.Minute)
				job, err := f.st.GetCommandRuntimeJob(t.Context(), started.Job.ID)
				if err != nil || completed.CommandID != "test.pnpm" || !preparationCommandSucceeded(job) || job.WorkingDirectory != "apps/web" {
					t.Fatalf("member test: %+v %+v %v", completed, job, err)
				}
				replayedTest, err := service.jobs.Run(t.Context(), testRequest)
				if err != nil || !replayedTest.Replayed || replayedTest.Job.ID != job.ID {
					t.Fatalf("member test replay: %+v %v", replayedTest, err)
				}
				wantJobs++
			}
			f.assertDispatchCounts(t, wantJobs, wantJobs)
		})
	}
}
