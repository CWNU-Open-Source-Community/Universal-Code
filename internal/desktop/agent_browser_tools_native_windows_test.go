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
	// Automatic segments carry earlier results in the model-visible evidence
	// envelope instead of repeating the previous native function-call exchange.
	for _, prefix := range []string{"Older native tool batches are represented by exact historical receipts;", "Completed tool segments of this exact accepted user input."} {
		for i := len(req.Messages) - 1; i >= 0; i-- {
			content := req.Messages[i].Content
			start := strings.Index(content, "\n{")
			if start < 0 {
				continue
			}
			var evidence struct {
				Content string `json:"content"`
			}
			if json.Unmarshal([]byte(content[start+1:]), &evidence) != nil || !strings.HasPrefix(evidence.Content, prefix) {
				continue
			}
			for _, line := range strings.Split(evidence.Content, "\n") {
				type receipt struct {
					Tool   string          `json:"tool"`
					Status string          `json:"status"`
					Result json.RawMessage `json:"result"`
				}
				var entry struct {
					receipt
					Calls []receipt `json:"calls"`
				}
				if json.Unmarshal([]byte(line), &entry) != nil {
					continue
				}
				for _, call := range append(entry.Calls, entry.receipt) {
					if call.Tool == name && call.Status == "completed" && len(call.Result) > 0 {
						return json.Unmarshal(call.Result, out)
					}
				}
			}
		}
	}
	return fmt.Errorf("missing real %s result", name)
}

// The provider uses the public command-string contract and host facts actually
// supplied to the model. It must not receive a hidden executable path or fill
// the low-level process launch contract on the model's behalf.
func nativeBrowserModelCommandEnvironment(req llm.ChatRequest) error {
	for _, message := range req.Messages {
		if message.Role != "system" {
			continue
		}
		start := strings.Index(message.Content, "\n{")
		if start < 0 {
			continue
		}
		var exposure struct {
			Version     string `json:"version"`
			Environment struct {
				OS          string `json:"os"`
				Shell       string `json:"preferred_shell_profile"`
				Executables []struct {
					Name string `json:"name"`
					Path string `json:"executable"`
				} `json:"process_executables"`
			} `json:"command_environment"`
		}
		if json.Unmarshal([]byte(message.Content[start+1:]), &exposure) != nil || exposure.Version != "root_tool_exposure.v1" {
			continue
		}
		if exposure.Environment.OS != "windows" || exposure.Environment.Shell != "powershell" {
			return errors.New("actual model request omitted the default Windows shell")
		}
		for _, executable := range exposure.Environment.Executables {
			if executable.Name == "node" && filepath.IsAbs(executable.Path) {
				return nil
			}
		}
	}
	return errors.New("actual model request omitted the available Node runtime")
}

func TestAgentToolsPreviewWorkspaceInNativeSharedBrowser(t *testing.T) {
	if os.Getenv("UC_NATIVE_BROWSER_PROBE") != "1" {
		t.Skip("set UC_NATIVE_BROWSER_PROBE=1 for hidden native tool integration")
	}
	if _, err := exec.LookPath("node"); err != nil {
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
			html := `<!doctype html><title>Agent tool preview</title><style>body{background:#9b5713}</style><h1>Workspace preview</h1><button onclick="document.querySelector('output').textContent='Clicked by agent';document.body.style.background='#13579b'">Verify preview</button><output>Not clicked</output>`
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
			// Match Windows OpenControlPlane startup, without a user network toggle.
			if err := st.EnableDesktopBrowserDefaults(ctx); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveWorkspace(ctx, store.WorkspaceRecord{ID: "native-tool-workspace", Name: "Native tool workspace", RootPath: root, CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			runs := application.NewRunService(st)
			_, run, err := runs.Create(ctx, application.CreateRunRequest{Goal: "Preview a local HTML file using actual Agent tools", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: "native-tool-workspace", ModelRoute: "native-browser-tools/model", Interactive: true, Budget: domain.Budget{MaxTurns: 3, MaxToolCalls: 12}})
			if err != nil {
				t.Fatal(err)
			}
			caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true, RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
			thread, err := st.GetThreadByRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Config.BrowserNetworkAccess != domain.BrowserNetworkInternet || run.Config.BrowserNetworkRevision != 1 {
				t.Fatalf("ordinary chat browser default=%+v", run.Config)
			}
			if _, err := application.NewThreadExecutionPermissionService(st, caps).Change(ctx, application.ChangeThreadExecutionPermissionRequest{ThreadID: thread.ID, Mode: "full", OperationKey: "native-tool-full-permission", RequestedBy: "test_operator", Reason: "Run the isolated local preview and browser tools", ConfirmFull: true}); err != nil {
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
				// WebView2 releases metrics files asynchronously after Close. This
				// test owns only this exact TempDir subtree, never a user profile.
				profileRoot := filepath.Join(home, "shared-browser")
				if !filepath.IsAbs(profileRoot) || filepath.Dir(profileRoot) != filepath.Clean(home) {
					t.Error("invalid isolated profile cleanup root")
					return
				}
				for deadline := time.Now().Add(10 * time.Second); ; {
					if err := os.RemoveAll(profileRoot); err == nil {
						break
					} else if time.Now().After(deadline) {
						t.Errorf("isolated profile cleanup: %v", err)
						break
					}
					time.Sleep(25 * time.Millisecond)
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
			// Neither root nor child is shown, including during navigation/input.
			if err := page.SetSurface(ctx, desktopbrowser.Bounds{Width: 800, Height: 600}, false); err != nil {
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
			var serviceJobID string
			workflowStep, boundaries, compactions, screenshotRequestIndex := 0, 0, 0, -1
			provider.respond = func(callCtx context.Context, req llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				response := &llm.ChatResponse{Provider: provider.Name(), Model: "model", Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}
				if req.Metadata["purpose"] == "context_compaction" {
					compactions++
					response.Text = "The accepted task is to preview workspace HTML. A local server is running, its page was opened, and its button was clicked. Continue the remaining screenshot and page verification without restarting completed work."
					return response, nil
				}
				if len(req.Tools) == 0 {
					for _, message := range req.Messages {
						if strings.Contains(message.Content, "This is a scheduling boundary") {
							boundaries++
							response.Text = `{"version":"root_lifecycle.v1","action":"continue","message":"Capture and verify the page after the click"}`
							return response, nil
						}
					}
					return nil, errors.New("tools disappeared without an explicit scheduling boundary")
				}
				add := func(name string, args any) {
					raw, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					response.ToolCalls = append(response.ToolCalls, llm.ToolCall{ID: fmt.Sprintf("native-tool-%d-%d", index, len(response.ToolCalls)), Name: name, Arguments: raw})
				}
				step := workflowStep
				workflowStep++
				switch step {
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
					if err := nativeBrowserModelCommandEnvironment(req); err != nil {
						return nil, err
					}
					add("workspace_read", map[string]any{"version": "agent-code-tools.v1", "path": "index.html", "start_line": 1, "end_line": 10})
					add("command_runtime", map[string]any{"command": "node server.cjs", "run_in_background": true, "timeout": 40000})
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
					serviceJobID = started.Jobs[0].ID
					add("command_runtime", map[string]any{"version": "command-runtime.v2", "action": "wait", "job_id": serviceJobID, "cursor": 0, "max_bytes": 4096, "wait_milliseconds": 5000})
				case 2:
					var observed struct {
						Pages []runner.CommandRuntimeOutputPage `json:"pages"`
					}
					if err := nativeBrowserToolResult(req, "command_runtime", &observed); err != nil {
						return nil, err
					}
					// Use only output returned through the model's real wait tool;
					// browser_navigate verifies the same Job owns the listener.
					var url string
					for _, output := range observed.Pages {
						if output.JobID != serviceJobID || output.State.Terminal() {
							return nil, fmt.Errorf("preview server output has wrong job or terminal state: %+v", output)
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
					add("browser_navigate", map[string]any{"version": "browser_navigate.v2", "url": url, "service_job_id": serviceJobID})
					add("browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
				case 3:
					var snap browserruntime.AgentBrowserSnapshot
					if err := nativeBrowserToolResult(req, "browser_snapshot", &snap); err != nil {
						return nil, err
					}
					if !strings.Contains(snap.Text, "Workspace preview") {
						return nil, fmt.Errorf("native snapshot did not see served file: %+v", snap)
					}
					add("browser_click", map[string]any{"version": "browser_click.v2", "snapshot_id": snap.SnapshotID, "target": map[string]any{"name": "Verify preview", "role": "button"}})
				case 4:
					if err := page.SetSurface(callCtx, desktopbrowser.Bounds{}, false); err != nil {
						return nil, err
					}
					var receipt browserruntime.AgentBrowserInteraction
					if err := nativeBrowserToolResult(req, "browser_click", &receipt); err != nil {
						return nil, err
					}
					if !receipt.Dispatched {
						return nil, fmt.Errorf("native click was not dispatched: %+v", receipt)
					}
					add("browser_screenshot", map[string]any{"version": "browser_screenshot.v2"})
					add("browser_snapshot", map[string]any{"version": "browser_snapshot.v2"})
				case 5:
					screenshotRequestIndex = index
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
					return nil, fmt.Errorf("unexpected additional model workflow step %d", step)
				}
				return response, nil
			}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			if vision == llm.VisionSupported {
				// Declare the vision fixture's capacity: this test verifies actual
				// screenshot delivery alongside the complete tool catalogue and a
				// prior segment. Context-pressure recovery has separate tests.
				window := llm.DefaultContextWindow()
				window.WindowTokens, window.Source = 64*1024, "native_vision_fixture"
				if err := router.SetContextWindow(llm.ModelRef{Provider: provider.Name(), Model: "model"}, window); err != nil {
					t.Fatal(err)
				}
			}
			execution := application.NewRunExecutionHandoffWithRuntime(st, router, policy.NewDefaultChecker(), application.RunRuntimeDependencies{ExecutionCapabilities: caps, CommandRuntime: commands, AgentBrowser: browser})
			turns := application.NewThreadTurnServiceWithExecutionCapabilities(st, application.NewRunLifecycleControlService(st), execution, caps).WithDefaultCommandRuntime(commands)
			result, err := turns.Execute(ctx, application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: "Preview the workspace HTML and verify its button using the shared browser", OperationKey: "native-preview-first-user-input", RequestedBy: "operator"})
			if err != nil {
				t.Logf("model responses before failure: %d", len(provider.requests))
				checkpoint, _, _ := st.GetSupervisorCheckpoint(context.Background(), run.ID)
				t.Logf("supervisor failure: %s", checkpoint.LastError)
				rounds, _ := st.ListRunSupervisorToolRoundsPage(context.Background(), run.ID, 0, 8)
				for _, round := range rounds {
					for _, call := range round.Calls {
						t.Logf("durable tool %s: status=%s error=%s", call.ToolName, call.Status, call.ErrorCode)
					}
				}
				t.Fatalf("actual Agent tool execution: %v; scripted provider: %v", err, provider.lastError)
			}
			toolCalls := 0
			if result.Execution != nil {
				for _, step := range result.Execution.Execution.Steps {
					toolCalls += step.ToolCalls
				}
			}
			if !result.ToolCalled || toolCalls != 8 || len(provider.requests) != 7+compactions || boundaries != 1 {
				t.Fatalf("unexpected actual tool flow: %+v requests=%d boundaries=%d compactions=%d", result, len(provider.requests), boundaries, compactions)
			}
			profile, err := st.GetRunExecutionProfile(ctx, run.ID)
			if err != nil || profile.Profile != domain.RunExecutionProfileLocal {
				t.Fatalf("ordinary desktop profile=%+v %v", profile, err)
			}
			if artifactPath == "" {
				t.Fatal("missing native screenshot")
			}
			pixels, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(artifactPath, "workspace:///"))))
			if err != nil {
				t.Fatal(err)
			}
			capture, err := png.Decode(bytes.NewReader(pixels))
			if err != nil {
				t.Fatal(err)
			}
			red, green, blue, alpha := capture.At(400, 400).RGBA()
			// WebView2 may color-manage PNG output through the Windows display
			// profile. The fixture's solid blue must still be rendered, not a
			// transparent/white image caused by the empty native window region.
			if !(blue > green && green > red && blue-red > 0x4040 && alpha == 0xffff) {
				t.Fatalf("hidden capture did not render actual page background: %x %x %x %x", red, green, blue, alpha)
			}
			observer := observeSharedBrowserNativePage(t, ctx, page)
			if got := observer.evaluate(`document.querySelector('output').textContent`); got != "Clicked by agent" {
				t.Fatalf("host page differs from Agent result: %s", got)
			}
			// Decode the exact native image delivered to the vision model.
			if vision == llm.VisionSupported {
				for _, message := range provider.requests[screenshotRequestIndex].Messages {
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
