package projectprep_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

const cargoLockFixture = `version = 4
[[package]]
name = 'app'
version = '0.1.0'
[[package]]
name = 'itoa'
version = '1.0.15'
source = 'registry+https://github.com/rust-lang/crates.io-index'
checksum = '4a5f13b858c8d314ee3e8f639011f7ccefe71f97f96e50151fb991f267928e2c'
`

func TestCargoPreparationBindsExplicitRootMembersAndArtifacts(t *testing.T) {
	root := fixture(t, map[string]string{"repo/Cargo.toml": "[workspace]\nmembers=['crates/*']\n", "repo/crates/app/Cargo.toml": "[package]\nname='app'\nversion='0.1.0'\n[dependencies]\nitoa='=1.0.15'\n", "repo/Cargo.lock": cargoLockFixture})
	plan, err := projectprep.Detect(t.Context(), root, "repo")
	if err != nil || !plan.Ready || plan.Target != "repo" || plan.WorkspaceRoot != "" || len(plan.Lockfiles) != 2 || len(plan.Packages) != 1 || plan.Packages[0].URL != "https://static.crates.io/crates/itoa/itoa-1.0.15.crate" || plan.Test.ID != "test.rust" {
		t.Fatalf("plan=%+v %v", plan, err)
	}
	file := filepath.Join(root, "repo/crates/app/Cargo.toml")
	if err := os.WriteFile(file, []byte("[package]\nname='app'\nversion='0.1.0'\n[dependencies]\nitoa='1'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := projectprep.Detect(t.Context(), root, "repo")
	if err != nil || changed.Fingerprint == plan.Fingerprint {
		t.Fatal("member dependency change did not invalidate consent", err)
	}
	member, err := projectprep.Detect(t.Context(), root, "repo/crates/app")
	if err != nil || member.Ready || !strings.Contains(strings.Join(member.Issues, " "), "explicit Cargo") {
		t.Fatalf("member borrowed ancestor lock: %+v %v", member, err)
	}
}

func TestCargoPreparationRejectsUnboundSourcesAndPaths(t *testing.T) {
	for _, test := range []struct{ name, manifest, lock, config, issue string }{
		{"git", "", strings.Replace(cargoLockFixture, "registry+https://github.com/rust-lang/crates.io-index", "git+https://example.com/source", 1), "", "crates.io"},
		{"checksum", "", strings.Replace(cargoLockFixture, "4a5f13b858c8d314ee3e8f639011f7ccefe71f97f96e50151fb991f267928e2c", "bad", 1), "", "SHA-256"},
		{"path", "[dependencies]\nother={path='../outside'}\n", cargoLockFixture, "", "outside"},
		{"manifest git", "[dependencies]\nother={git='https://example.com/repo'}\n", cargoLockFixture, "", "Git"},
		{"registry", "[dependencies]\nitoa={version='1', registry='private'}\n", cargoLockFixture, "", "custom registry"},
		{"config", "", cargoLockFixture, "[source.crates-io]\nreplace-with='other'\n", "configuration"},
		{"compiler", "", cargoLockFixture, "[build]\nrustc-wrapper='malicious'\n", "configuration"},
		{"missing local", "", strings.Replace(cargoLockFixture, "name = 'app'", "name = 'unknown'", 1), "", "bound manifests"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{"Cargo.toml": "[package]\nname='app'\nversion='0.1.0'\n" + test.manifest, "Cargo.lock": test.lock}
			if test.config != "" {
				files[".cargo/config.toml"] = test.config
			}
			root := fixture(t, files)
			plan, err := projectprep.Detect(t.Context(), root, ".")
			if err != nil || plan.Ready || !strings.Contains(strings.Join(plan.Issues, " "), test.issue) {
				t.Fatalf("plan=%+v %v", plan, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := projectprep.Detect(ctx, t.TempDir(), "."); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}
