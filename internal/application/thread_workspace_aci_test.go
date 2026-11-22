package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/llm"
)

// A scripted decision maker verifies the production Thread/Supervisor path:
// repeated reads receive advice, native pairs survive, and the next segment can
// patch using a retained page without a mandatory reread. Approval remains real.
func TestThreadWorkspaceACIReadFeedbackAndRetainedPagePatch(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: int64(domain.MaxSupervisorToolRounds + 10)})
	provider := &boundaryJourneyProvider{}
	var edit fileedit.Edit
	provider.respond = func(ctx context.Context, request llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		if hasToolSpec(request, "workspace_change") {
			permission, err := st.GetRunExecutionPermission(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, message := range request.Messages {
				found = found || (message.Role == "system" && strings.Contains(message.Content, "Go-issued file-edit capability snapshot") && strings.Contains(message.Content, fmt.Sprintf(`"permission_mode":%q`, permission.Mode)))
			}
			if !found {
				t.Fatal("actual model request omitted the file-edit approval policy; internal metadata is insufficient")
			}
		}
		paired := map[string]int{}
		for _, msg := range request.Messages {
			for _, call := range msg.ToolCalls {
				paired[call.ID]++
			}
			for _, result := range msg.ToolResults {
				paired[result.ToolCallID]--
			}
		}
		for id, count := range paired {
			if count != 0 {
				t.Fatalf("unpaired native call %s", id)
			}
		}
		feedback := false
		for _, msg := range request.Messages {
			feedback = feedback || strings.Contains(msg.Content, "Execution feedback from the completed tool ledger:")
		}
		if index >= 3 && index <= domain.MaxSupervisorToolRounds && !feedback {
			t.Fatal("actual model request omitted repeated-read feedback")
		}
		if index == domain.MaxSupervisorToolRounds+1 && feedback {
			t.Fatal("boundary received contradictory tool advice")
		}
		switch {
		case index <= domain.MaxSupervisorToolRounds:
			return boundaryRead(fmt.Sprintf("aci-read-%d", index), 1), nil
		case index == domain.MaxSupervisorToolRounds+1:
			assertBoundaryPrompt(t, request)
			return textResponse(rootActionResponse(domain.RootActionContinue, "Apply the requested edit next", "", "")), nil
		case index == domain.MaxSupervisorToolRounds+2:
			content := boundaryContextText(t, request)
			var observedText, observedHash string
			for _, line := range strings.Split(content, "\n") {
				var receipt struct {
					Tool   string `json:"tool"`
					Result struct {
						Content string `json:"content"`
						Hash    string `json:"content_sha256"`
					} `json:"result"`
				}
				if json.Unmarshal([]byte(line), &receipt) == nil && receipt.Tool == "workspace_read" && receipt.Result.Content != "" {
					observedText, observedHash = receipt.Result.Content, receipt.Result.Hash
				}
			}
			if observedText == "" || len(observedHash) != 64 {
				t.Fatal("no usable retained page")
			}
			args, _ := json.Marshal(map[string]any{"version": "agent-code-tools.v1", "action": "propose_patch", "path": "README.md", "expected_sha256": observedHash, "replacements": []map[string]any{{"old_text": observedText, "new_text": "reviewed text", "expected_occurrences": 1}}})
			return toolResponse("aci-patch", "workspace_change", string(args)), nil
		case index == domain.MaxSupervisorToolRounds+3:
			if data, _ := os.ReadFile(filepath.Join(root, "README.md")); string(data) != "original text\n" {
				t.Fatal("proposal wrote before approval/apply")
			}
			edit = approveBoundaryEdit(t, st, run)
			return boundaryApply("aci-apply", edit), nil
		case index == domain.MaxSupervisorToolRounds+4:
			return boundaryRead("aci-verify", 1), nil
		case index == domain.MaxSupervisorToolRounds+5:
			if !hasToolResult(request, "reviewed text") {
				t.Fatal("verified file not delivered to model")
			}
			return textResponse(rootActionResponse(domain.RootActionFinish, "Verified requested edit", "done", "")), nil
		default:
			return nil, fmt.Errorf("unexpected call %d", index)
		}
	}
	if _, err := toolBoundaryServiceWithPolicy(st, st, provider, &fileOperationPolicy{review: true}).Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(provider.Requests()) != domain.MaxSupervisorToolRounds+5 {
		t.Fatalf("unexpected calls %d", len(provider.Requests()))
	}
	if data, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(data) != "reviewed text\n" {
		t.Fatalf("file=%q err=%v", data, err)
	}
	assertOneBoundaryTranscriptInput(t, st, input.ThreadID, input.Content)
}

func TestThreadReplaceApplyPreBoundaryFailureKeepsExplicitRecovery(t *testing.T) {
	st, run, root, input := toolBoundaryFixture(t, domain.Budget{MaxTurns: 8, MaxToolCalls: 20})
	proposal := &scriptedToolProvider{responses: []*llm.ChatResponse{
		boundaryPropose("replacement-proposal"), textResponse(rootActionResponse(domain.RootActionWait, "Review proposal", "", "review")),
	}}
	if _, err := toolBoundaryServiceWithPolicy(st, st, proposal, &fileOperationPolicy{review: true}).Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	edit := approveBoundaryEdit(t, st, run)
	apply := func(id string) *llm.ChatResponse {
		return toolResponse(id, "workspace_apply", fmt.Sprintf(`{"version":"agent-code-tools.v1","edit_id":%q,"expected_action":"replace","expected_original_sha256":%q,"expected_proposed_sha256":%q}`, edit.ID, edit.OriginalHash, edit.ProposedHash))
	}
	input.OperationKey, input.Content = "aci-replace-preboundary-fail", "Apply the reviewed replacement"
	failing := &beforeFileBoundaryStore{st}
	if _, err := toolBoundaryService(failing, failing, &scriptedToolProvider{responses: []*llm.ChatResponse{apply("failed-apply")}}).Execute(t.Context(), input); err == nil {
		t.Fatal("injected preparation failure was concealed")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "README.md")); string(data) != "original text\n" {
		t.Fatal("failed preparation wrote bytes")
	}
	if _, found, err := st.GetFailedFileApplyOperationKey(t.Context(), run.ID, edit.ID, beforeBoundaryRootAgent(t, st, run.ID)); err != nil || !found {
		t.Fatalf("replace alias lost the sealed pre-boundary recovery: found=%t err=%v", found, err)
	}
	// A new operator request explicitly retries; the failed request is not
	// silently replayed and its receipt is not changed into success.
	input.OperationKey, input.Content = "aci-replace-preboundary-retry", "Retry only the same reviewed replacement with exact hashes"
	p := &scriptedToolProvider{responses: []*llm.ChatResponse{apply("explicit-retry"), textResponse(rootActionResponse(domain.RootActionFinish, "Applied reviewed replacement", "done", ""))}}
	service := toolBoundaryService(st, st, p)
	if _, err := service.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "README.md")); string(data) != "reviewed text\n" {
		t.Fatal("explicit retry did not apply exact content")
	}
	if _, err := service.Execute(t.Context(), input); err != nil || len(p.Requests()) != 2 {
		t.Fatal("same-key confirmation repeated the replacement", err)
	}
}
