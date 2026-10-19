package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/dependencycache"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
)

type preparationBrokerTripwire struct{ calls int }

func (b *preparationBrokerTripwire) Prepare(context.Context, projectprep.Plan, []string, string, func(dependencycache.Progress), func(context.Context) error) (dependencycache.Stats, error) {
	b.calls++
	return dependencycache.Stats{}, errors.New("late worker reached dependency broker")
}
func (*preparationBrokerTripwire) CachedPackages(context.Context, projectprep.Plan) (int, error) {
	return 0, nil
}

func TestProjectPreparationLateInterruptedWorkerCannotDownloadOrCreateJob(t *testing.T) {
	f := newCommandApprovalSandboxFixture(t, domain.RunExecutionPermissionAsk, domain.RunExecutionProfileDocker, true)
	lease, found, err := f.st.GetRunExecutionLease(t.Context(), f.turn.Run.ID)
	if err != nil || !found {
		t.Fatalf("fixture lease %t %v", found, err)
	}
	if _, _, err := f.st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.owned.Path, "go.mod"), []byte("module example.org/late\ngo 1.26.9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := f.drydockFixture.service.Checkpoint(t.Context(), DrydockCheckpointRequest{RunID: f.turn.Run.ID, ExpectedGeneration: f.owned.Generation, OperationKey: "late-worker-manifest", RequestedBy: "operator", Title: "Project manifest", ConfirmObservedChanges: true})
	if err != nil {
		t.Fatal(err)
	}
	f.owned = checkpoint.Workspace
	plan, err := projectprep.Detect(t.Context(), f.owned.Path, ".")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewProjectPreparationService(f.st, f.mux, f.drydockFixture.service, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	tripwire := &preparationBrokerTripwire{}
	s.broker = tripwire
	r := PrepareProjectRequest{RunID: f.turn.Run.ID, Target: ".", ExpectedFingerprint: plan.Fingerprint, AllowedSources: []string{}, OperationKey: "late-worker-operation", RequestedBy: "operator", ConfirmExecution: true}
	raw, _ := json.Marshal(r)
	now := time.Now().UTC()
	o := domain.ProjectPreparationOperation{ID: "prep-late-worker", RunID: r.RunID, KeyDigest: runmutation.Fingerprint(r.OperationKey), RequestFingerprint: preparationRequestFingerprint(r), Target: ".", PlanFingerprint: plan.Fingerprint, Backend: CommandRuntimeDockerSandboxBackend, RequestedBy: r.RequestedBy, RequestJSON: string(raw), OwnerID: s.ownerID, PermissionRevision: 1, State: "pending", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := f.st.BeginProjectPreparation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := s.change(t.Context(), o.RunID, o.ID, func(v *domain.ProjectPreparationOperation) { v.State = "interrupted" }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	s.wg.Add(1)
	s.execute(ctx, cancel, o, r, plan, f.owned.Path, f.service)
	if tripwire.calls != 0 {
		t.Fatalf("late worker downloaded %d times", tripwire.calls)
	}
	f.assertDispatchCounts(t, 0, 0)
}

func savePreparationForFinalization(t *testing.T, f *operatorCommandFixture, s *ProjectPreparationService, key string) domain.ProjectPreparationOperation {
	t.Helper()
	now := time.Now().UTC().Add(-time.Second)
	o := domain.ProjectPreparationOperation{ID: "prep-" + key, RunID: f.run.ID, KeyDigest: runmutation.Fingerprint(key), RequestFingerprint: runmutation.Fingerprint("request", key), Target: ".", PlanFingerprint: runmutation.Fingerprint("plan", key), Backend: CommandRuntimeLocalSandboxBackend, RequestedBy: "operator", RequestJSON: `{"run_install_scripts":false}`, OwnerID: s.ownerID, PermissionRevision: 1, State: "pending", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := f.st.BeginProjectPreparation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestProjectPreparationFinalCancellationWinsAndProgressClockStaysMonotonic(t *testing.T) {
	f, s := preparationFixture(t)
	o := savePreparationForFinalization(t, f, s, "final-cancel")
	future := time.Now().UTC().Add(time.Minute)
	o.State, o.CancelRequested, o.Revision, o.UpdatedAt = "downloading", true, 2, future
	if _, err := f.st.UpdateProjectPreparation(t.Context(), o, 1); err != nil {
		t.Fatal(err)
	}
	final, err := s.finishPreparation(t.Context(), o.RunID, o.ID, f.root, "", "", "", nil)
	if err != nil || final.State != "cancelled" || !final.CancelRequested || final.UpdatedAt.Before(future) {
		t.Fatalf("late cancellation/clock rollback lost: %+v %v", final, err)
	}
	o = savePreparationForFinalization(t, f, s, "final-failed")
	final, err = s.finishPreparation(t.Context(), o.RunID, o.ID, f.root, "", "", "", errors.New("install exit 1"))
	if err != nil || final.State != "failed" || final.Error != "install exit 1" {
		t.Fatalf("failed install became ready: %+v %v", final, err)
	}
}

func TestProjectPreparationSuccessRequiresCompletedReapedCommand(t *testing.T) {
	zero := 0
	job := runner.CommandRuntimeJob{State: runner.CommandRuntimeJobCompleted, ExitCode: &zero, TreeReaped: true}
	if !preparationCommandSucceeded(job) {
		t.Fatal("successful command rejected")
	}
	job.State = runner.CommandRuntimeJobCancelled
	if preparationCommandSucceeded(job) {
		t.Fatal("cancelled command with exit 0 became ready")
	}
	job.State, job.TreeReaped = runner.CommandRuntimeJobCompleted, false
	if preparationCommandSucceeded(job) {
		t.Fatal("unreaped command became ready")
	}
}

// This selection double tests admission/Close ordering. Its service lacks live
// command authority, so no native or guest process can be created.
type preparationAdmissionRuntime struct {
	*CommandRuntimeService
	selected *CommandRuntimeService
}

func (s preparationAdmissionRuntime) projectCommandService(context.Context, string) (*CommandRuntimeService, error) {
	return s.selected, nil
}

type preparationAdmissionStore struct {
	ProjectPreparationStore
	entered, release chan struct{}
}

func (s preparationAdmissionStore) BeginProjectPreparation(ctx context.Context, o domain.ProjectPreparationOperation) (domain.ProjectPreparationOperation, bool, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return o, false, ctx.Err()
	}
	return s.ProjectPreparationStore.BeginProjectPreparation(ctx, o)
}

func TestProjectPreparationCloseSerializesAdmissionAndWaitGroup(t *testing.T) {
	f, s := preparationFixture(t)
	s.runtime = preparationAdmissionRuntime{CommandRuntimeService: f.service, selected: &CommandRuntimeService{store: f.st, adapter: commandruntimeadapter.SandboxedWorkspace(CommandRuntimeSBXBackend, "close-fixture", "generation")}}
	plan, err := projectprep.Detect(t.Context(), f.root, ".")
	if err != nil {
		t.Fatal(err)
	}
	r := PrepareProjectRequest{RunID: f.run.ID, Target: ".", ExpectedFingerprint: plan.Fingerprint, AllowedSources: []string{}, OperationKey: "prepare-during-close", RequestedBy: "operator", ConfirmExecution: true}
	entered, release := make(chan struct{}), make(chan struct{})
	s.store = preparationAdmissionStore{ProjectPreparationStore: f.st, entered: entered, release: release}
	prepared := make(chan error, 1)
	go func() { _, err := s.Prepare(t.Context(), r); prepared <- err }()
	<-entered
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	close(release)
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close missed its admitted worker")
	}
	o, found, err := f.st.LatestProjectPreparation(t.Context(), f.run.ID, ".")
	if err != nil || !found || !o.Terminal() || o.State == "ready" {
		t.Fatalf("closed service left an unowned operation: %+v %v", o, err)
	}
	r.OperationKey = "prepare-after-close"
	if _, err := s.Prepare(t.Context(), r); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("closed service accepted admission: %v", err)
	}
	key := runmutation.Fingerprint("project_preparation_key.v1", r.RunID, r.OperationKey)
	if _, err := f.st.GetProjectPreparation(t.Context(), r.RunID, "prep-"+key); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("closed admission created durable pending work: %v", err)
	}
}

func preparationFixture(t *testing.T) (*operatorCommandFixture, *ProjectPreparationService) {
	t.Helper()
	f := newOperatorCommandFixture(t, domain.RunExecutionPermissionAsk)
	for name, body := range map[string]string{"package.json": `{"name":"preparation-fixture","version":"1.0.0","scripts":{"test":"node --test"}}`, "package-lock.json": `{"name":"preparation-fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"preparation-fixture","version":"1.0.0"}}}`} {
		if err := os.WriteFile(filepath.Join(f.root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewProjectPreparationService(f.st, f.service, nil, home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return f, s
}

func TestProjectPreparationNeverFallsBackToHost(t *testing.T) {
	f, s := preparationFixture(t)
	v, err := s.Inspect(t.Context(), f.run.ID, ".")
	if err != nil || !v.Plan.Ready || v.Available || !strings.Contains(v.Reason, "请选择") && !strings.Contains(v.Reason, "请为任务选择") {
		t.Fatalf("host preparation %+v %v", v, err)
	}
	_, err = s.Prepare(t.Context(), PrepareProjectRequest{RunID: f.run.ID, Target: ".", ExpectedFingerprint: v.Plan.Fingerprint, AllowedSources: []string{}, OperationKey: "preparation-host-denied", RequestedBy: "operator", ConfirmExecution: true})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("host install accepted: %v", err)
	}
	if _, found, err := f.st.LatestProjectPreparation(t.Context(), f.run.ID, "."); err != nil || found {
		t.Fatalf("denied request created intent: %t %v", found, err)
	}
}

func TestProjectPreparationRebuildRechecksReviewedFilesAndWorkspace(t *testing.T) {
	f, _ := preparationFixture(t)
	plan, err := projectprep.Detect(t.Context(), f.root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if err := recheckPreparationPlan(t.Context(), f.root, f.root, plan); err != nil {
		t.Fatal(err)
	}
	if err := recheckPreparationPlan(t.Context(), t.TempDir(), f.root, plan); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed workspace accepted for rebuild: %v", err)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(f.root, name)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			if err := recheckPreparationPlan(t.Context(), f.root, f.root, plan); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("changed %s accepted for rebuild: %v", name, err)
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProjectPreparationColdReplaySurvivesMissingProjectAndNeverRestarts(t *testing.T) {
	f, s := preparationFixture(t)
	plan, err := projectprep.Detect(t.Context(), f.root, ".")
	if err != nil {
		t.Fatal(err)
	}
	r := PrepareProjectRequest{RunID: f.run.ID, Target: ".", ExpectedFingerprint: plan.Fingerprint, AllowedSources: []string{}, OperationKey: "preparation-replay-key", RequestedBy: "operator", ConfirmExecution: true}
	key := runmutation.Fingerprint("project_preparation_key.v1", r.RunID, r.OperationKey)
	raw, _ := json.Marshal(r)
	at := time.Now().UTC().Add(-time.Minute)
	o := domain.ProjectPreparationOperation{ID: "prep-" + key, RunID: r.RunID, KeyDigest: key, RequestFingerprint: preparationRequestFingerprint(r), Target: ".", PlanFingerprint: plan.Fingerprint, Backend: CommandRuntimeLocalSandboxBackend, RequestedBy: r.RequestedBy, RequestJSON: string(raw), OwnerID: "old-preparation-process", PermissionRevision: 1, State: "pending", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if _, _, err = f.st.BeginProjectPreparation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(f.root, "package.json")); err != nil {
		t.Fatal(err)
	}
	s.runtime = nil
	v, err := s.Prepare(t.Context(), r)
	if err != nil || v.Latest == nil || v.Latest.ID != o.ID || v.Latest.State != "interrupted" || len(v.Plan.Fingerprint) != 64 || v.Plan.Files == nil || v.Plan.Sources == nil || v.Plan.Issues == nil {
		t.Fatalf("cold receipt %+v %v", v, err)
	}
	r.RunInstallScripts = true
	if _, err = s.Prepare(t.Context(), r); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed script grant reused key: %v", err)
	}
	op, err := s.Cancel(t.Context(), o.RunID, o.ID)
	if err != nil || op.State != "interrupted" || op.CanCancel {
		t.Fatalf("orphan finalization %+v %v", op, err)
	}
	op, err = s.Cancel(t.Context(), o.RunID, o.ID)
	if err != nil || op.State != "interrupted" {
		t.Fatalf("cancel replay %+v %v", op, err)
	}
	if _, err = s.GetOperation(t.Context(), "unrelated-run", o.ID); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("foreign operation exposed %v", err)
	}
}
