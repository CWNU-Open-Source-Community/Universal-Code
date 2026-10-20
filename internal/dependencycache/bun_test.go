package dependencycache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/projectprep"
)

func bunArchive(t *testing.T, entries []tar.Header) []byte {
	t.Helper()
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	writer := tar.NewWriter(gz)
	manifest := []byte(`{"name":"fixture","version":"1.0.0"}`)
	if err := writer.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0600, Size: int64(len(manifest)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(manifest); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := writer.WriteHeader(&entry); err != nil {
			t.Fatal(err)
		}
		if entry.Size > 0 {
			if _, err := writer.Write(bytes.Repeat([]byte("x"), int(entry.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func TestBunTarballPathsAndExpandedBudget(t *testing.T) {
	pkg := projectprep.Package{Kind: projectprep.KindBunTarball, ModulePath: "fixture", Version: "1.0.0"}
	for _, test := range []struct {
		name    string
		entries []tar.Header
		budget  int64
	}{
		{"escape", []tar.Header{{Name: "package/../../outside", Typeflag: tar.TypeReg}}, 1024},
		{"symlink", []tar.Header{{Name: "package/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}}, 1024},
		{"hardlink", []tar.Header{{Name: "package/link", Typeflag: tar.TypeLink, Linkname: "package/package.json"}}, 1024},
		{"case prefix", []tar.Header{{Name: "package/lib/a", Typeflag: tar.TypeReg}, {Name: "package/Lib/b", Typeflag: tar.TypeReg}}, 1024},
		{"device", []tar.Header{{Name: "package/con.txt", Typeflag: tar.TypeReg}}, 1024},
		{"duplicate", []tar.Header{{Name: "package/a", Typeflag: tar.TypeReg}, {Name: "package/a", Typeflag: tar.TypeReg}}, 1024},
		{"expanded", []tar.Header{{Name: "package/huge", Typeflag: tar.TypeReg, Size: 100}}, 50},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if _, err := stageBunTarball(t.Context(), root, ".", pkg, bunArchive(t, test.entries), test.budget, func(context.Context) error { return nil }); err == nil {
				t.Fatal("unsafe tarball accepted")
			}
		})
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := stageBunTarball(ctx, root, ".", pkg, bunArchive(t, nil), 1024, func(context.Context) error { return nil }); err != context.Canceled {
		t.Fatal("cancel ignored", err)
	}
	if _, err := stageBunTarball(t.Context(), root, ".", pkg, bunArchive(t, nil), 1024, func(context.Context) error { return errors.New("revoked") }); err == nil {
		t.Fatal("revoked authority wrote cache")
	}
}

func TestBunCacheResetNeedsCurrentAuthority(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file := filepath.Join(workspace, filepath.FromSlash(projectprep.BunCacheDirectory(".")), "old")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := projectprep.Plan{Ecosystem: "bun", Target: "."}
	if err := resetBunCache(t.Context(), root, plan, func(context.Context) error { return errors.New("revoked") }); err == nil {
		t.Fatal("revoked reset succeeded")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("revoked reset removed files")
	}
	if err := resetBunCache(t.Context(), root, plan, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old unbound cache survived", err)
	}
}

func TestBunCacheHitsStillRequireLockedIntegrity(t *testing.T) {
	pkg := projectprep.Package{Kind: projectprep.KindBunTarball, Integrity: "sha512-41Cifkg6e8TylSpdtTpeLVMqvSBEVzTttHvERD741+pnZ8ANv0004MRL43QKPDlK9cGvNp6NZWZUBlbGXYxxng=="}
	if err := verifyArtifact(pkg, []byte("corrupt registry tarball")); !errors.Is(err, ErrIntegrity) {
		t.Fatal("Bun corrupt bytes accepted", err)
	}
	if err := validateBunPlan(projectprep.Plan{Ecosystem: "npm", Packages: []projectprep.Package{pkg}}); err == nil {
		t.Fatal("Bun artifact crossed another plan")
	}
}
