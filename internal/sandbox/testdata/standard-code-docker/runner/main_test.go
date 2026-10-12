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
