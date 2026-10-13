package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/toolgateway"
)

func TestThreadActivityHTTPReadsLiveProcessRingBeforeDurableHeartbeat(t *testing.T) {
	fixture := newAPIFixture(t)
	runRecord, root, lease, checkpoint, attempt, capabilities :=
		newThreadActivityCommandRuntimeFixture(t, fixture)
	profile := runner.CommandRuntimeBash
	const environmentValue = "ordinary-value-never-public"
	script := "printf 'phase-one %s\\n' \"$VISIBLE_ACTIVITY\"; sleep 0.7; " +
		"printf '%07000dphase-two\\n' 0; sleep 2"
	if runtime.GOOS == "windows" {
		profile = runner.CommandRuntimePowerShell
		script = "Write-Output ('phase-one ' + $env:VISIBLE_ACTIVITY); " +
			"Start-Sleep -Milliseconds 700; " +
			"Write-Output (('x' * 7000) + 'phase-two'); Start-Sleep -Seconds 2"
	}
	maxBytes := runner.MinCommandRuntimeOutputRead
	input := toolgateway.CommandRuntimeInput{
		Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action:  toolgateway.CommandRuntimeActionRun,
		Commands: []runner.CommandRuntimeSpec{{
			Version: runner.CommandRuntimeProtocolVersion, Profile: profile,
			Script: script, WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{{
				Name: "VISIBLE_ACTIVITY", Value: environmentValue}},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 10_000,
			Output: runner.CommandRuntimeOutputPolicy{InlineBytes: runner.MinCommandRuntimeInlineBytes,
				ArtifactBytes: 4 * runner.MinCommandRuntimeInlineBytes},
			Network:     runner.CommandRuntimeNetworkDisabled,
			Credentials: runner.CommandRuntimeCredentialsNone,
			Purpose:     "prove live Thread activity output refresh",
		}},
		FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := toolgateway.NormalizeSupervisorToolPayload(
		toolgateway.CommandRuntimeTool, raw)
	if err != nil {
		t.Fatal(err)
	}
	intentKey := runmutation.SupervisorToolOperationKey(runRecord.ID,
		checkpoint.NextTurn, string(toolgateway.CommandRuntimeTool), string(canonical))
	callID, err := runmutation.SupervisorToolCallID(intentKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	operationKey := runmutation.SupervisorToolExecutionKey(runRecord.ID, checkpoint.NextTurn,
		string(toolgateway.CommandRuntimeTool), string(canonical), callID)
	manager, err := runner.NewPlatformCommandRuntimeManager(fixture.store,
		"thread-activity-live-owner")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	adapter, _ := manager.AdapterIdentity()
	permission, err := fixture.store.GetRunExecutionPermission(t.Context(), runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	generation, live := capabilities.FullAccessGeneration(permission)
	if !live || generation == 0 {
		t.Fatal("fixture has no current Full grant")
	}
	fence, err := capabilities.RuntimeAuthority.IssueRunAuthorizationFence(runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := commandruntimeadapter.EncodeAuthority(commandruntimeadapter.Authority{
		ProtocolVersion: commandruntimeadapter.OperationAuthorityVersion, RunID: runRecord.ID, Adapter: adapter,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionMode: permission.Mode,
		PermissionGeneration: generation, PermissionRuntimeEpoch: capabilities.RuntimeAuthority.RuntimeEpoch(),
		RunAuthorizationFence: fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, err = fixture.store.RecordSupervisorModelCompleted(t.Context(),
		checkpoint, attempt, llm.ChatResponse{Provider: attempt.Provider,
			Model: attempt.Model, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			ToolCalls: []llm.ToolCall{{ID: callID,
				Name: string(toolgateway.CommandRuntimeTool), Arguments: raw,
				Authority: authority}}})
	if err != nil {
		t.Fatal(err)
	}
	if inserted, startErr := fixture.store.RecordSupervisorToolExecutionStarted(
		t.Context(), checkpoint, callID); startErr != nil || !inserted {
		t.Fatalf("record command execution started: inserted=%t err=%v", inserted, startErr)
	}
	resolved, err := runner.NormalizeCommandRuntimeSpec(input.Commands[0],
		fixture.workspace.RootPath)
	if errors.Is(err, runner.ErrCommandRuntimeUnavailable) {
		t.Skipf("%s is unavailable: %v", profile, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	batchDigest := sha256.Sum256([]byte(fmt.Sprintf("command-runtime-batch.v2:%d:%s",
		0, operationKey)))
	batchOperationKey := "command-runtime-" + hex.EncodeToString(batchDigest[:])
	mode, err := fixture.store.GetRunMode(t.Context(), runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	executionProfile, err := fixture.store.GetRunExecutionProfile(t.Context(), runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	// This projection fixture starts the native manager directly. It tests live
	// output and durable bindings, not the application operation authorizer.
	snapshot, _, err := manager.Start(t.Context(), runner.CommandRuntimeStartRequest{
		Scope: runner.CommandRuntimeScope{InvocationID: "thread-activity-live-invocation",
			OperationKey: batchOperationKey, RunID: runRecord.ID,
			MissionID: runRecord.MissionID, RootAgentID: root.ID,
			AgentID: root.ID, AgentAttemptID: checkpoint.AttemptID,
			AttributionSource: domain.AgentAttributionRecorded,
			SessionID:         runRecord.SessionID, WorkspaceID: fixture.workspace.ID,
			WorkspaceRootSHA256: resolved.WorkspaceRootSHA256,
			ModeSnapshotID:      mode.ID, ModeRevision: mode.Revision,
			ProfileSnapshotID: executionProfile.ID, ProfileRevision: executionProfile.Revision,
			PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision,
			PermissionGeneration: generation, PermissionRuntimeEpoch: capabilities.RuntimeAuthority.RuntimeEpoch(),
			RunAuthorizationFence: fence,
			PermissionMode:        permission.Mode,
			LeaseID:               lease.LeaseID, LeaseGeneration: lease.Generation,
			LeaseOwnerID: lease.OwnerID, Adapter: adapter},
		Spec: resolved,
	})
	if errors.Is(err, runner.ErrCommandRuntimeUnavailable) {
		t.Skipf("platform Command Runtime is unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.commandActivitySource = manager
	thread, err := fixture.store.GetThreadByRun(t.Context(), runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/threads/" + thread.ID + "/activities/" + callID

	first := waitForThreadActivityCommand(t, fixture, root.ID, path, 4*time.Second,
		func(command ThreadActivityCommandDetailView) bool {
			return strings.Contains(command.StdoutPreview, "phase-one") &&
				!strings.Contains(command.StdoutPreview, "phase-two")
		})
	if first.Status != string(runner.CommandRuntimeJobRunning) {
		t.Fatalf("first live status=%q", first.Status)
	}
	if strings.Contains(first.StdoutPreview, environmentValue) {
		t.Fatalf("live preview exposed an environment value: %#v", first)
	}
	durableBeforeHeartbeat, err := fixture.store.GetCommandRuntimeJob(t.Context(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if durableBeforeHeartbeat.Stdout != "" || durableBeforeHeartbeat.OutputCursor != 0 {
		t.Fatalf("test did not observe pre-heartbeat state: stdout=%q cursor=%d",
			durableBeforeHeartbeat.Stdout, durableBeforeHeartbeat.OutputCursor)
	}

	second := waitForThreadActivityCommand(t, fixture, root.ID, path, 4*time.Second,
		func(command ThreadActivityCommandDetailView) bool {
			return strings.Contains(command.StdoutPreview, "phase-two") && command.Truncated
		})
	if strings.Contains(second.StdoutPreview, "phase-one") || !second.Truncated {
		t.Fatalf("bounded ring tail did not discard old output: %#v", second)
	}
	response := fixture.get(t, path)
	if response.Code != 200 || strings.Contains(response.Body.String(), "pid") ||
		strings.Contains(response.Body.String(), "environment_sha256") ||
		strings.Contains(response.Body.String(), "stdin") ||
		strings.Contains(response.Body.String(), environmentValue) {
		t.Fatalf("live public projection exposed a private runtime field: %s",
			response.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	var terminal runner.CommandRuntimeJobSnapshot
	for {
		terminal, _, err = manager.Wait(t.Context(), snapshot.ID, 50*time.Millisecond,
			0, runner.MaxCommandRuntimeOutputRead)
		if err != nil {
			t.Fatal(err)
		}
		if terminal.State.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real command did not reach a terminal state")
		}
	}
	durable, err := fixture.store.GetCommandRuntimeJob(t.Context(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if durable.PermissionSnapshotID != permission.ID || durable.PermissionRevision != permission.Revision ||
		durable.PermissionMode != domain.RunExecutionPermissionFull || durable.PermissionGeneration != generation ||
		durable.PermissionRuntimeEpoch != capabilities.RuntimeAuthority.RuntimeEpoch() || durable.RunAuthorizationFence != fence {
		t.Fatalf("durable command lost its exact current authority binding: %+v", durable)
	}
	if durable.OutputFramesJSON == "[]" || durable.OutputCursor == 0 {
		t.Fatalf("terminal durable ring was not persisted: %#v", durable)
	}
	for {
		_, _, live, tailErr := manager.ReadCommandRuntimeActivityTail(t.Context(),
			durable, runner.MaxCommandRuntimeOutputRead)
		if tailErr != nil {
			t.Fatal(tailErr)
		}
		if !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal Command Runtime entry was not released")
		}
		time.Sleep(10 * time.Millisecond)
	}
	third := waitForThreadActivityCommand(t, fixture, root.ID, path, time.Second,
		func(command ThreadActivityCommandDetailView) bool {
			return command.Status == string(runner.CommandRuntimeJobCompleted) &&
				strings.Contains(command.StdoutPreview, "phase-two") && command.Truncated
		})
	if strings.Contains(third.StdoutPreview, environmentValue) {
		t.Fatalf("durable tail exposed an environment value: %#v", third)
	}
	descriptors, err := fixture.store.CaptureToolOutput(t.Context(), artifact.CaptureRequest{
		RunID: durable.RunID, SessionID: durable.SessionID,
		WorkspaceID: durable.WorkspaceID, SourceID: durable.ID,
		ToolName: string(toolgateway.CommandRuntimeTool),
		Outputs: []artifact.Output{{Stream: artifact.StreamStdout,
			MIME: "text/plain; charset=utf-8", Content: durable.Stdout}},
	})
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("capture terminal output artifact: descriptors=%#v err=%v",
			descriptors, err)
	}
	detailResponse := fixture.get(t, path)
	var detailEnvelope struct {
		Data ThreadActivityDetailView `json:"data"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detailEnvelope); err != nil {
		t.Fatal(err)
	}
	refs := detailEnvelope.Data.Tools[0].Detail.Command.Commands[0].Artifacts
	if len(refs) != 1 || refs[0].ArtifactRef != descriptors[0].ID || refs[0].Truncated {
		t.Fatalf("safe artifact references=%#v", refs)
	}
	artifactResponse := fixture.get(t, path+"/artifacts/"+descriptors[0].ID)
	if artifactResponse.Code != 200 {
		t.Fatalf("activity artifact status=%d body=%s", artifactResponse.Code,
			artifactResponse.Body.String())
	}
	var artifactEnvelope struct {
		Data ThreadActivityArtifactView `json:"data"`
	}
	if err := json.Unmarshal(artifactResponse.Body.Bytes(), &artifactEnvelope); err != nil {
		t.Fatal(err)
	}
	projected := artifactEnvelope.Data
	if projected.Version != application.ThreadActivityArtifactProtocolVersion ||
		projected.ArtifactRef != descriptors[0].ID || !projected.Redacted ||
		projected.Truncated || !projected.Untrusted || projected.InstructionAuthorized ||
		!strings.Contains(projected.Content, "phase-one") ||
		!strings.Contains(projected.Content, "phase-two") ||
		strings.Contains(projected.Content, environmentValue) ||
		strings.Contains(artifactResponse.Body.String(), "payload_json") ||
		strings.Contains(artifactResponse.Body.String(), "intent_json") {
		t.Fatalf("unsafe activity artifact projection: %#v body=%s", projected,
			artifactResponse.Body.String())
	}
	otherThread, err := fixture.store.GetThreadByRun(t.Context(), fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, fixture.get(t, "/api/v1/threads/"+otherThread.ID+
		"/activities/"+callID+"/artifacts/"+descriptors[0].ID),
		http.StatusNotFound, "NOT_FOUND")
	assertAPIError(t, fixture.get(t, path+"/artifacts/"+descriptors[0].ID+"?raw=true"),
		http.StatusBadRequest, "INVALID_ARGUMENT")
}

func waitForThreadActivityCommand(t *testing.T, fixture *apiFixture, expectedAgentID,
	path string,
	timeout time.Duration, accept func(ThreadActivityCommandDetailView) bool,
) ThreadActivityCommandDetailView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		response := fixture.get(t, path)
		if response.Code != 200 {
			t.Fatalf("activity detail status=%d body=%s", response.Code,
				response.Body.String())
		}
		var envelope struct {
			Data ThreadActivityDetailView `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.Data.Tools) == 1 && envelope.Data.Tools[0].Detail.Command != nil &&
			len(envelope.Data.Tools[0].Detail.Command.Commands) == 1 {
			command := envelope.Data.Tools[0].Detail.Command.Commands[0]
			if envelope.Data.Tools[0].AgentID != expectedAgentID {
				t.Fatalf("Agent attribution=%q, want durable root %q",
					envelope.Data.Tools[0].AgentID, expectedAgentID)
			}
			if accept(command) {
				return command
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for live activity detail: %s", response.Body.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func newThreadActivityCommandRuntimeFixture(t *testing.T, fixture *apiFixture) (
	domain.Run, domain.AgentNode, domain.RunExecutionLease,
	domain.SupervisorCheckpoint, llm.ModelAttempt, domain.ExecutionPermissionRuntimeCapabilities,
) {
	t.Helper()
	ctx := t.Context()
	runs := application.NewRunService(fixture.store)
	_, runRecord, err := runs.Create(ctx, application.CreateRunRequest{
		Goal: "observe a real command through Thread activity", Profile: "review",
		WorkspaceID: fixture.workspace.ID,
		Budget:      domain.Budget{MaxTurns: 4, MaxTokens: 1000, MaxToolCalls: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := application.NewExternalSkillSelectionService(fixture.store).Select(ctx,
		application.SelectExternalSkillsRequest{RunID: runRecord.ID,
			PackageRefs:   []string{"api-projection-review@1.0.0"},
			SpecialistRef: "api-projection-review@1.0.0", TokenBudget: 1024,
			OperationKey: "thread-activity-live-selection-operation",
			RequestedBy:  "thread-activity-live-operator", ConfirmUntrustedContext: true})
	if err != nil {
		t.Fatal(err)
	}
	selection := selected.Selection
	if _, err := application.NewRunExecutionProfileService(fixture.store).Change(ctx,
		application.ChangeRunExecutionProfileRequest{RunID: runRecord.ID, Profile: "local",
			OperationKey: "thread-activity-live-profile-0001", RequestedBy: "test_operator",
			Reason: "exercise the host Command Runtime"}); err != nil {
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority(),
	}
	if _, err := application.NewRunExecutionPermissionService(fixture.store,
		capabilities).Change(ctx, application.ChangeRunExecutionPermissionRequest{
		RunID: runRecord.ID, Mode: string(domain.RunExecutionPermissionFull),
		OperationKey: "thread-activity-live-permission-0001", RequestedBy: "test_operator",
		Reason: "exercise the host Command Runtime", ConfirmFull: true,
	}); err != nil {
		t.Fatal(err)
	}
	runRecord, err = runs.Start(ctx, runRecord.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := fixture.store.GetRootAgent(ctx, runRecord.ID)
	if err != nil || !found {
		t.Fatalf("root agent found=%t err=%v", found, err)
	}
	acquired, err := fixture.store.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: runRecord.ID,
			OwnerID: "thread-activity-live-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := fixture.store.BeginSupervisorTurn(ctx, acquired.Lease,
		"private prompt must not enter command activity")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.PrepareExternalRootSkillContext(ctx, turn.Checkpoint,
		skills.ExternalRootContextPreparationRequest{RunID: runRecord.ID,
			MissionID: runRecord.MissionID, RootAgentID: root.ID,
			SupervisorAttemptID: turn.Checkpoint.AttemptID,
			Turn:                turn.Checkpoint.NextTurn, SelectionID: selection.ID,
			ProtocolVersion: skills.ExternalContextProtocolVersion,
			Surface:         selection.Surface, Profile: selection.Profile,
			SelectionFingerprint: selection.Fingerprint,
			ContextFingerprint: runmutation.Fingerprint("thread-activity-live-context",
				selection.Fingerprint),
			ItemCount: selection.ItemCount, TokenBudget: selection.TokenBudget,
			TokenUpperBound: selection.TokenUpperBound,
		}); err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 3,
		Provider: "thread-activity-live", Model: "test-model"}
	if inserted, err := fixture.store.RecordSupervisorModelStarted(ctx,
		turn.Checkpoint, attempt); err != nil || !inserted {
		t.Fatalf("record model start inserted=%t err=%v", inserted, err)
	}
	return runRecord, root, acquired.Lease, turn.Checkpoint, attempt, capabilities
}
