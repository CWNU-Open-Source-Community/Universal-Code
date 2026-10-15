package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/toolcontract"
)

func TestContentManagerApprovedReadTemplateAndPrompt(t *testing.T) {
	for _, kind := range []string{"resource", "template", "prompt"} {
		for _, scenario := range []string{"success", "denied", "deny_send", "drift", "cancel", "input_required"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				var calls atomic.Int32
				var drift atomic.Bool
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost {
						w.WriteHeader(404)
						return
					}
					var req Envelope
					if json.NewDecoder(r.Body).Decode(&req) != nil {
						w.WriteHeader(400)
						return
					}
					if len(req.ID) == 0 {
						w.WriteHeader(202)
						return
					}
					raw := "{}"
					switch req.Method {
					case "initialize":
						raw = `{"protocolVersion":"2025-06-18","capabilities":{"resources":{},"prompts":{}},"serverInfo":{"name":"fixture","version":"1"}}`
					case "resources/list":
						raw = `{"resources":[{"uri":"fixture://readme","name":"Readme"}]}`
					case "resources/templates/list":
						raw = `{"resourceTemplates":[{"uriTemplate":"fixture://files/{path}","name":"Files"}]}`
					case "prompts/list":
						raw = `{"prompts":[{"name":"review","arguments":[{"name":"language","required":true}]}]}`
						if drift.Load() {
							raw = `{"prompts":[{"name":"review","arguments":[{"name":"changed","required":true}]}]}`
						}
					case "resources/read", "prompts/get":
						calls.Add(1)
						var params map[string]any
						_ = json.Unmarshal(req.Params, &params)
						if kind == "resource" && params["uri"] != "fixture://readme" {
							t.Errorf("wrong URI: %v", params)
						}
						if kind == "template" && params["uri"] != "fixture://files/a%2Fb" {
							t.Errorf("template not expanded: %v", params)
						}
						if kind == "prompt" && params["name"] != "review" {
							t.Errorf("wrong prompt: %v", params)
						}
						if scenario == "cancel" {
							cancel()
							<-r.Context().Done()
							return
						}
						if req.Method == "resources/read" {
							raw = `{"contents":[{"uri":"fixture://readme","text":"AKIAABCDEFGHIJKLMNOP"}],"vendor":9007199254740993}`
						} else {
							raw = `{"messages":[{"role":"user","content":{"type":"text","text":"AKIAABCDEFGHIJKLMNOP"}}],"vendor":9007199254740993}`
						}
						if scenario == "input_required" {
							raw = `{"resultType":"input_required","requestState":"opaque","inputRequests":{}}`
						}
					}
					sdkWriteResponse(w, req.ID, raw)
				}))
				defer server.Close()
				st := &clientStoreFake{}
				manager, err := NewClientManager(st, nil, ManagerOptions{HTTPClient: server.Client()})
				if err != nil {
					t.Fatal(err)
				}
				descriptor := ServerDescriptor{ProtocolVersion: ClientProtocolVersion, ID: "content", Name: "Content", Transport: TransportStreamableHTTP, Target: server.URL, DeclaredCapabilities: []CapabilityKind{CapabilityResources, CapabilityPrompts}, Scope: ScopeRun, RunID: "run-1", WorkspaceID: "workspace-1", Source: Source{Kind: "manual", URI: "operator"}, CallTimeoutMillis: 5000, MaxResultBytes: 8192}
				record, _, err := manager.Stage(t.Context(), descriptor)
				if err != nil {
					t.Fatal(err)
				}
				record, err = manager.Review(t.Context(), descriptor.ID, ReviewRequest{Action: ReviewApproveDiscovery, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ReviewedBy: "operator"})
				if err != nil {
					t.Fatal(err)
				}
				record, err = manager.Refresh(t.Context(), descriptor.ID)
				if err != nil {
					t.Fatal(err)
				}
				record, err = manager.Review(t.Context(), descriptor.ID, ReviewRequest{Action: ReviewEnableCapabilities, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ExpectedCapabilityFingerprint: record.Capabilities.Fingerprint, ReviewedBy: "operator"})
				if err != nil {
					t.Fatal(err)
				}
				caps, err := manager.Capabilities(t.Context(), "run-1", "workspace-1")
				if err != nil || len(caps.Servers) != 1 || len(caps.Servers[0].ContentChoices()) != 3 {
					t.Fatalf("content not exposed: %+v %v", caps, err)
				}
				method, name, args := "resources/read", "resources.read", `{"uri":"fixture://readme"}`
				if kind == "template" {
					args = `{"uri_template":"fixture://files/{path}","variables":{"path":"a/b"}}`
				}
				if kind == "prompt" {
					method, name, args = "prompts/get", "prompts.get", `{"name":"review","arguments":{"language":"Go"}}`
				}
				var callChecks atomic.Int32
				authorizer := executionauth.NewPolicyAuthorizer(func(ctx context.Context, _ executionauth.SubjectRef, operation toolcontract.Operation, _ string) (executionauth.OperationAuthority, error) {
					if scenario == "denied" || (scenario == "deny_send" && operation.Kind == toolcontract.OperationToolCall && callChecks.Add(1) > 1) {
						return executionauth.OperationAuthority{}, errors.New("host denied")
					}
					return executionauth.OperationAuthority{Mode: domain.ExecutionApprovalFull, BindingFingerprint: strings.Repeat("a", 64), RuntimeAvailable: true, FullActivated: true}, ctx.Err()
				})
				if scenario == "drift" {
					drift.Store(true)
				}
				request := InvokeRequest{RunID: "run-1", WorkspaceID: "workspace-1", ServerID: descriptor.ID, Method: method, ToolName: name, Arguments: json.RawMessage(args), CapabilityFingerprint: record.Capabilities.Fingerprint, OperationID: "content-op", Subject: executionauth.SubjectRef{RunID: "run-1", ActorID: "agent-1"}, Authorizer: authorizer}
				result, err := manager.Invoke(ctx, request)
				switch scenario {
				case "success":
					if err != nil || calls.Load() != 1 || strings.Contains(result.Content, "AKIAABCDEFGHIJKLMNOP") || !strings.Contains(result.Content, "9007199254740993") {
						t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
					}
				case "denied", "deny_send", "drift":
					if err == nil || calls.Load() != 0 {
						t.Fatalf("unapproved dispatch: %d %v", calls.Load(), err)
					}
					if scenario == "deny_send" {
						record, loadErr := st.GetMCPClientServer(t.Context(), descriptor.ID)
						if loadErr != nil || record.Health != HealthHealthy {
							t.Fatalf("authority rejection incorrectly marked server unavailable: %+v %v", record, loadErr)
						}
					}
				case "cancel":
					receipt, ok := InvocationReceipt(err)
					if !ok || receipt.State != toolcontract.ReceiptOutcomeUnknown || calls.Load() != 1 || !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel receipt=%+v calls=%d err=%v", receipt, calls.Load(), err)
					}
				case "input_required":
					var required *sdkInputRequiredError
					receipt, ok := InvocationReceipt(err)
					if !ok || receipt.State != toolcontract.ReceiptResultReceived || !errors.As(err, &required) || calls.Load() != 1 {
						t.Fatalf("input-required receipt=%+v calls=%d err=%v", receipt, calls.Load(), err)
					}
				}
				if len(st.audits) != 1 {
					t.Fatalf("missing durable audit: %+v", st.audits)
				}
			})
		}
	}
}

func TestContentValidationAndHistoricalFingerprint(t *testing.T) {
	snapshot, err := NewCapabilitySnapshot("old", "1", []CapabilityKind{CapabilityPrompts}, nil, nil, []RemotePrompt{{Name: "legacy"}}, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	// Frozen old field order and absence of new optional fields preserve v1 hashes.
	old := fmt.Sprintf(`{"protocol_version":"%s","server_name":"old","server_version":"1","negotiated":["prompts"],"tools":null,"resources":null,"prompts":[{"name":"legacy"}],"fingerprint":"","discovered_at":"0001-01-01T00:00:00Z"}`, ProtocolVersion)
	if snapshot.Fingerprint != digestBytes([]byte(old)) {
		t.Fatal("historical snapshot fingerprint changed")
	}
	snapshot.Prompts = []RemotePrompt{{Name: "review", Arguments: []RemotePromptArgument{{Name: "language", Required: true}}}}
	snapshot.Resources = []RemoteResource{{URI: "fixture://known", Name: "Known"}}
	snapshot.ResourceTemplates = []RemoteResourceTemplate{{URITemplate: "fixture://{name}", Name: "Template"}}
	for _, tc := range []struct{ method, name, args string }{
		{"resources/read", "resources.read", `{"uri":"file:///private"}`},
		{"resources/read", "resources.read", `{"uri_template":"fixture://{unknown}","variables":{}}`},
		{"resources/read", "resources.read", `{"uri_template":"fixture://{name}","variables":{"other":"x"}}`},
		{"prompts/get", "prompts.get", `{"name":"review","arguments":{}}`},
		{"prompts/get", "prompts.get", `{"name":"review","arguments":{"language":"Go","extra":"x"}}`},
		{"prompts/get", "prompts.get", `{"name":"review","arguments":{"language":42}}`},
		{"prompts/get", "prompts.get", `{"name":"review","arguments":{"language":null}}`},
		{"resources/read", "resources.read", `{"uri_template":"fixture://{name}","variables":null}`},
		{"prompts/get", "prompts.get", `{"name":"review","arguments":{"language":"Go"},"requestState":"forged"}`},
	} {
		if _, err := ValidateInvocation(snapshot, tc.method, tc.name, json.RawMessage(tc.args)); err == nil {
			t.Errorf("accepted %s", tc.args)
		}
	}
}
