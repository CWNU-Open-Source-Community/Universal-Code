package mcp

import (
	"context"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/toolcontract"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitInput(t *testing.T, b *InputBroker) InputView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		items, err := b.List(t.Context(), "run-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 1 {
			return items[0]
		}
		time.Sleep(time.Millisecond * 5)
	}
	t.Fatal("input was not published")
	return InputView{}
}
func TestInputBrokerValidationAndSingleConsumption(t *testing.T) {
	for _, mode := range []string{"form", "url"} {
		t.Run(mode, func(t *testing.T) {
			b := NewInputBroker()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			params := &sdk.ElicitParams{Message: "Choose", RequestedSchema: json.RawMessage(`{"type":"object","properties":{"color":{"type":"string","oneOf":[{"const":"blue","title":"Blue"}],"default":"blue"},"tags":{"type":"array","items":{"anyOf":[{"const":"one","title":"One"},{"const":"two","title":"Two"}]},"uniqueItems":true}},"required":["color"]}`)}
			if mode == "url" {
				params = &sdk.ElicitParams{Mode: "url", URL: "https://auth.example/approve?state=opaque", Message: "Authorize"}
			}
			done := make(chan error, 1)
			go func() {
				answer, err := b.request(ctx, inputBinding{runID: "run-1", operationID: "op-1"}, params, func(ctx context.Context) error { return ctx.Err() })
				if err == nil && answer.Action != "accept" {
					err = errors.New("wrong answer")
				}
				done <- err
			}()
			item := waitInput(t, b)
			response := InputResponse{Version: InputProtocolVersion, Action: "accept"}
			if mode == "form" {
				response.Content = json.RawMessage(`{"color":"wrong"}`)
				if b.Respond(t.Context(), "run-1", item.ID, response) == nil {
					t.Fatal("invalid selection accepted")
				}
				response.Content = json.RawMessage(`{"color":"blue","tags":["one"]}`)
			} else if item.Host != "auth.example" || item.URL != params.URL {
				t.Fatal("external authorization target missing")
			}
			if b.Respond(t.Context(), "other-run", item.ID, response) == nil {
				t.Fatal("cross-run response accepted")
			}
			if err := b.Respond(t.Context(), "run-1", item.ID, response); err != nil {
				t.Fatal(err)
			}
			if b.Respond(t.Context(), "run-1", item.ID, response) == nil {
				t.Fatal("response replayed")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			items, _ := b.List(t.Context(), "run-1")
			if len(items) != 0 {
				t.Fatal("answered request retained")
			}
		})
	}
	for _, url := range []string{"http://auth.example/", "javascript:alert(1)", "https://user:pass@auth.example/", "data:text/plain,test", "file:///secret"} {
		if _, _, err := prepareInput(&sdk.ElicitParams{Mode: "url", URL: url}); err == nil {
			t.Fatalf("unsafe URL accepted: %s", url)
		}
	}
	for _, schema := range []string{`{"type":"object","properties":{"password":{"type":"string"}}}`, `{"type":"object","properties":{"data":{"type":"object"}}}`, `{"type":"object","properties":{"data":{"type":"string","$ref":"https://invalid/schema"}}}`} {
		if _, _, err := prepareInput(&sdk.ElicitParams{RequestedSchema: json.RawMessage(schema)}); err == nil {
			t.Fatal("unsupported form accepted")
		}
	}
}
func TestInputBrokerCancelAndRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{true: "revoked", false: "cancelled"}[revoke], func(t *testing.T) {
			b := NewInputBroker()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var revoked atomic.Bool
			done := make(chan error, 1)
			go func() {
				_, err := b.request(ctx, inputBinding{runID: "run-1", operationID: "op"}, &sdk.ElicitParams{Mode: "url", URL: "https://example.test/"}, func(ctx context.Context) error {
					if revoked.Load() {
						return errors.New("authority revoked")
					}
					return ctx.Err()
				})
				done <- err
			}()
			item := waitInput(t, b)
			if revoke {
				revoked.Store(true)
			} else {
				cancel()
			}
			if b.Respond(t.Context(), "run-1", item.ID, InputResponse{Version: InputProtocolVersion, Action: "accept"}) == nil {
				t.Fatal("stale request accepted")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stale request completed")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stale request remained live")
			}
		})
	}
}
func TestInputLegacyRequiresDispatchedAttemptAndPausesNetworkClock(t *testing.T) {
	b := NewInputBroker()
	c := newSDKClient(nil)
	defer c.Close()
	c.inputHandler = func(ctx context.Context, p *sdk.ElicitParams) (*sdk.ElicitResult, error) {
		return b.request(ctx, inputBinding{runID: "run-1", operationID: "op"}, p, func(ctx context.Context) error { return ctx.Err() })
	}
	req := &sdk.ElicitRequest{Params: &sdk.ElicitParams{Mode: "url", URL: "https://example.test/"}}
	if _, err := c.handleLegacyInput(t.Context(), req); err == nil {
		t.Fatal("unsolicited input accepted")
	}
	ctx, stop := timedInputCall(t.Context(), 100*time.Millisecond)
	defer stop()
	attempt := &sdkCallAttempt{}
	clear := c.activateInput(ctx, attempt)
	defer clear()
	if _, err := c.handleLegacyInput(t.Context(), req); err == nil {
		t.Fatal("pre-dispatch input accepted")
	}
	attempt.dispatched.Store(true)
	id, _ := jsonrpc.MakeID("legacy-q")
	c.receiveLegacyInput(&jsonrpc.Request{ID: id, Method: "elicitation/create"})
	done := make(chan error, 1)
	go func() { _, err := c.handleLegacyInput(t.Context(), req); done <- err }()
	item := waitInput(t, b)
	time.Sleep(150 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("operator wait consumed network timeout")
	}
	if err := b.Respond(t.Context(), "run-1", item.ID, InputResponse{Version: InputProtocolVersion, Action: "decline"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestInputManagerExplicitContinuation(t *testing.T) {
	for _, method := range []string{"tools/call", "resources/read", "prompts/get"} {
		for _, action := range []string{"accept", "decline", "cancel", "revoke", "stop", "lost", "deny_continuation"} {
			t.Run(method+"/"+action, func(t *testing.T) {
				var calls atomic.Int32
				var revoked atomic.Bool
				var continuationChecks atomic.Int32
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
					var raw string
					switch req.Method {
					case "server/discover":
						raw = `{"supportedVersions":["2026-07-28"],"capabilities":{"tools":{},"resources":{},"prompts":{}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"fixture","version":"1"}},"resultType":"complete","ttlMs":0,"cacheScope":"private"}`
					case "initialize":
						raw = `{"protocolVersion":"2026-07-28","capabilities":{"tools":{},"resources":{},"prompts":{}},"serverInfo":{"name":"fixture","version":"1"}}`
					case "tools/list":
						raw = `{"tools":[{"name":"lookup","inputSchema":{"type":"object"}}],"resultType":"complete","ttlMs":0,"cacheScope":"private"}`
					case "resources/list":
						raw = `{"resources":[{"uri":"fixture://item","name":"Item"}],"resultType":"complete","ttlMs":0,"cacheScope":"private"}`
					case "resources/templates/list":
						raw = `{"resourceTemplates":[],"resultType":"complete","ttlMs":0,"cacheScope":"private"}`
					case "prompts/list":
						raw = `{"prompts":[{"name":"review"}],"resultType":"complete","ttlMs":0,"cacheScope":"private"}`
					case "tools/call", "resources/read", "prompts/get":
						if req.Method != method {
							t.Errorf("method changed to %s", req.Method)
						}
						count := calls.Add(1)
						var params map[string]json.RawMessage
						_ = json.Unmarshal(req.Params, &params)
						if count == 1 {
							if len(params["inputResponses"]) != 0 {
								t.Error("initial call had answers")
							}
							raw = `{"resultType":"input_required","requestState":"opaque-state","inputRequests":{"question":{"method":"elicitation/create","params":{"mode":"form","message":"Choose a label","requestedSchema":{"type":"object","properties":{"label":{"type":"string"}},"required":["label"]}}}}}`
						} else {
							if count != 2 || string(params["requestState"]) != `"opaque-state"` {
								t.Error("wrong continuation state or count")
							}
							var answers map[string]sdk.ElicitResult
							_ = json.Unmarshal(params["inputResponses"], &answers)
							expectedAction := action
							if action == "lost" || action == "deny_continuation" {
								expectedAction = "accept"
							}
							if answers["question"].Action != expectedAction {
								t.Errorf("wrong action: %+v", answers)
							}
							if method == "tools/call" && (string(params["name"]) != `"lookup"` || string(params["arguments"]) != `{"original":9007199254740993}`) {
								t.Error("tool inputs changed")
							}
							if method == "resources/read" && string(params["uri"]) != `"fixture://item"` {
								t.Error("resource changed")
							}
							if method == "prompts/get" && string(params["name"]) != `"review"` {
								t.Error("prompt changed")
							}
							if action == "lost" {
								conn, _, err := w.(http.Hijacker).Hijack()
								if err == nil {
									_ = conn.Close()
								}
								return
							}
							raw = `{"resultType":"complete","content":[{"type":"text","text":"operator-private-answer"}]}`
							if method == "resources/read" {
								raw = `{"resultType":"complete","contents":[{"uri":"fixture://item","text":"operator-private-answer"}]}`
							}
							if method == "prompts/get" {
								raw = `{"resultType":"complete","messages":[{"role":"user","content":{"type":"text","text":"operator-private-answer"}}]}`
							}
						}
					default:
						t.Errorf("unexpected method %s", req.Method)
						raw = `{}`
					}
					sdkWriteResponse(w, req.ID, raw)
				}))
				defer server.Close()
				st := &clientStoreFake{}
				broker := NewInputBroker()
				manager, err := NewClientManager(st, nil, ManagerOptions{HTTPClient: server.Client(), InputBroker: broker})
				if err != nil {
					t.Fatal(err)
				}
				d := ServerDescriptor{ProtocolVersion: ClientProtocolVersion, ID: "input-server", Name: "Input server", Transport: TransportStreamableHTTP, Target: server.URL, DeclaredCapabilities: []CapabilityKind{CapabilityTools, CapabilityResources, CapabilityPrompts}, Scope: ScopeRun, RunID: "run-1", WorkspaceID: "workspace-1", Source: Source{Kind: "manual", URI: "operator"}, CallTimeoutMillis: 5000, MaxResultBytes: 8192}
				record, _, err := manager.Stage(t.Context(), d)
				if err != nil {
					t.Fatal(err)
				}
				record, err = manager.Review(t.Context(), d.ID, ReviewRequest{Action: ReviewApproveDiscovery, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ReviewedBy: "operator"})
				if err != nil {
					t.Fatal(err)
				}
				record, err = manager.Refresh(t.Context(), d.ID)
				if err != nil {
					t.Fatal(err)
				}
				record, err = manager.Review(t.Context(), d.ID, ReviewRequest{Action: ReviewEnableCapabilities, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ExpectedCapabilityFingerprint: record.Capabilities.Fingerprint, ReviewedBy: "operator"})
				if err != nil {
					t.Fatal(err)
				}
				authorizer := executionauth.NewPolicyAuthorizer(func(ctx context.Context, _ executionauth.SubjectRef, operation toolcontract.Operation, _ string) (executionauth.OperationAuthority, error) {
					if action == "deny_continuation" && strings.HasPrefix(operation.ID, "mcp-continue-") && continuationChecks.Add(1) > 1 {
						return executionauth.OperationAuthority{}, errors.New("continuation revoked before wire")
					}
					if revoked.Load() {
						return executionauth.OperationAuthority{}, errors.New("revoked")
					}
					return executionauth.OperationAuthority{Mode: domain.ExecutionApprovalFull, BindingFingerprint: strings.Repeat("a", 64), RuntimeAvailable: true, FullActivated: true}, ctx.Err()
				})
				name, args, wireMethod := "lookup", `{"original":9007199254740993}`, ""
				if method == "resources/read" {
					name, args, wireMethod = "resources.read", `{"uri":"fixture://item"}`, method
				}
				if method == "prompts/get" {
					name, args, wireMethod = "prompts.get", `{"name":"review"}`, method
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					result, err := manager.Invoke(ctx, InvokeRequest{RunID: "run-1", WorkspaceID: "workspace-1", ServerID: d.ID, ToolName: name, Method: wireMethod, Arguments: json.RawMessage(args), CapabilityFingerprint: record.Capabilities.Fingerprint, OperationID: "invocation-1", Subject: executionauth.SubjectRef{RunID: "run-1", ActorID: "agent-1"}, Authorizer: authorizer})
					if err == nil && action == "accept" && strings.Contains(result.Content, "operator-private-answer") {
						err = errors.New("answer leaked into result")
					}
					done <- err
				}()
				item := waitInput(t, broker)
				if action == "revoke" {
					revoked.Store(true)
				} else if action == "stop" {
					cancel()
				} else {
					response := InputResponse{Version: InputProtocolVersion, Action: action}
					if action == "lost" || action == "deny_continuation" {
						response.Action = "accept"
					}
					if response.Action == "accept" {
						response.Content = json.RawMessage(`{"label":"operator-private-answer"}`)
					}
					if err := broker.Respond(t.Context(), "run-1", item.ID, response); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case err := <-done:
					if action == "lost" {
						receipt, ok := InvocationReceipt(err)
						if !ok || receipt.State != toolcontract.ReceiptOutcomeUnknown || receipt.OperationID != "invocation-1" || calls.Load() != 2 {
							t.Fatalf("unknown continuation outcome: %+v %v", receipt, err)
						}
					} else if action == "revoke" || action == "stop" || action == "deny_continuation" {
						receipt, ok := InvocationReceipt(err)
						if err == nil || !ok || receipt.State != toolcontract.ReceiptResultReceived || calls.Load() != 1 {
							t.Fatalf("stopped invocation: calls=%d receipt=%+v err=%v", calls.Load(), receipt, err)
						}
					} else if err != nil || calls.Load() != 2 {
						t.Fatalf("continuation: calls=%d err=%v", calls.Load(), err)
					}
				case <-time.After(8 * time.Second):
					t.Fatal("invocation remained blocked")
				}
			})
		}
	}
}
func TestInputLegacySDKServerRoundTrip(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "legacy-input", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "lookup", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		answer, err := req.Session.Elicit(ctx, &sdk.ElicitParams{Message: "Choose color", RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"color": map[string]any{"type": "string"}}, "required": []string{"color"}}})
		if err != nil {
			return nil, err
		}
		if answer.Action != "accept" || answer.Content["color"] != "blue" {
			return nil, errors.New("wrong legacy answer")
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "completed"}}}, nil
	})
	peer := httptest.NewTLSServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	defer peer.Close()
	launch := toolcontract.ResolvedLaunch{Component: toolcontract.ComponentRef{PackageID: "fixture", ComponentID: "legacy-input"}, InstanceID: "instance", Format: "mcp-client", FormatVersion: "1", ProtocolVersions: []string{"2025-11-25"}, Transport: toolcontract.TransportStreamableHTTP, HTTP: &toolcontract.HTTPLaunch{Endpoint: peer.URL}}
	probe := &resolvedGuardProbe{}
	client := resolvedFixtureClient(t, launch, probe, peer.Client())
	b := NewInputBroker()
	client.inputHandler = func(ctx context.Context, p *sdk.ElicitParams) (*sdk.ElicitResult, error) {
		return b.request(ctx, inputBinding{runID: "run-1", operationID: "legacy-op"}, p, func(ctx context.Context) error { return ctx.Err() })
	}
	if _, err := client.DiscoverWithScope(t.Context(), testResolvedScope(t, launch, 3)); err != nil {
		t.Fatal(err)
	}
	operation := resolvedFixtureCall(t, client, launch, `{}`)
	done := make(chan error, 1)
	ctx, stop := timedInputCall(t.Context(), time.Second)
	defer stop()
	go func() {
		_, _, receipt, err := client.CallToolWithReceipt(ctx, operation, "lookup", json.RawMessage(`{}`))
		if err == nil && receipt.State != toolcontract.ReceiptResultReceived {
			err = errors.New("missing legacy receipt")
		}
		done <- err
	}()
	item := waitInput(t, b)
	if err := b.Respond(t.Context(), "run-1", item.ID, InputResponse{Version: InputProtocolVersion, Action: "accept", Content: json.RawMessage(`{"color":"blue"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if probe.calls.Load() != 1 {
		t.Fatal("legacy callback repeated tools/call")
	}
}
