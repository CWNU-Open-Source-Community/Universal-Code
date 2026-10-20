package projectprep_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

const bunManifestFixture = `{"name":"app","packageManager":"bun@1.4.3","dependencies":{"is-number":"7.0.0"},"scripts":{"test":"bun test"}}`
const bunLockFixture = `{
 // This is the actual Bun 1.4.3 text layout, including trailing commas.
 "lockfileVersion":2,"configVersion":1,
 "workspaces":{"":{"name":"app","dependencies":{"is-number":"7.0.0",},},},
 "packages":{"is-number":["is-number@7.0.0","",{},"sha512-41Cifkg6e8TylSpdtTpeLVMqvSBEVzTttHvERD741+pnZ8ANv0004MRL43QKPDlK9cGvNp6NZWZUBlbGXYxxng=="],},
}`

func TestBunTextLockPinsRegistryCacheAndManifest(t *testing.T) {
	root := fixture(t, map[string]string{"web/package.json": bunManifestFixture, "web/bun.lock": bunLockFixture})
	plan, err := projectprep.Detect(t.Context(), root, "web")
	if err != nil || !plan.Ready || plan.Ecosystem != "bun" || plan.RequestedVersion != "1.4.3" || len(plan.Packages) != 1 || plan.Packages[0].URL != "https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz" || plan.Packages[0].Kind != projectprep.KindBunTarball || plan.Test.ID != "test.bun" {
		t.Fatalf("plan=%+v %v", plan, err)
	}
	if !strings.HasSuffix(projectprep.BunCachePackageDirectory("web", "@scope/pkg", "1.2.3"), "/bun-cache/@scope/pkg@1.2.3@@@1") {
		t.Fatal("fixed Bun cache version or scoped name lost")
	}
	if err := os.WriteFile(filepath.Join(root, "web/package.json"), []byte(strings.Replace(bunManifestFixture, "bun test", "bun test changed", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := projectprep.Detect(t.Context(), root, "web")
	if err != nil || changed.Fingerprint == plan.Fingerprint {
		t.Fatal("manifest change reused consent", err)
	}
}

func TestBunRejectsUnsupportedLocksSourcesAndConfiguration(t *testing.T) {
	for _, test := range []struct{ name, manifest, lock, config, issue string }{
		{"version", strings.Replace(bunManifestFixture, "1.4.3", "1.4.2", 1), bunLockFixture, "", "bun@1.4.3"},
		{"root mismatch", strings.Replace(bunManifestFixture, "7.0.0", "^7.0.0", 1), bunLockFixture, "", "differs"},
		{"git", strings.ReplaceAll(bunManifestFixture, "7.0.0", "git:https://example.com/repo"), strings.ReplaceAll(bunLockFixture, "\"7.0.0\"", "\"git:https://example.com/repo\""), "", "non-registry"},
		{"workspace", strings.Replace(bunManifestFixture, "\"scripts\"", "\"workspaces\":[\"packages/*\"],\"scripts\"", 1), bunLockFixture, "", "workspaces"},
		{"custom registry", bunManifestFixture, strings.Replace(bunLockFixture, "\"is-number@7.0.0\",\"\"", "\"is-number@7.0.0\",\"https://private.example\"", 1), "", "default-registry"},
		{"tarball source", bunManifestFixture, strings.Replace(bunLockFixture, "is-number@7.0.0", "https://registry.npmjs.org/other.tgz", 1), "", "identity"},
		{"integrity", bunManifestFixture, strings.Replace(bunLockFixture, "sha512-", "sha1-", 1), "", "integrity"},
		{"legacy text version", bunManifestFixture, strings.Replace(bunLockFixture, "\"lockfileVersion\":2", "\"lockfileVersion\":1", 1), "", "lockfileVersion 2"},
		{"config", bunManifestFixture, bunLockFixture, "[install]\nregistry='https://example.com'\n", "unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{"package.json": test.manifest, "bun.lock": test.lock}
			if test.config != "" {
				files["bunfig.toml"] = test.config
			}
			plan, err := projectprep.Detect(t.Context(), fixture(t, files), ".")
			if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), test.issue) {
				t.Fatalf("plan=%+v %v", plan, err)
			}
		})
	}
	root := fixture(t, map[string]string{"package.json": bunManifestFixture, "bun.lockb": "legacy binary"})
	plan, err := projectprep.Detect(t.Context(), root, ".")
	if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), "binary bun.lockb") {
		t.Fatalf("legacy=%+v %v", plan, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := projectprep.Detect(ctx, root, "."); err != context.Canceled {
		t.Fatal("cancel ignored", err)
	}
}
