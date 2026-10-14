//go:build windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestWindowsLocalNodeEnvironmentOwnsLoaderOptions(t *testing.T) {
	values, err := buildLocalEnvironment(filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "tmp"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(utf16.Decode(values)), "NODE_OPTIONS=--preserve-symlinks --preserve-symlinks-main\x00") {
		t.Fatal("Node loader still resolves denied ancestors")
	}
	if _, err := buildLocalEnvironment(filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "tmp"), nil, []EnvironmentBinding{{Name: "NODE_OPTIONS", Source: EnvironmentLiteral, Value: "--require=repository-code.js"}}); err == nil {
		t.Fatal("repository overrode Node startup policy")
	}
}

func TestWindowsLocalSandboxLoadsNPMWithoutDriveMetadataAccess(t *testing.T) {
	if os.Getenv("TRAVERSE_BOARD_NODE_LPAC_FIXTURE") == "" {
		t.Skip("set TRAVERSE_BOARD_NODE_LPAC_FIXTURE to an installed Node/npm distribution for real LPAC compatibility validation")
	}
	source, err := filepath.Abs(os.Getenv("TRAVERSE_BOARD_NODE_LPAC_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	base := windowsTestTempDir(t)
	toolchain, drydock := filepath.Join(base, "node"), filepath.Join(base, "drydock")
	if err := os.MkdirAll(drydock, 0700); err != nil {
		t.Fatal(err)
	}
	// Never grant ACLs to the active application's distribution. This owned
	// copy retains the exact executable/module bytes for the controlled probe.
	if err := os.CopyFS(toolchain, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	backend, err := NewPlatformLocalBackend(WithLocalOwnerRoot(filepath.Join(base, "owners")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	readiness, err := backend.Readiness(t.Context(), LocalRuntimeCapabilities{Enabled: true})
	if err != nil || !readiness.Ready {
		t.Fatalf("Local not ready: %+v %v", readiness, err)
	}
	request := localWindowsTestRequest(t, backend, drydock, toolchain, "/node", "/node/node.exe", []string{"/node/node_modules/npm/bin/npm-cli.js", "--version"})
	request.Manifest.Resources.MemoryBytes = 2 * 1024 * 1024 * 1024
	request.Manifest.Resources.PIDs = 16
	result, err := backend.Run(context.Background(), request)
	if err != nil || result.ExitCode != 0 || !result.TreeReaped || !result.ACLsRestored || strings.TrimSpace(string(result.Stdout.Data)) == "" {
		t.Fatalf("npm LPAC load: %v stdout=%s stderr=%s", err, result.Stdout.Data, result.Stderr.Data)
	}
	for name, body := range map[string]string{
		"package.json":      `{"name":"local-npm-fixture","version":"1.0.0","scripts":{"test":"node -e \"console.log('isolated-npm-test')\""}}`,
		"package-lock.json": `{"name":"local-npm-fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"local-npm-fixture","version":"1.0.0"}}}`,
	} {
		if err := os.WriteFile(filepath.Join(drydock, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"ci", "test"} {
		request.Manifest.Command.Arguments = []string{"/node/node_modules/npm/bin/npm-cli.js", action, "--offline", "--no-audit", "--no-fund", "--cache", "/workspace/npm-cache"}
		if action == "ci" {
			request.Manifest.Command.Arguments = append(request.Manifest.Command.Arguments, "--ignore-scripts", "--bin-links=false")
		}
		request.Binding.OperationKeySHA256 = localFingerprint("npm-command", action, t.Name())
		result, err = backend.Run(t.Context(), request)
		if err != nil || result.ExitCode != 0 || !result.TreeReaped || !result.ACLsRestored {
			t.Fatalf("npm %s in LPAC: %v stdout=%s stderr=%s", action, err, result.Stdout.Data, result.Stderr.Data)
		}
		if action == "test" && !strings.Contains(string(result.Stdout.Data), "isolated-npm-test") {
			t.Fatalf("npm test did not run child Node: %s", result.Stdout.Data)
		}
	}
	// The same options must load an ordinary CommonJS program and preserve the
	// private sibling boundary; system-drive metadata access varies with host
	// ACLs, so record that observation without requiring a particular drive.
	private := filepath.Join(base, "private", "sentinel.txt")
	if err := os.MkdirAll(filepath.Dir(private), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(private, []byte("private host content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drydock, "main.cjs"), []byte(`const fs=require('node:fs'),path=require('node:path');fs.readFileSync(__filename);try{fs.readFileSync(path.join(__dirname,'..','private','sentinel.txt'));process.exit(2)}catch(e){if(e.code!=='EPERM'&&e.code!=='EACCES')throw e}const root=path.parse(__filename).root;for(const [name,probe] of [['lstat',fs.lstatSync],['list',fs.readdirSync]]){try{probe(root);console.log('drive-root-'+name+'=allowed')}catch(e){if(e.code!=='EPERM'&&e.code!=='EACCES')throw e;console.log('drive-root-'+name+'=denied')}}console.log('private-denied-script-loaded')`), 0600); err != nil {
		t.Fatal(err)
	}
	request.Manifest.Command.Arguments = []string{"/workspace/main.cjs"}
	request.Binding.OperationKeySHA256 = localFingerprint("node-script", t.Name())
	result, err = backend.Run(t.Context(), request)
	if err != nil || result.ExitCode != 0 || !strings.Contains(string(result.Stdout.Data), "private-denied-script-loaded") {
		t.Fatalf("Node LPAC load: %v stdout=%s stderr=%s", err, result.Stdout.Data, result.Stderr.Data)
	}
	t.Logf("Node sandbox boundary observation: %s", strings.TrimSpace(string(result.Stdout.Data)))
}

func TestWindowsLocalToolchainPreflightGoBinAvailableToNestedProcesses(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "go.exe"), []byte("PATH inspection does not execute this file"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := buildLocalEnvironment(filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "tmp"), []localPinnedRoot{{path: root}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range strings.Split(string(utf16.Decode(values)), "\x00") {
		if strings.HasPrefix(value, "PATH=") {
			if !strings.Contains(value, filepath.Join(root, "bin")) {
				t.Fatalf("nested go process cannot find its pinned toolchain: %s", value)
			}
			return
		}
	}
	t.Fatal("isolated PATH is missing")
}
