package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
)

// NormalizeThreadTitle keeps the existing Thread API's 4096-character bound.
func NormalizeThreadTitle(title string) (string, error) {
	hasControl := strings.IndexFunc(title, unicode.IsControl) >= 0
	title = strings.TrimSpace(title)
	if title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 4096 || hasControl {
		return "", apperror.New(apperror.CodeInvalidArgument, "Conversation title must contain 1–4096 characters without control characters")
	}
	return title, nil
}
