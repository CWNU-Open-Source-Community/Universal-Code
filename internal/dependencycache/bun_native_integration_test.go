package dependencycache_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/dependencycache"
	"cyberagent-workbench/internal/projectprep"
)

// This executes a verified native Bun, not a Docker substitute. CI supplies
// exact public registry archives; the product detector/broker/argv are real.
func TestBunNativeFrozenOfflineCache(t *testing.T) {
	bun, archive, scopedArchive := os.Getenv("TRAVERSE_BUN_TEST_BIN"), os.Getenv("TRAVERSE_BUN_TEST_TARBALL"), os.Getenv("TRAVERSE_BUN_TEST_SCOPED_TARBALL")
	if bun == "" || archive == "" || scopedArchive == "" {
		t.Skip("set native Bun 1.4.3 and integrity-pinned registry tarball paths")
	}
	if !filepath.IsAbs(bun) {
		t.Fatal("native Bun must be an explicit path")
	}
	version, err := exec.Command(bun, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != projectprep.BunVersion {
		t.Fatalf("Bun version=%s %v", version, err)
	}
	files := map[string]string{
		"package.json":     `{"name":"frozen-bun-fixture","packageManager":"bun@1.4.3","dependencies":{"is-number":"7.0.0","@colors/colors":"1.6.0"},"scripts":{"test":"bun test","postinstall":"bun -e \"require('node:fs').writeFileSync('unexpected-postinstall','ran')\""}}`,
		"bun.lock":         `{"lockfileVersion":2,"configVersion":1,"workspaces":{"":{"name":"frozen-bun-fixture","dependencies":{"is-number":"7.0.0","@colors/colors":"1.6.0",},},},"packages":{"is-number":["is-number@7.0.0","",{},"sha512-41Cifkg6e8TylSpdtTpeLVMqvSBEVzTttHvERD741+pnZ8ANv0004MRL43QKPDlK9cGvNp6NZWZUBlbGXYxxng=="],"@colors/colors":["@colors/colors@1.6.0","",{},"sha512-Ir+AOibqzrIsL6ajt3Rz3LskB7OiMVHqltZmspbW/TJuTVuyOMirVqAkjfY6JISiLHgyNqicAC8AyHHGzNd/dA=="],}}`,
		"original.test.ts": `import {test,expect} from 'bun:test';import isNumber from 'is-number';import colors from '@colors/colors';test('original_registry_test',()=>{expect(isNumber('42')).toBe(true);expect(colors.stripColors(colors.green('ok'))).toBe('ok');});`,
	}
	shared := t.TempDir()
	broker, err := dependencycache.New(shared)
	if err != nil {
		t.Fatal(err)
	}
	var network atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		network.Add(1)
		http.Error(w, "unexpected network request", 502)
	}))
	defer proxy.Close()
	check := func(context.Context) error { return nil }
	for attempt := 0; attempt < 2; attempt++ {
		root := filepath.Join(t.TempDir(), "Bun 中文 project")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := projectprep.Detect(t.Context(), root, ".")
		if err != nil || !plan.Ready {
			t.Fatalf("plan=%+v %v", plan, err)
		}
		if attempt == 0 {
			stats, err := broker.Prepare(t.Context(), plan, nil, root, nil, check)
			if !errors.Is(err, dependencycache.ErrSourceDenied) || stats.Bytes != 0 {
				t.Fatalf("unapproved source %+v %v", stats, err)
			}
			for _, pkg := range plan.Packages {
				input := archive
				if pkg.ModulePath == "@colors/colors" {
					input = scopedArchive
				}
				body, err := os.ReadFile(input)
				if err != nil {
					t.Fatal(err)
				}
				key := sha256.Sum256([]byte(pkg.Kind + "\x00" + pkg.ModulePath + "\x00" + pkg.Version + "\x00" + pkg.Integrity))
				if err := os.WriteFile(filepath.Join(shared, hex.EncodeToString(key[:])+".blob"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		stats, err := broker.Prepare(t.Context(), plan, nil, root, nil, check)
		if err != nil || stats.CachedPackages != 2 || stats.Bytes != 0 {
			t.Fatalf("cache %+v %v", stats, err)
		}
		adapter := commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeDockerSandboxBackend, "native-verifier", "generation")
		install, err := application.PrepareProjectPreparationCommand(plan, adapter, root, false)
		if err != nil {
			t.Fatal(err)
		}
		args := append([]string{}, install.Arguments...)
		// Only translate the fixed guest cache prefix to the native fixture root.
		for index, arg := range args {
			if strings.HasPrefix(arg, "--cache-dir=/workspace/") {
				args[index] = "--cache-dir=" + filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(arg, "--cache-dir=/workspace/")))
			}
		}
		command := exec.CommandContext(t.Context(), bun, args...)
		command.Dir = root
		command.Env = append(os.Environ(), "PATH="+filepath.Dir(bun)+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+t.TempDir(), "HTTP_PROXY="+proxy.URL, "HTTPS_PROXY="+proxy.URL, "ALL_PROXY="+proxy.URL, "NO_PROXY=", "BUN_FEATURE_FLAG_DISABLE_NATIVE_DEPENDENCY_LINKER=1")
		out, err := command.CombinedOutput()
		t.Logf("Bun install %s", out)
		if err != nil {
			t.Fatal(err)
		}

		// A matching name/version alone must not retain edited node_modules.
		installed := filepath.Join(root, "node_modules", "is-number", "index.js")
		if err := os.WriteFile(installed, []byte("module.exports = () => false;"), 0600); err != nil {
			t.Fatal(err)
		}
		retry := exec.CommandContext(t.Context(), bun, args...)
		retry.Dir = root
		retry.Env = command.Env
		if output, err := retry.CombinedOutput(); err != nil {
			t.Fatalf("offline forced reinstall: %s %v", output, err)
		}
		test, err := application.PrepareProjectPreparationTestCommand(plan, adapter, root)
		if err != nil {
			t.Fatal(err)
		}
		command = exec.CommandContext(t.Context(), bun, test.Arguments...)
		command.Dir = root
		command.Env = append(os.Environ(), "PATH="+filepath.Dir(bun)+string(os.PathListSeparator)+os.Getenv("PATH"), "HTTP_PROXY="+proxy.URL, "HTTPS_PROXY="+proxy.URL, "ALL_PROXY="+proxy.URL, "NO_PROXY=")
		out, err = command.CombinedOutput()
		t.Logf("Bun original test %s", out)
		if err != nil || !strings.Contains(string(out), "original_registry_test") {
			t.Fatalf("original test %v", err)
		}
		for name, before := range files {
			after, err := os.ReadFile(filepath.Join(root, name))
			if err != nil || string(after) != before {
				t.Fatalf("original input changed: %s %v", name, err)
			}
		}
		if _, err := os.Stat(filepath.Join(root, "unexpected-postinstall")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("root lifecycle script executed", err)
		}
	}
	if network.Load() != 0 {
		t.Fatal("offline commands contacted proxy", network.Load())
	}
}
