package toolgateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
)

type commandRuntimeExecutorStub struct {
	calls int
	scope CommandRuntimeContext
	input CommandRuntimeInput
}

type commandRuntimePolicyStore struct {
	*trackedStructuredStore
	decisions []policy.DecisionRecord
}

func (s *commandRuntimePolicyStore) RecordPolicyDecision(_ context.Context,
	record policy.DecisionRecord,
) error {
	s.decisions = append(s.decisions, record)
	return nil
}

func (s *commandRuntimeExecutorStub) ExecuteCommandRuntime(_ context.Context,
	scope CommandRuntimeContext, input CommandRuntimeInput,
) (CommandRuntimeExecutionResult, error) {
	s.calls++
	s.scope = scope
	s.input = input
	exitCode := 0
	return CommandRuntimeExecutionResult{
		Backend: scope.Adapter.Backend, Adapter: scope.Adapter, Action: input.Action,
		Jobs: []runner.CommandRuntimeJobSnapshot{{
			ID: "command-job-1", State: runner.CommandRuntimeJobCompleted,
			Adapter: scope.Adapter,
			Profile: runner.CommandRuntimePowerShell, ExitCode: &exitCode,
			OutputCursor: 45, TreeReaped: true,
		}},
		Pages: []runner.CommandRuntimeOutputPage{{
			JobID: "command-job-1", BaseCursor: 0, NextCursor: 45, EndCursor: 45,
			State: runner.CommandRuntimeJobCompleted, ExitCode: &exitCode,
			Frames: []runner.CommandRuntimeFrame{{Cursor: 0, NextCursor: 45,
				Stream: runner.CommandRuntimeStdout, Timestamp: time.Now().UTC(),
				Text: "\x1b[31mok\x1b[0m token=secret-value-1234567890"}},
		}},
		Artifacts: []CommandRuntimeArtifactOutput{{
			JobID:  "command-job-1",
			Stdout: "\x1b[32martifact-ok\x1b[0m token=secret-value-1234567890\n",
		}},
	}, nil
}

func TestCommandRuntimePayloadIsExplicitStrictAndCanonical(t *testing.T) {
	valid := commandRuntimeValidPayload("Write-Output ok")
	input, canonical, err := normalizeCommandRuntimePayload(valid)
	if err != nil || input.Action != CommandRuntimeActionRun ||
		len(input.Commands) != 1 || !json.Valid(canonical) {
		t.Fatalf("valid command runtime payload failed: %s err=%v", canonical, err)
	}
	if !strings.Contains(string(canonical), `"environment":[]`) {
		t.Fatalf("canonical payload lost an explicit empty boundary: %s", canonical)
	}
	for _, payload := range []json.RawMessage{
		json.RawMessage(`{"version":"command-runtime.v2","action":"list","cursor":0}`),
		json.RawMessage(`{"version":"command-runtime.v2","action":"start","commands":null}`),
		json.RawMessage(`{"version":"command-runtime.v2","action":"write_stdin","job_id":"job-1","stdin":null,"close_stdin":false}`),
		json.RawMessage(`{"version":"command-runtime.v2","action":"run","failure_policy":"fail_fast","max_bytes":4096,"commands":[{"version":"command-runtime.v2","profile":"powershell","script":"Write-Output ok","working_directory":".","stdin_policy":"closed","close_initial_stdin":true,"timeout_milliseconds":1000,"output":{"inline_bytes":4096,"artifact_bytes":4096},"network":"disabled","credentials":"none","purpose":"test"}]}`),
		json.RawMessage(`{"version":"command-runtime.v2","action":"start","commands":[{"version":"command-runtime.v2","profile":"process","executable":"C:\\\\tool.exe","working_directory":".","environment":[],"stdin_policy":"closed","close_initial_stdin":true,"timeout_milliseconds":1000,"output":{"inline_bytes":4096,"artifact_bytes":4096},"network":"disabled","credentials":"none","purpose":"test"}]}`),
	} {
		if _, _, err := normalizeCommandRuntimePayload(payload); err == nil {
			t.Fatalf("incomplete or inapplicable payload was accepted: %s", payload)
		}
	}
	if _, _, err := normalizeCommandRuntimePayload(commandRuntimeValidPayload(
		"Write-Output token=secret-value-1234567890")); err == nil {
		t.Fatal("secret-bearing command reached the durable tool boundary")
	}
}

func TestCommandRuntimeGatewayUsesFencedScopeAndSanitizesUntrustedFrames(t *testing.T) {
	state := &commandRuntimePolicyStore{trackedStructuredStore: newTrackedStructuredStore()}
	executor := &commandRuntimeExecutorStub{}
	gateway := New(state, policy.NewDefaultChecker()).WithCommandRuntimeExecutor(executor)
	outcome, err := gateway.Invoke(t.Context(), commandRuntimeToolCall(
		commandRuntimeValidPayload("Write-Output ok")))
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || executor.scope.LeaseID != "lease-1" ||
		executor.scope.LeaseGeneration != 7 || executor.input.Action != CommandRuntimeActionRun ||
		executor.scope.MissionID != "mission-1" ||
		executor.scope.Surface != domain.ExecutionSurfaceCode ||
		executor.scope.Phase != domain.ExecutionPhaseDeliver ||
		executor.scope.Role != domain.AgentRoleRoot ||
		executor.scope.Profile != domain.ProfileCode ||
		executor.scope.PermissionMode != domain.RunExecutionPermissionFullAccess ||
		executor.scope.ModeRevision != 1 || executor.scope.PermissionRevision != 1 ||
		outcome.Result == nil || outcome.Result.Status != StatusCompleted ||
		outcome.Result.Metadata["owner"] != "host_unsandboxed" ||
		outcome.Result.Metadata["job_1_artifact_stdout_id"] == "" ||
		outcome.Result.Metadata["job_1_artifact_stdout_sha256"] == "" ||
		outcome.Result.Metadata["user_terminal_shared"] != "false" ||
		outcome.Result.Metadata["debug_terminal_shared"] != "false" ||
		outcome.Result.Metadata["sandbox_shared"] != "false" {
		t.Fatalf("unexpected command runtime outcome: %#v", outcome)
	}
	if len(state.decisions) != 1 || !state.decisions[0].Decision.Allowed ||
		state.decisions[0].Context != "tool_run.command_runtime.command_1" {
		t.Fatalf("exact command policy review was not audited: %#v", state.decisions)
	}
	if strings.Contains(outcome.Result.Stdout, "\x1b") ||
		strings.Contains(outcome.Result.Stdout, "secret-value") ||
		!strings.Contains(outcome.Result.Stdout, "ok") {
		t.Fatalf("untrusted output was not sanitized: %q", outcome.Result.Stdout)
	}
}

func TestCommandRuntimeGatewayRejectsNetworkIntentBeforeExecution(t *testing.T) {
	state := &commandRuntimePolicyStore{trackedStructuredStore: newTrackedStructuredStore()}
	executor := &commandRuntimeExecutorStub{}
	gateway := New(state, policy.NewDefaultChecker()).WithCommandRuntimeExecutor(executor)
	for _, script := range []string{
		"Invoke-WebRequest https://example.com", "git fetch origin", "curl http://example.com",
	} {
		outcome, err := gateway.Invoke(t.Context(), commandRuntimeToolCall(
			commandRuntimeValidPayload(script)))
		if err != nil || outcome.Result == nil || outcome.Result.Status != StatusDenied ||
			outcome.Decision.Allowed {
			t.Fatalf("network command %q was not denied: %#v err=%v", script, outcome, err)
		}
	}
	if executor.calls != 0 {
		t.Fatalf("network-denied requests reached executor %d times", executor.calls)
	}
	if len(state.decisions) != 3 {
		t.Fatalf("network denials were not audited per command: %#v", state.decisions)
	}
}

func TestCommandRuntimeHostNetworkRequiresFullHostAuthority(t *testing.T) {
	hostDefinition := CommandRuntimeDefinitionForAdapter(
		commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64)))
	if !strings.Contains(string(hostDefinition.InputSchema), `"network":{"const":"host"}`) ||
		!strings.Contains(hostDefinition.Description, "host network intent") {
		t.Fatalf("Full Access host network was not advertised: %+v", hostDefinition)
	}
	sandbox := commandruntimeadapter.SandboxedWorkspace(
		"local_windows_lpac", "local-windows-lpac.v1", strings.Repeat("b", 64))
	sandboxDefinition := CommandRuntimeDefinitionForAdapter(sandbox)
	if !strings.Contains(string(sandboxDefinition.InputSchema), `"network":{"const":"disabled"}`) {
		t.Fatal("Workspace Sandbox network schema was widened")
	}
	var input CommandRuntimeInput
	if err := json.Unmarshal(commandRuntimeValidPayload("Invoke-WebRequest https://example.com"), &input); err != nil {
		t.Fatal(err)
	}
	input.Commands[0].Network = runner.CommandRuntimeNetworkHost
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	state := &commandRuntimePolicyStore{trackedStructuredStore: newTrackedStructuredStore()}
	executor := &commandRuntimeExecutorStub{}
	gateway := New(state, policy.NewDefaultChecker()).WithCommandRuntimeExecutor(executor)
	fullCall := commandRuntimeToolCall(payload)
	outcome, err := gateway.Invoke(t.Context(), fullCall)
	if err != nil || outcome.Result == nil || outcome.Result.Status != StatusCompleted ||
		executor.calls != 1 || executor.input.Commands[0].Network != runner.CommandRuntimeNetworkHost {
		t.Fatalf("Full Access host network outcome=%+v calls=%d err=%v", outcome, executor.calls, err)
	}
	workspaceCall := fullCall
	workspaceCall.OperationKey = "workspace-host-network-denied"
	workspaceCall.PermissionMode = domain.RunExecutionPermissionWorkspaceAccess
	workspaceCall.CommandRuntimeAdapter = sandbox
	workspaceCall.CapabilityGeneration = sandbox.Generation
	outcome, err = gateway.Invoke(t.Context(), workspaceCall)
	if err != nil || outcome.Result == nil || outcome.Result.Status != StatusDenied ||
		executor.calls != 1 {
		t.Fatalf("Workspace Access widened to host network: outcome=%+v calls=%d err=%v",
			outcome, executor.calls, err)
	}
	input.Commands[0].Script = "masscan 0.0.0.0/0"
	unsafePayload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	unsafeCall := fullCall
	unsafeCall.OperationKey = "full-host-network-sensitive-denied"
	unsafeCall.Payload = unsafePayload
	outcome, err = gateway.Invoke(t.Context(), unsafeCall)
	if err != nil || outcome.Result == nil || outcome.Result.Status != StatusDenied ||
		executor.calls != 1 {
		t.Fatalf("known sensitive host command bypassed policy: outcome=%+v calls=%d err=%v",
			outcome, executor.calls, err)
	}
}

func TestCommandRuntimeGatewayAuditsEveryCommandBeforeDenyingBatch(t *testing.T) {
	state := &commandRuntimePolicyStore{trackedStructuredStore: newTrackedStructuredStore()}
	executor := &commandRuntimeExecutorStub{}
	gateway := New(state, policy.NewDefaultChecker()).WithCommandRuntimeExecutor(executor)
	var input CommandRuntimeInput
	if err := json.Unmarshal(commandRuntimeValidPayload("Write-Output first"), &input); err != nil {
		t.Fatal(err)
	}
	second := input.Commands[0]
	second.Script = "Invoke-WebRequest https://example.com"
	second.Purpose = "network command that must be denied"
	third := input.Commands[0]
	third.Script = "Write-Output third"
	third.Purpose = "third command that must still be audited"
	input.Commands = append(input.Commands, second, third)
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := gateway.Invoke(t.Context(), commandRuntimeToolCall(payload))
	if err != nil || outcome.Result == nil || outcome.Result.Status != StatusDenied ||
		executor.calls != 0 {
		t.Fatalf("mixed-policy batch outcome=%#v calls=%d err=%v", outcome, executor.calls, err)
	}
	if len(state.decisions) != 3 || !state.decisions[0].Decision.Allowed ||
		state.decisions[1].Decision.Allowed || !state.decisions[2].Decision.Allowed {
		t.Fatalf("batch policy audit is incomplete: %#v", state.decisions)
	}
}

func TestCommandRuntimeWorkerContextRequiresItsOwnIntent(t *testing.T) {
	scope := CommandRuntimeContext{InvocationID: "charge-worker", OperationKey: "child-operation", RunID: "run-worker",
		MissionID: "mission-worker", RootAgentID: "root-worker", AgentID: "child-worker", AgentAttemptID: "child-attempt",
		SessionID: "child-session", WorkspaceID: "workspace-worker", LeaseID: "lease-worker", LeaseGeneration: 1,
		RequestedBy: "specialist_runner", SpecialistToolCallID: "provider-call", Surface: domain.ExecutionSurfaceCode,
		Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleSpecialist, Profile: domain.ProfileCode,
		ModeRevision: 1, PermissionRevision: 1, PermissionMode: domain.RunExecutionPermissionAuto, PermissionSnapshotID: "permission-worker",
		Adapter: commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64)), CapabilityGeneration: strings.Repeat("a", 64),
		PolicyDecision: Decision{Allowed: true, Approval: ApprovalAutomatic, Risk: "low", Reason: "recorded child intent"}}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	if actor := scope.Attribution(); actor.Source != domain.AgentAttributionRecorded || actor.AgentID != scope.AgentID || actor.AgentAttemptID != scope.AgentAttemptID {
		t.Fatalf("child attribution changed: %+v", actor)
	}
	for name, mutate := range map[string]func(*CommandRuntimeContext){
		"missing recorded child call": func(c *CommandRuntimeContext) { c.SpecialistToolCallID = "" },
		"root masquerade":             func(c *CommandRuntimeContext) { c.AgentID = c.RootAgentID },
		"wrong actor role":            func(c *CommandRuntimeContext) { c.Role = domain.AgentRoleRoot },
		"supervisor source":           func(c *CommandRuntimeContext) { c.SupervisorToolCallID = "root-call" },
		"root dispatcher":             func(c *CommandRuntimeContext) { c.RequestedBy = "run_supervisor" },
		"missing child attempt":       func(c *CommandRuntimeContext) { c.AgentAttemptID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := scope
			mutate(&invalid)
			if invalid.Validate() == nil {
				t.Fatal("partial or relabelled worker authority was accepted")
			}
		})
	}
}

func commandRuntimeValidPayload(script string) json.RawMessage {
	payload, _ := json.Marshal(CommandRuntimeInput{
		Version: CommandRuntimeToolProtocolVersion, Action: CommandRuntimeActionRun,
		FailurePolicy: CommandRuntimeFailFast, MaxBytes: commandRuntimePointer(4096),
		Commands: []runner.CommandRuntimeSpec{{
			Version: runner.CommandRuntimeProtocolVersion,
			Profile: runner.CommandRuntimePowerShell, Script: script,
			WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
			StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true,
			TimeoutMilliseconds: 1000,
			Output:              runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
			Network:             runner.CommandRuntimeNetworkDisabled,
			Credentials:         runner.CommandRuntimeCredentialsNone, Purpose: "test command",
		}},
	})
	return payload
}

func commandRuntimeToolCall(payload json.RawMessage) ToolCall {
	adapter := commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64))
	return ToolCall{Name: CommandRuntimeTool, Payload: payload,
		OperationKey: "command-runtime-operation-0001", RunID: "run-1",
		MissionID: "mission-1", AgentID: "agent-root-1",
		AgentAttemptID: "attempt-root-1",
		SessionID:      "session-1", WorkspaceID: "workspace-1",
		Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver,
		Role: domain.AgentRoleRoot, Profile: domain.ProfileCode,
		PermissionMode: domain.RunExecutionPermissionFullAccess,
		ModeRevision:   1, PermissionRevision: 1,
		CapabilityGeneration: adapter.Generation, CommandRuntimeAdapter: adapter,
		LeaseID: "lease-1", LeaseGeneration: 7, RequestedBy: "run_supervisor"}
}
