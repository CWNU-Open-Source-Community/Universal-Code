package application

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/redact"
)

const MaxThreadActivityMCPContents = 16
const MaxThreadActivityMCPContentBytes = 128 * 1024
const MaxThreadActivityMCPTextBytes = 32 * 1024

// These are inert projections of already-saved SDK content. URI is provenance,
// never an instruction to fetch. Media contains only validated embedded bytes.
type ThreadActivityMCPContent struct {
	Kind       string
	Text       string
	URI        string
	Name       string
	MIMEType   string
	DataBase64 string
	ByteSize   int
	Width      int
	Height     int
	Role       string
}

func projectThreadActivityMCPContents(stdout string) ([]ThreadActivityMCPContent, int, bool) {
	if len(stdout) > MaxThreadActivityMCPContentBytes {
		return nil, 1, true
	}
	value, valid := decodeThreadActivityJSON([]byte(stdout))
	root, ok := value.(map[string]any)
	if !valid || !ok {
		return nil, 0, false
	}
	contents := []ThreadActivityMCPContent{}
	omitted, totalBytes, truncated := 0, 0, false
	add := func(raw map[string]any, role string, resource bool) {
		if len(contents) >= MaxThreadActivityMCPContents {
			omitted++
			return
		}
		item, cut, valid := projectThreadActivityMCPContent(raw, role, resource)
		if !valid {
			omitted++
			return
		}
		// Conservative JSON/key overhead keeps the entire content projection
		// within the existing saved-result bound, including escaped text.
		encoded, _ := json.Marshal(item)
		cost := len(encoded) + 256
		if cost > MaxThreadActivityMCPContentBytes-totalBytes {
			omitted++
			truncated = true
			return
		}
		totalBytes += cost
		truncated = truncated || cut
		contents = append(contents, item)
	}
	for _, key := range []string{"content", "contents"} {
		if list, ok := root[key].([]any); ok {
			for _, raw := range list {
				if item, ok := raw.(map[string]any); ok {
					add(item, "", key == "contents")
				} else {
					omitted++
				}
			}
		}
	}
	if list, ok := root["messages"].([]any); ok {
		for _, raw := range list {
			message, ok := raw.(map[string]any)
			if !ok {
				omitted++
				continue
			}
			role := mcpContentString(message, "role")
			if role != "user" && role != "assistant" {
				omitted++
				continue
			}
			item, ok := message["content"].(map[string]any)
			if !ok {
				omitted++
				continue
			}
			add(item, role, false)
		}
	}
	return contents, omitted, truncated
}

func mcpContentString(value map[string]any, name string) string {
	text, _ := value[name].(string)
	return text
}

func projectThreadActivityMCPContent(raw map[string]any, role string, resource bool) (ThreadActivityMCPContent, bool, bool) {
	kind := mcpContentString(raw, "type")
	if kind == "resource" {
		if resource {
			return ThreadActivityMCPContent{}, false, false
		}
		inner, ok := raw["resource"].(map[string]any)
		if !ok {
			return ThreadActivityMCPContent{}, false, false
		}
		return projectThreadActivityMCPContent(inner, role, true)
	}
	item := ThreadActivityMCPContent{Role: role, URI: safeMCPContentURI(mcpContentString(raw, "uri")),
		Name: safeMCPContentMetadata(mcpContentString(raw, "name")), MIMEType: safeMCPContentMetadata(mcpContentString(raw, "mimeType"))}
	if len(item.MIMEType) > 256 {
		item.MIMEType = ""
	}
	if kind == "resource_link" {
		if item.URI == "" {
			return item, false, false
		}
		item.Kind = "resource_link"
		return item, false, true
	}
	if text, ok := raw["text"].(string); ok && (kind == "text" || resource) {
		var cut bool
		item.Kind, item.Text = "text", cleanMCPContentText(text)
		if resource {
			item.Kind = "resource_text"
		}
		if len(item.Text) > MaxThreadActivityMCPTextBytes {
			item.Text = truncateMCPContentText(item.Text, MaxThreadActivityMCPTextBytes-3) + "…"
			cut = true
		}
		return item, cut, item.Text != ""
	}
	encoded := mcpContentString(raw, "data")
	if resource {
		encoded = mcpContentString(raw, "blob")
	}
	if encoded == "" || len(encoded) > MaxThreadActivityMCPContentBytes {
		return item, false, false
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(data) != encoded {
		return item, false, false
	}
	if kind == "image" || resource && strings.HasPrefix(item.MIMEType, "image/") {
		image, err := imageattachment.Validate(data, item.MIMEType, "")
		if err != nil {
			return item, false, false
		}
		item.Kind, item.Width, item.Height = "image", image.Width, image.Height
	} else if kind == "audio" || resource && strings.HasPrefix(item.MIMEType, "audio/") {
		if !validMCPAudio(data, item.MIMEType) {
			return item, false, false
		}
		item.Kind = "audio"
	} else {
		return item, false, false
	}
	item.DataBase64, item.ByteSize = encoded, len(data)
	return item, false, true
}

func cleanMCPContentText(value string) string {
	if json.Valid([]byte(value)) {
		if safe, err := redact.SanitizeSensitiveJSON([]byte(value)); err == nil {
			value = string(safe)
		}
	}
	value = redact.String(strings.ReplaceAll(value, "\r\n", "\n"))
	return strings.Map(func(current rune) rune {
		if (unicode.IsControl(current) && current != '\n' && current != '\t') || unicode.In(current, unicode.Cf) {
			return '�'
		}
		return current
	}, value)
}

func safeMCPContentMetadata(value string) string {
	value = strings.NewReplacer("\n", " ", "\t", " ").Replace(cleanMCPContentText(value))
	return safeThreadActivityFactValue(value)
}

func truncateMCPContentText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func safeMCPContentURI(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 4096 || !utf8.ValidString(value) || strings.IndexFunc(value, func(current rune) bool { return unicode.IsControl(current) || unicode.In(current, unicode.Cf) }) >= 0 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.Fragment, parsed.RawFragment = "", ""
	if redact.String(parsed.Host) != parsed.Host {
		return ""
	}
	parsed.Path, parsed.RawPath = redact.String(parsed.Path), ""
	parsed.Opaque = redact.String(parsed.Opaque)
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return ""
	}
	for key, values := range query {
		if sensitiveThreadActivityURLQueryKey(key) {
			query.Del(key)
			continue
		}
		for _, current := range values {
			if redact.String(current) != current {
				query.Del(key)
				break
			}
		}
	}
	parsed.RawQuery, parsed.ForceQuery = query.Encode(), false
	result := parsed.String()
	if len(result) > 4096 {
		return ""
	}
	return result
}

func validMCPAudio(data []byte, mime string) bool {
	switch mime {
	case "audio/wav", "audio/x-wav":
		return len(data) >= 44 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE" && uint64(binary.LittleEndian.Uint32(data[4:8]))+8 == uint64(len(data))
	case "audio/mpeg":
		return len(data) >= 10 && (string(data[:3]) == "ID3" || data[0] == 0xff && data[1]&0xe0 == 0xe0)
	case "audio/ogg":
		return len(data) >= 27 && string(data[:4]) == "OggS" && data[4] == 0
	default:
		return false
	}
}

func validThreadActivityMCPContents(contents []ThreadActivityMCPContent) error {
	if len(contents) > MaxThreadActivityMCPContents {
		return errors.New("MCP content projection exceeds its item limit")
	}
	total := 0
	for _, item := range contents {
		if (item.Role != "" && item.Role != "user" && item.Role != "assistant") || len(item.MIMEType) > 256 || len(item.URI) > 4096 ||
			!validThreadActivityFactText(item.MIMEType, 256, true) ||
			(item.URI != "" && safeMCPContentURI(item.URI) != item.URI) || !validThreadActivityFactText(item.Name, MaxThreadActivityFactValueRunes, true) {
			return errors.New("MCP content metadata is invalid")
		}
		switch item.Kind {
		case "text", "resource_text":
			if item.Text == "" || len(item.Text) > MaxThreadActivityMCPTextBytes || cleanMCPContentText(item.Text) != item.Text ||
				item.DataBase64 != "" || item.ByteSize != 0 || item.Width != 0 || item.Height != 0 {
				return errors.New("MCP text projection is invalid")
			}
		case "resource_link":
			if item.URI == "" || item.Text != "" || item.DataBase64 != "" || item.ByteSize != 0 || item.Width != 0 || item.Height != 0 {
				return errors.New("MCP link projection is invalid")
			}
		case "image", "audio":
			data, err := base64.StdEncoding.Strict().DecodeString(item.DataBase64)
			if err != nil || len(data) != item.ByteSize || len(data) == 0 || len(item.DataBase64) > MaxThreadActivityMCPContentBytes ||
				base64.StdEncoding.EncodeToString(data) != item.DataBase64 || item.Text != "" {
				return errors.New("MCP embedded media projection is invalid")
			}
			if item.Kind == "image" {
				image, err := imageattachment.Validate(data, item.MIMEType, "")
				if err != nil || image.Width != item.Width || image.Height != item.Height {
					return errors.New("MCP image projection is invalid")
				}
			} else if !validMCPAudio(data, item.MIMEType) || item.Width != 0 || item.Height != 0 {
				return errors.New("MCP audio projection is invalid")
			}
		default:
			return errors.New("MCP content projection kind is invalid")
		}
		encoded, _ := json.Marshal(item)
		total += len(encoded) + 256
		if total > MaxThreadActivityMCPContentBytes {
			return errors.New("MCP content projection exceeds its aggregate limit")
		}
	}
	return nil
}
