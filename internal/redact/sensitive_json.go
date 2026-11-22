package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
)

const SensitiveJSONFieldPlaceholder = "[REDACTED:sensitive-field]"

var sensitiveJSONMetadataSuffixes = map[string]bool{
	"reference": true,
	"ref":       true,
	"id":        true,
	"name":      true,
	"count":     true,
	"limit":     true,
	"budget":    true,
	"usage":     true,
	"type":      true,
}

// SensitiveJSONFieldName identifies fields whose values are credential-like.
// Explicit reference and accounting metadata remain visible because they name
// an already-reviewed credential; they do not carry the credential itself.
func SensitiveJSONFieldName(value string) bool {
	words := sensitiveJSONFieldWords(value)
	if len(words) == 0 {
		return false
	}
	if len(words) > 1 && sensitiveJSONMetadataSuffixes[words[len(words)-1]] {
		return false
	}
	compact := strings.Join(words, "")
	if compact == "" {
		return false
	}
	for _, word := range words {
		switch word {
		case "auth", "authentication", "authorization", "bearer", "credential",
			"credentials", "cookie", "password", "passwd", "pass", "secret", "token":
			return true
		}
	}
	return strings.Contains(compact, "password") ||
		strings.Contains(compact, "passwd") ||
		strings.Contains(compact, "apikey") ||
		strings.Contains(compact, "privatekey") ||
		strings.Contains(compact, "authorization") ||
		strings.Contains(compact, "authentication") ||
		strings.Contains(compact, "clientsecret") ||
		strings.Contains(compact, "accesskey") ||
		strings.Contains(compact, "signingkey") ||
		strings.Contains(compact, "sessionkey") ||
		strings.Contains(compact, "encryptionkey") ||
		strings.Contains(compact, "decryptionkey") ||
		strings.HasPrefix(compact, "setcookie") || strings.HasSuffix(compact, "cookie")
}

func sensitiveJSONFieldWords(value string) []string {
	value = strings.TrimSpace(value)
	words := make([]string, 0, 4)
	current := make([]rune, 0, len(value))
	flush := func() {
		if len(current) == 0 {
			return
		}
		words = append(words, strings.ToLower(string(current)))
		current = current[:0]
	}
	var previous rune
	for _, currentRune := range value {
		if !unicode.IsLetter(currentRune) && !unicode.IsDigit(currentRune) {
			flush()
			previous = 0
			continue
		}
		if len(current) > 0 && unicode.IsUpper(currentRune) &&
			(unicode.IsLower(previous) || unicode.IsDigit(previous)) {
			flush()
		}
		current = append(current, currentRune)
		previous = currentRune
	}
	flush()
	return words
}

// SanitizeSensitiveJSON replaces values labelled by sensitive field names and
// applies the shared value-pattern redactor before callers truncate or persist
// remote JSON. Secret-shaped object keys are omitted entirely.
func SanitizeSensitiveJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("sensitive JSON contains trailing data")
	}
	canonical, err := json.Marshal(sanitizeSensitiveJSONValue(value))
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func sanitizeSensitiveJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			if String(key) != key {
				continue
			}
			if SensitiveJSONFieldName(key) {
				result[key] = SensitiveJSONFieldPlaceholder
				continue
			}
			result[key] = sanitizeSensitiveJSONValue(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = sanitizeSensitiveJSONValue(child)
		}
		return result
	case string:
		return String(typed)
	default:
		return value
	}
}
