package projectprep_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
	"gopkg.in/yaml.v3"
)

func pnpmFiles() map[string]string {
	return map[string]string{
		"package.json":                    `{"private":true,"packageManager":"pnpm@10.28.1"}`,
		"pnpm-workspace.yaml":             "packages:\n  - 'apps/*'\n  - 'packages/**'\n  - '!packages/excluded'\n",
		"apps/web/package.json":           `{"name":"web","version":"1.0.0","scripts":{"test":"node test.cjs"},"dependencies":{"local-lib":"workspace:*","is-number":"7.0.0"}}`,
		"packages/local-lib/package.json": `{"name":"local-lib","version":"1.0.0","dependencies":{"is-number":"7.0.0"}}`,
		"packages/excluded/package.json":  `{"name":"excluded"}`,
		"pnpm-lock.yaml":                  "lockfileVersion: '9.0'\nsettings:\n  autoInstallPeers: true\n  excludeLinksFromLockfile: false\nimporters:\n  .: {}\n  apps/web:\n    dependencies:\n      local-lib:\n        specifier: workspace:*\n        version: link:../../packages/local-lib\n      is-number:\n        specifier: 7.0.0\n        version: 7.0.0\n  packages/local-lib:\n    dependencies:\n      is-number:\n        specifier: 7.0.0\n        version: 7.0.0\npackages:\n  is-number@7.0.0:\n    resolution: {integrity: " + integrity("sha512", 64) + "}\n    engines: {node: '>=0.12.0'}\nsnapshots:\n  is-number@7.0.0: {}\n",
	}
}

func TestPNPMWorkspaceSelectedMemberAndSeed(t *testing.T) {
	files := pnpmFiles()
	files["packages/local-lib/.npmrc"] = "audit=false\n"
	root := fixture(t, files)
	plan, err := projectprep.Detect(t.Context(), root, "apps/web")
	if err != nil || !plan.Ready {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if plan.Target != "apps/web" || plan.WorkspaceRoot != "." || plan.Ecosystem != "pnpm" || plan.Toolchain != "pnpm" || plan.RequestedVersion != projectprep.PNPMVersion || plan.Install.ID != "install.pnpm" || plan.Test.ID != "test.pnpm" {
		t.Fatalf("wrong selected project: %#v", plan)
	}
	if len(plan.Packages) != 1 || plan.Packages[0].ID != "is-number@7.0.0" || plan.Packages[0].Kind != projectprep.KindPNPMTarball || plan.Packages[0].URL != "https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz" {
		t.Fatalf("wrong packages: %#v", plan.Packages)
	}
	if !reflect.DeepEqual(plan.Sources, []string{"https://registry.npmjs.org"}) {
		t.Fatal(plan.Sources)
	}
	bound := map[string]bool{plan.Manifest.Path: true}
	for _, file := range plan.Lockfiles {
		if bound[file.Path] {
			t.Fatalf("duplicate binding %s", file.Path)
		}
		bound[file.Path] = true
	}
	for _, name := range []string{"package.json", "apps/web/package.json", "packages/local-lib/package.json", "packages/local-lib/.npmrc", "pnpm-workspace.yaml", "pnpm-lock.yaml"} {
		if !bound[name] {
			t.Fatalf("unbound %s", name)
		}
	}
	if bound["packages/excluded/package.json"] {
		t.Fatal("excluded package was bound as a member")
	}
	var original, seed map[string]any
	if yaml.Unmarshal([]byte(files["pnpm-lock.yaml"]), &original) != nil || yaml.Unmarshal(plan.PNPMSeedLock, &seed) != nil {
		t.Fatal("invalid seed YAML")
	}
	resolution := original["packages"].(map[string]any)["is-number@7.0.0"].(map[string]any)["resolution"].(map[string]any)
	resolution["tarball"] = "file:../pnpm-tarballs/" + projectprep.PNPMTarballFile("is-number@7.0.0")
	if !reflect.DeepEqual(original, seed) {
		t.Fatalf("seed changed more than tarball: %#v", seed)
	}
	public, _ := json.Marshal(plan)
	if strings.Contains(string(public), "sha512-") || strings.Contains(string(public), "file:../") || strings.Contains(string(public), "node test.cjs") {
		t.Fatalf("private plan leaked: %s", public)
	}
	again, _ := projectprep.Detect(t.Context(), root, "apps/web")
	if !reflect.DeepEqual(plan, again) {
		t.Fatal("detection is nondeterministic")
	}
	files["packages/local-lib/package.json"] = strings.Replace(files["packages/local-lib/package.json"], `"version":"1.0.0"`, `"version":"1.0.1"`, 1)
	changed, _ := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
	if changed.Fingerprint == plan.Fingerprint {
		t.Fatal("member change retained review fingerprint")
	}
	if projectprep.PNPMSeedDirectory(plan.Target) != projectprep.StageDirectory(plan.Target)+"/pnpm-seed" || projectprep.PNPMStoreDirectory(plan.Target) != projectprep.StageDirectory(plan.Target)+"/pnpm-store" {
		t.Fatal("stage paths changed")
	}
}

func standalonePNPMFiles() map[string]string {
	return map[string]string{"web/package.json": `{"packageManager":"pnpm@10.28.1","dependencies":{"@scope/pkg":"1.2.3"}}`, "web/pnpm-lock.yaml": "lockfileVersion: '9.0'\nimporters:\n  .:\n    dependencies:\n      '@scope/pkg': {specifier: 1.2.3, version: 1.2.3}\npackages:\n  '@scope/pkg@1.2.3':\n    resolution: {integrity: " + integrity("sha256", 32) + "}\nsnapshots:\n  '@scope/pkg@1.2.3': {}\n"}
}

func TestPNPMStandaloneAndRegistryRouting(t *testing.T) {
	files := standalonePNPMFiles()
	files["web/.npmrc"] = "@scope:registry=https://packages.example.org/npm/\n"
	plan, err := projectprep.Detect(t.Context(), fixture(t, files), "web")
	if err != nil || !plan.Ready || plan.WorkspaceRoot != "web" || plan.Packages[0].URL != "https://packages.example.org/npm/@scope/pkg/-/pkg-1.2.3.tgz" {
		t.Fatalf("%#v %v", plan, err)
	}
	files["web/pnpm-lock.yaml"] = strings.Replace(files["web/pnpm-lock.yaml"], "resolution: {integrity:", "resolution: {tarball: https://cdn.example.org/exact.tgz, integrity:", 1)
	plan, err = projectprep.Detect(t.Context(), fixture(t, files), "web")
	if err != nil || !plan.Ready || plan.Packages[0].URL != "https://cdn.example.org/exact.tgz" || !reflect.DeepEqual(plan.Sources, []string{"https://cdn.example.org"}) {
		t.Fatalf("%#v %v", plan, err)
	}
}

func TestPNPMRejectsUnsafeOrInconsistentInputs(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(map[string]string)
	}{
		{"missing lock", "missing", func(f map[string]string) { delete(f, "pnpm-lock.yaml") }},
		{"unsupported version", "pnpm@10.28.1", func(f map[string]string) {
			f["package.json"] = strings.Replace(f["package.json"], "10.28.1", "11.1.3", 1)
		}},
		{"old lock", "lockfileVersion 9.0", func(f map[string]string) {
			f["pnpm-lock.yaml"] = strings.Replace(f["pnpm-lock.yaml"], "'9.0'", "'6.0'", 1)
		}},
		{"manifest mismatch", "differs", func(f map[string]string) {
			f["apps/web/package.json"] = strings.Replace(f["apps/web/package.json"], "7.0.0", "6.0.0", 1)
		}},
		{"extra importer", "not a declared workspace", func(f map[string]string) {
			f["pnpm-lock.yaml"] = strings.Replace(f["pnpm-lock.yaml"], "  .: {}", "  .: {}\n  stale: {}", 1)
		}},
		{"missing snapshot", "missing", func(f map[string]string) {
			f["pnpm-lock.yaml"] = strings.Replace(f["pnpm-lock.yaml"], "  is-number@7.0.0: {}", "  other@1.0.0: {}", 1)
		}},
		{"escaping link", "declared workspace", func(f map[string]string) {
			f["pnpm-lock.yaml"] = strings.Replace(f["pnpm-lock.yaml"], "../../packages/local-lib", "../../../outside", 1)
		}},
		{"wrong link", "package name", func(f map[string]string) {
			f["pnpm-lock.yaml"] = strings.Replace(f["pnpm-lock.yaml"], "../../packages/local-lib", "../../apps/web", 1)
		}},
		{"local package", "supported registry", func(f map[string]string) {
			f["pnpm-lock.yaml"] = strings.ReplaceAll(f["pnpm-lock.yaml"], "is-number@7.0.0", "is-number@file:../outside")
		}},
		{"weak integrity", "SHA-256 or SHA-512", func(f map[string]string) {
			f["pnpm-lock.yaml"] = strings.Replace(f["pnpm-lock.yaml"], integrity("sha512", 64), integrity("sha1", 20), 1)
		}},
		{"private URL", "public HTTPS", func(f map[string]string) {
			f["pnpm-lock.yaml"] = strings.Replace(f["pnpm-lock.yaml"], "resolution: {integrity:", "resolution: {tarball: https://127.0.0.1/pkg.tgz, integrity:", 1)
		}},
		{"registry env", "explicit public", func(f map[string]string) { f[".npmrc"] = "registry=${NPM_REGISTRY}\n" }},
		{"escaping pattern", "relative paths", func(f map[string]string) { f["pnpm-workspace.yaml"] = "packages: ['../outside']\n" }},
		{"duplicate key", "lockfileVersion", func(f map[string]string) { f["pnpm-lock.yaml"] += "packages: {}\n" }},
		{"alias YAML", "lockfileVersion", func(f map[string]string) { f["pnpm-lock.yaml"] += "extra: &x {}\nother: *x\n" }},
		{"nested lock", "conflicts", func(f map[string]string) {
			f["apps/web/pnpm-lock.yaml"] = "lockfileVersion: '9.0'\nimporters: {.: {}}\n"
		}},
		{"conflicting manager", "conflicts", func(f map[string]string) { f["apps/web/package-lock.json"] = "{}" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := pnpmFiles()
			tc.change(files)
			plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
			if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), tc.want) {
				t.Fatalf("%#v err=%v", plan, err)
			}
		})
	}
}

func TestPNPMDiscoveryDoesNotCrossRunRoot(t *testing.T) {
	outer := fixture(t, pnpmFiles())
	plan, err := projectprep.Detect(t.Context(), filepath.Join(outer, "apps", "web"), ".")
	if err != nil || plan.Ready || plan.Ecosystem == "pnpm" {
		t.Fatalf("ancestor outside Run selected: %#v %v", plan, err)
	}
}

func TestPNPMWorkspaceSymlinkRejected(t *testing.T) {
	files := pnpmFiles()
	delete(files, "packages/local-lib/package.json")
	root := fixture(t, files)
	outside := fixture(t, map[string]string{"package.json": `{"name":"local-lib","version":"1.0.0"}`})
	link := filepath.Join(root, "packages", "local-lib")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("create test junction: %v %s", err, output)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	plan, err := projectprep.Detect(context.Background(), root, "apps/web")
	if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), "symlink") {
		t.Fatalf("%#v %v", plan, err)
	}
}

func TestPNPMBoundedWorkspaceDiscovery(t *testing.T) {
	files := pnpmFiles()
	for i := 0; i < 61; i++ {
		files[fmt.Sprintf("packages/p%d/package.json", i)] = fmt.Sprintf(`{"name":"p%d","version":"1.0.0"}`, i)
	}
	plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
	if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), "60 members") || len(plan.Lockfiles) > 64 {
		t.Fatalf("%#v %v", plan, err)
	}
}

func TestPNPMPeerAndTransitiveGraphRetained(t *testing.T) {
	files := standalonePNPMFiles()
	files["web/pnpm-lock.yaml"] = strings.Replace(files["web/pnpm-lock.yaml"], "version: 1.2.3}", "version: 1.2.3(peer@2.0.0)}", 1)
	files["web/pnpm-lock.yaml"] = strings.Replace(files["web/pnpm-lock.yaml"], "snapshots:\n  '@scope/pkg@1.2.3': {}", "  peer@2.0.0:\n    resolution: {integrity: "+integrity("sha512", 64)+"}\nsnapshots:\n  '@scope/pkg@1.2.3(peer@2.0.0)':\n    dependencies: {peer: 2.0.0}\n  peer@2.0.0: {}", 1)
	plan, err := projectprep.Detect(t.Context(), fixture(t, files), "web")
	if err != nil || !plan.Ready || len(plan.Packages) != 2 || !strings.Contains(string(plan.PNPMSeedLock), "@scope/pkg@1.2.3(peer@2.0.0)") {
		t.Fatalf("%#v %v", plan, err)
	}
	files["web/pnpm-lock.yaml"] = strings.Replace(files["web/pnpm-lock.yaml"], "dependencies: {peer: 2.0.0}", "dependencies: {peer: 3.0.0}", 1)
	plan, err = projectprep.Detect(t.Context(), fixture(t, files), "web")
	if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), "missing dependency peer") {
		t.Fatalf("%#v %v", plan, err)
	}
}

func TestPNPMConfigBindingsStayWithinPublicFileLimit(t *testing.T) {
	files := pnpmFiles()
	lock := files["pnpm-lock.yaml"]
	for i := 0; i < 13; i++ {
		directory := fmt.Sprintf("packages/p%d", i)
		files[directory+"/package.json"] = fmt.Sprintf(`{"name":"p%d","version":"1.0.0"}`, i)
		for _, name := range []string{".npmrc", ".nvmrc", ".node-version", ".tool-versions"} {
			files[directory+"/"+name] = ""
		}
		lock = strings.Replace(lock, "  .: {}", "  .: {}\n  "+directory+": {}", 1)
	}
	files["pnpm-lock.yaml"] = lock
	plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
	if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), "64 bound") || len(plan.Lockfiles) > 63 {
		t.Fatalf("%#v %v", plan, err)
	}
}

func TestPNPMCorepackExactVersionIntegritySuffix(t *testing.T) {
	for _, tc := range []struct {
		manager string
		ready   bool
	}{
		{"pnpm@10.28.1+sha512." + strings.Repeat("a1", 64), true},
		{"pnpm@10.28.1+sha224." + strings.Repeat("ab", 28), true},
		{"pnpm@10.28.1+sha256." + strings.Repeat("ab", 32), true},
		{"pnpm@10.28.1+sha384." + strings.Repeat("ab", 48), true},
		{"pnpm@10.28.2+sha512." + strings.Repeat("ab", 64), false},
		{"pnpm@10.28.1+sha512." + strings.Repeat("z", 128), false},
		{"pnpm@10.28.1+sha512." + strings.Repeat("a", 127), false},
		{"pnpm@10.28.1+sha512." + strings.Repeat("a", 128) + "+extra", false},
		{"pnpm@10.28.1+file./runtime.js", false},
	} {
		files := pnpmFiles()
		files["package.json"] = fmt.Sprintf(`{"private":true,"packageManager":%q}`, tc.manager)
		plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
		if err != nil || plan.Ready != tc.ready || plan.RequestedVersion != projectprep.PNPMVersion {
			t.Fatalf("manager=%s plan=%#v err=%v", tc.manager, plan, err)
		}
	}
}

func TestPNPMPatchesAreExplicitlyUnavailable(t *testing.T) {
	for _, location := range []string{"lock", "workspace", "manifest", "npmrc"} {
		t.Run(location, func(t *testing.T) {
			files := pnpmFiles()
			switch location {
			case "lock":
				files["pnpm-lock.yaml"] += "patchedDependencies:\n  is-number@7.0.0:\n    hash: abcdef\n    path: patches/is-number.patch\n"
			case "workspace":
				files["pnpm-workspace.yaml"] += "patchedDependencies:\n  is-number@7.0.0: patches/is-number.patch\n"
			case "manifest":
				files["package.json"] = `{"packageManager":"pnpm@10.28.1","pnpm":{"patchedDependencies":{"is-number@7.0.0":"patches/is-number.patch"}}}`
			case "npmrc":
				files[".npmrc"] = "patched-dependencies[is-number@7.0.0]=patches/is-number.patch\n"
			}
			plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
			if err != nil || plan.Ready || len(plan.PNPMSeedLock) > 0 || !strings.Contains(strings.Join(plan.Issues, " "), "patch files are not staged") {
				t.Fatalf("%#v %v", plan, err)
			}
		})
	}
}

func TestPNPMRejectsConfigurationThatChangesReviewedInputs(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"modulesDir", "custom_modules"}, {"virtualStoreDir", "../other"}, {"globalVirtualStoreDir", "../other"},
		{"enableGlobalVirtualStore", "true"}, {"lockfileDir", "../other"}, {"dir", "../other"}, {"workspaceDir", "../other"},
		{"global", "true"}, {"sharedWorkspaceLockfile", "false"}, {"nodeOptions", "--require=./unreviewed.cjs"},
		{"useNodeVersion", "20.0.0"}, {"executionEnv", "{nodeVersion: 20.0.0}"}, {"scriptShell", "./unreviewed.sh"},
		{"shellEmulator", "true"}, {"configDependencies", "{some-config: 1.0.0}"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			files := pnpmFiles()
			files["pnpm-workspace.yaml"] += tc.key + ": " + tc.value + "\n"
			plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
			if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), tc.key+" changes") {
				t.Fatalf("%#v %v", plan, err)
			}
		})
	}
	for _, config := range []string{"node-options=--require=./unreviewed.cjs\n", "lockfile-dir=../other\n", "config-dependencies[plugin]=1.0.0\n"} {
		files := pnpmFiles()
		files[".npmrc"] = config
		plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
		if err != nil || plan.Ready {
			t.Fatalf("config=%s plan=%#v %v", config, plan, err)
		}
	}
	files := pnpmFiles()
	files["package.json"] = `{"pnpm":{"executionEnv":{"nodeVersion":"20.0.0"}}}`
	plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
	if err != nil || plan.Ready {
		t.Fatalf("manifest executionEnv: %#v %v", plan, err)
	}
}

func TestPNPMDefaultLayoutAndPinnedCacheSettingsRemainUsable(t *testing.T) {
	files := pnpmFiles()
	files["pnpm-workspace.yaml"] += "modulesDir: node_modules\nvirtualStoreDir: node_modules/.pnpm\nenableGlobalVirtualStore: false\nglobal: false\nsharedWorkspaceLockfile: true\nshellEmulator: false\nnodeOptions: ''\nconfigDependencies: {}\npatchedDependencies: {}\ncacheDir: ../runtime-overrides-this\nstateDir: ../runtime-overrides-this\nglobalDir: ../runtime-overrides-this\n"
	files[".npmrc"] = "modules-dir=node_modules\nvirtual-store-dir=node_modules/.pnpm\nenable-global-virtual-store=false\nshared-workspace-lockfile=true\n"
	plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
	if err != nil || !plan.Ready {
		t.Fatalf("%#v %v", plan, err)
	}
}

type pnpmCancelContext struct {
	context.Context
	cancel        context.CancelFunc
	checks, after int
}

func (c *pnpmCancelContext) Err() error {
	c.checks++
	if c.after > 0 && c.checks >= c.after {
		c.cancel()
	}
	return c.Context.Err()
}

func TestPNPMCancellationDuringDiscoveryReturnsContextError(t *testing.T) {
	root := fixture(t, pnpmFiles())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	count := &pnpmCancelContext{Context: ctx, cancel: cancel}
	if _, err := projectprep.Detect(count, root, "apps/web"); err != nil {
		t.Fatal(err)
	}
	for _, after := range []int{1, count.checks / 2, count.checks - 1} {
		ctx, cancel := context.WithCancel(t.Context())
		_, err := projectprep.Detect(&pnpmCancelContext{Context: ctx, cancel: cancel, after: after}, root, "apps/web")
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation after check %d returned %v", after, err)
		}
	}
}

func TestPNPMRejectsAlternateManifestsDiscoveredByNativeWorkspaceFinder(t *testing.T) {
	for _, name := range []string{"package.yaml", "package.json5"} {
		for _, directory := range []string{".", "packages/hidden", "packages/local-lib"} {
			t.Run(directory+"/"+name, func(t *testing.T) {
				files := pnpmFiles()
				filename := filepath.ToSlash(filepath.Join(directory, name))
				files[filename] = "name: hidden\nversion: 1.0.0\nscripts:\n  postinstall: node unexpected.cjs\n"
				if name == "package.json5" {
					files[filename] = "{name: 'hidden', version: '1.0.0', scripts: {postinstall: 'node unexpected.cjs'}}"
				}
				plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
				if err != nil || plan.Ready || len(plan.PNPMSeedLock) > 0 || !strings.Contains(strings.Join(plan.Issues, " "), filename+" is an unsupported pnpm manifest") {
					t.Fatalf("%#v %v", plan, err)
				}
			})
		}
	}
	files := pnpmFiles()
	files["packages/excluded/package.yaml"] = "name: excluded\n"
	plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
	if err != nil || !plan.Ready {
		t.Fatalf("excluded alternate manifest changed the selected workspace: %#v %v", plan, err)
	}
}

func TestPNPMPeerDependenciesMatchNativeImporterGroups(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, group, specifier, workspace, npmrc string
		auto, ready                                        bool
	}{
		{name: "default auto peers", manifest: `"peerDependencies":{"is-number":"7.0.0"}`, group: "dependencies", specifier: "7.0.0", auto: true, ready: true},
		{name: "disabled YAML", manifest: `"peerDependencies":{"is-number":"7.0.0"}`, workspace: "autoInstallPeers: false\n", auto: false, ready: true},
		{name: "disabled npmrc", manifest: `"peerDependencies":{"is-number":"7.0.0"}`, npmrc: "auto-install-peers=false\n", auto: false, ready: true},
		{name: "YAML overrides npmrc", manifest: `"peerDependencies":{"is-number":"7.0.0"}`, group: "dependencies", specifier: "7.0.0", workspace: "autoInstallPeers: true\n", npmrc: "auto-install-peers=false\n", auto: true, ready: true},
		{name: "prod overrides peer", manifest: `"peerDependencies":{"is-number":"^6.0.0"},"dependencies":{"is-number":"7.0.0"}`, group: "dependencies", specifier: "7.0.0", auto: true, ready: true},
		{name: "dev overrides peer", manifest: `"peerDependencies":{"is-number":"^6.0.0"},"devDependencies":{"is-number":"7.0.0"}`, group: "devDependencies", specifier: "7.0.0", auto: true, ready: true},
		{name: "optional overrides all", manifest: `"peerDependencies":{"is-number":"^6.0.0"},"dependencies":{"is-number":"^7.0.0"},"devDependencies":{"is-number":"~7.0.0"},"optionalDependencies":{"is-number":"7.0.0"}`, group: "optionalDependencies", specifier: "7.0.0", auto: true, ready: true},
		{name: "prod overrides dev", manifest: `"peerDependencies":{"is-number":"^6.0.0"},"dependencies":{"is-number":"7.0.0"},"devDependencies":{"is-number":"~7.0.0"}`, group: "dependencies", specifier: "7.0.0", auto: true, ready: true},
		{name: "missing installed peer", manifest: `"peerDependencies":{"is-number":"7.0.0"}`, auto: true, ready: false},
		{name: "stale auto setting", manifest: `"peerDependencies":{"is-number":"7.0.0"}`, workspace: "autoInstallPeers: false\n", auto: true, ready: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := pnpmFiles()
			files["packages/local-lib/package.json"] = `{"name":"local-lib","version":"1.0.0",` + tc.manifest + `}`
			files["pnpm-workspace.yaml"] += tc.workspace
			if tc.npmrc != "" {
				files[".npmrc"] = tc.npmrc
			}
			importer := "  packages/local-lib: {}\n"
			if tc.group != "" {
				importer = "  packages/local-lib:\n    " + tc.group + ":\n      is-number:\n        specifier: " + tc.specifier + "\n        version: 7.0.0\n"
			}
			files["pnpm-lock.yaml"] = strings.Replace(files["pnpm-lock.yaml"], "  packages/local-lib:\n    dependencies:\n      is-number:\n        specifier: 7.0.0\n        version: 7.0.0\n", importer, 1)
			if !tc.auto {
				files["pnpm-lock.yaml"] = strings.Replace(files["pnpm-lock.yaml"], "autoInstallPeers: true", "autoInstallPeers: false", 1)
			}
			plan, err := projectprep.Detect(t.Context(), fixture(t, files), "apps/web")
			if err != nil || plan.Ready != tc.ready {
				t.Fatalf("%#v %v", plan, err)
			}
		})
	}
}
