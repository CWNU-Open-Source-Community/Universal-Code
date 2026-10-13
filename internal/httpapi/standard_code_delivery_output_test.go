package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"reflect"
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
	"cyberagent-workbench/internal/standardcodedelivery"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func TestStandardCodeDeliveryOutputChild(t *testing.T) {
	if os.Getenv("TRAVERSE_REPORT_OUTPUT_CHILD") != "1" {
		return
	}
	fmt.Println("saved-stdout " + os.Getenv("REPORT_PRIVATE_VALUE"))
	fmt.Fprintln(os.Stderr, "saved-stderr")
	os.Exit(0)
}

type deliveryOutputController struct{ report standardcodedelivery.Report }

func (s *deliveryOutputController) Current(context.Context, string) (standardcodedelivery.Report, bool, error) {
	return s.report, true, nil
}
func (s *deliveryOutputController) Record(context.Context, application.StandardCodeDeliveryRecordRequest) (application.StandardCodeDeliveryRecordResult, error) {
	return application.StandardCodeDeliveryRecordResult{Report: s.report, Replayed: true}, nil
}

type outputProjectionCountingStore struct {
	*store.SQLiteStore
	bodyReads, fullJobReads int
	changeJob               func(*runner.CommandRuntimeJobMetadata)
}

func (s *outputProjectionCountingStore) GetRunArtifact(ctx context.Context, id string) (artifact.Blob, error) {
	s.bodyReads++
	return s.SQLiteStore.GetRunArtifact(ctx, id)
}
func (s *outputProjectionCountingStore) GetThreadCommandRuntimeJob(ctx context.Context, threadID, jobID string) (runner.CommandRuntimeJob, error) {
	s.fullJobReads++
	return s.SQLiteStore.GetThreadCommandRuntimeJob(ctx, threadID, jobID)
}
func (s *outputProjectionCountingStore) GetThreadCommandRuntimeJobMetadata(ctx context.Context, threadID, jobID string) (runner.CommandRuntimeJobMetadata, error) {
	job, err := s.SQLiteStore.GetThreadCommandRuntimeJobMetadata(ctx, threadID, jobID)
	if s.changeJob != nil {
		s.changeJob(&job)
	}
	return job, err
}

// The child is an actual host process, and the Job/artifacts/call are committed
// through production SQLite APIs. The report controller supplies the immutable
// report metadata to isolate this HTTP sidecar; this is not a Standard Code
// sandbox or end-to-end verification-gate test.
func TestStandardCodeDeliveryOutputSourcesUseDurableActivityWithoutReadingBodies(t *testing.T) {
	fixture := newAPIFixture(t)
	run, root, lease, checkpoint, attempt, capabilities := newThreadActivityCommandRuntimeFixture(t, fixture)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const privateValue = "private-output-value-not-for-public-view"
	maxBytes := runner.MinCommandRuntimeOutputRead
	input := toolgateway.CommandRuntimeInput{
		Version: toolgateway.CommandRuntimeToolProtocolVersion, Action: toolgateway.CommandRuntimeActionRun,
		FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes,
		Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion,
			Profile: runner.CommandRuntimeProcess, Executable: executable,
			Arguments: []string{"-test.run=^TestStandardCodeDeliveryOutputChild$"}, WorkingDirectory: ".",
			Environment: []runner.CommandRuntimeEnvironment{{Name: "TRAVERSE_REPORT_OUTPUT_CHILD", Value: "1"},
				{Name: "REPORT_PRIVATE_VALUE", Value: privateValue}},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 5000, Output: runner.CommandRuntimeOutputPolicy{
				InlineBytes: runner.MinCommandRuntimeInlineBytes, ArtifactBytes: runner.MinCommandRuntimeInlineBytes},
			Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone,
			Purpose: "exercise saved output provenance"}},
	}
	raw := mustActivityJSON(t, input)
	canonical, err := toolgateway.NormalizeSupervisorToolPayload(toolgateway.CommandRuntimeTool, raw)
	if err != nil {
		t.Fatal(err)
	}
	intentKey := runmutation.SupervisorToolOperationKey(run.ID, checkpoint.NextTurn,
		string(toolgateway.CommandRuntimeTool), string(canonical))
	callID, err := runmutation.SupervisorToolCallID(intentKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	operationKey := runmutation.SupervisorToolExecutionKey(run.ID, checkpoint.NextTurn,
		string(toolgateway.CommandRuntimeTool), string(canonical), callID)
	manager, err := runner.NewPlatformCommandRuntimeManager(fixture.store, "report-output-owner")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	adapter, _ := manager.AdapterIdentity()
	permission, err := fixture.store.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	generation, live := capabilities.FullAccessGeneration(permission)
	if !live || generation == 0 {
		t.Fatal("fixture has no current Full grant")
	}
	fence, err := capabilities.RuntimeAuthority.IssueRunAuthorizationFence(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := commandruntimeadapter.EncodeAuthority(commandruntimeadapter.Authority{
		ProtocolVersion: commandruntimeadapter.OperationAuthorityVersion, RunID: run.ID, Adapter: adapter,
		PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionMode: permission.Mode,
		PermissionGeneration: generation, PermissionRuntimeEpoch: capabilities.RuntimeAuthority.RuntimeEpoch(),
		RunAuthorizationFence: fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	checkpoint, err = fixture.store.RecordSupervisorModelCompleted(t.Context(), checkpoint, attempt,
		llm.ChatResponse{Provider: attempt.Provider, Model: attempt.Model,
			Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			ToolCalls: []llm.ToolCall{{ID: callID, Name: string(toolgateway.CommandRuntimeTool),
				Arguments: raw, Authority: authority}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.RecordSupervisorToolExecutionStarted(t.Context(), checkpoint, callID); err != nil {
		t.Fatal(err)
	}
	resolved, err := runner.NormalizeCommandRuntimeSpec(input.Commands[0], fixture.workspace.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	batchDigest := sha256.Sum256([]byte(fmt.Sprintf("command-runtime-batch.v2:%d:%s", 0, operationKey)))
	mode, err := fixture.store.GetRunMode(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := fixture.store.GetRunExecutionProfile(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := manager.Start(t.Context(), runner.CommandRuntimeStartRequest{
		Scope: runner.CommandRuntimeScope{InvocationID: "report-output-invocation",
			OperationKey: "command-runtime-" + hex.EncodeToString(batchDigest[:]),
			RunID:        run.ID, MissionID: run.MissionID, RootAgentID: root.ID,
			AgentID: root.ID, AgentAttemptID: checkpoint.AttemptID, AttributionSource: domain.AgentAttributionRecorded,
			SessionID: run.SessionID, WorkspaceID: fixture.workspace.ID,
			WorkspaceRootSHA256: resolved.WorkspaceRootSHA256, ModeSnapshotID: mode.ID, ModeRevision: mode.Revision,
			ProfileSnapshotID: profile.ID, ProfileRevision: profile.Revision,
			PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionMode: permission.Mode,
			PermissionGeneration: generation, PermissionRuntimeEpoch: capabilities.RuntimeAuthority.RuntimeEpoch(),
			RunAuthorizationFence: fence,
			LeaseID:               lease.LeaseID, LeaseGeneration: lease.Generation, LeaseOwnerID: lease.OwnerID, Adapter: adapter}, Spec: resolved})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !snapshot.State.Terminal() {
		snapshot, _, err = manager.Wait(t.Context(), snapshot.ID, 50*time.Millisecond, 0, runner.MaxCommandRuntimeOutputRead)
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("output child did not finish")
		}
	}
	job, err := fixture.store.GetCommandRuntimeJob(t.Context(), snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.PermissionSnapshotID != permission.ID || job.PermissionRevision != permission.Revision ||
		job.PermissionMode != domain.RunExecutionPermissionFull || job.PermissionGeneration != generation ||
		job.PermissionRuntimeEpoch != capabilities.RuntimeAuthority.RuntimeEpoch() || job.RunAuthorizationFence != fence {
		t.Fatalf("saved output lost its exact current authority binding: %+v", job)
	}
	if job.ExitCode == nil || *job.ExitCode != 0 || !strings.Contains(job.Stdout, "saved-stdout") ||
		!strings.Contains(job.Stderr, "saved-stderr") {
		t.Fatalf("actual child: %#v", job)
	}
	descriptors, err := fixture.store.CaptureToolOutput(t.Context(), artifact.CaptureRequest{
		RunID: run.ID, SessionID: run.SessionID, WorkspaceID: fixture.workspace.ID, SourceID: job.ID,
		ToolName: "command_runtime", Outputs: []artifact.Output{
			{Stream: artifact.StreamStdout, MIME: "text/plain; charset=utf-8", Content: job.Stdout},
			{Stream: artifact.StreamStderr, MIME: "text/plain; charset=utf-8", Content: job.Stderr}}})
	if err != nil || len(descriptors) != 2 {
		t.Fatalf("capture %v %v", descriptors, err)
	}
	report := standardcodedelivery.Report{ID: "report-output-projection", ProtocolVersion: standardcodedelivery.ProtocolVersion,
		ReceiptSHA256: strings.Repeat("a", 64), Status: standardcodedelivery.StatusStale,
		ReceiptStatus: standardcodedelivery.StatusPassed, Binding: standardcodedelivery.Binding{
			RunID: run.ID, SessionID: run.SessionID, MissionID: run.MissionID, SourceWorkspaceID: fixture.workspace.ID},
		Verifications: []standardcodedelivery.Verification{{JobID: job.ID}}}
	for _, d := range descriptors {
		report.Verifications[0].Artifacts = append(report.Verifications[0].Artifacts, standardcodedelivery.Artifact{
			ID: d.ID, Stream: string(d.Stream), SHA256: d.SHA256, SizeBytes: d.SizeBytes, Redacted: d.Redacted,
			URL: "/api/v1/artifacts/" + d.ID})
	}
	controller := &deliveryOutputController{report: report}
	fixture.api.standardCodeDeliveryController = controller
	counter := &outputProjectionCountingStore{SQLiteStore: fixture.store}
	fixture.api.store = counter
	reportPath := "/api/v1/runs/" + run.ID + "/standard-code-delivery"
	var current StandardCodeDeliveryReportView
	current = StandardCodeDeliveryReportView{}
	decodeDataStatus(t, fixture.get(t, reportPath), 200, &current)
	assertDeliveryOutputSources(t, current, "metadata_only", "activity_source_unavailable")
	// Only now seal the actual tool result that references this actual Job.
	stdout := mustActivityJSONString(t, map[string]any{"version": runner.CommandRuntimeResultVersion,
		"jobs": []map[string]string{{"id": job.ID}}})
	if _, _, err := fixture.store.RecordSupervisorToolResult(t.Context(), checkpoint, domain.SupervisorToolResult{
		CallID: callID, Status: domain.SupervisorToolCompleted,
		ResultJSON:  mustActivityResultEnvelope(t, toolgateway.CommandRuntimeTool, nil, stdout),
		CompletedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	current = StandardCodeDeliveryReportView{}
	decodeDataStatus(t, fixture.get(t, reportPath), 200, &current)
	assertDeliveryOutputSources(t, current, "available", "")
	if !reflect.DeepEqual(current.Report, report) {
		t.Fatal("HTTP source projection rewrote report receipt")
	}
	var recorded StandardCodeDeliveryRecordResultView
	decodeDataStatus(t, performControlMethodPathRequest(t, fixture.api, http.MethodPost, reportPath,
		"report-output-post-0001", strings.NewReader(`{"operation_key":"original-report-key"}`)), 200, &recorded)
	if !recorded.Replayed || !reflect.DeepEqual(recorded.Report, current) {
		t.Fatal("GET and replayed POST disagree")
	}
	if counter.bodyReads != 0 || counter.fullJobReads != 0 {
		t.Fatalf("report pre-read output bodies: blobs=%d full Jobs=%d", counter.bodyReads, counter.fullJobReads)
	}
	thread, err := fixture.store.GetThreadByRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range current.OutputSources {
		if source.ThreadID != thread.ID || source.ActivityRef != callID {
			t.Fatalf("guessed source: %#v", source)
		}
		var body ThreadActivityArtifactView
		decodeDataStatus(t, fixture.get(t, "/api/v1/threads/"+source.ThreadID+"/activities/"+
			source.ActivityRef+"/artifacts/"+source.ArtifactID), 200, &body)
		if !body.Untrusted || body.InstructionAuthorized || strings.Contains(body.Content, privateValue) ||
			!strings.Contains(body.Content, "saved-") {
			t.Fatalf("unsafe saved output: %#v", body)
		}
	}
	if counter.bodyReads != 2 {
		t.Fatalf("explicit body opens=%d", counter.bodyReads)
	}
	_, otherRun, err := application.NewRunService(fixture.store).Create(t.Context(), application.CreateRunRequest{
		Goal: "other output Thread", WorkspaceID: fixture.workspace.ID, Profile: "review", Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	otherThread, err := fixture.store.GetThreadByRun(t.Context(), otherRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, fixture.get(t, "/api/v1/threads/"+otherThread.ID+"/activities/"+callID+"/artifacts/"+descriptors[0].ID), 404, "NOT_FOUND")
	for _, test := range []struct {
		name   string
		change func(*runner.CommandRuntimeJobMetadata)
	}{
		{"running", func(j *runner.CommandRuntimeJobMetadata) { j.State = runner.CommandRuntimeJobRunning }},
		{"pipe", func(j *runner.CommandRuntimeJobMetadata) { j.StdinPolicy = runner.CommandRuntimeStdinPipe }},
		{"written stdin", func(j *runner.CommandRuntimeJobMetadata) { j.StdinWriteCount = 1 }},
		{"credentials", func(j *runner.CommandRuntimeJobMetadata) { j.Credentials = "configured" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			counter.changeJob = test.change
			current = StandardCodeDeliveryReportView{}
			decodeDataStatus(t, fixture.get(t, reportPath), 200, &current)
			assertDeliveryOutputSources(t, current, "metadata_only", "output_not_public")
		})
	}
	counter.changeJob = nil
	controller.report.Verifications[0].Artifacts[0].SHA256 = strings.Repeat("b", 64)
	current = StandardCodeDeliveryReportView{}
	decodeDataStatus(t, fixture.get(t, reportPath), 200, &current)
	if current.OutputSources[0].Status != "metadata_only" || current.OutputSources[0].Reason != "artifact_binding_mismatch" {
		t.Fatalf("mismatched sealed artifact accepted: %#v", current.OutputSources)
	}
	controller.report.Verifications[0].JobID = "command-job-unrelated"
	current = StandardCodeDeliveryReportView{}
	decodeDataStatus(t, fixture.get(t, reportPath), 200, &current)
	assertDeliveryOutputSources(t, current, "metadata_only", "activity_source_unavailable")
}

func assertDeliveryOutputSources(t *testing.T, report StandardCodeDeliveryReportView, status, reason string) {
	t.Helper()
	if len(report.OutputSources) != 2 {
		t.Fatalf("incomplete source projection: %#v", report.OutputSources)
	}
	for _, source := range report.OutputSources {
		if source.Status != status || source.Reason != reason ||
			(status == "metadata_only" && (source.ThreadID != "" || source.ActivityRef != "")) {
			t.Fatalf("source=%#v, want %s / %s", source, status, reason)
		}
	}
}
