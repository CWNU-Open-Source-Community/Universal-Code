package application

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/runmutation"
)

type preparationRecoveryFixture struct {
	f                   *operatorCommandFixture
	s                   *ProjectPreparationService
	old                 domain.ProjectPreparationOperation
	oldRequest, request PrepareProjectRequest
}

func newPreparationRecoveryFixture(t *testing.T) preparationRecoveryFixture {
	t.Helper()
	f, s := preparationFixture(t)
	s.runtime = preparationAdmissionRuntime{CommandRuntimeService: f.service, selected: &CommandRuntimeService{store: f.st, adapter: commandruntimeadapter.SandboxedWorkspace(CommandRuntimeSBXBackend, "recovery-fixture", "generation")}}
	if err := os.Mkdir(filepath.Join(f.root, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		body, err := os.ReadFile(filepath.Join(f.root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, "web", name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldPlan, err := projectprep.Detect(t.Context(), f.root, "web")
	if err != nil {
		t.Fatal(err)
	}
	oldRequest := PrepareProjectRequest{RunID: f.run.ID, Target: "web", ExpectedFingerprint: oldPlan.Fingerprint, AllowedSources: []string{}, OperationKey: "previous-web-preparation", RequestedBy: "operator", ConfirmExecution: true}
	key := runmutation.Fingerprint("project_preparation_key.v1", oldRequest.RunID, oldRequest.OperationKey)
	raw, _ := json.Marshal(oldRequest)
	at := time.Now().UTC().Add(-time.Minute)
	old := domain.ProjectPreparationOperation{ID: "prep-" + key, RunID: f.run.ID, KeyDigest: key, RequestFingerprint: preparationRequestFingerprint(oldRequest), PlanFingerprint: oldPlan.Fingerprint, Target: "web", Backend: CommandRuntimeSBXBackend, RequestedBy: "operator", OwnerID: "previous-process", RequestJSON: string(raw), PermissionRevision: 1, State: "pending", Revision: 1, CreatedAt: at, UpdatedAt: at}
	if _, _, err := f.st.BeginProjectPreparation(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	plan, err := projectprep.Detect(t.Context(), f.root, ".")
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareProjectRequest{RunID: f.run.ID, Target: ".", ExpectedFingerprint: plan.Fingerprint, AllowedSources: []string{}, OperationKey: "new-root-preparation", RequestedBy: "operator", ConfirmExecution: true}
	return preparationRecoveryFixture{f: f, s: s, old: old, oldRequest: oldRequest, request: request}
}

func TestProjectPreparationStaleOtherTargetDoesNotBlockNewPreparation(t *testing.T) {
	fixture := newPreparationRecoveryFixture(t)
	view, err := fixture.s.Inspect(t.Context(), fixture.f.run.ID, ".")
	if err != nil || !view.Available || view.Latest != nil {
		t.Fatalf("new target inspection: %+v %v", view, err)
	}
	result, err := fixture.s.Prepare(t.Context(), fixture.request)
	if err != nil || result.Latest == nil || result.Latest.ID == fixture.old.ID {
		t.Fatalf("new target blocked by abandoned preparation: %+v %v", result, err)
	}
	old, err := fixture.f.st.GetProjectPreparation(t.Context(), fixture.old.RunID, fixture.old.ID)
	if err != nil || old.State != "interrupted" || old.Revision != fixture.old.Revision+1 {
		t.Fatalf("old target not durably recovered: %+v %v", old, err)
	}
	replay, err := fixture.s.Prepare(t.Context(), fixture.oldRequest)
	if err != nil || replay.Latest == nil || replay.Latest.ID != old.ID || replay.Latest.State != "interrupted" {
		t.Fatalf("old key did not replay its original result: %+v %v", replay, err)
	}
	unchanged, err := fixture.f.st.GetProjectPreparation(t.Context(), old.RunID, old.ID)
	if err != nil || unchanged.Revision != old.Revision {
		t.Fatalf("old key replay mutated recovery: %+v %v", unchanged, err)
	}
}

func TestProjectPreparationRecoveryPreservesLiveOtherTarget(t *testing.T) {
	for _, protection := range []string{"local owner", "Run lease", "recent progress"} {
		t.Run(protection, func(t *testing.T) {
			fixture := newPreparationRecoveryFixture(t)
			switch protection {
			case "local owner":
				fixture.s.mu.Lock()
				fixture.s.active[fixture.old.ID] = func() {}
				fixture.s.mu.Unlock()
				t.Cleanup(func() {
					fixture.s.mu.Lock()
					delete(fixture.s.active, fixture.old.ID)
					fixture.s.mu.Unlock()
				})
			case "Run lease":
				lease, err := fixture.f.st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: fixture.old.RunID, OwnerID: "live-preparation-owner", TTL: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _, _ = fixture.f.st.ReleaseRunExecutionLease(t.Context(), lease.Lease) })
			case "recent progress":
				fixture.old.Revision++
				fixture.old.UpdatedAt = time.Now().UTC()
				if _, err := fixture.f.st.UpdateProjectPreparation(t.Context(), fixture.old, fixture.old.Revision-1); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := fixture.s.Prepare(t.Context(), fixture.request); apperror.CodeOf(err) != apperror.CodeConflict {
				t.Fatalf("live preparation was replaced: %v", err)
			}
			old, err := fixture.f.st.GetProjectPreparation(t.Context(), fixture.old.RunID, fixture.old.ID)
			if err != nil || old.State != "pending" || old.Revision != fixture.old.Revision {
				t.Fatalf("live owner changed: %+v %v", old, err)
			}
		})
	}
}

func TestProjectPreparationCancelStillSignalsLiveOwnerDuringRecovery(t *testing.T) {
	fixture := newPreparationRecoveryFixture(t)
	var cancelled atomic.Bool
	fixture.s.mu.Lock()
	fixture.s.active[fixture.old.ID] = func() { cancelled.Store(true) }
	fixture.s.mu.Unlock()
	t.Cleanup(func() {
		fixture.s.mu.Lock()
		delete(fixture.s.active, fixture.old.ID)
		fixture.s.mu.Unlock()
	})
	view, err := fixture.s.Cancel(t.Context(), fixture.old.RunID, fixture.old.ID)
	if err != nil || view.State != "pending" || view.CanCancel || !cancelled.Load() {
		t.Fatalf("live cancellation changed during recovery: %+v %v", view, err)
	}
	stored, err := fixture.f.st.GetProjectPreparation(t.Context(), fixture.old.RunID, fixture.old.ID)
	if err != nil || stored.State != "pending" || !stored.CancelRequested || stored.Revision != fixture.old.Revision+1 {
		t.Fatalf("cancellation did not retain its live owner: %+v %v", stored, err)
	}
}

type preparationRecoveryAdmissionStore struct {
	ProjectPreparationStore
	readEntered, readRelease     chan struct{}
	finishEntered, finishRelease chan struct{}
	finishOnce                   sync.Once
	finishCalls                  atomic.Int32
}

func (s *preparationRecoveryAdmissionStore) ActiveProjectPreparation(ctx context.Context, runID string) (domain.ProjectPreparationOperation, bool, error) {
	o, found, err := s.ProjectPreparationStore.ActiveProjectPreparation(ctx, runID)
	s.readEntered <- struct{}{}
	select {
	case <-s.readRelease:
	case <-ctx.Done():
		return o, found, ctx.Err()
	}
	return o, found, err
}

func (s *preparationRecoveryAdmissionStore) UpdateProjectPreparation(ctx context.Context, o domain.ProjectPreparationOperation, revision int64) (domain.ProjectPreparationOperation, error) {
	if o.State == "failed" {
		s.finishCalls.Add(1)
		s.finishOnce.Do(func() { close(s.finishEntered) })
		select {
		case <-s.finishRelease:
		case <-ctx.Done():
			return o, ctx.Err()
		}
	}
	return s.ProjectPreparationStore.UpdateProjectPreparation(ctx, o, revision)
}

func TestProjectPreparationConcurrentRecoveryKeepsAdmissionAndReplayExclusive(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "different keys", true: "same key"}[sameKey], func(t *testing.T) {
			fixture := newPreparationRecoveryFixture(t)
			gate := &preparationRecoveryAdmissionStore{ProjectPreparationStore: fixture.f.st, readEntered: make(chan struct{}, 2), readRelease: make(chan struct{}), finishEntered: make(chan struct{}), finishRelease: make(chan struct{})}
			fixture.s.store = gate
			var readOnce, finishOnce sync.Once
			t.Cleanup(func() {
				readOnce.Do(func() { close(gate.readRelease) })
				finishOnce.Do(func() { close(gate.finishRelease) })
			})
			requests := []PrepareProjectRequest{fixture.request, fixture.request}
			if !sameKey {
				requests[1] = fixture.oldRequest
				requests[1].OperationKey = "new-web-preparation"
			}
			type outcome struct {
				view ProjectPreparationView
				err  error
			}
			results := make(chan outcome, 2)
			for _, request := range requests {
				go func() {
					view, err := fixture.s.Prepare(t.Context(), request)
					results <- outcome{view, err}
				}()
			}
			for range requests {
				select {
				case <-gate.readEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("concurrent requests did not observe the abandoned operation")
				}
			}
			readOnce.Do(func() { close(gate.readRelease) })
			accepted, conflicts := 0, 0
			var id string
			for range requests {
				select {
				case result := <-results:
					if result.err == nil {
						accepted++
						if result.view.Latest == nil || sameKey && id != "" && id != result.view.Latest.ID {
							t.Fatalf("replay returned another operation: %+v", result.view)
						}
						id = result.view.Latest.ID
					} else if apperror.CodeOf(result.err) == apperror.CodeConflict {
						conflicts++
					} else {
						t.Fatal(result.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("concurrent preparation admission stalled")
				}
			}
			if sameKey && (accepted != 2 || conflicts != 0) || !sameKey && (accepted != 1 || conflicts != 1) {
				t.Fatalf("admission accepted=%d conflicts=%d", accepted, conflicts)
			}
			select {
			case <-gate.finishEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("admitted worker did not finish")
			}
			active, found, err := fixture.f.st.ActiveProjectPreparation(t.Context(), fixture.old.RunID)
			if err != nil || !found || active.ID != id || gate.finishCalls.Load() != 1 {
				t.Fatalf("admission duplicated its worker: %+v found=%t calls=%d err=%v", active, found, gate.finishCalls.Load(), err)
			}
			finishOnce.Do(func() { close(gate.finishRelease) })
			fixture.s.Close()
			if gate.finishCalls.Load() != 1 {
				t.Fatalf("replay started %d workers", gate.finishCalls.Load())
			}
		})
	}
}
