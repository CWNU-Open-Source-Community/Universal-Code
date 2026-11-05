package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/githubreview"
)

type githubReviewControllerStub struct {
	configured application.GitHubReviewConfigureRequest
	fetched    application.GitHubReviewFetchRequest
	evidence   application.GitHubReviewEvidenceRequest
	reviewed   application.GitHubReviewWriteReviewRequest
	executed   application.GitHubReviewWriteExecuteRequest
	runID      string
	connection string
}

func (s *githubReviewControllerStub) Configure(_ context.Context,
	request application.GitHubReviewConfigureRequest,
) (application.GitHubReviewConfigureResult, error) {
	s.configured = request
	return application.GitHubReviewConfigureResult{
		ProtocolVersion: application.GitHubReviewAPIProtocolVersion,
		Connection:      githubreview.Connection{ID: "github-connection-openapi"}}, nil
}

func (s *githubReviewControllerStub) ListConnections(context.Context, bool) (
	[]application.GitHubReviewCredentialView, error,
) {
	return []application.GitHubReviewCredentialView{}, nil
}

func (s *githubReviewControllerStub) CredentialStatus(_ context.Context, id string) (
	application.GitHubReviewCredentialView, error,
) {
	s.connection = id
	return application.GitHubReviewCredentialView{
		ProtocolVersion: application.GitHubReviewAPIProtocolVersion,
		Connection:      githubreview.Connection{ID: id}}, nil
}

func (s *githubReviewControllerStub) BeginDeviceFlow(context.Context, string) (
	githubreview.DeviceAuthorization, error,
) {
	return githubreview.DeviceAuthorization{ProtocolVersion: githubreview.DeviceFlowProtocolVersion}, nil
}

func (s *githubReviewControllerStub) PollDeviceFlow(context.Context, string, string) (
	githubreview.DevicePollResult, error,
) {
	return githubreview.DevicePollResult{ProtocolVersion: githubreview.DeviceFlowProtocolVersion}, nil
}

func (s *githubReviewControllerStub) Disconnect(_ context.Context, id string) (
	application.GitHubReviewCredentialView, error,
) {
	return s.CredentialStatus(context.Background(), id)
}

func (s *githubReviewControllerStub) Qualify(context.Context, string, int64) (
	application.GitHubReviewQualificationResult, error,
) {
	return application.GitHubReviewQualificationResult{
		ProtocolVersion: application.GitHubReviewAPIProtocolVersion}, nil
}

func (s *githubReviewControllerStub) Fetch(_ context.Context,
	request application.GitHubReviewFetchRequest,
) (application.GitHubReviewFetchResult, error) {
	s.fetched = request
	return application.GitHubReviewFetchResult{
		ProtocolVersion: application.GitHubReviewAPIProtocolVersion}, nil
}

func (s *githubReviewControllerStub) BuildEvidence(_ context.Context,
	request application.GitHubReviewEvidenceRequest,
) (application.GitHubReviewEvidenceResult, error) {
	s.evidence = request
	return application.GitHubReviewEvidenceResult{
		ProtocolVersion: application.GitHubReviewAPIProtocolVersion}, nil
}

func (s *githubReviewControllerStub) ReviewWrite(_ context.Context,
	request application.GitHubReviewWriteReviewRequest,
) (application.GitHubReviewWriteReviewResult, error) {
	s.reviewed = request
	return application.GitHubReviewWriteReviewResult{
		ProtocolVersion: application.GitHubReviewAPIProtocolVersion}, nil
}

func (s *githubReviewControllerStub) ExecuteWrite(_ context.Context,
	request application.GitHubReviewWriteExecuteRequest,
) (application.GitHubReviewWriteExecuteResult, error) {
	s.executed = request
	return application.GitHubReviewWriteExecuteResult{
		ProtocolVersion: application.GitHubReviewAPIProtocolVersion}, nil
}

func (s *githubReviewControllerStub) Projection(_ context.Context, runID,
	connectionID string, _ int64, _ int,
) (application.GitHubReviewProjection, error) {
	s.runID, s.connection = runID, connectionID
	return application.GitHubReviewProjection{
		ProtocolVersion: application.GitHubReviewAPIProtocolVersion,
		RunID:           runID, Connection: githubreview.Connection{ID: connectionID},
		Snapshots: []githubreview.Snapshot{}, Evidence: []githubreview.EvidenceRecord{},
		Writes: []githubreview.WriteRecord{}}, nil
}

func TestGitHubReviewHTTPUsesReadAndControlAuthorities(t *testing.T) {
	fixture := newAPIFixture(t)
	controller := &githubReviewControllerStub{}
	fixture.api.githubReviewController = controller
	fixture.api.githubReviewControlEnabled = true

	read := fixture.get(t, "/api/v1/github-review/connections")
	if read.Code != http.StatusOK {
		t.Fatalf("read route status=%d body=%s", read.Code, read.Body.String())
	}
	denied := fixture.get(t, "/api/v1/runs/"+fixture.run.ID+
		"/github-review?connection_id=github-connection-openapi")
	if denied.Code != http.StatusOK || controller.runID != fixture.run.ID {
		t.Fatalf("projection status=%d body=%s", denied.Code, denied.Body.String())
	}
	response := performControlMethodPathRequest(t, fixture.api, http.MethodPost,
		"/api/v1/github-review/connections/github-connection-openapi/fetch",
		"github-review-fetch-test", strings.NewReader(`{"pull_request":118}`))
	if response.Code != http.StatusCreated || controller.fetched.PullRequest != 118 {
		t.Fatalf("fetch status=%d request=%#v body=%s", response.Code,
			controller.fetched, response.Body.String())
	}
}

func TestGitHubReviewHTTPRejectsNonBooleanFilter(t *testing.T) {
	fixture := newAPIFixture(t)
	fixture.api.githubReviewController = &githubReviewControllerStub{}
	fixture.api.githubReviewControlEnabled = true
	response := fixture.get(t, "/api/v1/github-review/connections?enabled_only=maybe")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid filter status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGitHubReviewHTTPConfigurationRequiresEveryAuthorityGate(t *testing.T) {
	fixture := newAPIFixture(t)
	valid := Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true},
		ApprovalControlEnabled: true,
		ApprovalController:     application.NewApprovalControlService(fixture.store, nil, nil),
		GitHubReviewControlEnabled: true,
		GitHubReviewController:     &githubReviewControllerStub{}}
	if _, err := New(fixture.store, valid); err != nil {
		t.Fatalf("valid GitHub review configuration failed: %v", err)
	}
	tests := map[string]func(*Config){
		"permission control": func(config *Config) {
			config.ExecutionPermissionControlEnabled = false
		},
		"operator approval": func(config *Config) {
			config.ExecutionPermissionCapabilities.OperatorApprovalEnabled = false
		},
		"approval control": func(config *Config) {
			config.ApprovalControlEnabled = false
		},
		"controller": func(config *Config) {
			config.GitHubReviewController = nil
		},
	}
	for name, disable := range tests {
		t.Run(name, func(t *testing.T) {
			config := valid
			disable(&config)
			if _, err := New(fixture.store, config); err == nil ||
				!strings.Contains(err.Error(), "GitHub review") {
				t.Fatalf("missing %s gate was accepted: %v", name, err)
			}
		})
	}
}
