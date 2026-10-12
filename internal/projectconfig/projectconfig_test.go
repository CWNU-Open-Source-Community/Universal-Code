package projectconfig

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ConfigDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ConfigDirName, ConfigFileName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadWorkspaceRoundTripAndFingerprint(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `protocol: project_config.v1
read_only: true
allowed_profiles: [review]
budget:
  max_turns: 10
  max_tool_calls: 5
exclude_paths: [fixtures, "secrets/**"]
skill_suggestions: [scan-aid@1.0.0]
`)
	config, found, err := LoadWorkspace(context.Background(), dir)
	if err != nil || !found {
		t.Fatalf("load: %v found=%t", err, found)
	}
	ceiling := Ceiling{AllowedProfiles: []string{"code", "review"}, MaxTurns: 100, MaxToolCalls: 100,
		RegisteredCommands: map[string]struct{}{"test.go": {}}}
	effective, rejections, err := config.Narrow(ceiling)
	if err != nil || len(rejections) != 0 {
		t.Fatalf("narrow: %v rejections=%v", err, rejections)
	}
	if !effective.ReadOnly || effective.MaxTurns != 10 || effective.MaxToolCalls != 5 ||
		len(effective.AllowedProfiles) != 1 || effective.AllowedProfiles[0] != "review" ||
		len(effective.ExcludePaths) != 2 || len(effective.SkillSuggestions) != 1 {
		t.Fatalf("unexpected effective view: %#v", effective)
	}
	if effective.Fingerprint() == "" || len(effective.Fingerprint()) != 64 {
		t.Fatalf("fingerprint missing: %q", effective.Fingerprint())
	}
	// Fingerprint is stable across loads.
	again, _, err := LoadWorkspace(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	againEffective, _, _ := again.Narrow(ceiling)
	if againEffective.Fingerprint() != effective.Fingerprint() {
		t.Fatal("fingerprint drifted across identical loads")
	}
}

func TestLoadFailsClosedOnHostileInputs(t *testing.T) {
	ctx := context.Background()
	cases := []struct{ name, content string }{

		{name: "unknown field", content: "protocol: project_config.v1\nsecret_key: abc\n"},

		{name: "wrong protocol", content: "protocol: project_config.v9\n"},

		{name: "type error", content: "protocol: project_config.v1\nread_only: maybe\n"},

		{name: "alias", content: "protocol: project_config.v1\nread_only: &a true\nbudget:\n  max_turns: *a\n"},

		{name: "path escape", content: "protocol: project_config.v1\nexclude_paths: [../etc]\n"},

		{name: "absolute path", content: "protocol: project_config.v1\nexclude_paths: [" + filepath.Join("/", "etc", "passwd") + "]\n"},

		{name: "bad skill ref", content: "protocol: project_config.v1\nskill_suggestions: [shell]\n"},

		{name: "negative budget", content: "protocol: project_config.v1\nbudget:\n  max_turns: -3\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfig(t, dir, tc.content)
			if _, err := Load(ctx, path); err == nil {
				t.Fatalf("hostile config was accepted: %s", tc.content)
			}
		})
	}
}

func TestLoadRejectsSymlinkConfig(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(target, []byte("protocol: project_config.v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ConfigDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ConfigDirName, ConfigFileName)
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation unavailable")
		}
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), link); err == nil {
		t.Fatal("symlinked config was accepted")
	}
}

func TestLoadRejectsOversizedAndDeep(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := writeConfig(t, dir, "protocol: project_config.v1\n"+strings.Repeat("x", MaxConfigBytes))
	if _, err := Load(ctx, path); err == nil {
		t.Fatal("oversized config accepted")
	}
	dir2 := t.TempDir()
	nesting := strings.Repeat("[", MaxYAMLDepth+5)
	path2 := writeConfig(t, dir2, "protocol: project_config.v1\nread_only: "+nesting)
	if _, err := Load(ctx, path2); err == nil {
		t.Fatal("over-deep config accepted")
	}
}

func TestNarrowRejectsWidening(t *testing.T) {
	config := Config{Protocol: ProtocolVersion, Budget: &Budget{MaxTurns: 500}}
	ceiling := Ceiling{AllowedProfiles: []string{"code"}, MaxTurns: 100, MaxToolCalls: 100,
		RegisteredCommands: map[string]struct{}{"test.go": {}}}
	_, rejections, err := config.Narrow(ceiling)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejections) != 1 || rejections[0].Field != "budget.max_turns" {
		t.Fatalf("widening budget was not rejected: %v", rejections)
	}
	config = Config{Protocol: ProtocolVersion, AllowedProfiles: []string{"script"}}
	_, rejections, err = config.Narrow(ceiling)
	if err != nil || len(rejections) != 1 || rejections[0].Field != "allowed_profiles" {
		t.Fatalf("widening profile set was not rejected: %v err=%v", rejections, err)
	}
	config = Config{Protocol: ProtocolVersion, TestCommandID: "rm-rf-root"}
	_, rejections, err = config.Narrow(ceiling)
	if err != nil || len(rejections) != 1 || rejections[0].Field != "test_command_id" {
		t.Fatalf("unregistered command id was not rejected: %v err=%v", rejections, err)
	}
}

func FuzzLoadNeverAcceptsHostileConfig(f *testing.F) {
	f.Add("protocol: project_config.v1\n")
	f.Add("protocol: project_config.v1\nread_only: true\n")
	f.Add("&anchor x\n*anchor\n")
	f.Add("protocol: project_config.v1\nexclude_paths: [../../etc]\n")
	f.Add(strings.Repeat("a: b\n", 3000))
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > MaxConfigBytes+64 {
			raw = raw[:MaxConfigBytes+64]
		}
		dir := t.TempDir()
		path := writeConfig(t, dir, raw)
		// Load must either succeed with a validated config or fail; it must
		// never panic or produce an unvalidated struct.
		if config, err := Load(context.Background(), path); err == nil {
			if config.Protocol != ProtocolVersion {
				t.Fatalf("unvalidated protocol escaped: %#v", config)
			}
		}
	})
}

func TestProjectConfigRejectsAnchorAndParentIndirection(t *testing.T) {
	for _, content := range []string{"protocol: project_config.v1\nread_only: &anchor true\n", "protocol: project_config.v1\nread_only: true\n---\nread_only: false\n"} {
		dir := t.TempDir()
		writeConfig(t, dir, content)
		if _, _, err := LoadWorkspace(t.Context(), dir); err == nil {
			t.Fatal("hostile YAML admitted")
		}
	}
	root, outside := t.TempDir(), t.TempDir()
	writeConfig(t, outside, "protocol: project_config.v1\nread_only: true\n")
	if err := os.Symlink(filepath.Join(outside, ConfigDirName), filepath.Join(root, ConfigDirName)); err != nil {
		t.Skipf("directory symlink creation unavailable: %v", err)
	}
	if _, _, err := LoadWorkspace(t.Context(), root); err == nil {
		t.Fatal("project config followed an outside parent symlink")
	}
}
