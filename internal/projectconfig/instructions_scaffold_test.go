package projectconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// scaffoldRefusalMessage mirrors the stable refusal wording the scaffold must
// keep for a target directory that already contains the named file.
func scaffoldRefusalMessage(fileName, targetRelative string) string {
	return fmt.Sprintf("%s already exists in %s; scaffolding never overwrites project files (--print renders a preview)", fileName, targetRelative)
}

func TestScaffoldProjectInstructionsNeverOverwritesExistingFiles(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		content  string
	}{
		{"agents exists", InstructionScaffoldFileName, "existing guidance\n"},
		{"empty agents rejected by discovery", InstructionScaffoldFileName, ""},
		{"override shadows draft", InstructionScaffoldOverrideFileName, "operator override\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeInstruction(t, root, testCase.existing, testCase.content)
			result, err := ScaffoldProjectInstructions(t.Context(), root, ".", true)
			if err == nil {
				t.Fatalf("expected refusal, got %+v", result)
			}
			want := scaffoldRefusalMessage(testCase.existing, ".")
			if err.Error() != want {
				t.Fatalf("unexpected refusal:\n got: %v\nwant: %s", err, want)
			}
			unchanged, readErr := os.ReadFile(filepath.Join(root, testCase.existing))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(unchanged) != testCase.content {
				t.Fatalf("existing file was modified: got %q want %q", string(unchanged), testCase.content)
			}
			if testCase.existing != InstructionScaffoldFileName {
				if _, statErr := os.Lstat(filepath.Join(root, InstructionScaffoldFileName)); statErr == nil {
					t.Fatal("refused scaffold must not write AGENTS.md")
				}
			}
		})
	}
}

func TestScaffoldProjectInstructionsConcurrentCreateWritesOnce(t *testing.T) {
	root := t.TempDir()
	const writers = 8
	ctx := t.Context()
	type outcome struct {
		result InstructionScaffoldResult
		err    error
	}
	outcomes := make([]outcome, writers)
	gate := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(writers)
	done.Add(writers)
	for index := range writers {
		go func(index int) {
			defer done.Done()
			ready.Done()
			<-gate
			result, err := ScaffoldProjectInstructions(ctx, root, ".", true)
			outcomes[index] = outcome{result: result, err: err}
		}(index)
	}
	// Every writer parks on the gate before any of them starts, so all of
	// them contend for the same exclusive create. The outcome never depends
	// on scheduling: the filesystem grants the exclusive create to exactly
	// one writer and refuses the others.
	ready.Wait()
	close(gate)
	done.Wait()

	want := scaffoldRefusalMessage(InstructionScaffoldFileName, ".")
	successes := 0
	var written InstructionScaffoldResult
	for index, outcome := range outcomes {
		if outcome.err == nil {
			successes++
			written = outcome.result
			continue
		}
		if outcome.err.Error() != want {
			t.Fatalf("writer %d refused with an unexpected error:\n got: %v\nwant: %s", index, outcome.err, want)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one successful scaffold, got %d", successes)
	}
	if !written.Written || written.Content == "" {
		t.Fatalf("successful scaffold did not report a written draft: %+v", written)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, InstructionScaffoldFileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) == 0 {
		t.Fatal("written scaffold must not be empty")
	}
	if string(onDisk) != written.Content {
		t.Fatalf("written file differs from the successful scaffold content:\n got: %q\nwant: %q", string(onDisk), written.Content)
	}
	digest := sha256.Sum256(onDisk)
	if hex.EncodeToString(digest[:]) != written.ContentSHA256 {
		t.Fatalf("written file digest mismatch: got %s want %s", hex.EncodeToString(digest[:]), written.ContentSHA256)
	}
}
