package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/workspace"
)

func TestRunProgressGuardRepeatedAndNestedWorkspaceReadStillPauses(t *testing.T) {
	f := newProgressToolFixture(t)
	f.writeFile(t, "read.txt", "first\nsecond\nthird\nfourth\n")
	var fingerprint string
	for index, bounds := range [][2]int{{1, 4}, {2, 3}, {1, 4}} {
		page := f.readPage(t, "read.txt", bounds[0], bounds[1])
		completed := f.completeRead(t, page, "continue accepted task", nil)
		guard := f.guard(t)
		if index == 0 {
			fingerprint = guard.StateFingerprint
		}
		if guard.StateFingerprint != fingerprint || guard.RepeatedActionCount != index+1 {
			t.Fatalf("duplicate/nested read invented progress: index=%d guard=%+v", index, guard)
		}
		if index == domain.RunProgressRepeatThreshold-1 && completed.run.Status != domain.RunPaused {
			t.Fatal("same contents did not reach the original repeat threshold")
		}
	}
}

func TestRunProgressGuardNewWorkspaceReadRangesAndHashesAreProgress(t *testing.T) {
	f := newProgressToolFixture(t)
	f.writeFile(t, "read.txt", "first\nsecond\nthird\nfourth\nfifth\nsixth\n")
	var fingerprint string
	for index, bounds := range [][2]int{{1, 2}, {2, 3}, {4, 5}, {5, 6}} {
		page := f.readPage(t, "read.txt", bounds[0], bounds[1])
		if !page.Truncated {
			t.Fatal("fixture must exercise inner pagination")
		}
		completed := f.completeRead(t, page, "continue accepted task", nil)
		guard := f.guard(t)
		if completed.run.Status != domain.RunRunning || guard.StateFingerprint == fingerprint ||
			guard.RepeatedActionCount != 1 || guard.StagnantTurnCount != 1 {
			t.Fatalf("new page %d was mistaken for livelock: guard=%+v", index, guard)
		}
		fingerprint = guard.StateFingerprint
	}
	f.writeFile(t, "read.txt", "changed\nsecond\nthird\nfourth\nfifth\nsixth\n")
	completed := f.completeRead(t, f.readPage(t, "read.txt", 1, 2), "continue accepted task", nil)
	guard := f.guard(t)
	if completed.run.Status != domain.RunRunning || guard.StateFingerprint == fingerprint || guard.StagnantTurnCount != 1 {
		t.Fatalf("new full-file hash was not observed: guard=%+v", guard)
	}
}

func TestRunProgressGuardReadOnlyNewMaterialSurvivesStagnantThreshold(t *testing.T) {
	f := newProgressToolFixture(t)
	for index := 0; index < domain.RunProgressStagnantThreshold; index++ {
		path := fmt.Sprintf("material-%d.txt", index)
		f.writeFile(t, path, "new source material\n")
		completed := f.completeRead(t, f.readPage(t, path, 1, 1), fmt.Sprintf("review source %d", index), nil)
		if guard := f.guard(t); completed.run.Status != domain.RunRunning || guard.StagnantTurnCount != 1 {
			t.Fatalf("legitimate reading required a write: run=%s guard=%+v", completed.run.Status, guard)
		}
	}
}

func TestRunProgressGuardReadEvidenceIsStableAcrossReopenAndExactReplay(t *testing.T) {
	f := newProgressToolFixture(t)
	f.writeFile(t, "read.txt", "first\nsecond\n")
	page := f.readPage(t, "read.txt", 1, 2)
	payload := progressJSON(t, toolgateway.WorkspaceReadPayload{Version: toolgateway.AgentCodeRegistryVersion,
		Path: page.Path, StartLine: page.StartLine, EndLine: page.EndLine})
	cp, callID := f.beginCall(t, "workspace_read", payload)
	call := f.recordResult(t, cp, callID, domain.SupervisorToolCompleted, "",
		f.envelope(t, "workspace_read", string(progressJSON(t, page)), nil))
	fingerprint := f.fingerprint(t)
	if _, replayed, err := f.st.RecordSupervisorToolResult(t.Context(), cp, domain.SupervisorToolResult{
		CallID: callID, Status: call.Status, ResultJSON: call.ResultJSON, CompletedAt: *call.CompletedAt,
	}); err != nil || !replayed || f.fingerprint(t) != fingerprint {
		t.Fatalf("exact sealed replay drifted: replay=%t err=%v", replayed, err)
	}
	f.finishCall(t, cp, "continue accepted task")
	before := f.guard(t)
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.st = reopened
	t.Cleanup(func() { _ = reopened.Close() })
	if after := f.guard(t); after != before || f.fingerprint(t) != fingerprint {
		t.Fatalf("reopen changed progress evidence: before=%+v after=%+v", before, after)
	}
	for index := 1; index < domain.RunProgressRepeatThreshold; index++ {
		completed := f.completeRead(t, page, "continue accepted task", nil)
		if guard := f.guard(t); guard.StateFingerprint != fingerprint || guard.RepeatedActionCount != index+1 ||
			(index == domain.RunProgressRepeatThreshold-1 && completed.run.Status != domain.RunPaused) {
			t.Fatalf("reopened duplicate read reset progress: guard=%+v run=%s", guard, completed.run.Status)
		}
	}
}

func TestRunProgressGuardInvalidWorkspaceReadDoesNotInventProgress(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*workspace.AgentCodeRead, map[string]any)
	}{
		{"contradictory overlap with new lines", func(page *workspace.AgentCodeRead, _ map[string]any) { page.Content = "forged third\nfourth" }},
		{"body line count", func(page *workspace.AgentCodeRead, _ map[string]any) { page.Content = "only one line" }},
		{"wrong authority scope", func(page *workspace.AgentCodeRead, _ map[string]any) { page.WorkspaceID = "foreign-workspace" }},
		{"wrong root metadata", func(_ *workspace.AgentCodeRead, env map[string]any) {
			env["metadata"] = map[string]string{"root_fingerprint": strings.Repeat("0", 64)}
		}},
		{"wrong hash metadata", func(_ *workspace.AgentCodeRead, env map[string]any) {
			env["metadata"] = map[string]string{"content_sha256": strings.Repeat("0", 64)}
		}},
		{"contradictory file size", func(page *workspace.AgentCodeRead, _ map[string]any) { page.TotalBytes++ }},
		{"contradictory encoding", func(page *workspace.AgentCodeRead, _ map[string]any) { page.Encoding = "utf-8-bom" }},
		{"contradictory newline", func(page *workspace.AgentCodeRead, _ map[string]any) { page.Newline = "crlf" }},
		{"redacted", func(page *workspace.AgentCodeRead, _ map[string]any) { page.RedactionCount = 1 }},
		{"outer truncated", func(_ *workspace.AgentCodeRead, env map[string]any) { env["truncated"] = true }},
		{"missing body", func(_ *workspace.AgentCodeRead, env map[string]any) {
			var page map[string]any
			_ = json.Unmarshal([]byte(env["stdout"].(string)), &page)
			delete(page, "content")
			body, _ := json.Marshal(page)
			env["stdout"] = string(body)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProgressToolFixture(t)
			f.writeFile(t, "read.txt", "first\nsecond\nthird\nfourth\n")
			f.completeRead(t, f.readPage(t, "read.txt", 1, 3), "continue accepted task", nil)
			completeProgressTurn(t, t.Context(), f.st, f.lease, "continue accepted task")
			fingerprint := f.guard(t).StateFingerprint
			page := f.readPage(t, "read.txt", 3, 4)
			completed := f.completeRead(t, page, "continue accepted task", tc.mutate)
			if guard := f.guard(t); guard.StateFingerprint != fingerprint || guard.RepeatedActionCount != domain.RunProgressRepeatThreshold ||
				completed.run.Status != domain.RunPaused {
				t.Fatalf("invalid read invented progress: run=%s guard=%+v", completed.run.Status, guard)
			}
		})
	}
}

func TestRunProgressGuardFileProposalFailureAndReplayDoNotInventProgress(t *testing.T) {
	for _, tc := range []struct {
		name, tool, status, errorCode string
		written, replayed             bool
		mutate                        func(map[string]any, map[string]any)
	}{
		{name: "proposal reports applied", tool: "workspace_change", status: "completed", replayed: true},
		{name: "apply receipt replay", tool: "workspace_apply", status: "completed", replayed: true},
		{name: "no write proof", tool: "workspace_apply", status: "completed"},
		{name: "contradictory replay write", tool: "workspace_apply", status: "completed", written: true, replayed: true},
		{name: "cancelled", tool: "workspace_apply", status: "failed", errorCode: "cancelled", written: true},
		{name: "denied", tool: "workspace_apply", status: "denied", errorCode: "policy_denied", written: true},
		{name: "conflicting metadata", tool: "workspace_apply", status: "completed", written: true,
			mutate: func(_ map[string]any, env map[string]any) {
				env["metadata"] = map[string]string{"workspace_id": "foreign-workspace"}
			}},
		{name: "no content change", tool: "workspace_apply", status: "completed", written: true,
			mutate: func(result map[string]any, _ map[string]any) { result["original_sha256"] = result["proposed_sha256"] }},
		{name: "unexpected original hash", tool: "workspace_apply", status: "completed", written: true,
			mutate: func(result map[string]any, _ map[string]any) { result["original_sha256"] = strings.Repeat("d", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProgressToolFixture(t)
			completeProgressTurn(t, t.Context(), f.st, f.lease, "continue accepted task")
			completeProgressTurn(t, t.Context(), f.st, f.lease, "continue accepted task")
			fingerprint := f.guard(t).StateFingerprint
			payload := progressJSON(t, toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
				EditID: "edit-progress", ExpectedAction: "propose_patch", ExpectedOriginalSHA256: strings.Repeat("a", 64), ExpectedProposedSHA256: strings.Repeat("b", 64)})
			if tc.name == "no content change" {
				payload = progressJSON(t, toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
					EditID: "edit-progress", ExpectedAction: "propose_patch", ExpectedOriginalSHA256: strings.Repeat("b", 64), ExpectedProposedSHA256: strings.Repeat("b", 64)})
			}
			if tc.tool == "workspace_change" {
				payload = progressJSON(t, toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
					Action: "create", Path: "new.txt", ExpectedSHA256: "missing", Content: "proposal only"})
			}
			cp, callID := f.beginCall(t, tc.tool, payload)
			result := map[string]any{"version": "agent-code-tools.v1", "edit_id": "edit-progress", "path": "new.txt",
				"operation": "replace", "status": "applied", "original_sha256": strings.Repeat("a", 64), "proposed_sha256": strings.Repeat("b", 64),
				"file_written": tc.written, "replayed": tc.replayed}
			env := map[string]any{"version": "supervisor_tool_result.v1", "tool": tc.tool, "status": tc.status}
			if tc.mutate != nil {
				tc.mutate(result, env)
			}
			env["stdout"] = string(progressJSON(t, result))
			f.recordResult(t, cp, callID, domain.SupervisorToolCallStatus(tc.status), tc.errorCode, string(progressJSON(t, env)))
			completed := f.finishCall(t, cp, "continue accepted task")
			if guard := f.guard(t); guard.StateFingerprint != fingerprint || guard.RepeatedActionCount != domain.RunProgressRepeatThreshold ||
				completed.run.Status != domain.RunPaused {
				t.Fatalf("non-effect invented progress: run=%s guard=%+v", completed.run.Status, guard)
			}
		})
	}
}

func TestRunProgressGuardPendingAndNotDispatchedDoNotInventProgress(t *testing.T) {
	f := newProgressToolFixture(t)
	completeProgressTurn(t, t.Context(), f.st, f.lease, "continue accepted task")
	completeProgressTurn(t, t.Context(), f.st, f.lease, "continue accepted task")
	fingerprint := f.guard(t).StateFingerprint
	payload := progressJSON(t, toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
		EditID: "edit-not-dispatched", ExpectedAction: "create", ExpectedOriginalSHA256: "missing", ExpectedProposedSHA256: strings.Repeat("b", 64)})
	cp, callID := f.beginCall(t, "workspace_apply", payload)
	if f.fingerprint(t) != fingerprint {
		t.Fatal("pending tool counted as progress")
	}
	if _, err := f.st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{
		RunID: f.run.ID, SessionID: f.run.SessionID, Content: "cancel the stale edit", OperationKey: "progress-cancel-stale-0001", RequestedBy: "operator",
		DeliveryMode: domain.OperatorSteeringCurrentTurn,
	}); err != nil {
		t.Fatal(err)
	}
	if started, superseded, err := f.st.RecordSupervisorToolExecutionStartedWithSteering(t.Context(), cp, callID); err != nil || started || !superseded {
		t.Fatalf("stale edit dispatched: started=%t superseded=%t err=%v", started, superseded, err)
	}
	if f.fingerprint(t) != fingerprint {
		t.Fatal("not-dispatched receipt counted as progress")
	}
	completed := f.finishCall(t, cp, "continue accepted task")
	if guard := f.guard(t); guard.StateFingerprint != fingerprint || guard.RepeatedActionCount != domain.RunProgressRepeatThreshold || completed.run.Status != domain.RunPaused {
		t.Fatalf("not-dispatched edit reset guard: run=%s guard=%+v", completed.run.Status, guard)
	}
}

func TestRunProgressGuardRealWorkspaceApplyResetsCounters(t *testing.T) {
	f := newProgressToolFixtureWithChecker(t, progressApprovalRequiredChecker{policy.NewDefaultChecker()})
	for turn := 1; turn < domain.RunProgressRepeatThreshold; turn++ {
		completeProgressTurn(t, t.Context(), f.st, f.lease, "continue accepted task")
	}
	var replayPayload json.RawMessage
	var replayScope toolgateway.AgentCodeExecutionScope
	for index, path := range []string{"first.txt", "second.txt"} {
		proposeScope := f.scope
		proposeScope.InvocationID = fmt.Sprintf("progress-propose-%d", index)
		proposeScope.OperationKey = fmt.Sprintf("progress-propose-file-%d", index)
		proposed, err := f.executor.ExecuteAgentCode(t.Context(), proposeScope,
			toolgateway.WorkspaceChangeTool, progressJSON(t, toolgateway.WorkspaceChangePayload{
				Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: path,
				ExpectedSHA256: "missing", Content: "real durable contents\n"}))
		if err != nil {
			t.Fatal(err)
		}
		var edit struct {
			EditID   string `json:"edit_id"`
			Original string `json:"original_sha256"`
			Proposed string `json:"proposed_sha256"`
		}
		if err := json.Unmarshal([]byte(proposed.JSON), &edit); err != nil {
			t.Fatal(err)
		}
		if _, err := application.NewFileEditReviewService(f.st).Review(t.Context(), application.ReviewFileEditRequest{
			Version: application.FileEditReviewProtocolVersion, RunID: f.run.ID,
			EditID: edit.EditID, Action: application.FileEditApproveIntent,
		}); err != nil {
			t.Fatal(err)
		}
		payload := progressJSON(t, toolgateway.WorkspaceApplyPayload{
			Version: toolgateway.AgentCodeRegistryVersion, EditID: edit.EditID,
			ExpectedAction: "create", ExpectedOriginalSHA256: edit.Original,
			ExpectedProposedSHA256: edit.Proposed})
		cp, callID := f.beginCall(t, "workspace_apply", payload)
		if started, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), cp, callID); err != nil || !started {
			t.Fatalf("real apply execution start=%t err=%v", started, err)
		}
		applyScope := f.scope
		applyScope.InvocationID = callID
		applyScope.OperationKey = runmutation.SupervisorToolOperationKey(f.run.ID,
			cp.NextTurn, "workspace_apply", string(payload))
		applied, err := f.executor.ExecuteAgentCode(t.Context(), applyScope,
			toolgateway.WorkspaceApplyTool, payload)
		if err != nil || !strings.Contains(applied.JSON, `"file_written":true`) {
			t.Fatalf("real apply result=%s err=%v", applied.JSON, err)
		}
		if data, err := os.ReadFile(filepath.Join(f.root, path)); err != nil || string(data) != "real durable contents\n" {
			t.Fatalf("actual file contents=%q err=%v", data, err)
		}
		call := f.recordResult(t, cp, callID, domain.SupervisorToolCompleted, "",
			f.envelope(t, "workspace_apply", applied.JSON, applied.Metadata))
		fingerprint := f.fingerprint(t)
		if _, replayed, err := f.st.RecordSupervisorToolResult(t.Context(), cp, domain.SupervisorToolResult{
			CallID: callID, Status: call.Status, ResultJSON: call.ResultJSON, CompletedAt: *call.CompletedAt,
		}); err != nil || !replayed || f.fingerprint(t) != fingerprint {
			t.Fatalf("exact applied ledger replay drifted: replay=%t err=%v", replayed, err)
		}
		completed := f.finishCall(t, cp, "continue accepted task")
		guard := f.guard(t)
		if completed.run.Status != domain.RunRunning || guard.RepeatedActionCount != 1 || guard.StagnantTurnCount != 1 {
			t.Fatalf("real file apply was mistaken for livelock: run=%s guard=%+v", completed.run.Status, guard)
		}
		replayPayload, replayScope = payload, applyScope
	}
	fingerprint := f.guard(t).StateFingerprint
	for count := 2; count <= domain.RunProgressRepeatThreshold; count++ {
		cp, callID := f.beginCall(t, "workspace_apply", replayPayload)
		if started, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), cp, callID); err != nil || !started {
			t.Fatalf("real replay execution start=%t err=%v", started, err)
		}
		replayed, err := f.executor.ExecuteAgentCode(t.Context(), replayScope, toolgateway.WorkspaceApplyTool, replayPayload)
		if err != nil || !replayed.Replayed || !strings.Contains(replayed.JSON, `"file_written":false`) {
			t.Fatalf("real apply replay=%s err=%v", replayed.JSON, err)
		}
		f.recordResult(t, cp, callID, domain.SupervisorToolCompleted, "", f.envelope(t, "workspace_apply", replayed.JSON, replayed.Metadata))
		completed := f.finishCall(t, cp, "continue accepted task")
		if guard := f.guard(t); guard.StateFingerprint != fingerprint || guard.RepeatedActionCount != count ||
			(count == domain.RunProgressRepeatThreshold && completed.run.Status != domain.RunPaused) {
			t.Fatalf("real idempotent replay reset counters: run=%s guard=%+v", completed.run.Status, guard)
		}
	}
}

func TestRunProgressGuardReplaceApplyAliasIsObserved(t *testing.T) {
	f := newProgressToolFixtureWithChecker(t, progressApprovalRequiredChecker{policy.NewDefaultChecker()})
	f.writeFile(t, "replace.txt", "before\n")
	for turn := 1; turn < domain.RunProgressRepeatThreshold; turn++ {
		completeProgressTurn(t, t.Context(), f.st, f.lease, "continue accepted task")
	}
	proposeScope := f.scope
	proposeScope.InvocationID, proposeScope.OperationKey = "progress-replace-propose", "progress-replace-propose-0001"
	page := f.readPage(t, "replace.txt", 1, 1)
	proposed, err := f.executor.ExecuteAgentCode(t.Context(), proposeScope, toolgateway.WorkspaceChangeTool,
		progressJSON(t, toolgateway.WorkspaceChangePayload{Version: toolgateway.AgentCodeRegistryVersion,
			Action: "replace", Path: "replace.txt", ExpectedSHA256: page.ContentSHA256, Content: "after\n"}))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ApplyArguments toolgateway.WorkspaceApplyPayload `json:"apply_arguments"`
	}
	if err := json.Unmarshal([]byte(proposed.JSON), &result); err != nil || result.ApplyArguments.ExpectedAction != "replace" {
		t.Fatalf("replace apply contract=%+v err=%v", result, err)
	}
	if _, err := application.NewFileEditReviewService(f.st).Review(t.Context(), application.ReviewFileEditRequest{
		Version: application.FileEditReviewProtocolVersion, RunID: f.run.ID,
		EditID: result.ApplyArguments.EditID, Action: application.FileEditApproveIntent,
	}); err != nil {
		t.Fatal(err)
	}
	payload := progressJSON(t, result.ApplyArguments)
	cp, callID := f.beginCall(t, "workspace_apply", payload)
	if _, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), cp, callID); err != nil {
		t.Fatal(err)
	}
	applyScope := f.scope
	applyScope.InvocationID = callID
	applyScope.OperationKey = runmutation.SupervisorToolOperationKey(f.run.ID, cp.NextTurn, "workspace_apply", string(payload))
	applied, err := f.executor.ExecuteAgentCode(t.Context(), applyScope, toolgateway.WorkspaceApplyTool, payload)
	if err != nil || !strings.Contains(applied.JSON, `"file_written":true`) {
		t.Fatalf("replace=%s err=%v", applied.JSON, err)
	}
	f.recordResult(t, cp, callID, domain.SupervisorToolCompleted, "", f.envelope(t, "workspace_apply", applied.JSON, applied.Metadata))
	completed := f.finishCall(t, cp, "continue accepted task")
	if guard := f.guard(t); completed.run.Status != domain.RunRunning || guard.RepeatedActionCount != 1 || guard.StagnantTurnCount != 1 {
		t.Fatalf("new replace action was mistaken for livelock: run=%s guard=%+v", completed.run.Status, guard)
	}
	if data, err := os.ReadFile(filepath.Join(f.root, "replace.txt")); err != nil || string(data) != "after\n" {
		t.Fatalf("replace file=%q err=%v", data, err)
	}
}

func TestRunProgressGuardOneStepWorkspaceChangeResetsCountersAndExcludesProposals(t *testing.T) {
	f := newProgressToolFixture(t)
	var prevFingerprint string
	runOneStep := func(payload toolgateway.WorkspaceChangePayload, opKeyOverride string) (completedProgressTurn, domain.RunProgressGuard, string) {
		t.Helper()
		raw := progressJSON(t, payload)
		cp, callID := f.beginCall(t, "workspace_change", raw)
		if started, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), cp, callID); err != nil || !started {
			t.Fatalf("one-step start=%t err=%v", started, err)
		}
		scope := f.scope
		scope.InvocationID = callID
		if opKeyOverride != "" {
			scope.OperationKey = opKeyOverride
		} else {
			scope.OperationKey = runmutation.SupervisorToolOperationKey(f.run.ID, cp.NextTurn, "workspace_change", string(raw))
		}
		res, err := f.executor.ExecuteAgentCode(t.Context(), scope, toolgateway.WorkspaceChangeTool, raw)
		if err != nil {
			t.Fatalf("ExecuteAgentCode(%s) err=%v", payload.Action, err)
		}
		f.recordResult(t, cp, callID, domain.SupervisorToolCompleted, "", f.envelope(t, "workspace_change", res.JSON, res.Metadata))
		completed := f.finishCall(t, cp, "continue accepted task")
		return completed, f.guard(t), res.JSON
	}

	// Execute StagnantThreshold+2 turns of genuine 1-step workspace_change writes
	// (create, replace, propose_patch, move, and more creates). None may trigger livelock_detected.
	steps := []toolgateway.WorkspaceChangePayload{
		{Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: "step1.txt", ExpectedSHA256: "missing", Content: "v1\n"},
		{Version: toolgateway.AgentCodeRegistryVersion, Action: "replace", Path: "step1.txt", ExpectedSHA256: fileedit.HashText("v1\n"), Content: "v2\n"},
		{Version: toolgateway.AgentCodeRegistryVersion, Action: "propose_patch", Path: "step1.txt", ExpectedSHA256: fileedit.HashText("v2\n"),
			Replacements: []toolgateway.WorkspaceReplacement{{OldText: "v2", NewText: "v3", ExpectedOccurrences: 1}}},
		{Version: toolgateway.AgentCodeRegistryVersion, Action: "move", Path: "step1.txt", ExpectedSHA256: fileedit.HashText("v3\n"),
			DestinationPath: "step1-moved.txt", DestinationExpectedSHA256: "missing"},
		{Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: "step5.txt", ExpectedSHA256: "missing", Content: "five\n"},
		{Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: "step6.txt", ExpectedSHA256: "missing", Content: "six\n"},
		{Version: toolgateway.AgentCodeRegistryVersion, Action: "create", Path: "step7.txt", ExpectedSHA256: "missing", Content: "seven\n"},
	}
	if len(steps) <= domain.RunProgressStagnantThreshold {
		t.Fatalf("expected > %d steps to cross stagnant threshold", domain.RunProgressStagnantThreshold)
	}
	var lastOpKey string
	var lastPayload toolgateway.WorkspaceChangePayload
	for i, step := range steps {
		completed, guard, out := runOneStep(step, "")
		if !strings.Contains(out, `"status":"applied"`) || !strings.Contains(out, `"file_written":true`) {
			t.Fatalf("step %d (%s) was not 1-step applied: %s", i, step.Action, out)
		}
		if completed.run.Status != domain.RunRunning || guard.Status != domain.RunProgressObserving ||
			guard.RepeatedActionCount != 1 || guard.StagnantTurnCount != 1 || guard.StateFingerprint == prevFingerprint {
			t.Fatalf("step %d (%s) failed to advance progress: run=%s guard=%+v prevFP=%s", i, step.Action, completed.run.Status, guard, prevFingerprint)
		}
		prevFingerprint = guard.StateFingerprint
		lastPayload = step
		lastOpKey = runmutation.SupervisorToolOperationKey(f.run.ID, completed.checkpoint.NextTurn, "workspace_change", string(progressJSON(t, step)))
	}

	// Idempotent replay of the last applied workspace_change (same operation key -> file_written:false, replayed:true)
	// must NOT advance StateFingerprint.
	_, replayGuard, replayOut := runOneStep(lastPayload, lastOpKey)
	if !strings.Contains(replayOut, `"file_written":false`) || !strings.Contains(replayOut, `"replayed":true`) {
		t.Fatalf("expected idempotent replay, got %s", replayOut)
	}
	if replayGuard.StateFingerprint != prevFingerprint || replayGuard.StagnantTurnCount != 2 {
		t.Fatalf("replayed workspace_change invented progress: prevFP=%s guard=%+v", prevFingerprint, replayGuard)
	}

	// Proposal-only workspace_change calls (status="proposed", no file_written) must NOT count as progress
	// and must still pause at RunProgressRepeatThreshold.
	fProp := newProgressToolFixtureWithChecker(t, progressApprovalRequiredChecker{policy.NewDefaultChecker()})
	completeProgressTurn(t, t.Context(), fProp.st, fProp.lease, "continue accepted task")
	propBaselineFP := fProp.guard(t).StateFingerprint
	for count := 2; count <= domain.RunProgressRepeatThreshold; count++ {
		raw := progressJSON(t, toolgateway.WorkspaceChangePayload{
			Version: toolgateway.AgentCodeRegistryVersion, Action: "create",
			Path: fmt.Sprintf("proposal-only-%d.txt", count), ExpectedSHA256: "missing", Content: "pending\n",
		})
		cp, callID := fProp.beginCall(t, "workspace_change", raw)
		if started, err := fProp.st.RecordSupervisorToolExecutionStarted(t.Context(), cp, callID); err != nil || !started {
			t.Fatalf("proposal start=%t err=%v", started, err)
		}
		scope := fProp.scope
		scope.InvocationID = callID
		scope.OperationKey = runmutation.SupervisorToolOperationKey(fProp.run.ID, cp.NextTurn, "workspace_change", string(raw))
		res, err := fProp.executor.ExecuteAgentCode(t.Context(), scope, toolgateway.WorkspaceChangeTool, raw)
		if err != nil || !strings.Contains(res.JSON, `"status":"proposed"`) || strings.Contains(res.JSON, `"file_written":true`) {
			t.Fatalf("expected proposal-only result, got %s err=%v", res.JSON, err)
		}
		fProp.recordResult(t, cp, callID, domain.SupervisorToolCompleted, "", fProp.envelope(t, "workspace_change", res.JSON, res.Metadata))
		completed := fProp.finishCall(t, cp, "continue accepted task")
		guard := fProp.guard(t)
		if guard.StateFingerprint != propBaselineFP || guard.RepeatedActionCount != count {
			t.Fatalf("proposal-only workspace_change invented progress: count=%d baseline=%s guard=%+v", count, propBaselineFP, guard)
		}
		if count == domain.RunProgressRepeatThreshold && completed.run.Status != domain.RunPaused {
			t.Fatalf("proposal-only loop did not pause at repeat threshold: run=%s guard=%+v", completed.run.Status, guard)
		}
	}
}

type progressApprovalRequiredChecker struct {
	policy.Checker
}

func (c progressApprovalRequiredChecker) CheckToolCall(call tools.Call) policy.Decision {
	decision := c.Checker.CheckToolCall(call)
	if decision.Allowed && (call.Name == "create_file" || call.Name == "replace_file" || call.Name == "move_file") {
		decision.NeedsApproval = true
	}
	return decision
}

type progressToolFixture struct {
	st        *SQLiteStore
	run       domain.Run
	lease     domain.RunExecutionLease
	root      string
	path      string
	authority json.RawMessage
	scope     toolgateway.AgentCodeExecutionScope
	executor  *application.AgentCodeToolExecutor
}

func newProgressToolFixture(t *testing.T) *progressToolFixture {
	return newProgressToolFixtureWithChecker(t, policy.NewDefaultChecker())
}

func newProgressToolFixtureWithChecker(t *testing.T, checker policy.Checker) *progressToolFixture {
	t.Helper()
	f := &progressToolFixture{root: t.TempDir(), path: filepath.Join(t.TempDir(), "progress-tools.db")}
	f.st = openCurrentTestDatabase(t, f.path)
	workspaceRecord := WorkspaceRecord{ID: "workspace-progress-tools", Name: "progress tools", RootPath: f.root}
	if err := f.st.SaveWorkspace(t.Context(), workspaceRecord); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(f.st)
	mission, created, err := runs.Create(t.Context(), application.CreateRunRequest{
		Goal: "observe file progress", Profile: "code", WorkspaceID: workspaceRecord.ID,
		Budget: domain.Budget{MaxTurns: 20, MaxToolCalls: 60}})
	if err != nil {
		t.Fatal(err)
	}
	runtimeAuthority := domain.NewExecutionPermissionRuntimeAuthority()
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: runtimeAuthority}
	selected, err := application.NewRunExecutionPermissionService(f.st, capabilities).Change(t.Context(),
		application.ChangeRunExecutionPermissionRequest{RunID: created.ID, Mode: string(domain.RunExecutionPermissionFull),
			OperationKey: "progress-full-access-0001", RequestedBy: "operator", Reason: "test actual file effects", ConfirmFull: true})
	if err != nil {
		t.Fatal(err)
	}
	f.run, err = runs.Start(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeAuthority.ActivateRunFullAccess(selected.Permission); err != nil {
		t.Fatal(err)
	}
	generation, live := capabilities.FullAccessGeneration(selected.Permission)
	if !live {
		t.Fatal("runtime grant was not activated")
	}
	f.lease = acquireTestRunExecutionLease(t, t.Context(), f.st, f.run.ID)
	mode, err := f.st.GetRunMode(t.Context(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, found, err := f.st.GetRootAgent(t.Context(), f.run.ID)
	if err != nil || !found {
		t.Fatalf("root agent found=%t err=%v", found, err)
	}
	rootHash, err := workspace.AgentCodeRootFingerprint(f.root)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := runtimeAuthority.IssueRunAuthorizationFence(f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	capabilityContext := toolgateway.AgentCodeCapabilityContext{RunID: f.run.ID, MissionID: mission.ID,
		RootAgentID: agent.ID, WorkspaceID: workspaceRecord.ID, RootFingerprint: rootHash,
		Surface: mode.Surface, Phase: mode.Phase, Role: agent.Role, Profile: agent.Profile,
		PermissionMode: selected.Permission.Mode, PermissionSnapshotID: selected.Permission.ID,
		PermissionGeneration: generation, PermissionRuntimeEpoch: runtimeAuthority.RuntimeEpoch(), RunAuthorizationFence: fence,
		ModeRevision: mode.Revision, PermissionRevision: selected.Permission.Revision}
	authority, err := toolgateway.NewAgentCodeCallAuthority(capabilityContext, f.run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	f.authority, err = toolgateway.EncodeAgentCodeCallAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	f.scope = toolgateway.AgentCodeExecutionScope{InvocationID: "progress-tool-invocation", OperationKey: "progress-tool-operation",
		RunID: f.run.ID, MissionID: mission.ID, RootAgentID: agent.ID, SessionID: f.run.SessionID,
		WorkspaceID: workspaceRecord.ID, WorkspaceRoot: f.root, RootFingerprint: rootHash,
		Surface: mode.Surface, Phase: mode.Phase, Role: agent.Role, Profile: agent.Profile,
		PermissionMode: selected.Permission.Mode, PermissionSnapshotID: selected.Permission.ID,
		PermissionGeneration: generation, PermissionRuntimeEpoch: runtimeAuthority.RuntimeEpoch(), RunAuthorizationFence: fence,
		ModeRevision: mode.Revision, PermissionRevision: selected.Permission.Revision,
		CapabilityGeneration: authority.CapabilityGeneration, LeaseID: f.lease.LeaseID, LeaseGeneration: f.lease.Generation,
		RequestedBy: "run_supervisor", PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "low", Reason: "test allowed"}}
	f.executor = application.NewAgentCodeToolExecutor(f.st, checker).WithExecutionPermissionCapabilities(capabilities)
	return f
}

func progressJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func (f *progressToolFixture) beginCall(t *testing.T, name string, payload json.RawMessage) (domain.SupervisorCheckpoint, string) {
	t.Helper()
	turn, err := f.st.BeginSupervisorTurn(t.Context(), f.lease, "continue accepted task")
	if err != nil {
		t.Fatal(err)
	}
	key := runmutation.SupervisorToolOperationKey(f.run.ID, turn.Checkpoint.NextTurn, name, string(payload))
	callID, err := runmutation.SupervisorToolCallID(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"}
	if _, err := f.st.RecordSupervisorModelStarted(t.Context(), turn.Checkpoint, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	cp, err := f.st.RecordSupervisorModelCompleted(t.Context(), turn.Checkpoint, attempt,
		llm.ChatResponse{Provider: "test", Model: "model", ToolCalls: []llm.ToolCall{{ID: callID, Name: name, Arguments: payload, Authority: f.authority}}})
	if err != nil {
		t.Fatal(err)
	}
	return cp, callID
}

func (f *progressToolFixture) envelope(t *testing.T, name, stdout string, metadata map[string]string) string {
	t.Helper()
	values := map[string]string{"workspace_id": f.scope.WorkspaceID, "root_fingerprint": f.scope.RootFingerprint}
	for key, value := range metadata {
		values[key] = value
	}
	return string(progressJSON(t, map[string]any{"version": "supervisor_tool_result.v1", "tool": name,
		"status": "completed", "stdout": stdout, "metadata": values}))
}

func (f *progressToolFixture) recordResult(t *testing.T, cp domain.SupervisorCheckpoint, callID string,
	status domain.SupervisorToolCallStatus, errorCode, resultJSON string,
) domain.SupervisorToolCall {
	t.Helper()
	if _, err := f.st.RecordSupervisorToolExecutionStarted(t.Context(), cp, callID); err != nil {
		t.Fatal(err)
	}
	call, replayed, err := f.st.RecordSupervisorToolResult(t.Context(), cp, domain.SupervisorToolResult{
		CallID: callID, Status: status, ErrorCode: errorCode, ResultJSON: resultJSON, CompletedAt: time.Now().UTC()})
	if err != nil || replayed {
		t.Fatalf("record result replay=%t err=%v", replayed, err)
	}
	return call
}

func (f *progressToolFixture) finishCall(t *testing.T, cp domain.SupervisorCheckpoint, message string) completedProgressTurn {
	t.Helper()
	_, sequence, err := f.st.ClaimSupervisorMidTurnSteering(t.Context(), cp)
	if err != nil {
		t.Fatal(err)
	}
	attempt := llm.ModelAttempt{Number: 2, ToolRound: 1, SteeringSequence: sequence,
		TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"}
	if _, err := f.st.RecordSupervisorModelStarted(t.Context(), cp, attempt); err != nil {
		t.Fatal(err)
	}
	attempt.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{Text: message, Provider: "test", Model: "model"}
	cp, err = f.st.RecordSupervisorModelCompleted(t.Context(), cp, attempt, response)
	if err != nil {
		t.Fatal(err)
	}
	action := domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: message}
	run, completed, _, err := f.st.CompleteSupervisorTurn(t.Context(), cp, response, action, policy.Decision{Allowed: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return completedProgressTurn{checkpoint: cp, response: response, action: action, run: run, completed: completed}
}

func (f *progressToolFixture) guard(t *testing.T) domain.RunProgressGuard {
	t.Helper()
	guard, found, err := f.st.GetRunProgressGuard(t.Context(), f.run.ID)
	if err != nil || !found {
		t.Fatalf("progress guard found=%t err=%v", found, err)
	}
	return guard
}

func (f *progressToolFixture) writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, path), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *progressToolFixture) readPage(t *testing.T, path string, start, end int) workspace.AgentCodeRead {
	t.Helper()
	page, err := workspace.AgentCodeReadFile(f.root, f.scope.WorkspaceID, path, start, end, false)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func (f *progressToolFixture) completeRead(t *testing.T, page workspace.AgentCodeRead, message string,
	mutate func(*workspace.AgentCodeRead, map[string]any),
) completedProgressTurn {
	t.Helper()
	payload := progressJSON(t, toolgateway.WorkspaceReadPayload{Version: toolgateway.AgentCodeRegistryVersion,
		Path: page.Path, StartLine: page.StartLine, EndLine: page.EndLine})
	cp, callID := f.beginCall(t, "workspace_read", payload)
	env := map[string]any{"version": "supervisor_tool_result.v1", "tool": "workspace_read", "status": "completed",
		"stdout": string(progressJSON(t, page)), "metadata": map[string]string{"artifact_stdout_id": callID,
			"workspace_id": f.scope.WorkspaceID, "root_fingerprint": f.scope.RootFingerprint}}
	if mutate != nil {
		originalStdout := env["stdout"]
		mutate(&page, env)
		if env["stdout"] == originalStdout {
			env["stdout"] = string(progressJSON(t, page))
		}
	}
	f.recordResult(t, cp, callID, domain.SupervisorToolCompleted, "", string(progressJSON(t, env)))
	return f.finishCall(t, cp, message)
}

func (f *progressToolFixture) fingerprint(t *testing.T) string {
	t.Helper()
	tx, err := f.st.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	fingerprint, err := supervisorProgressStateFingerprintTx(t.Context(), tx, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
