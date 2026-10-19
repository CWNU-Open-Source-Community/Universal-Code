package dependencycache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/projectprep"
)

// This opt-in verifier executes only the fixture created below. The operator
// supplies absolute paths; product preparation never falls back to host tools.
// Set TRAVERSE_PNPM_TEST_NODE and TRAVERSE_PNPM_TEST_CLI (pnpm 10.28.1's CLI JS
// file), then run go test ./internal/dependencycache -run TestPNPMNativeWorkspacePreparation -v.
func TestPNPMNativeWorkspacePreparation(t *testing.T) {
	node, cli := os.Getenv("TRAVERSE_PNPM_TEST_NODE"), os.Getenv("TRAVERSE_PNPM_TEST_CLI")
	if node == "" || cli == "" {
		t.Skip("set TRAVERSE_PNPM_TEST_NODE and TRAVERSE_PNPM_TEST_CLI to opt into native pnpm verification")
	}
	for _, tool := range []string{node, cli} {
		if info, err := os.Stat(tool); !filepath.IsAbs(tool) || err != nil || !info.Mode().IsRegular() {
			t.Fatalf("native verifier needs an explicit absolute tool file: %q (%v)", tool, err)
		}
	}
	tools := t.TempDir()
	config := filepath.Join(tools, "empty-config")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	networkLog := filepath.Join(tools, "network-attempts")
	preload := filepath.Join(tools, "deny-network.cjs")
	blockNetwork := fmt.Sprintf(`const fs = require('node:fs');
const deny = () => { fs.appendFileSync(%q, 'attempt\n'); throw new Error('NATIVE_PNPM_NETWORK_DENIED'); };
const net = require('node:net'); net.connect = net.createConnection = net.Socket.prototype.connect = deny;
require('node:tls').connect = deny;
for (const name of ['node:http', 'node:https']) { const mod = require(name); mod.request = mod.get = deny; }
require('node:dns').lookup = deny;
globalThis.fetch = deny;
require('node:module').syncBuiltinESMExports();
`, networkLog)
	if err := os.WriteFile(preload, []byte(blockNetwork), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, directory string, arguments ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		args := append([]string{"--require", preload, cli}, arguments...)
		command := exec.CommandContext(ctx, node, args...)
		command.Dir = directory
		command.Env = append(os.Environ(), "CI=true", "NODE_OPTIONS=", "NODE_COMPILE_CACHE="+filepath.Join(tools, "compile-cache"),
			"PATH="+filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"), "npm_config_userconfig="+config, "npm_config_globalconfig="+config,
			"npm_config_registry=http://127.0.0.1:9/", "npm_config_fetch_retries=0",
			"XDG_CONFIG_HOME="+filepath.Join(tools, "config"), "XDG_CACHE_HOME="+filepath.Join(tools, "cache"),
			"XDG_DATA_HOME="+filepath.Join(tools, "data"), "PNPM_HOME="+filepath.Join(tools, "home"))
		output, err := command.CombinedOutput()
		t.Logf("pnpm %v (cwd %s):\n%s", arguments, directory, output)
		if err != nil {
			t.Fatalf("native pnpm failed: %v", err)
		}
		return strings.TrimSpace(string(output))
	}
	if got := run(t, tools, "--version"); got != projectprep.PNPMVersion {
		t.Fatalf("native verifier requires pnpm %s; got %q", projectprep.PNPMVersion, got)
	}

	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, file := range []struct{ name, body string }{
		{"package/package.json", `{"name":"external-fixture","version":"1.0.0","main":"index.js","scripts":{"postinstall":"node -e \"require('fs').writeFileSync('install-ran','bad')\""}}`},
		{"package/index.js", "module.exports = 'external-ok';\n"},
	} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: file.name, Mode: 0644, Size: int64(len(file.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tarWriter, file.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(tarWriter.Close(), gzipWriter.Close()); err != nil {
		t.Fatal(err)
	}
	body := archive.Bytes()
	sum := sha512.Sum512(body)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	files := map[string]string{
		"package.json":                    `{"private":true,"packageManager":"pnpm@10.28.1","scripts":{"postinstall":"node -e \"require('fs').writeFileSync('install-ran','bad')\""}}`,
		"pnpm-workspace.yaml":             "packages:\n  - 'apps/*'\n  - 'packages/*'\n",
		"apps/app/package.json":           `{"name":"app","version":"1.0.0","scripts":{"test":"node test.cjs","postinstall":"node -e \"require('fs').writeFileSync('install-ran','bad')\""},"dependencies":{"external-fixture":"1.0.0","local-lib":"workspace:*"}}`,
		"apps/app/test.cjs":               "const assert = require('node:assert/strict');\nassert.equal(require('external-fixture'), 'external-ok');\nassert.equal(require('local-lib'), 'workspace-ok');\nrequire('node:fs').writeFileSync('test-passed', 'external+workspace');\nconsole.log('external+workspace passed');\n",
		"packages/local-lib/package.json": `{"name":"local-lib","version":"1.0.0","main":"index.js","scripts":{"postinstall":"node -e \"require('fs').writeFileSync('install-ran','bad')\""}}`,
		"packages/local-lib/index.js":     "module.exports = 'workspace-ok';\n",
		"pnpm-lock.yaml":                  "lockfileVersion: '9.0'\nsettings:\n  autoInstallPeers: true\n  excludeLinksFromLockfile: false\nimporters:\n  .: {}\n  apps/app:\n    dependencies:\n      external-fixture:\n        specifier: 1.0.0\n        version: 1.0.0\n      local-lib:\n        specifier: workspace:*\n        version: link:../../packages/local-lib\n  packages/local-lib: {}\npackages:\n  external-fixture@1.0.0:\n    resolution: {integrity: " + integrity + "}\nsnapshots:\n  external-fixture@1.0.0: {}\n",
	}
	for _, scenario := range []struct {
		name, peerFields, importer string
		autoInstallPeers, wantPeer bool
		trials                     int
	}{
		{"auto-install-peers", `,"peerDependencies":{"external-fixture":"1.0.0"}`, "  packages/local-lib:\n    dependencies:\n      external-fixture:\n        specifier: 1.0.0\n        version: 1.0.0\n", true, true, 2},
		{"auto-install-peers-disabled", `,"peerDependencies":{"external-fixture":"1.0.0"}`, "  packages/local-lib: {}\n", false, false, 1},
		{"dev-dependency-overrides-peer", `,"peerDependencies":{"external-fixture":"^1.0.0"},"devDependencies":{"external-fixture":"1.0.0"}`, "  packages/local-lib:\n    devDependencies:\n      external-fixture:\n        specifier: 1.0.0\n        version: 1.0.0\n", true, true, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixtureFiles := maps.Clone(files)
			fixtureFiles["packages/local-lib/package.json"] = strings.Replace(fixtureFiles["packages/local-lib/package.json"], `"main":"index.js"`, `"main":"index.js"`+scenario.peerFields, 1)
			fixtureFiles["pnpm-lock.yaml"] = strings.Replace(fixtureFiles["pnpm-lock.yaml"], "  packages/local-lib: {}\n", scenario.importer, 1)
			if !scenario.autoInstallPeers {
				fixtureFiles["pnpm-workspace.yaml"] += "autoInstallPeers: false\n"
				fixtureFiles["pnpm-lock.yaml"] = strings.Replace(fixtureFiles["pnpm-lock.yaml"], "autoInstallPeers: true", "autoInstallPeers: false", 1)
			}
			peerCheck := "const fromLocal = require('node:module').createRequire(require.resolve('local-lib'));\n"
			if scenario.wantPeer {
				peerCheck += "assert.equal(fromLocal('external-fixture'), 'external-ok');\n"
			} else {
				peerCheck += "assert.throws(() => fromLocal.resolve('external-fixture'), {code: 'MODULE_NOT_FOUND'});\n"
			}
			fixtureFiles["apps/app/test.cjs"] = strings.Replace(fixtureFiles["apps/app/test.cjs"], "require('node:fs').writeFileSync", peerCheck+"require('node:fs').writeFileSync", 1)
			requests := 0
			broker, firstWorkspace := brokerFixture(t, func(request *http.Request) (*http.Response, error) {
				requests++
				if request.URL.String() != "https://registry.npmjs.org/external-fixture/-/external-fixture-1.0.0.tgz" {
					t.Fatalf("unexpected broker artifact request: %s", request.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			for trial := 0; trial < scenario.trials; trial++ {
				workspace := firstWorkspace
				if trial > 0 {
					var err error
					workspace, err = filepath.EvalSymlinks(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
				}
				for name, contents := range fixtureFiles {
					file := filepath.Join(workspace, filepath.FromSlash(name))
					if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
						t.Fatal(err)
					}
				}
				plan, err := projectprep.Detect(t.Context(), workspace, "apps/app")
				if err != nil || !plan.Ready || plan.WorkspaceRoot != "." || plan.Test.ID != "test.pnpm" || len(plan.Packages) != 1 {
					t.Fatalf("detector did not produce a runnable member plan: %+v err=%v", plan, err)
				}
				sources := plan.Sources
				if trial > 0 {
					sources = nil
				}
				stats, err := broker.Prepare(t.Context(), plan, sources, workspace, nil, func(ctx context.Context) error {
					current, err := projectprep.Detect(ctx, workspace, plan.Target)
					if err != nil {
						return err
					}
					if !current.Ready || current.Fingerprint != plan.Fingerprint {
						return errors.New("fixture preparation authority changed")
					}
					return nil
				})
				if err != nil || requests != 1 || stats.FetchedPackages != 1-trial || stats.CachedPackages != trial {
					t.Fatalf("trial %d preparation: %+v requests=%d err=%v", trial, stats, requests, err)
				}
				seed := filepath.Join(workspace, filepath.FromSlash(projectprep.PNPMSeedDirectory(plan.Target)))
				if got, err := os.ReadFile(filepath.Join(seed, "pnpm-lock.yaml")); err != nil || !bytes.Equal(got, plan.PNPMSeedLock) {
					t.Fatalf("broker did not write the detector's seed: %v", err)
				}
				store := filepath.Join(workspace, filepath.FromSlash(projectprep.PNPMStoreDirectory(plan.Target)))
				stage := filepath.Join(workspace, filepath.FromSlash(projectprep.StageDirectory(plan.Target)))
				// Match the production builder's argument order and keep every pnpm
				// directory under the target's stage. Only the CLI/guest paths differ.
				common := []string{"--offline", "--config.ignore-pnpmfile=true", "--config.manage-package-manager-versions=false", "--reporter=append-only",
					"--store-dir=" + store, "--config.cache-dir=" + filepath.Join(stage, "pnpm-cache"),
					"--config.state-dir=" + filepath.Join(stage, "pnpm-state"), "--config.global-dir=" + filepath.Join(stage, "pnpm-global")}
				run(t, seed, append([]string{"fetch", "--frozen-lockfile", "--ignore-scripts"}, common...)...)
				run(t, filepath.Join(workspace, filepath.FromSlash(plan.WorkspaceRoot)), append([]string{"install", "--frozen-lockfile", "--ignore-scripts"}, common...)...)
				for _, marker := range []string{"install-ran", "apps/app/install-ran", "packages/local-lib/install-ran", "apps/app/node_modules/external-fixture/install-ran"} {
					if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(marker))); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("install lifecycle script ran: %s (%v)", marker, err)
					}
				}
				testArgs := append(append([]string{}, common...), "--config.verify-deps-before-run=false")
				testArgs = append(testArgs, plan.Test.Arguments...)
				run(t, filepath.Join(workspace, filepath.FromSlash(plan.Target)), testArgs...)
				if got, err := os.ReadFile(filepath.Join(workspace, "apps", "app", "test-passed")); err != nil || string(got) != "external+workspace" {
					t.Fatalf("selected workspace member test did not finish: %q err=%v", got, err)
				}
				if got, err := os.ReadFile(filepath.Join(workspace, "pnpm-lock.yaml")); err != nil || string(got) != fixtureFiles["pnpm-lock.yaml"] {
					t.Fatalf("native commands changed the original lockfile: %q err=%v", got, err)
				}
				t.Logf("trial %d passed: fetched=%d cached=%d; original lock unchanged; lifecycle scripts skipped", trial, stats.FetchedPackages, stats.CachedPackages)
			}
		})
	}
	if _, err := os.Stat(networkLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native verifier attempted network access: %v", err)
	}
}
