package desktop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/runner"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestWindowsDesktopDefaultCommandsUseChatApprovalPolicy(t *testing.T) {
	for _, mode := range []string{"ask", "auto", "full", "full_repair", "full_command_failure", "full_nonzero_exit", "runtime_missing"} {
		t.Run(mode, func(t *testing.T) {
			const model = "desktop-default-command-model"
			const missingCommand = "uc_missing_fixture_command_761982.exe"
			ordinaryFailure := mode == "full_command_failure" || mode == "full_nonzero_exit"
			var calls atomic.Int32
			qualificationFixture := &anthropicHarnessFixture{}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				phase, nonce, err := qualificationFixture.phase(raw)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if phase == "tool_call" {
					writeAnthropicToolSSE(t, w, model, nonce)
					return
				}
				if phase == "tool_result" {
					writeAnthropicTextSSE(t, w, model, fmt.Sprintf(`{"version":"model_harness_probe.v1","status":"ok","nonce":%q}`, nonce))
					return
				}
				callNumber := calls.Add(1)
				var request struct {
					desktopSourceWiringRequest
					System string `json:"system"`
				}
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if callNumber > 1 && !(mode == "full_repair" && callNumber == 2) {
					receipt, err := desktopDefaultCommandResult(request.Messages)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					wantFailed := ordinaryFailure && callNumber == 2
					var stderr strings.Builder
					for _, page := range receipt.Pages {
						if page.ExitCode == nil || (wantFailed && *page.ExitCode == 0) || (!wantFailed && *page.ExitCode != 0) {
							t.Errorf("actual tool output page has incorrect exit: %+v", page)
						}
						for _, frame := range page.Frames {
							if frame.Stream == runner.CommandRuntimeStderr {
								stderr.WriteString(frame.Text)
							}
						}
					}
					if wantFailed {
						wantError := missingCommand
						if mode == "full_nonzero_exit" {
							wantError = "fixture expected failure"
						}
						if !strings.Contains(stderr.String(), wantError) {
							t.Errorf("actual model result lost the command error %q: %s", wantError, stderr.String())
						}
					}
					for _, job := range receipt.Jobs {
						wantState := runner.CommandRuntimeJobCompleted
						if wantFailed {
							wantState = runner.CommandRuntimeJobFailed
						}
						if job.State != wantState || job.ExitCode == nil || (wantFailed && *job.ExitCode == 0) || (!wantFailed && *job.ExitCode != 0) {
							t.Errorf("actual model Job receipt lost command failure/completion: %+v", job)
						}
					}
					if len(receipt.Pages) != 1 || len(receipt.Jobs) != 1 {
						t.Errorf("missing real Job output in model result: %+v", receipt)
					}
					if ordinaryFailure && strings.Contains(request.System, "Tool-request correction") {
						t.Error("ordinary command failure entered protocol repair")
					}
				}
				if callNumber == 1 || ((mode == "full_repair" || ordinaryFailure) && callNumber == 2) {
					tool, found := desktopSourceWiringToolsByName(request.Tools)["command_runtime"]
					if !found {
						t.Error("the actual model request omitted command_runtime")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					var exposure struct {
						Environment runner.CommandRuntimeHostEnvironment `json:"command_environment"`
					}
					start := strings.Index(request.System, `{"version":"root_tool_exposure.v1"`)
					if start < 0 || json.NewDecoder(strings.NewReader(request.System[start:])).Decode(&exposure) != nil ||
						exposure.Environment.OS != "windows" || exposure.Environment.PreferredShellProfile != runner.CommandRuntimePowerShell {
						t.Error("actual provider request omitted current native Windows/default-shell facts")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					foundNode := false
					for _, executable := range exposure.Environment.ProcessExecutables {
						if executable.Name == "node" {
							foundNode = true
						}
					}
					if !foundNode {
						t.Error("actual host facts did not identify the Node runtime used by the command")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if mode == "full_repair" && callNumber == 2 &&
						(!strings.Contains(request.System, "Tool-request correction 1 of 1") || !strings.Contains(request.System, "absolute native executable and literal argv")) {
						t.Error("the one repair lost original rejection or host facts")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					command := `node -e "process.stdout.write('desktop-default-command')"`
					if mode == "full_command_failure" && callNumber == 1 {
						// A path fails directly; a bare name scans installed modules
						// and can exceed the command budget on CI hosts.
						command = `& '.\` + missingCommand + `'`
					}
					if mode == "full_nonzero_exit" && callNumber == 1 {
						command = `node -e "process.stderr.write('fixture expected failure'); process.exit(7)"`
					}
					payload, _ := json.Marshal(map[string]any{"command": command})
					invalidRelative := mode == "full_repair" && callNumber == 1
					if invalidRelative {
						payload, _ = json.Marshal(map[string]any{"version": "command-runtime.v2", "action": "run", "failure_policy": "fail_fast", "max_bytes": 4096,
							"commands": []any{map[string]any{"version": "command-runtime.v2", "profile": "process", "executable": "python", "arguments": []string{"--version"},
								"working_directory": ".", "environment": []any{}, "stdin_policy": "closed", "close_initial_stdin": true, "timeout_milliseconds": 10000,
								"output": map[string]any{"inline_bytes": 4096, "artifact_bytes": 4096}, "network": "host", "credentials": "none", "purpose": "Verify strict legacy process boundary"}}})
					}
					compiler := jsonschema.NewCompiler()
					if err := compiler.AddResource("actual-command.json", bytes.NewReader(tool.InputSchema)); err != nil {
						t.Error(err)
					}
					published, err := compiler.Compile("actual-command.json")
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					var arguments any
					_ = json.Unmarshal(payload, &arguments)
					if valid := published.Validate(arguments) == nil; valid == invalidRelative {
						t.Errorf("actual offered schema disagrees with model call: valid=%t intentional_rejection=%t", valid, invalidRelative)
					}
					writeAnthropicSSE(t, w,
						map[string]any{"type": "message_start", "message": map[string]any{"model": model, "usage": map[string]any{"input_tokens": 12, "output_tokens": 0}}},
						map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("desktop-command-%d", callNumber), "name": "command_runtime", "input": map[string]any{}}},
						map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(payload)}},
						map[string]any{"type": "content_block_stop", "index": 0},
						map[string]any{"type": "message_delta", "usage": map[string]any{"output_tokens": 8}}, map[string]any{"type": "message_stop"})
					return
				}
				writeAnthropicTextSSE(t, w, model, `{"version":"root_lifecycle.v1","action":"finish","message":"Command receipt observed","summary":"done"}`)
			}))
			defer provider.Close()
			t.Setenv("CYBERAGENT_ANTHROPIC_API_KEY", "isolated-command-fixture")
			t.Setenv("CYBERAGENT_ANTHROPIC_BASE_URL", provider.URL)
			t.Setenv("CYBERAGENT_ANTHROPIC_MODEL", model)
			caps := domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: mode != "runtime_missing",
				RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()}
			plane, err := OpenControlPlane(ControlPlaneConfig{DatabasePath: filepath.Join(t.TempDir(), "default-command.db"),
				ReadToken: desktopControlPlaneTestToken, ControlToken: desktopControlPlaneControlToken,
				RunCreationEnabled: true, RunControlEnabled: true, SessionMessageEnabled: true, RunLifecycleEnabled: true,
				RunExecutionEnabled: true, ModelControlEnabled: true, ExecutionPermissionControlEnabled: true, ApprovalControlEnabled: true,
				ExecutionPermissionCapabilities: caps, CredentialStore: credential.NewMemoryStore(), AppVersion: "isolated-default-command-test"})
			if err != nil {
				t.Fatal(err)
			}
			defer plane.Close()
			workspace, err := plane.RegisterWorkspaceDirectory(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			qualified := desktopControlRequest(plane.Handler(), http.MethodPost, httpapi.ModelHarnessQualificationPath, "default-command-harness",
				fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q,"confirm_qualification":true}`, modelregistry.HarnessQualificationProtocolVersion, model))
			if qualified.Code != http.StatusAccepted {
				t.Fatalf("qualification: %d %s", qualified.Code, qualified.Body.String())
			}
			route := desktopControlRequest(plane.Handler(), http.MethodPost, "/api/v1/models/routes/code", "default-command-route",
				fmt.Sprintf(`{"version":%q,"provider":"anthropic","model":%q}`, modelregistry.RouteControlProtocolVersion, model))
			if route.Code != http.StatusAccepted {
				t.Fatalf("route: %d %s", route.Code, route.Body.String())
			}
			thread := createDesktopSourceWiringThread(t, plane, workspace.ID, "Run a temporary local command", "default-command-thread")
			if mode == "auto" || mode == "full" || mode == "full_repair" || ordinaryFailure {
				selectedMode := mode
				if mode == "full_repair" || ordinaryFailure {
					selectedMode = "full"
				}
				selected := desktopControlRequest(plane.Handler(), http.MethodPost, "/api/v1/threads/"+thread.Thread.ID+"/execution-permission", "default-command-permission",
					fmt.Sprintf(`{"mode":%q,"confirm_full":%t,"reason":"explicit chat approval preference"}`, selectedMode, selectedMode == "full"))
				if selected.Code != http.StatusAccepted {
					t.Fatalf("permission: %d %s", selected.Code, selected.Body.String())
				}
			}
			response := desktopControlRequest(plane.Handler(), http.MethodPost, "/api/v1/threads/"+thread.Thread.ID+"/turns", "default-command-input",
				`{"version":"thread_message_submission.v1","content":"Run the isolated command under my selected approval policy"}`)
			if mode == "runtime_missing" {
				profile, err := plane.stateStore.GetRunExecutionProfile(t.Context(), thread.Run.ID)
				if response.Code != http.StatusPreconditionFailed || !strings.Contains(response.Body.String(), "local command environment is unavailable") ||
					calls.Load() != 0 || err != nil || !profile.IsDefaultPreview() {
					t.Fatalf("missing desktop adapter was hidden or changed the default: status=%d calls=%d profile=%+v body=%s err=%v", response.Code, calls.Load(), profile, response.Body.String(), err)
				}
				return
			}
			if response.Code != http.StatusAccepted {
				t.Fatalf("turn: %d %s", response.Code, response.Body.String())
			}
			profile, err := plane.stateStore.GetRunExecutionProfile(t.Context(), thread.Run.ID)
			if err != nil || profile.Profile != domain.RunExecutionProfileLocal || profile.RequestedBy != "desktop_default" {
				t.Fatalf("desktop did not choose its real command adapter: %+v err=%v", profile, err)
			}
			jobs, err := plane.stateStore.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: thread.Run.ID})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "full" || (mode == "full_repair" || ordinaryFailure) {
				wantCalls := int32(2)
				if mode == "full_repair" || ordinaryFailure {
					wantCalls++
				}
				wantJobs := 1
				if ordinaryFailure {
					wantJobs = 2
				}
				completed, failed := 0, 0
				for _, job := range jobs {
					if job.State == runner.CommandRuntimeJobCompleted && job.ExitCode != nil && *job.ExitCode == 0 && strings.Contains(job.Stdout, "desktop-default-command") {
						completed++
					}
					if job.State == runner.CommandRuntimeJobFailed && job.ExitCode != nil && *job.ExitCode != 0 {
						failed++
					}
					if job.Profile != runner.CommandRuntimePowerShell {
						t.Errorf("simple form did not use host PowerShell: %+v", job)
					}
				}
				if len(jobs) != wantJobs || completed != 1 || failed != wantJobs-1 || calls.Load() != wantCalls {
					t.Fatalf("Full model tool did not execute/correct the real native command: jobs=%+v calls=%d", jobs, calls.Load())
				}
			} else {
				pending, err := plane.stateStore.ListApprovals(t.Context(), approval.ListFilter{RunID: thread.Run.ID, Status: approval.StatusPending})
				if err != nil || len(jobs) != 0 || len(pending) != 1 || pending[0].ToolName != "command_runtime" || calls.Load() != 1 {
					t.Fatalf("%s did not preserve native host command approval: jobs=%d approvals=%+v calls=%d err=%v", mode, len(jobs), pending, calls.Load(), err)
				}
			}
		})
	}
}

type desktopCommandResult struct {
	Jobs  []runner.CommandRuntimeJobSnapshot `json:"jobs"`
	Pages []runner.CommandRuntimeOutputPage  `json:"pages"`
}

func desktopDefaultCommandResult(messages []json.RawMessage) (desktopCommandResult, error) {
	var last desktopCommandResult
	found := false
	for _, raw := range messages {
		var message struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		var blocks []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
			IsError bool   `json:"is_error"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			var envelope struct {
				Tool   string `json:"tool"`
				Stdout string `json:"stdout"`
			}
			if json.Unmarshal([]byte(block.Content), &envelope) != nil || envelope.Tool != "command_runtime" {
				continue
			}
			if block.IsError {
				return last, fmt.Errorf("command invocation failed before a Job receipt: %s", block.Content)
			}
			if err := json.Unmarshal([]byte(envelope.Stdout), &last); err != nil {
				return last, err
			}
			found = true
		}
	}
	if !found {
		return last, fmt.Errorf("missing command Job result in actual provider request")
	}
	return last, nil
}
