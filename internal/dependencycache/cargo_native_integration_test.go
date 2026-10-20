package dependencycache_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/dependencycache"
	"cyberagent-workbench/internal/projectprep"
)

// Native Cargo is an opt-in verification of the production detector, broker,
// directory-source layout and argv; this does not substitute a product sandbox.
func TestCargoNativeLockedWorkspacePreparation(t *testing.T) {
	cargo, archive := os.Getenv("TRAVERSE_CARGO_TEST_BIN"), os.Getenv("TRAVERSE_CARGO_TEST_CRATE")
	if cargo == "" || archive == "" {
		t.Skip("set native Cargo executable and integrity-locked itoa crate paths")
	}
	if !filepath.IsAbs(cargo) {
		t.Fatal("native Cargo must be explicit")
	}
	cache := t.TempDir()
	broker, err := dependencycache.New(cache)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"repo/Cargo.toml":        "[workspace]\nmembers=['app','shared']\nresolver='2'\n",
		"repo/app/Cargo.toml":    "[package]\nname='app'\nversion='0.1.0'\nedition='2021'\n[dependencies]\nitoa='=1.0.15'\nshared={path='../shared'}\n",
		"repo/shared/Cargo.toml": "[package]\nname='shared'\nversion='0.1.0'\nedition='2021'\n",
		"repo/shared/src/lib.rs": "pub fn answer() -> u64 { 42 }",
		"repo/app/src/lib.rs":    `#[test] fn original_test(){let mut b=itoa::Buffer::new();assert_eq!(b.format(shared::answer()),"42");}`,
		"repo/Cargo.lock":        "version=4\n[[package]]\nname='app'\nversion='0.1.0'\ndependencies=['itoa','shared']\n[[package]]\nname='shared'\nversion='0.1.0'\n[[package]]\nname='itoa'\nversion='1.0.15'\nsource='registry+https://github.com/rust-lang/crates.io-index'\nchecksum='4a5f13b858c8d314ee3e8f639011f7ccefe71f97f96e50151fb991f267928e2c'\n",
	}
	for attempt := 0; attempt < 2; attempt++ {
		root := filepath.Join(t.TempDir(), "Cargo 中文 project")
		for name, body := range files {
			full := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := projectprep.Detect(t.Context(), root, "repo")
		if err != nil || !plan.Ready {
			t.Fatalf("plan=%+v %v", plan, err)
		}
		check := func(context.Context) error { return nil }
		if attempt == 0 {
			stats, err := broker.Prepare(t.Context(), plan, nil, root, nil, check)
			if !errors.Is(err, dependencycache.ErrSourceDenied) || stats.Bytes != 0 {
				t.Fatalf("unapproved source %+v %v", stats, err)
			}
			pkg := plan.Packages[0]
			key := sha256.Sum256([]byte(pkg.Kind + "\x00" + pkg.ModulePath + "\x00" + pkg.Version + "\x00" + pkg.Integrity))
			body, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cache, hex.EncodeToString(key[:])+".blob"), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
		stats, err := broker.Prepare(t.Context(), plan, nil, root, nil, check)
		if err != nil || stats.CachedPackages != 1 || stats.Bytes != 0 {
			t.Fatalf("broker %+v %v", stats, err)
		}
		adapter := commandruntimeadapter.SandboxedWorkspace(application.CommandRuntimeLocalSandboxBackend, "native-verifier", "generation")
		for _, testingCommand := range []bool{false, true} {
			var specArgs []string
			if testingCommand {
				spec, err := application.PrepareProjectPreparationTestCommand(plan, commandruntimeadapter.HostUnsandboxed("native-verifier"), root)
				if err != nil {
					t.Fatal(err)
				}
				specArgs = spec.Arguments
			} else {
				spec, err := application.PrepareProjectPreparationCommand(plan, adapter, root, false)
				if err != nil {
					t.Fatal(err)
				}
				specArgs = spec.Arguments
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			command := exec.CommandContext(ctx, cargo, specArgs...)
			command.Dir = filepath.Join(root, "repo")
			command.Env = append(os.Environ(), "PATH="+filepath.Dir(cargo)+string(os.PathListSeparator)+os.Getenv("PATH"), "CARGO_HOME="+filepath.Join(t.TempDir(), "cargo-home"), "CARGO_NET_OFFLINE=true", "RUSTUP_NO_UPDATE_CHECK=1")
			output, err := command.CombinedOutput()
			cancel()
			t.Logf("cargo %v\n%s", specArgs, output)
			if err != nil {
				t.Fatal(err)
			}
			if testingCommand && !strings.Contains(string(output), "original_test ... ok") {
				t.Fatal("original workspace test did not execute")
			}
		}
		for name, before := range files {
			after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil || string(after) != before {
				t.Fatalf("original input changed: %s %v", name, err)
			}
		}
	}
}
