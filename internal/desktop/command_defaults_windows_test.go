package desktop

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
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
)

func TestWindowsDesktopDefaultCommandsUseChatApprovalPolicy(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("this real command integration requires the installed Node runtime", err)
	}
	for _, mode := range []string{"ask", "auto", "full", "runtime_missing"} {
		t.Run(mode, func(t *testing.T) {
			const model = "desktop-default-command-model"
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
				if calls.Add(1) == 1 {
					var request desktopSourceWiringRequest
					if err := json.Unmarshal(raw, &request); err != nil {
						t.Error(err)
					}
					if _, found := desktopSourceWiringToolsByName(request.Tools)["command_runtime"]; !found {
						t.Error("the actual model request omitted command_runtime")
					}
					payload, _ := json.Marshal(map[string]any{"version": "command-runtime.v2", "action": "run", "failure_policy": "fail_fast", "max_bytes": 4096,
						"commands": []any{map[string]any{"version": "command-runtime.v2", "workspace_mode": "exclusive", "profile": "process",
							"executable": node, "arguments": []string{"-e", "process.stdout.write('desktop-default-command')"}, "working_directory": ".", "environment": []any{},
							"stdin_policy": "closed", "close_initial_stdin": true, "timeout_milliseconds": 10000,
							"output": map[string]any{"inline_bytes": 4096, "artifact_bytes": 4096}, "network": "disabled", "credentials": "none", "purpose": "Verify the isolated desktop default command"}}})
					writeAnthropicSSE(t, w,
						map[string]any{"type": "message_start", "message": map[string]any{"model": model, "usage": map[string]any{"input_tokens": 12, "output_tokens": 0}}},
						map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "desktop-command-call", "name": "command_runtime", "input": map[string]any{}}},
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
			if mode == "auto" || mode == "full" {
				selected := desktopControlRequest(plane.Handler(), http.MethodPost, "/api/v1/threads/"+thread.Thread.ID+"/execution-permission", "default-command-permission",
					fmt.Sprintf(`{"mode":%q,"confirm_full":%t,"reason":"explicit chat approval preference"}`, mode, mode == "full"))
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
			if mode == "full" {
				if len(jobs) != 1 || jobs[0].State != runner.CommandRuntimeJobCompleted || !strings.Contains(jobs[0].Stdout, "desktop-default-command") || calls.Load() != 2 {
					t.Fatalf("Full model tool did not execute the real native command: jobs=%+v calls=%d", jobs, calls.Load())
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
