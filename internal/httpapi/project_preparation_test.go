package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
)

type preparationControllerStub struct {
	starts, cancels int
	request         application.PrepareProjectRequest
}

func (*preparationControllerStub) Inspect(_ context.Context, runID, target string) (application.ProjectPreparationView, error) {
	return application.ProjectPreparationView{ProtocolVersion: application.ProjectPreparationProtocolVersion, RunID: runID, Target: target, Available: true}, nil
}
func (s *preparationControllerStub) Prepare(_ context.Context, r application.PrepareProjectRequest) (application.ProjectPreparationView, error) {
	s.starts++
	s.request = r
	return application.ProjectPreparationView{ProtocolVersion: application.ProjectPreparationProtocolVersion, RunID: r.RunID, Target: r.Target, Latest: &application.ProjectPreparationOperationView{ID: "prep-saved", State: "pending"}}, nil
}
func (*preparationControllerStub) GetOperation(_ context.Context, runID, id string) (application.ProjectPreparationOperationView, error) {
	return application.ProjectPreparationOperationView{ProtocolVersion: application.ProjectPreparationProtocolVersion, RunID: runID, ID: id, State: "installing", CanCancel: true}, nil
}
func (s *preparationControllerStub) Cancel(_ context.Context, runID, id string) (application.ProjectPreparationOperationView, error) {
	s.cancels++
	return application.ProjectPreparationOperationView{ProtocolVersion: application.ProjectPreparationProtocolVersion, RunID: runID, ID: id, State: "cancelled"}, nil
}

func TestProjectPreparationHTTPRequiresExplicitControlAndPreservesSourceChoice(t *testing.T) {
	f := newAPIFixture(t)
	c := &preparationControllerStub{}
	f.api.projectPreparationController = c
	f.api.runExecutionEnabled = true
	path := "/api/v1/runs/" + f.run.ID + "/project-preparation"
	var view application.ProjectPreparationView
	decodeData(t, f.get(t, path+"?target=web"), &view)
	if view.Target != "web" || !view.Available {
		t.Fatalf("inspection %+v", view)
	}
	body := `{"version":"project-preparation.v1","target":"web","expected_fingerprint":"` + strings.Repeat("a", 64) + `","allowed_sources":["https://registry.npmjs.org"],"confirm_execution":true,"run_install_scripts":false}`
	decodeDataStatus(t, performControlPathRequest(t, f.api, path, "preparation-request-1", strings.NewReader(body)), http.StatusAccepted, &view)
	if c.starts != 1 || c.request.RunID != f.run.ID || c.request.RequestedBy != "web_operator" || c.request.OperationKey != "preparation-request-1" || c.request.RunInstallScripts || len(c.request.AllowedSources) != 1 {
		t.Fatalf("operator intent %+v", c.request)
	}
	for _, rejected := range []string{strings.Replace(body, `"run_install_scripts":false`, `"run_install_scripts":false,"shell":"npm install"`, 1), strings.Replace(body, `"target":"web"`, `"target":"web","target":"elsewhere"`, 1), strings.Replace(body, "project-preparation.v1", "unknown.v1", 1)} {
		assertAPIError(t, performControlPathRequest(t, f.api, path, "preparation-reject-1", strings.NewReader(rejected)), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
	assertAPIError(t, f.get(t, path+"?target=a&target=b"), http.StatusBadRequest, "INVALID_ARGUMENT")
	assertAPIError(t, f.get(t, path+"?shell=whoami"), http.StatusBadRequest, "INVALID_ARGUMENT")
	if c.starts != 1 {
		t.Fatal("invalid input reached preparation")
	}
	var operation application.ProjectPreparationOperationView
	decodeData(t, f.get(t, path+"/operations/prep-saved"), &operation)
	if operation.ID != "prep-saved" || !operation.CanCancel {
		t.Fatal(operation)
	}
	decodeData(t, performControlPathRequest(t, f.api, path+"/operations/prep-saved/cancel", "cancel-preparation", strings.NewReader(`{"version":"project-preparation.v1"}`)), &operation)
	if c.cancels != 1 || operation.State != "cancelled" {
		t.Fatal(operation)
	}
	f.api.runExecutionEnabled = false
	decodeData(t, f.get(t, path), &view)
	if view.Available {
		t.Fatal("read-only API advertised execution")
	}
	assertAPIError(t, performControlPathRequest(t, f.api, path, "prep-disabled", strings.NewReader(body)), http.StatusNotFound, "NOT_FOUND")
}
