package dependencycache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

func cargoTestArchive(t *testing.T, entries ...*tar.Header) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(gz)
	manifest := []byte("[package]\nname='fixture'\nversion='1.0.0'\n")
	if err := archive.WriteHeader(&tar.Header{Name: "fixture-1.0.0/Cargo.toml", Mode: 0600, Size: int64(len(manifest)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(manifest); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := archive.WriteHeader(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestCargoCrateIntegrityAndArchiveBoundary(t *testing.T) {
	pkg := projectprep.Package{ID: "fixture@1.0.0", Kind: projectprep.KindCargoCrate, ModulePath: "fixture", Version: "1.0.0"}
	for _, scenario := range []struct {
		name      string
		entries   []*tar.Header
		remaining int64
		want      string
	}{
		{"valid", nil, MaxPreparationBytes, ""},
		{"traversal", []*tar.Header{{Name: "fixture-1.0.0/../outside", Typeflag: tar.TypeReg}}, MaxPreparationBytes, "path"},
		{"wrong prefix", []*tar.Header{{Name: "other-1.0.0/file", Typeflag: tar.TypeReg}}, MaxPreparationBytes, "path"},
		{"backslash", []*tar.Header{{Name: `fixture-1.0.0/a\b`, Typeflag: tar.TypeReg}}, MaxPreparationBytes, "path"},
		{"duplicate", []*tar.Header{{Name: "fixture-1.0.0/CARGO.TOML", Typeflag: tar.TypeReg}}, MaxPreparationBytes, "duplicate"},
		{"symlink", []*tar.Header{{Name: "fixture-1.0.0/linked", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}}, MaxPreparationBytes, "links"},
		{"hardlink", []*tar.Header{{Name: "fixture-1.0.0/linked", Typeflag: tar.TypeLink, Linkname: "../../outside"}}, MaxPreparationBytes, "links"},
		{"metadata", []*tar.Header{{Name: "fixture-1.0.0/.cargo-checksum.json", Typeflag: tar.TypeReg}}, MaxPreparationBytes, "path"},
		{"Windows device", []*tar.Header{{Name: "fixture-1.0.0/NUL", Typeflag: tar.TypeReg}}, MaxPreparationBytes, "path"},
		{"Windows alias", []*tar.Header{{Name: "fixture-1.0.0/file.", Typeflag: tar.TypeReg}}, MaxPreparationBytes, "path"},
		{"expanded limit", nil, 1, "limit"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			body := cargoTestArchive(t, scenario.entries...)
			sum := sha256.Sum256(body)
			pkg.Integrity = hex.EncodeToString(sum[:])
			if err := verifyArtifact(pkg, body); err != nil {
				t.Fatal(err)
			}
			if err := verifyArtifact(pkg, append(body, 'x')); !errors.Is(err, ErrIntegrity) {
				t.Fatal("corrupt crate accepted", err)
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			_, err = stageCargoCrate(t.Context(), root, ".", pkg, body, scenario.remaining, func(context.Context) error { return nil })
			if scenario.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("archive=%v", err)
			}
			if _, err := root.Lstat("outside"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("archive wrote outside its vendor directory")
			}
		})
	}
}

func TestCargoStagingRechecksAuthorityAndClearsOldSources(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	plan := projectprep.Plan{Ecosystem: "rust", Target: "."}
	old := projectprep.CargoVendorDirectory(".") + "/old/source.rs"
	if err := writeAtomic(root, old, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("permission changed")
	if err := resetCargoVendor(t.Context(), root, plan, func(context.Context) error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if _, err := root.Stat(old); err != nil {
		t.Fatal("denied preparation changed cache", err)
	}
	if err := resetCargoVendor(t.Context(), root, plan, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old unbound Cargo sources survived reset")
	}
	body := cargoTestArchive(t)
	pkg := projectprep.Package{ID: "fixture@1.0.0", Kind: projectprep.KindCargoCrate, ModulePath: "fixture", Version: "1.0.0"}
	if _, err := stageCargoCrate(t.Context(), root, ".", pkg, body, MaxPreparationBytes, func(context.Context) error { return context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled Cargo staging wrote files", err)
	}
}
