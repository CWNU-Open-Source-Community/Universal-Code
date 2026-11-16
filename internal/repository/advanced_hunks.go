package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"cyberagent-workbench/internal/gitadvanced"
)

var advancedHunkHeader = regexp.MustCompile(
	`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@(?: .*)?$`)

type parsedAdvancedHunk struct {
	path     string
	header   string
	body     string
	oldStart int
	oldLines int
	newStart int
	newLines int
}

func (e *AdvancedExecutor) previewHunks(ctx context.Context, root string,
	spec gitadvanced.Spec,
) ([]gitadvanced.Hunk, []gitadvanced.FileImpact, error) {
	args := []string{"diff", "--no-ext-diff", "--no-renames", "--full-index",
		"--unified=3", "--src-prefix=a/", "--dst-prefix=b/"}
	if spec.Operation == gitadvanced.HunkUnstage {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	args = append(args, spec.Paths...)
	stdout, stderr, code, err := e.git(ctx, root, nil, args...)
	if err != nil || code != 0 {
		return nil, nil, fmt.Errorf("render Git hunks: %w: %s", err,
			strings.TrimSpace(stderr))
	}
	if len(stdout) > gitadvanced.MaxPreviewPatchBytes || unsupportedGitDiff(stdout) {
		return nil, nil, errors.New("binary, combined, or oversized Git hunks are not supported")
	}
	parsed, err := parseAdvancedUnifiedDiff(stdout)
	if err != nil {
		return nil, nil, err
	}
	selected := make(map[string]struct{}, len(spec.HunkIDs))
	for _, id := range spec.HunkIDs {
		if _, duplicate := selected[id]; duplicate {
			return nil, nil, errors.New("duplicate Git hunk identity")
		}
		selected[id] = struct{}{}
	}
	seenSelected := make(map[string]struct{}, len(selected))
	identities := make(map[string]advancedFileIdentity)
	selectedByPath := make(map[string][]parsedAdvancedHunk)
	hunks := make([]gitadvanced.Hunk, 0, len(parsed))
	for _, item := range parsed {
		if err := checkNativeGitPath(item.path); err != nil {
			return nil, nil, err
		}
		identity, ok := identities[item.path]
		if !ok {
			identity, err = e.captureAdvancedFileIdentity(ctx, root, item.path)
			if err != nil {
				return nil, nil, err
			}
			identities[item.path] = identity
		}
		contextLines := make([]string, 0)
		for _, line := range strings.Split(item.body, "\n") {
			if strings.HasPrefix(line, " ") {
				contextLines = append(contextLines, line)
			}
		}
		patch := item.header + item.body
		patchSum := sha256.Sum256([]byte(patch))
		contextDigest := gitadvanced.Fingerprint("hunk-context", item.path,
			strings.Join(contextLines, "\n"))
		id := gitadvanced.Fingerprint("hunk", string(spec.Operation), item.path,
			identity.baseBlob, identity.indexBlob, identity.worktreeSHA256,
			contextDigest, hex.EncodeToString(patchSum[:]))
		if len(selected) != 0 {
			if _, ok := selected[id]; !ok {
				continue
			}
			seenSelected[id] = struct{}{}
		}
		hunks = append(hunks, gitadvanced.Hunk{ID: id, Path: item.path,
			OldStart: item.oldStart, OldLines: item.oldLines,
			NewStart: item.newStart, NewLines: item.newLines,
			BaseBlob: identity.baseBlob, IndexBlob: identity.indexBlob,
			WorktreeSHA256: identity.worktreeSHA256, ContextSHA256: contextDigest,
			PatchSHA256: hex.EncodeToString(patchSum[:]), Patch: patch,
			Destructive: spec.Operation == gitadvanced.HunkRevert})
		selectedByPath[item.path] = append(selectedByPath[item.path], item)
	}
	if len(selected) != len(seenSelected) {
		return nil, nil, &gitadvanced.Error{Code: gitadvanced.FailureStalePreview,
			Message: "one or more selected hunk identities are absent from the current diff"}
	}
	files := make([]gitadvanced.FileImpact, 0, len(selectedByPath))
	for path, selectedHunks := range selectedByPath {
		identity := identities[path]
		source, reverse, change := identity.indexContent, false, "index <- worktree hunk"
		if spec.Operation == gitadvanced.HunkUnstage {
			source, reverse, change = identity.indexContent, true, "index <- base hunk"
		} else if spec.Operation == gitadvanced.HunkRevert {
			source, reverse, change = identity.worktreeContent, true, "worktree <- index hunk"
		}
		projected, projectionErr := applyAdvancedHunkProjection(source, selectedHunks, reverse)
		if projectionErr != nil {
			return nil, nil, projectionErr
		}
		files = append(files, gitadvanced.FileImpact{Path: path,
			BeforeSHA256: digestBytes(source), AfterSHA256: digestBytes(projected),
			Change: change, Destructive: spec.Operation == gitadvanced.HunkRevert})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return hunks, files, nil
}

func unsupportedGitDiff(patch string) bool {
	for line := range strings.SplitSeq(patch, "\n") {
		if line == "GIT binary patch" || strings.HasPrefix(line, "Binary files ") ||
			strings.HasPrefix(line, "diff --cc ") || strings.HasPrefix(line, "diff --combined ") {
			return true
		}
	}
	return false
}

func parseAdvancedUnifiedDiff(value string) ([]parsedAdvancedHunk, error) {
	if value == "" {
		return []parsedAdvancedHunk{}, nil
	}
	lines := strings.SplitAfter(value, "\n")
	var out []parsedAdvancedHunk
	currentPath, currentHeader := "", ""
	for index := 0; index < len(lines); {
		line := strings.TrimSuffix(lines[index], "\n")
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "diff --git ") {
			start := index
			currentPath = ""
			var oldPath, newPath string
			var oldSeen, newSeen bool
			index++
			for index < len(lines) {
				candidate := strings.TrimSuffix(strings.TrimSuffix(lines[index], "\n"), "\r")
				if strings.HasPrefix(candidate, "diff --git ") || strings.HasPrefix(candidate, "@@ ") {
					break
				}
				if name, found := strings.CutPrefix(candidate, "--- "); found {
					var valid bool
					oldPath, valid = advancedDiffPath(name, "a/")
					if !valid || oldSeen {
						return nil, errors.New("Git diff contains an unsupported or unsafe old path")
					}
					oldSeen = true
				} else if name, found := strings.CutPrefix(candidate, "+++ "); found {
					var valid bool
					newPath, valid = advancedDiffPath(name, "b/")
					if !valid || newSeen {
						return nil, errors.New("Git diff contains an unsupported or unsafe new path")
					}
					newSeen = true
				}
				index++
			}
			currentHeader = strings.Join(lines[start:index], "")
			if oldSeen != newSeen || (oldPath != "" && newPath != "" && oldPath != newPath) {
				return nil, errors.New("Git diff file headers disagree")
			}
			currentPath = newPath
			if currentPath == "" {
				currentPath = oldPath
			}
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			if currentPath == "" || currentHeader == "" {
				return nil, errors.New("Git hunk is missing an exact file header")
			}
			match := advancedHunkHeader.FindStringSubmatch(line)
			if match == nil {
				return nil, errors.New("Git hunk range is unsupported")
			}
			oldStart, _ := strconv.Atoi(match[1])
			oldLines := hunkRangeCount(match[2])
			newStart, _ := strconv.Atoi(match[3])
			newLines := hunkRangeCount(match[4])
			body := lines[index]
			index++
			for index < len(lines) {
				candidate := strings.TrimSuffix(strings.TrimSuffix(lines[index], "\n"), "\r")
				if strings.HasPrefix(candidate, "@@ ") || strings.HasPrefix(candidate, "diff --git ") {
					break
				}
				if candidate != "\\ No newline at end of file" && candidate != "" &&
					!strings.HasPrefix(candidate, " ") && !strings.HasPrefix(candidate, "+") &&
					!strings.HasPrefix(candidate, "-") {
					return nil, errors.New("Git hunk contains an unsupported record")
				}
				body += lines[index]
				index++
			}
			out = append(out, parsedAdvancedHunk{path: currentPath,
				header: currentHeader, body: body, oldStart: oldStart,
				oldLines: oldLines, newStart: newStart, newLines: newLines})
			if len(out) > gitadvanced.MaxHunks {
				return nil, errors.New("Git diff exceeds the hunk count bound")
			}
			continue
		}
		index++
	}
	return out, nil
}

func advancedDiffPath(value, prefix string) (string, bool) {
	// Git adds a tab delimiter to unquoted names with spaces. The spaces
	// before it, including any at the end of the filename, are significant.
	value = strings.TrimSuffix(value, "\t")
	if value == "/dev/null" {
		return "", true
	}
	if strings.HasPrefix(value, "\"") {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", false
		}
		value = decoded
	}
	path, ok := strings.CutPrefix(value, prefix)
	return path, ok && gitadvanced.ValidRelativePath(path)
}

func hunkRangeCount(value string) int {
	if value == "" {
		return 1
	}
	parsed, _ := strconv.Atoi(value)
	return parsed
}

type advancedFileIdentity struct {
	baseBlob, indexBlob     string
	baseSHA256, indexSHA256 string
	worktreeSHA256          string
	baseMode, indexMode     string
	baseContent             []byte
	indexContent            []byte
	worktreeContent         []byte
}

func (e *AdvancedExecutor) captureAdvancedFileIdentity(ctx context.Context,
	root, path string,
) (advancedFileIdentity, error) {
	if !gitadvanced.ValidRelativePath(path) {
		return advancedFileIdentity{}, errors.New("Git hunk path is unsafe")
	}
	baseMode, baseBlob, baseContent, err := e.readGitTreeFile(ctx, root, "HEAD", path)
	if err != nil {
		return advancedFileIdentity{}, err
	}
	indexMode, indexBlob, indexContent, err := e.readGitIndexFile(ctx, root, path)
	if err != nil {
		return advancedFileIdentity{}, err
	}
	for _, mode := range []string{baseMode, indexMode} {
		if mode == "120000" || mode == "160000" {
			return advancedFileIdentity{}, errors.New("symlink and submodule hunks are not supported")
		}
		if mode != "" && mode != "100644" && mode != "100755" {
			return advancedFileIdentity{}, errors.New("Git hunk has an unsupported file mode")
		}
	}
	worktree, err := readBoundedWorktreeFile(root, path, MaxAdvancedTrackedBytes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return advancedFileIdentity{}, err
	}
	return advancedFileIdentity{baseMode: baseMode, baseBlob: baseBlob,
		indexMode: indexMode, indexBlob: indexBlob,
		baseSHA256: digestBytes(baseContent), indexSHA256: digestBytes(indexContent),
		worktreeSHA256:  digestBytes(worktree),
		baseContent:     append([]byte(nil), baseContent...),
		indexContent:    append([]byte(nil), indexContent...),
		worktreeContent: append([]byte(nil), worktree...)}, nil
}

type advancedPatchRecord struct {
	kind    byte
	content string
}

func applyAdvancedHunkProjection(source []byte, hunks []parsedAdvancedHunk,
	reverse bool,
) ([]byte, error) {
	sourceLines := splitAdvancedExactLines(string(source))
	result := make([]string, 0, len(sourceLines))
	cursor := 0
	for _, hunk := range hunks {
		position := hunk.oldStart
		if reverse {
			position = hunk.newStart
		}
		if position > 0 {
			position--
		}
		if position < cursor || position > len(sourceLines) {
			return nil, errors.New("selected Git hunk ranges do not match the projected source")
		}
		result = append(result, sourceLines[cursor:position]...)
		cursor = position
		records, err := advancedHunkPatchRecords(hunk.body)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			consume := record.kind == ' ' || record.kind == '-'
			emit := record.kind == ' ' || record.kind == '+'
			if reverse {
				consume = record.kind == ' ' || record.kind == '+'
				emit = record.kind == ' ' || record.kind == '-'
			}
			if consume {
				if cursor >= len(sourceLines) || sourceLines[cursor] != record.content {
					return nil, errors.New("selected Git hunk content does not match the projected source")
				}
				cursor++
			}
			if emit {
				result = append(result, record.content)
			}
		}
	}
	result = append(result, sourceLines[cursor:]...)
	return []byte(strings.Join(result, "")), nil
}

func advancedHunkPatchRecords(body string) ([]advancedPatchRecord, error) {
	lines := strings.SplitAfter(body, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "@@ ") {
		return nil, errors.New("selected Git hunk body is invalid")
	}
	records := make([]advancedPatchRecord, 0, len(lines)-1)
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		candidate := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if candidate == `\ No newline at end of file` {
			if len(records) == 0 || !strings.HasSuffix(records[len(records)-1].content, "\n") {
				return nil, errors.New("selected Git hunk has an invalid no-newline marker")
			}
			records[len(records)-1].content = strings.TrimSuffix(
				records[len(records)-1].content, "\n")
			continue
		}
		if line[0] != ' ' && line[0] != '+' && line[0] != '-' {
			return nil, errors.New("selected Git hunk contains an invalid patch record")
		}
		records = append(records, advancedPatchRecord{kind: line[0], content: line[1:]})
	}
	return records, nil
}

func splitAdvancedExactLines(value string) []string {
	if value == "" {
		return []string{}
	}
	lines := strings.SplitAfter(value, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func (e *AdvancedExecutor) readGitTreeFile(ctx context.Context, root, tree,
	path string,
) (string, string, []byte, error) {
	if tree == "HEAD" {
		if _, _, code, _ := e.git(ctx, root, nil, "rev-parse", "--verify", "HEAD"); code != 0 {
			return "", "", nil, nil
		}
	}
	listing, stderr, code, err := e.git(ctx, root, nil, "ls-tree", "-z", tree, "--", path)
	if err != nil || code != 0 {
		return "", "", nil, fmt.Errorf("inspect Git tree file: %w: %s", err, strings.TrimSpace(stderr))
	}
	if listing == "" {
		return "", "", nil, nil
	}
	if strings.Count(listing, "\x00") != 1 {
		return "", "", nil, errors.New("Git tree path identity is ambiguous")
	}
	record := strings.TrimSuffix(listing, "\x00")
	metadata, observedPath, ok := strings.Cut(record, "\t")
	fields := strings.Fields(metadata)
	if !ok || observedPath != path || len(fields) != 3 || fields[1] != "blob" ||
		!gitadvanced.ValidObjectID(fields[2]) {
		return "", "", nil, errors.New("Git tree path identity is invalid")
	}
	if fields[0] == "120000" || fields[0] == "160000" {
		return "", "", nil, errors.New("symlink and submodule hunks are not supported")
	}
	if fields[0] != "100644" && fields[0] != "100755" {
		return "", "", nil, errors.New("Git tree path has an unsupported file mode")
	}
	content, stderr, code, err := e.git(ctx, root, nil, "cat-file", "blob", fields[2])
	if err != nil || code != 0 || len(content) > MaxAdvancedTrackedBytes {
		return "", "", nil, fmt.Errorf("read Git tree blob: %w: %s", err, strings.TrimSpace(stderr))
	}
	return fields[0], fields[2], []byte(content), nil
}

func (e *AdvancedExecutor) readGitIndexFile(ctx context.Context, root,
	path string,
) (string, string, []byte, error) {
	listing, stderr, code, err := e.git(ctx, root, nil, "ls-files", "-s", "-z", "--", path)
	if err != nil || code != 0 {
		return "", "", nil, fmt.Errorf("inspect Git index file: %w: %s", err, strings.TrimSpace(stderr))
	}
	if listing == "" {
		return "", "", nil, nil
	}
	if strings.Count(listing, "\x00") != 1 {
		return "", "", nil, errors.New("unmerged or ambiguous Git index entries cannot be hunk edited")
	}
	record := strings.TrimSuffix(listing, "\x00")
	metadata, observedPath, ok := strings.Cut(record, "\t")
	fields := strings.Fields(metadata)
	if !ok || observedPath != path || len(fields) != 3 || fields[2] != "0" ||
		!gitadvanced.ValidObjectID(fields[1]) {
		return "", "", nil, errors.New("Git index path identity is invalid")
	}
	if fields[0] == "120000" || fields[0] == "160000" {
		return "", "", nil, errors.New("symlink and submodule hunks are not supported")
	}
	if fields[0] != "100644" && fields[0] != "100755" {
		return "", "", nil, errors.New("Git index path has an unsupported file mode")
	}
	content, stderr, code, err := e.git(ctx, root, nil, "cat-file", "blob", fields[1])
	if err != nil || code != 0 || len(content) > MaxAdvancedTrackedBytes {
		return "", "", nil, fmt.Errorf("read Git index blob: %w: %s", err, strings.TrimSpace(stderr))
	}
	return fields[0], fields[1], []byte(content), nil
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func selectedHunkPatch(hunks []gitadvanced.Hunk) ([]byte, error) {
	if len(hunks) == 0 {
		return nil, errors.New("hunk execution requires at least one reviewed hunk")
	}
	type group struct {
		header string
		bodies []string
	}
	order := []string{}
	groups := make(map[string]*group)
	for _, hunk := range hunks {
		if !gitadvanced.ValidDigest(hunk.ID) || digestBytes([]byte(hunk.Patch)) != hunk.PatchSHA256 {
			return nil, errors.New("Git hunk patch evidence is invalid")
		}
		marker := strings.Index(hunk.Patch, "\n@@ ")
		if marker < 0 {
			return nil, errors.New("Git hunk patch has no range header")
		}
		marker++ // Keep the file header's final newline.
		header, body := hunk.Patch[:marker], hunk.Patch[marker:]
		value, ok := groups[hunk.Path]
		if !ok {
			value = &group{header: header}
			groups[hunk.Path] = value
			order = append(order, hunk.Path)
		} else if value.header != header {
			return nil, errors.New("Git hunk file headers drifted")
		}
		value.bodies = append(value.bodies, body)
	}
	var patch strings.Builder
	for _, path := range order {
		value := groups[path]
		patch.WriteString(value.header)
		for _, body := range value.bodies {
			patch.WriteString(body)
		}
	}
	if patch.Len() > gitadvanced.MaxPreviewPatchBytes {
		return nil, errors.New("selected Git hunk patch exceeds safe bound")
	}
	return []byte(patch.String()), nil
}

func (e *AdvancedExecutor) executeHunks(ctx context.Context, root string,
	preview gitadvanced.Preview,
) (string, string, int, error) {
	patch, err := selectedHunkPatch(preview.Hunks)
	if err != nil {
		return "", "", 0, err
	}
	args := []string{"apply", "--whitespace=nowarn", "--recount"}
	switch preview.Operation {
	case gitadvanced.HunkStage:
		args = append(args, "--cached")
	case gitadvanced.HunkUnstage:
		args = append(args, "--cached", "--reverse")
	case gitadvanced.HunkRevert:
		args = append(args, "--reverse")
	default:
		return "", "", 0, errors.New("unsupported Git hunk operation")
	}
	args = append(args, "-")
	return e.git(ctx, root, patch, args...)
}
