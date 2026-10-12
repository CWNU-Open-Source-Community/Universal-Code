package workspaceidentity

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
)

// PathPolicy narrows agent file access using the Run's pinned project
// configuration. Patterns are root-relative; matching a directory excludes all
// descendants. Neither include_hidden nor .gitignore negation overrides it.
type PathPolicy struct {
	ExcludePaths []string
}

func (p PathPolicy) Validate() error {
	if len(p.ExcludePaths) > 64 {
		return apperror.New(apperror.CodeInvalidArgument, "too many project exclusion patterns")
	}
	for _, pattern := range p.ExcludePaths {
		if pattern == "" || len(pattern) > 1024 || !utf8.ValidString(pattern) ||
			pattern != strings.TrimSpace(pattern) || strings.ContainsAny(pattern, `\:`) ||
			strings.HasPrefix(pattern, "/") || path.Clean(pattern) != pattern || pattern == "." {
			return apperror.New(apperror.CodeInvalidArgument, "invalid project exclusion pattern")
		}
		for _, component := range strings.Split(pattern, "/") {
			if component == ".." {
				return apperror.New(apperror.CodeInvalidArgument, "project exclusion cannot leave the workspace")
			}
			if _, err := path.Match(component, "probe"); err != nil {
				return apperror.New(apperror.CodeInvalidArgument, "invalid project exclusion glob")
			}
		}
		for _, r := range pattern {
			if unicode.IsControl(r) {
				return apperror.New(apperror.CodeInvalidArgument, "project exclusion contains control characters")
			}
		}
	}
	return nil
}

// Check also covers paths that do not exist yet, such as new files and move
// destinations. Callers retain their normal path resolution and permission checks.
func (p PathPolicy) Check(requested string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	relative := requested
	if relative == "" {
		relative = "."
	}
	if !utf8.ValidString(relative) || len(relative) > 4096 || path.Clean(relative) != relative ||
		strings.HasPrefix(relative, "/") || strings.ContainsAny(relative, `\:`) || relative == ".." ||
		strings.HasPrefix(relative, "../") {
		return apperror.New(apperror.CodeInvalidArgument, "project path must be workspace-relative")
	}
	for _, r := range relative {
		if unicode.IsControl(r) {
			return apperror.New(apperror.CodeInvalidArgument, "project path contains control characters")
		}
	}
	if p.Excludes(relative) {
		return apperror.New(apperror.CodePolicyDenied, "path is excluded by the task's project configuration")
	}
	return nil
}

func (p PathPolicy) Excludes(relative string) bool {
	for candidate := relative; candidate != "."; candidate = path.Dir(candidate) {
		for _, pattern := range p.ExcludePaths {
			if MatchPath(pattern, candidate) {
				return true
			}
		}
	}
	return false
}

// MatchPath matches a root-relative glob with ** directory segments.
func MatchPath(pattern, candidate string) bool {
	patternParts := strings.Split(pattern, "/")
	candidateParts := strings.Split(candidate, "/")
	type state struct{ pattern, candidate int }
	memo := make(map[state]bool)
	seen := make(map[state]bool)
	var match func(int, int) bool
	match = func(patternIndex, candidateIndex int) bool {
		current := state{patternIndex, candidateIndex}
		if seen[current] {
			return memo[current]
		}
		seen[current] = true
		matched := false
		switch {
		case patternIndex == len(patternParts):
			matched = candidateIndex == len(candidateParts)
		case patternParts[patternIndex] == "**":
			matched = match(patternIndex+1, candidateIndex) ||
				(candidateIndex < len(candidateParts) && match(patternIndex, candidateIndex+1))
		case candidateIndex < len(candidateParts):
			segment, _ := path.Match(patternParts[patternIndex], candidateParts[candidateIndex])
			matched = segment && match(patternIndex+1, candidateIndex+1)
		}
		memo[current] = matched
		return matched
	}
	return match(0, 0)
}
