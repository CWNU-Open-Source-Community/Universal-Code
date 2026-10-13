package workspace

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/workspaceidentity"
)

const (
	AgentCodeToolsProtocolVersion = "agent-code-tools.v1"
	MaxAgentCodePageSize          = 200
	MaxAgentCodeReadBytes         = 256 * 1024
	MaxAgentCodeReadLines         = 2000
	MaxAgentCodeScanDirectories   = 256
	MaxAgentCodeScanEntries       = 4096
	MaxAgentCodeScanFiles         = 512
	MaxAgentCodeGrepMatches       = 200
	MaxAgentCodeGlobPatternRunes  = 256
	MaxAgentCodeGrepQueryRunes    = 256
	MaxAgentCodeCursorBytes       = 8 * 1024
)

type AgentCodeEntry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"size_bytes"`
}

type AgentCodeList struct {
	ProtocolVersion string           `json:"protocol_version"`
	WorkspaceID     string           `json:"workspace_id"`
	RootFingerprint string           `json:"root_fingerprint"`
	Path            string           `json:"path"`
	Items           []AgentCodeEntry `json:"items"`
	NextCursor      string           `json:"next_cursor,omitempty"`
	Truncated       bool             `json:"truncated"`
}

type AgentCodeRead struct {
	ProtocolVersion string `json:"protocol_version"`
	WorkspaceID     string `json:"workspace_id"`
	RootFingerprint string `json:"root_fingerprint"`
	Path            string `json:"path"`
	Content         string `json:"content"`
	ContentSHA256   string `json:"content_sha256"`
	Encoding        string `json:"encoding"`
	Newline         string `json:"newline"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	TotalLines      int    `json:"total_lines"`
	TotalBytes      int64  `json:"total_bytes"`
	RedactionCount  int    `json:"redaction_count"`
	Truncated       bool   `json:"truncated"`
}

type AgentCodeGlob struct {
	ProtocolVersion string   `json:"protocol_version"`
	WorkspaceID     string   `json:"workspace_id"`
	RootFingerprint string   `json:"root_fingerprint"`
	Pattern         string   `json:"pattern"`
	Paths           []string `json:"paths"`
	NextCursor      string   `json:"next_cursor,omitempty"`
	Truncated       bool     `json:"truncated"`
}

type AgentCodeGrepMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

type AgentCodeGrep struct {
	ProtocolVersion string               `json:"protocol_version"`
	WorkspaceID     string               `json:"workspace_id"`
	RootFingerprint string               `json:"root_fingerprint"`
	Query           string               `json:"query"`
	Matches         []AgentCodeGrepMatch `json:"matches"`
	NextCursor      string               `json:"next_cursor,omitempty"`
	ScannedFiles    int                  `json:"scanned_files"`
	Truncated       bool                 `json:"truncated"`
}

type agentCodeCursor struct {
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
	After       string `json:"after"`
}

type agentIgnoreRule struct {
	pattern  string
	anchored bool
	dirOnly  bool
	negated  bool
}

type agentWorkspace struct {
	root        string
	fingerprint string
	ignore      []agentIgnoreRule
	policy      AgentCodePathPolicy
}

func AgentCodeRootFingerprint(root string) (string, error) {
	workspace, err := openAgentWorkspace(root)
	if err != nil {
		return "", err
	}
	return workspace.fingerprint, nil
}

func AgentCodeListDirectory(root, workspaceID, requested, cursor string, limit int,
	includeHidden bool, policies ...AgentCodePathPolicy,
) (AgentCodeList, error) {
	if err := validateAgentWorkspaceID(workspaceID); err != nil {
		return AgentCodeList{}, err
	}
	if limit <= 0 || limit > MaxAgentCodePageSize {
		return AgentCodeList{}, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("workspace list limit must be between 1 and %d", MaxAgentCodePageSize))
	}
	workspace, err := openAgentWorkspace(root, policies...)
	if err != nil {
		return AgentCodeList{}, err
	}
	relative, err := normalizeAgentPath(requested, true)
	if err != nil {
		return AgentCodeList{}, err
	}
	if err := workspace.policy.Check(relative); err != nil {
		return AgentCodeList{}, err
	}
	target, info, err := workspace.resolve(relative, false)
	if err != nil {
		return AgentCodeList{}, err
	}
	if !info.IsDir() {
		return AgentCodeList{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace list target is not a directory")
	}
	fingerprint := workspace.cursorFingerprint("list", workspaceID, workspace.fingerprint, relative,
		fmt.Sprintf("hidden=%t", includeHidden))
	after, err := decodeAgentCursor(cursor, fingerprint)
	if err != nil {
		return AgentCodeList{}, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return AgentCodeList{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace directory could not be listed")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	items := make([]AgentCodeEntry, 0, min(limit, len(entries)))
	truncated := false
	hasMore := false
	for _, entry := range entries {
		entryPath := entry.Name()
		if relative != "." {
			entryPath = relative + "/" + entry.Name()
		}
		if entryPath <= after || workspace.blocked(entryPath, entry.IsDir(), includeHidden) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			truncated = true
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			truncated = true
			continue
		}
		_, resolvedInfo, resolveErr := workspace.resolve(entryPath, false)
		if resolveErr != nil || !os.SameFile(info, resolvedInfo) {
			truncated = true
			continue
		}
		if len(items) == limit {
			truncated = true
			hasMore = true
			break
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		}
		items = append(items, AgentCodeEntry{Path: entryPath, Kind: kind,
			SizeBytes: info.Size()})
	}
	next := ""
	if hasMore && len(items) != 0 {
		next, err = encodeAgentCursor(fingerprint, items[len(items)-1].Path)
		if err != nil {
			return AgentCodeList{}, err
		}
	}
	return AgentCodeList{ProtocolVersion: AgentCodeToolsProtocolVersion,
		WorkspaceID: workspaceID, RootFingerprint: workspace.fingerprint,
		Path: relative, Items: items, NextCursor: next, Truncated: truncated}, nil
}

func AgentCodeReadFile(root, workspaceID, requested string, startLine, endLine int,
	includeHidden bool, policies ...AgentCodePathPolicy,
) (AgentCodeRead, error) {
	if err := validateAgentWorkspaceID(workspaceID); err != nil {
		return AgentCodeRead{}, err
	}
	if startLine <= 0 || endLine < startLine ||
		endLine-startLine >= MaxAgentCodeReadLines {
		return AgentCodeRead{}, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("workspace read line range must contain 1 to %d lines", MaxAgentCodeReadLines))
	}
	workspace, err := openAgentWorkspace(root, policies...)
	if err != nil {
		return AgentCodeRead{}, err
	}
	relative, err := normalizeAgentPath(requested, false)
	if err != nil {
		return AgentCodeRead{}, err
	}
	if workspace.blocked(relative, false, includeHidden) {
		return AgentCodeRead{}, apperror.New(apperror.CodePolicyDenied,
			"workspace file is hidden or ignored")
	}
	target, info, err := workspace.resolve(relative, false)
	if err != nil {
		return AgentCodeRead{}, err
	}
	if !info.Mode().IsRegular() {
		return AgentCodeRead{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace read target is not a regular file")
	}
	if info.Size() > MaxAgentCodeReadBytes {
		return AgentCodeRead{}, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("workspace file exceeds %d bytes", MaxAgentCodeReadBytes))
	}
	data, err := readAgentCodeFile(target, info)
	if err != nil {
		return AgentCodeRead{}, err
	}
	hasBOM := bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})
	textData := data
	encoding := "utf-8"
	if hasBOM {
		textData = textData[3:]
		encoding = "utf-8-bom"
	}
	if !utf8.Valid(textData) || agentLooksBinary(textData) {
		return AgentCodeRead{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace read supports UTF-8 text files only")
	}
	normalizedText := strings.ReplaceAll(string(textData), "\r\n", "\n")
	lines := strings.Split(normalizedText, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)
	if totalLines == 0 {
		totalLines = 1
		lines = []string{""}
	}
	if startLine > totalLines {
		return AgentCodeRead{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace read start line exceeds the current file")
	}
	from := min(startLine, totalLines+1) - 1
	to := min(endLine, totalLines)
	selected := ""
	if from < to {
		selected = strings.Join(lines[from:to], "\n")
	}
	redacted := redact.Text(selected)
	sum := sha256.Sum256(data)
	return AgentCodeRead{ProtocolVersion: AgentCodeToolsProtocolVersion,
		WorkspaceID: workspaceID, RootFingerprint: workspace.fingerprint,
		Path: relative, Content: redacted.Text, ContentSHA256: hex.EncodeToString(sum[:]),
		Encoding: encoding, Newline: agentNewlineStyle(textData), StartLine: startLine,
		EndLine: min(endLine, totalLines), TotalLines: totalLines, TotalBytes: info.Size(),
		RedactionCount: len(redacted.Findings), Truncated: startLine > 1 || endLine < totalLines}, nil
}

func AgentCodeGlobFiles(root, workspaceID, pattern, cursor string, limit int,
	includeHidden bool, policies ...AgentCodePathPolicy,
) (AgentCodeGlob, error) {
	if err := validateAgentWorkspaceID(workspaceID); err != nil {
		return AgentCodeGlob{}, err
	}
	pattern, err := normalizeAgentPattern(pattern)
	if err != nil {
		return AgentCodeGlob{}, err
	}
	if limit <= 0 || limit > MaxAgentCodePageSize {
		return AgentCodeGlob{}, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("workspace glob limit must be between 1 and %d", MaxAgentCodePageSize))
	}
	workspace, err := openAgentWorkspace(root, policies...)
	if err != nil {
		return AgentCodeGlob{}, err
	}
	fingerprint := workspace.cursorFingerprint("glob", workspaceID, workspace.fingerprint, pattern,
		fmt.Sprintf("hidden=%t", includeHidden))
	after, err := decodeAgentCursor(cursor, fingerprint)
	if err != nil {
		return AgentCodeGlob{}, err
	}
	files, scanTruncated, err := workspace.scanFiles(includeHidden)
	if err != nil {
		return AgentCodeGlob{}, err
	}
	matches := make([]string, 0, min(limit, len(files)))
	truncated := scanTruncated
	hasMore := false
	for _, candidate := range files {
		if candidate <= after || !agentGlobMatch(pattern, candidate) {
			continue
		}
		if len(matches) == limit {
			truncated = true
			hasMore = true
			break
		}
		matches = append(matches, candidate)
	}
	next := ""
	if hasMore && len(matches) != 0 {
		next, err = encodeAgentCursor(fingerprint, matches[len(matches)-1])
		if err != nil {
			return AgentCodeGlob{}, err
		}
	}
	return AgentCodeGlob{ProtocolVersion: AgentCodeToolsProtocolVersion,
		WorkspaceID: workspaceID, RootFingerprint: workspace.fingerprint,
		Pattern: pattern, Paths: matches, NextCursor: next, Truncated: truncated}, nil
}

func AgentCodeGrepFiles(root, workspaceID, query, pattern, cursor string, limit int,
	caseSensitive, includeHidden bool, policies ...AgentCodePathPolicy,
) (AgentCodeGrep, error) {
	if err := validateAgentWorkspaceID(workspaceID); err != nil {
		return AgentCodeGrep{}, err
	}
	if query == "" || query != strings.TrimSpace(query) || !utf8.ValidString(query) ||
		utf8.RuneCountInString(query) > MaxAgentCodeGrepQueryRunes || strings.ContainsRune(query, 0) {
		return AgentCodeGrep{}, apperror.New(apperror.CodeInvalidArgument,
			"workspace grep query must be normalized, non-empty, and bounded")
	}
	if pattern == "" {
		pattern = "**"
	}
	pattern, err := normalizeAgentPattern(pattern)
	if err != nil {
		return AgentCodeGrep{}, err
	}
	if limit <= 0 || limit > MaxAgentCodeGrepMatches {
		return AgentCodeGrep{}, apperror.New(apperror.CodeInvalidArgument,
			fmt.Sprintf("workspace grep limit must be between 1 and %d", MaxAgentCodeGrepMatches))
	}
	workspace, err := openAgentWorkspace(root, policies...)
	if err != nil {
		return AgentCodeGrep{}, err
	}
	fingerprint := workspace.cursorFingerprint("grep", workspaceID, workspace.fingerprint, query, pattern,
		fmt.Sprintf("case=%t", caseSensitive), fmt.Sprintf("hidden=%t", includeHidden))
	after, err := decodeAgentCursor(cursor, fingerprint)
	if err != nil {
		return AgentCodeGrep{}, err
	}
	files, scanTruncated, err := workspace.scanFiles(includeHidden)
	if err != nil {
		return AgentCodeGrep{}, err
	}
	needle := query
	if !caseSensitive {
		needle = strings.ToLower(needle)
	}
	result := AgentCodeGrep{ProtocolVersion: AgentCodeToolsProtocolVersion,
		WorkspaceID: workspaceID, RootFingerprint: workspace.fingerprint,
		Query: query, Matches: []AgentCodeGrepMatch{}, Truncated: scanTruncated}
	afterPath := ""
	if separator := strings.LastIndex(after, "\x1f"); separator >= 0 {
		afterPath = after[:separator]
	}
	hasMore := false
	for _, candidate := range files {
		if !agentGlobMatch(pattern, candidate) {
			continue
		}
		if afterPath != "" && candidate < afterPath {
			continue
		}
		if result.ScannedFiles >= MaxAgentCodeScanFiles {
			result.Truncated = true
			break
		}
		target, info, resolveErr := workspace.resolve(candidate, false)
		if resolveErr != nil || !info.Mode().IsRegular() || info.Size() > MaxAgentCodeReadBytes {
			result.Truncated = true
			continue
		}
		data, readErr := readAgentCodeFile(target, info)
		if readErr != nil || !utf8.Valid(data) || agentLooksBinary(data) {
			result.Truncated = true
			continue
		}
		if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
			data = data[3:]
		}
		reader := bufio.NewScanner(bytes.NewReader(data))
		buffer := make([]byte, 0, 64*1024)
		reader.Buffer(buffer, MaxAgentCodeReadBytes)
		line := 0
		binary := false
		for reader.Scan() {
			line++
			raw := reader.Text()
			if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
				binary = true
				break
			}
			haystack := raw
			if !caseSensitive {
				haystack = strings.ToLower(haystack)
			}
			if !strings.Contains(haystack, needle) {
				continue
			}
			key := fmt.Sprintf("%s\x1f%010d", candidate, line)
			if key <= after {
				continue
			}
			if len(result.Matches) == limit {
				result.Truncated = true
				hasMore = true
				break
			}
			snippet := redact.String(strings.TrimSpace(raw))
			if len([]byte(snippet)) > MaxSearchSnippetBytes {
				projected, prefixErr := validExplorerUTF8Prefix([]byte(snippet),
					MaxSearchSnippetBytes)
				if prefixErr != nil {
					binary = true
					break
				}
				snippet = string(projected)
			}
			result.Matches = append(result.Matches,
				AgentCodeGrepMatch{Path: candidate, Line: line, Snippet: snippet})
		}
		result.ScannedFiles++
		if binary || reader.Err() != nil {
			result.Truncated = true
		}
		if hasMore {
			break
		}
	}
	if hasMore && len(result.Matches) != 0 {
		last := result.Matches[len(result.Matches)-1]
		result.NextCursor, err = encodeAgentCursor(fingerprint,
			fmt.Sprintf("%s\x1f%010d", last.Path, last.Line))
		if err != nil {
			return AgentCodeGrep{}, err
		}
	}
	return result, nil
}

// AgentCodeResolveWritePath applies the same strict path and redirect checks
// used by model reads. The final component may be absent, but every parent
// must exist and no component may be a symlink, junction, or case alias.
func AgentCodeResolveWritePath(root, requested string, allowMissing bool, policies ...AgentCodePathPolicy) (string, string, error) {
	return agentCodeResolveMutationPath(root, requested, allowMissing, false, policies...)
}

// AgentCodeResolveCreatePath verifies a create proposal without writing any
// directory. Existing components, hidden paths and ignored paths remain closed.
func AgentCodeResolveCreatePath(root, requested string, policies ...AgentCodePathPolicy) (string, string, error) {
	return agentCodeResolveMutationPath(root, requested, true, true, policies...)
}

func agentCodeResolveMutationPath(root, requested string, allowMissing, allowMissingParents bool, policies ...AgentCodePathPolicy) (string, string, error) {
	workspace, err := openAgentWorkspace(root, policies...)
	if err != nil {
		return "", "", err
	}
	relative, err := normalizeAgentPath(requested, false)
	if err != nil {
		return "", "", err
	}
	if workspace.blocked(relative, false, false) {
		return "", "", apperror.New(apperror.CodePolicyDenied,
			"workspace mutation target is hidden, ignored, or excluded by project configuration")
	}
	target, _, err := workspace.resolvePath(relative, allowMissing, allowMissingParents)
	if err != nil {
		return "", "", err
	}
	return target, workspace.fingerprint, nil
}

// AgentCodeReadMutationSource returns exact, unredacted UTF-8 bytes only to the
// Go-owned proposal builder. It shares the model read path policy and verifies
// that the opened handle is the same regular file that was resolved, closing the
// resolve/open symlink-swap window before any patch text is derived.
func AgentCodeReadMutationSource(root, requested string, policies ...AgentCodePathPolicy) (string, string, error) {
	workspace, err := openAgentWorkspace(root, policies...)
	if err != nil {
		return "", "", err
	}
	relative, err := normalizeAgentPath(requested, false)
	if err != nil {
		return "", "", err
	}
	if workspace.blocked(relative, false, false) {
		return "", "", apperror.New(apperror.CodePolicyDenied,
			"workspace mutation source is hidden, ignored, or excluded by project configuration")
	}
	target, info, err := workspace.resolve(relative, false)
	if err != nil {
		return "", "", err
	}
	data, err := readAgentCodeFile(target, info)
	if err != nil {
		return "", "", err
	}
	if !utf8.Valid(data) || agentLooksBinary(data) {
		return "", "", apperror.New(apperror.CodeFailedPrecondition,
			"workspace mutation supports UTF-8 text files only")
	}
	sum := sha256.Sum256(data)
	return string(data), hex.EncodeToString(sum[:]), nil
}

func readAgentCodeFile(target string, expected os.FileInfo) ([]byte, error) {
	if expected == nil || !expected.Mode().IsRegular() {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace read target is not a regular file")
	}
	if expected.Size() > MaxAgentCodeReadBytes {
		return nil, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("workspace file exceeds %d bytes", MaxAgentCodeReadBytes))
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace file could not be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return nil, apperror.New(apperror.CodeConflict,
			"workspace file changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxAgentCodeReadBytes+1))
	if err != nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace file could not be read")
	}
	if len(data) > MaxAgentCodeReadBytes {
		return nil, apperror.New(apperror.CodeResourceExhausted,
			fmt.Sprintf("workspace file exceeds %d bytes", MaxAgentCodeReadBytes))
	}
	completed, err := file.Stat()
	if err != nil || !os.SameFile(opened, completed) || opened.Size() != completed.Size() ||
		!opened.ModTime().Equal(completed.ModTime()) {
		return nil, apperror.New(apperror.CodeConflict,
			"workspace file changed while it was read")
	}
	return data, nil
}

func openAgentWorkspace(root string, policies ...AgentCodePathPolicy) (agentWorkspace, error) {
	root = strings.TrimSpace(root)
	if root == "" || !utf8.ValidString(root) {
		return agentWorkspace{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace root is unavailable")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return agentWorkspace{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace root could not be resolved")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return agentWorkspace{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace root must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !sameExplorerPath(absolute, resolved) {
		return agentWorkspace{}, apperror.New(apperror.CodePolicyDenied,
			"workspace root cannot be redirected")
	}
	canonical := filepath.Clean(resolved)
	canonicalInfo, err := os.Lstat(canonical)
	if err != nil || !canonicalInfo.IsDir() || canonicalInfo.Mode()&os.ModeSymlink != 0 {
		return agentWorkspace{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace root identity could not be inspected")
	}
	fingerprint, err := workspaceidentity.Fingerprint(canonical)
	if err != nil {
		return agentWorkspace{}, apperror.New(apperror.CodeFailedPrecondition,
			"workspace root identity could not be established")
	}
	workspace := agentWorkspace{root: canonical, fingerprint: fingerprint}
	for _, policy := range policies {
		workspace.policy.ExcludePaths = append(workspace.policy.ExcludePaths, policy.ExcludePaths...)
	}
	if err := workspace.policy.Validate(); err != nil {
		return agentWorkspace{}, err
	}
	workspace.ignore = loadAgentIgnoreRules(canonical)
	return workspace, nil
}

func (w agentWorkspace) resolve(relative string, allowMissingFinal bool) (string, os.FileInfo, error) {
	return w.resolvePath(relative, allowMissingFinal, false)
}

func (w agentWorkspace) resolvePath(relative string, allowMissingFinal, allowMissingParents bool) (string, os.FileInfo, error) {
	current := w.root
	components := strings.Split(relative, "/")
	if relative == "." {
		components = nil
	}
	for index, component := range components {
		entries, err := os.ReadDir(current)
		if err != nil {
			return "", nil, apperror.New(apperror.CodeFailedPrecondition,
				"workspace path parent could not be inspected")
		}
		exact := false
		caseAlias := false
		for _, entry := range entries {
			if entry.Name() == component {
				exact = true
				break
			}
			if strings.EqualFold(entry.Name(), component) {
				caseAlias = true
			}
		}
		last := index == len(components)-1
		if !exact {
			if caseAlias {
				return "", nil, apperror.New(apperror.CodeConflict,
					"workspace path casing does not match the stored entry")
			}
			if (allowMissingFinal && last) || allowMissingParents {
				target := filepath.Join(append([]string{current}, components[index:]...)...)
				if !explorerWithinRoot(w.root, target) {
					return "", nil, apperror.New(apperror.CodePolicyDenied,
						"workspace path cannot leave the workspace")
				}
				return target, nil, nil
			}
			return "", nil, apperror.New(apperror.CodeNotFound,
				"workspace entry was not found")
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return "", nil, apperror.New(apperror.CodeFailedPrecondition,
				"workspace entry could not be inspected")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, apperror.New(apperror.CodePolicyDenied,
				"workspace tools do not follow symbolic links or reparse points")
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil || !explorerWithinRoot(w.root, resolved) || !sameExplorerPath(current, resolved) {
			return "", nil, apperror.New(apperror.CodePolicyDenied,
				"workspace tools do not follow redirected paths")
		}
		if !last && !info.IsDir() {
			return "", nil, apperror.New(apperror.CodeFailedPrecondition,
				"workspace path parent is not a directory")
		}
	}
	info, err := os.Lstat(current)
	if err != nil {
		return "", nil, apperror.New(apperror.CodeFailedPrecondition,
			"workspace entry could not be inspected")
	}
	return current, info, nil
}

func (w agentWorkspace) scanFiles(includeHidden bool) ([]string, bool, error) {
	queue := []string{"."}
	files := make([]string, 0, 256)
	entriesSeen := 0
	directories := 0
	truncated := false
	for len(queue) != 0 {
		if directories >= MaxAgentCodeScanDirectories {
			truncated = true
			break
		}
		relative := queue[0]
		queue = queue[1:]
		directories++
		target := w.root
		if relative != "." {
			resolved, info, err := w.resolve(relative, false)
			if err != nil || !info.IsDir() {
				truncated = true
				continue
			}
			target = resolved
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			if relative == "." {
				return nil, false, apperror.New(apperror.CodeFailedPrecondition,
					"workspace root could not be scanned")
			}
			truncated = true
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if entriesSeen >= MaxAgentCodeScanEntries {
				truncated = true
				break
			}
			entriesSeen++
			candidate := entry.Name()
			if relative != "." {
				candidate = relative + "/" + entry.Name()
			}
			if w.blocked(candidate, entry.IsDir(), includeHidden) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				truncated = true
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				truncated = true
				continue
			}
			_, resolvedInfo, resolveErr := w.resolve(candidate, false)
			if resolveErr != nil || !os.SameFile(info, resolvedInfo) {
				truncated = true
				continue
			}
			if info.IsDir() {
				queue = append(queue, candidate)
			} else if info.Mode().IsRegular() {
				files = append(files, candidate)
			} else {
				truncated = true
			}
		}
	}
	sort.Strings(files)
	return files, truncated, nil
}

func (w agentWorkspace) blocked(relative string, directory, includeHidden bool) bool {
	if w.policy.Excludes(relative) {
		return true
	}
	if relative == ".git" || strings.HasPrefix(relative, ".git/") ||
		strings.HasPrefix(path.Base(relative), ".cyberagent-edit-") {
		return true
	}
	if !includeHidden {
		for _, component := range strings.Split(relative, "/") {
			// Repository-owned CI, ownership, and contribution policy is code
			// evidence, so .github is the sole dot-path allowlist. .git and all
			// other hidden namespaces remain unavailable to the model.
			if strings.HasPrefix(component, ".") && component != ".github" {
				return true
			}
		}
	}
	// Apply the rules to each parent before the leaf, just as a directory
	// scan does. A file exception cannot reopen an ignored parent directory.
	components := strings.Split(relative, "/")
	for index := range components {
		candidate := strings.Join(components[:index+1], "/")
		isDirectory := index < len(components)-1 || directory
		ignored := false
		for _, rule := range w.ignore {
			if agentIgnoreMatches(rule, candidate, isDirectory) {
				ignored = !rule.negated
			}
		}
		if ignored {
			return true
		}
	}
	return false
}

func loadAgentIgnoreRules(root string) []agentIgnoreRule {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil || len(data) > 64*1024 || !utf8.Valid(data) {
		return nil
	}
	rules := make([]agentIgnoreRule, 0, 32)
	for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		value := strings.TrimSpace(raw)
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		rule := agentIgnoreRule{}
		if strings.HasPrefix(value, "!") {
			rule.negated = true
			value = strings.TrimPrefix(value, "!")
		}
		rule.anchored = strings.HasPrefix(value, "/")
		value = strings.TrimPrefix(value, "/")
		rule.dirOnly = strings.HasSuffix(value, "/")
		value = strings.TrimSuffix(value, "/")
		if value == "" || strings.ContainsRune(value, 0) {
			continue
		}
		rule.pattern = filepath.ToSlash(value)
		rules = append(rules, rule)
	}
	return rules
}

func agentIgnoreMatches(rule agentIgnoreRule, relative string, directory bool) bool {
	if rule.dirOnly && !directory {
		return false
	}
	if rule.anchored || strings.Contains(rule.pattern, "/") {
		matched, _ := path.Match(rule.pattern, relative)
		return matched
	}
	matched, _ := path.Match(rule.pattern, path.Base(relative))
	return matched
}

func normalizeAgentPath(value string, allowRoot bool) (string, error) {
	original := value
	if value == "" && allowRoot {
		value = "."
	}
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > MaxExplorerPathRunes || strings.ContainsRune(value, 0) ||
		filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.ContainsAny(value, `\:`) {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"workspace path must be a bounded canonical relative path")
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return "", apperror.New(apperror.CodeInvalidArgument,
				"workspace path cannot contain control characters")
		}
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", apperror.New(apperror.CodePolicyDenied,
			"workspace path cannot leave the workspace")
	}
	if clean != value || (!allowRoot && clean == ".") || (original == "" && !allowRoot) {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"workspace path must be canonical")
	}
	return clean, nil
}

func normalizeAgentPattern(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > MaxAgentCodeGlobPatternRunes || strings.ContainsRune(value, 0) ||
		strings.ContainsAny(value, `\:`) || strings.HasPrefix(value, "/") ||
		value == ".." || strings.HasPrefix(value, "../") {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"workspace glob pattern must be bounded and workspace-relative")
	}
	if _, err := path.Match(strings.ReplaceAll(value, "**", "*"), "probe"); err != nil {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"workspace glob pattern is invalid")
	}
	return value, nil
}

func agentGlobMatch(pattern, candidate string) bool {
	return workspaceidentity.MatchPath(pattern, candidate)
}

func validateAgentWorkspaceID(value string) error {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		strings.ContainsRune(value, 0) {
		return apperror.New(apperror.CodeInvalidArgument, "workspace identity is invalid")
	}
	return nil
}

func agentCursorFingerprint(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%d:", len(part))
		_, _ = io.WriteString(hash, part)
		_, _ = io.WriteString(hash, "|")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func encodeAgentCursor(fingerprint, after string) (string, error) {
	data, err := json.Marshal(agentCodeCursor{Version: AgentCodeToolsProtocolVersion,
		Fingerprint: fingerprint, After: after})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeAgentCursor(value, fingerprint string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > MaxAgentCodeCursorBytes || value != strings.TrimSpace(value) {
		return "", apperror.New(apperror.CodeInvalidArgument, "workspace cursor is invalid")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", apperror.New(apperror.CodeInvalidArgument, "workspace cursor is invalid")
	}
	var cursor agentCodeCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return "", apperror.New(apperror.CodeInvalidArgument, "workspace cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) ||
		cursor.Version != AgentCodeToolsProtocolVersion || cursor.Fingerprint != fingerprint ||
		cursor.After == "" || !utf8.ValidString(cursor.After) || strings.ContainsRune(cursor.After, 0) {
		return "", apperror.New(apperror.CodeConflict,
			"workspace cursor does not match the current query or root")
	}
	return cursor.After, nil
}

func agentLooksBinary(data []byte) bool {
	if bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	if len(data) == 0 {
		return false
	}
	controls := 0
	for _, current := range data {
		if current < 0x20 && current != '\n' && current != '\r' && current != '\t' && current != '\f' {
			controls++
		}
	}
	return controls*100 > len(data)*2
}

func agentNewlineStyle(data []byte) string {
	crlf := bytes.Count(data, []byte("\r\n"))
	lf := bytes.Count(data, []byte("\n")) - crlf
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
