package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	MaxURLImportBytes       = 1 << 20
	MaxGitImportDuration    = 5 * time.Minute
	MaxDirectoryImportFiles = 3
	MaxDirectoryImportBytes = MaxPackageArchiveBytes
)

// FetchPinnedURL downloads exactly one pinned HTTPS resource and requires its
// SHA-256 to match before returning bytes. Redirects must stay HTTPS on the
// original host, so a redirect can never silently swap the reviewed content.
func FetchPinnedURL(ctx context.Context, rawURL, expectedSHA256 string, client *http.Client) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("skill URL import requires an absolute HTTPS URL without credentials")
	}
	if !validSHA256(expectedSHA256) {
		return nil, errors.New("skill URL import requires the expected SHA-256 pin")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("skill URL import: too many redirects")
		}
		if request.URL.Scheme != "https" {
			return errors.New("skill URL import: redirect must stay HTTPS")
		}
		if len(via) > 0 && request.URL.Host != via[0].URL.Host {
			return errors.New("skill URL import: redirect must stay on the original host")
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("skill URL import: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("skill URL import: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skill URL import: unexpected HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxURLImportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("skill URL import: read body: %w", err)
	}
	if len(body) == 0 || len(body) > MaxURLImportBytes {
		return nil, fmt.Errorf("skill URL import: body must contain between 1 and %d bytes", MaxURLImportBytes)
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return nil, errors.New("skill URL import: downloaded bytes do not match the pinned SHA-256")
	}
	return body, nil
}

// FetchGitCommit stages a repository at an exact commit into stagingDir via
// git itself. No hooks, scripts, submodules, or build tools from the remote
// repository ever run; the only process executed is the local git binary.
func FetchGitCommit(ctx context.Context, repoURL, commitSHA, stagingDir string) error {
	parsed, err := url.Parse(repoURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("skill Git import requires an absolute HTTPS URL without credentials")
	}
	if !validGitCommit(commitSHA) {
		return errors.New("skill Git import requires a full lowercase 40-character commit SHA")
	}
	if stagingDir == "" {
		return errors.New("skill Git import staging directory is required")
	}
	return stageGitCommit(ctx, repoURL, commitSHA, stagingDir)
}

// stageGitCommit performs the clone/checkout/verify sequence. It is separated
// from FetchGitCommit's URL policy so tests can stage local file:// fixtures.
func stageGitCommit(ctx context.Context, repoURL, commitSHA, stagingDir string) error {
	stagingDir, err := filepath.Abs(stagingDir)
	if err != nil {
		return fmt.Errorf("skill Git import: resolve staging: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(stagingDir), 0o700); err != nil {
		return fmt.Errorf("skill Git import: prepare staging: %w", err)
	}
	if err := os.RemoveAll(stagingDir); err != nil {
		return fmt.Errorf("skill Git import: reset staging: %w", err)
	}
	importCtx, cancel := context.WithTimeout(ctx, MaxGitImportDuration)
	defer cancel()
	hooksDir, err := os.MkdirTemp(filepath.Dir(stagingDir), "skill-import-hooks-")
	if err != nil {
		return fmt.Errorf("skill Git import: disable hooks: %w", err)
	}
	defer os.Remove(hooksDir)
	protocol := "https"
	if strings.HasPrefix(repoURL, "file://") {
		protocol = "file" // private helper's offline fixtures; FetchGitCommit requires HTTPS
	}
	command := func(args ...string) *exec.Cmd {
		prefix := []string{"-c", "core.autocrlf=false", "-c", "core.hooksPath=" + hooksDir,
			"-c", "core.fsmonitor=false", "-c", "credential.helper=",
			"-c", "core.attributesFile=" + os.DevNull, "-c", "http.followRedirects=false"}
		cmd := exec.CommandContext(importCtx, "git", append(prefix, args...)...)
		cmd.Dir = hooksDir
		cmd.Env = append(skillGitEnvironment(), "GIT_ALLOW_PROTOCOL="+protocol,
			"GIT_CEILING_DIRECTORIES="+hooksDir)
		return cmd
	}
	// autocrlf must be off: the pinned-commit staging checkout must reproduce
	// exact repository bytes, never a platform-normalized working tree.
	// An empty template and isolated environment also prevent inherited
	// hooks, executable filters, and Git configuration injection before review.
	clone := command("clone", "--quiet", "--no-checkout", "--no-recurse-submodules",
		"--template=", "--", repoURL, stagingDir)
	if output, err := clone.CombinedOutput(); err != nil {
		return fmt.Errorf("skill Git import: clone pinned commit: %w: %s", err, strings.TrimSpace(string(output)))
	}
	checkout := command("-C", stagingDir, "checkout", "--quiet", "--detach", commitSHA)
	if output, err := checkout.CombinedOutput(); err != nil {
		return fmt.Errorf("skill Git import: checkout pinned commit: %w: %s", err, strings.TrimSpace(string(output)))
	}
	head := command("-C", stagingDir, "rev-parse", "HEAD")
	output, err := head.Output()
	if err != nil {
		return fmt.Errorf("skill Git import: verify pinned commit: %w", err)
	}
	if resolved := strings.TrimSpace(string(output)); resolved != commitSHA {
		return fmt.Errorf("skill Git import: branch drift detected: HEAD %q does not match pinned commit %q", resolved, commitSHA)
	}
	return nil
}

func skillGitEnvironment() []string {
	allowed := map[string]bool{"PATH": true, "PATHEXT": true, "SYSTEMROOT": true,
		"WINDIR": true, "TMP": true, "TEMP": true, "TMPDIR": true}
	environment := make([]string, 0, len(allowed)+8)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if allowed[strings.ToUpper(name)] {
			environment = append(environment, entry)
		}
	}
	return append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=Never", "GIT_LFS_SKIP_SMUDGE=1")
}

func validGitCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, current := range []byte(value) {
		if (current >= '0' && current <= '9') || (current >= 'a' && current <= 'f') {
			continue
		}
		return false
	}
	return true
}

// BuildPackageFromDir packages a validated skill directory into a
// deterministic ZIP without writing anything outside the returned bytes.
// Only the exact package files are allowed at the directory root; symlinks,
// junctions, subdirectories, hidden metadata, and oversized files are
// rejected, so path traversal and hostile metadata cannot ride along.
func BuildPackageFromDir(dir string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("skill directory import: %w", err)
	}
	if len(entries) < 2 || len(entries) > MaxDirectoryImportFiles {
		return nil, fmt.Errorf("skill directory import: directory must contain the package files only (manifest.json + SKILL.md, optional SIGNATURE.json)")
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("skill directory import: stat %q: %w", entry.Name(), err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("skill directory import: symlink or junction %q is forbidden", entry.Name())
		}
		if entry.Name() == ".git" && info.IsDir() {
			continue // git staging metadata is inert; it is never packaged
		}
		if info.IsDir() {
			return nil, fmt.Errorf("skill directory import: subdirectory %q is forbidden", entry.Name())
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("skill directory import: special file %q is forbidden", entry.Name())
		}
		name := filepath.Base(entry.Name())
		if entry.Name() != name || strings.ContainsAny(name, "\\/") || strings.Contains(name, "..") {
			return nil, fmt.Errorf("skill directory import: unsafe file name %q", entry.Name())
		}
		switch name {
		case PackageManifestPath, PackageContentPath, PackageSignaturePath:
		default:
			return nil, fmt.Errorf("skill directory import: unexpected file %q (only manifest.json, SKILL.md, SIGNATURE.json are allowed)", name)
		}
		limit := int64(MaxContentBytes)
		if name == PackageManifestPath {
			limit = MaxManifestBytes
		} else if name == PackageSignaturePath {
			limit = MaxSignatureBytes
		}
		if info.Size() <= 0 || info.Size() > limit {
			return nil, fmt.Errorf("skill directory import: file %q violates its size bound", name)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(raw) != int(info.Size()) || len(raw) > int(limit) {
			return nil, fmt.Errorf("skill directory import: read %q: %w", name, err)
		}
		files[name] = raw
	}
	manifestRaw, ok := files[PackageManifestPath]
	if !ok {
		return nil, errors.New("skill directory import: manifest.json is required")
	}
	content, ok := files[PackageContentPath]
	if !ok {
		return nil, errors.New("skill directory import: SKILL.md is required")
	}
	manifest, err := decodeManifest(manifestRaw)
	if err != nil {
		return nil, fmt.Errorf("skill directory import: manifest: %w", err)
	}
	if manifest.ContentPath != PackageContentPath {
		return nil, fmt.Errorf("skill directory import: manifest content_path must be %q", PackageContentPath)
	}
	if err := manifest.Validate(content); err != nil {
		return nil, fmt.Errorf("skill directory import: manifest: %w", err)
	}
	if signatureRaw, signed := files[PackageSignaturePath]; signed {
		signature, err := decodePackageSignature(signatureRaw)
		if err != nil {
			return nil, fmt.Errorf("skill directory import: signature: %w", err)
		}
		if signature.Publisher != manifest.Publisher {
			return nil, errors.New("skill directory import: signature publisher does not match manifest publisher")
		}
		if err := validatePublisher(manifest.Publisher); err != nil {
			return nil, fmt.Errorf("skill directory import: %w", err)
		}
		return buildDeterministicPackage([]deterministicZipEntry{
			{name: PackageManifestPath, data: manifestRaw},
			{name: PackageContentPath, data: content},
			{name: PackageSignaturePath, data: signatureRaw},
		})
	}
	if manifest.Publisher != "" {
		return nil, errors.New("skill directory import: signed manifest requires SIGNATURE.json")
	}
	return buildDeterministicPackage([]deterministicZipEntry{
		{name: PackageManifestPath, data: manifestRaw},
		{name: PackageContentPath, data: content},
	})
}

var _ = json.Marshal
