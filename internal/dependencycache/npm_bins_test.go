package dependencycache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalizeNPMArrayBins(t *testing.T) {
	for _, guest := range []bool{false, true} {
		t.Run(map[bool]string{false: "windows", true: "guest"}[guest], func(t *testing.T) {
			root := t.TempDir()
			packageDir := filepath.Join(root, "node_modules", "fixture")
			if err := os.MkdirAll(filepath.Join(packageDir, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{"cli.js", "other.js"} {
				if err := os.WriteFile(filepath.Join(packageDir, "bin", file), []byte("#!/usr/bin/env node\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			manifest := []byte(`{"name":"fixture","bin":["./bin/cli.js","bin/other.js"]}`)
			if err := os.WriteFile(filepath.Join(packageDir, "package.json"), manifest, 0600); err != nil {
				t.Fatal(err)
			}
			if err := FinalizeNPM(t.Context(), root, ".", guest); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"cli.js", "other.js"} {
				wrapper := name
				if !guest {
					wrapper += ".cmd"
				}
				body, err := os.ReadFile(filepath.Join(root, "node_modules", ".bin", wrapper))
				if err != nil || !strings.Contains(string(body), name) || !strings.Contains(string(body), "node ") {
					t.Fatalf("array command %s: %s %v", wrapper, body, err)
				}
			}
		})
	}
}

func TestFinalizeNPMArrayBinsRejectUnsafeMetadata(t *testing.T) {
	for _, value := range []any{[]string{"../../outside.js"}, []string{"/absolute.js"}, []string{"bin/unsafe;.js"}, []any{1}, make([]string, 257)} {
		root := t.TempDir()
		packageDir := filepath.Join(root, "node_modules", "fixture")
		if err := os.MkdirAll(packageDir, 0700); err != nil {
			t.Fatal(err)
		}
		manifest, err := json.Marshal(map[string]any{"name": "fixture", "bin": value})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(packageDir, "package.json"), manifest, 0600); err != nil {
			t.Fatal(err)
		}
		if err := FinalizeNPM(t.Context(), root, ".", true); err == nil {
			t.Fatalf("unsafe bin metadata accepted: %s", manifest)
		}
	}
}
