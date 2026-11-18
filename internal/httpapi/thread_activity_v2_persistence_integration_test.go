package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/coordinator"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
	workspacepkg "cyberagent-workbench/internal/workspace"
)

// TestThreadActivityV2PersistsTypedDetailsAndRecordedAgent exercises the public
// activity endpoint across a real SQLite close/reopen boundary. The execution
// actor is deliberately a Specialist while each authority document remains
// rooted at the Run's root Agent. This prevents the public projection from
// silently substituting the authority anchor for the Agent that performed the
// work.
func TestThreadActivityV2PersistsTypedDetailsAndRecordedAgent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "thread-activity-v2.db")
	state, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = state.Close()
		}
	}()

	workspaceRoot := t.TempDir()
	diffCanary := "FILE_DIFF_VISIBLE_CANARY_94BE"
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "src", "session.ts"),
		[]byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceRecord := store.WorkspaceRecord{ID: "workspace-activity-v2",
		Name: "activity-v2", RootPath: workspaceRoot, CreatedAt: time.Now().UTC()}
	if err := state.SaveWorkspace(ctx, workspaceRecord); err != nil {
		t.Fatal(err)
	}
	runService := application.NewRunService(state)
	mission, created, err := runService.Create(ctx, application.CreateRunRequest{
		Goal: "persist typed activity details", Profile: "code", ModelRoute: "test",
		WorkspaceID: workspaceRecord.ID, NetworkMode: "allowlist",
		AllowedTargets: []string{"docs.example.com", "search.example.com"},
		Budget:         domain.Budget{MaxTurns: 8, MaxTokens: 256, MaxToolCalls: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true,
		DangerFullAccessEnabled: true,
		RuntimeAuthority:        domain.NewExecutionPermissionRuntimeAuthority()}
	permissionResult, err := application.NewRunExecutionPermissionService(state, capabilities).Change(ctx,
		application.ChangeRunExecutionPermissionRequest{RunID: created.ID,
			Mode:         string(domain.RunExecutionPermissionFull),
			OperationKey: "activity-v2-full-access-0001", RequestedBy: "activity-v2-test",
			Reason:      "exercise MCP and typed activity persistence",
			ConfirmFull: true})
	if err != nil {
		t.Fatal(err)
	}
	generation, live := capabilities.FullAccessGeneration(permissionResult.Permission)
	if !live || generation == 0 {
		t.Fatal("fixture has no current Full grant")
	}
	fence, err := capabilities.RuntimeAuthority.IssueRunAuthorizationFence(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := runService.Start(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, found, err := state.GetRootAgent(ctx, run.ID)
	if err != nil || !found {
		t.Fatalf("root Agent found=%t err=%v", found, err)
	}
	coord, err := coordinator.NewWithSpecialistAdmission(state,
		coordinator.SpecialistAdmissionPolicy{MaxChildren: 1, MaxTurnsPerChild: 2,
			MaxTokensPerChild: 32})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := coord.AdmitSpecialist(ctx, coordinator.AdmitSpecialistRequest{
		RunID: run.ID, ParentAgentID: root.ID, Title: "activity projection specialist",
		Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 32,
		IdempotencyKey: "activity-v2-specialist-admission-0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseResult, err := state.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: run.ID,
			OwnerID: "activity-v2-worker", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := state.BeginSupervisorTurn(ctx, leaseResult.Lease,
		"persist typed activity details")
	if err != nil {
		t.Fatal(err)
	}
	specialistAttempt, _, err := state.BeginSpecialistAttempt(ctx,
		domain.AgentAttemptStart{AttemptID: "attempt-activity-v2-specialist-0001",
			RunID: run.ID, AgentID: admitted.Agent.ID, ParentAgentID: root.ID,
			Lease: leaseResult.Lease, StartedAt: time.Now().UTC()},
		"activity-v2-specialist-attempt-0001")
	if err != nil {
		t.Fatal(err)
	}
	modelAttempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1,
		MaxAttempts: 3, Provider: "activity-v2-provider", Model: "test-model"}
	if inserted, err := state.RecordSupervisorModelStarted(ctx, turn.Checkpoint,
		modelAttempt); err != nil || !inserted {
		t.Fatalf("record model start inserted=%t err=%v", inserted, err)
	}

	mode, err := state.GetRunMode(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootFingerprint, err := workspacepkg.AgentCodeRootFingerprint(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	agentCodeAuthority, err := toolgateway.NewAgentCodeCallAuthority(
		toolgateway.AgentCodeCapabilityContext{RunID: run.ID, MissionID: mission.ID,
			RootAgentID: root.ID, WorkspaceID: workspaceRecord.ID,
			RootFingerprint: rootFingerprint, Surface: mode.Surface, Phase: mode.Phase,
			Role: root.Role, Profile: mode.Profile,
			PermissionMode:       permissionResult.Permission.Mode,
			PermissionSnapshotID: permissionResult.Permission.ID,
			PermissionGeneration: generation, PermissionRuntimeEpoch: capabilities.RuntimeAuthority.RuntimeEpoch(),
			RunAuthorizationFence: fence,
			ModeRevision:          mode.Revision,
			PermissionRevision:    permissionResult.Permission.Revision}, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	encodedAgentCodeAuthority, err := toolgateway.EncodeAgentCodeCallAuthority(agentCodeAuthority)
	if err != nil {
		t.Fatal(err)
	}
	agentCodeScope := toolgateway.AgentCodeExecutionScope{
		InvocationID: "invocation-activity-v2-propose", OperationKey: "activity-v2-propose-0001",
		RunID: run.ID, MissionID: mission.ID, RootAgentID: root.ID, SessionID: run.SessionID,
		WorkspaceID: workspaceRecord.ID, WorkspaceRoot: workspaceRoot,
		RootFingerprint: rootFingerprint, Surface: mode.Surface, Phase: mode.Phase,
		Role: root.Role, Profile: mode.Profile, PermissionMode: permissionResult.Permission.Mode,
		ModeRevision: mode.Revision, PermissionRevision: permissionResult.Permission.Revision,
		PermissionSnapshotID: permissionResult.Permission.ID,
		PermissionGeneration: generation, PermissionRuntimeEpoch: capabilities.RuntimeAuthority.RuntimeEpoch(),
		RunAuthorizationFence: fence,
		CapabilityGeneration:  agentCodeAuthority.CapabilityGeneration,
		LeaseID:               leaseResult.Lease.LeaseID, LeaseGeneration: leaseResult.Lease.Generation,
		RequestedBy: "run_supervisor", PolicyDecision: toolgateway.Decision{Allowed: true,
			Approval: toolgateway.ApprovalAutomatic, Risk: "low", Reason: "integration test"},
	}
	agentCodeExecutor := application.NewAgentCodeToolExecutor(state, httpAlwaysReviewPolicy{base: policy.NewDefaultChecker()}).
		WithExecutionPermissionCapabilities(capabilities)
	proposedResult, err := agentCodeExecutor.ExecuteAgentCode(ctx, agentCodeScope,
		toolgateway.WorkspaceChangeTool, mustActivityJSON(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch",
			Path: "src/session.ts", ExpectedSHA256: fileedit.HashText("old\n"),
			Replacements: []toolgateway.WorkspaceReplacement{{OldText: "old\n",
				NewText: "new\n" + diffCanary + "\n", ExpectedOccurrences: 1}},
		}))
	if err != nil {
		t.Fatal(err)
	}
	var proposed struct {
		EditID       string `json:"edit_id"`
		OriginalHash string `json:"original_sha256"`
		ProposedHash string `json:"proposed_sha256"`
	}
	if err := json.Unmarshal([]byte(proposedResult.JSON), &proposed); err != nil ||
		proposed.EditID == "" || proposed.OriginalHash == "" || proposed.ProposedHash == "" {
		t.Fatalf("decode real workspace_change result: result=%s err=%v", proposedResult.JSON, err)
	}
	if _, err := application.NewFileEditReviewService(state).Review(ctx,
		application.ReviewFileEditRequest{Version: application.FileEditReviewProtocolVersion,
			RunID: run.ID, EditID: proposed.EditID,
			Action: application.FileEditApproveIntent}); err != nil {
		t.Fatal(err)
	}
	applyPayload := mustActivityJSON(t, toolgateway.WorkspaceApplyPayload{
		Version: toolgateway.AgentCodeRegistryVersion, EditID: proposed.EditID,
		ExpectedAction: "propose_patch", ExpectedOriginalSHA256: proposed.OriginalHash,
		ExpectedProposedSHA256: proposed.ProposedHash})
	applyScope := agentCodeScope
	applyScope.InvocationID = "invocation-activity-v2-apply"
	applyScope.OperationKey = "activity-v2-apply-0001"
	appliedResult, err := agentCodeExecutor.ExecuteAgentCode(ctx, applyScope,
		toolgateway.WorkspaceApplyTool, applyPayload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(appliedResult.JSON, `"diff"`) ||
		!strings.Contains(appliedResult.JSON, `"file_written":true`) {
		t.Fatalf("workspace_apply result is not production-shaped: %s", appliedResult.JSON)
	}
	webAuthority, err := toolgateway.NewWebEvidenceCallAuthority(
		toolgateway.WebEvidenceCapabilityContext{RunID: run.ID, MissionID: mission.ID,
			SessionID: run.SessionID, RootAgentID: root.ID,
			WorkspaceID: workspaceRecord.ID, Surface: mode.Surface, Phase: mode.Phase,
			Role: root.Role, Profile: mode.Profile,
			PermissionMode:     permissionResult.Permission.Mode,
			PermissionRevision: permissionResult.Permission.Revision,
			ModeRevision:       mode.Revision, NetworkMode: "allowlist",
			AllowedTargets:    []string{"docs.example.com", "search.example.com"},
			ProviderAvailable: true, ProviderFingerprint: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	encodedWebAuthority, err := toolgateway.EncodeWebEvidenceCallAuthority(webAuthority)
	if err != nil {
		t.Fatal(err)
	}
	encodedMCPAuthority, err := mcp.EncodeSupervisorCallAuthority(mcp.SupervisorCallAuthority{
		Version: mcp.SupervisorCallAuthorityVersion, RunID: run.ID, MissionID: mission.ID,
		WorkspaceID:          workspaceRecord.ID,
		PermissionSnapshotID: permissionResult.Permission.ID,
		PermissionRevision:   permissionResult.Permission.Revision,
		PermissionMode:       permissionResult.Permission.Mode,
	})
	if err != nil {
		t.Fatal(err)
	}

	searchPayload := mustActivityJSON(t, toolgateway.WebSearchPayload{
		Version: "web_search.v1", Query: "durable activity projection", Limit: 2})
	fetchPayload := mustActivityJSON(t, toolgateway.WebFetchPayload{
		Version: "web_fetch.v1", URL: "https://docs.example.com/start"})
	mcpArgumentCanary := "MCP_ARGUMENT_PRIVATE_CANARY_47D3"
	unsafeMCPPayload := mustActivityJSON(t, toolgateway.MCPToolCallPayload{
		Version: toolgateway.MCPClientToolProtocolVersion, ServerID: "issue-tracker",
		ToolName: "create_issue", CapabilityFingerprint: strings.Repeat("d", 64),
		Arguments: mustActivityJSON(t, map[string]any{"project": "activity-v2",
			"password": mcpArgumentCanary}),
	})
	if _, err := toolgateway.NormalizeSupervisorToolPayload(
		toolgateway.MCPToolCallTool, unsafeMCPPayload); err == nil {
		t.Fatal("inline MCP credential reached the durable Supervisor boundary")
	}
	mcpPayload := mustActivityJSON(t, toolgateway.MCPToolCallPayload{
		Version: toolgateway.MCPClientToolProtocolVersion, ServerID: "issue-tracker",
		ToolName: "create_issue", CapabilityFingerprint: strings.Repeat("d", 64),
		Arguments: mustActivityJSON(t, map[string]any{"project": "activity-v2",
			"credential_ref": "credential-issue-tracker", "details": map[string]any{"priority": 2}}),
	})
	calls := []llm.ToolCall{
		mustActivityToolCall(t, run.ID, turn.Checkpoint.NextTurn, 1,
			toolgateway.WebSearchTool, searchPayload, encodedWebAuthority),
		mustActivityToolCall(t, run.ID, turn.Checkpoint.NextTurn, 1,
			toolgateway.WebFetchTool, fetchPayload, encodedWebAuthority),
		mustActivityToolCall(t, run.ID, turn.Checkpoint.NextTurn, 1,
			toolgateway.WorkspaceApplyTool, applyPayload, encodedAgentCodeAuthority),
		mustActivityToolCall(t, run.ID, turn.Checkpoint.NextTurn, 1,
			toolgateway.MCPToolCallTool, mcpPayload, encodedMCPAuthority),
	}
	modelAttempt.Outcome = llm.OutcomeSuccess
	checkpoint, err := state.RecordSupervisorModelCompletedForAgent(ctx, turn.Checkpoint,
		modelAttempt, llm.ChatResponse{Provider: modelAttempt.Provider, Model: modelAttempt.Model,
			Usage:     llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
			ToolCalls: calls}, domain.AgentAttribution{AgentID: admitted.Agent.ID,
			AgentAttemptID: specialistAttempt.ID, Source: domain.AgentAttributionRecorded})
	if err != nil {
		t.Fatal(err)
	}

	searchSnippetCanary := "SEARCH_SNIPPET_PRIVATE_CANARY_A810"
	searchStdout := mustActivityJSONString(t, webevidence.SearchResult{
		ProtocolVersion: webevidence.SearchProtocolVersion,
		Query:           "durable activity projection", Provider: "native-search",
		SearchPolicy: "provider_native", SelectionReason: "configured Provider supports search",
		SearchedAt: time.Now().UTC(), Sources: []webevidence.SearchStub{
			{SourceID: "source-activity-v2-1", CanonicalURL: "https://search.example.com/one",
				Title: "First source", Snippet: searchSnippetCanary, Rank: 1,
				Provider: "native-search", Citeable: true},
			{SourceID: "source-activity-v2-2", CanonicalURL: "https://search.example.com/two",
				Title: "Second source", Rank: 2, Provider: "native-search", Citeable: true},
		},
	})
	fetchBodyCanary := "FETCH_BODY_PRIVATE_CANARY_5C21"
	fetchStdout := mustActivityJSONString(t, map[string]any{
		"protocol_version": webevidence.FetchProtocolVersion,
		"source":           map[string]any{"source_id": "source-activity-v2-fetch"},
		"snapshot": map[string]any{"source_id": "source-activity-v2-fetch",
			"snapshot_id": "snapshot-activity-v2-fetch",
			"url":         "https://docs.example.com/final", "state": "fetched",
			"robots": "bypassed_disallow", "redirects": 2, "citeable": true,
			"body": fetchBodyCanary},
	})
	mcpResultCanary := "MCP_RESULT_PRIVATE_CANARY_C664"
	rawMCPStdout := []byte(mustActivityJSONString(t, map[string]any{"status": "created",
		"issue_id": "ISSUE-42", "nested": map[string]any{"accepted": true,
			"auth_header": mcpResultCanary}}))
	safeMCPStdout, err := redact.SanitizeSensitiveJSON(rawMCPStdout)
	if err != nil {
		t.Fatalf("sanitize MCP result fixture: %v", err)
	}
	mcpStdout := string(safeMCPStdout)
	completedAt := time.Now().UTC()
	results := []string{
		mustActivityResultEnvelope(t, toolgateway.WebSearchTool,
			map[string]string{"provider": "native-search", "search_policy": "provider_native",
				"selection_reason": "configured Provider supports search", "source_count": "2",
				"citeable": "true"}, searchStdout),
		mustActivityResultEnvelope(t, toolgateway.WebFetchTool,
			map[string]string{"source_id": "source-activity-v2-fetch",
				"snapshot_id": "snapshot-activity-v2-fetch", "citation_id": "",
				"url": "https://docs.example.com/final", "title": "Fetched source",
				"state": "fetched", "http_status": "200",
				"robots": "bypassed_disallow", "robots_policy": "observe_only",
				"redirects": "2", "partial": "false", "stale": "false",
				"citeable": "true", "fetched_at": completedAt.Format(time.RFC3339Nano),
				"stale_at": completedAt.Add(time.Hour).Format(time.RFC3339Nano),
				"digest":   strings.Repeat("e", 64)}, fetchStdout),
		mustActivityResultEnvelope(t, toolgateway.WorkspaceApplyTool,
			appliedResult.Metadata, appliedResult.JSON),
		mustActivityResultEnvelope(t, toolgateway.MCPToolCallTool,
			map[string]string{"server_id": "issue-tracker", "tool_name": "create_issue",
				"untrusted_output": "true"}, mcpStdout),
	}
	for index, call := range calls {
		if inserted, err := state.RecordSupervisorToolExecutionStarted(ctx, checkpoint,
			call.ID); err != nil || !inserted {
			t.Fatalf("start tool %d inserted=%t err=%v", index, inserted, err)
		}
		if _, replayed, err := state.RecordSupervisorToolResult(ctx, checkpoint,
			domain.SupervisorToolResult{CallID: call.ID,
				Status: domain.SupervisorToolCompleted, ResultJSON: results[index],
				CompletedAt: completedAt.Add(time.Duration(index) * time.Millisecond)}); err != nil || replayed {
			t.Fatalf("record tool %d replayed=%t err=%v", index, replayed, err)
		}
	}
	thread, err := state.GetThreadByRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	api, err := New(reopened, Config{AccessToken: testAccessToken,
		AppVersion: "thread-activity-v2-test"})
	if err != nil {
		t.Fatal(err)
	}

	for _, call := range calls {
		stored, err := reopened.GetThreadSupervisorToolCall(ctx, thread.ID, call.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.AgentID != admitted.Agent.ID || stored.AgentAttemptID != specialistAttempt.ID ||
			stored.AgentAttribution != domain.AgentAttributionRecorded {
			t.Fatalf("persisted Agent attribution=%+v", stored)
		}
		if strings.Contains(stored.PayloadJSON, mcpArgumentCanary) ||
			strings.Contains(stored.ResultJSON, mcpResultCanary) {
			t.Fatalf("sensitive MCP canary reached durable storage: payload=%s result=%s",
				stored.PayloadJSON, stored.ResultJSON)
		}
	}

	forbidden := []string{searchSnippetCanary, fetchBodyCanary, diffCanary,
		mcpArgumentCanary, mcpResultCanary, `"payload_json"`, `"result_json"`,
		`"authority_json"`, strings.Repeat("d", 64)}
	views := make(map[toolgateway.ToolName]ThreadActivityDetailView, len(calls))
	for index, call := range calls {
		response := performRequest(t, api, http.MethodGet,
			"/api/v1/threads/"+thread.ID+"/activities/"+call.ID,
			testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("activity %s status=%d body=%s", call.Name, response.Code,
				response.Body.String())
		}
		for _, value := range forbidden {
			if strings.Contains(response.Body.String(), value) {
				t.Fatalf("activity %s leaked %q: %s", call.Name, value,
					response.Body.String())
			}
		}
		var view ThreadActivityDetailView
		decodeData(t, response, &view)
		if view.Version != application.ThreadActivityDetailProtocolVersion ||
			view.ActivityRef != call.ID || view.RunID != run.ID || len(view.Tools) != 1 {
			t.Fatalf("activity %d has invalid envelope: %+v", index, view)
		}
		tool := view.Tools[0]
		if tool.AgentID != admitted.Agent.ID || tool.AgentRole != string(domain.AgentRoleSpecialist) ||
			tool.AgentLabel != "Specialist Agent" || tool.Name != call.Name {
			t.Fatalf("activity %s has fabricated Agent attribution: %+v", call.Name, tool)
		}
		assertOnlyActivityDetailKind(t, tool.Detail)
		views[toolgateway.ToolName(call.Name)] = view
	}

	search := views[toolgateway.WebSearchTool].Tools[0].Detail.WebSearch
	if search == nil || search.Query != "durable activity projection" ||
		search.Provider != "native-search" || search.SourceCount != 2 || !search.Citeable ||
		len(search.Sources) != 2 || search.Sources[0].Rank != 1 ||
		search.Sources[0].Title != "First source" ||
		search.Sources[0].URL != "https://search.example.com/one" ||
		!search.Sources[0].Citeable {
		t.Fatalf("search detail=%+v", search)
	}
	fetch := views[toolgateway.WebFetchTool].Tools[0].Detail.WebFetch
	if fetch == nil || fetch.URL != "https://docs.example.com/final" ||
		fetch.State != "fetched" || fetch.HTTPStatus != 200 ||
		fetch.Robots != "bypassed_disallow" || fetch.RobotsPolicy != "observe_only" ||
		fetch.Redirects != 2 || fetch.Partial || !fetch.Citeable {
		t.Fatalf("fetch detail=%+v", fetch)
	}
	edit := views[toolgateway.WorkspaceApplyTool].Tools[0].Detail.FileEdit
	if edit == nil || edit.Path != "src/session.ts" || edit.ApplyStatus != "applied" ||
		edit.EditID != proposed.EditID || !edit.DiffAvailable ||
		!edit.Applied || !edit.FileWritten || edit.Replayed || edit.Diff.AddedLines != 2 ||
		edit.Diff.RemovedLines != 1 || edit.Diff.Hunks != 1 ||
		!strings.Contains(edit.Diff.Summary, "+2") || strings.Contains(edit.Diff.Summary, diffCanary) {
		t.Fatalf("file edit detail=%+v", edit)
	}
	diffResponse := performRequest(t, api, http.MethodGet,
		"/api/v1/runs/"+run.ID+"/file-edits/"+proposed.EditID,
		testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if diffResponse.Code != http.StatusOK {
		t.Fatalf("lazy file diff status=%d body=%s", diffResponse.Code,
			diffResponse.Body.String())
	}
	var diffView FileEditPreviewView
	decodeData(t, diffResponse, &diffView)
	if diffView.ID != proposed.EditID || diffView.SessionID != run.SessionID ||
		diffView.WorkspaceID != workspaceRecord.ID ||
		!strings.Contains(diffView.Diff, "-old") ||
		!strings.Contains(diffView.Diff, "+new") ||
		!strings.Contains(diffView.Diff, diffCanary) {
		t.Fatalf("lazy Run-bound file diff=%+v", diffView)
	}
	mcpDetail := views[toolgateway.MCPToolCallTool].Tools[0].Detail.MCP
	if mcpDetail == nil || mcpDetail.Server != "issue-tracker" ||
		mcpDetail.Tool != "create_issue" || len(mcpDetail.Arguments) != 3 ||
		mcpDetail.Result.Type != "object" || mcpDetail.Result.Count != 3 ||
		len(mcpDetail.Result.Fields) != 3 || !mcpDetail.Boundary.Untrusted {
		t.Fatalf("MCP detail=%+v", mcpDetail)
	}
	argumentSummaries := make(map[string]string, len(mcpDetail.Arguments))
	for _, argument := range mcpDetail.Arguments {
		argumentSummaries[argument.Name] = argument.Summary
	}
	if argumentSummaries["project"] != "activity-v2" ||
		argumentSummaries["credential_ref"] != "[已脱敏]" ||
		argumentSummaries["details"] == "" {
		t.Fatalf("MCP argument summaries are not useful and safe: %+v",
			mcpDetail.Arguments)
	}
	resultSummaries := make(map[string]string, len(mcpDetail.Result.Fields))
	for _, field := range mcpDetail.Result.Fields {
		resultSummaries[field.Name] = field.Summary
	}
	if resultSummaries["status"] != "created" ||
		resultSummaries["issue_id"] != "ISSUE-42" ||
		resultSummaries["nested"] == "" {
		t.Fatalf("MCP result summaries are not useful and safe: %+v",
			mcpDetail.Result.Fields)
	}
}

func mustActivityJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustActivityJSONString(t *testing.T, value any) string {
	t.Helper()
	return string(mustActivityJSON(t, value))
}

func mustActivityToolCall(t *testing.T, runID string, turn, round int,
	name toolgateway.ToolName, payload, authority json.RawMessage,
) llm.ToolCall {
	t.Helper()
	canonical, err := toolgateway.NormalizeSupervisorToolPayload(name, payload)
	if err != nil {
		t.Fatal(err)
	}
	callID, err := runmutation.SupervisorToolCallID(
		runmutation.SupervisorToolOperationKey(runID, turn, string(name), string(canonical)),
		round)
	if err != nil {
		t.Fatal(err)
	}
	return llm.ToolCall{ID: callID, Name: string(name), Arguments: canonical,
		Authority: authority}
}

func mustActivityResultEnvelope(t *testing.T, name toolgateway.ToolName,
	metadata map[string]string, stdout string,
) string {
	t.Helper()
	return mustActivityJSONString(t, map[string]any{"version": "supervisor_tool_result.v1",
		"tool": name, "status": "completed", "metadata": metadata, "stdout": stdout})
}

func assertOnlyActivityDetailKind(t *testing.T, detail ThreadActivityTypedDetailView) {
	t.Helper()
	branches := 0
	for _, present := range []bool{detail.Command != nil, detail.WebSearch != nil,
		detail.WebFetch != nil, detail.FileRead != nil, detail.FileEdit != nil,
		detail.MCP != nil, detail.Verification != nil, detail.Browser != nil} {
		if present {
			branches++
		}
	}
	if branches != 1 {
		t.Fatalf("activity detail %q has %d branches: %+v", detail.Kind, branches, detail)
	}
	matching := map[string]bool{"command": detail.Command != nil,
		"web_search": detail.WebSearch != nil, "web_fetch": detail.WebFetch != nil,
		"file_read": detail.FileRead != nil, "file_edit": detail.FileEdit != nil,
		"mcp": detail.MCP != nil, "verification": detail.Verification != nil,
		"browser": detail.Browser != nil}
	if !matching[detail.Kind] {
		t.Fatalf("activity detail kind %q does not match its branch: %+v", detail.Kind, detail)
	}
}
