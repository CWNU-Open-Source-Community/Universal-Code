package application_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/store"
)

func TestRunWebAccessPersistsReplaysNarrowsAndInherits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "web-access.db")
	state, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(state)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "choose web access", WebAccessMode: "full", NetworkMode: "allowlist", AllowedTargets: []string{"command.example.org"}, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	fence, _ := authority.IssueRunAuthorizationFence(run.ID)
	service := application.NewRunWebAccessService(state).WithRuntimeAuthority(authority)
	request := application.ChangeRunWebAccessRequest{Version: application.RunWebAccessControlProtocolVersion, RunID: run.ID, ExpectedModeRevision: 1, WebAccessMode: "rules", WebAllowedTargets: []string{"HTTPS://Docs.Example.org/", "api.example.org", "docs.example.org"}, OperationKey: "web-access-change-0001", RequestedBy: "operator"}
	result, err := service.Change(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed || result.Mode.Revision != 2 || result.Mode.Scope.WebAccessMode != domain.WebAccessRules || !reflect.DeepEqual(result.Mode.Scope.WebAllowedTargets, []string{"api.example.org", "docs.example.org"}) || result.Mode.Scope.NetworkMode != "allowlist" || !reflect.DeepEqual(result.Mode.Scope.AllowedTargets, []string{"command.example.org"}) || authority.AllowsRunAuthorizationFence(run.ID, fence) {
		t.Fatalf("mode or independent authority drifted: %+v", result)
	}
	replayed, err := service.Change(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Mode.ID != result.Mode.ID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	changed := request
	changed.WebAccessMode = "full"
	changed.WebAllowedTargets = nil
	if _, err := service.Change(ctx, changed); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("different-key-intent err=%v", err)
	}
	changed.OperationKey = "web-access-stale-0001"
	if _, err := service.Change(ctx, changed); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale revision err=%v", err)
	}
	request.ExpectedModeRevision = 2
	request.WebAccessMode = "disabled"
	request.WebAllowedTargets = nil
	request.OperationKey = "web-access-disable-0001"
	if _, err := service.Change(ctx, request); err != nil {
		t.Fatal(err)
	}
	items, err := runs.Events(ctx, run.ID)
	if err != nil || countEventType(items, events.RunWebAccessChangedEvent) != 2 {
		t.Fatalf("events=%v err=%v", items, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	mode, err := state.GetRunMode(ctx, run.ID)
	if err != nil || mode.Scope.WebAccessMode != domain.WebAccessDisabled || mode.Revision != 3 {
		t.Fatalf("reopened mode=%+v err=%v", mode, err)
	}
	if replayed, err := application.NewRunWebAccessService(state).Change(ctx, request); err != nil || !replayed.Replayed || replayed.Mode.ID != mode.ID {
		t.Fatalf("reopened replay=%+v err=%v", replayed, err)
	}
	runs = application.NewRunService(state)
	if _, err := runs.Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	thread, err := state.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := application.NewThreadService(state).Submit(ctx, application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: "continue with disabled web", OperationKey: "web-access-successor-0001", RequestedBy: "operator"})
	if err != nil || !continued.SuccessorCreated {
		t.Fatalf("successor=%+v err=%v", continued, err)
	}
	inherited, err := state.GetRunMode(ctx, continued.Run.ID)
	if err != nil || inherited.Scope.WebAccessMode != domain.WebAccessDisabled {
		t.Fatalf("inherited=%+v err=%v", inherited, err)
	}
}

func TestRunWebAccessRejectsLeaseRunningAndBroadRules(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "web-access-boundaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	runs := application.NewRunService(state)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "restrict web mode", WebAccessMode: "rules", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	request := application.ChangeRunWebAccessRequest{Version: application.RunWebAccessControlProtocolVersion, RunID: run.ID, ExpectedModeRevision: 1, WebAccessMode: "rules", OperationKey: "web-access-boundaries-0001"}
	service := application.NewRunWebAccessService(state)
	for _, target := range []string{"public_https", "*.example.org", "localhost", "127.0.0.1", "metadata.google.internal", "https://example.org/path"} {
		request.WebAllowedTargets = []string{target}
		if _, err := service.Change(ctx, request); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
			t.Fatalf("target=%s err=%v", target, err)
		}
	}
	request.WebAllowedTargets = nil
	request.WebAccessMode = "full"
	lease, err := state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "web-access-test", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Change(ctx, request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("leased mode err=%v", err)
	}
	if _, _, err := state.ReleaseRunExecutionLease(ctx, lease.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Change(ctx, request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("running mode err=%v", err)
	}
	if _, err := runs.Pause(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Change(ctx, request); err != nil || result.Mode.Scope.WebAccessMode != domain.WebAccessFull {
		t.Fatalf("paused change=%+v err=%v", result, err)
	}
}

type initiallyMissingWebAccessOperation struct {
	application.RunWebAccessStore
	missed bool
}

func (s *initiallyMissingWebAccessOperation) GetRunWebAccessOperation(ctx context.Context, key string) (domain.RunWebAccessOperation, bool, error) {
	if !s.missed {
		s.missed = true
		return domain.RunWebAccessOperation{}, false, nil
	}
	return s.RunWebAccessStore.GetRunWebAccessOperation(ctx, key)
}

func TestRunWebAccessOriginalKeyConvergesAfterConcurrentRevisionRead(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "web-access-concurrent-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, run, err := application.NewRunService(state).Create(t.Context(), application.CreateRunRequest{Goal: "converge web selection", WebAccessMode: "rules", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	request := application.ChangeRunWebAccessRequest{Version: application.RunWebAccessControlProtocolVersion, RunID: run.ID, ExpectedModeRevision: 1, WebAccessMode: "full", OperationKey: "web-access-concurrent-key"}
	committed, err := application.NewRunWebAccessService(state).Change(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a retry whose first lookup overlapped the original commit, then
	// observed the new mode revision. Its original key must still replay.
	delayed := &initiallyMissingWebAccessOperation{RunWebAccessStore: state}
	replay, err := application.NewRunWebAccessService(delayed).Change(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Mode.ID != committed.Mode.ID {
		t.Fatalf("concurrent replay=%+v err=%v", replay, err)
	}
}
