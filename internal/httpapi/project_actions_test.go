package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/runner"
)

type projectActionsControllerStub struct {
	request application.ProjectActionRequest
	starts  int
}

func (*projectActionsControllerStub) List(_ context.Context, runID string) (application.ProjectActionsView, error) {
	return application.ProjectActionsView{ProtocolVersion: application.ProjectActionsProtocolVersion, RunID: runID, Backend: "docker_standard_code", Actions: []application.ProjectActionView{{Slot: "test", CommandID: "test.npm", Configured: true, Available: true, TargetKind: "directory"}, {Slot: "format", CommandID: "format.go", Configured: true, Available: true, TargetKind: "go_file"}}}, nil
}
func (s *projectActionsControllerStub) Run(_ context.Context, request application.ProjectActionRequest) (application.ProjectActionResultView, error) {
	s.request, s.starts = request, s.starts+1
	return application.ProjectActionResultView{ProtocolVersion: application.ProjectActionsProtocolVersion, RunID: request.RunID, Slot: request.Slot, CommandID: "test.npm", Backend: "docker_standard_code", Job: application.ProjectActionJobView{ID: "project-action-fixture-job", State: runner.CommandRuntimeJobRunning, CanStop: true}}, nil
}
func (*projectActionsControllerStub) GetJob(_ context.Context, runID, jobID string) (application.ProjectActionResultView, error) {
	code := 1
	return application.ProjectActionResultView{ProtocolVersion: application.ProjectActionsProtocolVersion, RunID: runID, Slot: "test", CommandID: "test.npm", Backend: "docker_standard_code", Replayed: true, Job: application.ProjectActionJobView{ID: jobID, State: runner.CommandRuntimeJobFailed, ExitCode: &code, Stdout: "real saved output", Stderr: "test failed"}}, nil
}

func TestProjectActionsHTTPReadExecuteAndSavedFailure(t *testing.T) {
	f := newAPIFixture(t)
	controller := &projectActionsControllerStub{}
	f.api.projectActionsController, f.api.runExecutionEnabled = controller, true
	path := "/api/v1/runs/" + f.run.ID + "/project-actions"
	var available ProjectActionsView
	decodeData(t, f.get(t, path), &available)
	if len(available.Actions) != 2 || !available.Actions[0].Available || available.CapabilityGrant {
		t.Fatalf("available actions %+v", available)
	}
	var started ProjectActionResultView
	decodeDataStatus(t, performControlPathRequest(t, f.api, path+"/test", "project-http-once-0001", strings.NewReader(`{"version":"project-actions.v1","target":"web","confirm_execution":true}`)), http.StatusAccepted, &started)
	if controller.request.Target != "web" || controller.request.Slot != "test" || !controller.request.ConfirmExecution || controller.request.OperationKey != "project-http-once-0001" || started.Job.ID == "" {
		t.Fatalf("request/receipt %+v %+v", controller.request, started)
	}
	var saved ProjectActionResultView
	decodeData(t, f.get(t, path+"/jobs/"+started.Job.ID), &saved)
	if saved.Job.State != runner.CommandRuntimeJobFailed || saved.Job.ExitCode == nil || *saved.Job.ExitCode != 1 || saved.Job.Stderr != "test failed" {
		t.Fatalf("failed Job lost real output: %+v", saved)
	}
	for _, body := range []string{`{"version":"project-actions.v1","target":"web","confirm_execution":true,"command":"shell text"}`, `{"version":"project-actions.v1","target":"web","target":"other","confirm_execution":true}`, `{"version":"wrong","target":"","confirm_execution":true}`} {
		assertAPIError(t, performControlPathRequest(t, f.api, path+"/test", "project-http-rejected", strings.NewReader(body)), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
	if controller.starts != 1 {
		t.Fatalf("invalid HTTP input executed %d actions", controller.starts)
	}
}
