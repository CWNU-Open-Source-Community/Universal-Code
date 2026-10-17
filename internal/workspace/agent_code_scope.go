package workspace

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/redact"
)

// AgentCodeScopePath describes the only files a narrowed child may discover.
// Directory scopes include descendants; file scopes include exactly one file.
type AgentCodeScopePath struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
}

func AgentCodeScopedGlobFiles(root, workspaceID, pattern, cursor string, limit int,
	scopes []AgentCodeScopePath, policies ...AgentCodePathPolicy,
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
	normalized, err := normalizeAgentCodeScopes(scopes)
	if err != nil {
		return AgentCodeGlob{}, err
	}
	fingerprint := workspace.cursorFingerprint("scoped-glob", workspaceID,
		workspace.fingerprint, pattern, agentCodeScopeFingerprint(normalized))
	after, err := decodeAgentCursor(cursor, fingerprint)
	if err != nil {
		return AgentCodeGlob{}, err
	}
	files, scanTruncated, err := workspace.scanScopedFiles(normalized)
	if err != nil {
		return AgentCodeGlob{}, err
	}
	matches := make([]string, 0, min(limit, len(files)))
	truncated, hasMore := scanTruncated, false
	for _, candidate := range files {
		if candidate <= after || !agentGlobMatch(pattern, candidate) {
			continue
		}
		if len(matches) == limit {
			truncated, hasMore = true, true
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

func AgentCodeScopedGrepFiles(root, workspaceID, query, pattern, cursor string,
	limit int, caseSensitive bool, scopes []AgentCodeScopePath, policies ...AgentCodePathPolicy,
) (AgentCodeGrep, error) {
	if err := validateAgentWorkspaceID(workspaceID); err != nil {
		return AgentCodeGrep{}, err
	}
	if query == "" || query != strings.TrimSpace(query) || !utf8.ValidString(query) ||
		utf8.RuneCountInString(query) > MaxAgentCodeGrepQueryRunes ||
		strings.ContainsRune(query, 0) {
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
	normalized, err := normalizeAgentCodeScopes(scopes)
	if err != nil {
		return AgentCodeGrep{}, err
	}
	fingerprint := workspace.cursorFingerprint("scoped-grep", workspaceID,
		workspace.fingerprint, query, pattern, fmt.Sprintf("case=%t", caseSensitive),
		agentCodeScopeFingerprint(normalized))
	after, err := decodeAgentCursor(cursor, fingerprint)
	if err != nil {
		return AgentCodeGrep{}, err
	}
	files, scanTruncated, err := workspace.scanScopedFiles(normalized)
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
		if !agentGlobMatch(pattern, candidate) ||
			(afterPath != "" && candidate < afterPath) {
			continue
		}
		if result.ScannedFiles >= MaxAgentCodeScanFiles {
			result.Truncated = true
			break
		}
		target, info, resolveErr := workspace.resolve(candidate, false)
		if resolveErr != nil || !info.Mode().IsRegular() ||
			info.Size() > MaxAgentCodeReadBytes {
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
		reader.Buffer(make([]byte, 0, 64*1024), MaxAgentCodeReadBytes)
		line, unsafe := 0, false
		for reader.Scan() {
			line++
			raw := reader.Text()
			if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
				unsafe = true
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
				result.Truncated, hasMore = true, true
				break
			}
			snippet := redact.String(strings.TrimSpace(raw))
			if len([]byte(snippet)) > MaxSearchSnippetBytes {
				projected, prefixErr := validExplorerUTF8Prefix([]byte(snippet),
					MaxSearchSnippetBytes)
				if prefixErr != nil {
					unsafe = true
					break
				}
				snippet = string(projected)
			}
			result.Matches = append(result.Matches,
				AgentCodeGrepMatch{Path: candidate, Line: line, Snippet: snippet})
		}
		result.ScannedFiles++
		if unsafe || reader.Err() != nil {
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

func normalizeAgentCodeScopes(scopes []AgentCodeScopePath) ([]AgentCodeScopePath, error) {
	if len(scopes) == 0 || len(scopes) > 32 {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"workspace search scope is missing or too large")
	}
	normalized := make([]AgentCodeScopePath, 0, len(scopes))
	for _, scope := range scopes {
		path, err := normalizeAgentPath(scope.Path, true)
		if err != nil {
			return nil, err
		}
		scope.Path = path
		normalized = append(normalized, scope)
	}
	sort.Slice(normalized, func(left, right int) bool {
		if normalized[left].Path == normalized[right].Path {
			return !normalized[left].Directory && normalized[right].Directory
		}
		return normalized[left].Path < normalized[right].Path
	})
	return normalized, nil
}

func agentCodeScopeFingerprint(scopes []AgentCodeScopePath) string {
	parts := make([]string, len(scopes))
	for index, scope := range scopes {
		parts[index] = fmt.Sprintf("%s:%t", scope.Path, scope.Directory)
	}
	return strings.Join(parts, "\x1e")
}

func (w agentWorkspace) scanScopedFiles(scopes []AgentCodeScopePath) ([]string, bool, error) {
	files := make([]string, 0, 256)
	seen := make(map[string]struct{})
	entriesSeen, directories := 0, 0
	truncated := false
	for _, scope := range scopes {
		if w.policy.Excludes(scope.Path) {
			continue
		}
		if scope.Path != "." && w.blocked(scope.Path, scope.Directory, false) {
			return nil, false, apperror.New(apperror.CodePolicyDenied,
				"workspace search scope is hidden or ignored")
		}
		if !scope.Directory {
			target, info, err := w.resolve(scope.Path, false)
			if err != nil || !info.Mode().IsRegular() {
				truncated = true
				continue
			}
			_ = target
			if _, duplicate := seen[scope.Path]; !duplicate {
				seen[scope.Path] = struct{}{}
				files = append(files, scope.Path)
			}
			continue
		}
		queue := []string{scope.Path}
		for len(queue) != 0 {
			if directories >= MaxAgentCodeScanDirectories ||
				entriesSeen >= MaxAgentCodeScanEntries {
				truncated = true
				break
			}
			relative := queue[0]
			queue = queue[1:]
			directories++
			target, info, resolveErr := w.resolve(relative, false)
			if resolveErr != nil || !info.IsDir() {
				truncated = true
				continue
			}
			entries, readErr := os.ReadDir(target)
			if readErr != nil {
				truncated = true
				continue
			}
			sort.Slice(entries, func(left, right int) bool {
				return entries[left].Name() < entries[right].Name()
			})
			for _, entry := range entries {
				if entriesSeen >= MaxAgentCodeScanEntries {
					truncated = true
					break
				}
				entriesSeen++
				candidate := relative + "/" + entry.Name()
				if relative == "." {
					candidate = entry.Name()
				}
				if w.blocked(candidate, entry.IsDir(), false) {
					continue
				}
				if entry.Type()&os.ModeSymlink != 0 {
					truncated = true
					continue
				}
				entryInfo, infoErr := entry.Info()
				_, resolvedInfo, verifyErr := w.resolve(candidate, false)
				if infoErr != nil || verifyErr != nil || !os.SameFile(entryInfo, resolvedInfo) {
					truncated = true
					continue
				}
				if entryInfo.IsDir() {
					queue = append(queue, candidate)
				} else if entryInfo.Mode().IsRegular() {
					if _, duplicate := seen[candidate]; !duplicate {
						seen[candidate] = struct{}{}
						files = append(files, candidate)
					}
				} else {
					truncated = true
				}
			}
		}
	}
	sort.Strings(files)
	return files, truncated, nil
}
