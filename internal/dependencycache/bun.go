package dependencycache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/projectprep"
)

func validateBunPlan(plan projectprep.Plan) error {
	for _, pkg := range plan.Packages {
		if (plan.Ecosystem == "bun") != (pkg.Kind == projectprep.KindBunTarball) {
			return errors.New("Bun plan and artifact kinds do not match")
		}
	}
	return nil
}

func resetBunCache(ctx context.Context, root *os.Root, plan projectprep.Plan, check func(context.Context) error) error {
	if plan.Ecosystem != "bun" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := check(ctx); err != nil {
		return err
	}
	directory := projectprep.BunCacheDirectory(plan.Target)
	if err := root.RemoveAll(directory); err != nil {
		return err
	}
	return writeAtomic(root, path.Join(directory, ".traverse-bun-cache"), []byte("Bun 1.4.3 verified registry cache\n"), 0600)
}

// Bun's fixed-version cache consumes extracted package/ tarballs. Shared
// artifacts remain integrity checked download bytes; all extracted files are
// private Run copies. No archive path, mode or link selects a host resource.
func stageBunTarball(ctx context.Context, root *os.Root, target string, pkg projectprep.Package, body []byte, remaining int64, check func(context.Context) error) (int64, error) {
	if !projectprep.ValidBunPackage(pkg.ModulePath, pkg.Version) {
		return 0, errors.New("Bun cache package identity is invalid")
	}
	compressed, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("Bun tarball is not gzip")
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	files := map[string][]byte{}
	modes := map[string]os.FileMode{}
	seen := map[string]string{}
	entries := map[string]bool{}
	var expanded int64
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := check(ctx); err != nil {
			return 0, err
		}
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, errors.New("Bun tarball is invalid")
		}
		if count >= 250000 || len(entry.Name) > 4096 || !utf8.ValidString(entry.Name) || !strings.HasPrefix(entry.Name, "package/") {
			return 0, errors.New("Bun tarball requires bounded package/ paths")
		}
		name := strings.TrimPrefix(entry.Name, "package/")
		if entry.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
			if name == "" {
				continue
			}
		}
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:\x00\r\n") {
			return 0, errors.New("Bun tarball path escapes its package")
		}
		parts := strings.Split(name, "/")
		for index, part := range parts {
			if !bunArchiveComponent(part) {
				return 0, errors.New("Bun tarball has a nonportable path")
			}
			prefix := strings.Join(parts[:index+1], "/")
			folded := strings.ToLower(prefix)
			if previous, exists := seen[folded]; exists && previous != prefix {
				return 0, errors.New("Bun tarball has case-colliding paths")
			}
			seen[folded] = prefix
		}
		if entries[name] {
			return 0, errors.New("Bun tarball has duplicate paths")
		}
		entries[name] = true
		if entry.Typeflag == tar.TypeDir {
			continue
		}
		if entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeRegA {
			return 0, errors.New("Bun tarball links and special files are unsupported")
		}
		if entry.Size < 0 || entry.Size > MaxArtifactBytes || expanded+entry.Size > remaining {
			return 0, errors.New("Bun expanded tarball exceeds preparation limits")
		}
		data, err := io.ReadAll(io.LimitReader(archive, entry.Size+1))
		if err != nil || int64(len(data)) != entry.Size {
			return 0, errors.New("Bun tarball file is truncated")
		}
		files[name], modes[name] = data, 0600
		if entry.Mode&0111 != 0 {
			modes[name] = 0700
		}
		expanded += int64(len(data))
	}
	var manifest struct{ Name, Version string }
	if json.Unmarshal(files["package.json"], &manifest) != nil || manifest.Name != pkg.ModulePath || manifest.Version != pkg.Version {
		return 0, errors.New("Bun tarball manifest differs from its locked identity")
	}
	directory := projectprep.BunCachePackageDirectory(target, pkg.ModulePath, pkg.Version)
	for name, data := range files {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := check(ctx); err != nil {
			return 0, err
		}
		if err := writeAtomic(root, path.Join(directory, name), data, modes[name]); err != nil {
			return 0, err
		}
	}
	return expanded, nil
}

func bunArchiveComponent(value string) bool {
	if strings.TrimRight(value, ". ") != value {
		return false
	}
	for _, c := range value {
		if c < 32 || c == 127 {
			return false
		}
	}
	base := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CLOCK$" {
		return false
	}
	return !(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9')
}
