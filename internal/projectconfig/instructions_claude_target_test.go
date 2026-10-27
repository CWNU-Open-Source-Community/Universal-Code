package projectconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverInstructionsClaudeDirectoryTargetsKeepContainingScope(t *testing.T) {
	for _, scope := range []string{"", "src", ".claude/rules"} {
		for _, suffix := range []string{"CLAUDE.md", ".", "rules", "rules/nested"} {
			target := filepath.Join(scope, ".claude", suffix)
			t.Run(filepath.ToSlash(target), func(t *testing.T) {
				root := t.TempDir()
				writeInstruction(t, root, "CLAUDE.md", "root guidance\n")
				writeInstruction(t, root, ".claude/CLAUDE.md", "root hidden guidance\n")
				hidden := filepath.Join(scope, ".claude", "CLAUDE.md")
				writeInstruction(t, root, hidden, "containing scope guidance\n")
				if suffix != "CLAUDE.md" {
					if err := os.MkdirAll(filepath.Join(root, target), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				snapshot, err := DiscoverInstructions(t.Context(), root, target)
				if err != nil {
					t.Fatal(err)
				}
				wantPaths := []string{"CLAUDE.md", ".claude/CLAUDE.md"}
				if scope != "" {
					wantPaths = append(wantPaths, hidden)
				}
				if len(snapshot.Sources) != len(wantPaths) {
					t.Fatalf("sources=%+v want paths=%v", snapshot.Sources, wantPaths)
				}
				for index, source := range snapshot.Sources {
					wantScope, wantDepth := "", 0
					if index == 2 {
						wantScope = canonicalInstructionPath(scope)
						wantDepth = len(strings.Split(filepath.ToSlash(scope), "/"))
					}
					if source.Path != canonicalInstructionPath(wantPaths[index]) ||
						source.Scope != wantScope || source.Depth != wantDepth ||
						source.Ordinal != index+1 || source.Precedence != 100+index {
						t.Fatalf("source lost its containing scope or order: %+v", source)
					}
				}
				again, err := DiscoverInstructions(t.Context(), root, target)
				if err != nil || again.Fingerprint != snapshot.Fingerprint {
					t.Fatalf("repeated discovery changed: %v", err)
				}
			})
		}
	}
}

func TestDiscoverInstructionsClaudeDirectoryTargetRecordsIgnoreOnce(t *testing.T) {
	root := t.TempDir()
	writeInstruction(t, root, "AGENTS.md", "retained guidance\n")
	writeInstruction(t, root, ".claude/CLAUDE.md", "ignored guidance\n")
	writeInstruction(t, root, ".prayu/instructions.ignore", ".claude/CLAUDE.md\n")
	snapshot, err := DiscoverInstructions(t.Context(), root, ".claude/CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sources) != 1 || snapshot.Sources[0].Path != canonicalInstructionPath("AGENTS.md") ||
		len(snapshot.Ignored) != 2 || snapshot.Ignored[1].Path != canonicalInstructionPath(".claude/CLAUDE.md") {
		t.Fatalf("ignored guidance was repeated or delivered: sources=%+v ignored=%+v", snapshot.Sources, snapshot.Ignored)
	}
}

func TestDiscoverInstructionsClaudeDirectoryTargetCountsUniqueFiles(t *testing.T) {
	root := t.TempDir()
	writeInstruction(t, root, ".claude/CLAUDE.md", "hidden guidance\n")
	for index := 0; index < MaxInstructionFiles-1; index++ {
		writeInstruction(t, root, fmt.Sprintf(".prayu/rules/%02d.md", index), "root rule\n")
	}
	snapshot, err := DiscoverInstructions(t.Context(), root, ".claude/CLAUDE.md")
	if err != nil || len(snapshot.Sources) != MaxInstructionFiles {
		t.Fatalf("unique files at the limit were rejected: sources=%d err=%v", len(snapshot.Sources), err)
	}
	writeInstruction(t, root, ".claude/AGENTS.md", "one extra source\n")
	if _, err := DiscoverInstructions(t.Context(), root, ".claude/CLAUDE.md"); err == nil || !strings.Contains(err.Error(), "instruction count exceeds") {
		t.Fatalf("a distinct source bypassed the file limit: %v", err)
	}
}
