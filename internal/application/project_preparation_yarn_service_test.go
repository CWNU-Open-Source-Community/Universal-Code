package application

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

type yarnPreparationReadiness struct{ sandbox.ReadinessProbe }

func (yarnPreparationReadiness) YarnPreparationAvailable(context.Context, string) (bool, error) {
	return true, nil
}

// Exercise the real preparation/action/approval ledgers with the existing Docker
// transport double. Native Yarn extraction and script behavior are tested separately.
func TestYarnPreparationAndDetectedTestReceipts(t *testing.T) {
	f, service, _ := pnpmPreparationFixture(t)
	root := filepath.Join(f.owned.Path, "yarn")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"package.json": `{"private":true,"packageManager":"yarn@1.22.22","scripts":{"test":"node --test"}}`,
		"yarn.lock":    "# yarn lockfile v1\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := f.drydockFixture.service.Checkpoint(t.Context(), DrydockCheckpointRequest{RunID: f.turn.Run.ID, ExpectedGeneration: f.owned.Generation, OperationKey: "yarn-fixture-manifests", RequestedBy: "operator", Title: "Yarn project", ConfirmObservedChanges: true})
	if err != nil {
		t.Fatal(err)
	}
	f.owned = checkpoint.Workspace
	executor := f.service.sandbox.(*DockerSandboxCommandRuntimeExecutor)
	view, err := service.Inspect(t.Context(), f.turn.Run.ID, "yarn")
	if err != nil || view.Available {
		t.Fatal("older image promised Yarn", view, err)
	}
	executor.service.docker.readiness = yarnPreparationReadiness{executor.service.docker.readiness}
	view, err = service.Inspect(t.Context(), f.turn.Run.ID, "yarn")
	if err != nil || !view.Available {
		t.Fatal(view, err)
	}
	view, err = service.Prepare(t.Context(), PrepareProjectRequest{RunID: f.turn.Run.ID, Target: "yarn", ExpectedFingerprint: view.Plan.Fingerprint, AllowedSources: []string{}, OperationKey: "prepare-yarn-offline", RequestedBy: "operator", ConfirmExecution: true})
	if err != nil || view.Latest == nil {
		t.Fatal(view, err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		result, err := service.GetOperation(t.Context(), f.turn.Run.ID, view.Latest.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.State == "ready" {
			break
		}
		if time.Now().After(deadline) || result.State == "failed" {
			t.Fatal("Yarn preparation failed", result)
		}
		time.Sleep(20 * time.Millisecond)
	}
	actions, err := service.jobs.List(t.Context(), f.turn.Run.ID, "yarn")
	if err != nil || !actions.Actions[0].Available || actions.Actions[0].CommandID != "test.yarn" {
		t.Fatal(actions, err)
	}
	request := ProjectActionRequest{RunID: f.turn.Run.ID, Slot: "test", Target: "yarn", OperationKey: "test-yarn-offline", RequestedBy: "operator", ConfirmExecution: true}
	started, err := service.jobs.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	completed := waitProjectAction(t, service.jobs, f.turn.Run.ID, started.Job.ID, time.Minute)
	if completed.CommandID != "test.yarn" || completed.Job.State != runner.CommandRuntimeJobCompleted {
		t.Fatal(completed)
	}
	replayed, err := service.jobs.Run(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Job.ID != started.Job.ID {
		t.Fatal(replayed, err)
	}
	f.assertDispatchCounts(t, 2, 2)
}
