package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func TestThreadRunRecoveryViewDoesNotExposeRawFailureText(t *testing.T) {
	view := threadRunRecoveryView(domain.ThreadRunRecovery{
		ProtocolVersion: domain.ThreadRunRecoveryProtocolVersion,
		ThreadID:        "thread-redaction-test", RunID: "run-redaction-test",
		HandoffOperationID: "run-handoff-redaction-test",
		ErrorCode:          "provider_secret_error", StopReason: "provider-secret-response-must-not-appear",
	})
	if view.ErrorCode != "durable_failure" || view.StopReason != "durable_failure" ||
		view.Detail == "" || strings.Contains(view.Detail, "结束旧 Run") {
		t.Fatalf("raw recovery failure escaped its allowlist: %#v", view)
	}
}

func TestThreadRunRecoveryViewDoesNotGuessFailureCause(t *testing.T) {
	for _, test := range []struct {
		code, want string
	}{
		{"failed_precondition", "执行条件未满足"},
		{"unavailable", "服务或运行环境暂不可用"},
	} {
		t.Run(test.code, func(t *testing.T) {
			view := threadRunRecoveryView(domain.ThreadRunRecovery{
				ProtocolVersion: domain.ThreadRunRecoveryProtocolVersion,
				RunID:           "run-recovery-test", HandoffOperationID: "handoff-recovery-test",
				ErrorCode: test.code, StopReason: "internal-provider-secret",
				Detail: "internal raw cause must not be exposed", Quiescent: true,
			})
			if view.ErrorCode != test.code || view.StopReason != test.code ||
				view.RunID != "run-recovery-test" || !view.Quiescent ||
				!strings.Contains(view.Detail, test.want) {
				t.Fatalf("recovery identity or actionable generic cause lost: %#v", view)
			}
			for _, unsupported := range []string{"固定权限", "模型或运行配置", "新模型", "模型服务", "internal"} {
				if strings.Contains(view.Detail, unsupported) {
					t.Fatalf("generic failure inferred or exposed a cause: %q", view.Detail)
				}
			}
		})
	}
}

func TestThreadHTTPContractContinuesTerminalRunAndPreservesLifecycleHistory(t *testing.T) {
	fixture := newAPIFixture(t)
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, RunCreationEnabled: true,
		SessionMessageEnabled: true, AppVersion: "thread-http-contract-test"})
	if err != nil {
		t.Fatal(err)
	}
	creationBody, _ := json.Marshal(ThreadCreationControlRequestView{
		Version: domain.ThreadCreationProtocolVersion, Goal: "HTTP stable Thread task",
		WorkspaceID: fixture.workspace.ID, Profile: "review", Surface: "code", Phase: "deliver",
	})
	response := performSessionMessageRequest(t, api, http.MethodPost, ThreadCollectionPath,
		testControlToken, "thread-http-create-operation-0001", "application/json",
		bytes.NewReader(creationBody))
	var created ThreadCreationControlView
	decodeDataStatus(t, response, http.StatusAccepted, &created)
	if created.Thread.ID == "" || created.Thread.ActiveRunID != created.Run.ID ||
		created.Thread.LastRunID != created.Run.ID || created.Thread.Status != "active" {
		t.Fatalf("invalid Thread creation projection: %#v", created)
	}

	runs := application.NewRunService(fixture.store)
	started, err := runs.Start(t.Context(), created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := runs.Fail(t.Context(), started.ID, "HTTP fixture terminal")
	if err != nil || terminal.Status != domain.RunFailed {
		t.Fatalf("terminal Run=%#v err=%v", terminal, err)
	}
	messageBody, _ := json.Marshal(ThreadMessageControlRequestView{
		Version: domain.ThreadMessageProtocolVersion, Content: "continue after failure",
	})
	response = performSessionMessageRequest(t, api, http.MethodPost,
		ThreadCollectionPath+"/"+created.Thread.ID+"/messages", testControlToken,
		"thread-http-message-operation-0001", "application/json", bytes.NewReader(messageBody))
	var continued ThreadMessageControlView
	decodeDataStatus(t, response, http.StatusAccepted, &continued)
	if continued.Thread.ID != created.Thread.ID || !continued.SuccessorCreated ||
		continued.PredecessorRunID != terminal.ID || continued.RunID == terminal.ID ||
		continued.Thread.ActiveRunID != continued.RunID || continued.CapabilityGrant ||
		continued.ExecutionStarted || continued.ModelCalled || continued.ToolCalled {
		t.Fatalf("invalid Thread continuation projection: %#v", continued)
	}

	response = performSessionMessageRequest(t, api, http.MethodGet,
		ThreadCollectionPath+"/"+created.Thread.ID, testAccessToken, "", "", nil)
	var detail ThreadDetailView
	decodeDataStatus(t, response, http.StatusOK, &detail)
	if detail.Thread.ID != created.Thread.ID || detail.ActiveRun == nil ||
		detail.ActiveRun.ID != continued.RunID || len(detail.Runs) != 2 ||
		detail.Runs[1].PredecessorRunID != terminal.ID {
		t.Fatalf("invalid Thread detail after continuation: %#v", detail)
	}
	if detail.Statistics == nil || detail.Statistics.Turns != 1 || detail.Statistics.Steps != 0 || detail.Statistics.Context != nil {
		t.Fatalf("invalid root statistics on the existing read endpoint: %#v", detail.Statistics)
	}
	response = performSessionMessageRequest(t, api, http.MethodGet,
		ThreadCollectionPath+"/"+created.Thread.ID+"/messages?limit=100&include_compacted=true",
		testAccessToken, "", "", nil)
	var messages []ThreadMessageView
	decodeDataStatus(t, response, http.StatusOK, &messages)
	if len(messages) != 1 || messages[0].ThreadID != created.Thread.ID ||
		messages[0].RunID != continued.RunID || messages[0].Content != "continue after failure" ||
		messages[0].ProvenanceVersion != session.ContextProvenanceVersion ||
		messages[0].SourceKind != session.SourceOperatorMessage ||
		messages[0].ContentSHA256 == "" || !messages[0].InstructionAuthorized {
		t.Fatalf("invalid cross-Run Thread messages: %#v", messages)
	}

	archiveBody, _ := json.Marshal(ThreadLifecycleControlRequestView{
		Version:         domain.ThreadLifecycleProtocolVersion,
		ExpectedVersion: continued.Thread.Version,
	})
	archivePath := ThreadCollectionPath + "/" + created.Thread.ID + "/archive"
	response = performSessionMessageRequest(t, api, http.MethodPost, archivePath,
		testControlToken, "thread-http-archive-operation-0001", "application/json",
		bytes.NewReader(archiveBody))
	var archived ThreadLifecycleControlView
	decodeDataStatus(t, response, http.StatusOK, &archived)
	if archived.Thread.Status != "archived" || archived.CapabilityGrant {
		t.Fatalf("invalid archived Thread: %#v", archived)
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, archivePath,
		testControlToken, "thread-http-archive-operation-0001", "application/json",
		bytes.NewReader(archiveBody))
	var replayed ThreadLifecycleControlView
	decodeDataStatus(t, response, http.StatusOK, &replayed)
	if replayed.Thread.Version != archived.Thread.Version ||
		replayed.Thread.UpdatedAt != archived.Thread.UpdatedAt {
		t.Fatalf("Thread lifecycle replay drifted: original=%#v replay=%#v", archived, replayed)
	}
	restoreBody, _ := json.Marshal(ThreadLifecycleControlRequestView{
		Version:         domain.ThreadLifecycleProtocolVersion,
		ExpectedVersion: archived.Thread.Version,
	})
	response = performSessionMessageRequest(t, api, http.MethodPost,
		ThreadCollectionPath+"/"+created.Thread.ID+"/restore", testControlToken,
		"thread-http-restore-operation-0001", "application/json", bytes.NewReader(restoreBody))
	var restored ThreadLifecycleControlView
	decodeDataStatus(t, response, http.StatusOK, &restored)
	if restored.Thread.Status != "active" {
		t.Fatalf("invalid restored Thread: %#v", restored)
	}

	response = performSessionMessageRequest(t, api, http.MethodGet,
		ThreadCollectionPath+"/"+created.Thread.ID+"/export", testAccessToken, "", "", nil)
	var exported ThreadExportView
	decodeDataStatus(t, response, http.StatusOK, &exported)
	if exported.Thread.ID != created.Thread.ID || len(exported.Runs) != 2 ||
		len(exported.Sessions) != 2 ||
		len(exported.Messages) != 1 || len(exported.Events) < 5 ||
		len(exported.AuditEvents) == 0 {
		t.Fatalf("Thread export lost bound history: %#v", exported)
	}
}

func TestThreadHTTPCreationBindsCanonicalExactNetworkAllowlist(t *testing.T) {
	fixture := newAPIFixture(t)
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, RunCreationEnabled: true,
		SessionMessageEnabled: true, AppVersion: "thread-network-authority-test"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ThreadCreationControlRequestView{
		Version: domain.ThreadCreationProtocolVersion, Goal: "Research official sources",
		WorkspaceID: fixture.workspace.ID, NetworkMode: "allowlist",
		AllowedTargets: []string{"HTTPS://Docs.Example.COM:443/", "api.example.com"},
	})
	response := performSessionMessageRequest(t, api, http.MethodPost, ThreadCollectionPath,
		testControlToken, "thread-http-network-operation-0001", "application/json",
		bytes.NewReader(body))
	var created ThreadCreationControlView
	decodeDataStatus(t, response, http.StatusAccepted, &created)
	if created.Thread.ID == "" || created.Mission.Scope.NetworkMode != "allowlist" ||
		created.Mode.Scope.NetworkMode != "allowlist" ||
		len(created.Mission.Scope.AllowedTargets) != 2 ||
		created.Mission.Scope.AllowedTargets[0] != "api.example.com" ||
		created.Mission.Scope.AllowedTargets[1] != "docs.example.com" {
		t.Fatalf("Thread network authority drifted: %#v", created)
	}
}

func TestThreadHTTPControlFailsClosedWithoutControlBearer(t *testing.T) {
	fixture := newAPIFixture(t)
	body := bytes.NewBufferString(`{"version":"thread_message_submission.v1","content":"no"}`)
	response := performSessionMessageRequest(t, fixture.api, http.MethodPost,
		ThreadCollectionPath+"/"+domain.InitialThreadID(fixture.run.ID)+"/messages",
		testAccessToken, "thread-http-denied-operation-0001", "application/json", body)
	assertAPIError(t, response, http.StatusUnauthorized, "POLICY_DENIED")
}

func TestThreadHTTPCreationFailsClosedWithoutCompleteThreadCapability(t *testing.T) {
	fixture := newAPIFixture(t)
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, RunCreationEnabled: true,
		SessionMessageEnabled: false, AppVersion: "thread-http-gate-test"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ThreadCreationControlRequestView{
		Version: domain.ThreadCreationProtocolVersion, Goal: "must stay unavailable",
		WorkspaceID: fixture.workspace.ID,
	})
	response := performSessionMessageRequest(t, api, http.MethodPost, ThreadCollectionPath,
		testControlToken, "thread-http-disabled-operation-0001", "application/json",
		bytes.NewReader(body))
	assertAPIError(t, response, http.StatusNotFound, "NOT_FOUND")
}

func TestThreadTurnHTTPUsesProductFacadeWithoutRunControlsInRequest(t *testing.T) {
	fixture := newAPIFixture(t)
	threadRecord, err := fixture.store.GetThreadByRun(t.Context(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	linkedSession, err := fixture.store.GetSession(t.Context(), fixture.run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := fixture.store.EnqueueOperatorSteering(t.Context(),
		domain.EnqueueOperatorSteeringRequest{RunID: fixture.run.ID,
			SessionID: fixture.run.SessionID, Content: "facade result",
			OperationKey: "thread-turn-http-fixture-message-0001",
			RequestedBy:  "http_thread_operator"})
	if err != nil {
		t.Fatal(err)
	}
	controller := &threadTurnControllerStub{result: application.ExecuteThreadTurnResult{
		Submission: application.SubmitThreadMessageResult{Thread: threadRecord,
			Run: fixture.run, Session: linkedSession, Message: queued.Message},
		ExecutionStarted: true, ModelCalled: true,
	}}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, RunCreationEnabled: true,
		SessionMessageEnabled: true, RunLifecycleEnabled: true,
		RunExecutionEnabled:    true,
		RunLifecycleController: application.NewRunLifecycleControlService(fixture.store),
		RunExecutionController: runExecutionControllerFake{},
		ThreadTurnController:   controller, AppVersion: "thread-turn-http-test"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ThreadMessageControlRequestView{
		Version: domain.ThreadMessageProtocolVersion,
		Content: "Execute this complete product turn.",
	})
	response := performSessionMessageRequest(t, api, http.MethodPost,
		ThreadCollectionPath+"/"+threadRecord.ID+"/turns", testControlToken,
		"thread-turn-http-operation-0001", "application/json", bytes.NewReader(body))
	var view ThreadMessageControlView
	decodeDataStatus(t, response, http.StatusAccepted, &view)
	if view.Thread.ID != threadRecord.ID || view.RunID != fixture.run.ID ||
		view.SessionID != fixture.run.SessionID || !view.ExecutionStarted ||
		!view.ModelCalled || view.ToolCalled || view.CapabilityGrant ||
		controller.request.ThreadID != threadRecord.ID ||
		controller.request.Content != "Execute this complete product turn." ||
		controller.request.OperationKey != "thread-turn-http-operation-0001" ||
		controller.request.RequestedBy != "http_thread_operator" ||
		bytes.Contains(response.Body.Bytes(), []byte(`"max_steps"`)) ||
		bytes.Contains(response.Body.Bytes(), []byte(`"run_status"`)) {
		t.Fatalf("unexpected Thread turn HTTP contract: view=%#v request=%#v body=%s",
			view, controller.request, response.Body.String())
	}
}

func TestThreadTurnHTTPFailsClosedWithoutEveryExistingExecutionCapability(t *testing.T) {
	fixture := newAPIFixture(t)
	threadRecord, err := fixture.store.GetThreadByRun(t.Context(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, RunCreationEnabled: true,
		SessionMessageEnabled: true, RunLifecycleEnabled: true,
		RunLifecycleController: application.NewRunLifecycleControlService(fixture.store),
		ThreadTurnController:   &threadTurnControllerStub{},
		AppVersion:             "thread-turn-http-gate-test"})
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"version":"thread_message_submission.v1",` +
		`"content":"must remain unavailable"}`)
	response := performSessionMessageRequest(t, api, http.MethodPost,
		ThreadCollectionPath+"/"+threadRecord.ID+"/turns", testControlToken,
		"thread-turn-http-disabled-0001", "application/json", body)
	assertAPIError(t, response, http.StatusNotFound, string(apperror.CodeNotFound))
	if _, err := New(fixture.store, Config{AccessToken: testAccessToken,
		ControlToken: testControlToken, RunCreationEnabled: true,
		SessionMessageEnabled: true, RunLifecycleEnabled: true,
		RunExecutionEnabled:    true,
		RunLifecycleController: application.NewRunLifecycleControlService(fixture.store),
		RunExecutionController: runExecutionControllerFake{},
		AppVersion:             "thread-turn-missing-controller-test"}); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("complete Thread turn capability accepted no facade controller: %v", err)
	}
}

type threadTurnControllerStub struct {
	request                application.ExecuteThreadTurnRequest
	result                 application.ExecuteThreadTurnResult
	err                    error
	delay                  time.Duration
	interruptedThreadID    string
	interruptedExecutionID string
}

func (s *threadTurnControllerStub) ExecutionStates(ctx context.Context, ids []string) (map[string]application.ThreadExecutionState, error) {
	states := make(map[string]application.ThreadExecutionState, len(ids))
	for _, id := range ids {
		states[id], _ = s.ExecutionState(ctx, id)
	}
	return states, nil
}

func (s *threadTurnControllerStub) ResumeApproval(context.Context, application.ApprovalContinuationRequest) application.ApprovalContinuationResult {
	return application.ApprovalContinuationResult{State: "not_started"}
}

func (s *threadTurnControllerStub) ExecutionState(_ context.Context, threadID string) (application.ThreadExecutionState, error) {
	return application.ThreadExecutionState{Version: application.ThreadExecutionProtocolVersion,
		ThreadID: threadID, State: "idle"}, nil
}

func (s *threadTurnControllerStub) Interrupt(_ context.Context, threadID, executionID string) (application.ThreadExecutionState, error) {
	s.interruptedThreadID, s.interruptedExecutionID = threadID, executionID
	return application.ThreadExecutionState{Version: application.ThreadExecutionProtocolVersion,
		ThreadID: threadID, ExecutionID: executionID, State: "stopping"}, nil
}

func TestThreadInterruptHTTPRequiresControlAndExactRequestShape(t *testing.T) {
	fixture := newAPIFixture(t)
	threadRecord, err := fixture.store.GetThreadByRun(t.Context(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	controller := &threadTurnControllerStub{}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true, RunExecutionEnabled: true,
		RunLifecycleController: application.NewRunLifecycleControlService(fixture.store),
		RunExecutionController: runExecutionControllerFake{}, ThreadTurnController: controller})
	if err != nil {
		t.Fatal(err)
	}
	path := ThreadCollectionPath + "/" + threadRecord.ID + "/interrupt"
	body := `{"version":"thread_execution.v1","execution_id":"thread-execution-current"}`
	response := performSessionMessageRequest(t, api, http.MethodPost, path, testAccessToken,
		"thread-stop-http-operation-0001", "application/json", strings.NewReader(body))
	if response.Code < 400 || controller.interruptedExecutionID != "" {
		t.Fatal("read token stopped execution")
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken,
		"thread-stop-http-operation-0002", "application/json",
		strings.NewReader(`{"version":"thread_execution.v1","execution_id":"thread-execution-current","run_id":"another-run"}`))
	if response.Code != http.StatusBadRequest || controller.interruptedExecutionID != "" {
		t.Fatal("unknown control field was accepted")
	}
	response = performSessionMessageRequest(t, api, http.MethodPost, path, testControlToken,
		"thread-stop-http-operation-0003", "application/json", strings.NewReader(body))
	var state application.ThreadExecutionState
	decodeDataStatus(t, response, http.StatusAccepted, &state)
	if controller.interruptedThreadID != threadRecord.ID || controller.interruptedExecutionID != "thread-execution-current" ||
		state.State != "stopping" || state.CapabilityGrant {
		t.Fatalf("stop projection=%#v", state)
	}
	response = performSessionMessageRequest(t, api, http.MethodGet, ThreadCollectionPath+"/"+threadRecord.ID+"/execution",
		testAccessToken, "", "", nil)
	decodeDataStatus(t, response, http.StatusOK, &state)
	if state.ThreadID != threadRecord.ID || state.State != "idle" {
		t.Fatalf("read projection=%#v", state)
	}
}

func (s *threadTurnControllerStub) Execute(ctx context.Context,
	request application.ExecuteThreadTurnRequest,
) (application.ExecuteThreadTurnResult, error) {
	s.request = request
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return application.ExecuteThreadTurnResult{}, ctx.Err()
		}
	}
	return s.result, s.err
}

func TestThreadTurnHTTPRespondsAfterOrdinaryWriteTimeout(t *testing.T) {
	fixture := newAPIFixture(t)
	controller := &threadTurnControllerStub{delay: 100 * time.Millisecond}
	api, err := New(fixture.store, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		RunCreationEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true, RunExecutionEnabled: true,
		RunLifecycleController: application.NewRunLifecycleControlService(fixture.store),
		RunExecutionController: runExecutionControllerFake{}, ThreadTurnController: controller})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(api)
	server.Config.WriteTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+ThreadCollectionPath+"/thread-timeout/turns",
		strings.NewReader(`{"version":"thread_message_submission.v1","content":"long turn"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "thread-long-response-operation-0001")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusAccepted {
		t.Fatalf("lost long turn response: status=%d err=%v body=%s", response.StatusCode, err, body)
	}
	if server.Config.WriteTimeout != 20*time.Millisecond {
		t.Fatal("Thread handler changed the global HTTP timeout")
	}
}
