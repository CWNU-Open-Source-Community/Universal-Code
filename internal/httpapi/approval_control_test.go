package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

type cancelAfterWebApprovalController struct {
	cancel context.CancelFunc
}

func (c cancelAfterWebApprovalController) Decide(_ context.Context,
	request application.DecideApprovalControlRequest,
) (application.DecideApprovalControlResult, error) {
	c.cancel()
	return application.DecideApprovalControlResult{Approval: approval.Record{
		ID: request.ApprovalID, ProposalID: "web-fetch-authorization-detached",
		RunID: request.RunID, ToolName: "web_fetch", Status: approval.StatusApproved},
		Action: request.Action}, nil
}

type detachedWebFetchResumeController struct {
	runExecutionControllerFake
	called chan error
}

func (c detachedWebFetchResumeController) Execute(context.Context,
	application.ExecuteRunHandoffRequest,
) (application.ExecuteRunHandoffResult, error) {
	return application.ExecuteRunHandoffResult{}, nil
}

func (c detachedWebFetchResumeController) ResumeWebFetchAuthorization(ctx context.Context,
	_, _ string,
) (application.LifecycleResult, bool, error) {
	c.called <- ctx.Err()
	return application.LifecycleResult{Turn: 1}, false, nil
}

func TestWebFetchApprovalQueuesDurableContinuationAfterClientCancellation(t *testing.T) {
	requestContext, cancelRequest := context.WithCancel(context.Background())
	resumeCalled := make(chan error, 1)
	api := &API{controlEnabled: true, approvalControlEnabled: true,
		controlTokenHash:                      sha256.Sum256([]byte(testControlToken)),
		approvalController:                    cancelAfterWebApprovalController{cancel: cancelRequest},
		runExecutionController:                detachedWebFetchResumeController{called: resumeCalled},
		webFetchAuthorizationSchedulerEnabled: true,
		appVersion:                            "approval-detached-test"}
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/run-detached/approvals/approval-detached/decision",
		strings.NewReader(`{"version":"approval_control.v1","action":"approve_once"}`)).
		WithContext(requestContext)
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "approval-detached-operation-0001")
	response := httptest.NewRecorder()
	api.serveApprovalDecisionControl(response, request, "request-detached",
		"run-detached", "approval-detached")
	if response.Code != http.StatusAccepted ||
		!strings.Contains(response.Body.String(), `"retry_scheduled":true`) {
		t.Fatalf("detached approval status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-resumeCalled:
		t.Fatal("HTTP handler started an untracked continuation worker")
	case <-time.After(25 * time.Millisecond):
	}
	// Replaying the same decision only leaves the same durable queue item for
	// the single process-owned reconciler; it must not fan out another worker.
	replayRequest := httptest.NewRequest(http.MethodPost,
		request.URL.String(),
		strings.NewReader(`{"version":"approval_control.v1","action":"approve_once"}`))
	replayRequest.Header = request.Header.Clone()
	replayResponse := httptest.NewRecorder()
	api.serveApprovalDecisionControl(replayResponse, replayRequest, "request-detached-replay",
		"run-detached", "approval-detached")
	if replayResponse.Code != http.StatusAccepted {
		t.Fatalf("detached replay status=%d body=%s", replayResponse.Code,
			replayResponse.Body.String())
	}
	select {
	case <-resumeCalled:
		t.Fatal("decision replay started a duplicate untracked continuation worker")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestWebFetchApprovalDoesNotClaimRetryWithoutScheduler(t *testing.T) {
	api := &API{controlEnabled: true, approvalControlEnabled: true,
		controlTokenHash: sha256.Sum256([]byte(testControlToken)),
		approvalController: cancelAfterWebApprovalController{
			cancel: func() {},
		},
		appVersion: "approval-unscheduled-test"}
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/run-unscheduled/approvals/approval-unscheduled/decision",
		strings.NewReader(`{"version":"approval_control.v1","action":"approve_once"}`))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "approval-unscheduled-operation-0001")
	response := httptest.NewRecorder()
	api.serveApprovalDecisionControl(response, request, "request-unscheduled",
		"run-unscheduled", "approval-unscheduled")
	if response.Code != http.StatusAccepted ||
		!strings.Contains(response.Body.String(), `"retry_scheduled":false`) {
		t.Fatalf("unscheduled approval status=%d body=%s", response.Code,
			response.Body.String())
	}
}

func TestRecoverableWebFetchApprovalExposesOnlyOriginalDecision(t *testing.T) {
	for _, test := range []struct {
		name   string
		status domain.WebFetchAuthorizationStatus
		scope  domain.WebFetchAuthorizationScope
		want   application.ApprovalControlAction
	}{
		{name: "allow once", status: domain.WebFetchAuthorizationApproved,
			scope: domain.WebFetchAuthorizationOnce, want: application.ApprovalControlApproveOnce},
		{name: "allow thread", status: domain.WebFetchAuthorizationApproved,
			scope: domain.WebFetchAuthorizationThread, want: application.ApprovalControlApproveForThread},
		{name: "consumed once", status: domain.WebFetchAuthorizationConsumed,
			scope: domain.WebFetchAuthorizationOnce, want: application.ApprovalControlApproveOnce},
		{name: "deny", status: domain.WebFetchAuthorizationDenied,
			scope: domain.WebFetchAuthorizationOnce, want: application.ApprovalControlDeny},
	} {
		t.Run(test.name, func(t *testing.T) {
			actions := recoverableWebFetchApprovalActions(domain.WebFetchAuthorization{
				Status: test.status, Scope: test.scope})
			if len(actions) != 1 || actions[0] != test.want {
				t.Fatalf("recoverable actions=%v want only %s", actions, test.want)
			}
		})
	}
}

func TestRecoverableApprovalReadOverwritesPendingSnapshotByID(t *testing.T) {
	pending := approval.Record{ID: "approval-race", ProposalID: "proposal-race",
		RunID: "run-race", ToolName: "web_fetch", Status: approval.StatusPending,
		Version: 1}
	decided := pending
	decided.Status = approval.StatusApproved
	decided.Version = 2
	records := []approval.Record{pending}
	index := map[string]int{pending.ID: 0}
	records = upsertApprovalQueueRecord(records, index, decided)
	if len(records) != 1 || records[0].ID != pending.ID ||
		records[0].Status != approval.StatusApproved || records[0].Version != 2 {
		t.Fatalf("recoverable read did not replace pending snapshot: %#v", records)
	}
	other := approval.Record{ID: "approval-other", ProposalID: "proposal-other",
		RunID: "run-race", ToolName: "web_fetch", Status: approval.StatusDenied,
		Version: 2}
	records = upsertApprovalQueueRecord(records, index, other)
	if len(records) != 2 || records[1].ID != other.ID {
		t.Fatalf("distinct approval was not appended exactly once: %#v", records)
	}
}

func TestApprovalHTTPQueueIsMetadataOnlyAndApproveOnceIsClosedAuthority(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "approval-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	workspace := store.WorkspaceRecord{ID: "workspace-approval-http", Name: "approval-http",
		RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}
	if err := st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "inspect approval queue", Profile: "code", WorkspaceID: workspace.ID,
		Budget: domain.Budget{MaxTurns: 4, MaxToolCalls: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = application.NewRunService(st).Start(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	checker := policy.NewDefaultChecker()
	gateway := toolgateway.New(st, checker)
	const privateCommand = "echo private approval payload"
	proposed, err := gateway.Invoke(t.Context(), toolgateway.ToolCall{
		Name: toolgateway.ShellTool, Arguments: map[string]string{"command": privateCommand},
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: workspace.ID,
		RequestedBy: "approval_http_test",
	})
	if err != nil || proposed.Proposal == nil {
		t.Fatalf("proposal=%#v err=%v", proposed, err)
	}
	record, err := st.GetApprovalByProposal(t.Context(), proposed.Proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	controller := application.NewApprovalControlService(st, gateway, checker)
	api, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		ApprovalControlEnabled: true, ApprovalController: controller,
		AppVersion: "approval-control-test"})
	if err != nil {
		t.Fatal(err)
	}
	queuePath := "/api/v1/runs/" + run.ID + "/approvals"
	queue := performSessionMessageRequest(t, api, http.MethodGet, queuePath,
		testAccessToken, "", "", nil)
	if queue.Code != http.StatusOK || strings.Contains(queue.Body.String(), privateCommand) ||
		strings.Contains(queue.Body.String(), "request_fingerprint") ||
		strings.Contains(queue.Body.String(), "decision_reason") {
		t.Fatalf("approval queue leaked proposal content: status=%d body=%s", queue.Code, queue.Body.String())
	}
	var queueEnvelope struct {
		Data ApprovalQueueView `json:"data"`
	}
	if err := json.Unmarshal(queue.Body.Bytes(), &queueEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(queueEnvelope.Data.Items) != 1 || queueEnvelope.Data.Items[0].ID != record.ID ||
		len(queueEnvelope.Data.Items[0].AllowedActions) != 2 ||
		queueEnvelope.Data.ProcessExecutionEnabled || queueEnvelope.Data.SessionGrantCreated ||
		queueEnvelope.Data.CapabilityGrant {
		t.Fatalf("unexpected approval queue: %#v", queueEnvelope.Data)
	}
	previewPath := queuePath + "/" + record.ID + "/preview"
	preview := performSessionMessageRequest(t, api, http.MethodGet, previewPath,
		testAccessToken, "", "", nil)
	var previewEnvelope struct {
		Data ApprovalPreviewView `json:"data"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &previewEnvelope); err != nil {
		t.Fatal(err)
	}
	if preview.Code != http.StatusOK || previewEnvelope.Data.Effect != "dry_run" ||
		!previewEnvelope.Data.SourceCurrent || previewEnvelope.Data.WorkingDirectory != "." ||
		previewEnvelope.Data.ApprovalID != record.ID || previewEnvelope.Data.ProposalID != record.ProposalID ||
		len(previewEnvelope.Data.Fields) != 1 || previewEnvelope.Data.Fields[0].Value != privateCommand {
		t.Fatalf("preview must expose the bound simulated command: status=%d body=%s", preview.Code, preview.Body.String())
	}
	unchanged, err := st.GetApproval(t.Context(), record.ID)
	if err != nil || unchanged.Status != approval.StatusPending {
		t.Fatalf("read-only preview decided approval: %#v %v", unchanged, err)
	}

	decisionPath := "/api/v1/runs/" + run.ID + "/approvals/" + record.ID + "/decision"
	decision := performSessionMessageRequest(t, api, http.MethodPost, decisionPath,
		testControlToken, "approval-http-approve-0001", "application/json",
		strings.NewReader(`{"version":"approval_control.v1","action":"approve_once"}`))
	if decision.Code != http.StatusAccepted || strings.Contains(decision.Body.String(), privateCommand) {
		t.Fatalf("approval decision status=%d body=%s", decision.Code, decision.Body.String())
	}
	var decisionEnvelope struct {
		Data ApprovalDecisionControlView `json:"data"`
	}
	if err := json.Unmarshal(decision.Body.Bytes(), &decisionEnvelope); err != nil {
		t.Fatal(err)
	}
	view := decisionEnvelope.Data
	if view.Status != string(approval.StatusApproved) || view.Replayed ||
		view.ProcessExecutionEnabled || view.ShellExecutionEnabled || view.DockerExecutionEnabled ||
		view.WorkspaceWriteApplied || view.SessionGrantCreated || view.CapabilityGrant {
		t.Fatalf("approval response widened authority: %#v", view)
	}
	replay := performSessionMessageRequest(t, api, http.MethodPost, decisionPath,
		testControlToken, "approval-http-approve-0001", "application/json",
		strings.NewReader(`{"version":"approval_control.v1","action":"approve_once"}`))
	if replay.Code != http.StatusAccepted || !strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatalf("approval replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	empty := performSessionMessageRequest(t, api, http.MethodGet, queuePath,
		testAccessToken, "", "", nil)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatalf("decided approval remained queued: status=%d body=%s", empty.Code, empty.Body.String())
	}
	oldPreview := performSessionMessageRequest(t, api, http.MethodGet, previewPath,
		testAccessToken, "", "", nil)
	if oldPreview.Code != http.StatusOK || !strings.Contains(oldPreview.Body.String(), `"source_current":false`) {
		t.Fatalf("decided proposal was still current: %d %s", oldPreview.Code, oldPreview.Body.String())
	}
}

func TestApprovalHTTPControlCapabilityIsIndependentAndRequiresController(t *testing.T) {
	fixture := newAPIFixture(t)
	path := "/api/v1/runs/" + fixture.run.ID + "/approvals/missing-approval/decision"
	disabled := performSessionMessageRequest(t, fixture.api, http.MethodPost, path,
		testControlToken, "approval-http-disabled-0001", "application/json",
		strings.NewReader(`{"version":"approval_control.v1","action":"deny"}`))
	assertAPIError(t, disabled, http.StatusNotFound, "NOT_FOUND")
	if _, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, ApprovalControlEnabled: true,
		AppVersion: "approval-control-test"}); err == nil {
		t.Fatal("approval capability accepted a missing controller")
	}
	if _, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, WebFetchAuthorizationSchedulerEnabled: true,
		AppVersion: "approval-control-test"}); err == nil {
		t.Fatal("Web fetch scheduler accepted missing execution and approval gates")
	}
	configured, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, RunExecutionEnabled: true,
		ExecutionPermissionControlEnabled:     true,
		ApprovalControlEnabled:                true,
		WebFetchAuthorizationSchedulerEnabled: true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			OperatorApprovalEnabled: true,
		},
		RunExecutionController: detachedWebFetchResumeController{called: make(chan error, 1)},
		ApprovalController:     cancelAfterWebApprovalController{cancel: func() {}},
		AppVersion:             "approval-control-test"})
	if err != nil || !configured.webFetchAuthorizationSchedulerEnabled {
		t.Fatalf("valid Web fetch scheduler config api=%#v err=%v", configured, err)
	}
}
