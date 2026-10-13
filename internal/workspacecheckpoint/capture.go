package workspacecheckpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/workspaceidentity"
)

const maxGitCaptureOutputBytes = 32 * 1024 * 1024

type gitIndexEntry struct {
	OID   string
	Mode  string
	Stage string
}

type captureInventory struct {
	baseCommit string
	branch     string
	indexPath  string
	tracked    map[string]gitIndexEntry
	untracked  map[string]struct{}
	ignored    map[string]struct{}
	staged     map[string]struct{}
	git        bool
	unmerged   bool
	truncated  bool
}

// Capture builds a deterministic, bounded snapshot without modifying either
// the Workspace or its Git index. Raw content is returned only as verified,
// content-addressed blobs for the store to commit transactionally.
func Capture(ctx context.Context, request CaptureRequest) (Snapshot, error) {
	if ctx == nil || ctx.Err() != nil {
		return Snapshot{}, context.Canceled
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = time.Now().UTC()
	}
	if err := request.Validate(); err != nil {
		return Snapshot{}, err
	}
	root, err := canonicalRoot(request.WorkspaceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open Workspace root: %w", err)
	}
	defer rootHandle.Close()
	rootFingerprint, err := workspaceidentity.Fingerprint(root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("capture Workspace identity: %w", err)
	}
	rootPathDigest := sha256.Sum256([]byte(canonicalPathIdentity(root)))
	inventory, err := inspectCaptureInventory(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}

	reasons := make(map[string]struct{})
	level := RecoveryComplete
	for _, reason := range request.IncompleteReasons {
		reasons[reason] = struct{}{}
		level = RecoveryPartial
	}
	if !inventory.git {
		level = RecoveryUnavailable
		reasons["workspace is not an exact Git worktree"] = struct{}{}
	}
	if inventory.truncated {
		level = RecoveryUnavailable
		reasons["workspace manifest exceeds the entry limit"] = struct{}{}
	}
	if inventory.unmerged {
		if level == RecoveryComplete {
			level = RecoveryPartial
		}
		reasons["Git index contains unmerged stages; the raw index remains exact but manifest index metadata projects ours"] = struct{}{}
	}

	blobByDigest := make(map[string]Blob)
	storedBytes := int64(0)
	indexSHA := sha256.Sum256(nil)
	indexBlobSHA := ""
	if inventory.git && inventory.indexPath != "" {
		indexRaw, readErr := os.ReadFile(inventory.indexPath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return Snapshot{}, fmt.Errorf("read Git index: %w", readErr)
		}
		indexSHA = sha256.Sum256(indexRaw)
		if errors.Is(readErr, os.ErrNotExist) {
			// A missing index is a legitimate, exactly recoverable state for a
			// newly initialized repository. The empty digest plus an absent blob
			// distinguishes it from an existing zero-byte index.
		} else if len(indexRaw) <= MaxStoredIndexBytes && int64(len(indexRaw)) <= MaxCheckpointBlobBytes {
			indexBlobSHA = addCaptureBlob(blobByDigest, indexRaw, request.CreatedAt)
			storedBytes = captureBlobBytes(blobByDigest)
		} else {
			if level == RecoveryComplete {
				level = RecoveryPartial
			}
			reasons["Git index exceeds the checkpoint limit"] = struct{}{}
		}
	}

	paths := capturePaths(inventory)
	entries := make([]Entry, 0, len(paths))
	casePaths := make(map[string]string, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		caseKey := path
		if runtime.GOOS == "windows" {
			caseKey = strings.ToLower(path)
		}
		if previous, exists := casePaths[caseKey]; exists && previous != path {
			level = RecoveryUnavailable
			reasons["case-colliding Workspace paths cannot be restored safely"] = struct{}{}
		}
		casePaths[caseKey] = path
		if runtime.GOOS == "windows" {
			actualPath, exists, caseErr := actualCapturePathCase(rootHandle, path)
			if caseErr != nil {
				return Snapshot{}, caseErr
			}
			if exists && actualPath != path {
				level = RecoveryUnavailable
				reasons["Workspace path casing differs from the Git index"] = struct{}{}
			}
		}
		entry, content, incomplete, entryErr := captureEntry(ctx, rootHandle, path, inventory)
		if entryErr != nil {
			return Snapshot{}, entryErr
		}
		if incomplete != "" {
			if level == RecoveryComplete {
				level = RecoveryPartial
			}
			reasons[incomplete] = struct{}{}
		}
		if content != nil {
			digest := sha256.Sum256(content)
			additionalBytes := int64(len(content))
			if _, exists := blobByDigest[hex.EncodeToString(digest[:])]; exists {
				additionalBytes = 0
			}
			if storedBytes+additionalBytes > MaxCheckpointBlobBytes {
				entry.BlobSHA256 = ""
				entry.StoragePolicy = StorageExcludedLarge
				entry.Recoverable = false
				entry.Reason = "checkpoint blob quota exceeded"
				if level == RecoveryComplete {
					level = RecoveryPartial
				}
				reasons["checkpoint blob quota excluded recoverable content"] = struct{}{}
			} else {
				entry.BlobSHA256 = addCaptureBlob(blobByDigest, content, request.CreatedAt)
				storedBytes = captureBlobBytes(blobByDigest)
			}
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	manifestJSON, err := json.Marshal(entries)
	if err != nil {
		return Snapshot{}, err
	}
	manifestSHA := sha256.Sum256(manifestJSON)
	incompleteReasons := make([]string, 0, len(reasons))
	for reason := range reasons {
		incompleteReasons = append(incompleteReasons, reason)
	}
	sort.Strings(incompleteReasons)
	blobs := make([]Blob, 0, len(blobByDigest))
	for _, blob := range blobByDigest {
		blobs = append(blobs, blob)
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].SHA256 < blobs[j].SHA256 })
	checkpoint := Checkpoint{ID: request.ID, ProtocolVersion: ProtocolVersion,
		RunID: request.RunID, MissionID: request.MissionID, SessionID: request.SessionID,
		WorkspaceID: request.WorkspaceID, AttemptID: request.AttemptID,
		CapabilityGeneration: request.CapabilityGeneration, Trigger: request.Trigger,
		Phase: request.Phase, TriggerReceiptID: request.TriggerReceiptID,
		RequestedBy: request.RequestedBy, Title: request.Title,
		ParentCheckpointID: request.ParentCheckpointID,
		RootFingerprint:    rootFingerprint, RootPathSHA256: hex.EncodeToString(rootPathDigest[:]),
		BaseCommit: inventory.baseCommit, Branch: inventory.branch,
		IndexSHA256: hex.EncodeToString(indexSHA[:]), IndexBlobSHA256: indexBlobSHA,
		ManifestSHA256: hex.EncodeToString(manifestSHA[:]), RecoveryLevel: level,
		IncompleteReasons: incompleteReasons, EntryCount: len(entries),
		StoredBytes: storedBytes, CreatedAt: request.CreatedAt.UTC()}
	snapshot := Snapshot{Checkpoint: checkpoint, Entries: entries, Blobs: blobs}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func actualCapturePathCase(root *os.Root, normalized string) (string, bool, error) {
	parts := strings.Split(normalized, "/")
	current := "."
	actual := make([]string, 0, len(parts))
	for _, part := range parts {
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("inspect Workspace path casing: %w", err)
		}
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return "", false, nil
		}
		directory, err := root.Open(current)
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("inspect Workspace path casing: %w", err)
		}
		entries, readErr := directory.ReadDir(-1)
		if err := errors.Join(readErr, directory.Close()); err != nil {
			return "", false, fmt.Errorf("inspect Workspace path casing: %w", err)
		}
		matched := ""
		for _, entry := range entries {
			if entry.Name() == part {
				matched = entry.Name()
				break
			}
			if matched == "" && strings.EqualFold(entry.Name(), part) {
				matched = entry.Name()
			}
		}
		if matched == "" {
			return "", false, nil
		}
		actual = append(actual, matched)
		current = filepath.Join(current, matched)
	}
	return strings.Join(actual, "/"), true, nil
}

func canonicalRoot(value string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	requestedInfo, err := os.Lstat(abs)
	if err != nil || !requestedInfo.IsDir() || requestedInfo.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return "", errors.New("workspace checkpoint root identity is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || !sameCapturePath(abs, resolved) {
		return "", errors.New("workspace checkpoint root identity is redirected")
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return "", errors.New("workspace checkpoint root must be a real directory")
	}
	return filepath.Clean(resolved), nil
}

func canonicalPathIdentity(value string) string {
	value = filepath.ToSlash(filepath.Clean(value))
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

func inspectCaptureInventory(ctx context.Context, root string) (captureInventory, error) {
	result := captureInventory{baseCommit: "non-git", tracked: map[string]gitIndexEntry{},
		untracked: map[string]struct{}{}, ignored: map[string]struct{}{},
		staged: map[string]struct{}{}}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		paths, truncated, walkErr := walkNonGitFiles(ctx, root)
		if walkErr != nil {
			return result, walkErr
		}
		for _, path := range paths {
			result.untracked[path] = struct{}{}
		}
		result.truncated = truncated
		return result, nil
	}
	top, err := captureGitOutput(ctx, gitPath, root, "rev-parse", "--show-toplevel")
	if err != nil || !sameCapturePath(strings.TrimSpace(string(top)), root) {
		paths, truncated, walkErr := walkNonGitFiles(ctx, root)
		if walkErr != nil {
			return result, walkErr
		}
		for _, path := range paths {
			result.untracked[path] = struct{}{}
		}
		result.truncated = truncated
		return result, nil
	}
	result.git = true
	if head, headErr := captureGitOutput(ctx, gitPath, root, "rev-parse", "--verify", "HEAD"); headErr == nil {
		result.baseCommit = strings.TrimSpace(string(head))
	} else {
		result.baseCommit = "unborn"
	}
	if branch, branchErr := captureGitOutput(ctx, gitPath, root, "branch", "--show-current"); branchErr == nil {
		result.branch = strings.TrimSpace(string(branch))
	}
	indexPath, err := captureGitOutput(ctx, gitPath, root, "rev-parse", "--git-path", "index")
	if err != nil {
		return result, fmt.Errorf("locate Git index: %w", err)
	}
	result.indexPath = strings.TrimSpace(string(indexPath))
	if !filepath.IsAbs(result.indexPath) {
		result.indexPath = filepath.Join(root, result.indexPath)
	}
	result.indexPath = filepath.Clean(result.indexPath)

	tracked, err := captureGitOutput(ctx, gitPath, root, "ls-files", "-s", "-z")
	if err != nil {
		return result, err
	}
	for _, raw := range splitNUL(tracked) {
		tab := strings.IndexByte(raw, '\t')
		if tab <= 0 {
			continue
		}
		fields := strings.Fields(raw[:tab])
		path, pathErr := normalizeCapturePath(raw[tab+1:])
		if len(fields) != 3 || pathErr != nil ||
			(fields[2] != "0" && fields[2] != "1" && fields[2] != "2" && fields[2] != "3") {
			return result, errors.New("git index contains an unsupported or unsafe entry")
		}
		candidate := gitIndexEntry{Mode: fields[0], OID: fields[1], Stage: fields[2]}
		if fields[2] == "0" {
			result.tracked[path] = candidate
		} else {
			result.unmerged = true
			// The raw index blob is the restoration authority. For the bounded
			// manifest projection prefer stage 2 (ours), then base/theirs when
			// ours is absent, so each path remains unique and inspectable.
			current, exists := result.tracked[path]
			if !exists || fields[2] == "2" || current.Stage == "1" && fields[2] == "3" {
				result.tracked[path] = candidate
			}
		}
		if len(result.tracked) > MaxEntries {
			result.truncated = true
			break
		}
	}
	if !result.truncated {
		untracked, listErr := captureGitOutput(ctx, gitPath, root,
			"ls-files", "-z", "--others", "--exclude-standard")
		if listErr != nil {
			return result, listErr
		}
		for _, raw := range splitNUL(untracked) {
			path, pathErr := normalizeCapturePath(raw)
			if pathErr != nil {
				return result, pathErr
			}
			result.untracked[path] = struct{}{}
			if len(result.tracked)+len(result.untracked) > MaxEntries {
				result.truncated = true
				break
			}
		}
	}
	ignored, _ := captureGitOutput(ctx, gitPath, root,
		"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	for _, raw := range splitNUL(ignored) {
		path, pathErr := normalizeCapturePath(strings.TrimSuffix(raw, "/"))
		if pathErr == nil && path != "" {
			result.ignored[path] = struct{}{}
		}
		if len(result.tracked)+len(result.untracked)+len(result.ignored) >= MaxEntries {
			result.truncated = true
			break
		}
	}
	staged, _ := captureGitOutput(ctx, gitPath, root,
		"diff", "--cached", "--name-only", "-z", "--diff-filter=ACDMRTUXB")
	for _, raw := range splitNUL(staged) {
		if path, pathErr := normalizeCapturePath(raw); pathErr == nil {
			result.staged[path] = struct{}{}
		}
	}
	return result, nil
}

func capturePaths(inventory captureInventory) []string {
	paths := make([]string, 0, len(inventory.tracked)+len(inventory.untracked)+len(inventory.ignored))
	seen := make(map[string]struct{}, cap(paths))
	for path := range inventory.tracked {
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	for path := range inventory.untracked {
		if _, exists := seen[path]; !exists {
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	for path := range inventory.ignored {
		if _, exists := seen[path]; !exists {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	if len(paths) > MaxEntries {
		paths = paths[:MaxEntries]
	}
	return paths
}

func captureEntry(ctx context.Context, root *os.Root, path string,
	inventory captureInventory,
) (Entry, []byte, string, error) {
	index, tracked := inventory.tracked[path]
	_, staged := inventory.staged[path]
	entry := Entry{Path: path, Kind: EntryFile, State: StatePresent,
		StoragePolicy: StorageStored, WorktreeSHA256: "missing", Tracked: tracked,
		Staged: staged, IndexOID: index.OID, IndexMode: index.Mode, Recoverable: true}
	if _, ignored := inventory.ignored[path]; ignored && !tracked {
		entry.Kind = EntryDirectory
		entry.State = StateIgnored
		entry.StoragePolicy = StorageExcludedIgnored
		entry.Recoverable = false
		entry.Reason = "ignored content is outside automatic restore scope"
		return entry, nil, "ignored Workspace content is not captured", nil
	}
	info, linked, err := capturePathInfo(root, path)
	if errors.Is(err, os.ErrNotExist) && tracked {
		entry.State = StateMissing
		entry.StoragePolicy = StorageMissing
		entry.Recoverable = true
		return entry, nil, "", nil
	}
	if err != nil {
		entry.State = StateExternal
		entry.StoragePolicy = StorageUnreadable
		entry.Recoverable = false
		entry.Reason = "Workspace entry could not be read"
		return entry, nil, "unreadable Workspace content is not captured", nil
	}
	if linked {
		entry.StoragePolicy = StorageExcludedLink
		entry.Recoverable = false
		entry.Reason = "path contains a symlink or junction requiring manual review"
		if info != nil {
			entry.Kind = EntrySymlink
			entry.Mode = uint32(info.Mode().Perm())
			entry.Size = info.Size()
		}
		return entry, nil, "linked Workspace content is not captured", nil
	}
	entry.Mode = uint32(info.Mode().Perm())
	entry.Size = info.Size()
	if info.IsDir() {
		entry.Kind = EntryDirectory
		entry.StoragePolicy = StorageExcludedSpecial
		entry.Recoverable = false
		entry.Reason = "directory metadata is informational"
		return entry, nil, "directory content is not directly recoverable", nil
	}
	if !info.Mode().IsRegular() {
		entry.Kind = EntryOther
		entry.StoragePolicy = StorageExcludedSpecial
		entry.Recoverable = false
		entry.Reason = "special filesystem entry is unsupported"
		return entry, nil, "special Workspace content is not captured", nil
	}
	digest, content, err := hashCaptureFile(ctx, root, path, info.Size() <= MaxStoredFileBytes)
	if err != nil {
		entry.StoragePolicy = StorageUnreadable
		entry.Recoverable = false
		entry.Reason = "Workspace file could not be read consistently"
		return entry, nil, "unreadable Workspace content is not captured", nil
	}
	entry.WorktreeSHA256 = digest
	if content != nil {
		entry.Binary = looksBinary(content)
		entry.LineEndings = detectLineEndings(content, entry.Binary)
	}
	if sensitiveCapturePath(path) || sensitiveCaptureContent(content) {
		entry.StoragePolicy = StorageExcludedSensitive
		entry.Recoverable = false
		entry.Reason = "sensitive content is hash-only and never persisted"
		return entry, nil, "sensitive Workspace content is excluded", nil
	}
	if info.Size() > MaxStoredFileBytes {
		entry.StoragePolicy = StorageExcludedLarge
		entry.Recoverable = false
		entry.Reason = "file exceeds the per-blob checkpoint limit"
		return entry, nil, "large Workspace content is hash-only", nil
	}
	if !tracked && generatedCapturePath(path) {
		entry.State = StateGenerated
		entry.StoragePolicy = StorageExcludedGenerated
		entry.Recoverable = false
		entry.Reason = "generated untracked content is not persisted"
		return entry, nil, "generated Workspace content is not captured", nil
	}
	entry.BlobSHA256 = digest
	return entry, content, "", nil
}

func capturePathInfo(root *os.Root, relative string) (os.FileInfo, bool, error) {
	parts := strings.Split(relative, "/")
	current := "."
	var info os.FileInfo
	for index, part := range parts {
		current = filepath.Join(current, part)
		var err error
		info, err = root.Lstat(current)
		if err != nil {
			return nil, false, err
		}
		// Windows junctions are ModeIrregular. Never traverse them, including
		// links to another directory inside this Workspace.
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			if index != len(parts)-1 {
				info = nil
			}
			return info, true, nil
		}
	}
	return info, false, nil
}

func hashCaptureFile(ctx context.Context, root *os.Root, path string, keep bool) (string, []byte, error) {
	// Keep the actual read beneath the same held root even if a parent is
	// replaced by a link after capturePathInfo inspected it.
	file, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	hash := sha256.New()
	var buffer bytes.Buffer
	writer := io.Writer(hash)
	if keep {
		writer = io.MultiWriter(hash, &buffer)
	}
	chunk := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		count, readErr := file.Read(chunk)
		if count > 0 {
			if _, err := writer.Write(chunk[:count]); err != nil {
				return "", nil, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", nil, readErr
		}
	}
	if !keep {
		return hex.EncodeToString(hash.Sum(nil)), nil, nil
	}
	if buffer.Len() == 0 {
		// nil means hash-only capture to the caller; a retained empty file
		// still needs a content-addressed blob and must remain recoverable.
		return hex.EncodeToString(hash.Sum(nil)), []byte{}, nil
	}
	return hex.EncodeToString(hash.Sum(nil)), buffer.Bytes(), nil
}

func addCaptureBlob(values map[string]Blob, content []byte, createdAt time.Time) string {
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if _, exists := values[digest]; !exists {
		values[digest] = Blob{SHA256: digest, Content: append([]byte{}, content...),
			CreatedAt: createdAt.UTC()}
	}
	return digest
}

func captureBlobBytes(values map[string]Blob) int64 {
	var total int64
	for _, value := range values {
		total += int64(len(value.Content))
	}
	return total
}

func walkNonGitFiles(ctx context.Context, root string) ([]string, bool, error) {
	values := make([]string, 0, 256)
	truncated := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if sameCapturePath(path, root) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if len(values) >= MaxEntries {
			truncated = true
			return filepath.SkipAll
		}
		normalized, err := normalizeCapturePath(relative)
		if err != nil {
			return err
		}
		values = append(values, normalized)
		return nil
	})
	sort.Strings(values)
	return values, truncated, err
}

func captureGitOutput(ctx context.Context, gitPath, root string, args ...string) ([]byte, error) {
	base := []string{"-C", root, "--no-optional-locks", "-c", "core.autocrlf=false",
		"-c", "core.hooksPath=", "-c", "diff.external=", "-c", "core.fsmonitor=false"}
	command := checkpointCommandContext(ctx, gitPath, append(base, args...)...)
	command.Dir = root
	command.Env = captureGitEnvironment()
	var stdout, stderr limitedCaptureBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err,
			strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded {
		return nil, errors.New("git checkpoint inventory exceeds its output limit")
	}
	return append([]byte{}, stdout.Bytes()...), nil
}

type limitedCaptureBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *limitedCaptureBuffer) Write(value []byte) (int, error) {
	remaining := maxGitCaptureOutputBytes - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = b.Buffer.Write(value[:remaining])
		b.exceeded = true
		return len(value), nil
	}
	return b.Buffer.Write(value)
}

func captureGitEnvironment() []string {
	keys := []string{"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TMP", "TEMP"}
	values := make([]string, 0, len(keys)+8)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			values = append(values, key+"="+value)
		}
	}
	values = append(values, "LANG=C", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_PAGER=cat", "GIT_EDITOR=true", "GIT_OPTIONAL_LOCKS=0")
	return values
}

func splitNUL(value []byte) []string {
	parts := bytes.Split(value, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			result = append(result, string(part))
		}
	}
	return result
}

func normalizeCapturePath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) || !utf8.ValidString(value) ||
		filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return "", errors.New("workspace checkpoint path is invalid")
	}
	cleaned := filepath.ToSlash(filepath.Clean(value))
	if cleaned != value || !validPath(cleaned) || cleaned == ".git" ||
		strings.HasPrefix(cleaned, ".git/") {
		return "", errors.New("workspace checkpoint path is not normalized")
	}
	return cleaned, nil
}

func sameCapturePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func sensitiveCapturePath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(lower))
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "credentials" ||
		base == "credentials.json" || base == "id_rsa" || base == "id_ed25519" ||
		strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".p12") ||
		strings.HasSuffix(base, ".pfx") || strings.HasSuffix(base, ".key") {
		return true
	}
	return strings.Contains(lower, "/.ssh/") || strings.Contains(lower, "/.aws/") ||
		strings.Contains(lower, "/.azure/") || strings.Contains(lower, "/.config/gcloud/")
}

func sensitiveCaptureContent(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	upper := strings.ToUpper(string(value))
	if strings.Contains(upper, "-----BEGIN PRIVATE KEY-----") ||
		strings.Contains(upper, "-----BEGIN OPENSSH PRIVATE KEY-----") ||
		strings.Contains(upper, "-----BEGIN RSA PRIVATE KEY-----") {
		return true
	}
	if utf8.Valid(value) {
		text := string(value)
		return redact.String(text) != text
	}
	return false
}

func generatedCapturePath(path string) bool {
	parts := strings.Split(strings.ToLower(filepath.ToSlash(path)), "/")
	for _, part := range parts {
		switch part {
		case "node_modules", "dist", "build", "target", "coverage", ".cache", ".next",
			"bin", "obj", "vendor":
			return true
		}
	}
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".tmp" || ext == ".log" || ext == ".pyc"
}

func looksBinary(value []byte) bool {
	if bytes.IndexByte(value, 0) >= 0 || !utf8.Valid(value) {
		return true
	}
	control := 0
	for _, current := range value {
		if current < 0x20 && current != '\n' && current != '\r' && current != '\t' {
			control++
		}
	}
	return len(value) > 0 && control*100/len(value) > 1
}

func detectLineEndings(value []byte, binary bool) string {
	if binary {
		return ""
	}
	crlf := bytes.Count(value, []byte("\r\n"))
	lf := bytes.Count(value, []byte("\n")) - crlf
	switch {
	case crlf > 0 && lf > 0:
		return "mixed"
	case crlf > 0:
		return "crlf"
	case lf > 0:
		return "lf"
	default:
		return "none"
	}
}

// ParseFileMode is exposed for restore and tests; persisted modes are always
// bounded permission bits rather than platform-specific os.FileMode flags.
func ParseFileMode(value uint32) (os.FileMode, error) {
	if value > 0o7777 {
		return 0, errors.New("workspace checkpoint file mode is invalid")
	}
	parsed, err := strconv.ParseUint(fmt.Sprintf("%o", value), 8, 32)
	return os.FileMode(parsed), err
}
