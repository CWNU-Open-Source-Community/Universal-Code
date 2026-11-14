package readonlyfanout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/rootfs"
)

type BuildPlanRequest struct {
	PlanID        string
	RunID         string
	WorkspaceID   string
	WorkspaceRoot string
	ScopePath     string
	Goal          string
	Tier          domain.ReadOnlyFanoutTier
	RequestedBy   string
	CreatedAt     time.Time
}

type snapshotCandidate struct {
	relativePath string
	sizeBytes    int64
	digest       string
}

// SourceFile is an execution-only, redacted copy of one manifest file. Its
// content is deliberately never part of the durable fan-out projection.
type SourceFile struct {
	RelativePath  string
	Content       string
	ContentSHA256 string
	Redactions    int
}

type VerifiedShard struct {
	Ordinal     int
	InputDigest string
	Files       []SourceFile
}

type VerifiedSnapshot struct {
	PlanID         string
	SnapshotDigest string
	Shards         []VerifiedShard
}

var skippedDirectories = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {}, ".cache": {}, ".next": {},
	"node_modules": {}, "vendor": {}, "target": {}, "dist": {}, "build": {},
}

var supportedExtensions = map[string]struct{}{
	".c": {}, ".cc": {}, ".cfg": {}, ".conf": {}, ".cpp": {}, ".cs": {},
	".css": {}, ".go": {}, ".h": {}, ".hpp": {}, ".html": {}, ".ini": {},
	".java": {}, ".js": {}, ".jsx": {}, ".json": {}, ".kt": {}, ".lock": {},
	".md": {}, ".mod": {}, ".php": {}, ".proto": {}, ".py": {}, ".rb": {},
	".rs": {}, ".scss": {}, ".sh": {}, ".sql": {}, ".sum": {}, ".swift": {},
	".toml": {}, ".ts": {}, ".tsx": {}, ".txt": {}, ".xml": {}, ".yaml": {},
	".yml": {}, ".zig": {},
}

var supportedBasenames = map[string]struct{}{
	"dockerfile": {}, "license": {}, "makefile": {}, "readme": {},
}

var sensitiveBasenames = map[string]struct{}{
	".netrc": {}, ".npmrc": {}, ".pypirc": {}, "credentials": {},
	"credentials.json": {}, "id_dsa": {}, "id_ed25519": {}, "id_rsa": {},
	"secrets.json": {},
}

var sensitiveExtensions = map[string]struct{}{
	".jks": {}, ".kdbx": {}, ".key": {}, ".p12": {}, ".pem": {}, ".pfx": {},
}

func BuildPlan(ctx context.Context, request BuildPlanRequest) (domain.ReadOnlyFanoutPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.ScopePath == "" {
		request.ScopePath = "."
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = time.Now().UTC()
	} else {
		request.CreatedAt = request.CreatedAt.UTC()
	}
	root, scope, err := resolveSnapshotScope(request.WorkspaceRoot, request.ScopePath)
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, err
	}
	candidates, excluded, err := collectSnapshotCandidates(ctx, root, scope)
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, err
	}
	if len(candidates) == 0 {
		return domain.ReadOnlyFanoutPlan{}, errors.New(
			"read-only fan-out scope contains no eligible UTF-8 source files")
	}
	parallelism, err := domain.ResolveReadOnlyFanoutParallelism(request.Tier,
		len(candidates))
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, err
	}
	files, shardBytes, shardFiles := partitionCandidates(request.PlanID, candidates,
		parallelism)
	snapshotDigest, err := domain.ReadOnlyFanoutSnapshotDigest(files)
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, err
	}
	capabilityFingerprint, err := domain.ReadOnlyFanoutCapabilityFingerprint(
		domain.DefaultReadOnlyFanoutCapabilities())
	if err != nil {
		return domain.ReadOnlyFanoutPlan{}, err
	}
	shards := make([]domain.ReadOnlyFanoutShard, parallelism)
	for index := range shards {
		ordinal := index + 1
		digest, err := domain.ReadOnlyFanoutShardDigest(ordinal, files)
		if err != nil {
			return domain.ReadOnlyFanoutPlan{}, err
		}
		shards[index] = domain.ReadOnlyFanoutShard{
			PlanID: request.PlanID, Ordinal: ordinal,
			Status:    domain.ReadOnlyFanoutShardPending,
			FileCount: shardFiles[index], TotalBytes: shardBytes[index],
			InputDigest: digest, Version: 1,
			CreatedAt: request.CreatedAt, UpdatedAt: request.CreatedAt,
		}
	}
	var totalBytes int64
	for _, file := range files {
		totalBytes += file.SizeBytes
	}
	plan := domain.ReadOnlyFanoutPlan{
		ID: request.PlanID, RunID: request.RunID, WorkspaceID: request.WorkspaceID,
		ScopePath: request.ScopePath, Goal: request.Goal,
		ProtocolVersion: domain.ReadOnlyFanoutProtocolVersion,
		RequestedTier:   request.Tier, EffectiveParallelism: parallelism,
		Status:                domain.ReadOnlyFanoutPlanned,
		CapabilityFingerprint: capabilityFingerprint, SnapshotDigest: snapshotDigest,
		FileCount: len(files), TotalBytes: totalBytes, ExcludedCount: excluded,
		ShardCount: parallelism, RequestedBy: request.RequestedBy,
		Version: 1, CreatedAt: request.CreatedAt, UpdatedAt: request.CreatedAt,
		Files: files, Shards: shards,
	}
	if err := plan.Validate(); err != nil {
		return domain.ReadOnlyFanoutPlan{}, err
	}
	return plan, nil
}

// LoadVerifiedSnapshot rebuilds the manifest and then opens every admitted
// file through the workspace root. No model input is returned unless the complete
// workspace snapshot still matches the immutable plan.
func LoadVerifiedSnapshot(ctx context.Context, workspaceRoot string,
	plan domain.ReadOnlyFanoutPlan,
) (VerifiedSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := plan.Validate(); err != nil {
		return VerifiedSnapshot{}, fmt.Errorf("invalid read-only fan-out plan: %w", err)
	}
	fresh, err := BuildPlan(ctx, BuildPlanRequest{
		PlanID: plan.ID, RunID: plan.RunID, WorkspaceID: plan.WorkspaceID,
		WorkspaceRoot: workspaceRoot, ScopePath: plan.ScopePath, Goal: plan.Goal,
		Tier: plan.RequestedTier, RequestedBy: plan.RequestedBy,
		CreatedAt: plan.CreatedAt,
	})
	if err != nil {
		return VerifiedSnapshot{}, err
	}
	if !samePlannedSnapshot(plan, fresh) {
		return VerifiedSnapshot{}, errors.New(
			"read-only fan-out workspace changed after planning")
	}

	root, _, err := resolveSnapshotScope(workspaceRoot, plan.ScopePath)
	if err != nil {
		return VerifiedSnapshot{}, err
	}
	rootHandle, err := rootfs.OpenExactRoot(root)
	if err != nil {
		return VerifiedSnapshot{}, err
	}
	defer rootHandle.Close()

	verified := VerifiedSnapshot{
		PlanID: plan.ID, SnapshotDigest: plan.SnapshotDigest,
		Shards: make([]VerifiedShard, plan.ShardCount),
	}
	for index, shard := range plan.Shards {
		verified.Shards[index] = VerifiedShard{
			Ordinal: shard.Ordinal, InputDigest: shard.InputDigest,
			Files: make([]SourceFile, 0, shard.FileCount),
		}
	}
	for _, manifest := range plan.Files {
		if err := ctx.Err(); err != nil {
			return VerifiedSnapshot{}, err
		}
		file, err := readVerifiedFile(rootHandle, manifest)
		if err != nil {
			return VerifiedSnapshot{}, fmt.Errorf("verify %s: %w", manifest.RelativePath, err)
		}
		verified.Shards[manifest.ShardOrdinal-1].Files = append(
			verified.Shards[manifest.ShardOrdinal-1].Files, file)
	}
	return verified, nil
}

func samePlannedSnapshot(expected, current domain.ReadOnlyFanoutPlan) bool {
	return expected.ID == current.ID && expected.RunID == current.RunID &&
		expected.WorkspaceID == current.WorkspaceID &&
		expected.ScopePath == current.ScopePath && expected.Goal == current.Goal &&
		expected.ProtocolVersion == current.ProtocolVersion &&
		expected.RequestedTier == current.RequestedTier &&
		expected.EffectiveParallelism == current.EffectiveParallelism &&
		expected.Status == current.Status &&
		expected.CapabilityFingerprint == current.CapabilityFingerprint &&
		expected.SnapshotDigest == current.SnapshotDigest &&
		expected.FileCount == current.FileCount &&
		expected.TotalBytes == current.TotalBytes &&
		expected.ExcludedCount == current.ExcludedCount &&
		expected.ShardCount == current.ShardCount &&
		expected.RequestedBy == current.RequestedBy &&
		slices.Equal(expected.Files, current.Files) &&
		slices.Equal(expected.Shards, current.Shards)
}

func readVerifiedFile(root *rootfs.ExactRoot, manifest domain.ReadOnlyFanoutFile) (SourceFile, error) {
	rootRelative := filepath.FromSlash(manifest.RelativePath)
	before, err := root.Lstat(rootRelative)
	if err != nil {
		return SourceFile{}, err
	}
	if !before.Mode().IsRegular() || before.Size() != manifest.SizeBytes {
		return SourceFile{}, errors.New("manifest path is no longer a regular file of the planned size")
	}
	content, err := readSnapshotFile(root, rootRelative, before)
	if err != nil {
		return SourceFile{}, err
	}
	if !utf8.Valid(content) || containsNUL(content) {
		return SourceFile{}, errors.New("manifest file is not valid UTF-8 text")
	}
	digest := sha256.Sum256(content)
	digestText := hex.EncodeToString(digest[:])
	if digestText != manifest.ContentSHA256 {
		return SourceFile{}, errors.New("manifest content digest changed")
	}
	redacted := redact.Text(string(content))
	redactionCount := 0
	for _, finding := range redacted.Findings {
		redactionCount += finding.Count
	}
	return SourceFile{
		RelativePath: manifest.RelativePath, Content: redacted.Text,
		ContentSHA256: digestText, Redactions: redactionCount,
	}, nil
}

func readSnapshotFile(root *rootfs.ExactRoot, rootRelative string, before os.FileInfo) ([]byte, error) {
	handle, err := root.OpenReadOnly(rootRelative)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("manifest path changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(handle,
		int64(domain.MaxReadOnlyFanoutFileBytes)+1))
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(rootRelative)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(opened, after) || int64(len(content)) != before.Size() ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("manifest file changed while reading")
	}
	return content, nil
}

func resolveSnapshotScope(workspaceRoot, scopePath string) (string, string, error) {
	if workspaceRoot == "" {
		return "", "", errors.New("read-only fan-out workspace root is required")
	}
	if !utf8.ValidString(scopePath) || len(scopePath) > domain.MaxReadOnlyFanoutPathBytes ||
		strings.Contains(scopePath, "\\") || strings.ContainsRune(scopePath, 0) ||
		!filepath.IsLocal(filepath.FromSlash(scopePath)) {
		return "", "", errors.New("read-only fan-out scope must be a slash-separated workspace-relative path")
	}
	cleanScope := path.Clean(scopePath)
	if cleanScope != scopePath || cleanScope == ".." || strings.HasPrefix(cleanScope, "../") {
		return "", "", errors.New("read-only fan-out scope escapes the workspace")
	}
	root, err := rootfs.EvalExactSymlinks(workspaceRoot)
	if err != nil {
		return "", "", err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", "", err
	}
	if !rootInfo.IsDir() {
		return "", "", errors.New("read-only fan-out workspace root is not a directory")
	}
	scopeCandidate, err := rootfs.ExactAbsolutePath(filepath.Join(root, filepath.FromSlash(cleanScope)))
	if err != nil {
		return "", "", err
	}
	scope, err := rootfs.EvalExactSymlinks(scopeCandidate)
	if err != nil {
		return "", "", err
	}
	if !sameCanonicalPath(scopeCandidate, scope) {
		return "", "", errors.New("read-only fan-out scope cannot traverse a symlink")
	}
	relative, err := filepath.Rel(rootfs.OrdinaryPath(root), rootfs.OrdinaryPath(scope))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("read-only fan-out scope escapes the workspace")
	}
	scopeInfo, err := os.Stat(scope)
	if err != nil {
		return "", "", err
	}
	if !scopeInfo.IsDir() {
		return "", "", errors.New("read-only fan-out scope is not a directory")
	}
	return root, filepath.ToSlash(relative), nil
}

func collectSnapshotCandidates(ctx context.Context, root, scopeRelative string,
) ([]snapshotCandidate, int, error) {
	rootHandle, err := rootfs.OpenExactRoot(root)
	if err != nil {
		return nil, 0, err
	}
	defer rootHandle.Close()
	scopeInfo, err := rootHandle.Lstat(filepath.FromSlash(scopeRelative))
	if err != nil {
		return nil, 0, err
	}
	if !scopeInfo.IsDir() {
		return nil, 0, errors.New("read-only fan-out scope is no longer a directory")
	}
	candidates := make([]snapshotCandidate, 0, 64)
	excluded := 0
	entries := 0
	var totalBytes int64
	err = fs.WalkDir(rootHandle.FS(), scopeRelative, func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if relative == scopeRelative {
			return nil
		}
		entries++
		if entries > domain.MaxReadOnlyFanoutWalkEntries {
			return fmt.Errorf("read-only fan-out scope exceeds %d filesystem entries",
				domain.MaxReadOnlyFanoutWalkEntries)
		}
		// Treat a padded sensitive name as sensitive without changing its identity.
		name := strings.ToLower(strings.TrimSpace(entry.Name()))
		if !utf8.ValidString(relative) || len(relative) > domain.MaxReadOnlyFanoutPathBytes ||
			strings.Contains(relative, "\\") || !filepath.IsLocal(filepath.FromSlash(relative)) ||
			entry.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			excluded++
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if _, skip := skippedDirectories[name]; skip {
				excluded++
				return fs.SkipDir
			}
			return nil
		}
		if sensitiveFile(name) || !supportedSourceFile(name) || len(candidates) >= domain.MaxReadOnlyFanoutFiles {
			excluded++
			return nil
		}
		rootRelative := filepath.FromSlash(relative)
		info, err := rootHandle.Lstat(rootRelative)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > domain.MaxReadOnlyFanoutFileBytes ||
			totalBytes+info.Size() > domain.MaxReadOnlyFanoutTotalBytes {
			excluded++
			return nil
		}
		content, err := readSnapshotFile(rootHandle, rootRelative, info)
		if err != nil {
			return err
		}
		if !utf8.Valid(content) || containsNUL(content) {
			excluded++
			return nil
		}
		digest := sha256.Sum256(content)
		totalBytes += info.Size()
		candidates = append(candidates, snapshotCandidate{
			relativePath: relative, sizeBytes: int64(len(content)),
			digest: hex.EncodeToString(digest[:]),
		})
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	slices.SortFunc(candidates, func(left, right snapshotCandidate) int {
		return strings.Compare(left.relativePath, right.relativePath)
	})
	return candidates, excluded, nil
}

func sameCanonicalPath(left, right string) bool {
	left = filepath.Clean(rootfs.OrdinaryPath(left))
	right = filepath.Clean(rootfs.OrdinaryPath(right))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func partitionCandidates(planID string, candidates []snapshotCandidate,
	parallelism int,
) ([]domain.ReadOnlyFanoutFile, []int64, []int) {
	files := make([]domain.ReadOnlyFanoutFile, len(candidates))
	shardBytes := make([]int64, parallelism)
	shardFiles := make([]int, parallelism)
	for index, candidate := range candidates {
		shardIndex := 0
		for current := 1; current < parallelism; current++ {
			if shardBytes[current] < shardBytes[shardIndex] ||
				(shardBytes[current] == shardBytes[shardIndex] &&
					shardFiles[current] < shardFiles[shardIndex]) {
				shardIndex = current
			}
		}
		files[index] = domain.ReadOnlyFanoutFile{
			PlanID: planID, Ordinal: index + 1, ShardOrdinal: shardIndex + 1,
			RelativePath: candidate.relativePath, SizeBytes: candidate.sizeBytes,
			ContentSHA256: candidate.digest,
		}
		shardBytes[shardIndex] += candidate.sizeBytes
		shardFiles[shardIndex]++
	}
	return files, shardBytes, shardFiles
}

func supportedSourceFile(name string) bool {
	if _, allowed := supportedBasenames[name]; allowed {
		return true
	}
	_, allowed := supportedExtensions[strings.ToLower(filepath.Ext(name))]
	return allowed
}

func sensitiveFile(name string) bool {
	if name == ".env" || strings.HasPrefix(name, ".env.") {
		return true
	}
	if _, sensitive := sensitiveBasenames[name]; sensitive {
		return true
	}
	_, sensitive := sensitiveExtensions[strings.ToLower(filepath.Ext(name))]
	return sensitive
}

func containsNUL(content []byte) bool {
	for _, current := range content {
		if current == 0 {
			return true
		}
	}
	return false
}
