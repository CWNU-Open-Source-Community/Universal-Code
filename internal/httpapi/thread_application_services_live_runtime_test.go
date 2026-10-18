package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

const threadApplicationServicesFixturePrivateValue = "test-only-private-output-9d430ea5"

// The helper is the same pinned native test executable as its parent. All data,
// sockets and Jobs belong to this test's temporary store and workspace. It does
// not need a shell, an installed development runtime, or inherited credentials.
func TestThreadApplicationServicesFixtureProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--thread-service-fixture" {
		return
	}
	if os.Args[len(os.Args)-1] == "fail" {
		fmt.Fprintln(os.Stdout, "failed-during-startup stdout")
		fmt.Fprintln(os.Stderr, "failed-during-startup stderr")
		os.Exit(23)
	}
	first, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(24)
	}
	second, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = first.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(25)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "native-loopback-service-response")
	})
	go func() { _ = http.Serve(first, handler) }()
	go func() { _ = http.Serve(second, handler) }()
	firstPort := first.Addr().(*net.TCPAddr).Port
	fmt.Fprintf(os.Stdout, "Local: http://localhost:%d/\nSecondary: http://%s/\nservice-fixture-stdout: <literal-output>\n", firstPort, second.Addr())
	fmt.Fprintln(os.Stdout, "fixture environment value:", os.Getenv("SERVICE_FIXTURE_PRIVATE_VALUE"))
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(26)
	}
	fmt.Fprintln(os.Stderr, "fixture temporary directory:", workingDirectory)
	fmt.Fprintln(os.Stderr, "service-fixture-stderr: raw diagnostic")
	select {}
}

type threadApplicationServicesLiveFixture struct {
	store     *store.SQLiteStore
	api       *API
	runtime   *application.CommandRuntimeService
	manager   *runner.CommandRuntimeManager
	caps      domain.ExecutionPermissionRuntimeCapabilities
	workspace store.WorkspaceRecord
}

type threadApplicationServicesLiveJob struct {
	runID, threadID, jobID, callID string
	sourceMessageID                string
	turn                           int
	admitted                       runner.CommandRuntimeJob
	releasedAt                     time.Time
}

func newThreadApplicationServicesLiveFixture(t *testing.T) *threadApplicationServicesLiveFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "application-services.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &threadApplicationServicesLiveFixture{store: st,
		workspace: store.WorkspaceRecord{ID: "thread-service-live-workspace", Name: "loopback fixture", RootPath: root},
		caps: domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
			RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}}
	if err := st.SaveWorkspace(t.Context(), f.workspace); err != nil {
		t.Fatal(err)
	}
	f.manager, err = runner.NewPlatformCommandRuntimeManager(st, idgen.New("thread-service-live-owner"))
	if errors.Is(err, runner.ErrCommandRuntimeUnavailable) {
		t.Skipf("native Command Runtime is unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.manager.Shutdown(ctx); err != nil {
			t.Errorf("shutdown native fixture Jobs: %v", err)
		}
	})
	f.runtime, err = application.NewCommandRuntimeService(st, f.manager, f.caps)
	if err != nil {
		t.Fatal(err)
	}
	// The existing no-op execution controller satisfies the API startup gate.
	// No Run execution endpoint is called: native starts and service stops both
	// use the actual authorized CommandRuntimeService below.
	f.api, err = New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken, RunExecutionEnabled: true,
		RunExecutionController:             runExecutionControllerFake{},
		ExecutionPermissionControlEnabled:  true,
		ExecutionPermissionCapabilities:    f.caps,
		ThreadApplicationServiceController: application.NewThreadApplicationService(st).WithCommandRuntime(f.runtime)})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *threadApplicationServicesLiveFixture) start(t *testing.T, mode string) threadApplicationServicesLiveJob {
	t.Helper()
	ctx := t.Context()
	runs := application.NewRunService(f.store)
	_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "observe an owned native loopback service", Profile: "code",
		Surface: "code", Phase: "deliver", WorkspaceID: f.workspace.ID,
		Budget: domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 8}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunExecutionProfileService(f.store).Change(ctx,
		application.ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local",
			OperationKey: "thread-service-live-profile", RequestedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunExecutionPermissionService(f.store, f.caps).Change(ctx,
		application.ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: string(domain.RunExecutionPermissionFull),
			OperationKey: "thread-service-live-full", RequestedBy: "fixture", ConfirmFull: true}); err != nil {
		t.Fatal(err)
	}
	run, err = runs.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := f.store.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: run.ID, OwnerID: "thread-service-live-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	inputText := "start the deterministic loopback fixture"
	sourceMessageID := ""
	if mode != "fail" {
		queued, err := f.store.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
			RunID: run.ID, SessionID: run.SessionID, Content: inputText,
			OperationKey: "thread-service-live-input", RequestedBy: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		sourceMessageID, inputText = queued.Message.ID, ""
	}
	turn, err := f.store.BeginSupervisorTurn(ctx, acquired.Lease, inputText)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionStart, Commands: []runner.CommandRuntimeSpec{{
			Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
			Executable: executable, Arguments: []string{"-test.run=^TestThreadApplicationServicesFixtureProcess$", "--", "--thread-service-fixture", mode},
			WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{{
				Name: "SERVICE_FIXTURE_PRIVATE_VALUE", Value: threadApplicationServicesFixturePrivateValue}},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 60_000,
			Output:  runner.CommandRuntimeOutputPolicy{InlineBytes: 8192, ArtifactBytes: 8192},
			Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
			Purpose: "start a test-only owned loopback HTTP service"}}}
	if mode == "shared" || mode == "shared-ui" {
		input.Commands[0].WorkspaceMode = runner.CommandRuntimeWorkspaceSharedService
	}
	if mode == "shared-ui" {
		input.Commands[0].TimeoutMilliseconds = 30 * 60 * 1000
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	input, canonical, err := toolgateway.NormalizeCommandRuntimePayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	intentKey := runmutation.SupervisorToolOperationKey(run.ID, turn.Checkpoint.NextTurn,
		string(toolgateway.CommandRuntimeTool), string(canonical))
	callID, err := runmutation.SupervisorToolCallID(intentKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	operationKey := runmutation.SupervisorToolExecutionKey(run.ID, turn.Checkpoint.NextTurn,
		string(toolgateway.CommandRuntimeTool), string(canonical), callID)
	permission, err := f.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	generation, live := f.caps.FullAccessGeneration(permission)
	if !live || generation == 0 {
		t.Fatal("fixture has no live Full grant")
	}
	fence, err := f.caps.RuntimeAuthority.IssueRunAuthorizationFence(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	adapter, _ := f.manager.AdapterIdentity()
	authority, err := commandruntimeadapter.EncodeAuthority(commandruntimeadapter.Authority{
		ProtocolVersion: commandruntimeadapter.OperationAuthorityVersion, RunID: run.ID, Adapter: adapter,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionMode: permission.Mode,
		PermissionGeneration: generation, PermissionRuntimeEpoch: f.caps.RuntimeAuthority.RuntimeEpoch(), RunAuthorizationFence: fence})
	if err != nil {
		t.Fatal(err)
	}
	authority, err = f.runtime.BindCommandRuntimeAuthority(ctx, authority, canonical)
	if errors.Is(err, runner.ErrCommandRuntimeUnavailable) {
		t.Skipf("native Command Runtime is unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "offline-service-fixture", Model: "fixture"}
	if inserted, err := f.store.RecordSupervisorModelStarted(ctx, turn.Checkpoint, attempt); err != nil || !inserted {
		t.Fatalf("record fixture model start: inserted=%t err=%v", inserted, err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, err := f.store.RecordSupervisorModelCompleted(ctx, turn.Checkpoint, attempt,
		llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model,
			ToolCalls: []llm.ToolCall{{ID: callID, Name: string(toolgateway.CommandRuntimeTool), Arguments: canonical, Authority: authority}}})
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := f.store.RecordSupervisorToolExecutionStarted(ctx, checkpoint, callID); err != nil || !inserted {
		t.Fatalf("record fixture command start: inserted=%t err=%v", inserted, err)
	}
	scope := toolgateway.CommandRuntimeContext{InvocationID: "thread-service-live-invocation", OperationKey: operationKey,
		RunID: run.ID, MissionID: run.MissionID, SessionID: run.SessionID, WorkspaceID: f.workspace.ID,
		RootAgentID: turn.Agent.ID, AgentID: turn.Agent.ID, AgentAttemptID: checkpoint.AttemptID,
		Surface: turn.Mode.Surface, Phase: turn.Mode.Phase, Profile: turn.Mode.Profile, Role: domain.AgentRoleRoot, ModeRevision: turn.Mode.Revision,
		PermissionMode: permission.Mode, PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision,
		PermissionGeneration: generation, PermissionRuntimeEpoch: f.caps.RuntimeAuthority.RuntimeEpoch(), RunAuthorizationFence: fence,
		SupervisorToolCallID: callID, CapabilityGeneration: adapter.Generation,
		LeaseID: acquired.Lease.LeaseID, LeaseGeneration: acquired.Lease.Generation, RequestedBy: "run_supervisor", Adapter: adapter,
		PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "high", Reason: "offline fixture upstream decision"}}
	result, err := f.runtime.ExecuteCommandRuntime(ctx, scope, input)
	if err != nil || result.Replayed || len(result.Jobs) != 1 {
		t.Fatalf("authorized native start: jobs=%+v replayed=%t err=%v", result.Jobs, result.Replayed, err)
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := f.store.RecordSupervisorToolResult(ctx, checkpoint, domain.SupervisorToolResult{
		CallID: callID, Status: domain.SupervisorToolCompleted, ResultJSON: string(resultJSON), CompletedAt: time.Now().UTC()}); err != nil || replayed {
		t.Fatalf("record background start receipt: replayed=%t err=%v", replayed, err)
	}
	admitted, err := f.store.GetCommandRuntimeJob(ctx, result.Jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	releasedAt := f.completeTurnAndReleaseLease(t, checkpoint, acquired.Lease)
	thread, err := f.store.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return threadApplicationServicesLiveJob{runID: run.ID, threadID: thread.ID, jobID: result.Jobs[0].ID,
		callID: callID, turn: checkpoint.NextTurn, sourceMessageID: sourceMessageID, admitted: admitted, releasedAt: releasedAt}
}

func (f *threadApplicationServicesLiveFixture) completeTurnAndReleaseLease(t *testing.T,
	checkpoint domain.SupervisorCheckpoint, lease domain.RunExecutionLease,
) time.Time {
	t.Helper()
	ctx := t.Context()
	// Finish the actual model turn using a deterministic final response. The
	// background Job must keep its admitted owner after this lease is released.
	attempt := llm.ModelAttempt{Number: 2, ToolRound: 1, TransportAttempt: 1, MaxAttempts: 1,
		Provider: "offline-service-fixture", Model: "fixture"}
	if inserted, err := f.store.RecordSupervisorModelStarted(ctx, checkpoint, attempt); err != nil || !inserted {
		t.Fatalf("record final fixture model start: inserted=%t err=%v", inserted, err)
	}
	action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue,
		Message: "The owned background fixture was started."}
	raw, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model, Text: string(raw)}
	checkpoint, err = f.store.RecordSupervisorModelCompleted(ctx, checkpoint, attempt, response)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.store.CompleteSupervisorTurn(ctx, checkpoint, response, action, policy.Decision{Allowed: true}, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	return time.Now().UTC()
}

func (f *threadApplicationServicesLiveFixture) waitOwnerRenewal(t *testing.T, job threadApplicationServicesLiveJob) {
	t.Helper()
	deadline := time.Now().Add(time.Duration(job.admitted.TimeoutMilliseconds) * time.Millisecond)
	for {
		current, err := f.store.GetCommandRuntimeJob(t.Context(), job.jobID)
		if err != nil || current.State != runner.CommandRuntimeJobRunning {
			t.Fatalf("background service lost ownership after model turn ended: state=%s err=%v", current.State, err)
		}
		if current.OwnerRenewedAt.After(job.releasedAt) {
			if current.OwnerID != job.admitted.OwnerID || current.OwnerGeneration != job.admitted.OwnerGeneration ||
				current.LeaseID != job.admitted.LeaseID || current.LeaseGeneration != job.admitted.LeaseGeneration {
				t.Fatal("background continuation changed its admitted owner or original Run lease")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background native owner did not renew after its model turn lease was released")
		}
		if _, _, err := f.manager.Wait(t.Context(), job.jobID, 100*time.Millisecond, ^uint64(0), runner.MinCommandRuntimeOutputRead); err != nil {
			t.Fatal(err)
		}
	}
}

type threadApplicationServicesLiveDetail = application.ThreadApplicationServiceDetailView

func (job threadApplicationServicesLiveJob) path() string {
	return "/api/v1/threads/" + job.threadID + "/application-services/" + job.jobID
}

func (f *threadApplicationServicesLiveFixture) detail(t *testing.T, job threadApplicationServicesLiveJob) threadApplicationServicesLiveDetail {
	t.Helper()
	response := performRequest(t, f.api, http.MethodGet, job.path(), testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	var detail threadApplicationServicesLiveDetail
	decodeDataStatus(t, response, http.StatusOK, &detail)
	for _, private := range []string{`"pid"`, `"executable"`, `"executable_path"`, `"environment_sha256"`, `"workspace_root"`, `"lease_id"`, `"stdin"`} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("service detail exposed private runtime field %s: %s", private, response.Body.String())
		}
	}
	if detail.Version != "thread_application_services.v1" || detail.Service.ThreadID != job.threadID ||
		detail.Service.RunID != job.runID || detail.Service.JobID != job.jobID {
		t.Fatalf("service detail lost exact source identity: %+v", detail)
	}
	return detail
}

func (f *threadApplicationServicesLiveFixture) waitDetail(t *testing.T, job threadApplicationServicesLiveJob,
	accept func(threadApplicationServicesLiveDetail) bool,
) threadApplicationServicesLiveDetail {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		detail := f.detail(t, job)
		if accept(detail) {
			return detail
		}
		if time.Now().After(deadline) {
			t.Fatalf("native service detail did not converge: %+v", detail)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func threadApplicationServicesLiveOutput(detail threadApplicationServicesLiveDetail, stream string) string {
	if stream == "stdout" {
		return detail.Output.Stdout
	}
	return detail.Output.Stderr
}

func assertThreadApplicationServicesLoopbackResponse(t *testing.T, address string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Get(address)
	if err != nil {
		t.Fatalf("native loopback service at %s: %v", address, err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(content) != "native-loopback-service-response" {
		t.Fatalf("native loopback response: status=%d body=%q err=%v", response.StatusCode, content, err)
	}
}

func TestThreadApplicationServicesHTTPReadsNativeOutputAndStopsOnlyExactJob(t *testing.T) {
	f := newThreadApplicationServicesLiveFixture(t)
	selected, other := f.start(t, "serve"), f.start(t, "serve")
	f.waitOwnerRenewal(t, selected)
	f.waitOwnerRenewal(t, other)
	running := f.waitDetail(t, selected, func(detail threadApplicationServicesLiveDetail) bool {
		return len(detail.CandidateURLs) == 2 &&
			strings.Contains(threadApplicationServicesLiveOutput(detail, "stdout"), "service-fixture-stdout: <literal-output>") &&
			strings.Contains(threadApplicationServicesLiveOutput(detail, "stderr"), "service-fixture-stderr: raw diagnostic")
	})
	otherRunning := f.waitDetail(t, other, func(detail threadApplicationServicesLiveDetail) bool { return len(detail.CandidateURLs) == 2 })
	if running.Service.State != string(runner.CommandRuntimeJobRunning) || !running.Service.CanStop ||
		running.Service.SourceCallID != selected.callID || running.Service.SourceTurn != selected.turn ||
		running.Service.SourceMessageID != selected.sourceMessageID || selected.sourceMessageID == "" ||
		!running.Output.Available || running.Output.Dropped || running.Output.NextCursor != running.Output.EndCursor ||
		running.Output.EndCursor <= running.Output.BaseCursor {
		t.Fatalf("live service projection is incomplete: %+v", running)
	}
	if strings.Contains(running.Output.Stdout, threadApplicationServicesFixturePrivateValue) ||
		strings.Contains(running.Output.Stderr, f.workspace.RootPath) {
		t.Fatalf("public output exposed test environment or temporary host path: %+v", running.Output)
	}
	if !strings.Contains(running.Output.Stdout, "http://localhost:") {
		t.Fatalf("output scrubber lost the printed localhost port: %q", running.Output.Stdout)
	}
	printedLocal, err := url.Parse(strings.Split(strings.TrimPrefix(running.Output.Stdout, "Local: "), "\n")[0])
	if err != nil || printedLocal.Port() == "" {
		t.Fatalf("invalid printed localhost address: stdout=%q err=%v", running.Output.Stdout, err)
	}
	localCandidateFound := false
	for _, candidate := range running.CandidateURLs {
		loopbackURL := strings.HasPrefix(candidate.URL, "http://127.0.0.1:") || strings.HasPrefix(candidate.URL, "http://localhost:")
		if candidate.Verified || candidate.Source != "command_output" || !loopbackURL {
			t.Fatalf("printed address was incorrectly verified: %+v", candidate)
		}
		parsed, err := url.Parse(candidate.URL)
		if err != nil {
			t.Fatal(err)
		}
		localCandidateFound = localCandidateFound || parsed.Port() == printedLocal.Port()
		assertThreadApplicationServicesLoopbackResponse(t, candidate.URL)
	}
	if !localCandidateFound {
		t.Fatalf("candidate extraction lost the printed localhost port: %+v", running.CandidateURLs)
	}
	assertThreadApplicationServicesLoopbackResponse(t, otherRunning.CandidateURLs[0].URL)
	listResponse := performRequest(t, f.api, http.MethodGet,
		"/api/v1/threads/"+selected.threadID+"/application-services?limit=20", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	var list struct {
		ThreadID string `json:"thread_id"`
		Services []struct {
			JobID string `json:"job_id"`
			RunID string `json:"run_id"`
		} `json:"services"`
	}
	decodeDataStatus(t, listResponse, http.StatusOK, &list)
	if list.ThreadID != selected.threadID || len(list.Services) != 1 || list.Services[0].JobID != selected.jobID || list.Services[0].RunID != selected.runID {
		t.Fatalf("Thread service list mixed unrelated Jobs: %s", listResponse.Body.String())
	}
	stopBody := func(runID string) *strings.Reader {
		body, err := json.Marshal(map[string]string{"version": "thread_application_services.v1", "expected_run_id": runID})
		if err != nil {
			t.Fatal(err)
		}
		return strings.NewReader(string(body))
	}
	stopKey := "application-stop-" + selected.jobID
	crossThread := performControlPathRequest(t, f.api, strings.Replace(selected.path(), selected.threadID, other.threadID, 1)+"/stop",
		stopKey, stopBody(selected.runID))
	assertAPIError(t, crossThread, http.StatusNotFound, "NOT_FOUND")
	wrongRun := performControlPathRequest(t, f.api, selected.path()+"/stop", stopKey, stopBody(other.runID))
	assertAPIError(t, wrongRun, http.StatusConflict, "CONFLICT")
	readToken := performRequest(t, f.api, http.MethodPost, selected.path()+"/stop", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", stopBody(selected.runID))
	assertAPIError(t, readToken, http.StatusUnauthorized, "POLICY_DENIED")
	assertThreadApplicationServicesLoopbackResponse(t, running.CandidateURLs[0].URL)
	assertThreadApplicationServicesLoopbackResponse(t, otherRunning.CandidateURLs[0].URL)
	stop := performControlPathRequest(t, f.api, selected.path()+"/stop", stopKey, stopBody(selected.runID))
	if stop.Code != http.StatusOK {
		t.Fatalf("exact stop: status=%d body=%s", stop.Code, stop.Body.String())
	}
	stopped := f.waitDetail(t, selected, func(detail threadApplicationServicesLiveDetail) bool {
		return detail.Service.State == string(runner.CommandRuntimeJobCancelled)
	})
	if stopped.Service.CanStop || !strings.Contains(threadApplicationServicesLiveOutput(stopped, "stdout"), "<literal-output>") {
		t.Fatalf("terminal service lost output or remains stoppable: %+v", stopped)
	}
	durable, err := f.store.GetCommandRuntimeJob(t.Context(), selected.jobID)
	if err != nil || durable.State != runner.CommandRuntimeJobCancelled || !durable.TreeReaped {
		t.Fatalf("exact native tree was not reaped: state=%s reaped=%t err=%v", durable.State, durable.TreeReaped, err)
	}
	replay := performControlPathRequest(t, f.api, selected.path()+"/stop", stopKey, stopBody(selected.runID))
	var replayEnvelope struct {
		Replayed bool `json:"replayed"`
	}
	decodeDataStatus(t, replay, http.StatusOK, &replayEnvelope)
	if !replayEnvelope.Replayed {
		t.Fatalf("exact stop did not replay: %s", replay.Body.String())
	}
	assertThreadApplicationServicesLoopbackResponse(t, otherRunning.CandidateURLs[0].URL)
	if otherState := f.detail(t, other); otherState.Service.State != string(runner.CommandRuntimeJobRunning) || !otherState.Service.CanStop {
		t.Fatalf("selected stop affected another Job: %+v", otherState)
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	if response, err := client.Get(running.CandidateURLs[0].URL); err == nil {
		_ = response.Body.Close()
		t.Fatal("selected native loopback service still responds after durable tree reap")
	}
}

func TestThreadApplicationServicesHTTPRetainsNativeNonzeroExit(t *testing.T) {
	f := newThreadApplicationServicesLiveFixture(t)
	failed := f.start(t, "fail")
	detail := f.waitDetail(t, failed, func(detail threadApplicationServicesLiveDetail) bool {
		return detail.Service.State == string(runner.CommandRuntimeJobFailed)
	})
	if detail.Service.ExitCode == nil || *detail.Service.ExitCode != 23 || detail.Service.CanStop || detail.Service.SourceMessageID != "" ||
		!detail.Output.Available || len(detail.CandidateURLs) != 0 ||
		!strings.Contains(threadApplicationServicesLiveOutput(detail, "stdout"), "failed-during-startup stdout") ||
		!strings.Contains(threadApplicationServicesLiveOutput(detail, "stderr"), "failed-during-startup stderr") {
		t.Fatalf("native nonzero startup exit was lost or marked active: %+v", detail)
	}
	durable, err := f.store.GetCommandRuntimeJob(t.Context(), failed.jobID)
	if err != nil || !durable.TreeReaped || durable.ExitCode == nil || *durable.ExitCode != 23 {
		t.Fatalf("native failure receipt: job=%+v err=%v", durable, err)
	}
}
