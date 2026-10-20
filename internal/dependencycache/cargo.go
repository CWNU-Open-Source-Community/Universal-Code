package dependencycache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/projectprep"
	"github.com/pelletier/go-toml/v2"
)

func cargoCrateDirectory(target string, pkg projectprep.Package) string {
	return path.Join(projectprep.CargoVendorDirectory(target), pkg.ModulePath+"-"+pkg.Version)
}

// Only broker-owned staging is removed. os.Root never follows a repository
// path outside the Run workspace, including during replacement of an old cache.
func resetCargoVendor(ctx context.Context, root *os.Root, plan projectprep.Plan, check func(context.Context) error) error {
	if plan.Ecosystem != "rust" {
		return nil
	}
	if err := validateCargoPlan(plan); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := check(ctx); err != nil {
		return err
	}
	directory := projectprep.CargoVendorDirectory(plan.Target)
	if err := root.RemoveAll(directory); err != nil {
		return err
	}
	return writeAtomic(root, path.Join(directory, ".traverse-vendor"), []byte("locked Cargo sources\n"), 0600)
}

func validateCargoPlan(plan projectprep.Plan) error {
	if plan.Ecosystem == "rust" && (!plan.Ready || plan.Version != projectprep.ProtocolVersion || len(plan.Fingerprint) != 64) {
		return errors.New("reviewed Cargo preparation plan is unavailable")
	}
	for _, pkg := range plan.Packages {
		if (plan.Ecosystem == "rust") != (pkg.Kind == projectprep.KindCargoCrate) {
			return errors.New("Cargo preparation and artifact kinds do not match")
		}
	}
	return nil
}

// A .crate is a gzip tar with one name-version prefix. Reject all links,
// duplicate/case-colliding names and non-files before writing source content.
// Cargo's own directory-source checksums then bind each unpacked file as well
// as the archive checksum from Cargo.lock.
func stageCargoCrate(ctx context.Context, root *os.Root, target string, pkg projectprep.Package, body []byte, remaining int64, check func(context.Context) error) (int64, error) {
	if !projectprep.ValidCargoPackage(pkg.ModulePath, pkg.Version) || pkg.ID != pkg.ModulePath+"@"+pkg.Version {
		return 0, errors.New("Cargo artifact identity is invalid")
	}
	compressed, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("Cargo crate is not a gzip archive")
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	prefix := pkg.ModulePath + "-" + pkg.Version + "/"
	files := map[string][]byte{}
	executable := map[string]bool{}
	seen := map[string]bool{}
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
			return 0, errors.New("Cargo crate archive is invalid")
		}
		if count >= 250000 || !strings.HasPrefix(entry.Name, prefix) || !utf8.ValidString(entry.Name) || len(entry.Name) > 4096 {
			return 0, errors.New("Cargo crate archive has an invalid package path or entry count")
		}
		name := strings.TrimPrefix(entry.Name, prefix)
		if entry.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
			if name == "" {
				continue
			}
		}
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:\x00\r\n") || name == ".cargo-checksum.json" {
			return 0, errors.New("Cargo crate archive path escapes its package")
		}
		for _, part := range strings.Split(name, "/") {
			if !cargoArchiveComponent(part) {
				return 0, errors.New("Cargo crate archive has a nonportable path")
			}
		}
		folded := strings.ToLower(name)
		if seen[folded] {
			return 0, errors.New("Cargo crate archive contains duplicate paths")
		}
		seen[folded] = true
		if entry.Typeflag == tar.TypeDir {
			continue
		}
		if entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeRegA {
			return 0, errors.New("Cargo crate archive links and special entries are unsupported")
		}
		if entry.Size < 0 || entry.Size > MaxArtifactBytes || expanded+entry.Size > remaining {
			return 0, errors.New("Cargo crate expanded bytes exceed the preparation limit")
		}
		data, err := io.ReadAll(io.LimitReader(archive, entry.Size+1))
		if err != nil || int64(len(data)) != entry.Size {
			return 0, errors.New("Cargo crate file is truncated")
		}
		files[name] = data
		executable[name] = entry.Mode&0111 != 0
		expanded += int64(len(data))
	}
	var manifest struct {
		Package struct{ Name, Version string }
	}
	if err := toml.Unmarshal(files["Cargo.toml"], &manifest); err != nil || manifest.Package.Name != pkg.ModulePath || manifest.Package.Version != pkg.Version {
		return 0, errors.New("Cargo crate manifest differs from its locked identity")
	}
	checksums := struct {
		Files   map[string]string `json:"files"`
		Package string            `json:"package"`
	}{Files: map[string]string{}, Package: pkg.Integrity}
	directory := cargoCrateDirectory(target, pkg)
	for name, data := range files {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := check(ctx); err != nil {
			return 0, err
		}
		sum := sha256.Sum256(data)
		checksums.Files[name] = hex.EncodeToString(sum[:])
		if err := writeAtomic(root, path.Join(directory, name), data, 0600); err != nil {
			return 0, err
		}
		if executable[name] {
			if err := root.Chmod(path.Join(directory, name), 0700); err != nil {
				return 0, err
			}
		}
	}
	metadata, err := json.Marshal(checksums)
	if err != nil {
		return 0, err
	}
	if int64(len(metadata)) > remaining-expanded {
		return 0, errors.New("Cargo crate checksum metadata exceeds the preparation limit")
	}
	expanded += int64(len(metadata))
	if err := check(ctx); err != nil {
		return 0, err
	}
	if err := writeAtomic(root, path.Join(directory, ".cargo-checksum.json"), metadata, 0600); err != nil {
		return 0, err
	}
	return expanded, nil
}

func cargoArchiveComponent(value string) bool {
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
