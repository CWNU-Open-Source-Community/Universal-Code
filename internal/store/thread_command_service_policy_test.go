package store

import (
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestThreadCommandServicePolicyUpgradeDefaultsToAuto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "before-service-policy.db")
	old := openHistoricalTestDatabase(t, path, 211)
	_, run, err := application.NewRunService(old).Create(t.Context(), application.CreateRunRequest{
		Goal: "preserve existing task", Profile: "code", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		_ = old.Close()
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	policy, err := upgraded.GetThreadCommandServicePolicy(t.Context(), domain.InitialThreadID(run.ID))
	if err != nil || policy.Policy != domain.CommandServicePolicyAuto || policy.Revision != 1 {
		t.Fatalf("upgrade default: %+v %v", policy, err)
	}
	if _, err := upgraded.SaveThreadCommandServicePolicy(t.Context(), policy.ThreadID, policy.Revision, domain.CommandServicePolicyExclusive); err != nil {
		t.Fatal(err)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
}

func TestThreadCommandServicePolicyPersistsAndUsesCurrentRevision(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "service-policy.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	create := func() (domain.Run, domain.Thread) {
		t.Helper()
		_, run, err := application.NewRunService(st).Create(ctx, application.CreateRunRequest{
			Goal: "persist development service preference", Profile: "code", Budget: domain.Budget{MaxTurns: 2}})
		if err != nil {
			t.Fatal(err)
		}
		thread, err := st.GetThreadByRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return run, thread
	}
	run, thread := create()
	otherRun, otherThread := create()
	initial, err := st.GetThreadCommandServicePolicy(ctx, thread.ID)
	if err != nil || initial.Version != domain.ThreadCommandServicePolicyVersion || initial.Policy != domain.CommandServicePolicyAuto || initial.Revision != 1 {
		t.Fatalf("default preference: %+v %v", initial, err)
	}
	before, err := st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := st.SaveThreadCommandServicePolicy(ctx, thread.ID, 1, domain.CommandServicePolicyExclusive)
	if err != nil || saved.Policy != domain.CommandServicePolicyExclusive || saved.Revision != 2 {
		t.Fatalf("save: %+v %v", saved, err)
	}
	replay, err := st.SaveThreadCommandServicePolicy(ctx, thread.ID, 1, domain.CommandServicePolicyExclusive)
	if err != nil || replay != saved {
		t.Fatalf("retry changed preference: %+v %v", replay, err)
	}
	if _, err := st.SaveThreadCommandServicePolicy(ctx, thread.ID, 1, domain.CommandServicePolicyAuto); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale writer: %v", err)
	}
	if allowed, err := st.SharedCommandServicesAllowed(ctx, run.ID); err != nil || allowed {
		t.Fatalf("exclusive Run allowed shared service: %t %v", allowed, err)
	}
	if allowed, err := st.SharedCommandServicesAllowed(ctx, otherRun.ID); err != nil || !allowed {
		t.Fatalf("preference leaked to another Thread: %t %v", allowed, err)
	}
	other, err := st.GetThreadCommandServicePolicy(ctx, otherThread.ID)
	if err != nil || other.Policy != domain.CommandServicePolicyAuto || other.Revision != 1 {
		t.Fatalf("other Thread changed: %+v %v", other, err)
	}
	after, err := st.GetRunExecutionPermission(ctx, run.ID)
	if err != nil || after.ID != before.ID || after.Revision != before.Revision || after.Mode != before.Mode {
		t.Fatalf("preference changed execution permission: %+v %v", after, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := st.GetThreadCommandServicePolicy(ctx, thread.ID)
	if err != nil || reopened != saved {
		t.Fatalf("reopen lost preference: %+v %v", reopened, err)
	}
	auto, err := st.SaveThreadCommandServicePolicy(ctx, thread.ID, 2, domain.CommandServicePolicyAuto)
	if err != nil || auto.Revision != 3 {
		t.Fatalf("reenable: %+v %v", auto, err)
	}
	if allowed, err := st.SharedCommandServicesAllowed(ctx, run.ID); err != nil || !allowed {
		t.Fatalf("current preference ignored: %t %v", allowed, err)
	}
	if _, err := st.SaveThreadCommandServicePolicy(ctx, thread.ID, 1, domain.CommandServicePolicyAuto); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("old matching value accepted as retry: %v", err)
	}
	if _, err := st.TransitionThread(ctx, thread.ID, domain.ThreadArchive, thread.Version, "api_operator", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveThreadCommandServicePolicy(ctx, thread.ID, 3, domain.CommandServicePolicyExclusive); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("archived Thread accepted write: %v", err)
	}
}
