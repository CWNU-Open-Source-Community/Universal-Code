package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolcontract"
	"cyberagent-workbench/internal/toolgateway"
)

const managerStdioMarker = "manager-runtime-stdio-fixture"

type managerStdioEvent struct {
	Phase, Method, Cwd, Executable string
	Args, EnvNames                 []string
	CanaryInherited                bool
	ManualConfigMatched            bool
	Params                         json.RawMessage
}

func appendManagerStdioEvent(path string, event managerStdioEvent) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	closeErr := f.Close()
	return errors.Join(err, closeErr)
}

// A real subprocess peer. Only the remote protocol behavior and its file gates
// are fixtures; Manager, SDK transport, application PolicyAuthorizer, SQLite and
// (below) Supervisor recovery are production implementations.
func TestManagerRuntimeStdioHelper(t *testing.T) {
	i := slices.Index(os.Args, managerStdioMarker)
	if i < 0 {
		t.Skip("helper subprocess only")
	}
	if err := runManagerStdioPeer(os.Args[i+1], os.Args[i+2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0) // never put testing's PASS marker on the protocol stream
}

func runManagerStdioPeer(dir, scenario string) error {
	phase := "review"
	if _, err := os.Stat(filepath.Join(dir, "runtime")); err == nil {
		phase = "runtime"
	}
	trace := filepath.Join(dir, "wire.jsonl")
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	names := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, strings.ToUpper(name))
	}
	slices.Sort(names)
	if err := appendManagerStdioEvent(trace, managerStdioEvent{Phase: phase, Method: "spawn", Cwd: cwd,
		Executable: executable, Args: os.Args[1:], EnvNames: names, CanaryInherited: os.Getenv("TRAVERSE_MANAGER_PARENT_CANARY") != "",
		ManualConfigMatched: cwd == dir && os.Getenv("TRAVERSE_CONFIG_LITERAL") == "selected" && os.Getenv("TRAVERSE_CONFIG_SECRET") == "synthetic-environment-secret"}); err != nil {
		return err
	}
	waitGate := func(name string) error {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return nil
			}
			time.Sleep(5 * time.Millisecond)
		}
		return fmt.Errorf("fixture gate %s timed out", name)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), mcp.MaxMessageBytes)
	page := 0
	for scanner.Scan() {
		var request mcp.Envelope
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return err
		}
		if err := appendManagerStdioEvent(trace, managerStdioEvent{Phase: phase, Method: request.Method, Params: request.Params}); err != nil {
			return err
		}
		if len(request.ID) == 0 {
			continue
		}
		var result json.RawMessage
		switch request.Method {
		case "initialize":
			result = json.RawMessage(`{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"manager-stdio-peer","version":"1"}}`)
		case "tools/list":
			page++
			if phase == "runtime" && strings.HasSuffix(scenario, "_discovery") {
				if err := waitGate("release-list"); err != nil {
					return err
				}
			}
			result = json.RawMessage(`{"tools":[{"name":"lookup","description":"Controlled stdio lookup","inputSchema":{"type":"object","additionalProperties":false,"required":["value"],"properties":{"value":{"type":"integer"}}}}]}`)
			if phase == "runtime" && (scenario == "revoke_discovery" || scenario == "pagination") {
				result = json.RawMessage(fmt.Sprintf(`{"tools":[],"nextCursor":"page-%d"}`, page))
			}
		case "tools/call":
			if phase != "runtime" {
				return errors.New("control-plane review dispatched a tool")
			}
			if scenario == "lost_response" {
				return nil // the marker proves receipt; EOF carries no result
			}
			if scenario == "revoke_after_send" {
				if err := waitGate("release-call"); err != nil {
					return err
				}
			}
			result = json.RawMessage(`{"content":[{"type":"text","text":"stdio result"}],"structuredContent":{"value":9007199254740993},"_meta":{"vendor":{"number":9007199254740993}},"vendorExtension":{"preserved":true}}`)
			if scenario == "remote_error" {
				result = json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"remote partial work failed"}]}`)
			}
			if strings.HasPrefix(scenario, "manual_config") {
				raw, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": os.Getenv("TRAVERSE_CONFIG_SECRET")}}})
				if err != nil {
					return err
				}
				result = raw
			}
		default:
			return fmt.Errorf("unexpected request %s", request.Method)
		}
		if err := json.NewEncoder(os.Stdout).Encode(mcp.Envelope{JSONRPC: "2.0", ID: request.ID, Result: result}); err != nil {
			return err
		}
	}
	return scanner.Err()
}

type managerStdioStore struct {
	*store.SQLiteStore
	onGet func()
}

func (s *managerStdioStore) GetMCPClientServer(ctx context.Context, id string) (mcp.ServerRecord, error) {
	if s.onGet != nil {
		s.onGet()
	}
	return s.SQLiteStore.GetMCPClientServer(ctx, id)
}

type managerStdioFixture struct {
	state        *managerStdioStore
	manager      *mcp.Manager
	dbPath, dir  string
	run          domain.Run
	reviewed     mcp.ServerRecord
	capabilities domain.ExecutionPermissionRuntimeCapabilities
	authority    *domain.ExecutionPermissionRuntimeAuthority
	credentials  *credential.MemoryStore
}

func newManagerStdioFixture(t *testing.T, scenario string) *managerStdioFixture {
	t.Helper()
	f := &managerStdioFixture{dir: t.TempDir()}
	f.dbPath = filepath.Join(f.dir, "state.db")
	st, err := store.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f.state = &managerStdioStore{SQLiteStore: st}
	t.Cleanup(func() { _ = f.state.Close() })
	workspace := store.WorkspaceRecord{ID: "stdio-workspace", Name: "stdio-workspace", RootPath: f.dir}
	if err := st.SaveWorkspace(t.Context(), workspace); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	_, f.run, err = runs.Create(t.Context(), application.CreateRunRequest{Goal: "Verify one reviewed stdio MCP call", Profile: "code",
		WorkspaceID: workspace.ID, Surface: "code", Phase: "deliver", ModelRoute: "manager-stdio-fixture/model",
		Budget: domain.Budget{MaxTurns: 8, MaxTokens: 100000, MaxToolCalls: 8}})
	if err != nil {
		t.Fatal(err)
	}
	f.authority = domain.NewExecutionPermissionRuntimeAuthority()
	f.capabilities = domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: f.authority}
	initial, err := st.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil || initial.Mode != domain.RunExecutionPermissionAsk || initial.ProtocolVersion != domain.RunApprovalPermissionProtocolVersion {
		t.Fatalf("new Run did not persist the v2 ask preference: %+v err=%v", initial, err)
	}
	permissions := application.NewRunExecutionPermissionService(st, f.capabilities)
	legacy := f.fullRequest()
	legacy.Mode, legacy.OperationKey = string(domain.RunExecutionPermissionFullAccess), "stdio-legacy-rejected"
	legacy.ConfirmFull, legacy.ConfirmDangerFullAccess = false, true
	if _, err := permissions.Change(t.Context(), legacy); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("legacy full_access writer must be rejected: %v", err)
	}
	unconfirmed := f.fullRequest()
	unconfirmed.ConfirmFull = false
	if _, err := permissions.Change(t.Context(), unconfirmed); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("unconfirmed Full writer must be rejected: %v", err)
	}
	unchanged, err := st.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil || unchanged != initial {
		t.Fatalf("rejected writers changed the persisted preference: %+v err=%v", unchanged, err)
	}
	f.activateFull(t, false)
	// Exercise the same public reactivation path used after reopening SQLite.
	// Reading a persisted preference into a cold process must not grant Full.
	f.restartPermissionAuthority(t)
	f.activateFull(t, true)
	f.run, err = runs.Start(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.credentials = credential.NewMemoryStore()
	f.manager, err = mcp.NewClientManager(f.state, f.credentials, mcp.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.manager.Close() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion, ID: "stdio-runtime", Name: "stdio-runtime",
		Transport: mcp.TransportStdio, Target: executable,
		Arguments:            []string{"-test.run=^TestManagerRuntimeStdioHelper$", "--", managerStdioMarker, f.dir, scenario, "space value", `literal "quote"`, "$HOME", "%TEMP%", "${PLUGIN_ROOT}"},
		DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools}, Scope: mcp.ScopeRun, RunID: f.run.ID, WorkspaceID: workspace.ID,
		Source: mcp.Source{Kind: "manual", URI: "test://manager-stdio"}, CallTimeoutMillis: 20000, MaxResultBytes: 8192}
	if strings.HasPrefix(scenario, "manual_config") {
		descriptor.ProtocolVersion, descriptor.Cwd = mcp.ConfigClientProtocolVersion, f.dir
		descriptor.Env = map[string]string{"TRAVERSE_CONFIG_LITERAL": "selected"}
		descriptor.EnvCredentialRefs = map[string]string{"TRAVERSE_CONFIG_SECRET": "manual-config-secret"}
		if err := f.credentials.Put(t.Context(), "manual-config-secret", "synthetic-environment-secret"); err != nil {
			t.Fatal(err)
		}
	}
	f.reviewed, _, err = f.manager.Stage(t.Context(), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	f.reviewed, err = f.manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewApproveDiscovery,
		ExpectedDescriptorFingerprint: f.reviewed.DescriptorFingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	f.reviewed, err = f.manager.Refresh(t.Context(), descriptor.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.reviewed, err = f.manager.Review(t.Context(), descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewEnableCapabilities,
		ExpectedDescriptorFingerprint: f.reviewed.DescriptorFingerprint, ExpectedCapabilityFingerprint: f.reviewed.Capabilities.Fingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "runtime"), []byte("runtime"), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *managerStdioFixture) fullRequest() application.ChangeRunExecutionPermissionRequest {
	return application.ChangeRunExecutionPermissionRequest{RunID: f.run.ID, Mode: string(domain.RunExecutionPermissionFull),
		OperationKey: "stdio-confirmed-full", RequestedBy: "operator", Reason: "controlled MCP acceptance", ConfirmFull: true}
}

func (f *managerStdioFixture) activateFull(t *testing.T, replay bool) {
	t.Helper()
	before, err := f.state.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A confirmed Change activates the exact persisted current snapshot through
	// the application service. Exact operator replay reactivates it after restart;
	// the fixture never writes SQL grants or calls the authority grant directly.
	result, err := application.NewRunExecutionPermissionService(f.state, f.capabilities).Change(t.Context(), f.fullRequest())
	if err != nil || result.Replayed != replay {
		t.Fatalf("confirmed Full change/replay failed: replay=%t want=%t err=%v", result.Replayed, replay, err)
	}
	permission, err := f.state.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil || permission != result.Permission || (replay && permission != before) ||
		permission.Mode != domain.RunExecutionPermissionFull || permission.ProtocolVersion != domain.RunApprovalPermissionProtocolVersion ||
		permission.PolicyVersion != domain.OperationPermissionPolicyVersion || !permission.OperatorConfirmed ||
		permission.ProcessEnabled || permission.ExecutionAuthorized || permission.CapabilityGrant {
		t.Fatalf("Full writer persisted an invalid preference or replay: %+v err=%v", permission, err)
	}
	if generation, active := f.capabilities.FullAccessGeneration(permission); !active || generation == 0 {
		t.Fatal("confirmed application change did not explicitly activate current Full")
	}
	t.Logf("real permission writer: mode=%s protocol=%s replay=%t; explicit Full activation present; persisted grant flags=false",
		permission.Mode, permission.ProtocolVersion, replay)
}

func (f *managerStdioFixture) restartPermissionAuthority(t *testing.T) {
	t.Helper()
	f.authority = domain.NewExecutionPermissionRuntimeAuthority()
	f.capabilities.RuntimeAuthority = f.authority
	permission, err := f.state.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil || permission.Mode != domain.RunExecutionPermissionFull || f.capabilities.AllowsSnapshot(permission) {
		t.Fatalf("persisted Full was not inactive in the cold authority: %+v err=%v", permission, err)
	}
	t.Log("cold authority: persisted Full remains inactive until explicit operator replay")
}

func (f *managerStdioFixture) payload() toolgateway.MCPToolCallPayload {
	return toolgateway.MCPToolCallPayload{Version: toolgateway.MCPClientToolProtocolVersion, ServerID: f.reviewed.Descriptor.ID,
		ToolName: "lookup", CapabilityFingerprint: f.reviewed.ApprovedCapabilityFingerprint, Arguments: json.RawMessage(`{"value":9007199254740993}`)}
}

func (f *managerStdioFixture) scope(t *testing.T) (toolgateway.MCPExecutionScope, domain.RunExecutionLease) {
	t.Helper()
	permission, err := f.state.GetRunExecutionPermission(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.state.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: f.run.ID, OwnerID: "stdio-acceptance", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := f.state.BeginSupervisorTurn(t.Context(), lease.Lease, "verify stdio authority")
	if err != nil {
		t.Fatal(err)
	}
	generation, active := f.capabilities.FullAccessGeneration(permission)
	if !active {
		t.Fatal("fixture Full activation missing")
	}
	fence, err := f.authority.IssueRunAuthorizationFence(f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return toolgateway.MCPExecutionScope{InvocationID: "stdio-invoke", RunID: f.run.ID, MissionID: f.run.MissionID, WorkspaceID: "stdio-workspace",
		AgentID: turn.Agent.ID, AgentAttemptID: turn.Agent.ActiveAttemptID, Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver,
		Role: domain.AgentRoleRoot, PermissionMode: permission.Mode, PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision,
		PermissionGeneration: generation, PermissionRuntimeEpoch: f.authority.RuntimeEpoch(), RunAuthorizationFence: fence,
		LeaseID: lease.Lease.LeaseID, LeaseGeneration: lease.Lease.Generation, RequestedBy: "run_supervisor",
		PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "high", Reason: "fixture"}}, lease.Lease
}

func managerStdioEvents(t *testing.T, dir, phase string) []managerStdioEvent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var events []managerStdioEvent
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		var event managerStdioEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// Only a partial trailing write is tolerable while polling a gate.
			if i == len(lines)-1 && !strings.HasSuffix(string(raw), "\n") {
				continue
			}
			t.Fatalf("malformed subprocess trace: %v", err)
		}
		if event.Phase == phase {
			events = append(events, event)
		}
	}
	return events
}

func countManagerStdioEvents(events []managerStdioEvent, method string) int {
	n := 0
	for _, event := range events {
		if event.Method == method {
			n++
		}
	}
	return n
}

func waitManagerStdioEvent(t *testing.T, dir, method string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if countManagerStdioEvents(managerStdioEvents(t, dir, "runtime"), method) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("real child did not reach %s", method)
}

func TestManagerRuntimeStdioProductionAuthorityAndReceipts(t *testing.T) {
	for _, scenario := range []string{"result", "deny_before_spawn", "revoke_discovery", "disable_discovery", "lease_discovery", "revoke_after_send", "lost_response", "remote_error", "pagination"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("TRAVERSE_MANAGER_PARENT_CANARY", "must-not-enter-child")
			f := newManagerStdioFixture(t, scenario)
			scope, lease := f.scope(t)
			executor, err := application.NewMCPClientToolExecutor(f.manager, f.state, f.capabilities)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "deny_before_spawn" {
				var once sync.Once
				// Actual permission is revoked after application preflight, at the
				// first Manager store read. Its real common authorizer must stop it.
				f.state.onGet = func() { once.Do(func() { f.authority.RevokeRun(f.run.ID) }) }
			}
			type outcome struct {
				result toolgateway.MCPExecutionResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := executor.ExecuteMCP(t.Context(), scope, f.payload())
				done <- outcome{result, err}
			}()
			gate := ""
			if strings.HasSuffix(scenario, "_discovery") {
				waitManagerStdioEvent(t, f.dir, "tools/list")
				gate = "release-list"
			}
			if scenario == "revoke_after_send" {
				waitManagerStdioEvent(t, f.dir, "tools/call")
				gate = "release-call"
			}
			switch scenario {
			case "revoke_discovery", "revoke_after_send":
				f.authority.RevokeRun(f.run.ID)
			case "disable_discovery":
				if _, err := f.manager.Review(t.Context(), f.reviewed.Descriptor.ID, mcp.ReviewRequest{Action: mcp.ReviewDisable,
					ExpectedDescriptorFingerprint: f.reviewed.DescriptorFingerprint, ReviewedBy: "operator"}); err != nil {
					t.Fatal(err)
				}
			case "lease_discovery":
				if _, _, err := f.state.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
					t.Fatal(err)
				}
			}
			if gate != "" {
				if err := os.WriteFile(filepath.Join(f.dir, gate), []byte("continue"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var got outcome
			select {
			case got = <-done:
			case <-time.After(25 * time.Second):
				t.Fatal("Manager did not finish bounded invocation")
			}
			events := managerStdioEvents(t, f.dir, "runtime")
			t.Logf("ExecuteMCP completed: runtime spawn=%d initialize=%d tools/list=%d tools/call=%d err=%v",
				countManagerStdioEvents(events, "spawn"), countManagerStdioEvents(events, "initialize"),
				countManagerStdioEvents(events, "tools/list"), countManagerStdioEvents(events, "tools/call"), got.err)
			if scenario != "deny_before_spawn" && (countManagerStdioEvents(events, "spawn") != 1 ||
				countManagerStdioEvents(events, "initialize") != 1 || countManagerStdioEvents(events, "notifications/initialized") != 1) {
				t.Fatal("runtime repeated or omitted process/handshake sends")
			}
			wantCalls := 0
			if scenario == "result" || scenario == "remote_error" || scenario == "lost_response" || scenario == "revoke_after_send" {
				wantCalls = 1
			}
			if calls := countManagerStdioEvents(events, "tools/call"); calls != wantCalls {
				t.Fatalf("actual tool sends = %d, want %d; err=%v", calls, wantCalls, got.err)
			}
			if scenario == "result" || scenario == "remote_error" {
				if got.err != nil || got.result.IsError != (scenario == "remote_error") || got.result.Metadata["execution_receipt"] != "result_received" ||
					got.result.Metadata["operation_id"] != scope.InvocationID {
					t.Fatalf("received result/receipt: %+v err=%v", got.result, got.err)
				}
				if scenario == "result" && (!json.Valid([]byte(got.result.Content)) || strings.Count(got.result.Content, "9007199254740993") != 2 ||
					!strings.Contains(got.result.Content, `"vendorExtension"`) || !strings.Contains(got.result.Content, `"_meta"`)) {
					t.Fatalf("production raw result lost precision/extensions: %s", got.result.Content)
				}
			} else {
				want := toolcontract.ReceiptNotDispatched
				if scenario == "lost_response" {
					want = toolcontract.ReceiptOutcomeUnknown
				}
				if scenario == "revoke_after_send" {
					want = toolcontract.ReceiptResultReceived
				}
				receipt, found := mcp.InvocationReceipt(got.err)
				if got.err == nil || !found || receipt.State != want || receipt.OperationID != scope.InvocationID || got.result.Content != "" {
					t.Fatalf("production failure receipt=%+v found=%t want=%s err=%v", receipt, found, want, got.err)
				}
			}
			if scenario == "deny_before_spawn" && len(events) != 0 {
				t.Fatalf("revoked authority spawned a child: %+v", events)
			}
			if scenario == "revoke_discovery" && countManagerStdioEvents(events, "tools/list") != 1 {
				t.Fatal("revocation allowed an extra discovery page")
			}
			if scenario == "pagination" && countManagerStdioEvents(events, "tools/list") != 16 {
				t.Fatal("native catalog page bound was not enforced")
			}
			for _, event := range events {
				if event.Method == "spawn" {
					actualDir, dirErr := os.Stat(event.Cwd)
					expectedDir, expectedErr := os.Stat(os.TempDir())
					if dirErr != nil || expectedErr != nil || !os.SameFile(actualDir, expectedDir) || event.CanaryInherited ||
						!slices.Equal(event.Args, f.reviewed.Descriptor.Arguments) {
						t.Fatal("actual process cwd/argv/minimal environment differs from reviewed launch")
					}
					actualExe, exeErr := os.Stat(event.Executable)
					expectedExe, expectedErr := os.Stat(f.reviewed.Descriptor.Target)
					if exeErr != nil || expectedErr != nil || !os.SameFile(actualExe, expectedExe) {
						t.Fatal("different executable was launched")
					}
					for _, name := range event.EnvNames {
						if !slices.Contains([]string{"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "TMP", "TEMP", "TMPDIR", "LANG", "LC_ALL"}, name) {
							t.Fatalf("unexpected inherited environment name %q", name)
						}
					}
				}
				if event.Method == "tools/call" && !strings.Contains(string(event.Params), "9007199254740993") {
					t.Fatal("actual sent arguments lost integer precision")
				}
			}
			if scenario == "revoke_discovery" || scenario == "lease_discovery" {
				current, err := f.state.GetMCPClientServer(t.Context(), f.reviewed.Descriptor.ID)
				if err != nil || current.State != mcp.TrustEnabled || current.Health != mcp.HealthHealthy || current.Generation != f.reviewed.Generation {
					t.Fatal("Run-local authority loss incorrectly changed shared server health", err)
				}
			}
			audits, err := f.state.ListMCPClientCalls(t.Context(), f.run.ID, 10)
			if err != nil || len(audits) != 1 || (scenario == "lost_response" && audits[0].ErrorCode != "outcome_unknown") ||
				(scenario == "remote_error" && (audits[0].Status != "failed" || audits[0].ErrorCode != "remote_tool_error")) {
				t.Fatalf("existing MCP audit lost dispatch evidence: %+v err=%v", audits, err)
			}
			t.Logf("real runtime wire: spawn=%d initialize=%d tools/list=%d tools/call=%d; audit=%s/%s",
				countManagerStdioEvents(events, "spawn"), countManagerStdioEvents(events, "initialize"),
				countManagerStdioEvents(events, "tools/list"), countManagerStdioEvents(events, "tools/call"), audits[0].Status, audits[0].ErrorCode)
		})
	}
}

// This store fault interrupts exactly the existing receipt write after a real
// Manager invocation. It neither simulates the transport nor creates a ledger.
type managerStdioReceiptFault struct {
	*store.SQLiteStore
	checkpoint  domain.SupervisorCheckpoint
	injected    bool
	interrupted domain.SupervisorToolResult
}

func (s *managerStdioReceiptFault) RecordSupervisorToolResult(ctx context.Context, checkpoint domain.SupervisorCheckpoint, result domain.SupervisorToolResult) (domain.SupervisorToolCall, bool, error) {
	if !s.injected {
		s.injected, s.checkpoint = true, checkpoint
		s.interrupted = result
		return domain.SupervisorToolCall{}, false, apperror.New(apperror.CodeInternal, "controlled receipt persistence interruption")
	}
	return s.SQLiteStore.RecordSupervisorToolResult(ctx, checkpoint, result)
}

type managerStdioProvider struct {
	llm.MockProvider
	payload json.RawMessage
	calls   int
}

func (*managerStdioProvider) Name() string              { return "manager-stdio-fixture" }
func (*managerStdioProvider) SupportsTools(string) bool { return true }
func (p *managerStdioProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	response := &llm.ChatResponse{Provider: p.Name(), Model: "model", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}
	if p.calls == 1 {
		if !slices.ContainsFunc(request.Tools, func(tool llm.ToolSpec) bool { return tool.Name == string(toolgateway.MCPToolCallTool) }) {
			return nil, errors.New("production Supervisor did not advertise reviewed MCP tools")
		}
		response.ToolCalls = []llm.ToolCall{{ID: "fixture-mcp-call", Name: string(toolgateway.MCPToolCallTool), Arguments: p.payload}}
	} else {
		raw, err := json.Marshal(domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionWait,
			Message: "Inspect the prior MCP receipt.", Reason: "operator input required"})
		if err != nil {
			return nil, err
		}
		response.Text = string(raw)
	}
	return response, nil
}
func (p *managerStdioProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	if response.Text != "" {
		chunks <- llm.ChatChunk{Text: response.Text}
	}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}

func TestManagerRuntimeStdioSupervisorRecoveryDoesNotResend(t *testing.T) {
	for _, scenario := range []string{"result", "lost_response", "remote_error"} {
		t.Run(scenario, func(t *testing.T) {
			f := newManagerStdioFixture(t, scenario)
			payload, err := json.Marshal(f.payload())
			if err != nil {
				t.Fatal(err)
			}
			provider := &managerStdioProvider{payload: payload}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			fault := &managerStdioReceiptFault{SQLiteStore: f.state.SQLiteStore}
			supervisor := application.NewAgentRunner(fault, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(f.capabilities).WithMCPClient(f.manager)
			_, firstErr := supervisor.Step(t.Context(), f.run.ID)
			t.Logf("first Supervisor receipt: runtime spawn=%d tools/call=%d injected=%t status=%s error_code=%s result=%s",
				countManagerStdioEvents(managerStdioEvents(t, f.dir, "runtime"), "spawn"),
				countManagerStdioEvents(managerStdioEvents(t, f.dir, "runtime"), "tools/call"),
				fault.injected, fault.interrupted.Status, fault.interrupted.ErrorCode, fault.interrupted.ResultJSON)
			if firstErr == nil || !fault.injected || countManagerStdioEvents(managerStdioEvents(t, f.dir, "runtime"), "tools/call") != 1 {
				t.Fatalf("real Supervisor -> Manager dispatch did not reach receipt interruption: injected=%t err=%v", fault.injected, firstErr)
			}
			rounds, err := f.state.ListSupervisorToolRounds(t.Context(), fault.checkpoint)
			if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 || rounds[0].Calls[0].Status != domain.SupervisorToolPending ||
				rounds[0].Calls[0].ToolName != string(toolgateway.MCPToolCallTool) {
				t.Fatal("interrupted result was not left pending in the existing ledger", err)
			}
			_ = f.manager.Close()
			if err := f.state.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(f.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			f.state = &managerStdioStore{SQLiteStore: st}
			f.manager, err = mcp.NewClientManager(f.state, nil, mcp.ManagerOptions{})
			if err != nil {
				t.Fatal(err)
			}
			// Recreate authority as well as Manager/Supervisor/SQLite handles. A
			// cold persisted Full mode never by itself authorizes a retry.
			f.restartPermissionAuthority(t)
			for resume := 0; resume < 2; resume++ {
				run, err := st.GetRun(t.Context(), f.run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if run.Status == domain.RunPaused {
					if _, err := application.NewRunService(st).Resume(t.Context(), run.ID); err != nil {
						t.Fatal(err)
					}
				}
				f.activateFull(t, true)
				supervisor = application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(f.capabilities).WithMCPClient(f.manager)
				step, err := supervisor.Step(t.Context(), f.run.ID)
				if err != nil {
					t.Fatalf("recovery %d failed: %v", resume, err)
				}
				if resume == 0 && !step.Recovered {
					t.Fatal("Supervisor did not recover the existing interrupted checkpoint")
				}
				rounds, err = st.ListSupervisorToolRounds(t.Context(), fault.checkpoint)
				if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 1 {
					t.Fatal("could not reload original durable call", err)
				}
				call := rounds[0].Calls[0]
				var envelope struct {
					Stdout   string            `json:"stdout"`
					Metadata map[string]string `json:"metadata"`
				}
				if err := json.Unmarshal([]byte(call.ResultJSON), &envelope); err != nil {
					t.Fatal(err)
				}
				if call.Status != domain.SupervisorToolFailed || call.ErrorCode != "outcome_unknown" || envelope.Stdout != "" ||
					envelope.Metadata["execution_receipt"] != "outcome_unknown" || envelope.Metadata["automatic_retry"] != "forbidden" {
					t.Fatalf("recovery lost conservative receipt: %+v", call)
				}
				events := managerStdioEvents(t, f.dir, "runtime")
				if countManagerStdioEvents(events, "tools/call") != 1 {
					t.Fatalf("recovery %d resent tools/call", resume)
				}
				t.Logf("reopened SQLite, recovery=%d recovered=%t: runtime spawn=%d tools/call=1; new catalog discovery permitted; durable receipt=outcome_unknown automatic_retry=forbidden", resume, step.Recovered, countManagerStdioEvents(events, "spawn"))
			}
		})
	}
}
