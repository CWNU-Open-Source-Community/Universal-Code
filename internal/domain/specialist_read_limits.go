package domain

import (
	"errors"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Read paths are literal workspace-relative files or directory subtrees. A
// caller must explicitly use "." to authorize the whole workspace.
func ValidateSpecialistReadLimits(paths []string, tools, cost int64) error {
	if len(paths) > 32 || tools < 0 || cost < 0 || tools > MaxAgentTokenReservation || cost > MaxAgentTokenReservation {
		return errors.New("invalid child read limits")
	}
	if len(paths) > 0 && tools == 0 {
		return errors.New("child read scope requires a positive tool-call limit")
	}
	seen := map[string]bool{}
	for _, value := range paths {
		if value == "" || len(value) > 1024 || !utf8.ValidString(value) || strings.TrimSpace(value) != value || path.Clean(value) != value || strings.HasPrefix(value, "/") || value == ".." || strings.HasPrefix(value, "../") || strings.ContainsAny(value, `\:*?[]`) || seen[value] {
			return errors.New("child read paths must be distinct literal workspace-relative paths")
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return errors.New("child read path contains control characters")
			}
		}
		seen[value] = true
	}
	return nil
}
