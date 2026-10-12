package application_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestTaskConfigurationCreationReplayRestartAndSuccessor(t *testing.T) {
	database := filepath.Join(t.TempDir(), "config.db")
	state := openRunCreationStore(t, database)
	workspace := saveRunCreationWorkspace(t, state)
	write := func(content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(workspace.RootPath, ".prayu"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace.RootPath, ".prayu", "config.yaml"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("protocol: project_config.v1\nallowed_profiles: [learn, review]\nread_only: true\nbudget:\n  max_turns: 12\n  max_tool_calls: 8\nexclude_paths: [secret-fixture]\nskill_suggestions: [fixture@1.0.0]\n")
	turns, tools, tokens, timeout, cost := 50, int64(30), int64(123456), int64(600), 1.234567
	settings := &domain.TaskBudgetSettings{MaxTurns: &turns, MaxToolCalls: &tools, MaxTokens: &tokens, TimeoutSeconds: &timeout, MaxCostUSD: &cost}
	preview, err := application.NewTaskConfigurationService(state).Preview(t.Context(), application.TaskConfigurationRequest{WorkspaceID: workspace.ID, Profile: "review", Budget: settings})
	if err != nil || len(preview.Rejections) != 0 || preview.ProjectDisposition != "applied" || preview.Budget.MaxTurns != 12 || preview.Budget.MaxToolCalls != 8 || preview.Sources[0].Source != "project" || preview.Sources[1].Source != "operator" || preview.Fingerprint == "" {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	raw, _ := json.Marshal(preview)
	if bytes.Contains(raw, []byte(workspace.RootPath)) || bytes.Contains(raw, []byte("secret-fixture")) || preview.CapabilityGrant {
		t.Fatalf("unsafe projection %s", raw)
	}
	if preview.Project == nil || len(preview.Project.SkillSuggestions) != 1 || preview.Project.SkillSuggestions[0] != "fixture@1.0.0" {
		t.Fatal("exact Skill identity was not projected")
	}
	request := application.ControlledRunCreationRequest{Version: domain.RunCreationProtocolVersion, Goal: "review scoped files", WorkspaceID: workspace.ID, Profile: "review", Budget: settings, OperationKey: "config-create-replay-0001", RequestedBy: "test_operator"}
	created, err := application.NewControlledRunCreationService(state).Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := application.PinnedTaskConfiguration(created.Run, workspace.ID, created.Mission.Profile)
	if err != nil || pinned.Fingerprint != preview.Fingerprint || created.Run.Config.CreationBudget().MaxTurns != turns {
		t.Fatalf("pinned=%#v err=%v", pinned, err)
	}
	// Even an invalid later file must not be read during replay or succession.
	write("api_key: sk-untrusted-project-secret\n")
	replayed, err := application.NewControlledRunCreationService(state).Create(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Run.ID != created.Run.ID || replayed.Run.Budget != created.Run.Budget {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	changed := request
	newTurns := turns - 1
	changed.Budget = &domain.TaskBudgetSettings{MaxTurns: &newTurns}
	if _, err := application.NewControlledRunCreationService(state).Create(t.Context(), changed); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed budget error=%v", err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openRunCreationStore(t, database)
	replayed, err = application.NewControlledRunCreationService(reopened).Create(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Run.Budget != created.Run.Budget {
		t.Fatalf("restart replay=%#v err=%v", replayed, err)
	}
	if _, err := application.NewRunService(reopened).Cancel(t.Context(), created.Run.ID); err != nil {
		t.Fatal(err)
	}
	thread, err := reopened.GetThreadByRun(t.Context(), created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	successor, err := application.NewThreadService(reopened).Submit(t.Context(), application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: "continue review", OperationKey: "config-successor-0001", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if !successor.SuccessorCreated || successor.Run.Budget != created.Run.Budget || successor.Run.Config.ProjectConfigFingerprint != created.Run.Config.ProjectConfigFingerprint || *successor.Run.Config.RequestedBudget != *created.Run.Config.RequestedBudget {
		t.Fatalf("successor lost pinned configuration: budget=%#v fingerprint=%s", successor.Run.Budget, successor.Run.Config.ProjectConfigFingerprint)
	}
	successorView, err := application.PinnedTaskConfiguration(successor.Run, workspace.ID, created.Mission.Profile)
	if err != nil || successorView.Fingerprint != pinned.Fingerprint {
		t.Fatalf("successor view=%#v err=%v", successorView, err)
	}
	permission, err := reopened.GetRunExecutionPermission(t.Context(), successor.Run.ID)
	if err != nil || permission.CapabilityGrant || permission.ExecutionAuthorized || permission.OperatorConfirmed {
		t.Fatalf("successor authority=%#v err=%v", permission, err)
	}
}

func TestTaskConfigurationRejectsWholeCreationAndDoesNotLeakDecodeErrors(t *testing.T) {
	state := openRunCreationStore(t, filepath.Join(t.TempDir(), "reject.db"))
	workspace := saveRunCreationWorkspace(t, state)
	if err := os.MkdirAll(filepath.Join(workspace.RootPath, ".prayu"), 0700); err != nil {
		t.Fatal(err)
	}
	for index, content := range []string{
		"protocol: project_config.v1\nbudget: {max_turns: 100}\n",
		"protocol: project_config.v1\nread_only: true\n",
		"protocol: project_config.v1\nallowed_profiles: [review]\n",
		"protocol: project_config.v1\ntest_command_id: arbitrary_shell_command\n",
		"protocol: project_config.v1\napi_key: sk-dont-project-this-secret\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace.RootPath, ".prayu", "config.yaml"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		view, err := application.NewTaskConfigurationService(state).Preview(t.Context(), application.TaskConfigurationRequest{WorkspaceID: workspace.ID})
		if err != nil || view.ProjectDisposition != "rejected" || len(view.Rejections) == 0 || view.Fingerprint != "" || view.Project != nil {
			t.Fatalf("case %d projection=%#v err=%v", index, view, err)
		}
		raw, _ := json.Marshal(view)
		if strings.Contains(string(raw), "sk-dont") || strings.Contains(string(raw), workspace.RootPath) {
			t.Fatalf("decode error leaked %s", raw)
		}
		_, err = application.NewControlledRunCreationService(state).Create(t.Context(), application.ControlledRunCreationRequest{Version: domain.RunCreationProtocolVersion, Goal: "must reject", WorkspaceID: workspace.ID, OperationKey: "config-reject-create-0001"})
		if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || strings.Contains(err.Error(), "sk-dont") {
			t.Fatalf("creation rejection=%v", err)
		}
	}
	runs, err := state.ListRuns(t.Context(), domain.RunFilter{Limit: 100})
	if err != nil || len(runs) != 0 {
		t.Fatalf("rejected configuration created Run: %v %v", runs, err)
	}
}

func TestTaskConfigurationLegacySnapshotPreservesUnboundedAndLargerLimits(t *testing.T) {
	budget := domain.Budget{MaxTurns: 20_000, MaxTokens: 2_000_000_000, MaxToolCalls: 0, MaxCostUSD: 200_000, TimeoutSeconds: 1_000_000}
	if err := budget.Validate(); err != nil {
		t.Fatal(err)
	}
	view, err := application.PinnedTaskConfiguration(domain.Run{Budget: budget}, "workspace-legacy", domain.ProfileCode)
	if err != nil || view.Budget != budget || view.RequestedBudget != budget {
		t.Fatalf("legacy snapshot=%#v err=%v", view, err)
	}
	for _, source := range view.Sources {
		if source.Source != "snapshot" {
			t.Fatalf("fabricated operator source=%#v", source)
		}
	}
	raw, err := json.Marshal(view)
	if err != nil || bytes.Contains(raw, []byte(`"max_tool_calls"`)) {
		t.Fatalf("legacy zero tool dimension must remain omitted: %s err=%v", raw, err)
	}
}
