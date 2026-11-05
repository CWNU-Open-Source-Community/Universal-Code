package toolgateway

import (
	"context"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/policy"
	"encoding/json"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

type agentBrowserGatewayProbe struct{ calls int }

func (p *agentBrowserGatewayProbe) ExecuteAgentBrowserAction(context.Context, AgentBrowserExecutionScope, ToolName, json.RawMessage) (BrowserActionExecutionResult, error) {
	p.calls++
	return BrowserActionExecutionResult{Content: `{"ok":true}`}, nil
}
func TestAgentBrowserV2ProtocolIsExplicitAndTyped(t *testing.T) {
	valid := map[ToolName]string{BrowserNavigateTool: `{"version":"browser_navigate.v2","url":"https://developer.mozilla.org/en-US/#guide"}`, BrowserClickTool: `{"version":"browser_click.v2","snapshot_id":"snap-1","element_ref":"ref-1"}`, BrowserTypeTool: `{"version":"browser_type.v2","snapshot_id":"snap-1","element_ref":"ref-1","value":"public search","mode":"replace"}`, BrowserKeyTool: `{"version":"browser_key.v2","key":"Enter"}`, BrowserScrollTool: `{"version":"browser_scroll.v2","delta_x":0,"delta_y":800}`}
	for name, p := range valid {
		if _, e := NormalizeBrowserActionPayload(name, json.RawMessage(p)); e != nil {
			t.Errorf("%s %v", name, e)
		}
		d, ok := AgentBrowserToolDefinition(name)
		if !ok || !strings.Contains(string(d.InputSchema), string(name)+".v2") {
			t.Fatalf("missing v2 schema %s", name)
		}
	}
	bad := []struct {
		name ToolName
		raw  string
	}{{BrowserKeyTool, `{"version":"browser_key.v2","key":"Control+L"}`}, {BrowserKeyTool, `{"version":"browser_key.v1","key":"Enter"}`}, {BrowserScrollTool, `{"version":"browser_scroll.v2","delta_x":0,"delta_y":10001}`}, {BrowserNavigateTool, `{"version":"browser_navigate.v3","url":"https://example.org"}`}, {BrowserNavigateTool, `{"version":"browser_navigate.v1","url":"https://example.org"}`}, {BrowserClickTool, `{"version":"browser_click.v2","selector":"#publish"}`}, {BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"https://user:password@example.org"}`}, {BrowserNavigateTool, `{"version":"browser_navigate.v2","url":"file:///D:/secret"}`}, {BrowserTypeTool, `{"version":"browser_type.v2","snapshot_id":"s","element_ref":"r","value":"text","mode":"replace","confirmed":true}`}}
	for _, item := range bad {
		if _, e := NormalizeBrowserActionPayload(item.name, json.RawMessage(item.raw)); e == nil {
			t.Fatalf("invalid accepted %s", item.raw)
		}
	}
}
func TestAgentBrowserGatewayRoutesV2WithoutLegacyFallback(t *testing.T) {
	a := AgentBrowserCallAuthority{ProtocolVersion: AgentBrowserAuthorityVersion, RunID: "run-browser", MissionID: "mission-browser", SessionID: "session-browser", RootAgentID: "root-browser", WorkspaceID: "workspace-browser", Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot, Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionFullAccess, ModeRevision: 1, PermissionSnapshotID: "permission-browser", PermissionRevision: 1, PermissionActivation: 2, RunAuthorizationFence: 3, ManagerBootID: "boot-browser", BrowserSessionID: "managed-browser", SessionGeneration: 1}
	a.Generation = a.Fingerprint()
	auth, _ := json.Marshal(a)
	call := ToolCall{Name: BrowserNavigateTool, Payload: json.RawMessage(`{"version":"browser_navigate.v2","url":"https://example.org"}`), OperationKey: "browser-operation", RunID: a.RunID, MissionID: a.MissionID, AgentID: a.RootAgentID, AgentAttemptID: "attempt-browser", SessionID: a.SessionID, WorkspaceID: a.WorkspaceID, Surface: a.Surface, Phase: a.Phase, Role: a.Role, Profile: a.Profile, PermissionMode: a.PermissionMode, ModeRevision: a.ModeRevision, PermissionSnapshotID: a.PermissionSnapshotID, PermissionRevision: a.PermissionRevision, PermissionGeneration: a.PermissionActivation, RunAuthorizationFence: a.RunAuthorizationFence, CapabilityGeneration: a.Generation, LeaseID: "lease-browser", LeaseGeneration: 1, SupervisorTurn: 1, SupervisorToolCallID: "call-browser", RequestedBy: "run_supervisor", AgentBrowserAuthority: auth}
	probe := &agentBrowserGatewayProbe{}
	legacy := &browserActionExecutorStub{}
	gateway := New(nil, policy.NewDefaultChecker()).WithAgentBrowserExecutor(probe).WithBrowserActionExecutor(legacy)
	if _, e := gateway.Invoke(t.Context(), call); e != nil || probe.calls != 1 || legacy.calls != 0 {
		t.Fatalf("v2 routing %v %+v %+v", e, probe, legacy)
	}
	wrong := call
	wrong.PermissionGeneration++
	if _, e := gateway.Invoke(t.Context(), wrong); e == nil || probe.calls != 1 || legacy.calls != 0 {
		t.Fatal("authority mismatch was dispatched or fell back")
	}
	wrong = call
	wrong.Payload = json.RawMessage(`{"version":"browser_navigate.v3","url":"https://example.org"}`)
	if _, e := gateway.Invoke(t.Context(), wrong); e == nil || legacy.calls != 0 {
		t.Fatal("unknown protocol fell back")
	}
}

func TestAgentBrowserUploadPayloadRequiresSensitiveIntentAndBoundedPath(t *testing.T) {
	sensitive := `"sensitive_intent":{"version":"browser_sensitive_intent.v1","effect":"external_write","target":"https://example.com/upload","description":"Attach the operator-selected workspace report to the support form","document_epoch":7}`
	valid := []string{
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"docs/report.pdf",` + sensitive + `}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","target":{"name":"Upload report","role":"button"},"workspace_path":"docs/report.pdf",` + sensitive + `}`,
	}
	for _, raw := range valid {
		if _, e := NormalizeAgentBrowserPayload(BrowserUploadTool, json.RawMessage(raw)); e != nil {
			t.Fatalf("valid upload rejected: %v", e)
		}
	}
	if _, ok := AgentBrowserToolDefinition(BrowserUploadTool); !ok {
		t.Fatal("upload tool definition missing")
	}
	bad := []string{
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"docs/report.pdf"}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"C:/secrets/key.pem",` + sensitive + `}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"../../secrets/key.pem",` + sensitive + `}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"/etc/passwd",` + sensitive + `}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"docs\\report.pdf",` + sensitive + `}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"docs\u0000report.pdf",` + sensitive + `}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"docs//report.pdf",` + sensitive + `}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"docs/report.pdf","value":"x",` + sensitive + `}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"docs/report.pdf","element_ref2":"x",` + sensitive + `}`,
		`{"version":"browser_upload.v2","snapshot_id":"snap-1","element_ref":"ref-1","workspace_path":"docs/report.pdf","sensitive_intent":{"version":"browser_sensitive_intent.v1","effect":"external_delete","target":"https://example.com/upload","description":"wrong effect","document_epoch":7}}`,
		`{"version":"browser_upload.v2","target":{"name":"Upload","role":"button"},"workspace_path":"docs/report.pdf",` + sensitive + `}`,
	}
	for _, raw := range bad {
		if _, e := NormalizeAgentBrowserPayload(BrowserUploadTool, json.RawMessage(raw)); e == nil {
			t.Fatalf("invalid upload accepted: %s", raw)
		}
	}
}

func TestAgentBrowserSensitiveIntentSchemaMatchesNormalization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tool   ToolName
		effect string
		valid  bool
	}{
		{"upload write", BrowserUploadTool, "external_write", true},
		{"upload missing intent", BrowserUploadTool, "", false},
		{"upload delete", BrowserUploadTool, "external_delete", false},
		{"click without intent", BrowserClickTool, "", true},
		{"click delete", BrowserClickTool, "external_delete", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, ok := AgentBrowserToolDefinition(tc.tool)
			if !ok {
				t.Fatal("missing browser tool definition")
			}
			compiler := jsonschema.NewCompiler()
			if err := compiler.AddResource("browser-action.json", strings.NewReader(string(definition.InputSchema))); err != nil {
				t.Fatal(err)
			}
			schema, err := compiler.Compile("browser-action.json")
			if err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"version": string(tc.tool) + ".v2", "snapshot_id": "snap-1", "element_ref": "ref-1"}
			if tc.tool == BrowserUploadTool {
				payload["workspace_path"] = "docs/report.pdf"
			}
			if tc.effect != "" {
				payload["sensitive_intent"] = map[string]any{"version": "browser_sensitive_intent.v1", "effect": tc.effect,
					"target": "https://example.com/upload", "description": "Perform the selected external action", "document_epoch": 7}
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(decoded); (err == nil) != tc.valid {
				t.Fatalf("schema valid=%t, want %t: %v", err == nil, tc.valid, err)
			}
			if _, err := NormalizeAgentBrowserPayload(tc.tool, raw); (err == nil) != tc.valid {
				t.Fatalf("normalization valid=%t, want %t: %v", err == nil, tc.valid, err)
			}
		})
	}
}

func TestAgentBrowserScopedAuthorityBindsOriginsAndMode(t *testing.T) {
	base := AgentBrowserCallAuthority{ProtocolVersion: AgentBrowserAuthorityVersion, RunID: "run-scoped", MissionID: "mission-scoped", SessionID: "session-scoped", RootAgentID: "root-scoped", WorkspaceID: "workspace-scoped", Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot, Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionFullAccess, ModeRevision: 1, PermissionSnapshotID: "permission-scoped", PermissionRevision: 1, PermissionActivation: 2, RunAuthorizationFence: 3, ManagerBootID: "boot-scoped", BrowserSessionID: "managed-scoped", SessionGeneration: 1}
	base.Generation = base.Fingerprint()
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "declared_origins") || strings.Contains(string(raw), "scope_fingerprint") {
		t.Fatal("unscoped authority fingerprint material changed")
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("unscoped Full authority rejected: %v", err)
	}
	scope, err := browserruntime.NewAgentBrowserOriginScope([]string{"https://example.com", "http://localhost:3000"})
	if err != nil {
		t.Fatal(err)
	}
	scoped := base
	scoped.PermissionMode = domain.RunExecutionPermissionAuto
	scoped.DeclaredOrigins = []string{"https://example.com", "http://localhost:3000"}
	scoped.ScopeFingerprint = scope.Fingerprint
	scoped.Generation = scoped.Fingerprint()
	if err := scoped.Validate(); err != nil {
		t.Fatalf("scoped auto authority rejected: %v", err)
	}
	ask := scoped
	ask.PermissionMode = domain.RunExecutionPermissionAsk
	ask.Generation = ask.Fingerprint()
	if err := ask.Validate(); err != nil {
		t.Fatalf("scoped ask authority rejected: %v", err)
	}
	unscopedAsk := base
	unscopedAsk.PermissionMode = domain.RunExecutionPermissionAsk
	unscopedAsk.Generation = unscopedAsk.Fingerprint()
	if err := unscopedAsk.Validate(); err == nil {
		t.Fatal("unscoped ask authority accepted")
	}
	full := scoped
	full.PermissionMode = domain.RunExecutionPermissionFullAccess
	full.Generation = full.Fingerprint()
	if err := full.Validate(); err == nil || !strings.Contains(err.Error(), "scope binding") {
		t.Fatalf("scoped Full authority accepted: %v", err)
	}
	tampered := scoped
	tampered.ScopeFingerprint = strings.Repeat("0", 64)
	tampered.Generation = tampered.Fingerprint()
	if err := tampered.Validate(); err == nil || !strings.Contains(err.Error(), "declared origins") {
		t.Fatalf("tampered scope fingerprint accepted: %v", err)
	}
	missingFingerprint := scoped
	missingFingerprint.ScopeFingerprint = ""
	missingFingerprint.Generation = missingFingerprint.Fingerprint()
	if err := missingFingerprint.Validate(); err == nil {
		t.Fatal("origins without a scope fingerprint accepted")
	}
	widened := scoped
	widened.DeclaredOrigins = append(append([]string(nil), scoped.DeclaredOrigins...), "https://other.test")
	widened.Generation = widened.Fingerprint()
	if err := widened.Validate(); err == nil || !strings.Contains(err.Error(), "declared origins") {
		t.Fatalf("widened origin set accepted: %v", err)
	}
	if scoped.Fingerprint() == base.Fingerprint() {
		t.Fatal("the declared scope must enter the authority fingerprint")
	}
	other := scoped
	other.DeclaredOrigins = []string{"https://example.com"}
	if scoped.Equal(other) || !scoped.Equal(scoped) {
		t.Fatal("authority equality ignores the declared scope")
	}
	encoded, err := json.Marshal(scoped)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAgentBrowserAuthority(encoded)
	if err != nil || !decoded.Equal(scoped) {
		t.Fatalf("scoped authority decode %v %+v", err, decoded)
	}
}
