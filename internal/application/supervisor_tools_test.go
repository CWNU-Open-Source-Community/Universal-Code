package application

import (
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSupervisorProjectTestGuidanceFollowsOfferedTool(t *testing.T) {
	for _, phase := range []domain.ExecutionPhase{domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver} {
		for _, available := range []bool{false, true} {
			name := string(phase) + "/unavailable"
			options := supervisorToolOptions{}
			if available {
				name = string(phase) + "/available"
				options.CommandRuntime.Adapter = commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64))
			}
			t.Run(name, func(t *testing.T) {
				request := llm.ChatRequest{
					Messages: supervisorMessages(nil, "inspect the project", contextmgr.Selection{},
						skills.ContextAssembly{}, skills.ExternalContextAssembly{}, domain.RunModeSnapshot{
							Surface: domain.ExecutionSurfaceCode, Phase: phase}),
					Tools: supervisorStructuredToolSpecs(domain.ExecutionSurfaceCode, phase,
						domain.RunExecutionPermissionFull, false, false, options),
				}
				for _, message := range request.Messages {
					if strings.Contains(message.Content, string(toolgateway.RunTestsTool)) {
						t.Fatal("root policy contains unconditional project test guidance")
					}
				}
				found := false
				for _, tool := range request.Tools {
					if tool.Name != string(toolgateway.RunTestsTool) {
						continue
					}
					found = true
					for _, clause := range []string{"Prefer this tool for project tests", "relative directory",
						"bounded timeout", "write files and caches", "approval rules", "Job receipts"} {
						if !strings.Contains(tool.Description, clause) {
							t.Fatalf("offered run_tests omits %q", clause)
						}
					}
				}
				if found != available {
					t.Fatalf("run_tests offered=%t want=%t", found, available)
				}
			})
		}
	}
}

func TestSupervisorUnavailableWebSearchRejectionExplainsOperatorPath(t *testing.T) {
	// A missing search provider leaves per-call authorized web fetch available.
	// A model that still calls web_search must receive a
	// durable reason naming the operator path and stopping the per-round retry
	// loop, instead of the bare 2026-09-14 acceptance failure.
	capabilities := toolgateway.WebEvidenceCapabilities{
		ProtocolVersion: toolgateway.WebEvidenceRegistryVersion,
		Available:       true, FetchAvailable: true, SearchAvailable: false,
	}
	options := supervisorToolOptions{WebEvidence: supervisorWebEvidenceTools{
		Capabilities: capabilities,
		Authority:    json.RawMessage(`{"version":"` + toolgateway.WebEvidenceRegistryVersion + `"}`),
	}}
	_, err := prepareSupervisorToolCalls([]llm.ToolCall{{
		ID: "provider-call-web-search", Name: string(toolgateway.WebSearchTool),
		Arguments: json.RawMessage(`{"version":"web_search.v1","query":"Riemann Hypothesis latest progress","limit":3}`),
	}}, "run-web-rejection", 1, 1, domain.ExecutionSurfaceCode,
		domain.ExecutionPhaseDeliver, domain.RunExecutionPermissionConservative,
		false, false, options)
	if err == nil {
		t.Fatal("unavailable web_search was accepted")
	}
	if !strings.HasPrefix(err.Error(),
		`provider requested unavailable web evidence tool "web_search"`) {
		t.Fatalf("stable rejection prefix changed: %v", err)
	}
	for _, want := range []string{
		"not opened for the current Run",
		"Configure a search backend",
		"Do not retry web_search",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rejection omits %q: %v", want, err)
		}
	}
	// The repair reason parses round=N before the first "; " separator, so the
	// appended explanation must not embed that separator itself.
	if strings.Contains(err.Error(), "; ") {
		t.Fatalf("rejection reason must not embed round separators: %v", err)
	}
	// The same Run still offers web_fetch: the availability gate must pass it
	// through, so the call proceeds to later authority decoding instead.
	_, fetchErr := prepareSupervisorToolCalls([]llm.ToolCall{{
		ID: "provider-call-web-fetch", Name: string(toolgateway.WebFetchTool),
		Arguments: json.RawMessage(`{"version":"web_fetch.v1","url":"https://example.com/article"}`),
	}}, "run-web-rejection", 1, 1, domain.ExecutionSurfaceCode,
		domain.ExecutionPhaseDeliver, domain.RunExecutionPermissionConservative,
		false, false, options)
	if fetchErr == nil || strings.Contains(fetchErr.Error(),
		"unavailable web evidence tool") {
		t.Fatalf("offered web_fetch hit the availability rejection: %v", fetchErr)
	}
}

func TestSupervisorUnavailableWebFetchRejectionExplainsOperatorPath(t *testing.T) {
	options := supervisorToolOptions{WebEvidence: supervisorWebEvidenceTools{
		Capabilities: toolgateway.WebEvidenceCapabilities{
			Available: true, SearchAvailable: true, FetchAvailable: false},
		Authority: json.RawMessage(`{"version":"` + toolgateway.WebEvidenceRegistryVersion + `"}`),
	}}
	_, err := prepareSupervisorToolCalls([]llm.ToolCall{{
		ID: "provider-call-web-fetch", Name: string(toolgateway.WebFetchTool),
		Arguments: json.RawMessage(`{"version":"web_fetch.v1","url":"https://example.com/article"}`),
	}}, "run-fetch-closed", 1, 1, domain.ExecutionSurfaceCode,
		domain.ExecutionPhaseDeliver, domain.RunExecutionPermissionConservative,
		false, false, options)
	if err == nil ||
		!strings.HasPrefix(err.Error(),
			`provider requested unavailable web evidence tool "web_fetch"`) ||
		!strings.Contains(err.Error(), "web fetch is not opened for the current Run") ||
		!strings.Contains(err.Error(), "Do not retry this tool until it is opened") {
		t.Fatalf("web_fetch rejection lost its operator path: %v", err)
	}
}

func TestSupervisorClosedWebEvidenceRejectionKeepsGenericReason(t *testing.T) {
	_, err := prepareSupervisorToolCalls([]llm.ToolCall{{
		ID: "provider-call-web-search", Name: string(toolgateway.WebSearchTool),
		Arguments: json.RawMessage(`{"version":"web_search.v1","query":"bounded query","limit":1}`),
	}}, "run-web-closed", 1, 1, domain.ExecutionSurfaceCode,
		domain.ExecutionPhaseDeliver, domain.RunExecutionPermissionConservative,
		false, false)
	if err == nil {
		t.Fatal("web_search was accepted without any web evidence advertisement")
	}
	if !strings.HasPrefix(err.Error(),
		`provider requested unavailable web evidence tool "web_search"`) ||
		!strings.Contains(err.Error(), "web evidence tools are not open") ||
		!strings.Contains(err.Error(), "Do not retry this tool until it is opened") {
		t.Fatalf("closed web evidence rejection lost its reason: %v", err)
	}
}
