//go:build windows

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/desktopbrowser"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
)

// Only model responses are scripted. Tools run through the real AgentRunner,
// durable gateway, command worker and native shared page, with no paid model.
type nativeBrowserToolProvider struct {
	vision    llm.VisionSupport
	requests  []llm.ChatRequest
	respond   func(context.Context, llm.ChatRequest, int) (*llm.ChatResponse, error)
	lastError error
}

func (*nativeBrowserToolProvider) Name() string { return "native-browser-tools" }
func (*nativeBrowserToolProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model"}}, nil
}
func (*nativeBrowserToolProvider) SupportsTools(string) bool    { return true }
func (*nativeBrowserToolProvider) SupportsJSONMode(string) bool { return false }
func (p *nativeBrowserToolProvider) SupportsVision(string) bool {
	return p.vision == llm.VisionSupported
}
func (p *nativeBrowserToolProvider) DescribeVision(string) llm.VisionCapability {
	return llm.VisionCapability{State: p.vision, Source: "test_provider_metadata"}
}
func (p *nativeBrowserToolProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	index := len(p.requests)
	p.requests = append(p.requests, req)
	response, err := p.respond(ctx, req, index)
	p.lastError = err
	return response, err
}
func (p *nativeBrowserToolProvider) StreamChat(ctx context.Context, req llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan llm.ChatChunk, 2)
	if response.Text != "" {
		ch <- llm.ChatChunk{Text: response.Text}
	}
	ch <- llm.FinalChatChunk(response)
	close(ch)
	return ch, nil
}

func nativeBrowserToolResult(req llm.ChatRequest, name string, out any) error {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		for _, result := range req.Messages[i].ToolResults {
			var envelope struct {
				Tool   string `json:"tool"`
				Stdout string `json:"stdout"`
			}
			if json.Unmarshal([]byte(result.Content), &envelope) != nil || envelope.Tool != name {
				continue
			}
			if result.IsError {
				return fmt.Errorf("%s failed: %s", name, result.Content)
			}
			return json.Unmarshal([]byte(envelope.Stdout), out)
		}
	}
	return fmt.Errorf("missing real %s result", name)
}

func TestAgentToolsPreviewWorkspaceInNativeSharedBrowser(t *testing.T) {
	if os.Getenv("UC_NATIVE_BROWSER_PROBE") != "1" {
		t.Skip("set UC_NATIVE_BROWSER_PROBE=1 for hidden native tool integration")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the real command-owned local preview")
	}
	for _, vision := range []llm.VisionSupport{llm.VisionSupported, llm.VisionUnsupported} {
		t.Run(string(vision), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
			defer cancel()
			ui := startSharedBrowserNativeUI(t)
			home := t.TempDir()
			root := filepath.Join(home, "workspace")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			html := `<!doctype html><title>Agent tool preview</title><h1>Workspace preview</h1><button onclick="document.querySelector('output').textContent='Clicked by agent'">Verify preview</button><output>Not clicked</output>`
			if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(html), 0600); err != nil {
				t.Fatal(err)
			}
			server := `const http=require('node:http'),fs=require('node:fs');const s=http.createServer((q,r)=>{r.setHeader('Content-Type','text/html');r.end(fs.readFileSync('index.html'))});s.listen(0,'127.0.0.1',()=>console.log('http://127.0.0.1:'+s.address().port+'/'));`
			if err := os.WriteFile(filepath.Join(root, "server.cjs"), []byte(server), 0600); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(filepath.Join(home, "tools.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.SaveWorkspace(ctx, store.WorkspaceRecord{ID: "native-tool-workspace", Name: "Native tool workspace", RootPath: root, CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			runs := application.NewRunService(st)
			_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "Preview a local HTML file using actual Agent tools", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: "native-tool-workspace", ModelRoute: "native-browser-tools/model", BrowserNetworkAccess: domain.BrowserNetworkInternet, Budget: domain.Budget{MaxTurns: 3, MaxToolCalls: 12}})
			if err != nil {
				t.Fatal(err)
			}
			caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
			if _, err := application.NewRunExecutionProfileService(st).Change(ctx, application.ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local", OperationKey: "native-tool-local-profile", RequestedBy: "test_operator", Reason: "Use the installed host command runtime"}); err != nil {
				t.Fatal(err)
			}
			selected, err := application.NewRunExecutionPermissionService(st, caps).Change(ctx, application.ChangeRunExecutionPermissionRequest{RunID: run.ID, Mode: "full", OperationKey: "native-tool-full-permission", RequestedBy: "test_operator", Reason: "Run the isolated local preview and browser tools", ConfirmFull: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, live := caps.FullAccessGeneration(selected.Permission); !live {
				if _, err := caps.RuntimeAuthority.ActivateRunFullAccess(selected.Permission); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := runs.Start(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			shared, err := newSharedBrowserService(st, SharedBrowserOptions{HomePath: home, CreateHost: func(ctx context.Context, profile string, onEvent func(desktopbrowser.Event)) (*desktopbrowser.Host, error) {
				return desktopbrowser.NewHost(ctx, desktopbrowser.HostOptions{ParentHWND: ui.hwnd, Dispatcher: ui.dispatcher, UserDataDir: profile, OnEvent: onEvent})
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if err := shared.Close(cleanup); err != nil {
					t.Error(err)
				}
			})
			view, err := shared.Open(ctx, SharedBrowserOpenRequest{WorkspaceID: "native-tool-workspace", RunID: run.ID})
			if err != nil {
				t.Fatal(err)
			}
			session, err := shared.session(view.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			page, ok := session.host.Tab(view.ActiveTabID)
			if !ok {
				t.Fatal("host created no native tab")
			}
			// The root HWND stays hidden; this only enables rendering in its child.
			if err := page.SetSurface(ctx, desktopbrowser.Bounds{Width: 800, Height: 600}, true); err != nil {
				t.Fatal(err)
			}
			manager, err := runner.NewPlatformCommandRuntimeManager(st, idgen.New("native-preview-command-owner"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if err := manager.Shutdown(cleanup); err != nil {
					t.Error(err)
				}
			})
			commands, err := application.NewCommandRuntimeService(st, manager, caps)
			if err != nil {
				t.Fatal(err)
			}
			browser := application.NewAgentBrowserService(st, application.AgentBrowserOptions{HomePath: home, Capabilities: caps, SharedPage: shared.attachPage, ServiceReadiness: commands, SessionLifetime: 45 * time.Second})
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if err := browser.Shutdown(cleanup); err != nil {
					t.Error(err)
				}
			})
			provider := &nativeBrowserToolProvider{vision: vision}
			var artifactPath string
			provider.respond = func(callCtx context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				response := &llm.ChatResponse{Provider: provider.Name(), Model: "model", Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}
				add := func(name string, args any) {
					raw, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					response.ToolCalls = append(response.ToolCalls, llm.ToolCall{ID: fmt.Sprintf("native-tool-%d-%d", index, len(response.ToolCalls)), Name: name, Arguments: raw})
				}
				switch index {
				case 0:
					for _, name := range []string{"workspace_read", "command_runtime", "browser_navigate", "browser_snapshot", "browser_click", "browser_screenshot"} {
						found := false
						for _, tool := range req.Tools {
							found = found || tool.Name == name
						}
						if !found {
							return nil, fmt.Errorf("real request omitted %s", name)
						}
					}
					add("workspace_read", map[string]any{"version": "agent-code-tools.v1", "path": "index.html", "start_line": 1, "end_line": 10})
					add("command_runtime", map[string]any{"version": "command-runtime.v2", "action": "start", "commands": []any{map[string]any{"version": "command-runtime.v2", "workspace_mode": "shared_service", "profile": "process", "executable": node, "arguments": []string{"server.cjs"}, "working_directory": ".", "environment": []any{}, "stdin_policy": "closed", "close_initial_stdin": true, "timeout_milliseconds": 40000, "output": map[string]any{"inline_bytes": 4096, "artifact_bytes": 4096}, "network": "host", "credentials": "none", "purpose": "Serve the temporary workspace HTML on loopback for preview"}}})
				case 1:
					var read map[string]any
					if err := nativeBrowserToolResult(req, "workspace_read", &read); err != nil {
						return nil, err
					}
					if encoded, _ := json.Marshal(read); !strings.Contains(string(encoded), "Workspace preview") {
						return nil, errors.New("workspace_read did not return the actual file")
					}
					var started struct {
						Jobs []struct {
							ID string `json:"id"`
						} `json:"jobs"`
					}
					if err := nativeBrowserToolResult(req, "command_runtime", &started); err != nil {
						return nil, err
					}
					if len(started.Jobs) != 1 || started.Jobs[0].ID == "" {
						return nil, fmt.Errorf("command tool did not return its job: %+v", started)
					}
					// Wait on the very same owned Job to make startup deterministic;
					// browser_navigate still independently verifies listener ownership.
					var url string
					for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && url == ""; {
						job, output, err := manager.Wait(callCtx, started.Jobs[0].ID, 100*time.Millisecond, 0, 4096)
						if err != nil {
							return nil, err
						}
						if job.State.Terminal() {
							return nil, fmt.Errorf("preview server terminated: %+v", job)
						}
						for _, frame := range output.Frames {
							if strings.HasPrefix(strings.TrimSpace(frame.Text), "http://127.0.0.1:") {
								url = strings.TrimSpace(frame.Text)
							}
						}
					}
					if url == "" {
						return nil, errors.New("real command produced no listening URL")
					}
					add("browser_navigate", map[string]any{"version": "browser_navigate.v2", "url": url, "service_job_id": started.Jobs[0].ID})
					add("browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
				case 2:
					var snap browserruntime.AgentBrowserSnapshot
					if err := nativeBrowserToolResult(req, "browser_snapshot", &snap); err != nil {
						return nil, err
					}
					if !strings.Contains(snap.Text, "Workspace preview") {
						return nil, fmt.Errorf("native snapshot did not see served file: %+v", snap)
					}
					add("browser_click", map[string]any{"version": "browser_click.v2", "snapshot_id": snap.SnapshotID, "target": map[string]any{"name": "Verify preview", "role": "button"}})
				case 3:
					var receipt browserruntime.AgentBrowserInteraction
					if err := nativeBrowserToolResult(req, "browser_click", &receipt); err != nil {
						return nil, err
					}
					if !receipt.Dispatched {
						return nil, fmt.Errorf("native click was not dispatched: %+v", receipt)
					}
					add("browser_screenshot", map[string]any{"version": "browser_screenshot.v2"})
					add("browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
				case 4:
					var snap browserruntime.AgentBrowserSnapshot
					if err := nativeBrowserToolResult(req, "browser_snapshot", &snap); err != nil {
						return nil, err
					}
					if !strings.Contains(snap.Text, "Clicked by agent") {
						return nil, fmt.Errorf("real DOM did not receive Agent input: %+v", snap)
					}
					var shot struct {
						Artifact string `json:"artifact_locator"`
						SHA      string `json:"sha256"`
						Bytes    int64  `json:"bytes"`
					}
					if err := nativeBrowserToolResult(req, "browser_screenshot", &shot); err != nil {
						return nil, err
					}
					if shot.Artifact == "" || shot.SHA == "" || shot.Bytes == 0 {
						return nil, fmt.Errorf("screenshot lacks persisted pixels: %+v", shot)
					}
					artifactPath = shot.Artifact
					images := 0
					hasPixelNote := false
					for _, message := range req.Messages {
						images += len(message.Images)
						hasPixelNote = hasPixelNote || strings.Contains(message.Content, `"pixels_supplied":true`)
						if vision == llm.VisionUnsupported && strings.Contains(message.Content, `"pixels_supplied":false`) {
							hasPixelNote = true
						}
					}
					if (vision == llm.VisionSupported && images != 1) || (vision == llm.VisionUnsupported && images != 0) || !hasPixelNote {
						return nil, fmt.Errorf("model screenshot delivery vision=%s images=%d note=%t", vision, images, hasPixelNote)
					}
					if page.State().URL != snap.CanonicalURL {
						return nil, errors.New("Agent tools navigated a different page than the host tab")
					}
					response.Text = `{"version":"root_lifecycle.v1","action":"wait","message":"Local preview and native interaction verified","reason":"test completed"}`
				default:
					return nil, fmt.Errorf("unexpected additional model call %d", index)
				}
				return response, nil
			}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			agent := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).WithExecutionPermissionCapabilities(caps).WithCommandRuntime(commands).WithAgentBrowser(browser)
			result, err := agent.Step(ctx, run.ID)
			if err != nil {
				rounds, _ := st.ListRunSupervisorToolRoundsPage(context.Background(), run.ID, 0, 8)
				for _, round := range rounds {
					for _, call := range round.Calls {
						if call.Status != domain.SupervisorToolCompleted {
							t.Logf("durable tool %s: status=%s error=%s result=%s", call.ToolName, call.Status, call.ErrorCode, call.ResultJSON)
						}
					}
				}
				t.Fatalf("actual Agent tool execution: %v; scripted provider: %v", err, provider.lastError)
			}
			if result.ToolCalls != 7 || len(provider.requests) != 5 {
				t.Fatalf("unexpected actual tool flow: %+v requests=%d", result, len(provider.requests))
			}
			if artifactPath == "" {
				t.Fatal("missing native screenshot")
			}
			observer := observeSharedBrowserNativePage(t, ctx, page)
			if got := observer.evaluate(`document.querySelector('output').textContent`); got != "Clicked by agent" {
				t.Fatalf("host page differs from Agent result: %s", got)
			}
			// Decode the exact native image delivered to the vision model.
			if vision == llm.VisionSupported {
				for _, message := range provider.requests[4].Messages {
					for _, image := range message.Images {
						decoded, err := png.Decode(bytes.NewReader(image.Data))
						if err != nil {
							t.Fatal(err)
						}
						if decoded.Bounds().Dx() < 100 || decoded.Bounds().Dy() < 100 {
							t.Fatalf("native screenshot has invalid dimensions: %v", decoded.Bounds())
						}
					}
				}
			}
		})
	}
}
