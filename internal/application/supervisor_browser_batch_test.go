package application

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

func browserBatchCall(index int, name toolgateway.ToolName) llm.ToolCall {
	p := toolgateway.AgentBrowserPayload{Version: string(name) + ".v2"}
	switch name {
	case toolgateway.BrowserStatusTool:
		p.Version = toolgateway.AgentBrowserStatusPayloadVersion
	case toolgateway.BrowserNavigateTool:
		p.URL = "https://example.org"
	case toolgateway.BrowserClickTool, toolgateway.BrowserTypeTool:
		p.SnapshotID, p.ElementRef = "current-snapshot", fmt.Sprintf("current-ref-%d", index)
		if name == toolgateway.BrowserTypeTool {
			p.Value, p.Mode = "public text", "replace"
		}
	case toolgateway.BrowserKeyTool:
		p.Key = "Enter"
	case toolgateway.BrowserScrollTool:
		x, y := 0.0, 100.0
		p.DeltaX, p.DeltaY = &x, &y
	case toolgateway.BrowserUploadTool:
		p.SnapshotID, p.ElementRef = "current-snapshot", fmt.Sprintf("current-ref-%d", index)
		p.WorkspacePath = "evidence/sample.txt"
		p.Sensitive = &toolgateway.AgentBrowserSensitiveIntent{Version: "browser_sensitive_intent.v1", Effect: "external_write", Target: "https://example.org/form", Description: "Attach the local evidence file", DocumentEpoch: 1}
	}
	args, _ := json.Marshal(p)
	return llm.ToolCall{ID: fmt.Sprintf("batch-%d", index), Name: string(name), Arguments: args}
}

func TestSupervisorBrowserBatchRejectsUnresolvedRefDependencyBeforeEffects(t *testing.T) {
	for _, prefix := range []toolgateway.ToolName{toolgateway.BrowserNavigateTool, toolgateway.BrowserSnapshotTool,
		toolgateway.BrowserClickTool, toolgateway.BrowserTypeTool, toolgateway.BrowserKeyTool, toolgateway.BrowserScrollTool,
		toolgateway.BrowserUploadTool} {
		for _, last := range []toolgateway.ToolName{toolgateway.BrowserClickTool, toolgateway.BrowserTypeTool,
			toolgateway.BrowserUploadTool} {
			t.Run(string(prefix)+"-then-"+string(last), func(t *testing.T) {
				_, st, runtime, supervisor, turn := newAgentBrowserFixture(t)
				caps, auth, err := supervisor.supervisorBrowserActionCapabilities(t.Context(), turn, st.base.executionPermission)
				if err != nil || !caps.Available {
					t.Fatal("fixture has no usable authority", err)
				}
				out, err := prepareSupervisorToolCalls([]llm.ToolCall{browserBatchCall(1, prefix), browserBatchCall(2, toolgateway.BrowserStatusTool),
					browserBatchCall(3, toolgateway.BrowserScreenshotTool), browserBatchCall(4, last)}, turn.Run.ID, 1, 1,
					turn.Mode.Surface, turn.Mode.Phase, st.base.executionPermission.Mode, false, false,
					supervisorToolOptions{BrowserActions: supervisorBrowserActionTools{Capabilities: caps, Authority: auth}})
				if err == nil || len(out) != 0 || !strings.Contains(err.Error(), "unresolved_snapshot_dependency") {
					t.Fatalf("unsafe new batch passed preparation: prepared=%d err=%v", len(out), err)
				}
				if len(st.calls) != 0 || len(st.started) != 0 || len(runtime.actions) != 0 {
					t.Fatal("rejection recorded or dispatched a partial batch")
				}
			})
		}
	}
}

func TestSupervisorBrowserBatchKeepsInputObservationAndKeySequences(t *testing.T) {
	for _, names := range [][]toolgateway.ToolName{
		{toolgateway.BrowserNavigateTool, toolgateway.BrowserSnapshotTool, toolgateway.BrowserScreenshotTool},
		{toolgateway.BrowserTypeTool, toolgateway.BrowserSnapshotTool, toolgateway.BrowserScreenshotTool},
		{toolgateway.BrowserTypeTool, toolgateway.BrowserKeyTool, toolgateway.BrowserSnapshotTool},
		{toolgateway.BrowserScreenshotTool, toolgateway.BrowserStatusTool, toolgateway.BrowserClickTool},
		{toolgateway.BrowserKeyTool, toolgateway.BrowserScrollTool, toolgateway.BrowserSnapshotTool},
		{toolgateway.BrowserStatusTool, toolgateway.BrowserScreenshotTool, toolgateway.BrowserUploadTool},
	} {
		t.Run(fmt.Sprint(names), func(t *testing.T) {
			_, st, _, supervisor, turn := newAgentBrowserFixture(t)
			caps, auth, err := supervisor.supervisorBrowserActionCapabilities(t.Context(), turn, st.base.executionPermission)
			if err != nil {
				t.Fatal(err)
			}
			calls := make([]llm.ToolCall, 0, len(names))
			for index, name := range names {
				calls = append(calls, browserBatchCall(index+1, name))
			}
			out, err := prepareSupervisorToolCalls(calls, turn.Run.ID, 1, 1,
				turn.Mode.Surface, turn.Mode.Phase, st.base.executionPermission.Mode, false, false,
				supervisorToolOptions{BrowserActions: supervisorBrowserActionTools{Capabilities: caps, Authority: auth}})
			if err != nil || len(out) != len(calls) {
				t.Fatalf("safe native sequence was changed: %v", err)
			}
			for index, call := range out {
				if call.Name != calls[index].Name || len(call.Authority) == 0 {
					t.Fatal("preflight changed tool order or authority pairing")
				}
			}
		})
	}
}
