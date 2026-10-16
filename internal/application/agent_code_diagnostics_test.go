package application_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
)

type diagnosticsDeniedPolicy struct{ policy.Checker }

func (p diagnosticsDeniedPolicy) CheckToolCall(call tools.Call) policy.Decision {
	if call.Name == "code_diagnostics" {
		return policy.Decision{Allowed: false, Reason: "disabled for this run"}
	}
	return p.Checker.CheckToolCall(call)
}

func TestAgentCodeApplyDiagnosticDelta(t *testing.T) {
	for _, tc := range []struct {
		name, mode, status, reason string
		git                        bool
	}{
		{name: "added resolved and persisting", mode: "delta", status: "current"},
		{name: "unversioned baseline", mode: "push-unversioned", status: "unavailable", reason: "baseline_document_version_not_reported"},
		{name: "old baseline", mode: "push-old", status: "unavailable", reason: "baseline_document_version_not_reported"},
		{name: "truncated baseline", mode: "delta-many", status: "unavailable", reason: "baseline_incomplete"},
		{name: "truncated current", mode: "delta-many-current", status: "unavailable", reason: "current_diagnostics_incomplete"},
		{name: "inline comparison limit", mode: "delta-large", status: "unavailable", reason: "inline_summary_limit"},
		{name: "Git dirty state refresh", mode: "delta", status: "current", git: true},
		{name: "changed server capabilities", mode: "delta-capability-change", status: "unavailable", reason: "server_changed", git: true},
		{name: "baseline timeout", mode: "baseline-timeout", status: "unavailable", reason: "baseline_diagnostics_timeout"},
		{name: "baseline crash", mode: "baseline-crash", status: "unavailable", reason: "baseline_diagnostics_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentCodeReplaceFixture(t, true)
			if tc.git {
				if output, err := exec.Command("git", "init", "-q", f.root).CombinedOutput(); err != nil {
					t.Fatalf("git init: %s, %v", output, err)
				}
			}
			manager := newApplyDiagnosticsManager(t, f.scope.WorkspaceID, tc.mode)
			if _, err := manager.InitializeServer(t.Context(), f.scope.WorkspaceID, f.root, "apply-lsp"); err != nil {
				t.Fatal(err)
			}
			f.executor.WithCodeIntel(manager)
			proposal := f.propose(t, "broken\n", "delta-proposal-0001")
			applied, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "delta-apply-0001")
			if err != nil {
				t.Fatal(err)
			}
			feedback := decodeAppliedDiagnosticDelta(t, applied.JSON)
			if feedback.Delta.Status != tc.status || feedback.Delta.Reason != tc.reason {
				t.Fatalf("incorrect diagnostic delta: %s", applied.JSON)
			}
			f.assertFile(t, "broken\n")
			if tc.status != "current" {
				if feedback.Delta.Added != nil || feedback.Delta.Resolved != nil || feedback.Delta.Persisting != nil {
					t.Fatalf("incomparable diagnostics produced a delta: %s", applied.JSON)
				}
				return
			}
			assertDiagnosticMessages(t, feedback.Delta.Added, "new type error")
			assertDiagnosticMessages(t, feedback.Delta.Resolved, "old type error")
			assertDiagnosticMessages(t, feedback.Delta.Persisting, "existing type error")
			if feedback.Delta.Baseline == nil || feedback.Result == nil ||
				feedback.Delta.Baseline.DocumentSHA256 != fileedit.HashText("before\n") {
				t.Fatalf("delta lost its two version bindings: %s", applied.JSON)
			}
			if sameGeneration := feedback.Delta.Baseline.ServerGeneration == feedback.Result.Provenance.ServerGeneration; sameGeneration == tc.git || (!tc.git && feedback.Delta.Baseline.DocumentVersion >= feedback.Result.Provenance.DocumentVersion) {
				t.Fatalf("incorrect version or server generation after write: %s", applied.JSON)
			}
			replayed, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "delta-apply-0001")
			if err != nil {
				t.Fatal(err)
			}
			if delta := decodeAppliedDiagnosticDelta(t, replayed.JSON).Delta; delta.Status != "unavailable" || delta.Reason != "apply_replayed" || delta.Added != nil {
				t.Fatalf("replay invented a new comparison: %s", replayed.JSON)
			}
			payload := f.payload("fixed\n")
			payload["expected_sha256"] = fileedit.HashText("broken\n")
			proposed, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, payload), "delta-repair-proposal-0001")
			if err != nil {
				t.Fatal(err)
			}
			var repair replaceProposalResult
			if err := json.Unmarshal([]byte(proposed.JSON), &repair); err != nil {
				t.Fatal(err)
			}
			fixed, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, repair.ApplyArguments), "delta-repair-apply-0001")
			if err != nil {
				t.Fatal(err)
			}
			delta := decodeAppliedDiagnosticDelta(t, fixed.JSON).Delta
			if delta.Status != "current" || len(delta.Added) != 0 {
				t.Fatalf("repair did not compare its immediate original version: %s", fixed.JSON)
			}
			assertDiagnosticMessages(t, delta.Resolved, "new type error")
			assertDiagnosticMessages(t, delta.Persisting, "existing type error")
		})
	}
}

type appliedDiagnosticDeltaResult struct {
	Result *codeintel.Result `json:"result"`
	Delta  struct {
		Status, Reason              string
		Baseline                    *codeintel.Provenance `json:"baseline_provenance"`
		Added, Resolved, Persisting []codeintel.EvidenceItem
	} `json:"delta"`
}

func decodeAppliedDiagnosticDelta(t *testing.T, raw string) appliedDiagnosticDeltaResult {
	t.Helper()
	var result struct {
		Diagnostics appliedDiagnosticDeltaResult `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result.Diagnostics
}

func assertDiagnosticMessages(t *testing.T, items []codeintel.EvidenceItem, messages ...string) {
	t.Helper()
	if len(items) != len(messages) {
		t.Fatalf("diagnostic messages: got %+v, want %v", items, messages)
	}
	for index, message := range messages {
		if items[index].Detail != message {
			t.Fatalf("diagnostic messages: got %+v, want %v", items, messages)
		}
	}
}

func TestAgentCodeApplyDiagnosticFeedbackKeepsSuccessfulWrite(t *testing.T) {
	for _, tc := range []struct{ mode, want, reason string }{
		{"normal", "current", ""},
		{"push-current", "partial", ""},
		{"push-unversioned", "pending", "document_version_not_reported"},
		{"push-old", "pending", "document_version_not_reported"},
		{"pending", "pending", "document_version_not_reported"},
		{"empty-response", "pending", "document_version_not_reported"},
		{"timeout", "timeout", "diagnostics_timeout"},
		{"crash", "unavailable", "diagnostics_unavailable"},
		{"disabled", "unavailable", "diagnostics_policy_denied"},
		{"uninitialized", "unavailable", "no_ready_diagnostics_server"},
		{"absent", "unavailable", "no_semantic_backend"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newAgentCodeReplaceFixture(t, true)
			if tc.mode != "absent" {
				manager := newApplyDiagnosticsManager(t, f.scope.WorkspaceID, tc.mode)
				if tc.mode != "uninitialized" {
					if _, err := manager.InitializeServer(t.Context(), f.scope.WorkspaceID, f.root, "apply-lsp"); err != nil {
						t.Fatal(err)
					}
				}
				if tc.mode == "disabled" {
					f.executor = application.NewAgentCodeToolExecutor(f.state, diagnosticsDeniedPolicy{policy.NewDefaultChecker()}).WithExecutionPermissionCapabilities(f.capabilities)
				}
				f.executor.WithCodeIntel(manager)
			}
			proposal := f.propose(t, "broken\n", "feedback-proposal-0001")
			applied, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, proposal.ApplyArguments), "feedback-apply-0001")
			if err != nil {
				t.Fatalf("diagnostics changed apply success: %v", err)
			}
			var result struct {
				Status      string `json:"status"`
				FileWritten bool   `json:"file_written"`
				Diagnostics struct {
					Status, Reason, Path, Coverage string
					SHA256                         string `json:"document_sha256"`
					Result                         *codeintel.Result
				} `json:"diagnostics"`
			}
			if err := json.Unmarshal([]byte(applied.JSON), &result); err != nil {
				t.Fatal(err)
			}
			feedback := result.Diagnostics
			if result.Status != "applied" || !result.FileWritten || feedback.Status != tc.want || feedback.Reason != tc.reason || feedback.Path != "target.txt" || feedback.SHA256 != fileedit.HashText("broken\n") || feedback.Coverage != "single_document_single_server" {
				t.Fatalf("incorrect write or diagnostic feedback: %s", applied.JSON)
			}
			f.assertFile(t, "broken\n")
			if tc.want == "current" || tc.want == "partial" {
				if feedback.Result == nil || feedback.Result.Provenance.DocumentSHA256 != feedback.SHA256 || feedback.Result.Provenance.DocumentVersion < 1 || len(feedback.Result.Items) != 1 || feedback.Result.Items[0].Detail != "new type error" {
					t.Fatalf("diagnostics not bound to new bytes: %s", applied.JSON)
				}
			} else if feedback.Result != nil {
				t.Fatalf("unconfirmed feedback carried diagnostics: %s", applied.JSON)
			}
			if tc.mode == "normal" {
				payload := f.payload("fixed\n")
				payload["expected_sha256"] = fileedit.HashText("broken\n")
				proposed, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, payload), "feedback-repair-proposal-0001")
				if err != nil {
					t.Fatal(err)
				}
				var repair replaceProposalResult
				if err := json.Unmarshal([]byte(proposed.JSON), &repair); err != nil {
					t.Fatal(err)
				}
				fixed, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, repair.ApplyArguments), "feedback-repair-apply-0001")
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(fixed.JSON), &result); err != nil {
					t.Fatal(err)
				}
				if result.Diagnostics.Status != "current" || result.Diagnostics.Result == nil || len(result.Diagnostics.Result.Items) != 0 || result.Diagnostics.SHA256 != fileedit.HashText("fixed\n") {
					t.Fatalf("old diagnostic survived repair: %s", fixed.JSON)
				}
			}
		})
	}
}

func newApplyDiagnosticsManager(t *testing.T, workspaceID, mode string) *codeintel.Manager {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	manager, err := codeintel.NewManager([]codeintel.ServerDescriptor{{ProtocolVersion: codeintel.ProtocolVersion,
		ID: "apply-lsp", Name: "Apply diagnostics test", WorkspaceID: workspaceID,
		Languages:  []codeintel.Language{{ID: "plaintext", Extensions: []string{".txt"}}},
		Executable: executable, ExecutableSHA256: hex.EncodeToString(hash.Sum(nil)),
		Arguments:            []string{"-test.run=^TestApplyDiagnosticsLSPHelper$", "-test.outputdir=apply-lsp-" + mode},
		RequestTimeoutMillis: 3000, ReviewedBy: "test", ReviewedAt: time.Now().UTC(),
		Source: codeintel.Source{Kind: "operator_config", Label: "test-config.json", SHA256: strings.Repeat("d", 64)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager
}

// The child speaks real framed JSON-RPC over the normal owned LSP transport.
func TestApplyDiagnosticsLSPHelper(t *testing.T) {
	mode := ""
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-test.outputdir=apply-lsp-") {
			mode = strings.TrimPrefix(arg, "-test.outputdir=apply-lsp-")
		}
	}
	if mode == "" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	document := struct {
		URI     string `json:"uri"`
		Version int    `json:"version"`
		Text    string `json:"text"`
	}{}
	send := func(value any) {
		raw, _ := json.Marshal(value)
		fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(raw), raw)
	}
	items := func() []any {
		if strings.HasPrefix(mode, "delta") {
			messages := []string{"existing type error"}
			if mode == "delta-large" {
				messages[0] = strings.Repeat("existing type error ", 250)
			}
			if document.Text == "before\n" {
				messages = append(messages, "old type error")
			}
			if document.Text == "broken\n" {
				messages = append(messages, "new type error")
			}
			if (mode == "delta-many" && document.Text == "before\n") || (mode == "delta-many-current" && document.Text == "broken\n") {
				messages = append(messages, "error three", "error four", "error five", "error six")
			}
			values := make([]any, 0, len(messages))
			for _, message := range messages {
				values = append(values, map[string]any{"range": map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 6}}, "severity": 1, "message": message})
			}
			return values
		}
		if document.Text != "broken\n" {
			return []any{}
		}
		return []any{map[string]any{"range": map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 6}}, "severity": 1, "message": "new type error"}}
	}
	publish := func() {
		if strings.HasPrefix(mode, "push-") {
			params := map[string]any{"uri": document.URI, "diagnostics": items()}
			if mode == "push-current" {
				params["version"] = document.Version
			}
			if mode == "push-old" {
				params["version"] = document.Version - 1
			}
			send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": params})
		}
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(0)
		}
		var length int
		if _, err := fmt.Sscanf(line, "Content-Length: %d", &length); err != nil || length > codeintel.MaxMessageBytes {
			os.Exit(2)
		}
		if _, err := reader.ReadString('\n'); err != nil {
			os.Exit(2)
		}
		raw := make([]byte, length)
		if _, err := io.ReadFull(reader, raw); err != nil {
			os.Exit(2)
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &request) != nil {
			os.Exit(2)
		}
		if request.Method == "exit" {
			os.Exit(0)
		}
		if request.Method == "textDocument/didOpen" {
			var open struct {
				Document json.RawMessage `json:"textDocument"`
			}
			json.Unmarshal(request.Params, &open)
			json.Unmarshal(open.Document, &document)
			publish()
		}
		if request.Method == "textDocument/didChange" {
			var change struct {
				Document struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
				} `json:"textDocument"`
				Changes []struct {
					Text string `json:"text"`
				} `json:"contentChanges"`
			}
			json.Unmarshal(request.Params, &change)
			if len(change.Changes) > 0 {
				document.URI, document.Version, document.Text = change.Document.URI, change.Document.Version, change.Changes[0].Text
			}
			publish()
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		if request.Method == "initialize" {
			version := "1"
			if mode == "delta-capability-change" {
				if current, err := os.ReadFile("target.txt"); err == nil && string(current) == "broken\n" {
					version = "2"
				}
			}
			result = map[string]any{"capabilities": map[string]any{"diagnosticProvider": true, "textDocumentSync": 1}, "serverInfo": map[string]string{"name": "apply-lsp", "version": version}}
		}
		if request.Method == "textDocument/diagnostic" {
			if (mode == "timeout" && document.Text == "broken\n") || (mode == "baseline-timeout" && document.Text == "before\n") {
				time.Sleep(5 * time.Second)
			}
			if (mode == "crash" && document.Text == "broken\n") || (mode == "baseline-crash" && document.Text == "before\n") {
				os.Exit(3)
			}
			if strings.HasPrefix(mode, "push-") || mode == "pending" {
				send(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "push only"}})
				continue
			}
			result = map[string]any{"kind": "full", "items": items()}
			if mode == "empty-response" {
				result = nil
			}
		}
		send(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
}
