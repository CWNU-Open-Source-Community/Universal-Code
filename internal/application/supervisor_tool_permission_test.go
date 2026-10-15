package application

import (
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

func TestSupervisorWebSearchTimeoutOutlivesCredentialedProviderRequest(t *testing.T) {
	searchTimeout := supervisorToolExecutionTimeout(toolgateway.WebSearchTool)
	if searchTimeout <= webevidence.ProviderSearchRequestTimeout {
		t.Fatalf("web_search timeout=%s provider request timeout=%s",
			searchTimeout, webevidence.ProviderSearchRequestTimeout)
	}
	if timeout := supervisorToolExecutionTimeout(toolgateway.WebFetchTool); timeout != supervisorToolCallTimeout {
		t.Fatalf("web_fetch timeout=%s want=%s", timeout, supervisorToolCallTimeout)
	}
}

func TestSupervisorAcceptsChildTaskProposalInDeliverPhase(t *testing.T) {
	payload := json.RawMessage(`{"version":"child_task_proposal.v1","tasks":[{"title":"Inspect","goal":"Inspect the parser","skills":["model.chat","read_file"],"turn_limit":2,"token_limit":128,"timeout_millis":60000}]}`)
	calls := []llm.ToolCall{{ID: "provider-call-1", Name: string(toolgateway.ChildTaskProposeTool), Arguments: payload}}
	prepared, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionConservative, false, false)
	if err != nil || len(prepared) != 1 || prepared[0].Name != string(toolgateway.ChildTaskProposeTool) {
		t.Fatalf("child task proposal was rejected: %#v err=%v", prepared, err)
	}
}

func TestSupervisorAcceptsAdvertisedDockerSandboxProposal(t *testing.T) {
	payload := json.RawMessage(`{
  "version":"sandbox_docker_run_proposal.v1",
  "plan_id":"docker-plan-1",
  "manifest":{
    "protocol_version":"sandbox_manifest.v1",
    "backend":"docker",
    "command":{"executable":"/bin/echo","arguments":["ok"],"working_directory":"/workspace"},
    "mounts":[{"source":".","target":"/workspace","access":"read_write"}],
    "network":{"mode":"disabled"},
    "resources":{"cpu_quota_millis":1000,"memory_bytes":33554432,"pids":32,"max_output_bytes":4096},
    "output":{"capture_stdout":true,"capture_stderr":true},
    "timeout_seconds":30,
    "cancellation":{"grace_period_millis":1000}
  }
}`)
	calls := []llm.ToolCall{{ID: "provider-call-docker",
		Name: string(toolgateway.DockerSandboxRunProposeTool), Arguments: payload}}
	prepared, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionConservative, false, false)
	if err != nil || len(prepared) != 1 ||
		prepared[0].Name != string(toolgateway.DockerSandboxRunProposeTool) {
		t.Fatalf("advertised Docker Sandbox proposal was rejected: %#v err=%v", prepared, err)
	}
}

func TestSupervisorSkillCandidateToolRequiresExplicitGeneratorContext(t *testing.T) {
	found := func(enabled bool) bool {
		for _, spec := range supervisorStructuredToolSpecs(
			domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
			domain.RunExecutionPermissionConservative, enabled, false) {
			if spec.Name == string(toolgateway.SkillCandidateProposeTool) {
				return true
			}
		}
		return false
	}
	if found(false) || !found(true) {
		t.Fatal("Skill candidate tool exposure did not follow explicit generator context")
	}
	payload := json.RawMessage(`{"version":"skill_candidate_proposal.v1","name":"bounded-helper","skill_version":"1.0.0","description":"A reusable generated workflow.","profiles":["code"],"surfaces":["code"],"phases":["deliver"],"roles":["root"],"user_invocable":true,"model_invocable":false,"explicit_only":true,"tool_dependencies":["read_file"],"content":"# Bounded helper\n\nInspect and report verified facts.\n"}`)
	calls := []llm.ToolCall{{ID: "provider-call-candidate",
		Name: string(toolgateway.SkillCandidateProposeTool), Arguments: payload}}
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionConservative, false, false); err == nil {
		t.Fatal("forged Skill candidate proposal was accepted without generator context")
	}
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionConservative, true, false); err != nil {
		t.Fatalf("explicit generator Skill candidate proposal was rejected: %v", err)
	}
}

func TestSupervisorDebugTerminalRequiresDeliverFullAndRuntime(t *testing.T) {
	tests := []struct {
		name    string
		surface domain.ExecutionSurface
		phase   domain.ExecutionPhase
		mode    domain.RunExecutionPermissionMode
		enabled bool
		want    bool
	}{
		{name: "enabled", surface: domain.ExecutionSurfaceCode,
			phase: domain.ExecutionPhaseDeliver,
			mode:  domain.RunExecutionPermissionFull, enabled: true, want: true},
		{name: "no runtime", surface: domain.ExecutionSurfaceCode,
			phase: domain.ExecutionPhaseDeliver,
			mode:  domain.RunExecutionPermissionFull},
		{name: "Plan", surface: domain.ExecutionSurfaceCode,
			phase: domain.ExecutionPhasePlan,
			mode:  domain.RunExecutionPermissionFull, enabled: true},
		{name: "approval permission", surface: domain.ExecutionSurfaceCode,
			phase: domain.ExecutionPhaseDeliver,
			mode:  domain.RunExecutionPermissionApproval, enabled: true},
		{name: "Cyber surface", surface: domain.ExecutionSurfaceCyber,
			phase: domain.ExecutionPhaseDeliver,
			mode:  domain.RunExecutionPermissionFull, enabled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			found := false
			for _, spec := range supervisorStructuredToolSpecs(test.surface, test.phase,
				test.mode, false, test.enabled) {
				found = found || spec.Name == string(toolgateway.DebugTerminalTool)
			}
			if found != test.want {
				t.Fatalf("debug terminal visible=%t want=%t", found, test.want)
			}
		})
	}
	payload := json.RawMessage(`{"version":"debug_terminal.v1","action":"read","cursor":0,"max_bytes":4096,"wait_milliseconds":0}`)
	calls := []llm.ToolCall{{ID: "provider-call-debug", Name: string(toolgateway.DebugTerminalTool), Arguments: payload}}
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCyber, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionFull, false, true); err == nil {
		t.Fatal("forged Debug terminal call was accepted on the Cyber surface")
	}
	if recoverableSupervisorToolError(toolgateway.NoteCreateTool,
		apperror.CodeFailedPrecondition) {
		t.Fatal("Debug lease recovery semantics widened another Supervisor tool")
	}
	if !recoverableSupervisorToolError(toolgateway.DebugTerminalTool,
		apperror.CodeFailedPrecondition) {
		t.Fatal("missing Debug terminal lease was not model-recoverable")
	}
}

func TestSupervisorCommandRuntimeAdvertisesAndPreparesOnlyCurrentModes(t *testing.T) {
	for _, adapter := range []commandruntimeadapter.Identity{
		commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64)),
		commandruntimeadapter.SandboxedWorkspace(CommandRuntimeLocalSandboxBackend, "windows-local-sandbox.v1", strings.Repeat("b", 64)),
	} {
		for _, mode := range []domain.RunExecutionPermissionMode{
			domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull,
			domain.RunExecutionPermissionConservative, domain.RunExecutionPermissionWorkspaceAccess,
			domain.RunExecutionPermissionApproval, domain.RunExecutionPermissionFullAccess, domain.RunExecutionPermissionDebug,
		} {
			t.Run(string(adapter.Kind)+"/"+string(mode), func(t *testing.T) {
				authority := commandruntimeadapter.NewAuthority("run-1", adapter)
				if mode.IsApprovalMode() {
					authority.ProtocolVersion = commandruntimeadapter.OperationAuthorityVersion
					authority.PermissionMode, authority.PermissionRevision = mode, 1
					authority.PermissionSnapshotID = "permission-1"
					authority.PermissionRuntimeEpoch, authority.RunAuthorizationFence = "runtime-1", 1
					if mode == domain.RunExecutionPermissionFull {
						authority.PermissionGeneration = 1
					}
				}
				encoded, err := commandruntimeadapter.EncodeAuthority(authority)
				if err != nil {
					t.Fatal(err)
				}
				options := supervisorToolOptions{CommandRuntime: supervisorCommandRuntimeTools{Adapter: adapter, Authority: encoded}}
				for _, variant := range []string{"ready", "missing_runtime", "plan", "cyber"} {
					surface, phase, current := domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver, options
					switch variant {
					case "missing_runtime":
						current = supervisorToolOptions{}
					case "plan":
						phase = domain.ExecutionPhasePlan
					case "cyber":
						surface = domain.ExecutionSurfaceCyber
					}
					want := variant == "ready" && mode.IsApprovalMode()
					found := false
					for _, spec := range supervisorStructuredToolSpecs(surface, phase, mode, false, false, current) {
						found = found || spec.Name == string(toolgateway.CommandRuntimeTool)
					}
					calls := []llm.ToolCall{{ID: "provider-command", Name: string(toolgateway.CommandRuntimeTool), Arguments: json.RawMessage(`{"version":"command-runtime.v2","action":"list"}`)}}
					_, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1, surface, phase, mode, false, false, current)
					if found != want || (err == nil) != want {
						t.Fatalf("%s advertised=%t prepared=%t want=%t err=%v", variant, found, err == nil, want, err)
					}
				}
			})
		}
	}
	if !recoverableSupervisorToolError(toolgateway.CommandRuntimeTool, apperror.CodeFailedPrecondition) {
		t.Fatal("command lifecycle conflict is not recoverable")
	}
}

func TestSupervisorMCPRequiresReviewedSnapshotAndExactRuntimeScope(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	capabilities := mcp.ScopedCapabilities{ProtocolVersion: mcp.ClientProtocolVersion,
		Generation: strings.Repeat("b", 64), Servers: []mcp.ScopedServerCapability{{
			ServerID: "docs", Name: "Documentation", CapabilityFingerprint: fingerprint, DescriptorFingerprint: strings.Repeat("d", 64), RegistrationGeneration: 1,
			Tools: []mcp.RemoteTool{{Name: "lookup", Description: "Look up a document.",
				InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string"}}}`)}},
		}}}
	authority, err := mcp.EncodeSupervisorCallAuthority(mcp.SupervisorCallAuthority{
		Version:  mcp.SupervisorOperationAuthorityVersion,
		ServerID: "docs", DescriptorFingerprint: strings.Repeat("d", 64), ServerGeneration: 1,
		RunID: "run-1", MissionID: "mission-1", WorkspaceID: "workspace-1",
		PermissionSnapshotID: "permission-1", PermissionRevision: 1,
		PermissionMode: domain.RunExecutionPermissionFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	options := supervisorToolOptions{MCP: supervisorMCPTools{
		Capabilities: capabilities, Authority: authority}}
	visible := func(surface domain.ExecutionSurface, phase domain.ExecutionPhase,
		permission domain.RunExecutionPermissionMode, configured supervisorToolOptions,
	) (bool, json.RawMessage) {
		for _, spec := range supervisorStructuredToolSpecs(surface, phase, permission,
			false, false, configured) {
			if spec.Name == string(toolgateway.MCPToolCallTool) {
				return true, spec.Parameters
			}
		}
		return false, nil
	}
	found, schema := visible(domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionFull, options)
	if !found || !json.Valid(schema) || strings.Contains(string(schema), `"const":"lookup"`) {
		t.Fatalf("MCP invocation must expose a compact schema and discover operations on demand: %s", schema)
	}
	var schemaRoot struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(schema, &schemaRoot); err != nil || schemaRoot.Type != "object" {
		t.Fatalf("MCP input schema must declare an object at its root: %s", schema)
	}
	if found, _ := visible(domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionAuto, options); !found {
		t.Fatal("Auto did not expose the reviewed MCP capability")
	}
	for _, surface := range []domain.ExecutionSurface{domain.ExecutionSurfaceCode, domain.ExecutionSurfaceCyber} {
		for _, phase := range []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver} {
			for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull} {
				if found, _ := visible(surface, phase, mode, options); !found {
					t.Fatal("reviewed MCP was hidden behind a retired surface/phase/mode gate", surface, phase, mode)
				}
				if found, _ := visible(surface, phase, mode, supervisorToolOptions{}); found {
					t.Fatal("unreviewed MCP capability was exposed")
				}
			}
		}
	}
	payload := json.RawMessage(`{"version":"mcp-client.v1","server_id":"docs","tool_name":"lookup","capability_fingerprint":"` + fingerprint + `","arguments":{"query":"bounded"}}`)
	calls := []llm.ToolCall{{ID: "provider-mcp", Name: string(toolgateway.MCPToolCallTool),
		Arguments: payload}}
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionFull, false, false, options); err != nil {
		t.Fatalf("exact reviewed MCP call was rejected: %v", err)
	}
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionAuto, false, false, options); err != nil {
		t.Fatalf("Auto did not expose the exact reviewed MCP call: %v", err)
	}
	forged := options
	forged.MCP.Capabilities.Servers = append([]mcp.ScopedServerCapability(nil), options.MCP.Capabilities.Servers...)
	forged.MCP.Capabilities.Servers[0].CapabilityFingerprint = strings.Repeat("c", 64)
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionFull, false, false, forged); err == nil {
		t.Fatal("stale MCP capability fingerprint was accepted")
	}
	wrongRunAuthority, err := mcp.EncodeSupervisorCallAuthority(mcp.SupervisorCallAuthority{
		Version:  mcp.SupervisorOperationAuthorityVersion,
		ServerID: "docs", DescriptorFingerprint: strings.Repeat("d", 64), ServerGeneration: 1,
		RunID: "run-other", MissionID: "mission-1", WorkspaceID: "workspace-1",
		PermissionSnapshotID: "permission-1", PermissionRevision: 1,
		PermissionMode: domain.RunExecutionPermissionFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongRun := options
	wrongRun.MCP.Authority = wrongRunAuthority
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionFull, false, false, wrongRun); err == nil {
		t.Fatal("MCP advertisement authority for another Run was accepted")
	}
	malformed := options
	malformed.MCP.Authority = json.RawMessage(`{"version":1}`)
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhaseDeliver,
		domain.RunExecutionPermissionFull, false, false, malformed); err == nil {
		t.Fatal("malformed MCP advertisement authority was accepted")
	}
	if !recoverableSupervisorToolError(toolgateway.MCPToolCallTool,
		apperror.CodeFailedPrecondition) {
		t.Fatal("MCP lifecycle conflict was not model-recoverable")
	}
}

func TestSupervisorCodeIntelRequiresPinnedSnapshotAuthorityAndCodeScope(t *testing.T) {
	generation := strings.Repeat("a", 64)
	fingerprint := strings.Repeat("b", 64)
	capabilities := toolgateway.CodeIntelCapabilitySnapshot{
		ProtocolVersion: toolgateway.CodeIntelProtocolVersion,
		Servers: []toolgateway.CodeIntelServerCapability{{
			ServerID: "gopls", ServerName: "gopls", Languages: []string{"go"},
			Generation: generation, CapabilityFingerprint: fingerprint,
			Tools: []toolgateway.ToolName{toolgateway.CodeWorkspaceSymbolsTool},
		}},
	}
	options := supervisorToolOptions{CodeIntel: supervisorCodeIntelTools{
		Capabilities: capabilities, Authority: json.RawMessage(`{"bound":true}`),
	}}
	find := func(surface domain.ExecutionSurface, phase domain.ExecutionPhase,
	) (bool, json.RawMessage) {
		for _, spec := range supervisorStructuredToolSpecs(surface, phase,
			domain.RunExecutionPermissionConservative, false, false, options) {
			if spec.Name == string(toolgateway.CodeWorkspaceSymbolsTool) {
				return true, spec.Parameters
			}
		}
		return false, nil
	}
	for _, phase := range []domain.ExecutionPhase{
		domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver,
	} {
		found, schema := find(domain.ExecutionSurfaceCode, phase)
		if !found || !json.Valid(schema) ||
			!strings.Contains(string(schema), `"const":"gopls"`) ||
			!strings.Contains(string(schema), `"const":"`+generation+`"`) ||
			!strings.Contains(string(schema), `"const":"`+fingerprint+`"`) {
			t.Fatalf("pinned Code Intel schema was not exposed in %s: %s", phase, schema)
		}
	}
	if found, _ := find(domain.ExecutionSurfaceCyber, domain.ExecutionPhasePlan); found {
		t.Fatal("Code Intel tool leaked onto the Cyber surface")
	}
	payload := json.RawMessage(`{"version":"code-intel-lsp.v1","server_id":"gopls","server_generation":"` +
		generation + `","capability_fingerprint":"` + fingerprint +
		`","query":"Manager","limit":20}`)
	calls := []llm.ToolCall{{ID: "provider-code-intel",
		Name: string(toolgateway.CodeWorkspaceSymbolsTool), Arguments: payload}}
	prepared, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhasePlan,
		domain.RunExecutionPermissionConservative, false, false, options)
	if err != nil || len(prepared) != 1 || len(prepared[0].Authority) == 0 {
		t.Fatalf("exact Code Intel call was rejected or lost authority: %#v, %v", prepared, err)
	}
	forged := options
	forged.CodeIntel.Capabilities.Servers[0].Generation = strings.Repeat("c", 64)
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhasePlan,
		domain.RunExecutionPermissionConservative, false, false, forged); err == nil {
		t.Fatal("stale Code Intel generation was accepted")
	}
	withoutAuthority := options
	withoutAuthority.CodeIntel.Authority = nil
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCode, domain.ExecutionPhasePlan,
		domain.RunExecutionPermissionConservative, false, false, withoutAuthority); err == nil {
		t.Fatal("Code Intel call without Go-issued authority was accepted")
	}
	if _, err := prepareSupervisorToolCalls(calls, "run-1", 1, 1,
		domain.ExecutionSurfaceCyber, domain.ExecutionPhasePlan,
		domain.RunExecutionPermissionConservative, false, false, options); err == nil {
		t.Fatal("Code Intel call was accepted on the Cyber surface")
	}
	for _, code := range []apperror.Code{apperror.CodeConflict,
		apperror.CodeFailedPrecondition, apperror.CodeUnavailable} {
		if !recoverableSupervisorToolError(toolgateway.CodeWorkspaceSymbolsTool, code) {
			t.Fatalf("Code Intel lifecycle error %s was not model-recoverable", code)
		}
	}
}

func TestSupervisorUnsupportedToolsAreNeitherAdvertisedNorAccepted(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull,
		domain.RunExecutionPermissionConservative, domain.RunExecutionPermissionApproval, domain.RunExecutionPermissionWorkspaceAccess, domain.RunExecutionPermissionFullAccess, domain.RunExecutionPermissionDebug} {
		for _, phase := range []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver} {
			for _, name := range []toolgateway.ToolName{
				"unknown_tool",
				toolgateway.ControlledCommandProposeTool,
				toolgateway.OneShotCommandProposeTool,
				toolgateway.HostCommandProposeTool,
				toolgateway.ReadFileTool,
				toolgateway.ListWorkspaceTool,
				toolgateway.ShellTool,
				toolgateway.ReplaceFileTool,
				toolgateway.ScriptProcessTool,
			} {
				for _, spec := range supervisorStructuredToolSpecs(domain.ExecutionSurfaceCode, phase, mode, false, false) {
					if spec.Name == string(name) {
						t.Fatalf("unsupported tool %s advertised in %s/%s", name, mode, phase)
					}
				}
				_, err := prepareSupervisorToolCalls([]llm.ToolCall{{ID: "unsupported-call", Name: string(name), Arguments: json.RawMessage(`{}`)}}, "run-unsupported", 1, 1,
					domain.ExecutionSurfaceCode, phase, mode, false, false)
				if err == nil || !strings.Contains(err.Error(), "provider requested unsupported supervisor tool") {
					t.Fatalf("unsupported tool %s prepare error = %v in %s/%s", name, err, mode, phase)
				}
			}
		}
	}
}
