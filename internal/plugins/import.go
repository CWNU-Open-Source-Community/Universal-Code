package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	MaxPluginImportDuration  = 5 * time.Minute
	pluginHTTPTimeout        = 30 * time.Second
	pluginGitDiagnosticBytes = 16 * 1024
)

// FetchPinnedHTTPS retrieves one exact plugin archive. Redirects, credentials
// in the URL, query/fragment drift, oversized bodies, and digest drift fail
// closed before the bytes can enter inert staging.
func FetchPinnedHTTPS(ctx context.Context, rawURL, expectedSHA256 string,
	base *http.Client,
) ([]byte, error) {
	source := InstallSource{Kind: "https", URI: strings.TrimSpace(rawURL),
		SHA256: strings.TrimSpace(expectedSHA256)}
	if err := source.Validate(); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: pluginHTTPTimeout}
	if base != nil {
		copyClient := *base
		client = &copyClient
		client.Jar = nil
		if client.Timeout <= 0 || client.Timeout > pluginHTTPTimeout {
			client.Timeout = pluginHTTPTimeout
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("plugin HTTPS import rejects redirects")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URI, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch pinned plugin HTTPS archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("plugin HTTPS import returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read pinned plugin HTTPS archive: %w", err)
	}
	if len(raw) < 1 || len(raw) > MaxArchiveBytes {
		return nil, errors.New("plugin HTTPS archive violates its size bound")
	}
	if digest(raw) != source.SHA256 {
		return nil, errors.New("plugin HTTPS archive does not match its SHA-256 pin")
	}
	return raw, nil
}

// FetchPinnedGitArchive fetches an exact commit into a temporary bare
// repository and reads one ordinary ZIP blob without checkout, hooks,
// submodules, credential helpers, or repository code execution.
func FetchPinnedGitArchive(ctx context.Context, repoURL, commit, archivePath,
	stagingRoot string,
) ([]byte, error) {
	repoURL, commit = strings.TrimSpace(repoURL), strings.TrimSpace(commit)
	source := InstallSource{Kind: "git", URI: repoURL, Commit: commit,
		SHA256: strings.Repeat("0", sha256.Size*2)}
	if err := source.Validate(); err != nil {
		return nil, err
	}
	return fetchPinnedGitArchive(ctx, repoURL, commit, archivePath, stagingRoot, false)
}

func fetchPinnedGitArchive(ctx context.Context, repoURL, commit, archivePath,
	stagingRoot string, allowLocalFixture bool,
) ([]byte, error) {
	archivePath = strings.TrimSpace(archivePath)
	if archivePath == "" {
		archivePath = "plugin.zip"
	}
	if !validGitArchivePath(archivePath) || !fixedGitCommit(commit) {
		return nil, errors.New("plugin Git import requires an exact commit and safe ZIP blob path")
	}
	if !allowLocalFixture {
		parsed, err := url.Parse(repoURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("plugin Git import requires a fixed credential-free HTTPS repository")
		}
	}
	if strings.TrimSpace(stagingRoot) == "" {
		stagingRoot = os.TempDir()
	}
	staging, err := os.MkdirTemp(stagingRoot, "prayu-plugin-git-")
	if err != nil {
		return nil, fmt.Errorf("prepare plugin Git staging: %w", err)
	}
	defer os.RemoveAll(staging)
	repository := filepath.Join(staging, "repository.git")
	hooksDir := filepath.Join(staging, "disabled-hooks")
	if err := os.Mkdir(hooksDir, 0o700); err != nil {
		return nil, err
	}
	importCtx, cancel := context.WithTimeout(ctx, MaxPluginImportDuration)
	defer cancel()
	protocolFile := "never"
	if allowLocalFixture {
		protocolFile = "always"
	}
	objectFormat := "sha1"
	if len(commit) == sha256.Size*2 {
		objectFormat = "sha256"
	}
	if _, err := runPluginGit(importCtx, hooksDir, "init", "--bare", "--quiet",
		"--object-format="+objectFormat, repository); err != nil {
		return nil, err
	}
	if _, err := runPluginGit(importCtx, hooksDir, "-C", repository,
		"-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
		"-c", "protocol.file.allow="+protocolFile, "-c", "http.followRedirects=false",
		"fetch", "--quiet", "--no-tags",
		"--depth=1", "--no-recurse-submodules", "--", repoURL, commit); err != nil {
		return nil, err
	}
	resolvedRaw, err := runPluginGit(importCtx, hooksDir, "-C", repository,
		"rev-parse", "FETCH_HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	if resolved := strings.TrimSpace(string(resolvedRaw)); resolved != commit {
		return nil, fmt.Errorf("plugin Git commit drift: resolved %q", resolved)
	}
	entryRaw, err := runPluginGit(importCtx, hooksDir, "-C", repository,
		"ls-tree", commit, "--", archivePath)
	if err != nil {
		return nil, err
	}
	metadataText, entryPath, found := strings.Cut(strings.TrimSpace(string(entryRaw)), "\t")
	metadata := strings.Fields(metadataText)
	if !found || len(metadata) != 3 || (metadata[0] != "100644" && metadata[0] != "100755") ||
		metadata[1] != "blob" || entryPath != archivePath {
		return nil, errors.New("plugin Git archive path is absent or is not an ordinary blob")
	}
	raw, err := runPluginGitBounded(importCtx, hooksDir, MaxArchiveBytes+1,
		"-C", repository, "show", commit+":"+archivePath)
	if err != nil {
		return nil, err
	}
	if len(raw) < 1 || len(raw) > MaxArchiveBytes {
		return nil, errors.New("plugin Git archive violates its size bound")
	}
	return raw, nil
}

func runPluginGit(ctx context.Context, hooksDir string, args ...string) ([]byte, error) {
	return runPluginGitBounded(ctx, hooksDir, pluginGitDiagnosticBytes, args...)
}

func runPluginGitBounded(ctx context.Context, hooksDir string, limit int,
	args ...string,
) ([]byte, error) {
	prefix := []string{"-c", "core.hooksPath=" + hooksDir, "-c", "credential.helper="}
	command := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	command.Env = pluginGitEnvironment()
	stdout, stderr := &pluginImportBuffer{limit: limit},
		&pluginImportBuffer{limit: pluginGitDiagnosticBytes}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		message := boundedPluginImportMessage(stderr.String())
		if message == "" {
			return nil, fmt.Errorf("plugin Git import failed: %w", err)
		}
		return nil, fmt.Errorf("plugin Git import failed: %w: %s", err, message)
	}
	if stdout.overflow {
		return nil, errors.New("plugin Git output exceeds its bound")
	}
	return bytes.Clone(stdout.value), nil
}

func pluginGitEnvironment() []string {
	allowed := map[string]struct{}{"PATH": {}, "PATHEXT": {}, "SYSTEMROOT": {},
		"WINDIR": {}, "TMP": {}, "TEMP": {}, "TMPDIR": {}}
	result := make([]string, 0, len(allowed)+4)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, found := allowed[strings.ToUpper(name)]; found {
			result = append(result, entry)
		}
	}
	return append(result, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_OPTIONAL_LOCKS=0")
}

func validGitArchivePath(value string) bool {
	return value != "" && len(value) <= 512 && strings.HasSuffix(value, ".zip") &&
		value == path.Clean(value) && !strings.HasPrefix(value, "/") &&
		!strings.HasPrefix(value, "../") && !strings.Contains(value, "/../") &&
		!strings.ContainsAny(value, "\\:\x00")
}

func boundedPluginImportMessage(value string) string {
	value = strings.TrimSpace(redact.String(strings.ToValidUTF8(value, "?")))
	if len(value) > 2048 {
		value = value[:2048]
		for len(value) > 0 && !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

type pluginImportBuffer struct {
	limit    int
	value    []byte
	overflow bool
}

func (b *pluginImportBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := b.limit - len(b.value)
	if remaining > 0 {
		if len(value) > remaining {
			b.overflow = true
			value = value[:remaining]
		}
		b.value = append(b.value, value...)
	} else if len(value) > 0 {
		b.overflow = true
	}
	return written, nil
}

func (b *pluginImportBuffer) String() string { return string(b.value) }
