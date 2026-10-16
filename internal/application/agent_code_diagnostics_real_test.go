package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/toolgateway"
)

func TestRealGoplsAppliedDiagnosticDelta(t *testing.T) {
	executable := strings.TrimSpace(os.Getenv("CYBERAGENT_GOPLS_SMOKE_BINARY"))
	if executable == "" {
		t.Skip("CYBERAGENT_GOPLS_SMOKE_BINARY is not set")
	}
	for _, gitWorkspace := range []bool{false, true} {
		t.Run(fmt.Sprintf("git=%t", gitWorkspace), func(t *testing.T) {
			f := newAgentCodeReplaceFixture(t, true)
			original := "package sample\nvar Existing int = \"old\"\nvar Updated int = 1\n"
			broken := strings.Replace(original, "Updated int = 1", "Updated int = \"new\"", 1)
			path := filepath.Join(f.root, "sample.go")
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.root, "go.mod"), []byte("module example.invalid/applydiagnostics\n\ngo 1.25\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if gitWorkspace {
				for _, args := range [][]string{
					{"init", "-q", f.root},
					{"-C", f.root, "add", "go.mod", "sample.go", "target.txt"},
					{"-C", f.root, "-c", "user.name=diagnostics-test", "-c", "user.email=diagnostics@example.invalid", "commit", "-q", "-m", "initial"},
				} {
					if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
						t.Fatalf("git: %s, %v", output, err)
					}
				}
			}
			binary, err := os.ReadFile(executable)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(binary)
			config := codeintel.Config{ProtocolVersion: codeintel.ConfigProtocolVersion,
				Servers: []codeintel.ServerDescriptor{{ProtocolVersion: codeintel.ProtocolVersion,
					ID: "gopls-apply", Name: "gopls apply diagnostics", WorkspaceID: f.scope.WorkspaceID,
					Languages:  []codeintel.Language{{ID: "go", Extensions: []string{".go"}}},
					Executable: executable, ExecutableSHA256: hex.EncodeToString(digest[:]), Arguments: []string{"serve"},
					RequestTimeoutMillis: 10000, ReviewedBy: "test", ReviewedAt: time.Now().UTC(),
				}}}
			raw, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(t.TempDir(), "code-intel.json")
			if err := os.WriteFile(configPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			manager, _, err := codeintel.NewManagerFromConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Close(context.Background()) })
			snapshot, err := manager.InitializeServer(t.Context(), f.scope.WorkspaceID, f.root, "gopls-apply")
			if err != nil {
				t.Fatal(err)
			}
			// Initialize package loading outside the bounded automatic feedback.
			warm, err := manager.Execute(t.Context(), codeintel.Request{Tool: codeintel.ToolDiagnostics,
				WorkspaceID: f.scope.WorkspaceID, WorkspaceRoot: f.root, ServerID: snapshot.ServerID,
				ServerGeneration: snapshot.Generation, CapabilityFingerprint: snapshot.CapabilityFingerprint,
				Path: "sample.go", ExpectedDocumentSHA256: fileedit.HashText(original), Limit: 5})
			if err != nil || len(warm.Items) != 1 {
				t.Fatalf("initial gopls diagnostics: %+v, %v", warm, err)
			}
			f.executor.WithCodeIntel(manager)
			previous := original
			for step, content := range []string{broken, original} {
				payload := f.payload(content)
				payload["path"], payload["expected_sha256"] = "sample.go", fileedit.HashText(previous)
				proposal, err := f.execute(t, toolgateway.WorkspaceChangeTool, mustAgentCodePayload(t, payload), fmt.Sprintf("gopls-proposal-%04d", step))
				if err != nil {
					t.Fatal(err)
				}
				var edit replaceProposalResult
				if err := json.Unmarshal([]byte(proposal.JSON), &edit); err != nil {
					t.Fatal(err)
				}
				applied, err := f.execute(t, toolgateway.WorkspaceApplyTool, mustAgentCodePayload(t, edit.ApplyArguments), fmt.Sprintf("gopls-apply-%04d", step))
				if err != nil {
					t.Fatal(err)
				}
				feedback := decodeAppliedDiagnosticDelta(t, applied.JSON)
				delta := feedback.Delta
				if delta.Status != "current" || delta.Baseline == nil || feedback.Result == nil ||
					delta.Baseline.DocumentSHA256 != fileedit.HashText(previous) || feedback.Result.Provenance.DocumentSHA256 != fileedit.HashText(content) ||
					len(delta.Persisting) != 1 || !strings.Contains(delta.Persisting[0].Detail, `"old"`) {
					t.Fatalf("gopls apply comparison: %s", applied.JSON)
				}
				if (step == 0 && (len(delta.Added) != 1 || len(delta.Resolved) != 0 || !strings.Contains(delta.Added[0].Detail, `"new"`))) ||
					(step == 1 && (len(delta.Added) != 0 || len(delta.Resolved) != 1 || !strings.Contains(delta.Resolved[0].Detail, `"new"`))) {
					t.Fatalf("gopls added/resolved diagnostics: %s", applied.JSON)
				}
				actual, err := os.ReadFile(path)
				if err != nil || string(actual) != content {
					t.Fatalf("applied file differs: %q, %v", actual, err)
				}
				if gitWorkspace && delta.Baseline.ServerGeneration == feedback.Result.Provenance.ServerGeneration {
					t.Fatalf("Git content change did not exercise the new server generation: %s", applied.JSON)
				}
				t.Logf("step=%d old_sha=%s new_sha=%s added=%d resolved=%d persisting=%d", step,
					delta.Baseline.DocumentSHA256, feedback.Result.Provenance.DocumentSHA256,
					len(delta.Added), len(delta.Resolved), len(delta.Persisting))
				previous = content
			}
		})
	}
}
