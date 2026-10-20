// Package dependencycache downloads exact reviewed dependency artifacts. It
// never launches package managers or grants network access to a sandbox.
package dependencycache

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"cyberagent-workbench/internal/projectprep"
	"cyberagent-workbench/internal/webevidence"
	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
	modzip "golang.org/x/mod/zip"
)

const (
	MaxArtifactBytes    = 64 * 1024 * 1024
	MaxPreparationBytes = 512 * 1024 * 1024
)

var ErrSourceDenied = errors.New("package source has not been approved")
var ErrIntegrity = errors.New("dependency artifact integrity mismatch")

type Progress struct {
	Completed int    `json:"completed"`
	Total     int    `json:"total"`
	ID        string `json:"id"`
	FromCache bool   `json:"from_cache"`
	Bytes     int64  `json:"bytes"`
}

type Stats struct {
	FetchedPackages int   `json:"fetched_packages"`
	CachedPackages  int   `json:"cached_packages"`
	Bytes           int64 `json:"bytes"`
}

type Broker struct {
	root   string
	client *webevidence.SafeHTTPClient
}

func New(cacheRoot string) (*Broker, error) {
	if !filepath.IsAbs(cacheRoot) || filepath.Clean(cacheRoot) != cacheRoot {
		return nil, errors.New("dependency cache needs an absolute private directory")
	}
	if err := os.MkdirAll(cacheRoot, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(cacheRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("dependency cache directory is linked")
	}
	canonical, err := filepath.EvalSymlinks(cacheRoot)
	if err != nil || !cacheRootPathMatches(cacheRoot, canonical) {
		return nil, errors.New("dependency cache directory is linked")
	}
	return &Broker{root: canonical, client: webevidence.NewSafeHTTPClient()}, nil
}

// macOS exposes these system directories through fixed aliases. Only that
// prefix may change; linked application or cache subdirectories still fail the
// complete canonical-path comparison in New.
func cacheRootAfterSystemAliases(root string) string {
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/var", "/tmp"} {
			if root == alias || strings.HasPrefix(root, alias+"/") {
				return "/private" + root
			}
		}
	}
	return root
}

// Prepare checks authority before each read, network hop and staged mutation.
// Cache hits need no source grant, but must match the reviewed integrity again.
// The sandbox receives private copies and can never write the shared cache.
func (b *Broker) Prepare(ctx context.Context, plan projectprep.Plan, approvedOrigins []string,
	workspaceRoot string, progress func(Progress), authorityCheck func(context.Context) error,
) (Stats, error) {
	stats := Stats{}
	if b == nil || b.client == nil || ctx == nil || authorityCheck == nil ||
		!plan.Ready || len(plan.Packages) > projectprep.MaxPackages ||
		!filepath.IsAbs(workspaceRoot) || filepath.Clean(workspaceRoot) != workspaceRoot {
		return stats, errors.New("reviewed dependency preparation and current authority are required")
	}
	stagedBytes, err := pnpmSeedBytes(plan)
	if err != nil {
		return stats, err
	}
	if plan.Ecosystem == "yarn" {
		stagedBytes += int64(len(yarnPreparationRC))
		for _, pkg := range plan.Packages {
			if pkg.Kind != projectprep.KindYarnTarball {
				return stats, errors.New("Yarn preparation contains a different artifact kind")
			}
		}
	}
	if err := validateCargoPlan(plan); err != nil {
		return stats, err
	}
	origins := make(map[string]bool, len(approvedOrigins))
	for _, raw := range approvedOrigins {
		origin, err := canonicalOrigin(raw)
		if err != nil || origin != strings.TrimSuffix(raw, "/") {
			return stats, errors.New("package source must be an exact public HTTPS origin")
		}
		origins[origin] = true
	}
	if err := authorityCheck(ctx); err != nil {
		return stats, err
	}
	canonical, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil || canonical != workspaceRoot || pathsOverlap(workspaceRoot, b.root) {
		return stats, errors.New("dependency cache and workspace must be separate unlinked roots")
	}
	workspace, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return stats, err
	}
	defer workspace.Close()
	if err := authorityCheck(ctx); err != nil {
		return stats, err
	}
	if err := ensureStagingIgnore(workspace); err != nil {
		return stats, err
	}
	cache, err := os.OpenRoot(b.root)
	if err != nil {
		return stats, err
	}
	defer cache.Close()
	if err := resetCargoVendor(ctx, workspace, plan, authorityCheck); err != nil {
		return stats, err
	}
	for index, pkg := range plan.Packages {
		if err := authorityCheck(ctx); err != nil {
			return stats, err
		}
		relative, err := artifactStagePath(plan.Target, pkg)
		if err != nil {
			return stats, fmt.Errorf("invalid dependency artifact: %w", err)
		}
		key := sha256.Sum256([]byte(pkg.Kind + "\x00" + pkg.ModulePath + "\x00" + pkg.Version + "\x00" + pkg.Integrity))
		name := hex.EncodeToString(key[:]) + ".blob"
		body, hit, err := readCached(cache, name)
		if err != nil {
			return stats, err
		}
		if hit {
			if err := verifyArtifact(pkg, body); err != nil {
				parsed, sourceErr := artifactURL(pkg.URL)
				if !errors.Is(err, ErrIntegrity) || sourceErr != nil || !origins["https://"+parsed.Host] {
					return stats, fmt.Errorf("cached dependency is corrupt: %w", err)
				}
				// Approved retries can repair corrupt bytes. Preserve the old
				// object until the replacement passes the same download checks.
				hit = false
			}
		}
		if !hit {
			if _, err := artifactURL(pkg.URL); err != nil {
				return stats, err
			}
			guard := func(raw string) error {
				if err := authorityCheck(ctx); err != nil {
					return err
				}
				parsed, err := artifactURL(raw)
				if err != nil && pkg.Kind == projectprep.KindGoZip {
					canonical, redirectErr := webevidence.CanonicalizeGoProxyArchiveRedirect(pkg.URL, raw)
					if redirectErr == nil {
						parsed, err = url.Parse(canonical)
					}
				}
				if err != nil {
					return err
				}
				if !origins["https://"+parsed.Host] {
					return ErrSourceDenied
				}
				return nil
			}
			document, err := b.client.GetArtifactAuthorized(ctx, pkg.URL, MaxArtifactBytes, guard)
			if err != nil {
				return stats, fmt.Errorf("dependency download failed: %w", err)
			}
			if document.StatusCode != http.StatusOK || document.Truncated {
				return stats, errors.New("dependency download failed or exceeded its byte limit")
			}
			body = document.Body
			if err := verifyArtifact(pkg, body); err != nil {
				return stats, err
			}
			if err := authorityCheck(ctx); err != nil {
				return stats, err
			}
			if err := writeAtomic(cache, name, body, 0600); err != nil {
				return stats, err
			}
			stats.Bytes += int64(len(body))
		}
		stagedBytes += int64(len(body))
		if stagedBytes > MaxPreparationBytes {
			return stats, errors.New("dependency preparation exceeded its total byte limit")
		}
		if err := authorityCheck(ctx); err != nil {
			return stats, err
		}
		if err := writeAtomic(workspace, relative, body, 0600); err != nil {
			return stats, err
		}
		if pkg.Kind == projectprep.KindCargoCrate {
			expanded, err := stageCargoCrate(ctx, workspace, plan.Target, pkg, body, MaxPreparationBytes-stagedBytes, authorityCheck)
			if err != nil {
				return stats, err
			}
			stagedBytes += expanded
			if stagedBytes > MaxPreparationBytes {
				return stats, errors.New("Cargo vendor exceeded the preparation byte limit")
			}
		}
		if pkg.Kind == projectprep.KindGoMod || pkg.Kind == projectprep.KindGoZip {
			info, _ := json.Marshal(struct {
				Version string
				Time    string
			}{pkg.Version, "0001-01-01T00:00:00Z"})
			infoPath := strings.TrimSuffix(strings.TrimSuffix(relative, ".mod"), ".zip") + ".info"
			if err := writeAtomic(workspace, infoPath, info, 0600); err != nil {
				return stats, err
			}
			if pkg.Kind == projectprep.KindGoZip {
				if err := writeAtomic(workspace, relative+"hash", []byte(pkg.Integrity), 0600); err != nil {
					return stats, err
				}
			}
		}
		if hit {
			stats.CachedPackages++
		} else {
			stats.FetchedPackages++
		}
		if progress != nil {
			progress(Progress{Completed: index + 1, Total: len(plan.Packages), ID: pkg.ID, FromCache: hit, Bytes: stats.Bytes})
		}
	}
	if plan.Ecosystem == "pnpm" {
		if err := writePNPMSeed(ctx, workspace, plan, authorityCheck); err != nil {
			return stats, err
		}
	}
	if plan.Ecosystem == "yarn" {
		if err := authorityCheck(ctx); err != nil {
			return stats, err
		}
		if err := writeAtomic(workspace, projectprep.YarnRCPath(plan.Target), []byte(yarnPreparationRC), 0600); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

const yarnPreparationRC = "yarn-offline-mirror \"./yarn-mirror\"\nyarn-offline-mirror-pruning false\ndisable-self-update-check true\n"

// Git reads this directory-local exclusion itself, so staging never edits the
// linked worktree's shared repository configuration or a project's ignore file.
func ensureStagingIgnore(root *os.Root) error {
	const relative = ".traverse-preparation/.gitignore"
	if info, err := root.Lstat(relative); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != 2 {
			return errors.New("dependency staging has an unknown exclusion file")
		}
		file, err := root.Open(relative)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(file, 3))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || string(body) != "*\n" {
			return errors.New("dependency staging has an unknown exclusion file")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeAtomic(root, relative, []byte("*\n"), 0600)
}

// CachedPackages counts verified cache objects without making a request.
func (b *Broker) CachedPackages(ctx context.Context, plan projectprep.Plan) (int, error) {
	if b == nil || ctx == nil || len(plan.Packages) > projectprep.MaxPackages {
		return 0, errors.New("dependency cache is unavailable")
	}
	root, err := os.OpenRoot(b.root)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	count := 0
	for _, pkg := range plan.Packages {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		key := sha256.Sum256([]byte(pkg.Kind + "\x00" + pkg.ModulePath + "\x00" + pkg.Version + "\x00" + pkg.Integrity))
		body, hit, err := readCached(root, hex.EncodeToString(key[:])+".blob")
		if err != nil {
			return count, err
		}
		if hit {
			if err := verifyArtifact(pkg, body); err != nil {
				if errors.Is(err, ErrIntegrity) {
					continue
				}
				return count, err
			}
			count++
		}
	}
	return count, nil
}

func canonicalOrigin(raw string) (string, error) {
	u, err := artifactURL(raw)
	if err != nil || (u.Path != "" && u.Path != "/") {
		return "", errors.New("invalid package origin")
	}
	return "https://" + u.Host, nil
}

func artifactURL(raw string) (*url.URL, error) {
	canonical, err := webevidence.CanonicalizePublicHTTPSURL(raw)
	if err != nil {
		return nil, errors.New("dependency source is outside public HTTPS")
	}
	u, err := url.Parse(canonical)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("dependency URL cannot contain credentials, query or fragment")
	}
	return u, nil
}

func artifactStagePath(target string, pkg projectprep.Package) (string, error) {
	switch pkg.Kind {
	case projectprep.KindCargoCrate:
		if !projectprep.ValidCargoPackage(pkg.ModulePath, pkg.Version) || pkg.ID != pkg.ModulePath+"@"+pkg.Version {
			return "", errors.New("Cargo artifact requires a locked package identity")
		}
		return path.Join(projectprep.StageDirectory(target), "cargo-crates", pkg.ModulePath+"-"+pkg.Version+".crate"), nil
	case projectprep.KindNPMTarball:
		algorithm, digest, err := npmIntegrity(pkg.Integrity)
		if err != nil {
			return "", err
		}
		hexDigest := hex.EncodeToString(digest)
		return path.Join(projectprep.NPMCacheDirectory(target), "_cacache/content-v2", algorithm, hexDigest[:2], hexDigest[2:4], hexDigest[4:]), nil
	case projectprep.KindPNPMTarball:
		if strings.TrimSpace(pkg.ID) == "" {
			return "", errors.New("pnpm artifact needs a package identity")
		}
		if _, _, err := npmIntegrity(pkg.Integrity); err != nil {
			return "", err
		}
		return path.Join(projectprep.StageDirectory(target), "pnpm-tarballs", projectprep.PNPMTarballFile(pkg.ID)), nil
	case projectprep.KindYarnTarball:
		if _, _, err := npmIntegrity(pkg.Integrity); err != nil {
			return "", err
		}
		name, err := projectprep.YarnTarballFile(pkg.URL)
		if err != nil {
			return "", err
		}
		return path.Join(projectprep.YarnMirrorDirectory(target), name), nil
	case projectprep.KindGoMod, projectprep.KindGoZip:
		if module.Check(pkg.ModulePath, pkg.Version) != nil || module.CanonicalVersion(pkg.Version) != pkg.Version || !strings.HasPrefix(pkg.Integrity, "h1:") {
			return "", errors.New("Go artifact needs a canonical module version and h1 checksum")
		}
		modulePath, err := module.EscapePath(pkg.ModulePath)
		if err != nil {
			return "", err
		}
		version, err := module.EscapeVersion(pkg.Version)
		if err != nil {
			return "", err
		}
		extension := ".mod"
		if pkg.Kind == projectprep.KindGoZip {
			extension = ".zip"
		}
		return path.Join(projectprep.GoModuleCacheDirectory(target), "cache/download", modulePath, "@v", version+extension), nil
	default:
		return "", errors.New("unsupported dependency artifact kind")
	}
}

func npmIntegrity(value string) (string, []byte, error) {
	algorithm, encoded, ok := strings.Cut(value, "-")
	if !ok || (algorithm != "sha512" && algorithm != "sha256") {
		return "", nil, errors.New("npm artifact requires SHA-512 or SHA-256 integrity")
	}
	digest, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || (algorithm == "sha512" && len(digest) != 64) || (algorithm == "sha256" && len(digest) != 32) {
		return "", nil, errors.New("invalid npm integrity")
	}
	return algorithm, digest, nil
}

func verifyArtifact(pkg projectprep.Package, body []byte) error {
	if len(body) == 0 || len(body) > MaxArtifactBytes {
		return fmt.Errorf("dependency artifact byte limit is invalid: %w", ErrIntegrity)
	}
	switch pkg.Kind {
	case projectprep.KindCargoCrate:
		expected, err := hex.DecodeString(pkg.Integrity)
		sum := sha256.Sum256(body)
		if err != nil || len(expected) != 32 || !bytes.Equal(expected, sum[:]) {
			return ErrIntegrity
		}
	case projectprep.KindNPMTarball, projectprep.KindPNPMTarball, projectprep.KindYarnTarball:
		algorithm, expected, err := npmIntegrity(pkg.Integrity)
		if err != nil {
			return err
		}
		var actual []byte
		if algorithm == "sha512" {
			sum := sha512.Sum512(body)
			actual = sum[:]
		} else {
			sum := sha256.Sum256(body)
			actual = sum[:]
		}
		if !bytes.Equal(expected, actual) {
			return ErrIntegrity
		}
	case projectprep.KindGoMod:
		if len(body) > projectprep.MaxFileBytes {
			return fmt.Errorf("Go module manifest exceeded its limit: %w", ErrIntegrity)
		}
		digest, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil })
		if err != nil || digest != pkg.Integrity {
			return ErrIntegrity
		}
	case projectprep.KindGoZip:
		file, err := os.CreateTemp("", "traverse-module-*.zip")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		_, writeErr := file.Write(body)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return errors.Join(writeErr, closeErr)
		}
		checked, err := modzip.CheckZip(module.Version{Path: pkg.ModulePath, Version: pkg.Version}, name)
		if err != nil || checked.Err() != nil {
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				return err
			}
			return fmt.Errorf("Go module archive is invalid: %w", ErrIntegrity)
		}
		digest, err := dirhash.HashZip(name, dirhash.Hash1)
		if err != nil {
			return err
		}
		if digest != pkg.Integrity {
			return ErrIntegrity
		}
	default:
		return errors.New("unsupported dependency artifact")
	}
	return nil
}

func readCached(root *os.Root, name string) ([]byte, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > MaxArtifactBytes {
		return nil, false, errors.New("dependency cache contains an invalid object")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, MaxArtifactBytes+1))
	return body, true, err
}

func writeAtomic(root *os.Root, relative string, data []byte, mode os.FileMode) error {
	if filepath.IsAbs(relative) || filepath.ToSlash(filepath.Clean(relative)) != relative || strings.HasPrefix(relative, "../") {
		return errors.New("dependency output escaped its root")
	}
	directory := path.Dir(relative)
	if err := mkdirUnlinked(root, directory); err != nil {
		return err
	}
	if info, err := root.Lstat(relative); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("dependency output path is linked")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := relative + ".pending-" + hex.EncodeToString(random[:])
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return root.Rename(name, relative)
}

func mkdirUnlinked(root *os.Root, relative string) error {
	current := ""
	for _, part := range strings.Split(relative, "/") {
		if part == "." {
			continue
		}
		current = path.Join(current, part)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("dependency staging directory is linked")
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	for _, pair := range [][2]string{{first, second}, {second, first}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
