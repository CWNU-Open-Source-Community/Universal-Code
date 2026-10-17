package application_test

import (
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestProjectHooksTrustPersistsAcrossTasksAndRejectsChangedDefinition(t *testing.T) {
	database := filepath.Join(t.TempDir(), "hooks.db")
	state := openRunCreationStore(t, database)
	workspace := saveRunCreationWorkspace(t, state)
	path := filepath.Join(workspace.RootPath, ".prayu", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(command string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("protocol: project_config.v1\nformat_command_id: format.go\ntest_command_id: "+command+"\nhooks:\n  after_edit_format: true\n  before_finish_test: true\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("test.go")
	create := func(key string, turns int) application.ControlledRunCreationResult {
		t.Helper()
		result, err := application.NewControlledRunCreationService(state).Create(t.Context(), application.ControlledRunCreationRequest{Version: domain.RunCreationProtocolVersion, Goal: "project hooks acceptance", WorkspaceID: workspace.ID, Profile: "code", Budget: &domain.TaskBudgetSettings{MaxTurns: &turns}, OperationKey: key, RequestedBy: "test_operator"})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := create("project-hooks-first-0001", 20)
	thread := domain.InitialThreadID(first.Run.ID)
	service := application.NewProjectHooksService(state)
	view, err := service.Get(t.Context(), thread)
	if err != nil || view.State != "needs_review" || !view.MatchesTask || len(view.Hooks) != 2 {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	if _, err = service.AuthorizeProjectHooks(t.Context(), first.Run.ID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("unreviewed: %v", err)
	}
	request := application.ProjectHooksReviewRequest{Version: application.ProjectHooksVersion, ExpectedFingerprint: view.Fingerprint, ExpectedRevision: view.Revision}
	trusted, err := service.Review(t.Context(), thread, "trust", request)
	if err != nil || !trusted.Trusted || trusted.Revision != 1 {
		t.Fatalf("trust=%+v err=%v", trusted, err)
	}
	if replay, err := service.Review(t.Context(), thread, "trust", request); err != nil || replay.Revision != 1 {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	second := create("project-hooks-second-0001", 30)
	if selected, err := service.AuthorizeProjectHooks(t.Context(), second.Run.ID); err != nil || !selected.AfterEditFormat || !selected.BeforeFinishTest {
		t.Fatalf("new task=%+v err=%v", selected, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state = openRunCreationStore(t, database)
	service = application.NewProjectHooksService(state)
	if _, err := service.AuthorizeProjectHooks(t.Context(), second.Run.ID); err != nil {
		t.Fatalf("restart lost trust: %v", err)
	}
	write("test.npm")
	changed, err := service.Get(t.Context(), thread)
	if err != nil || changed.State != "changed" || changed.Trusted || changed.MatchesTask {
		t.Fatalf("changed=%+v err=%v", changed, err)
	}
	if _, err := service.Review(t.Context(), thread, "trust", request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale definition trusted: %v", err)
	}
	if _, err := service.AuthorizeProjectHooks(t.Context(), first.Run.ID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("changed definition ran: %v", err)
	}
	request.ExpectedFingerprint, request.ExpectedRevision = changed.Fingerprint, changed.Revision
	if _, err := service.Review(t.Context(), thread, "trust", request); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthorizeProjectHooks(t.Context(), first.Run.ID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("old task silently adopted new definition: %v", err)
	}
	third := create("project-hooks-third-0001", 40)
	thirdThread := domain.InitialThreadID(third.Run.ID)
	view, err = service.Get(t.Context(), thirdThread)
	if err != nil || !view.Trusted || !view.MatchesTask {
		t.Fatalf("new definition=%+v err=%v", view, err)
	}
	request.ExpectedRevision = view.Revision
	if _, err := service.Review(t.Context(), thirdThread, "disable", request); err != nil {
		t.Fatal(err)
	}
	if selected, err := service.AuthorizeProjectHooks(t.Context(), third.Run.ID); err != nil || selected.Fingerprint != "" {
		t.Fatalf("disabled=%+v err=%v", selected, err)
	}
	if _, err := service.Review(t.Context(), thirdThread, "trust", request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale consent reenabled hooks: %v", err)
	}
	if err := os.WriteFile(path, []byte("protocol: project_config.v1\nhooks: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if selected, err := service.AuthorizeProjectHooks(t.Context(), third.Run.ID); err != nil || selected.Fingerprint != "" {
		t.Fatalf("disabled hook still blocks on an edited config: %+v %v", selected, err)
	}
}

func TestProjectHooksReviewRejectsReplacedWorkspace(t *testing.T) {
	state := openRunCreationStore(t, filepath.Join(t.TempDir(), "hooks.db"))
	workspace := saveRunCreationWorkspace(t, state)
	writeConfig := func() {
		t.Helper()
		dir := filepath.Join(workspace.RootPath, ".prayu")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("protocol: project_config.v1\ntest_command_id: test.go\nhooks:\n  before_finish_test: true\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig()
	created, err := application.NewControlledRunCreationService(state).Create(t.Context(), application.ControlledRunCreationRequest{
		Version: domain.RunCreationProtocolVersion, Goal: "review exact workspace", WorkspaceID: workspace.ID, Profile: "code", OperationKey: "project-hooks-replaced-root-0001", RequestedBy: "test_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.InitialThreadID(created.Run.ID)
	service := application.NewProjectHooksService(state)
	view, err := service.Get(t.Context(), thread)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(workspace.RootPath, filepath.Join(t.TempDir(), "original")); err != nil {
		t.Fatal(err)
	}
	writeConfig()
	request := application.ProjectHooksReviewRequest{Version: application.ProjectHooksVersion, ExpectedFingerprint: view.Fingerprint, ExpectedRevision: view.Revision}
	if _, err := service.Review(t.Context(), thread, "trust", request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale review trusted a different workspace: %v", err)
	}
	current, err := service.Get(t.Context(), thread)
	if err != nil || current.Trusted || current.Fingerprint == view.Fingerprint || current.Revision != 0 {
		t.Fatalf("replacement trust=%+v err=%v", current, err)
	}
}
