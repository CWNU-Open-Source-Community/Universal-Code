package projectprep_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, contents := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func integrity(algorithm string, size int) string {
	return algorithm + "-" + base64.StdEncoding.EncodeToString(make([]byte, size))
}
func npmFiles() map[string]string {
	return map[string]string{
		"web/package.json":      `{"engines":{"node":">=24"},"packageManager":"npm@11.0.0","scripts":{"test":"echo untrusted; never executed"},"dependencies":{"is-number":"7.0.0"}}`,
		"web/package-lock.json": `{"lockfileVersion":3,"packages":{"":{"dependencies":{"is-number":"7.0.0"}},"node_modules/is-number":{"resolved":"https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz","integrity":"` + integrity("sha512", 64) + `"}}}`,
	}
}

func TestNPMPlanPinsSourcesCommandsAndPrivatePackages(t *testing.T) {
	root := fixture(t, npmFiles())
	plan, err := projectprep.Detect(context.Background(), root, "web")
	if err != nil || !plan.Ready || len(plan.Issues) != 0 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if plan.Manifest.Path != "web/package.json" || plan.Lockfiles[0].Path != "web/package-lock.json" || len(plan.Manifest.SHA256) != 64 || len(plan.Fingerprint) != 64 || plan.RequestedVersion != ">=24" {
		t.Fatalf("missing review identity: %#v", plan)
	}
	if !reflect.DeepEqual(plan.Sources, []string{"https://registry.npmjs.org"}) || len(plan.Packages) != 1 || plan.Packages[0].Kind != projectprep.KindNPMTarball || plan.Test.ID != "test.npm" {
		t.Fatalf("unexpected dependency plan: %#v", plan)
	}
	if !strings.Contains(plan.Install.Display, "--ignore-scripts") || !strings.Contains(plan.Install.Display, "--offline") {
		t.Fatal("install did not disclose fixed script/network behavior")
	}
	public, err := json.Marshal(plan)
	if err != nil || strings.Contains(string(public), "never executed") || strings.Contains(string(public), ".tgz") || strings.Contains(string(public), root) {
		t.Fatalf("private metadata leaked: %s %v", public, err)
	}
	again, _ := projectprep.Detect(context.Background(), root, "web")
	if !reflect.DeepEqual(plan, again) {
		t.Fatal("unchanged detection is nondeterministic")
	}
	files := npmFiles()
	files["web/package-lock.json"] = strings.Replace(files["web/package-lock.json"], "registry.npmjs.org", "packages.example.org", 1)
	changed, _ := projectprep.Detect(context.Background(), fixture(t, files), "web")
	if changed.Fingerprint == plan.Fingerprint || changed.CacheKey == plan.CacheKey {
		t.Fatal("changed download origin retained consent/cache identity")
	}
}

func TestNPMUnavailablePlansAreActionable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]string)
		want   string
	}{
		{"missing lock", func(f map[string]string) { delete(f, "web/package-lock.json") }, "missing"},
		{"null manifest", func(f map[string]string) { f["web/package.json"] = "null" }, "valid package manifest"},
		{"old lock", func(f map[string]string) {
			f["web/package-lock.json"] = strings.Replace(f["web/package-lock.json"], `"lockfileVersion":3`, `"lockfileVersion":1`, 1)
		}, "lockfileVersion"},
		{"weak integrity", func(f map[string]string) {
			f["web/package-lock.json"] = strings.Replace(f["web/package-lock.json"], integrity("sha512", 64), integrity("sha1", 20), 1)
		}, "SHA-256 or SHA-512"},
		{"credential URL", func(f map[string]string) {
			f["web/package-lock.json"] = strings.Replace(f["web/package-lock.json"], "https://registry", "https://user:password@registry", 1)
		}, "public HTTPS"},
		{"local URL", func(f map[string]string) {
			f["web/package-lock.json"] = strings.Replace(f["web/package-lock.json"], "https://registry.npmjs.org", "https://127.0.0.1", 1)
		}, "public HTTPS"},
		{"link", func(f map[string]string) {
			f["web/package-lock.json"] = strings.Replace(f["web/package-lock.json"], `"resolved":`, `"link":true,"resolved":`, 1)
		}, "link"},
		{"wrong manifest", func(f map[string]string) {
			f["web/package.json"] = strings.Replace(f["web/package.json"], "7.0.0", "6.0.0", 1)
		}, "differ"},
		{"pnpm", func(f map[string]string) { f["web/pnpm-lock.yaml"] = "lockfileVersion: '9.0'\n" }, "pnpm-lock.yaml"},
		{"workspace", func(f map[string]string) {
			f["web/package.json"] = strings.Replace(f["web/package.json"], `{"engines"`, `{"workspaces":["packages/*"],"engines"`, 1)
		}, "workspaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := npmFiles()
			tc.change(files)
			plan, err := projectprep.Detect(context.Background(), fixture(t, files), "web")
			if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), tc.want) {
				t.Fatalf("plan=%#v err=%v", plan, err)
			}
		})
	}
}

func goFiles() map[string]string {
	h1 := "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32))
	return map[string]string{
		"go.mod": "module example.org/app\n\ngo 1.26.9\nrequire example.org/Library v1.2.3\n",
		"go.sum": "example.org/Library v1.2.3 " + h1 + "\nexample.org/Library v1.2.3/go.mod " + h1 + "\nexample.org/transitive v2.0.0+incompatible/go.mod " + h1 + "\n",
	}
}

func TestGoPlanUsesOfficialEscapingAndModOnlyGraphEntries(t *testing.T) {
	plan, err := projectprep.Detect(context.Background(), fixture(t, goFiles()), "")
	if err != nil || !plan.Ready || plan.Target != "." || plan.RequestedVersion != "1.26.9" || len(plan.Packages) != 3 {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if plan.Packages[0].URL != "https://proxy.golang.org/example.org/!library/@v/v1.2.3.mod" || plan.Packages[2].Kind != projectprep.KindGoMod || plan.Test.ID != "test.go" {
		t.Fatalf("unexpected module downloads: %#v", plan.Packages)
	}
	if !reflect.DeepEqual(plan.Sources, []string{projectprep.GoProxyOrigin, projectprep.GoProxyArchiveOrigin}) {
		t.Fatalf("unexpected origins %#v", plan.Sources)
	}
}

func TestGoMissingUnsafeAndUnsupportedInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]string)
		want   string
	}{
		{"no sums", func(f map[string]string) { delete(f, "go.sum") }, "missing"},
		{"no zip", func(f map[string]string) { f["go.sum"] = strings.Join(strings.Split(f["go.sum"], "\n")[1:], "\n") }, "archive checksums"},
		{"bad checksum", func(f map[string]string) { f["go.sum"] = "example.org/Library v1.2.3 h1:bad\n" }, "invalid checksum"},
		{"bad version", func(f map[string]string) { f["go.sum"] = strings.Replace(f["go.sum"], "v1.2.3", "latest", 1) }, "invalid module"},
		{"local replacement", func(f map[string]string) { f["go.mod"] += "replace example.org/Library => ../Library\n" }, "local replace"},
		{"unknown directive", func(f map[string]string) { f["go.mod"] += "untrusted run-this\n" }, "unsupported"},
		{"workspace", func(f map[string]string) { f["go.work"] = "go 1.26.9\nuse .\n" }, "go.work"},
		{"mixed toolchains", func(f map[string]string) { f["Cargo.toml"] = "[package]\nname = \"example\"\n" }, "another dependency toolchain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := goFiles()
			tc.change(files)
			plan, err := projectprep.Detect(context.Background(), fixture(t, files), ".")
			if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), tc.want) {
				t.Fatalf("plan=%#v err=%v", plan, err)
			}
		})
	}
	plan, err := projectprep.Detect(context.Background(), fixture(t, map[string]string{"go.mod": "module example.org/empty\ngo 1.26.9\n"}), ".")
	if err != nil || !plan.Ready || len(plan.Packages) != 0 {
		t.Fatalf("dependency-free module blocked: %#v %v", plan, err)
	}
}

func TestDetectionBoundsPathsFilesAndCancellation(t *testing.T) {
	root := fixture(t, map[string]string{"Cargo.toml": "[package]\nname = \"example\"\n"})
	plan, err := projectprep.Detect(context.Background(), root, ".")
	if err != nil || plan.Ready || plan.Toolchain != "cargo" || !strings.Contains(strings.Join(plan.Issues, " "), "Cargo.lock is missing") {
		t.Fatalf("unsupported toolchain hidden: %#v %v", plan, err)
	}
	for _, target := range []string{"../outside", "C:/outside", "/outside", "web\\nested"} {
		if _, err := projectprep.Detect(context.Background(), root, target); err == nil {
			t.Fatal("unsafe target admitted", target)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := projectprep.Detect(ctx, root, "."); err != context.Canceled {
		t.Fatalf("cancel ignored: %v", err)
	}
	outside := fixture(t, map[string]string{"package.json": `{}`})
	if err := os.Symlink(filepath.Join(outside, "package.json"), filepath.Join(root, "package.json")); err == nil {
		plan, err = projectprep.Detect(context.Background(), root, ".")
		if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), "symlink") {
			t.Fatalf("outside symlink read: %#v %v", plan, err)
		}
	}
}
