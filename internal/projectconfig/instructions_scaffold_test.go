package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffoldProjectInstructionsWritesDeterministicDraft(t *testing.T) {
	root := t.TempDir()
	writeInstruction(t, root, "go.mod", "module example\n")
	writeInstruction(t, root, "package.json", "{\"name\":\"example\"}\n")
	writeInstruction(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	writeInstruction(t, root, "README.md", "# example\n")
	writeInstruction(t, root, "CLAUDE.md", "legacy guidance\n")

	result, err := ScaffoldProjectInstructions(t.Context(), root, ".", true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Written || result.FilePath != canonicalInstructionPath("AGENTS.md") {
		t.Fatalf("unexpected result: written=%t file=%q", result.Written, result.FilePath)
	}
	if result.ContentSHA256 == "" || result.ContentBytes != len(result.Content) {
		t.Fatalf("digest/bytes mismatch: %q %d", result.ContentSHA256, result.ContentBytes)
	}
	wantMarkers := []string{"go.mod", "package.json", "pnpm-lock.yaml", "README.md", "CLAUDE.md"}
	for _, marker := range wantMarkers {
		if !strings.Contains(result.Content, "`"+marker+"`") {
			t.Fatalf("draft does not mention %s:\n%s", marker, result.Content)
		}
	}
	written, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil || string(written) != result.Content {
		t.Fatalf("written file differs from reported content: err=%v", err)
	}
	if result.ContentBytes > MaxInstructionScaffoldBytes {
		t.Fatalf("draft exceeds scaffold byte bound: %d", result.ContentBytes)
	}

	again, err := ScaffoldProjectInstructions(t.Context(), root, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Content != result.Content || again.ContentSHA256 != result.ContentSHA256 {
		t.Fatal("scaffold render is not deterministic")
	}
	if again.Written {
		t.Fatal("preview scaffold must not report written")
	}
}

func TestScaffoldProjectInstructionsDraftIsDiscoverableUntrustedGuidance(t *testing.T) {
	root := t.TempDir()
	writeInstruction(t, root, "go.mod", "module example\n")
	if _, err := ScaffoldProjectInstructions(t.Context(), root, ".", true); err != nil {
		t.Fatal(err)
	}
	snapshot, err := DiscoverInstructions(t.Context(), root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sources) != 1 || snapshot.Sources[0].Path != canonicalInstructionPath("AGENTS.md") {
		t.Fatalf("scaffolded file not discovered: %+v", snapshot.Sources)
	}
	source := snapshot.Sources[0]
	if source.Trust != InstructionTrustClass {
		t.Fatalf("unexpected trust %q", source.Trust)
	}
	if source.Authority.ToolGrant || source.Authority.NetworkGrant || source.Authority.SecretAccess ||
		source.Authority.PolicyOverride || source.Authority.HookExecution {
		t.Fatalf("scaffolded guidance must stay workflow-only: %+v", source.Authority)
	}
	if !source.Authority.WorkflowGuidance {
		t.Fatalf("workflow guidance expected: %+v", source.Authority)
	}
	if len(source.Content) > MaxInstructionFileBytes {
		t.Fatalf("scaffolded file exceeds discovery limit: %d", len(source.Content))
	}
}

func TestScaffoldProjectInstructionsRefusesExistingInstructionFiles(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		content  string
	}{
		{"override shadows draft", "AGENTS.override.md", "operator override\n"},
		{"agents exists", "AGENTS.md", "existing guidance\n"},
		{"empty agents rejected by discovery", "AGENTS.md", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeInstruction(t, root, testCase.existing, testCase.content)
			result, err := ScaffoldProjectInstructions(t.Context(), root, ".", true)
			if err == nil {
				t.Fatalf("expected refusal, got %+v", result)
			}
			if _, statErr := os.Lstat(filepath.Join(root, "AGENTS.md")); testCase.existing != "AGENTS.md" && statErr == nil {
				t.Fatal("refused scaffold must not write AGENTS.md")
			}
			preview, previewErr := ScaffoldProjectInstructions(t.Context(), root, ".", false)
			if previewErr != nil {
				t.Fatalf("preview must stay available: %v", previewErr)
			}
			if preview.Written {
				t.Fatal("preview must not write")
			}
		})
	}
}

func TestScaffoldProjectInstructionsPrintDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	result, err := ScaffoldProjectInstructions(t.Context(), root, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Written || result.FilePath != canonicalInstructionPath("AGENTS.md") || result.Content == "" {
		t.Fatalf("unexpected preview result: %+v", result)
	}
	if _, err := os.Lstat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("preview scaffold must not create the file")
	}
}

func TestScaffoldProjectInstructionsTargetValidation(t *testing.T) {
	root := t.TempDir()
	if _, err := ScaffoldProjectInstructions(t.Context(), root, "../outside", true); err == nil {
		t.Fatal("expected boundary refusal")
	}
	// A single missing final segment would otherwise fall back to the parent
	// inside instructionBoundary and write AGENTS.md into the parent while
	// reporting the missing path.
	for _, missing := range []string{"missing", "ghost.txt", filepath.Join("src", "typo.go"), filepath.Join("missing", "nested")} {
		if _, err := ScaffoldProjectInstructions(t.Context(), root, missing, true); err == nil {
			t.Fatalf("scaffold target directory must exist: %s", missing)
		}
		if _, err := os.Lstat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
			t.Fatalf("missing target %s must not write into the workspace root", missing)
		}
	}
	writeInstruction(t, root, "src/go.mod", "module example\n")
	result, err := ScaffoldProjectInstructions(t.Context(), root, "src", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilePath != canonicalInstructionPath("src/AGENTS.md") || result.TargetPath != canonicalInstructionPath("src") {
		t.Fatalf("unexpected subdirectory result: file=%q target=%q", result.FilePath, result.TargetPath)
	}
	if _, err := os.Lstat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("subdirectory scaffold must not touch the workspace root")
	}
}

func TestScaffoldProjectInstructionsEmptyDirectoryAndSymlinkMarker(t *testing.T) {
	root := t.TempDir()
	result, err := ScaffoldProjectInstructions(t.Context(), root, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Detected) != 0 || !strings.Contains(result.Content, "No common project markers") {
		t.Fatalf("empty workspace detection unexpected: %+v", result.Detected)
	}
	// One real Makefile must produce exactly one detection even though the
	// table lists case variants that resolve to the same file on
	// case-insensitive filesystems.
	writeInstruction(t, root, "Makefile", "all:\n\techo hi\n")
	withMakefile, err := ScaffoldProjectInstructions(t.Context(), root, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	makeDetections := 0
	for _, detection := range withMakefile.Detected {
		if strings.EqualFold(detection.Path, "Makefile") {
			makeDetections++
		}
	}
	if makeDetections != 1 {
		t.Fatalf("expected one Makefile detection, got %+v", withMakefile.Detected)
	}
	writeInstruction(t, root, "real-go.mod", "module example\n")
	if err := os.Symlink(filepath.Join(root, "real-go.mod"), filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	withSymlink, err := ScaffoldProjectInstructions(t.Context(), root, ".", false)
	if err != nil {
		t.Fatal(err)
	}
	symlinked := ""
	for _, detection := range withSymlink.Detected {
		if detection.Path == "go.mod" {
			symlinked = detection.Path
		}
	}
	if len(withSymlink.Detected) != 2 || symlinked == "" {
		t.Fatalf("symlinked marker should still be reported without following it: %+v", withSymlink.Detected)
	}
}
