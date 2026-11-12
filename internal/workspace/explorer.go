package workspace

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/session"
)

const (
	ExplorerProtocolVersion   = "workspace_explorer.v1"
	MaxExplorerPathRunes      = 512
	MaxExplorerEntries        = 200
	MaxExplorerScanEntries    = 400
	MaxExplorerReadBytes      = 64 * 1024
	MaxExplorerProjectedBytes = 2 * MaxExplorerReadBytes
)

type ExplorerEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
	Readable  bool   `json:"readable"`
}

type ExplorerProvenance struct {
	Version               string `json:"version"`
	SourceKind            string `json:"source_kind"`
	SourceRef             string `json:"source_ref"`
	ContentSHA256         string `json:"content_sha256"`
	InstructionAuthorized bool   `json:"instruction_authorized"`
}

type ExplorerSnapshot struct {
	ProtocolVersion string             `json:"protocol_version"`
	WorkspaceID     string             `json:"workspace_id"`
	Path            string             `json:"path"`
	Kind            string             `json:"kind"`
	Entries         []ExplorerEntry    `json:"entries"`
	Content         string             `json:"content"`
	TotalBytes      int64              `json:"total_bytes"`
	ReturnedBytes   int                `json:"returned_bytes"`
	Truncated       bool               `json:"truncated"`
	RedactionCount  int                `json:"redaction_count"`
	RootPathExposed bool               `json:"root_path_exposed"`
	Provenance      ExplorerProvenance `json:"provenance"`
}

func Explore(root string, workspaceID string, requested string) (ExplorerSnapshot, error) {
	if workspaceID == "" || workspaceID != strings.TrimSpace(workspaceID) ||
		strings.ContainsRune(workspaceID, 0) {
		return ExplorerSnapshot{}, apperror.New(apperror.CodeInvalidArgument,
			"workspace identity is invalid")
	}
	relative, err := normalizeExplorerPath(requested)
	if err != nil {
		return ExplorerSnapshot{}, err
	}
	target, info, err := resolveExplorerTarget(root, relative)
	if err != nil {
		return ExplorerSnapshot{}, err
	}
	if info.IsDir() {
		return exploreDirectory(target, workspaceID, relative)
	}
	if !info.Mode().IsRegular() {
		return ExplorerSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace entry is not a regular file or directory")
	}
	return exploreFile(target, info, workspaceID, relative)
}

func normalizeExplorerPath(requested string) (string, error) {
	if requested == "" {
		requested = "."
	}
	if !utf8.ValidString(requested) || utf8.RuneCountInString(requested) > MaxExplorerPathRunes ||
		strings.ContainsRune(requested, 0) || filepath.IsAbs(requested) ||
		filepath.VolumeName(requested) != "" || strings.ContainsAny(requested, `\:`) {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"workspace explorer path must be a bounded relative path")
	}
	for _, current := range requested {
		if unicode.IsControl(current) {
			return "", apperror.New(apperror.CodeInvalidArgument,
				"workspace explorer path cannot contain control characters")
		}
	}
	cleaned := filepath.Clean(filepath.FromSlash(requested))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", apperror.New(apperror.CodePolicyDenied,
			"workspace explorer path cannot leave the workspace")
	}
	canonical := filepath.ToSlash(cleaned)
	if canonical != requested || !filepath.IsLocal(cleaned) {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"workspace explorer path must be canonical")
	}
	return canonical, nil
}

func resolveExplorerTarget(root string, relative string) (string, os.FileInfo, error) {
	if root == "" {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace root is unavailable")
	}
	resolvedRoot, err := rootfs.ExactAbsolutePath(root)
	if err != nil {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace root could not be resolved")
	}
	resolvedRoot, err = filepath.EvalSymlinks(resolvedRoot)
	if err != nil {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace root could not be resolved")
	}
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil || !rootInfo.IsDir() {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace root is unavailable")
	}
	target, err := rootfs.ExactAbsolutePath(filepath.Join(resolvedRoot, filepath.FromSlash(relative)))
	if err != nil || !explorerWithinRoot(resolvedRoot, target) {
		return "", nil, apperror.New(apperror.CodePolicyDenied,
			"workspace explorer path cannot leave the workspace")
	}
	current := resolvedRoot
	if relative != "." {
		for _, component := range strings.Split(filepath.FromSlash(relative), string(os.PathSeparator)) {
			current, err = rootfs.ExactAbsolutePath(filepath.Join(current, component))
			if err != nil {
				return "", nil, apperror.New(apperror.CodeFailedPrecondition,
					"workspace explorer entry could not be resolved")
			}
			info, lookupErr := os.Lstat(current)
			if os.IsNotExist(lookupErr) {
				return "", nil, apperror.New(apperror.CodeNotFound,
					"workspace explorer entry was not found")
			}
			if lookupErr != nil {
				return "", nil, apperror.New(apperror.CodeFailedPrecondition,
					"workspace explorer entry could not be inspected")
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return "", nil, apperror.New(apperror.CodePolicyDenied,
					"workspace explorer does not follow symbolic links")
			}
		}
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, apperror.New(apperror.CodeNotFound,
				"workspace explorer entry was not found")
		}
		return "", nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace explorer entry could not be resolved")
	}
	if !explorerWithinRoot(resolvedRoot, resolvedTarget) ||
		!sameExplorerPath(target, resolvedTarget) {
		return "", nil, apperror.New(apperror.CodePolicyDenied,
			"workspace explorer does not follow redirected paths")
	}
	// Keep the native prefix when opening or inspecting a Windows path.
	info, err := os.Lstat(target)
	if err != nil {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace explorer entry could not be inspected")
	}
	return target, info, nil
}

func exploreDirectory(target string, workspaceID string,
	relative string,
) (ExplorerSnapshot, error) {
	directory, err := os.Open(target)
	if err != nil {
		return ExplorerSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace directory could not be opened")
	}
	defer directory.Close()
	raw, err := directory.ReadDir(MaxExplorerScanEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return ExplorerSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace directory could not be listed")
	}
	truncated := len(raw) > MaxExplorerScanEntries
	if len(raw) > MaxExplorerScanEntries {
		raw = raw[:MaxExplorerScanEntries]
	}
	sort.Slice(raw, func(left, right int) bool { return raw[left].Name() < raw[right].Name() })
	entries := make([]ExplorerEntry, 0, min(len(raw), MaxExplorerEntries))
	redactionCount := 0
	for _, entry := range raw {
		if strings.HasPrefix(entry.Name(), ".cyberagent-edit-") {
			continue
		}
		if !safeExplorerName(entry.Name()) {
			truncated = true
			continue
		}
		redactedName := redact.Text(entry.Name())
		if redactedName.Text != entry.Name() {
			redactionCount += findingCount(redactedName.Findings)
			truncated = true
			continue
		}
		if len(entries) >= MaxExplorerEntries {
			truncated = true
			break
		}
		kind, readable, size := "blocked", false, int64(0)
		if entry.Type()&os.ModeSymlink == 0 {
			info, infoErr := entry.Info()
			if infoErr == nil && info.IsDir() {
				kind, readable = "directory", true
			} else if infoErr == nil && info.Mode().IsRegular() {
				kind, readable, size = "file", true, info.Size()
			}
		}
		entryPath := filepath.ToSlash(filepath.Join(filepath.FromSlash(relative), entry.Name()))
		if relative == "." {
			entryPath = filepath.ToSlash(entry.Name())
		}
		if normalized, pathErr := normalizeExplorerPath(entryPath); pathErr != nil ||
			normalized != entryPath {
			truncated = true
			continue
		}
		entries = append(entries, ExplorerEntry{Name: entry.Name(), Path: entryPath,
			Kind: kind, SizeBytes: size, Readable: readable})
	}
	projection, _ := json.Marshal(struct {
		Entries   []ExplorerEntry `json:"entries"`
		Truncated bool            `json:"truncated"`
	}{Entries: entries, Truncated: truncated})
	return ExplorerSnapshot{
		ProtocolVersion: ExplorerProtocolVersion, WorkspaceID: workspaceID,
		Path: relative, Kind: "directory", Entries: entries, Content: "",
		Truncated: truncated, RedactionCount: redactionCount, RootPathExposed: false,
		Provenance: explorerProvenance(session.SourceWorkspaceList, relative,
			string(projection)),
	}, nil
}

func exploreFile(target string, expected os.FileInfo, workspaceID string,
	relative string,
) (ExplorerSnapshot, error) {
	file, err := os.Open(target)
	if err != nil {
		return ExplorerSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace file could not be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return ExplorerSnapshot{}, apperror.New(apperror.CodePolicyDenied,
			"workspace file changed while it was being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxExplorerReadBytes+utf8.UTFMax))
	if err != nil {
		return ExplorerSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace file could not be read")
	}
	truncated := len(data) > MaxExplorerReadBytes
	if truncated {
		data, err = validExplorerUTF8Prefix(data, MaxExplorerReadBytes)
		if err != nil {
			return ExplorerSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
				"workspace explorer only previews UTF-8 text files")
		}
	}
	if !utf8.Valid(data) {
		return ExplorerSnapshot{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace explorer only previews UTF-8 text files")
	}
	redacted := redact.Text(string(data))
	projected := []byte(redacted.Text)
	if len(projected) > MaxExplorerProjectedBytes {
		projected, err = validExplorerUTF8Prefix(projected, MaxExplorerProjectedBytes)
		if err != nil {
			return ExplorerSnapshot{}, apperror.New(apperror.CodeInternal,
				"workspace explorer redacted projection is invalid")
		}
		redacted.Text = string(projected)
		truncated = true
	}
	return ExplorerSnapshot{
		ProtocolVersion: ExplorerProtocolVersion, WorkspaceID: workspaceID,
		Path: relative, Kind: "file", Entries: []ExplorerEntry{}, Content: redacted.Text,
		TotalBytes: opened.Size(), ReturnedBytes: len(projected),
		Truncated: truncated, RedactionCount: findingCount(redacted.Findings),
		RootPathExposed: false,
		Provenance: explorerProvenance(session.SourceWorkspaceFile, relative,
			redacted.Text),
	}, nil
}

func explorerProvenance(sourceKind string, sourceRef string,
	content string,
) ExplorerProvenance {
	return ExplorerProvenance{Version: session.ContextProvenanceVersion,
		SourceKind: sourceKind, SourceRef: sourceRef,
		ContentSHA256: session.ContentSHA256(content), InstructionAuthorized: false}
}

func validExplorerUTF8Prefix(data []byte, limit int) ([]byte, error) {
	if len(data) <= limit {
		if !utf8.Valid(data) {
			return nil, errors.New("file is not valid UTF-8 text")
		}
		return data, nil
	}
	prefix := data[:limit]
	if utf8.Valid(prefix) {
		return prefix, nil
	}
	start := len(prefix) - 1
	for start > 0 && !utf8.RuneStart(prefix[start]) {
		start--
	}
	if !utf8.Valid(prefix[:start]) {
		return nil, errors.New("file is not valid UTF-8 text")
	}
	current, size := utf8.DecodeRune(data[start:])
	if current == utf8.RuneError && size == 1 || start+size <= limit {
		return nil, errors.New("file is not valid UTF-8 text")
	}
	return prefix[:start], nil
}

func safeExplorerName(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 {
		return false
	}
	if strings.ContainsAny(value, `/\:`) || !filepath.IsLocal(value) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func findingCount(values []redact.Finding) int {
	total := 0
	for _, value := range values {
		total += value.Count
	}
	return total
}

func explorerWithinRoot(root string, candidate string) bool {
	relative, err := filepath.Rel(rootfs.OrdinaryPath(root), rootfs.OrdinaryPath(candidate))
	return err == nil && (relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))))
}

func sameExplorerPath(left string, right string) bool {
	left = filepath.Clean(rootfs.OrdinaryPath(left))
	right = filepath.Clean(rootfs.OrdinaryPath(right))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
