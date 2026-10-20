package projectprep_test

import (
	"context"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

func yarnFiles() map[string]string {
	return map[string]string{
		"package.json": `{"packageManager":"yarn@1.22.22","scripts":{"test":"node test.js"},"dependencies":{"a":"^1.0.0","@example/b":"2.0.0"}}`,
		"yarn.lock": "# yarn lockfile v1\n\n" +
			"a@^1.0.0, a@1.0.0:\n  version \"1.0.0\"\n  resolved \"https://registry.yarnpkg.com/a/-/a-1.0.0.tgz#legacy\"\n  integrity " + integrity("sha512", 64) + "\n\n" +
			"\"@example/b@2.0.0\":\n  version \"2.0.0\"\n  resolved \"https://registry.yarnpkg.com/@example/b/-/b-2.0.0.tgz\"\n  integrity " + integrity("sha256", 32) + "\n  dependencies:\n    a \"1.0.0\"\n",
	}
}

func TestYarnClassicPlanBindsRegistryEntries(t *testing.T) {
	plan, err := projectprep.Detect(context.Background(), fixture(t, yarnFiles()), ".")
	if err != nil || !plan.Ready || plan.Ecosystem != "yarn" || plan.RequestedVersion != projectprep.YarnVersion || len(plan.Packages) != 2 || plan.Test.ID != "test.yarn" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for _, pkg := range plan.Packages {
		if strings.Contains(pkg.URL, "#") || pkg.Kind != projectprep.KindYarnTarball {
			t.Fatalf("unverified archive identity: %+v", pkg)
		}
		filename, err := projectprep.YarnTarballFile(pkg.URL)
		if err != nil || (filename != "a-1.0.0.tgz" && filename != "@example-b-2.0.0.tgz") {
			t.Fatalf("mirror path %q %v", filename, err)
		}
	}
}

func TestYarnRejectsUnsupportedOrUnboundLockContent(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]string)
	}{
		{"modern", func(f map[string]string) {
			f["package.json"] = strings.ReplaceAll(f["package.json"], "1.22.22", "4.9.2")
		}},
		{"workspace", func(f map[string]string) {
			f["package.json"] = strings.Replace(f["package.json"], "{", `{"workspaces":["packages/*"],`, 1)
		}},
		{"missing lock", func(f map[string]string) { delete(f, "yarn.lock") }},
		{"conflicting lock", func(f map[string]string) { f["package-lock.json"] = "{}" }},
		{"duplicate entry", func(f map[string]string) { f["yarn.lock"] += "a@1.0.0:\n" }},
		{"duplicate field", func(f map[string]string) { f["yarn.lock"] += "  version \"3.0.0\"\n" }},
		{"missing transitive", func(f map[string]string) {
			f["yarn.lock"] = strings.Replace(f["yarn.lock"], `a "1.0.0"`, `a "9.0.0"`, 1)
		}},
		{"missing direct", func(f map[string]string) {
			f["package.json"] = strings.Replace(f["package.json"], "^1.0.0", "^9.0.0", 1)
		}},
		{"weak integrity", func(f map[string]string) {
			f["yarn.lock"] = strings.Replace(f["yarn.lock"], integrity("sha512", 64), integrity("sha1", 20), 1)
		}},
		{"private source", func(f map[string]string) {
			f["yarn.lock"] = strings.ReplaceAll(f["yarn.lock"], "registry.yarnpkg.com", "127.0.0.1")
		}},
		{"git", func(f map[string]string) {
			f["yarn.lock"] = strings.ReplaceAll(f["yarn.lock"], "https://registry.yarnpkg.com/a/-/a-1.0.0.tgz#legacy", "git+https://example.com/repo.git")
		}},
		{"unknown grammar", func(f map[string]string) { f["yarn.lock"] += "  scripts: true\n" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := yarnFiles()
			test.change(files)
			plan, err := projectprep.Detect(context.Background(), fixture(t, files), ".")
			if err != nil || plan.Ready || len(plan.Issues) == 0 {
				t.Fatalf("unsupported plan accepted: %+v %v", plan, err)
			}
		})
	}
}

func TestYarnMirrorRejectsPathIndirection(t *testing.T) {
	for _, raw := range []string{"https://example.com/a/../x.tgz", "https://example.com/a/-/%2e%2e%2fx.tgz", "https://example.com/a/-/x:1.tgz", "https://example.com/download.tgz"} {
		if name, err := projectprep.YarnTarballFile(raw); err == nil {
			t.Fatalf("unsafe mirror %s -> %s", raw, name)
		}
	}
}
