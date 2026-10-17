package application

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/projectconfig"
)

func TestSpecialistProjectRulesSelectionAndBinding(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"AGENTS.md": "REQUIRED_ORIGINAL", "CLAUDE.md": strings.Repeat("optional text ", 2000)} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := projectconfig.DiscoverInstructions(t.Context(), root, ".")
	if err != nil {
		t.Fatal(err)
	}
	classes := make([]projectconfig.InstructionSourceDelivery, len(snapshot.Sources))
	for i, source := range snapshot.Sources {
		requirement := projectconfig.InstructionOptional
		if strings.EqualFold(source.Path, "AGENTS.md") {
			requirement = projectconfig.InstructionMandatory
		}
		classes[i] = projectconfig.InstructionSourceDelivery{Path: source.Path, ContentSHA256: source.ContentSHA256, Requirement: requirement}
	}
	snapshot, err = projectconfig.ClassifyInstructionSnapshot(snapshot, classes)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshot)
	config := domain.RunConfig{ProjectInstructions: raw, ProjectInstructionsFingerprint: snapshot.Fingerprint}
	replayJSON, _ := json.Marshal(map[string]any{"version": 4, "provider": "kimi-fixture", "model": "kimi-k3", "transport": "openai_chat_completions", "binding": strings.Repeat("a", 64), "response_id": "project-rules-replay",
		"parts": []any{map[string]any{"kind": "kimi_metadata", "id": llm.StableStreamID("kimi-native-replay", "project-rules-replay", "metadata", "0"), "opaque": map[string]any{"wire_model": "kimi-k3", "upstream_model": "kimi-k3", "content_state": "absent", "reasoning_content": strings.Repeat("r", 512)}}}, "calls": []any{}})
	replay, err := llm.DecodeProviderReplay(replayJSON)
	if err != nil {
		t.Fatal(err)
	}
	request := llm.ChatRequest{Messages: []llm.Message{{Role: "system", Content: "system"}, {Role: "assistant", Replay: replay}, {Role: "user", Content: "brief"}}}

	layout := modelContextLayout{HistoryStart: 1, HistoryCount: 1}
	selection := contextmgr.Selection{TokenBudget: 16, EstimatedTokens: 16}
	got, gotLayout, audit, err := specialistProjectInstructionRequest(config, request, layout, selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 4 || gotLayout.HistoryStart != 2 || gotLayout.HistoryCount != 1 || len(audit.OmittedSources) != 1 || audit.OmittedSources[0].OmissionReason != "budget" {
		t.Fatalf("optional rule did not yield to required context: %+v %+v %+v", got, gotLayout, audit)
	}
	if err := requirePinnedProjectInstructionRequest(config, got); err != nil {
		t.Fatal(err)
	}

	// A selected optional rule may still overflow the actual model window (for
	// example after adding native tool schemas). Trim it and audit its omission.
	roomy := contextmgr.Selection{TokenBudget: 20000}
	withOptional, optionalLayout, optionalSelection, err := specialistProjectInstructionRequest(config, request, layout, roomy)
	if err != nil {
		t.Fatal(err)
	}
	withOptional, optionalLayout, err = trimSpecialistOptionalProjectInstructions(config, withOptional, llm.DefaultContextWindow(), optionalLayout, estimateModelRequestTokens(got))
	if err != nil {
		t.Fatal(err)
	}
	fitted, _, err := constrainRequestToModelWindow(withOptional, llm.DefaultContextWindow(), optionalLayout, estimateModelRequestTokens(got))
	if err != nil {
		t.Fatal(err)
	}
	if len(fitted.Messages) != 4 || fitted.Messages[2].Replay != replay || fitted.Messages[2].Role != "assistant" {
		t.Fatal("optional guidance trimming changed private assistant history")
	}
	actualAudit, err := specialistProjectInstructionAudit(config, fitted, supervisorModelContextAudit(optionalSelection))
	if err != nil || len(actualAudit.Included) != 1 || len(actualAudit.Omitted) != 1 || actualAudit.Omitted[0].OmissionReason != "budget" {
		t.Fatalf("trimmed optional rule claimed as delivered: %+v %v", actualAudit, err)
	}
	if err := requirePinnedProjectInstructionRequest(config, fitted); err != nil {
		t.Fatal(err)
	}
	tampered := config
	tampered.ProjectInstructionsFingerprint = strings.Repeat("0", 64)
	if _, _, _, err := specialistProjectInstructionRequest(tampered, request, layout, selection); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("fingerprint mismatch accepted: %v", err)
	}
	run := domain.Run{ID: "run", Config: config}
	latest := run
	latest.Config = tampered
	if err := requireCurrentPinnedProjectInstructions(t.Context(), specialistContractRunStore{latest}, run); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("changed dispatch binding accepted: %v", err)
	}
}

type specialistContractRunStore struct{ run domain.Run }

func (s specialistContractRunStore) GetRun(context.Context, string) (domain.Run, error) {
	return s.run, nil
}
