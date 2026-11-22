package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const MaxModelDataBytes = 4 * 1024 * 1024

// ModelJSONValue redacts decoded model data. Compaction data may contain a
// second JSON document inside a string; ordinary tool arguments keep it as text.
func ModelJSONValue(value any, nestedData bool) (any, error) {
	nodes := 0
	return modelJSONValue(value, 0, &nodes, nestedData)
}

// ModelHistoryText protects a string before surrounding prose or a quotation
// marker makes embedded JSON unrecognizable. Keep unchanged text byte-for-byte.
func ModelHistoryText(value string) (string, error) {
	nodes := 1
	return modelHistoryText(value, 0, &nodes)
}

func modelPlainText(value string) string {
	return textResult(value, strings.Contains(value, `[REDACTED:secret]\`)).Text
}

// A formatted transcript can contain an already protected JSON string. Its
// backslashes close the quoted value and must survive the outer redaction.
func replaceQuotedAssignments(rule rule, value string) string {
	return rule.pattern.ReplaceAllStringFunc(value, func(match string) string {
		fields := rule.pattern.FindStringSubmatch(match)
		if fields[4] != "" && strings.TrimRight(fields[3], `\`) == "[REDACTED:secret]" {
			return match
		}
		return rule.pattern.ReplaceAllString(match, rule.replacement)
	})
}

func modelHistoryText(value string, depth int, nodes *int) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return modelPlainText(value), nil
	}
	if len(value) > MaxModelDataBytes {
		if json.Valid([]byte(value)) {
			return "", errors.New("nested model JSON exceeds its byte bound")
		}
		return modelPlainText(value), nil
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var inner, trailing any
	if decoder.Decode(&inner) != nil || decoder.Decode(&trailing) != io.EOF {
		return modelPlainText(value), nil
	}
	safe, err := modelJSONValue(inner, depth+1, nodes, true)
	if err != nil {
		return "", err
	}
	before, err := json.Marshal(inner)
	if err != nil {
		return "", err
	}
	after, err := json.Marshal(safe)
	if err != nil {
		return "", err
	}
	if bytes.Equal(before, after) {
		return value, nil
	}
	return string(after), nil
}

func modelJSONValue(value any, depth int, nodes *int, nestedData bool) (any, error) {
	if depth > 64 {
		return nil, errors.New("model tool JSON exceeds depth limit")
	}
	(*nodes)++
	if *nodes > 100000 {
		return nil, errors.New("model tool JSON exceeds node limit")
	}
	switch current := value.(type) {
	case string:
		if nestedData {
			return modelHistoryText(current, depth, nodes)
		}
		return String(current), nil
	case []any:
		out := make([]any, len(current))
		for index, item := range current {
			redacted, err := modelJSONValue(item, depth+1, nodes, nestedData)
			if err != nil {
				return nil, err
			}
			out[index] = redacted
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			redacted, err := modelJSONValue(item, depth+1, nodes, nestedData)
			if err != nil {
				return nil, err
			}
			if nestedData {
				key = String(key)
				if _, exists := out[key]; exists {
					return nil, errors.New("model compaction JSON keys collide after redaction")
				}
			}
			out[key] = redacted
		}
		return out, nil
	default:
		return value, nil
	}
}
