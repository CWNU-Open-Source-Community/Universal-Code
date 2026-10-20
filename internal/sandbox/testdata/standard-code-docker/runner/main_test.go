package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFixedProjectToolchainsKeepFormatterAndNestedNPMInGuest(t *testing.T) {
	executable, environment, err := fixedToolchainAt("gofmt", t.TempDir())
	if err != nil || executable != "/opt/toolchains/go/bin/gofmt" {
		t.Fatalf("gofmt binding %q %v", executable, err)
	}
	if !strings.Contains(strings.Join(environment, "\n"), "PATH=/opt/toolchains/go/bin:") {
		t.Fatal("formatter lost its fixed toolchain PATH")
	}
	executable, environment, err = fixedToolchainAt("node", t.TempDir())
	if err != nil || executable != "/opt/toolchains/node/bin/node" || !strings.Contains(strings.Join(environment, "\n"), "npm_config_audit=false") || !strings.Contains(strings.Join(environment, "\n"), "npm_config_fund=false") {
		t.Fatalf("Node/npm binding %q %+v %v", executable, environment, err)
	}
	if _, _, err := fixedToolchainAt("shell", t.TempDir()); err == nil {
		t.Fatal("project actions granted a new shell toolchain")
	}
	bindings := map[string]string{}
	for _, value := range environment {
		if name, value, ok := strings.Cut(value, "="); ok {
			bindings[name] = value
		}
	}
	if bindings["YARN_IGNORE_PATH"] != "1" {
		t.Fatal("repository yarn-path can replace the fixed Yarn executable")
	}
	if home := bindings["HOME"]; home == "" {
		t.Fatal("npm has no isolated home")
	} else if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatal("isolated npm home is not created", err)
	}
	if bindings["NPM_CONFIG_USERCONFIG"] == "" || bindings["NPM_CONFIG_GLOBALCONFIG"] == "" || bindings["NPM_CONFIG_USERCONFIG"] == bindings["NPM_CONFIG_GLOBALCONFIG"] {
		t.Fatal("npm config files are not independently bound")
	}
}

func TestBunUsesFixedGuestToolchainAndPreparationBounds(t *testing.T) {
	for _, toolchain := range []string{"bun", "bun-prepare"} {
		executable, environment, err := fixedToolchainAt(toolchain, t.TempDir())
		joined := strings.Join(environment, "\n")
		if err != nil || executable != "/opt/toolchains/bun/bin/bun" || !strings.Contains(joined, "PATH=/opt/toolchains/bun/bin:/opt/toolchains/node/bin:/usr/bin:/bin") || !strings.Contains(joined, "BUN_FEATURE_FLAG_DISABLE_NATIVE_DEPENDENCY_LINKER=1") {
			t.Fatalf("Bun toolchain %q %+v %v", executable, environment, err)
		}
	}
	values := []string{"1073741824", "200000", "67108864", strconv.FormatInt(workspaceFreeBytes, 10), strconv.FormatInt(workspaceFreeEntries, 10)}
	if _, err := parseExecutionLimitsForToolchain(values, "bun-prepare"); err != nil {
		t.Fatal("Bun preparation bounds rejected", err)
	}
	if _, err := parseExecutionLimitsForToolchain(values, "bun"); err == nil {
		t.Fatal("Bun test acquired preparation write bounds")
	}
}

func TestDependencyWriteLimitsRemainSpecificAndOffline(t *testing.T) {
	values := []string{"1073741824", "200000", "67108864", strconv.FormatInt(workspaceFreeBytes, 10), strconv.FormatInt(workspaceFreeEntries, 10)}
	if _, err := parseExecutionLimits(values); err == nil {
		t.Fatal("ordinary command acquired preparation limits")
	}
	if limits, err := parseExecutionLimitsForToolchain(values, "go-prepare"); err != nil || limits.GrowthBytes != 1024*1024*1024 {
		t.Fatalf("preparation limits: %+v %v", limits, err)
	}
	_, environment, err := fixedToolchainAt("go-prepare", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off"} {
		if !strings.Contains(strings.Join(environment, "\n"), required) {
			t.Fatalf("preparation lost %s", required)
		}
	}
}

func TestParseExecutionLimitsAcceptsOnlyFixedProfile(t *testing.T) {
	values := []string{
		strconv.FormatInt(workspaceGrowthBytes, 10),
		strconv.FormatInt(workspaceGrowthEntries, 10),
		strconv.FormatInt(workspaceFileBytes, 10),
		strconv.FormatInt(workspaceFreeBytes, 10),
		strconv.FormatInt(workspaceFreeEntries, 10),
	}
	limits, err := parseExecutionLimits(values)
	if err != nil || limits.GrowthBytes != workspaceGrowthBytes ||
		limits.GrowthEntries != workspaceGrowthEntries ||
		limits.FileBytes != workspaceFileBytes || limits.FreeBytes != workspaceFreeBytes ||
		limits.FreeEntries != workspaceFreeEntries {
		t.Fatalf("fixed resource profile was not accepted: %#v err=%v", limits, err)
	}
	for index := range values {
		changed := append([]string(nil), values...)
		changed[index] = strconv.FormatInt(mustParseInt(t, changed[index])+1, 10)
		if _, err := parseExecutionLimits(changed); err == nil {
			t.Fatalf("changed resource field %d was accepted", index)
		}
	}
}

func TestWorkspaceUsageAccountsBytesEntriesAndDoesNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "source.txt"),
		[]byte("fixed"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	usage, err := captureWorkspaceUsage(root, 3)
	if err != nil || usage.Entries != 3 || usage.Bytes < 5 || usage.Bytes >= 1024 {
		t.Fatalf("workspace usage=%#v err=%v", usage, err)
	}
	if _, err := captureWorkspaceUsage(root, 2); err == nil {
		t.Fatal("workspace entry accounting bound was not enforced")
	}
	limits := executionLimits{GrowthBytes: 8, GrowthEntries: 2}
	if workspaceGrowthExceeded(usage, workspaceUsage{Bytes: usage.Bytes + 8,
		Entries: usage.Entries + 2}, limits) {
		t.Fatal("exact workspace growth limit was rejected")
	}
	if !workspaceGrowthExceeded(usage, workspaceUsage{Bytes: usage.Bytes + 9,
		Entries: usage.Entries + 2}, limits) ||
		!workspaceGrowthExceeded(usage, workspaceUsage{Bytes: usage.Bytes + 8,
			Entries: usage.Entries + 3}, limits) {
		t.Fatal("workspace byte or entry growth above the bound was accepted")
	}
}

func mustParseInt(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
