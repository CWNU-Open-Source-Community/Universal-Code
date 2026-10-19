package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
)

func NormalizeWorkspaceName(name string) (string, error) {
	hasControl := strings.IndexFunc(name, unicode.IsControl) >= 0
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 || !utf8.ValidString(name) || hasControl {
		return "", apperror.New(apperror.CodeInvalidArgument, "Project name must contain 1–128 UTF-8 bytes without control characters")
	}
	return name, nil
}
