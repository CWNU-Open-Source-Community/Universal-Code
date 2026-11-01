package skills

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func TestBuiltinRegistryIsDeterministicBoundedAndReadOnly(t *testing.T) {
	registry, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	listed := registry.List("")
	names := make([]string, 0, len(listed))
	for _, manifest := range listed {
		names = append(names, manifest.Name)
		for _, dependency := range manifest.ToolDependencies {
			if dependency == toolgateway.ShellTool || dependency == toolgateway.SpecialistDelegationProposeTool {
				t.Fatalf("built-in %s declares escalation dependency %s", manifest.Name, dependency)
			}
		}
	}
	if want := []string{
		"code", "debug", "doctor", "focused-checks", "frontend-design", "init", "learn", "loop-monitor", "plan-delivery",
		"review", "run-skill-generator", "run-verify", "script", "security-review", "simplify",
	}; !reflect.DeepEqual(names, want) {
		t.Fatalf("built-in order = %v, want %v", names, want)
	}
	review := registry.List(domain.ProfileReview)
	if want := []string{
		"debug", "doctor", "focused-checks", "loop-monitor", "plan-delivery", "review", "security-review",
	}; !reflect.DeepEqual(manifestNames(review), want) {
		t.Fatalf("review profile projection = %#v", review)
	}

	listed[0].Profiles[0] = domain.ProfileScript
	listed[0].ToolDependencies[0] = toolgateway.ShellTool
	again, ok := registry.Get("code")
	if !ok || again.Profiles[0] != domain.ProfileCode || again.ToolDependencies[0] != toolgateway.ListWorkspaceTool {
		t.Fatal("registry exposed mutable manifest slices")
	}
	if _, ok := registry.Get("Code"); ok {
		t.Fatal("registry normalized an invalid skill name")
	}
}

func manifestNames(manifests []Manifest) []string {
	names := make([]string, len(manifests))
	for index, manifest := range manifests {
		names[index] = manifest.Name
	}
	return names
}

func TestLoadFSUsesStrictJSONAndPathBinding(t *testing.T) {
	content := []byte("# Test\n")
	manifest := fixtureManifest(content)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	valid := func() fstest.MapFS {
		return fstest.MapFS{
			"skills/code/manifest.json": &fstest.MapFile{Data: append([]byte(nil), raw...)},
			"skills/code/SKILL.md":      &fstest.MapFile{Data: append([]byte(nil), content...)},
		}
	}
	if _, err := LoadFS(valid(), "skills"); err != nil {
		t.Fatalf("valid registry failed: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(fstest.MapFS)
		want   string
	}{
		{
			name: "unknown field",
			mutate: func(files fstest.MapFS) {
				files["skills/code/manifest.json"].Data = append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
			},
			want: "unknown field",
		},
		{
			name: "trailing JSON",
			mutate: func(files fstest.MapFS) {
				files["skills/code/manifest.json"].Data = append(append([]byte(nil), raw...), []byte(` {}`)...)
			},
			want: "trailing JSON",
		},
		{
			name: "duplicate field",
			mutate: func(files fstest.MapFS) {
				files["skills/code/manifest.json"].Data = append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"name":"code"}`)...)
			},
			want: "duplicate field",
		},
		{
			name: "invalid UTF-8",
			mutate: func(files fstest.MapFS) {
				files["skills/code/manifest.json"].Data = append([]byte(nil), raw...)
				files["skills/code/manifest.json"].Data[1] = 0xff
			},
			want: "valid UTF-8",
		},
		{
			name: "directory mismatch",
			mutate: func(files fstest.MapFS) {
				changed := manifest
				changed.Name = "review"
				files["skills/code/manifest.json"].Data, _ = json.Marshal(changed)
			},
			want: "does not match manifest name",
		},
		{
			name: "content symlink",
			mutate: func(files fstest.MapFS) {
				files["skills/code/SKILL.md"].Mode = fs.ModeSymlink
			},
			want: "symbolic links",
		},
		{
			name: "checksum drift",
			mutate: func(files fstest.MapFS) {
				files["skills/code/SKILL.md"].Data = []byte("# Changed\n")
			},
			want: "content_bytes",
		},
		{
			name: "non-directory registry entry",
			mutate: func(files fstest.MapFS) {
				files["skills/README.md"] = &fstest.MapFile{Data: []byte("unexpected")}
			},
			want: "invalid skill registry entry",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := valid()
			test.mutate(files)
			_, err := LoadFS(files, "skills")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadFSRejectsSymlinkRegistryRoot(t *testing.T) {
	files := fstest.MapFS{
		"skills": &fstest.MapFile{Mode: fs.ModeSymlink},
	}
	if _, err := LoadFS(files, "skills"); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink root error = %v", err)
	}
}

func TestRegistryValidationDetectsInternalDrift(t *testing.T) {
	registry, err := BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entry := registry.entries["code"]
	entry.content = append(entry.content, 'x')
	registry.entries["code"] = entry
	if err := registry.Validate(); err == nil || !strings.Contains(err.Error(), "content_bytes") {
		t.Fatalf("internal drift error = %v", err)
	}
}
