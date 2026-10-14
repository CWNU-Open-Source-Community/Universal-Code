package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

type threadActivityDetailStoreStub struct {
	threadID         string
	call             domain.SupervisorToolCall
	job              runner.CommandRuntimeJob
	jobs             map[string]runner.CommandRuntimeJob
	agent            domain.AgentNode
	jobAttributions  map[string]domain.AgentAttribution
	fullJobLoads     *int
	metadataJobLoads *int
}

func (s threadActivityDetailStoreStub) GetThreadSupervisorToolCall(_ context.Context,
	threadID, callID string,
) (domain.SupervisorToolCall, error) {
	if threadID != s.threadID || callID != s.call.CallID {
		return domain.SupervisorToolCall{}, apperror.New(
			apperror.CodeNotFound, "Thread activity was not found")
	}
	call := s.call
	if call.AgentAttribution == "" {
		agent := s.activityAgent()
		call.AgentID = agent.ID
		call.AgentAttemptID = "attempt-activity"
		call.AgentAttribution = domain.AgentAttributionRecorded
	}
	return call, nil
}

func (s threadActivityDetailStoreStub) activityAgent() domain.AgentNode {
	if s.agent.ID != "" {
		return s.agent
	}
	return domain.AgentNode{ID: "agent-activity", RunID: s.call.RunID,
		SessionID: "session-activity", Role: domain.AgentRoleRoot}
}

func (s threadActivityDetailStoreStub) GetThreadCommandRuntimeJob(_ context.Context,
	threadID, jobID string,
) (runner.CommandRuntimeJob, error) {
	if s.fullJobLoads != nil {
		*s.fullJobLoads++
	}
	if threadID != s.threadID {
		return runner.CommandRuntimeJob{}, apperror.New(
			apperror.CodeNotFound, "Thread command activity was not found")
	}
	if job, found := s.jobs[jobID]; found {
		return job, nil
	}
	if jobID != s.job.ID {
		return runner.CommandRuntimeJob{}, apperror.New(
			apperror.CodeNotFound, "Thread command activity was not found")
	}
	return s.job, nil
}

func (s threadActivityDetailStoreStub) GetThreadCommandRuntimeJobMetadata(_ context.Context,
	threadID, jobID string,
) (runner.CommandRuntimeJobMetadata, error) {
	if s.metadataJobLoads != nil {
		*s.metadataJobLoads++
	}
	if threadID != s.threadID {
		return runner.CommandRuntimeJobMetadata{}, apperror.New(
			apperror.CodeNotFound, "Thread command activity was not found")
	}
	job, found := s.jobs[jobID]
	if !found && jobID == s.job.ID {
		job, found = s.job, true
	}
	if !found {
		return runner.CommandRuntimeJobMetadata{}, apperror.New(
			apperror.CodeNotFound, "Thread command activity was not found")
	}
	return runner.CommandRuntimeJobMetadata{ID: job.ID,
		OperationDigest: job.OperationDigest, RunID: job.RunID,
		WorkingDirectory: job.WorkingDirectory, State: job.State,
		ExitCode: cloneActivityInt(job.ExitCode), StartedAt: cloneActivityTime(job.StartedAt),
		CompletedAt: cloneActivityTime(job.CompletedAt), UpdatedAt: job.UpdatedAt}, nil
}

func (s threadActivityDetailStoreStub) GetThreadCommandRuntimeJobAgentAttribution(
	_ context.Context, threadID, jobID string,
) (domain.AgentAttribution, error) {
	if threadID != s.threadID {
		return domain.AgentAttribution{}, apperror.New(
			apperror.CodeNotFound, "Thread command activity was not found")
	}
	if value, found := s.jobAttributions[jobID]; found {
		return value, nil
	}
	agent := s.activityAgent()
	return domain.AgentAttribution{AgentID: agent.ID,
		AgentAttemptID: "attempt-activity", Source: domain.AgentAttributionRecorded}, nil
}

func (s threadActivityDetailStoreStub) GetRun(_ context.Context, id string) (domain.Run, error) {
	if id != s.call.RunID {
		return domain.Run{}, errors.New("unexpected Run")
	}
	return domain.Run{ID: id, MissionID: "mission-activity", SessionID: "session-activity"}, nil
}

func (s threadActivityDetailStoreStub) GetAgentNode(_ context.Context,
	agentID string,
) (domain.AgentNode, error) {
	agent := s.activityAgent()
	if agentID != agent.ID {
		return domain.AgentNode{}, apperror.New(
			apperror.CodeNotFound, "Agent was not found")
	}
	return agent, nil
}

func (s threadActivityDetailStoreStub) GetRootAgent(_ context.Context,
	runID string,
) (domain.AgentNode, bool, error) {
	if runID != s.call.RunID {
		return domain.AgentNode{}, false, errors.New("unexpected Run")
	}
	return domain.AgentNode{ID: "agent-activity", RunID: runID,
		SessionID: "session-activity", Role: domain.AgentRoleRoot}, true, nil
}

func (threadActivityDetailStoreStub) GetMission(_ context.Context,
	id string,
) (domain.Mission, error) {
	if id != "mission-activity" {
		return domain.Mission{}, errors.New("unexpected Mission")
	}
	return domain.Mission{ID: id, WorkspaceID: "workspace-activity"}, nil
}

func (threadActivityDetailStoreStub) GetWorkspaceInfo(_ context.Context,
	id string,
) (session.WorkspaceInfo, error) {
	if id != "workspace-activity" {
		return session.WorkspaceInfo{}, errors.New("unexpected Workspace")
	}
	return session.WorkspaceInfo{ID: id, RootPath: `D:\private\workspace`}, nil
}

func TestThreadActivityDetailProjectsBoundedRedactedCommandFacts(t *testing.T) {
	t.Parallel()
	maxBytes := runner.MinCommandRuntimeOutputRead
	input := toolgateway.CommandRuntimeInput{
		Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action:  toolgateway.CommandRuntimeActionRun,
		Commands: []runner.CommandRuntimeSpec{{
			Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimePowerShell,
			Script:           `Get-Content D:\private\workspace\src\a.txt; Get-Content C:\Users\alice\private.txt`,
			WorkingDirectory: "packages/core", Environment: []runner.CommandRuntimeEnvironment{{
				Name: "API_KEY", Value: "sk-secret-value-never-project"}, {
				Name: "VISIBLE_TEST", Value: "plain-environment-value"}},
			InitialStdin: "stdin-must-never-project", TimeoutMilliseconds: 1000,
			Network:     runner.CommandRuntimeNetworkDisabled,
			Credentials: runner.CommandRuntimeCredentialsNone,
		}},
		FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes,
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := json.Marshal(map[string]any{
		"version": runner.CommandRuntimeResultVersion,
		"jobs":    []map[string]string{{"id": "job-activity"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(map[string]any{
		"version": "supervisor_tool_result.v1",
		"tool":    string(toolgateway.CommandRuntimeTool),
		"stdout":  string(projection),
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	completed := started.Add(2400 * time.Millisecond)
	exitCode := 0
	store := threadActivityDetailStoreStub{threadID: "thread-activity",
		call: domain.SupervisorToolCall{RunID: "run-activity", CallID: "call-activity",
			ToolName: string(toolgateway.CommandRuntimeTool), PayloadJSON: string(payload),
			Status: domain.SupervisorToolCompleted, ResultJSON: string(result),
			CreatedAt: started, CompletedAt: &completed},
		job: runner.CommandRuntimeJob{ID: "job-activity", RunID: "run-activity",
			RootAgentID:      "agent-activity",
			StdinPolicy:      runner.CommandRuntimeStdinClosed,
			Adapter:          commandruntimeadapter.Identity{Kind: commandruntimeadapter.KindHostUnsandboxed},
			WorkingDirectory: "packages/core", Network: runner.CommandRuntimeNetworkDisabled,
			State: runner.CommandRuntimeJobCompleted, StartedAt: &started, CompletedAt: &completed,
			UpdatedAt: completed, ExitCode: &exitCode,
			Stdout:              `ok D:\private\workspace\src\a.txt C:\Users\alice\private.txt /home/alice/private path=/srv/private (file:///home/alice/uri-private) sk-secret-value-never-project plain-environment-value stdin-must-never-project`,
			Stderr:              `authorization: Bearer sk-secret-value-never-project`,
			StdoutObservedBytes: 4096, StderrObservedBytes: 4096,
			IntentJSON: `{"environment":[{"name":"API_KEY","value":"never-project"}],"initial_stdin":"never-project"}`},
	}

	detail, err := NewThreadActivityDetailService(store).Get(context.Background(),
		"thread-activity", "call-activity")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Version != ThreadActivityDetailProtocolVersion || len(detail.Tools) != 1 ||
		len(detail.Tools[0].Commands) != 1 {
		t.Fatalf("unexpected detail: %#v", detail)
	}
	command := detail.Tools[0].Commands[0]
	if command.WorkingDirectory != "packages/core" || command.ExitCode == nil ||
		*command.ExitCode != 0 || command.DurationMilliseconds != 2400 || !command.Truncated ||
		command.Network != "disabled" || command.ExecutionEnvironment != "Host · Full Access" {
		t.Fatalf("unexpected safe command projection: %#v", command)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	public := string(encoded)
	for _, forbidden := range []string{"API_KEY", "never-project", "stdin-must-never-project",
		`D:\\private\\workspace`, `C:\\Users`, "/home/alice", "/srv/private",
		"file:///", "sk-secret-value", "plain-environment-value"} {
		if strings.Contains(public, forbidden) {
			t.Fatalf("safe activity detail exposed %q: %s", forbidden, public)
		}
	}
}

func TestThreadActivityDetailBindsPendingForegroundInvocationAndProjectsLiveTail(t *testing.T) {
	t.Parallel()
	maxBytes := runner.MinCommandRuntimeOutputRead
	input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionRun,
		Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion,
			Profile: runner.CommandRuntimePowerShell, Script: "pnpm test session",
			WorkingDirectory: "packages/core", TimeoutMilliseconds: 10_000,
			Output: runner.CommandRuntimeOutputPolicy{InlineBytes: runner.MinCommandRuntimeInlineBytes,
				ArtifactBytes: runner.MinCommandRuntimeInlineBytes},
			Network:     runner.CommandRuntimeNetworkDisabled,
			Credentials: runner.CommandRuntimeCredentialsNone}},
		FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)
	call := domain.SupervisorToolCall{RunID: "run-live", Turn: 3,
		CallID: "call-live", ToolName: string(toolgateway.CommandRuntimeTool),
		PayloadJSON: string(payload), Status: domain.SupervisorToolPending,
		CreatedAt: started}
	operationKey := runmutation.SupervisorToolExecutionKey(call.RunID, call.Turn, call.ToolName, call.PayloadJSON, call.CallID)
	digest, jobID := runner.CommandRuntimeOperationIdentity(call.RunID,
		commandRuntimeBatchOperationKey(operationKey, 0))
	updated := started.Add(1500 * time.Millisecond)
	job := runner.CommandRuntimeJob{ID: jobID, RunID: call.RunID,
		RootAgentID:     "agent-activity",
		StdinPolicy:     runner.CommandRuntimeStdinClosed,
		OperationDigest: digest, State: runner.CommandRuntimeJobRunning,
		Adapter:          commandruntimeadapter.Identity{Kind: commandruntimeadapter.KindSandboxedWorkspace},
		WorkingDirectory: "packages/core", Network: runner.CommandRuntimeNetworkDisabled,
		StartedAt: &started, UpdatedAt: updated, Stdout: "✓ 12 tests completed so far",
		StdoutObservedBytes: 27}
	fullLoads, metadataLoads := 0, 0
	store := threadActivityDetailStoreStub{threadID: "thread-live", call: call,
		jobs:         map[string]runner.CommandRuntimeJob{jobID: job},
		fullJobLoads: &fullLoads, metadataJobLoads: &metadataLoads}

	detail, err := NewThreadActivityDetailService(store).Get(context.Background(),
		"thread-live", call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	command := detail.Tools[0].Commands[0]
	if command.Status != string(runner.CommandRuntimeJobRunning) ||
		command.StdoutPreview != job.Stdout || command.DurationMilliseconds != 1500 {
		t.Fatalf("pending live detail = %#v", command)
	}
	summary, err := NewThreadActivityDetailService(store).Summary(context.Background(),
		"thread-live", call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Version != ThreadActivitySummaryProtocolVersion ||
		summary.Command != "pnpm test session" ||
		summary.Status != string(runner.CommandRuntimeJobRunning) ||
		summary.DurationMilliseconds != 1500 || summary.CommandCount != 1 {
		t.Fatalf("pending live summary = %#v", summary)
	}
	if fullLoads != 1 || metadataLoads != 1 {
		t.Fatalf("detail/summary Job loads: full=%d metadata=%d", fullLoads, metadataLoads)
	}
}

func TestThreadActivitySummaryUsesCommandFailureInsteadOfCompletedToolEnvelope(t *testing.T) {
	t.Parallel()
	maxBytes := runner.MinCommandRuntimeOutputRead
	input := toolgateway.CommandRuntimeInput{Version: toolgateway.CommandRuntimeToolProtocolVersion,
		Action: toolgateway.CommandRuntimeActionRun,
		Commands: []runner.CommandRuntimeSpec{{Version: runner.CommandRuntimeProtocolVersion,
			Profile: runner.CommandRuntimeBash, Script: "pnpm test session",
			TimeoutMilliseconds: 1000,
			Output: runner.CommandRuntimeOutputPolicy{InlineBytes: runner.MinCommandRuntimeInlineBytes,
				ArtifactBytes: runner.MinCommandRuntimeInlineBytes},
			Network:     runner.CommandRuntimeNetworkDisabled,
			Credentials: runner.CommandRuntimeCredentialsNone}},
		FailurePolicy: toolgateway.CommandRuntimeFailFast, MaxBytes: &maxBytes}
	payload, _ := json.Marshal(input)
	resultProjection, _ := json.Marshal(map[string]any{"version": runner.CommandRuntimeResultVersion,
		"jobs": []map[string]string{{"id": "job-failed"}}})
	result, _ := json.Marshal(map[string]any{"version": "supervisor_tool_result.v1",
		"tool": string(toolgateway.CommandRuntimeTool), "stdout": string(resultProjection)})
	started := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	completed := started.Add(99 * time.Millisecond)
	exitCode := 2
	call := domain.SupervisorToolCall{RunID: "run-failed", CallID: "call-failed",
		ToolName: string(toolgateway.CommandRuntimeTool), PayloadJSON: string(payload),
		Status: domain.SupervisorToolCompleted, ResultJSON: string(result),
		CreatedAt: started, CompletedAt: &completed}
	store := threadActivityDetailStoreStub{threadID: "thread-failed", call: call,
		job: runner.CommandRuntimeJob{ID: "job-failed", RunID: call.RunID,
			RootAgentID: "agent-activity",
			StdinPolicy: runner.CommandRuntimeStdinClosed,
			State:       runner.CommandRuntimeJobFailed, ExitCode: &exitCode, StartedAt: &started,
			CompletedAt: &completed, UpdatedAt: completed,
			Adapter: commandruntimeadapter.Identity{Kind: commandruntimeadapter.KindSandboxedWorkspace},
			Network: runner.CommandRuntimeNetworkDisabled}}
	summary, err := NewThreadActivityDetailService(store).Summary(context.Background(),
		"thread-failed", call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != string(runner.CommandRuntimeJobFailed) ||
		summary.ExitCode == nil || *summary.ExitCode != 2 ||
		summary.DurationMilliseconds != 99 {
		t.Fatalf("failed command summary = %#v", summary)
	}
}

func TestThreadActivityDetailReferenceCannotCrossThread(t *testing.T) {
	t.Parallel()
	store := threadActivityDetailStoreStub{threadID: "thread-owner",
		call: domain.SupervisorToolCall{CallID: "call-owner"}}
	_, err := NewThreadActivityDetailService(store).Get(context.Background(),
		"thread-other", "call-owner")
	if apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("cross-Thread reference error = %v, want not_found", err)
	}
}

func TestThreadActivityDetailProjectsTypedNonCommandFactsWithoutRawPayload(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(toolgateway.WebFetchPayload{
		Version: "web_fetch.v1",
		URL:     "https://docs.example.com/report?page=2",
	})
	if err != nil {
		t.Fatal(err)
	}
	call := threadActivityFactsCall(toolgateway.WebFetchTool, payload,
		map[string]string{"state": "citeable", "citeable": "true",
			"credential": "must-not-project"}, "private page body must not project")
	call.RunID = "run-web-detail"
	call.CallID = "call-web-detail"
	if _, found, projectionErr := ProjectThreadActivityToolFacts(call); projectionErr != nil || !found {
		t.Fatalf("direct typed projection: found=%t err=%v", found, projectionErr)
	}
	store := threadActivityDetailStoreStub{threadID: "thread-web-detail", call: call}

	detail, err := NewThreadActivityDetailService(store).Get(context.Background(),
		"thread-web-detail", call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Tools) != 1 || detail.Tools[0].Detail.WebFetch == nil ||
		len(detail.Tools[0].Commands) != 0 ||
		detail.Tools[0].Detail.Kind != "web_fetch" ||
		detail.Tools[0].Detail.WebFetch.URL != "https://docs.example.com/report?page=2" ||
		detail.Tools[0].AgentID != "agent-activity" {
		t.Fatalf("unexpected typed tool detail: %#v", detail)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"must-not-project", "private page body"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("typed tool detail exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestThreadActivityDetailProjectsRecordedSpecialistAgent(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(toolgateway.WebFetchPayload{
		Version: "web_fetch.v1", URL: "https://docs.example.com/report"})
	if err != nil {
		t.Fatal(err)
	}
	call := threadActivityFactsCall(toolgateway.WebFetchTool, payload,
		map[string]string{"state": "citeable", "citeable": "true"}, "")
	call.RunID = "run-specialist-detail"
	call.CallID = "call-specialist-detail"
	call.AgentID = "agent-specialist-detail"
	call.AgentAttemptID = "attempt-specialist-detail"
	call.AgentAttribution = domain.AgentAttributionRecorded
	agent := domain.AgentNode{ID: call.AgentID, RunID: call.RunID,
		ParentID: "agent-activity", SessionID: "session-specialist-detail",
		Role: domain.AgentRoleSpecialist}
	store := threadActivityDetailStoreStub{threadID: "thread-specialist-detail",
		call: call, agent: agent}

	detail, err := NewThreadActivityDetailService(store).Get(context.Background(),
		store.threadID, call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Tools) != 1 || detail.Tools[0].AgentID != agent.ID ||
		detail.Tools[0].AgentRole != string(domain.AgentRoleSpecialist) ||
		detail.Tools[0].AgentLabel != "Specialist Agent" {
		t.Fatalf("specialist attribution was not projected: %#v", detail)
	}
}

func TestThreadActivityDetailDoesNotFabricateLegacyUnknownAgent(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(toolgateway.WebFetchPayload{
		Version: "web_fetch.v1", URL: "https://docs.example.com/report"})
	if err != nil {
		t.Fatal(err)
	}
	call := threadActivityFactsCall(toolgateway.WebFetchTool, payload,
		map[string]string{"state": "citeable", "citeable": "true"}, "")
	call.RunID = "run-unknown-detail"
	call.CallID = "call-unknown-detail"
	call.AgentAttribution = domain.AgentAttributionLegacyUnknown
	store := threadActivityDetailStoreStub{threadID: "thread-unknown-detail", call: call}

	detail, err := NewThreadActivityDetailService(store).Get(context.Background(),
		store.threadID, call.CallID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Tools) != 1 || detail.Tools[0].AgentID != "unknown" ||
		detail.Tools[0].AgentRole != "unknown" ||
		detail.Tools[0].AgentLabel != "历史活动（执行者未知）" {
		t.Fatalf("legacy unknown attribution was fabricated: %#v", detail)
	}
}

func TestThreadActivityOperatorRuntimeCannotMasqueradeAsAgentAttempt(t *testing.T) {
	t.Parallel()
	call := domain.SupervisorToolCall{AgentID: "agent-root-activity",
		AgentAttemptID:   "attempt-root-activity",
		AgentAttribution: domain.AgentAttributionRecorded}
	operator := domain.AgentAttribution{AgentID: call.AgentID,
		Source: domain.AgentAttributionOperatorRoot}
	if threadActivityAgentAttributionMatches(call, operator, toolgateway.CommandRuntimeActionRun) {
		t.Fatal("operator-started Command Runtime Job matched a model Agent attempt")
	}
}

func TestThreadActivityCommandMutationKeepsExactAttemptAttribution(t *testing.T) {
	call := domain.SupervisorToolCall{AgentID: "agent-root-activity",
		AgentAttemptID: "attempt-current", AgentAttribution: domain.AgentAttributionRecorded}
	job := domain.AgentAttribution{AgentID: call.AgentID,
		AgentAttemptID: "attempt-started-job", Source: domain.AgentAttributionRecorded}
	for _, action := range []string{toolgateway.CommandRuntimeActionRun, toolgateway.CommandRuntimeActionStart,
		toolgateway.CommandRuntimeActionWriteStdin, toolgateway.CommandRuntimeActionCancel, toolgateway.CommandRuntimeActionKill} {
		if threadActivityAgentAttributionMatches(call, job, action) {
			t.Fatalf("%s accepted a different execution attempt", action)
		}
	}
}

func TestSafeRelativeActivityDirectoryKeepsOnlyWorkspaceDescendants(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, value, root, want string
	}{
		{name: "Windows child case insensitive", value: `D:\Private\Workspace\packages\core`,
			root: `d:\private\workspace`, want: "packages/core"},
		{name: "Windows sibling prefix", value: `D:\private\workspace-secret\core`,
			root: `D:\private\workspace`, want: "."},
		{name: "Unix child", value: "/srv/workspace/packages/core",
			root: "/srv/workspace", want: "packages/core"},
		{name: "Unix sibling prefix", value: "/srv/workspace-secret/core",
			root: "/srv/workspace", want: "."},
		{name: "relative child", value: "packages/core", root: "/srv/workspace",
			want: "packages/core"},
		{name: "relative traversal", value: "../private", root: "/srv/workspace", want: "."},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := safeRelativeActivityDirectory(test.value, test.root); got != test.want {
				t.Fatalf("safe relative directory = %q, want %q", got, test.want)
			}
		})
	}
}

func TestThreadActivityCommandHidesInteractiveOutputThatMayEchoStdin(t *testing.T) {
	t.Parallel()
	started := time.Now().UTC().Add(-time.Second)
	job := runner.CommandRuntimeJob{ID: "job-interactive", RunID: "run-interactive",
		RootAgentID: "agent-activity", StdinPolicy: runner.CommandRuntimeStdinPipe,
		StdinWriteCount: 1, State: runner.CommandRuntimeJobRunning,
		Adapter: commandruntimeadapter.Identity{Kind: commandruntimeadapter.KindHostUnsandboxed},
		Network: runner.CommandRuntimeNetworkDisabled, WorkingDirectory: ".",
		StartedAt: &started, UpdatedAt: time.Now().UTC(),
		Stdout: "ordinary stdin echoed by the child", Stderr: "echoed again"}
	projected := projectThreadActivityCommand(runner.CommandRuntimeSpec{
		Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimePowerShell,
		Script: "interactive command", WorkingDirectory: "."}, &job, nil,
		[]ThreadActivityArtifactReference{{ArtifactRef: "artifact-must-not-project",
			Stream: "stdout", MIME: "text/plain; charset=utf-8", SizeBytes: 1}},
		job.Adapter, `D:\private\workspace`, string(domain.SupervisorToolPending))
	if projected.StdoutPreview != "" || projected.StderrPreview != "" ||
		len(projected.Artifacts) != 0 {
		t.Fatalf("interactive output was publicly projected: %#v", projected)
	}
}
