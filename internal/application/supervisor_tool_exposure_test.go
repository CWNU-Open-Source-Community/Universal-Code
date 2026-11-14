package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

type exposureBrowserTestStore struct{ *agentBrowserTestStore }

func (s *exposureBrowserTestStore) GetRunExecutionProfile(_ context.Context, runID string) (domain.RunExecutionProfileSnapshot, error) {
	return domain.RunExecutionProfileSnapshot{RunID: runID, Profile: domain.RunExecutionProfilePreview}, nil
}

func TestSupervisorFullPermissionReadinessUsesCurrentProcessGrant(t *testing.T) {
	for _, mode := range []domain.RunExecutionPermissionMode{domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto} {
		if err := requireSupervisorPermissionReady(domain.ExecutionPermissionRuntimeCapabilities{}, domain.RunExecutionPermissionSnapshot{Mode: mode}); err != nil {
			t.Fatalf("%s unexpectedly requires Full activation: %v", mode, err)
		}
	}
	err := requireSupervisorPermissionReady(domain.ExecutionPermissionRuntimeCapabilities{}, domain.RunExecutionPermissionSnapshot{Mode: domain.RunExecutionPermissionFull})
	if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || !strings.Contains(err.Error(), "unavailable in this app process") || !strings.Contains(err.Error(), "Ask or Auto") {
		t.Fatalf("missing host Full gate has no recovery: %v", err)
	}
}

type exposurePermissionStore struct {
	AgentRunnerStore
	current domain.RunExecutionPermissionSnapshot
	reads   int
}

func (s *exposurePermissionStore) GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error) {
	s.reads++
	return s.current, nil
}

func TestSupervisorReadinessRechecksCurrentPermissionBeforeUsingPreparedScope(t *testing.T) {
	prepared := domain.RunExecutionPermissionSnapshot{ID: "full-before", RunID: "run-current", Revision: 2, Mode: domain.RunExecutionPermissionFull}
	st := &exposurePermissionStore{current: domain.RunExecutionPermissionSnapshot{ID: "ask-current", RunID: prepared.RunID, Revision: 3, Mode: domain.RunExecutionPermissionAsk}}
	runner := &AgentRunner{store: st}
	err := runner.requireSupervisorPermissionCurrent(t.Context(), prepared)
	if st.reads != 1 || apperror.CodeOf(err) != apperror.CodeConflict || strings.Contains(err.Error(), "Reconfirm Full") {
		t.Fatalf("stale Full was checked instead of current Ask: reads=%d err=%v", st.reads, err)
	}
	if err := runner.requireSupervisorPermissionCurrent(t.Context(), st.current); err != nil || st.reads != 2 {
		t.Fatalf("current Ask scope did not pass: reads=%d err=%v", st.reads, err)
	}
}

func TestSupervisorToolExposureMatchesActualOpenAIChatStreamSchema(t *testing.T) {
	for _, phase := range []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver} {
		t.Run(string(phase), func(t *testing.T) {
			files := toolgateway.AgentCodeCapabilities(toolgateway.AgentCodeCapabilityContext{Surface: domain.ExecutionSurfaceCode, Phase: phase, Role: domain.AgentRoleRoot, Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionAsk})
			options := supervisorToolOptions{AgentCode: supervisorAgentCodeTools{Capabilities: files},
				CommandRuntime: supervisorCommandRuntimeTools{Adapter: commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64))},
				BrowserActions: supervisorBrowserActionTools{Capabilities: toolgateway.BrowserActionCapabilities{ProtocolVersion: toolgateway.AgentBrowserAuthorityVersion, Available: true}},
			}
			normal := llm.ChatRequest{Messages: []llm.Message{{Role: "system", Content: "Return JSON."}, {Role: "user", Content: "Inspect current tool availability."}},
				Tools: supervisorStructuredToolSpecs(domain.ExecutionSurfaceCode, phase, domain.RunExecutionPermissionAsk, false, false, options), JSONMode: true}
			captured := make(chan []string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var wire struct {
					Tools []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Errorf("decode local wire request: %v", err)
					http.Error(w, "invalid request", 400)
					return
				}
				names := make([]string, 0, len(wire.Tools))
				for _, tool := range wire.Tools {
					names = append(names, tool.Function.Name)
				}
				var exposure struct {
					Tools []string `json:"tools"`
				}
				found := false
				for _, message := range wire.Messages {
					if message.Role == "system" && strings.Contains(message.Content, `"version":"root_tool_exposure.v1"`) {
						found = true
						start := strings.Index(message.Content, "{")
						if err := json.Unmarshal([]byte(message.Content[start:]), &exposure); err != nil {
							t.Errorf("decode exposure: %v", err)
						}
					}
				}
				if !found || strings.Join(exposure.Tools, ",") != strings.Join(names, ",") {
					t.Errorf("provider wire tools differ from context: tools=%v context=%v", names, exposure.Tools)
				}
				captured <- names
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"local-tool-schema\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"{}\"},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(w, "data: {\"id\":\"local-tool-schema\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"local-tool-schema\",\"model\":\"model\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			provider, err := llm.NewOpenAICompatibleProvider(llm.OpenAICompatibleConfig{Name: "local-tool-schema", BaseURL: server.URL, APIKey: "local-test-key", DefaultModel: "model"})
			if err != nil {
				t.Fatal(err)
			}
			for i, request := range []llm.ChatRequest{normal, supervisorToolBoundaryRequest(normal)} {
				request = supervisorToolExposureRequest(request, supervisorToolExposureContext{})
				chunks, err := provider.StreamChat(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
				names := <-captured
				if i == 1 {
					if len(names) != 0 {
						t.Fatalf("boundary re-enabled tools: %v", names)
					}
					continue
				}
				t.Logf("phase=%s actual streamed request tools=%v", phase, names)
				for _, name := range []string{"workspace_list", "workspace_read", "browser_navigate", "browser_snapshot", "browser_move"} {
					if !supervisorOffersTool(request.Tools, name) {
						t.Fatalf("actual request missing %s", name)
					}
				}
				for _, name := range []string{"workspace_change", "workspace_apply", "command_runtime"} {
					if supervisorOffersTool(request.Tools, name) != (phase == domain.ExecutionPhaseDeliver) {
						t.Fatalf("%s exposure did not follow phase %s", name, phase)
					}
				}
			}
		})
	}
}

func TestSupervisorToolExposureReportsBrowserNetworkRefusalWithoutLaunching(t *testing.T) {
	_, st, r, runner, turn := newAgentBrowserFixture(t)
	runner.store = &exposureBrowserTestStore{st}
	st.base.run.Config.BrowserNetworkAccess = domain.BrowserNetworkOff
	turn.Run = st.base.run
	caps, authority, err := runner.supervisorBrowserActionCapabilities(t.Context(), turn, st.base.executionPermission)
	if err != nil || caps.Available {
		t.Fatalf("network-off fixture advertised browser: %+v %v", caps, err)
	}
	options := supervisorToolOptions{BrowserActions: supervisorBrowserActionTools{Capabilities: caps, Authority: authority}}
	tools := supervisorStructuredToolSpecs(turn.Mode.Surface, turn.Mode.Phase, st.base.executionPermission.Mode, false, false, options)
	current, err := runner.supervisorToolExposureContext(t.Context(), turn, st.base.executionPermission, options, tools)
	if err != nil || !strings.Contains(current.Unavailable["browser_actions"], "Browser internet access is off") || !strings.Contains(current.Unavailable["browser_actions"], "chat settings") || len(r.actions) != 0 {
		t.Fatalf("browser refusal changed authority or hid recovery: context=%+v actions=%v error=%v", current, r.actions, err)
	}
}
