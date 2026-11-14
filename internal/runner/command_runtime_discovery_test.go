package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommandRuntimeDiscoveryDoesNotExecuteScriptsOrExposeEnvironment(t *testing.T) {
	root, programs := t.TempDir(), t.TempDir()
	marker := filepath.Join(programs, "unexpected-execution")
	name, script := "node", "#!/bin/sh\nprintf executed > '"+marker+"'\n"
	if runtime.GOOS == "windows" {
		name, script = "node.cmd", "@echo executed > \""+marker+"\"\r\n"
	}
	if err := os.WriteFile(filepath.Join(programs, name), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", programs)
	t.Setenv("DISCOVERY_PRIVATE_TEST_VALUE", "private-discovery-value")
	info, err := DiscoverCommandRuntimeHost(t.Context(), root)
	if err != nil || info.OS != runtime.GOOS || info.Architecture != runtime.GOARCH || len(info.ProcessExecutables) != 0 {
		t.Fatalf("script or invented runtime entered discovery: %+v err=%v", info, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime discovery executed a PATH script")
	}
	raw, err := json.Marshal(info)
	if err != nil || strings.Contains(string(raw), "private-discovery-value") || strings.Contains(string(raw), "DISCOVERY_PRIVATE_TEST_VALUE") {
		t.Fatal("runtime discovery exposed environment values", err)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if _, err := DiscoverCommandRuntimeHost(canceled, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery ignored its context: %v", err)
	}
}

func TestCommandRuntimeDiscoveryDoesNotInventMissingRuntimePaths(t *testing.T) {
	programs := t.TempDir()
	t.Setenv("PATH", programs)
	info, err := DiscoverCommandRuntimeHost(t.Context(), t.TempDir())
	if err != nil || len(info.ProcessExecutables) != 0 {
		t.Fatalf("missing PATH runtimes were invented: %+v err=%v", info, err)
	}
}

func TestCommandRuntimeDiscoveryExcludesWorkspaceOwnedExecutables(t *testing.T) {
	root := t.TempDir()
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(current)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	name := "node"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	copy, err := os.OpenFile(filepath.Join(root, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(copy, source)
	closeErr := copy.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal(copyErr, closeErr)
	}
	t.Setenv("PATH", root)
	info, err := DiscoverCommandRuntimeHost(t.Context(), root)
	if err != nil || len(info.ProcessExecutables) != 0 {
		t.Fatalf("workspace-supplied native image was advertised as a host runtime: %+v err=%v", info, err)
	}
}
