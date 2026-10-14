package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListModulesPreservesDependencyDirectoryWithSpaces(t *testing.T) {
	root := t.TempDir()
	dependency := filepath.Join(root, "dependency with spaces")
	if err := os.MkdirAll(dependency, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(dependency, "go.mod"):  "module example.com/spaced\n\ngo 1.26.9\n",
		filepath.Join(dependency, "dep.go"):  "package spaced\nconst Value = 1\n",
		filepath.Join(dependency, "LICENSE"): "MIT License\nPermission is hereby granted, free of charge\n",
		filepath.Join(root, "go.mod"): "module cyberagent-workbench\n\ngo 1.26.9\n" +
			"require example.com/spaced v0.0.0\nreplace example.com/spaced => \"" + filepath.ToSlash(dependency) + "\"\n",
		filepath.Join(root, "main.go"): "package main\nimport _ \"example.com/spaced\"\nfunc main() {}\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPROXY", "off")
	modules, err := listModules(".", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 1 || modules[0].Path != "example.com/spaced" ||
		modules[0].Version != "v0.0.0" || filepath.Clean(modules[0].Dir) != dependency {
		t.Fatalf("listed modules = %#v", modules)
	}
	if err := validateLicenses(modules); err != nil {
		t.Fatalf("valid dependency license was lost: %v", err)
	}
}
