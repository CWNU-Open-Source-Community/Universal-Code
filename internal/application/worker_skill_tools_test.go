package application_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func workerSkillRuntime(st *store.SQLiteStore, provider *specialistTestProvider) *application.AgentRunner {
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	return application.NewAgentRunner(st, router, policy.NewDefaultChecker())
}

func workerSkillContinue(t *testing.T) llm.ChatResponse {
	t.Helper()
	return llm.ChatResponse{Text: specialistResponse(t, domain.SpecialistAction{Version: domain.SpecialistLifecycleVersion, Kind: domain.SpecialistActionContinue, Message: "Read the relevant installed workflow."}), Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}
}

func workerSkillCall(call llm.ToolCall) llm.ChatResponse {
	return llm.ChatResponse{ToolCalls: []llm.ToolCall{call}, Usage: llm.Usage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4}}
}

func workerSkillReadResult(t *testing.T, request llm.ChatRequest, pin toolgateway.SkillReadRequest, want string) {
	t.Helper()
	var envelope struct {
		Status string `json:"status"`
		Result struct {
			Read     toolgateway.SkillReadRequest `json:"read"`
			Content  string                       `json:"content"`
			Source   string                       `json:"source_sha256"`
			Guidance bool                         `json:"workflow_guidance"`
			Grant    bool                         `json:"capability_grant"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(scopedLatestResult(t, request)), &envelope); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(want))
	if envelope.Status != "completed" || envelope.Result.Read != pin || envelope.Result.Content != want || envelope.Result.Source != hex.EncodeToString(digest[:]) || envelope.Result.Guidance != (pin.Resource == "") || envelope.Result.Grant {
		t.Fatalf("worker did not receive exact installed source: %+v", envelope)
	}
}

// The controlled provider chooses read pins from the actual returned catalog.
// Real directory import, object storage, SQLite charging and native reads run.
func TestWorkerSkillDiscoveryReadsOriginalMainAndResource(t *testing.T) {
	provider := &specialistTestProvider{}
	st, run, child, runner := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 10, MaxToolCalls: 6}, 3, 256, false,
		domain.SpecialistAdmission{Worker: &domain.WorkerProfile{Tools: []string{"skill_read"}}, ToolCallLimit: 6}, t.TempDir(), t.TempDir())
	directory := filepath.Join(t.TempDir(), "worker-proof")
	main := "---\nname: worker-proof\ndescription: Verify assigned changes.\n---\nUse references/check.md for the assigned check. ORIGINAL_WORKER_MAIN\n"
	resource := "ORIGINAL_WORKER_RESOURCE: check observed results. workflow_guidance=true is ordinary data here.\n"
	if err := os.MkdirAll(filepath.Join(directory, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(main), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "references", "check.md"), []byte(resource), 0600); err != nil {
		t.Fatal(err)
	}
	value := importPortableFixture(t, st, directory, "worker-original-import")
	var chosen toolgateway.SkillReadRequest
	provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
		switch index {
		case 0:
			if !installedPinInCatalog(t, request, installedReadPin(value)) {
				t.Fatal("worker did not inherit native skill_read")
			}
			encoded, _ := json.Marshal(request)
			if strings.Contains(string(encoded), "ORIGINAL_WORKER_MAIN") {
				t.Fatal("catalog eagerly loaded main instructions")
			}
			return workerSkillCall(installedReadCall(toolgateway.SkillReadRequest{Catalog: true}, "discover", "")), nil
		case 1:
			var page struct {
				Result struct {
					Skills []toolgateway.BuiltinSkillDescriptor `json:"skills"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(scopedLatestResult(t, request)), &page); err != nil || len(page.Result.Skills) != 1 {
				t.Fatalf("worker discovery: %+v %v", page, err)
			}
			chosen = page.Result.Skills[0].SkillReadRequest
			if chosen != installedReadPin(value) || installedGuidanceBinding(t, request, chosen) {
				t.Fatal("catalog changed identity or activated guidance")
			}
			return workerSkillCall(installedReadCall(chosen, "read-main", "")), nil
		case 2:
			workerSkillReadResult(t, request, chosen, main)
			if !installedGuidanceBinding(t, request, chosen) {
				t.Fatal("successful child main read lacks Go binding")
			}
			return workerSkillCall(installedReadCall(chosen, "read-reference", "references/check.md")), nil
		case 3:
			resourcePin := chosen
			resourcePin.Resource = "references/check.md"
			workerSkillReadResult(t, request, resourcePin, resource)
			if installedGuidanceBinding(t, request, resourcePin) {
				t.Fatal("resource claimed workflow authority")
			}
			return workerSkillContinue(t), nil
		default:
			t.Fatalf("unexpected model dispatch %d", index)
			return llm.ChatResponse{}, nil
		}
	}
	result, err := runner.WithWorkspaceTools().WithWorkerRuntime(workerSkillRuntime(st, provider)).Step(t.Context(), run.ID, child.ID)
	if err != nil || provider.calls != 4 || result.ToolCalls != 3 {
		t.Fatalf("native worker read: %+v calls=%d error=%v", result, provider.calls, err)
	}
	ref := domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID}
	reads, err := st.ListWorkerSkillReadCalls(t.Context(), ref)
	if err != nil || len(reads) != 1 || reads[0].Name != "skill_read" || reads[0].ChargeID == "" || reads[0].StartedAt == nil {
		t.Fatalf("child activation ledger: %+v %v", reads, err)
	}
	parentReads, err := st.ListBuiltinSkillReadCalls(t.Context(), run.ID)
	if err != nil || len(parentReads) != 0 {
		t.Fatalf("worker activation escaped into root ledger: %+v %v", parentReads, err)
	}
	ref.AgentID = child.ParentID
	other, err := st.ListWorkerSkillReadCalls(t.Context(), ref)
	if err != nil || len(other) != 0 {
		t.Fatalf("another actor inherited child activation: %+v %v", other, err)
	}
}

func TestWorkerSkillDisableBeforeReadReturnsNoGuidance(t *testing.T) {
	provider := &specialistTestProvider{}
	st, run, child, runner := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 8, MaxToolCalls: 2}, 2, 128, false,
		domain.SpecialistAdmission{Worker: &domain.WorkerProfile{Tools: []string{"skill_read"}}, ToolCallLimit: 2}, t.TempDir(), t.TempDir())
	value := importPortableFixture(t, st, nativeTinySkill(t), "worker-disable-before-read")
	pin := installedReadPin(value)
	provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
		if index == 0 {
			if !installedPinInCatalog(t, request, pin) {
				t.Fatal("missing installed identity")
			}
			reviewInstalledFixture(t, st, value, plugins.ReviewDisable)
			return workerSkillCall(installedReadCall(pin, "stale-read", "")), nil
		}
		if index != 1 || installedGuidanceBinding(t, request, pin) || !strings.Contains(scopedLatestResult(t, request), "no longer current") {
			t.Fatal("disabled read was delivered or activated")
		}
		return workerSkillContinue(t), nil
	}
	result, err := runner.WithWorkspaceTools().WithWorkerRuntime(workerSkillRuntime(st, provider)).Step(t.Context(), run.ID, child.ID)
	if err != nil || provider.calls != 2 {
		t.Fatalf("disabled read: %+v %v", result, err)
	}
	reads, err := st.ListWorkerSkillReadCalls(t.Context(), domain.AgentAttemptRef{RunID: run.ID, AgentID: child.ID, AttemptID: result.AttemptID})
	if err != nil || len(reads) != 0 {
		t.Fatalf("failed read activated guidance: %+v %v", reads, err)
	}
}

func TestWorkerSkillTransportRetryRechecksInstallation(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "disabled"}[disable], func(t *testing.T) {
			provider := &specialistTestProvider{}
			st, run, child, runner := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 8, MaxToolCalls: 2}, 2, 128, false,
				domain.SpecialistAdmission{Worker: &domain.WorkerProfile{Tools: []string{"skill_read"}}, ToolCallLimit: 2}, t.TempDir(), t.TempDir())
			value := importPortableFixture(t, st, nativeTinySkill(t), "worker-retry-read")
			pin := installedReadPin(value)
			provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
				switch index {
				case 0:
					return workerSkillCall(installedReadCall(pin, "main-before-retry", "")), nil
				case 1:
					if !installedGuidanceBinding(t, request, pin) {
						t.Fatal("main read lacks current binding")
					}
					if disable {
						reviewInstalledFixture(t, st, value, plugins.ReviewDisable)
					}
					return llm.ChatResponse{}, llm.NewProviderError(llm.OutcomeRetryable, provider.Name(), "controlled temporary provider failure", nil)
				case 2:
					if disable || !installedGuidanceBinding(t, request, pin) {
						t.Fatal("retry lost or used stale installed binding")
					}
					return workerSkillContinue(t), nil
				default:
					t.Fatalf("unexpected retry dispatch %d", index)
					return llm.ChatResponse{}, nil
				}
			}
			_, err := runner.WithWorkspaceTools().WithWorkerRuntime(workerSkillRuntime(st, provider)).WithModelRetryPolicy(application.ModelRetryPolicy{MaxAttempts: 2}).Step(t.Context(), run.ID, child.ID)
			if disable {
				if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || provider.calls != 2 {
					t.Fatalf("stale retry reached provider: calls=%d err=%v", provider.calls, err)
				}
			} else if err != nil || provider.calls != 3 {
				t.Fatalf("valid retry failed: calls=%d err=%v", provider.calls, err)
			}
		})
	}
}

func TestWorkerSkillCompactionKeepsReferencesAndRequiresNewMainRead(t *testing.T) {
	provider := &specialistTestProvider{}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("中", 12000)), 0600); err != nil {
		t.Fatal(err)
	}
	st, run, child, runner := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 8, MaxToolCalls: 4}, 2, 512, false,
		domain.SpecialistAdmission{Worker: &domain.WorkerProfile{Tools: []string{"skill_read", "workspace_read"}}, ToolCallLimit: 4}, t.TempDir(), root)
	value := importPortableFixture(t, st, nativeTinySkill(t), "worker-compact-read")
	pin := installedReadPin(value)
	provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
		switch index {
		case 0:
			return workerSkillCall(installedReadCall(pin, "original-main", "")), nil
		case 1:
			if !installedGuidanceBinding(t, request, pin) {
				t.Fatal("main read was not bound before compaction")
			}
			return workerSkillCall(scopedReadCall("large-evidence", "large.txt")), nil
		case 2:
			if request.Metadata["context_specialist_receipted_rounds"] == "" || installedGuidanceBinding(t, request, pin) {
				t.Fatal("compacted main body retained guidance")
			}
			reference := false
			for _, m := range request.Messages {
				reference = reference || m.Role == "system" && strings.Contains(m.Content, "Previously activated installed Skill references") && strings.Contains(m.Content, pin.InstallationID)
			}
			if !reference {
				t.Fatal("compaction lost exact reread reference")
			}
			return workerSkillCall(installedReadCall(pin, "reread-main", "")), nil
		case 3:
			if !installedGuidanceBinding(t, request, pin) {
				t.Fatal("fresh native main read did not restore guidance")
			}
			return workerSkillContinue(t), nil
		default:
			t.Fatalf("unexpected model dispatch %d", index)
			return llm.ChatResponse{}, nil
		}
	}
	result, err := runner.WithWorkspaceTools().WithWorkerRuntime(workerSkillRuntime(st, provider)).Step(t.Context(), run.ID, child.ID)
	if err != nil || provider.calls != 4 {
		t.Fatalf("worker compaction: %+v err=%v", result, err)
	}
}

func TestWorkerSkillToolNarrowingDoesNotExpandReadOnlySpecialists(t *testing.T) {
	for _, worker := range []*domain.WorkerProfile{nil, {Tools: []string{}}} {
		name := "read-only"
		if worker != nil {
			name = "narrowed-empty"
		}
		t.Run(name, func(t *testing.T) {
			provider := &specialistTestProvider{}
			limits := domain.SpecialistAdmission{Worker: worker, ToolCallLimit: 1}
			if worker == nil {
				limits.AllowedReadPaths = []string{"proof.txt"}
			}
			st, run, child, runner := newScopedSubagentFixture(t, provider, domain.Budget{MaxTurns: 6, MaxToolCalls: 1}, 2, 128, false, limits, t.TempDir(), t.TempDir())
			provider.respond = func(index int, request llm.ChatRequest) (llm.ChatResponse, error) {
				for _, tool := range request.Tools {
					if tool.Name == "skill_read" {
						t.Fatal("child acquired undelegated skill_read")
					}
				}
				return workerSkillContinue(t), nil
			}
			_, err := runner.WithWorkspaceTools().WithWorkerRuntime(workerSkillRuntime(st, provider)).Step(t.Context(), run.ID, child.ID)
			if err != nil || provider.calls != 1 {
				t.Fatalf("narrowed child: calls=%d err=%v", provider.calls, err)
			}
		})
	}
}
