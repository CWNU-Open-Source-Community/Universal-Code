package projectconfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	InstructionSnapshotProtocolVersion = "project_instruction_snapshot.v1"
	InstructionTrustClass              = "project_workflow_untrusted"
	InstructionIgnoreFile              = "instructions.ignore"
	MaxInstructionFiles                = 64
	MaxInstructionFileBytes            = 64 * 1024
	MaxInstructionTotalBytes           = 256 * 1024
	MaxInstructionDepth                = 32
	MaxInstructionIgnoreBytes          = 16 * 1024
	MaxInstructionIgnorePatterns       = 128
)

var errEmptyInstruction = errors.New("instruction source cannot be empty after normalization")

// InstructionAuthority is deliberately incapable of representing a grant.
// Project-owned text can suggest workflow, formatting, and validation only.
type InstructionAuthority struct {
	WorkflowGuidance   bool `json:"workflow_guidance"`
	FormattingGuidance bool `json:"formatting_guidance"`
	ValidationGuidance bool `json:"validation_guidance"`
	ToolGrant          bool `json:"tool_grant"`
	NetworkGrant       bool `json:"network_grant"`
	SecretAccess       bool `json:"secret_access"`
	DebugGrant         bool `json:"debug_grant"`
	PluginGrant        bool `json:"plugin_grant"`
	HookExecution      bool `json:"hook_execution"`
	PolicyOverride     bool `json:"policy_override"`
}

func workflowOnlyInstructionAuthority() InstructionAuthority {
	return InstructionAuthority{
		WorkflowGuidance: true, FormattingGuidance: true, ValidationGuidance: true,
	}
}

type InstructionSource struct {
	Ordinal       int                  `json:"ordinal"`
	Path          string               `json:"path"`
	Kind          string               `json:"kind"`
	Scope         string               `json:"scope"`
	Depth         int                  `json:"depth"`
	Precedence    int                  `json:"precedence"`
	Content       string               `json:"content"`
	ContentSHA256 string               `json:"content_sha256"`
	LoadedAt      time.Time            `json:"loaded_at"`
	Trust         string               `json:"trust"`
	ApplicableTo  []string             `json:"applicable_to"`
	WhyEffective  string               `json:"why_effective"`
	Redacted      bool                 `json:"redacted"`
	Authority     InstructionAuthority `json:"authority"`
}

type IgnoredInstruction struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type InstructionConflict struct {
	LowerPrecedencePath  string `json:"lower_precedence_path"`
	HigherPrecedencePath string `json:"higher_precedence_path"`
	Resolution           string `json:"resolution"`
}

type InstructionLimits struct {
	MaxFiles      int `json:"max_files"`
	MaxFileBytes  int `json:"max_file_bytes"`
	MaxTotalBytes int `json:"max_total_bytes"`
	MaxDepth      int `json:"max_depth"`
}

type InstructionSnapshot struct {
	ProtocolVersion string                `json:"protocol_version"`
	TargetPath      string                `json:"target_path"`
	Sources         []InstructionSource   `json:"sources"`
	Ignored         []IgnoredInstruction  `json:"ignored"`
	Conflicts       []InstructionConflict `json:"conflicts"`
	Fingerprint     string                `json:"fingerprint"`
	LoadedAt        time.Time             `json:"loaded_at"`
	Limits          InstructionLimits     `json:"limits"`
	Delivery        *InstructionDelivery  `json:"delivery,omitempty"`
}

type InstructionSnapshotDiff struct {
	FromFingerprint      string   `json:"from_fingerprint"`
	ToFingerprint        string   `json:"to_fingerprint"`
	Added                []string `json:"added"`
	Removed              []string `json:"removed"`
	Changed              []string `json:"changed"`
	OrderChanged         bool     `json:"order_changed"`
	RequiresConfirmation bool     `json:"requires_confirmation"`
}

type RunInstructionSnapshot struct {
	ID          string                  `json:"id"`
	RunID       string                  `json:"run_id"`
	Revision    int64                   `json:"revision"`
	Snapshot    InstructionSnapshot     `json:"snapshot"`
	Diff        InstructionSnapshotDiff `json:"diff"`
	ConfirmedBy string                  `json:"confirmed_by"`
	CreatedAt   time.Time               `json:"created_at"`
}

func (r RunInstructionSnapshot) Validate() error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.RunID) == "" || r.Revision < 1 ||
		strings.TrimSpace(r.ConfirmedBy) == "" || r.CreatedAt.IsZero() {
		return errors.New("Run instruction snapshot identity or audit metadata is invalid")
	}
	if err := r.Snapshot.Validate(); err != nil {
		return err
	}
	if r.Diff.ToFingerprint != r.Snapshot.Fingerprint {
		return errors.New("Run instruction snapshot diff does not bind the selected fingerprint")
	}
	return nil
}

// DiscoverInstructions resolves project guidance from the workspace boundary
// down to the target. Root guidance is ordered first and the nearest directory
// last, so later records have higher precedence without silently discarding
// inherited guidance.
func DiscoverInstructions(ctx context.Context, workspaceRoot, targetPath string) (InstructionSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	root, target, targetDir, targetRelative, err := instructionBoundary(workspaceRoot, targetPath)
	if err != nil {
		return InstructionSnapshot{}, err
	}
	patterns, ignoreRecord, err := loadInstructionIgnore(ctx, root)
	if err != nil {
		return InstructionSnapshot{}, err
	}
	directories, err := instructionDirectories(root, targetDir)
	if err != nil {
		return InstructionSnapshot{}, err
	}
	now := time.Now().UTC()
	snapshot := InstructionSnapshot{
		ProtocolVersion: InstructionSnapshotProtocolVersion,
		TargetPath:      targetRelative,
		Sources:         []InstructionSource{}, Ignored: []IgnoredInstruction{},
		Conflicts: []InstructionConflict{}, LoadedAt: now,
		Limits: InstructionLimits{MaxFiles: MaxInstructionFiles,
			MaxFileBytes: MaxInstructionFileBytes, MaxTotalBytes: MaxInstructionTotalBytes,
			MaxDepth: MaxInstructionDepth},
	}
	if ignoreRecord != nil {
		snapshot.Ignored = append(snapshot.Ignored, *ignoreRecord)
	}
	totalBytes := 0
	for depth, directory := range directories {
		if err := ctx.Err(); err != nil {
			return InstructionSnapshot{}, err
		}
		candidates, err := instructionCandidates(directory, root)
		if err != nil {
			return InstructionSnapshot{}, err
		}
		agentsOverride := ""
		for _, candidate := range candidates {
			relative, err := filepath.Rel(root, candidate.path)
			if err != nil || escapesInstructionRoot(relative) {
				return InstructionSnapshot{}, errors.New("project instruction candidate escapes the workspace boundary")
			}
			relative = canonicalInstructionPath(relative)
			if candidate.kind == "agents" && agentsOverride != "" {
				if _, err := os.Lstat(candidate.path); err == nil {
					snapshot.Ignored = append(snapshot.Ignored, IgnoredInstruction{
						Path: relative, Reason: "superseded by " + agentsOverride,
					})
				} else if !errors.Is(err, fs.ErrNotExist) {
					return InstructionSnapshot{}, fmt.Errorf("project instruction %s: %w", relative, err)
				}
				continue
			}
			if ignoredInstructionPath(relative, patterns) {
				snapshot.Ignored = append(snapshot.Ignored, IgnoredInstruction{
					Path: relative, Reason: "matched .prayu/instructions.ignore",
				})
				continue
			}
			content, digest, redacted, found, err := readStableInstruction(ctx, candidate.path)
			if candidate.agentsOverride && errors.Is(err, errEmptyInstruction) {
				snapshot.Ignored = append(snapshot.Ignored, IgnoredInstruction{
					Path: relative, Reason: "empty AGENTS.override.md; falling back to AGENTS.md",
				})
				continue
			}
			if err != nil {
				return InstructionSnapshot{}, fmt.Errorf("project instruction %s: %w", relative, err)
			}
			if !found {
				continue
			}
			if candidate.agentsOverride {
				agentsOverride = relative
			}
			if len(snapshot.Sources) >= MaxInstructionFiles {
				return InstructionSnapshot{}, fmt.Errorf("project instruction count exceeds %d", MaxInstructionFiles)
			}
			totalBytes += len([]byte(content))
			if totalBytes > MaxInstructionTotalBytes {
				return InstructionSnapshot{}, fmt.Errorf("project instruction content exceeds %d total bytes", MaxInstructionTotalBytes)
			}
			scopeRelative, err := filepath.Rel(root, directory)
			if err != nil || escapesInstructionRoot(scopeRelative) {
				return InstructionSnapshot{}, errors.New("project instruction scope escapes the workspace boundary")
			}
			scopeRelative = canonicalInstructionPath(scopeRelative)
			if scopeRelative == "." {
				scopeRelative = ""
			}
			ordinal := len(snapshot.Sources) + 1
			snapshot.Sources = append(snapshot.Sources, InstructionSource{
				Ordinal: ordinal, Path: relative, Kind: candidate.kind,
				Scope: scopeRelative, Depth: depth,
				Precedence: 100 + ordinal - 1,
				Content:    content, ContentSHA256: digest, LoadedAt: now,
				Trust: InstructionTrustClass, ApplicableTo: []string{targetRelative},
				WhyEffective: instructionWhyEffective(scopeRelative, targetRelative, depth),
				Redacted:     redacted, Authority: workflowOnlyInstructionAuthority(),
			})
		}
	}
	_ = target // retained above so the boundary check cannot be optimized away conceptually.
	snapshot.Conflicts = instructionConflicts(snapshot.Sources)
	snapshot.Fingerprint = snapshot.stableFingerprint()
	if err := snapshot.Validate(); err != nil {
		return InstructionSnapshot{}, err
	}
	return snapshot, nil
}

func (s InstructionSnapshot) Validate() error {
	if s.ProtocolVersion != InstructionSnapshotProtocolVersion {
		return fmt.Errorf("unsupported project instruction snapshot protocol %q", s.ProtocolVersion)
	}
	if s.TargetPath == "" || filepath.IsAbs(s.TargetPath) || strings.ContainsRune(s.TargetPath, 0) ||
		escapesInstructionRoot(filepath.FromSlash(s.TargetPath)) {
		return errors.New("project instruction target path is invalid")
	}
	if len(s.Sources) > MaxInstructionFiles || len(s.Ignored) > MaxInstructionFiles+1 {
		return errors.New("project instruction snapshot exceeds its source bound")
	}
	total := 0
	previousPrecedence := -1
	seen := make(map[string]struct{}, len(s.Sources))
	for index, source := range s.Sources {
		if source.Ordinal != index+1 || source.Path == "" || source.Path != canonicalInstructionPath(source.Path) ||
			escapesInstructionRoot(filepath.FromSlash(source.Path)) {
			return errors.New("project instruction source order or path is invalid")
		}
		if _, exists := seen[source.Path]; exists {
			return errors.New("project instruction source path is duplicated")
		}
		seen[source.Path] = struct{}{}
		if source.Precedence < previousPrecedence {
			return errors.New("project instruction precedence is not deterministic")
		}
		previousPrecedence = source.Precedence
		if source.Content == "" || len([]byte(source.Content)) > MaxInstructionFileBytes ||
			!utf8.ValidString(source.Content) || source.ContentSHA256 != instructionContentDigest(source.Content) {
			return errors.New("project instruction content or digest is invalid")
		}
		if source.Trust != InstructionTrustClass || source.Authority != workflowOnlyInstructionAuthority() {
			return errors.New("project instruction authority widened")
		}
		if len(source.ApplicableTo) != 1 || source.ApplicableTo[0] != s.TargetPath || source.LoadedAt.IsZero() {
			return errors.New("project instruction applicability metadata is invalid")
		}
		total += len([]byte(source.Content))
	}
	if total > MaxInstructionTotalBytes || s.LoadedAt.IsZero() {
		return errors.New("project instruction snapshot size or load time is invalid")
	}
	if err := s.validateDelivery(); err != nil {
		return err
	}
	if s.Fingerprint == "" || s.Fingerprint != s.stableFingerprint() {
		return errors.New("project instruction snapshot fingerprint mismatch")
	}
	return nil
}

func DiffInstructionSnapshots(before, after InstructionSnapshot) InstructionSnapshotDiff {
	diff := InstructionSnapshotDiff{FromFingerprint: before.Fingerprint, ToFingerprint: after.Fingerprint,
		Added: []string{}, Removed: []string{}, Changed: []string{}}
	left := make(map[string]InstructionSource, len(before.Sources))
	right := make(map[string]InstructionSource, len(after.Sources))
	leftDelivery := make(map[string]InstructionSourceDelivery)
	rightDelivery := make(map[string]InstructionSourceDelivery)
	if before.Delivery != nil {
		for _, item := range before.Delivery.Sources {
			leftDelivery[item.Path] = item
		}
	}
	if after.Delivery != nil {
		for _, item := range after.Delivery.Sources {
			rightDelivery[item.Path] = item
		}
	}
	leftOrder := make([]string, len(before.Sources))
	rightOrder := make([]string, len(after.Sources))
	for index, source := range before.Sources {
		left[source.Path] = source
		leftOrder[index] = source.Path
	}
	for index, source := range after.Sources {
		right[source.Path] = source
		rightOrder[index] = source.Path
		if prior, ok := left[source.Path]; !ok {
			diff.Added = append(diff.Added, source.Path)
		} else if prior.ContentSHA256 != source.ContentSHA256 || prior.Scope != source.Scope ||
			prior.Kind != source.Kind || prior.Precedence != source.Precedence ||
			leftDelivery[source.Path] != rightDelivery[source.Path] {
			diff.Changed = append(diff.Changed, source.Path)
		}
	}
	for _, source := range before.Sources {
		if _, ok := right[source.Path]; !ok {
			diff.Removed = append(diff.Removed, source.Path)
		}
	}
	diff.OrderChanged = strings.Join(leftOrder, "\x00") != strings.Join(rightOrder, "\x00")
	diff.RequiresConfirmation = before.Fingerprint != after.Fingerprint
	return diff
}

type instructionCandidate struct {
	path           string
	kind           string
	agentsOverride bool
}

func instructionBoundary(workspaceRoot, targetPath string) (string, string, string, string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" || strings.ContainsRune(workspaceRoot, 0) {
		return "", "", "", "", errors.New("workspace root is invalid")
	}
	root, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return "", "", "", "", err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return "", "", "", "", errors.New("workspace root must be a real directory without symlink indirection")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || !sameInstructionPath(root, resolved) {
		return "", "", "", "", errors.New("workspace root must not resolve through a symlink or junction")
	}
	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		targetPath = "."
	}
	if filepath.IsAbs(targetPath) || strings.ContainsRune(targetPath, 0) {
		return "", "", "", "", errors.New("project instruction target must be workspace-relative")
	}
	cleanTarget := filepath.Clean(filepath.FromSlash(targetPath))
	if escapesInstructionRoot(cleanTarget) {
		return "", "", "", "", errors.New("project instruction target escapes the workspace boundary")
	}
	target := filepath.Join(root, cleanTarget)
	relative, err := filepath.Rel(root, target)
	if err != nil || escapesInstructionRoot(relative) {
		return "", "", "", "", errors.New("project instruction target escapes the workspace boundary")
	}
	targetDir := target
	if targetInfo, statErr := os.Lstat(target); statErr == nil {
		if targetInfo.Mode()&fs.ModeSymlink != 0 {
			return "", "", "", "", errors.New("project instruction target cannot be a symlink or junction")
		}
		if !targetInfo.IsDir() {
			targetDir = filepath.Dir(target)
		}
	} else if errors.Is(statErr, fs.ErrNotExist) {
		targetDir = filepath.Dir(target)
	} else {
		return "", "", "", "", fmt.Errorf("project instruction target stat: %w", statErr)
	}
	if err := validateInstructionDirectoryChain(root, targetDir); err != nil {
		return "", "", "", "", err
	}
	targetRelative := canonicalInstructionPath(relative)
	if targetRelative == "." {
		targetRelative = "."
	}
	return root, target, targetDir, targetRelative, nil
}

func validateInstructionDirectoryChain(root, targetDir string) error {
	relative, err := filepath.Rel(root, targetDir)
	if err != nil || escapesInstructionRoot(relative) {
		return errors.New("project instruction directory escapes the workspace boundary")
	}
	current := root
	if relative == "." {
		return nil
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) > MaxInstructionDepth {
		return fmt.Errorf("project instruction target depth exceeds %d", MaxInstructionDepth)
	}
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return errors.New("project instruction target ancestry must contain only real directories")
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil || !sameInstructionPath(current, resolved) {
			return errors.New("project instruction target ancestry cannot contain symlink or junction indirection")
		}
	}
	return nil
}

func instructionDirectories(root, targetDir string) ([]string, error) {
	relative, err := filepath.Rel(root, targetDir)
	if err != nil || escapesInstructionRoot(relative) {
		return nil, errors.New("project instruction target directory escapes the workspace")
	}
	directories := []string{root}
	if relative == "." {
		return directories, nil
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		directories = append(directories, current)
	}
	if len(directories)-1 > MaxInstructionDepth {
		return nil, fmt.Errorf("project instruction depth exceeds %d", MaxInstructionDepth)
	}
	return directories, nil
}

func instructionCandidates(directory, root string) ([]instructionCandidate, error) {
	candidates := []instructionCandidate{
		{path: filepath.Join(directory, "AGENTS.override.md"), kind: "agents", agentsOverride: true},
		{path: filepath.Join(directory, "AGENTS.md"), kind: "agents"},
		{path: filepath.Join(directory, "CLAUDE.md"), kind: "claude"},
	}
	claudeDir := filepath.Join(directory, ".claude")
	if info, err := os.Lstat(claudeDir); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("project instruction .claude path must be a real directory")
		}
		resolved, err := filepath.EvalSymlinks(claudeDir)
		if err != nil || !sameInstructionPath(claudeDir, resolved) {
			return nil, errors.New("project instruction .claude path cannot contain symlink or junction indirection")
		}
		candidates = append(candidates, instructionCandidate{
			path: filepath.Join(claudeDir, "CLAUDE.md"), kind: "claude",
		})
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	prayuDir := filepath.Join(directory, ConfigDirName)
	if info, err := os.Lstat(prayuDir); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("project instruction .prayu path must be a real directory")
		}
		candidates = append(candidates, instructionCandidate{
			path: filepath.Join(prayuDir, "instructions.md"), kind: "prayu_instructions",
		})
		rulesDir := filepath.Join(prayuDir, "rules")
		if rulesInfo, rulesErr := os.Lstat(rulesDir); rulesErr == nil {
			if rulesInfo.Mode()&fs.ModeSymlink != 0 || !rulesInfo.IsDir() {
				return nil, errors.New("project instruction rules path must be a real directory")
			}
			var rules []string
			err = filepath.WalkDir(rulesDir, func(current string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				relative, relErr := filepath.Rel(rulesDir, current)
				if relErr != nil || escapesInstructionRoot(relative) {
					return errors.New("project instruction rule escapes its directory")
				}
				if relative != "." && len(strings.Split(relative, string(filepath.Separator))) > MaxInstructionDepth {
					return fmt.Errorf("project instruction rule depth exceeds %d", MaxInstructionDepth)
				}
				info, infoErr := entry.Info()
				if infoErr != nil {
					return infoErr
				}
				if info.Mode()&fs.ModeSymlink != 0 {
					return errors.New("project instruction rules cannot contain symlinks or junctions")
				}
				if entry.IsDir() {
					return nil
				}
				if !info.Mode().IsRegular() {
					return errors.New("project instruction rule must be a regular file")
				}
				if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
					rules = append(rules, current)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			sort.Slice(rules, func(i, j int) bool {
				left := canonicalInstructionPath(rules[i])
				right := canonicalInstructionPath(rules[j])
				return left < right
			})
			for _, rule := range rules {
				candidates = append(candidates, instructionCandidate{
					path: rule, kind: "prayu_rule",
				})
			}
		} else if !errors.Is(rulesErr, fs.ErrNotExist) {
			return nil, rulesErr
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, candidate := range candidates {
		if relative, err := filepath.Rel(root, candidate.path); err != nil || escapesInstructionRoot(relative) {
			return nil, errors.New("project instruction candidate escapes the workspace")
		}
	}
	return candidates, nil
}

func readStableInstruction(ctx context.Context, filename string) (string, string, bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", "", false, false, err
	}
	before, err := os.Lstat(filename)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", false, false, nil
	}
	if err != nil {
		return "", "", false, false, err
	}
	if before.Mode()&fs.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return "", "", false, false, errors.New("instruction source must be a regular file without symlink or junction indirection")
	}
	if before.Size() > MaxInstructionFileBytes {
		return "", "", false, false, fmt.Errorf("instruction source must contain between 1 and %d bytes", MaxInstructionFileBytes)
	}
	read := func() ([]byte, error) {
		file, openErr := os.Open(filename)
		if openErr != nil {
			return nil, openErr
		}
		defer file.Close()
		return io.ReadAll(io.LimitReader(file, MaxInstructionFileBytes+1))
	}
	first, err := read()
	if err != nil {
		return "", "", false, false, err
	}
	after, err := os.Lstat(filename)
	if err != nil || after.Mode()&fs.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		!os.SameFile(before, after) {
		return "", "", false, false, errors.New("instruction source changed concurrently")
	}
	second, err := read()
	if err != nil || !stableInstructionBytes(first, second) {
		return "", "", false, false, errors.New("instruction source changed concurrently")
	}
	if len(first) > MaxInstructionFileBytes || !utf8.Valid(first) || bytes.IndexByte(first, 0) >= 0 {
		return "", "", false, false, errors.New("instruction source must be bounded UTF-8 without NUL bytes")
	}
	content := normalizeInstructionContent(string(first))
	if content == "" {
		return "", "", false, false, errEmptyInstruction
	}
	safe := redact.String(content)
	redacted := safe != content
	return safe, instructionContentDigest(safe), redacted, true, nil
}

func stableInstructionBytes(first, second []byte) bool {
	return bytes.Equal(first, second)
}

func loadInstructionIgnore(ctx context.Context, root string) ([]string, *IgnoredInstruction, error) {
	filename := filepath.Join(root, ConfigDirName, InstructionIgnoreFile)
	info, err := os.Lstat(filename)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > MaxInstructionIgnoreBytes {
		return nil, nil, errors.New("project instruction ignore file must be a bounded regular file without indirection")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, nil, err
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, nil, errors.New("project instruction ignore file must be UTF-8 without NUL bytes")
	}
	patterns := make([]string, 0)
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if filepath.IsAbs(line) || strings.Contains(line, "\\") || strings.ContainsRune(line, 0) ||
			strings.Contains(line, "..") || strings.HasPrefix(line, "!") {
			return nil, nil, fmt.Errorf("invalid project instruction ignore pattern %q", line)
		}
		line = path.Clean(line)
		if line == "." || strings.HasPrefix(line, "/") {
			return nil, nil, fmt.Errorf("invalid project instruction ignore pattern %q", line)
		}
		patterns = append(patterns, canonicalInstructionPath(line))
		if len(patterns) > MaxInstructionIgnorePatterns {
			return nil, nil, fmt.Errorf("project instruction ignore patterns exceed %d", MaxInstructionIgnorePatterns)
		}
	}
	return patterns, &IgnoredInstruction{Path: canonicalInstructionPath(filepath.Join(ConfigDirName, InstructionIgnoreFile)),
		Reason: "discovery control file; never delivered as instruction content"}, nil
}

func ignoredInstructionPath(value string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, value); matched {
			return true
		}
		if !strings.Contains(pattern, "/") {
			if matched, _ := path.Match(pattern, path.Base(value)); matched {
				return true
			}
		}
		if strings.HasSuffix(pattern, "/") && strings.HasPrefix(value, strings.TrimSuffix(pattern, "/")+"/") {
			return true
		}
	}
	return false
}

func instructionConflicts(sources []InstructionSource) []InstructionConflict {
	latest := make(map[string]InstructionSource)
	conflicts := make([]InstructionConflict, 0)
	for _, source := range sources {
		key := source.Kind
		if prior, ok := latest[key]; ok && prior.Scope != source.Scope {
			conflicts = append(conflicts, InstructionConflict{
				LowerPrecedencePath: prior.Path, HigherPrecedencePath: source.Path,
				Resolution: "both apply; the nearer directory is evaluated later and wins on conflict",
			})
		}
		latest[key] = source
	}
	return conflicts
}

func instructionWhyEffective(scope, target string, depth int) string {
	if scope == "" {
		return fmt.Sprintf("workspace-root guidance inherited by target %s at depth %d", target, depth)
	}
	return fmt.Sprintf("nearest-ancestor guidance from scope %s applies to target %s at depth %d", scope, target, depth)
}

func (s InstructionSnapshot) stableFingerprint() string {
	type stableSource struct {
		Ordinal       int                  `json:"ordinal"`
		Path          string               `json:"path"`
		Kind          string               `json:"kind"`
		Scope         string               `json:"scope"`
		Depth         int                  `json:"depth"`
		Precedence    int                  `json:"precedence"`
		ContentSHA256 string               `json:"content_sha256"`
		Trust         string               `json:"trust"`
		ApplicableTo  []string             `json:"applicable_to"`
		Redacted      bool                 `json:"redacted"`
		Authority     InstructionAuthority `json:"authority"`
	}
	stable := struct {
		ProtocolVersion string                `json:"protocol_version"`
		TargetPath      string                `json:"target_path"`
		Sources         []stableSource        `json:"sources"`
		Ignored         []IgnoredInstruction  `json:"ignored"`
		Conflicts       []InstructionConflict `json:"conflicts"`
		Limits          InstructionLimits     `json:"limits"`
		Delivery        *InstructionDelivery  `json:"delivery,omitempty"`
	}{ProtocolVersion: s.ProtocolVersion, TargetPath: s.TargetPath,
		Ignored: s.Ignored, Conflicts: s.Conflicts, Limits: s.Limits,
		Sources: make([]stableSource, len(s.Sources)), Delivery: s.Delivery}
	for index, source := range s.Sources {
		stable.Sources[index] = stableSource{Ordinal: source.Ordinal, Path: source.Path,
			Kind: source.Kind, Scope: source.Scope, Depth: source.Depth,
			Precedence: source.Precedence, ContentSHA256: source.ContentSHA256,
			Trust: source.Trust, ApplicableTo: source.ApplicableTo, Redacted: source.Redacted,
			Authority: source.Authority}
	}
	raw, _ := json.Marshal(stable)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func instructionContentDigest(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func normalizeInstructionContent(value string) string {
	value = strings.TrimPrefix(value, "\ufeff")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return value + "\n"
}

func escapesInstructionRoot(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative)
}

func canonicalInstructionPath(value string) string {
	value = filepath.ToSlash(filepath.Clean(value))
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

func sameInstructionPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
