package computeruse

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ParseAppID extracts a display path from a syntactically valid application ID.
// This does not authorize the app or verify that its executable still exists;
// the executor independently checks native process and file identity per call.
func ParseAppID(id string) (string, error) {
	bad := errors.New("invalid Windows application identity")
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != "winapp" || len(parts[2]) != 64 || parts[2] != strings.ToLower(parts[2]) {
		return "", bad
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return "", bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != parts[1] || !utf8.Valid(raw) {
		return "", bad
	}
	p := string(raw)
	if len(p) < 4 || len(p) > 32767 || strings.Contains(p, "/") || p != strings.ToLower(p) || strings.IndexFunc(p, unicode.IsControl) >= 0 {
		return "", bad
	}
	rest := ""
	if len(p) > 3 && p[0] >= 'a' && p[0] <= 'z' && p[1:3] == `:\` {
		rest = p[3:]
	} else if strings.HasPrefix(p, `\\`) {
		rest = p[2:]
		if len(strings.Split(rest, `\`)) < 3 {
			return "", bad
		}
	} else {
		return "", bad
	}
	for _, part := range strings.Split(rest, `\`) {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `:*?"<>|`) {
			return "", bad
		}
	}
	return p, nil
}
