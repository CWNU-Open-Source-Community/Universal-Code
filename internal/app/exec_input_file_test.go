package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
)

func TestExecAttachRejectsAmbiguousPathsAndNonRegularFiles(t *testing.T) {
	a, _, st := newExecContinueTestApp(t)
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("private note"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{" " + path, path + " ", "\t" + path, t.TempDir()} {
		_, err := a.importExecAttachments(t.Context(), "exec-ws", []string{requested})
		if err == nil {
			t.Fatalf("ambiguous or nonregular input %q was accepted", requested)
		}
	}
	runs, err := st.ListRuns(t.Context(), domain.RunFilter{})
	if err != nil || len(runs) != 0 {
		t.Fatalf("bad input created %d runs: %v", len(runs), err)
	}
}

func TestReadExecInputFileRejectsOversizedFileBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := readExecInputFile(path, 5*1024*1024)
	if err == nil || !strings.Contains(err.Error(), "5 MiB") || data != nil {
		t.Fatalf("oversized file must fail before producing bytes: len=%d, error=%v", len(data), err)
	}
}
