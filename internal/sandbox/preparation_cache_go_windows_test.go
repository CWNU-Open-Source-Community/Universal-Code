//go:build windows

package sandbox

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
	"golang.org/x/mod/sumdb/dirhash"
)

func TestPreparedLocalCacheRealGoKeepsVetAtLongWorkspace(t *testing.T) {
	source := os.Getenv("TRAVERSE_BOARD_GO_LPAC_FIXTURE")
	if source == "" {
		t.Skip("set TRAVERSE_BOARD_GO_LPAC_FIXTURE to an installed Go distribution for real LPAC module/vet validation")
	}
	base := windowsTestTempDir(t)
	toolchain, drydock := filepath.Join(base, "go"), filepath.Join(base, "workspace 项目,cache-"+strings.Repeat("long-", 12))
	if err := os.CopyFS(toolchain, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(drydock, 0700); err != nil {
		t.Fatal(err)
	}
	const modulePath, version = "example.com/cache/dep", "v0.0.1"
	const moduleBody = "module example.com/cache/dep\n\ngo 1.25.0\n"
	cache := filepath.Join(drydock, filepath.FromSlash(projectprep.GoModuleCacheDirectory(".")))
	download := filepath.Join(cache, "cache", "download", filepath.FromSlash(modulePath), "@v")
	if err := os.MkdirAll(download, 0700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(download, version+".zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, body := range map[string]string{"go.mod": moduleBody, "dep.go": "package dep\nfunc Answer() int { return 42 }\n"} {
		entry, err := z.Create(modulePath + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	zipHash, err := dirhash.HashZip(archive, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	modHash, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewBufferString(moduleBody)), nil })
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		filepath.Join(download, version+".mod"):  moduleBody,
		filepath.Join(download, version+".info"): `{"Version":"v0.0.1","Time":"2020-01-01T00:00:00Z"}`,
		filepath.Join(drydock, "go.mod"):         "module example.com/lpac-vet\n\ngo 1.25.0\n\nrequire example.com/cache/dep v0.0.1\n",
		filepath.Join(drydock, "go.sum"):         fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", modulePath, version, zipHash, modulePath, version, modHash),
		filepath.Join(drydock, "answer_test.go"): "package lpacvet\nimport (\"testing\"; \"example.com/cache/dep\")\nfunc TestAnswer(t *testing.T) { if dep.Answer()!=42 { t.Fatal(dep.Answer()) } }\n",
	} {
		if err := os.WriteFile(name, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if length := len(filepath.Join(cache, filepath.FromSlash(modulePath)+"@"+version)); length <= 260 {
		t.Fatalf("fixture did not exceed Windows child cwd limit: %d", length)
	}
	// File-proxy artifacts are inputs. Go must not create locks/hash files or
	// rewrite them while expanding modules into its private cache.
	for _, name := range []string{version + ".mod", version + ".info", version + ".zip"} {
		if err := os.Chmod(filepath.Join(download, name), 0444); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(download, name), 0600) })
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
	request := localWindowsTestRequest(t, backend, drydock, toolchain, "/go", "/go/bin/go.exe", []string{"mod", "download"})
	request.Manifest.Resources = ResourceLimits{CPUQuotaMillis: 4000, MemoryBytes: 3 * 1024 * 1024 * 1024, PIDs: 128, MaxOutputBytes: 2 * 1024 * 1024}
	request.Manifest.Environment = []EnvironmentBinding{{Name: "GOMAXPROCS", Source: EnvironmentLiteral, Value: "2"}, {Name: "GOFLAGS", Source: EnvironmentLiteral, Value: "-p=2"}}
	request.Manifest.TimeoutSeconds = 90
	request.MaxDiskWriteBytes = 512 * 1024 * 1024
	for _, action := range []string{"download", "test"} {
		if action == "test" {
			request.Manifest.Command.Arguments = []string{"test", "-mod=readonly", "./...", "-count=1"}
		}
		request.Binding.OperationKeySHA256 = localFingerprint("offline-go-vet", action, t.Name())
		result, err := backend.Run(t.Context(), request)
		if err != nil || result.ExitCode != 0 || !result.TreeReaped || !result.ACLsRestored || !result.ZeroNetworkCapabilities {
			t.Fatalf("Go %s: %v stdout=%s stderr=%s", action, err, result.Stdout.Data, result.Stderr.Data)
		}
		if action == "test" && !strings.Contains(string(result.Stdout.Data), "ok") {
			t.Fatalf("Go test did not complete: %s", result.Stdout.Data)
		}
	}
	if _, err := os.Stat(filepath.Join(cache, filepath.FromSlash(modulePath)+"@"+version)); !os.IsNotExist(err) {
		t.Fatalf("Go extracted modules into overlong persistent cwd: %v", err)
	}
	entries, err := os.ReadDir(download)
	if err != nil || len(entries) != 3 {
		t.Fatalf("Go wrote into file-proxy inputs: %+v %v", entries, err)
	}
	if hash, err := dirhash.HashZip(archive, dirhash.Hash1); err != nil || hash != zipHash {
		t.Fatalf("Go changed cached archive: %q %v", hash, err)
	}
}
