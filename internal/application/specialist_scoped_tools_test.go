package application_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func scopedToolService(st *store.SQLiteStore, p *specialistTestProvider) *application.SpecialistExecutionService {
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	return application.NewSpecialistExecutionService(st, router, policy.NewDefaultChecker())
}
func scopedReadCall(id, path string) llm.ToolCall {
	raw, _ := json.Marshal(map[string]any{"version": "agent-code-tools.v1", "path": path, "start_line": 1, "end_line": 5})
	return llm.ToolCall{ID: id, Name: string(toolgateway.WorkspaceReadTool), Arguments: raw}
}
func scopedLatestResult(t *testing.T, r llm.ChatRequest) string {
	t.Helper()
	last := r.Messages[len(r.Messages)-1]
	if len(last.ToolResults) != 1 {
		t.Fatalf("expected one completed observation: %+v", last)
	}
	return last.ToolResults[0].Content
}

func TestSpecialistScopedEvidenceSelectsSecondToolThenSurvivesReopenAndRevocation(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	for _, dir := range []string{"auth", "other"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	needle := "symbol-" + idgen.New("evidence")
	for path, body := range map[string]string{"auth/index.txt": needle, "auth/implementation.txt": "guard " + needle, "other/private.txt": "private " + needle} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	usage := llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}
	provider := &specialistTestProvider{}
	provider.respond = func(index int, r llm.ChatRequest) (llm.ChatResponse, error) {
		encoded, _ := json.Marshal(r.Messages)
		if !strings.Contains(string(encoded), "Trace the assigned auth guard") {
			t.Fatal("effective delegation missing from continuation")
		}
		switch index {
		case 0:
			return llm.ChatResponse{ToolCalls: []llm.ToolCall{scopedReadCall("first", "auth/index.txt")}, Usage: usage}, nil
		case 1:
			var receipt struct {
				Result struct {
					Content string `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(scopedLatestResult(t, r)), &receipt); err != nil || receipt.Result.Content == "" {
				t.Fatalf("first read missing: %+v %v", receipt, err)
			}
			args, _ := json.Marshal(map[string]any{"version": "agent-code-tools.v1", "query": strings.TrimSpace(receipt.Result.Content), "pattern": "**/implementation.txt", "limit": 10, "case_sensitive": true})
			return llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "derived-search", Name: string(toolgateway.WorkspaceGrepTool), Arguments: args}}, Usage: usage}, nil
		case 2:
			raw := scopedLatestResult(t, r)
			if !strings.Contains(raw, "auth/implementation.txt") || strings.Contains(raw, "other/private.txt") {
				t.Fatalf("second scoped evidence: %s", raw)
			}
			return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "The entry points to the guard in auth/implementation.txt."}), Usage: usage}, nil
		default:
			return llm.ChatResponse{}, fmt.Errorf("unexpected provider dispatch %d", index)
		}
	}
	st, run, child, _ := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 10, MaxToolCalls: 20}, 3, 128, false, domain.SpecialistAdmission{AllowedReadPaths: []string{"auth"}, ToolCallLimit: 2}, home, root)
	instruction, _ := json.Marshal(domain.AgentInstructionPayload{Version: domain.SpecialistInstructionVersion, Instruction: "Trace the assigned auth guard"})
	if _, _, err := st.SendAgentMessage(t.Context(), domain.AgentMessage{ID: idgen.New("agentmsg"), RunID: run.ID, SenderAgentID: child.ParentID, RecipientAgentID: child.ID, Kind: domain.AgentMessageInstruction, Semantic: domain.AgentMessageSemanticMessage, PayloadJSON: string(instruction)}, "scoped-effective-delegation-0001"); err != nil {
		t.Fatal(err)
	}
	request := application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "scoped-proof-attempt-0001", WorkspaceTools: true}
	result, err := scopedToolService(st, provider).ExecuteSpecialist(t.Context(), request)
	if err != nil {
		t.Fatalf("two-step: %+v %v", result, err)
	}
	ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID}
	rounds, err := st.ListSpecialistToolRounds(t.Context(), ref)
	if err != nil || len(rounds) != 2 {
		t.Fatalf("two durable rounds: %+v %v", rounds, err)
	}
	for _, round := range rounds {
		if len(round.Calls) != 1 || round.Calls[0].Ref != ref || round.Calls[0].ChargeID == "" || round.Calls[0].Status != domain.SupervisorToolCompleted {
			t.Fatalf("child receipt identity: %+v", round)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(filepath.Join(home, "cyberagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	current, err := st.GetAgentNode(t.Context(), child.ID)
	if err != nil || current.ToolCallsUsed != 2 || current.ToolCallLimit != 2 || strings.Join(current.AllowedReadPaths, ",") != "auth" {
		t.Fatalf("reopened limits: %+v %v", current, err)
	}
	replay, err := scopedToolService(st, provider).ExecuteSpecialist(t.Context(), request)
	if err != nil || !replay.Replayed || provider.calls != 3 {
		t.Fatalf("replay dispatched: %+v %v", replay, err)
	}
	if _, err := st.RevokeSpecialistReadTools(t.Context(), run.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	request.OperationKey = "scoped-proof-attempt-0002"
	if _, err := scopedToolService(st, provider).ExecuteSpecialist(t.Context(), request); apperror.CodeOf(err) != apperror.CodePolicyDenied || provider.calls != 3 {
		t.Fatalf("revocation restored authority: %v calls=%d", err, provider.calls)
	}
}

func TestSpecialistChildScopesAndToolBudgetsStayIndependentAcrossTurns(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "b"} {
		os.Mkdir(filepath.Join(root, dir), 0700)
		os.WriteFile(filepath.Join(root, dir, "proof.txt"), []byte(dir+"-private"), 0600)
	}
	usage := llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	p := &specialistTestProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{scopedReadCall("outside", "b/proof.txt"), scopedReadCall("inside", "a/proof.txt")}, Usage: usage},
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "continue"}), Usage: usage},
		{ToolCalls: []llm.ToolCall{scopedReadCall("next-turn", "a/proof.txt")}, Usage: usage},
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "budget reached"}), Usage: usage},
		{ToolCalls: []llm.ToolCall{scopedReadCall("second-child", "b/proof.txt")}, Usage: usage},
		{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "own evidence"}), Usage: usage},
	}}
	home := t.TempDir()
	st, run, child, _ := newScopedSubagentFixture(t, p, domain.Budget{MaxTurns: 12, MaxToolCalls: 10}, 3, 128, false, domain.SpecialistAdmission{AllowedReadPaths: []string{"a"}, ToolCallLimit: 2, MaxChildren: 2}, home, root)
	second, _, err := st.AdmitSpecialist(t.Context(), domain.SpecialistAdmission{AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID, ParentAgentID: child.ParentID, Title: "other scope", Skills: []string{"model.chat"}, TurnLimit: 2, TokenLimit: 64, MaxChildren: 2, CreatedAt: time.Now().UTC(), AllowedReadPaths: []string{"b"}, ToolCallLimit: 1}, "scoped-second-child-admit")
	if err != nil {
		t.Fatal(err)
	}
	service := scopedToolService(st, p)
	for index, agent := range []string{child.ID, child.ID, second.ID} {
		if index == 1 {
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = store.Open(filepath.Join(home, "cyberagent.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			service = scopedToolService(st, p)
		}

		result, err := service.ExecuteSpecialist(context.Background(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: agent, OperationKey: fmt.Sprintf("scoped-budget-attempt-%04d", index), WorkspaceTools: true})
		if err != nil {
			t.Fatalf("turn %d: %v", index, err)
		}
		rounds, err := st.ListSpecialistToolRounds(t.Context(), domain.AgentAttemptRef{RunID: run.ID, AgentID: agent, AttemptID: result.AttemptID})
		if err != nil || len(rounds) != 1 {
			t.Fatalf("round %d: %+v %v", index, rounds, err)
		}
		if index == 0 && (rounds[0].Calls[0].ErrorCode != string(apperror.CodePolicyDenied) || rounds[0].Calls[1].Status != domain.SupervisorToolCompleted) {
			t.Fatalf("cross-child scope: %+v", rounds)
		}
		if index == 1 && (rounds[0].Calls[0].ErrorCode != string(apperror.CodeResourceExhausted) || rounds[0].Calls[0].ChargeID != "") {
			t.Fatalf("child budget reset: %+v", rounds)
		}
		if index == 2 && rounds[0].Calls[0].Status != domain.SupervisorToolCompleted {
			t.Fatalf("child borrowed budget: %+v", rounds)
		}
	}
	first, _ := st.GetAgentNode(t.Context(), child.ID)
	other, _ := st.GetAgentNode(t.Context(), second.ID)
	if first.ToolCallsUsed != 2 || other.ToolCallsUsed != 1 {
		t.Fatalf("usage mixed: first=%+v second=%+v", first, other)
	}
}

func TestSpecialistScopeRecheckedAfterFirstObservationOnRevokeAndStop(t *testing.T) {
	for _, action := range []string{"revoke", "stop"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "proof.txt"), []byte("first observation"), 0600); err != nil {
				t.Fatal(err)
			}
			p := &specialistTestProvider{}
			usage := llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
			st, run, child, _ := newScopedSubagentFixture(t, p, domain.Budget{MaxTurns: 10, MaxToolCalls: 10}, 2, 128, false, domain.SpecialistAdmission{AllowedReadPaths: []string{"proof.txt"}, ToolCallLimit: 4}, "", root)
			p.respond = func(index int, r llm.ChatRequest) (llm.ChatResponse, error) {
				switch index {
				case 0:
					return llm.ChatResponse{ToolCalls: []llm.ToolCall{scopedReadCall("before", "proof.txt")}, Usage: usage}, nil
				case 1:
					if !strings.Contains(scopedLatestResult(t, r), "first observation") {
						t.Fatal("read receipt absent")
					}
					var err error
					if action == "revoke" {
						_, err = st.RevokeSpecialistReadTools(t.Context(), run.ID, child.ID)
					} else {
						_, err = application.NewRunService(st).Pause(t.Context(), run.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					return llm.ChatResponse{ToolCalls: []llm.ToolCall{scopedReadCall("after", "proof.txt")}, Usage: usage}, nil
				case 2:
					last := r.Messages[len(r.Messages)-1]
					if !last.ToolResults[0].IsError {
						t.Fatal("revocation did not fence second observation")
					}
					return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "read access revoked"}), Usage: usage}, nil
				default:
					return llm.ChatResponse{}, fmt.Errorf("unexpected call %d", index)
				}
			}
			result, err := scopedToolService(st, p).ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "scope-revoke-stop-0001", WorkspaceTools: true})
			if action == "revoke" && err != nil {
				t.Fatal(err)
			}
			if action == "stop" && err == nil {
				t.Fatal("stopped Run continued")
			}
			rounds, err := st.ListSpecialistToolRounds(t.Context(), domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID})
			if err != nil || len(rounds) == 0 {
				t.Fatalf("first durable observation lost: %+v %v", rounds, err)
			}
			completed := 0
			for _, round := range rounds {
				for _, call := range round.Calls {
					if call.Status == domain.SupervisorToolCompleted {
						completed++
					}
				}
			}
			if completed != 1 {
				t.Fatalf("observations after %s: %+v", action, rounds)
			}
		})
	}
}

func TestSpecialistGatewayRejectsDirectoryLinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "allowed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "private.txt"), []byte("outside secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "allowed", "redirect")
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("create test junction: %s %v", out, err)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	usage := llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	p := &specialistTestProvider{responses: []llm.ChatResponse{{ToolCalls: []llm.ToolCall{scopedReadCall("link-read", "allowed/redirect/private.txt"), scopedReadCall("traversal-read", "../private.txt")}, Usage: usage}, {Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "redirect denied"}), Usage: usage}}}
	st, run, child, _ := newScopedSubagentFixture(t, p, domain.Budget{MaxTurns: 10, MaxToolCalls: 10}, 2, 128, false, domain.SpecialistAdmission{AllowedReadPaths: []string{"allowed"}, ToolCallLimit: 2}, "", root)
	result, err := scopedToolService(st, p).ExecuteSpecialist(t.Context(), application.ExecuteSpecialistRequest{RunID: run.ID, AgentID: child.ID, OperationKey: "scope-junction-escape-0001", WorkspaceTools: true})
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := st.ListSpecialistToolRounds(t.Context(), domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID})
	if err != nil || len(rounds) != 1 || len(rounds[0].Calls) != 2 || rounds[0].Calls[0].Status != domain.SupervisorToolFailed || rounds[0].Calls[1].Status != domain.SupervisorToolFailed || strings.Contains(rounds[0].Calls[0].ResultJSON, "outside secret") {
		t.Fatalf("link escaped: %+v %v", rounds, err)
	}
}
