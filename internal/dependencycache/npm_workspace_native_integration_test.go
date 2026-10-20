package dependencycache_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/dependencycache"
	"cyberagent-workbench/internal/projectprep"
)

// Opt in with explicit native Node/npm paths. The real broker verifies one
// public, integrity-locked package; npm and its test subprocess stay offline.
// TRAVERSE_NPM_TEST_TARBALL supplies a local cache artifact for offline machines.
// This verifier never substitutes host execution in the product runtime.
func TestNPMNativeWorkspacePreparation(t *testing.T) {
	node, cli := os.Getenv("TRAVERSE_NPM_TEST_NODE"), os.Getenv("TRAVERSE_NPM_TEST_CLI")
	if node == "" || cli == "" {
		t.Skip("set TRAVERSE_NPM_TEST_NODE and TRAVERSE_NPM_TEST_CLI to verify real npm workspace preparation")
	}
	for _, tool := range []string{node, cli} {
		if info, err := os.Stat(tool); !filepath.IsAbs(tool) || err != nil || !info.Mode().IsRegular() {
			t.Fatalf("explicit native tool required: %s %v", tool, err)
		}
	}
	tools := t.TempDir()
	preload := filepath.Join(tools, "deny-network.cjs")
	if err := os.WriteFile(preload, []byte(`const deny=()=>{throw new Error('NATIVE_NPM_NETWORK_DENIED')};
const net=require('node:net');net.connect=net.createConnection=net.Socket.prototype.connect=deny;
require('node:tls').connect=deny;require('node:dns').lookup=deny;
for(const name of ['node:http','node:https']){const m=require(name);m.get=m.request=deny;}
globalThis.fetch=deny;require('node:module').syncBuiltinESMExports();`), 0600); err != nil {
		t.Fatal(err)
	}
	userConfig, globalConfig := filepath.Join(tools, "user.npmrc"), filepath.Join(tools, "global.npmrc")
	for _, file := range []string{userConfig, globalConfig} {
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	broker, err := dependencycache.New(filepath.Join(tools, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := map[string]string{
		"repo/package.json":                 `{"name":"root","private":true,"workspaces":["apps/*","packages/*"],"scripts":{"postinstall":"node -e \"require('fs').writeFileSync('script-ran','bad')\""}}`,
		"repo/apps/web/package.json":        `{"name":"web","version":"1.0.0","scripts":{"test":"node test.cjs","postinstall":"node -e \"require('fs').writeFileSync('script-ran','bad')\""},"dependencies":{"is-number":"7.0.0","shared":"1.0.0"}}`,
		"repo/apps/web/test.cjs":            `const assert=require('node:assert/strict');assert.equal(require('shared'),'workspace');assert.equal(require('is-number')('42'),true);const cp=require('node:child_process').spawnSync('shared-tool',[],{shell:true,encoding:'utf8'});assert.equal(cp.status,0,cp.stderr);assert.match(cp.stdout,/workspace-bin/);console.log('offline external+workspace+bin passed');`,
		"repo/packages/shared/package.json": `{"name":"shared","version":"1.0.0","main":"index.cjs","bin":{"shared-tool":"cli.cjs"},"scripts":{"postinstall":"node -e \"require('fs').writeFileSync('script-ran','bad')\""}}`,
		"repo/packages/shared/index.cjs":    `module.exports='workspace';`,
		"repo/packages/shared/cli.cjs":      `console.log('workspace-bin');`,
		"repo/package-lock.json":            `{"name":"root","lockfileVersion":3,"packages":{"":{"name":"root","workspaces":["apps/*","packages/*"],"hasInstallScript":true},"apps/web":{"version":"1.0.0","hasInstallScript":true,"dependencies":{"is-number":"7.0.0","shared":"1.0.0"}},"packages/shared":{"version":"1.0.0","hasInstallScript":true,"bin":{"shared-tool":"cli.cjs"}},"node_modules/web":{"link":true,"resolved":"apps/web"},"node_modules/shared":{"link":true,"resolved":"packages/shared"},"node_modules/is-number":{"version":"7.0.0","resolved":"https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz","integrity":"sha512-41Cifkg6e8TylSpdtTpeLVMqvSBEVzTttHvERD741+pnZ8ANv0004MRL43QKPDlK9cGvNp6NZWZUBlbGXYxxng=="}}}`,
	}
	for index, target := range []string{"repo/apps/web", "repo"} {
		root := filepath.Join(t.TempDir(), "workspace 中文")
		for name, body := range fixture {
			file := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := projectprep.Detect(t.Context(), root, target)
		if err != nil || !plan.Ready {
			t.Fatalf("plan=%+v %v", plan, err)
		}
		authority := func(context.Context) error { return nil }
		if index == 0 {
			stats, err := broker.Prepare(t.Context(), plan, nil, root, nil, authority)
			if !errors.Is(err, dependencycache.ErrSourceDenied) || stats.Bytes != 0 {
				t.Fatalf("unapproved source: %+v %v", stats, err)
			}
			// An explicitly supplied tarball permits offline verification when
			// the machine cannot reach a registry through the product transport.
			// The broker still validates the exact lock integrity on this cache hit.
			if archive := os.Getenv("TRAVERSE_NPM_TEST_TARBALL"); archive != "" {
				body, err := os.ReadFile(archive)
				if err != nil {
					t.Fatal(err)
				}
				pkg := plan.Packages[0]
				key := sha256.Sum256([]byte(pkg.Kind + "\x00" + pkg.ModulePath + "\x00" + pkg.Version + "\x00" + pkg.Integrity))
				if err := os.WriteFile(filepath.Join(tools, "cache", hex.EncodeToString(key[:])+".blob"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		sources := []string{"https://registry.npmjs.org"}
		if index > 0 || os.Getenv("TRAVERSE_NPM_TEST_TARBALL") != "" {
			sources = nil
		}
		stats, err := broker.Prepare(t.Context(), plan, sources, root, nil, authority)
		if err != nil || index > 0 && stats.CachedPackages != 1 {
			t.Fatalf("broker %+v %v", stats, err)
		}
		adapter := commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeDockerSandboxBackend, "native-verifier", "generation")
		install, err := application.PrepareProjectPreparationCommand(plan, adapter, root, false)
		if err != nil {
			t.Fatal(err)
		}
		run := func(arguments []string, cwd string) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			args := append([]string{"--require", preload, cli}, arguments[1:]...)
			command := exec.CommandContext(ctx, node, args...)
			command.Dir = filepath.Join(root, filepath.FromSlash(cwd))
			command.Env = append(os.Environ(), "PATH="+filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"), "NODE_OPTIONS=--require="+preload, "NPM_CONFIG_USERCONFIG="+userConfig, "NPM_CONFIG_GLOBALCONFIG="+globalConfig, "NPM_CONFIG_OFFLINE=true", "npm_config_fetch_retries=0")
			output, err := command.CombinedOutput()
			t.Logf("native npm cwd=%s args=%v\n%s", cwd, arguments[1:], output)
			if err != nil {
				t.Fatalf("native npm: %v", err)
			}
		}
		run(install.Arguments, install.WorkingDirectory)
		if err := dependencycache.FinalizeNPMWorkspace(t.Context(), root, plan, runtime.GOOS != "windows"); err != nil {
			t.Fatal(err)
		}
		wrapperName := "shared-tool"
		if runtime.GOOS == "windows" {
			wrapperName += ".cmd"
		}
		if wrapper, err := os.ReadFile(filepath.Join(root, "repo/node_modules/.bin", wrapperName)); err != nil {
			t.Fatalf("workspace command wrapper missing: %v", err)
		} else {
			t.Logf("workspace wrapper: %s", wrapper)
		}
		memberPlan, err := projectprep.Detect(t.Context(), root, "repo/apps/web")
		if err != nil || !memberPlan.Ready {
			t.Fatalf("installed plan=%+v %v", memberPlan, err)
		}
		testSpec, err := application.PrepareProjectPreparationTestCommand(memberPlan, adapter, root)
		if err != nil {
			t.Fatal(err)
		}
		run(testSpec.Arguments, testSpec.WorkingDirectory)
		for name, before := range fixture {
			after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil || string(after) != before {
				t.Fatalf("fixture changed %s: %v", name, err)
			}
		}
		for _, dir := range []string{"repo", "repo/apps/web", "repo/packages/shared"} {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), "script-ran")); !os.IsNotExist(err) {
				t.Fatalf("unapproved script ran in %s", dir)
			}
		}
		if index > 0 && stats.Bytes != 0 {
			t.Fatal("cache reuse downloaded new bytes")
		}
	}
}

func TestFinalizeNPMWorkspaceRejectsChangedOrUndeclaredLinks(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "packages", "shared")
	if err := os.MkdirAll(member, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "package.json"), []byte(`{"name":"shared"}`), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "node_modules", "shared")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, member).CombinedOutput(); err != nil {
			t.Skipf("junction creation unavailable: %v %s", err, output)
		}
	} else if err := os.Symlink(member, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	plan := projectprep.Plan{WorkspaceRoot: ".", NPMWorkspaceLinks: map[string]string{"node_modules/shared": "packages/shared"}}
	if err := dependencycache.FinalizeNPMWorkspace(t.Context(), root, plan, false); err != nil {
		t.Fatal(err)
	}
	if err := dependencycache.FinalizeNPM(t.Context(), root, ".", false); err == nil {
		t.Fatal("standalone finalization accepted an unreviewed workspace link")
	}
	plan.NPMWorkspaceLinks["node_modules/shared"] = "packages/unreviewed"
	if err := dependencycache.FinalizeNPMWorkspace(t.Context(), root, plan, false); err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("changed link accepted: %v", err)
	}
}
