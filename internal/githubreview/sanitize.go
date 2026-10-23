package githubreview

import (
	"bytes"
	"regexp"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/outputsafe"
	"cyberagent-workbench/internal/redact"
)

var (
	remoteHTMLTag  = regexp.MustCompile(`<[^>\n]{1,1000}>`)
	remoteImage    = regexp.MustCompile(`!\[[^\]\n]{0,512}\]\([^\)\n]*\)`)
	remoteLink     = regexp.MustCompile(`\[([^\]\n]{0,512})\]\([^\)\n]*\)`)
	remoteURL      = regexp.MustCompile(`(?i)\b(?:https?|ftp)://[^\s<>{}\[\]]+`)
	actionsCommand = regexp.MustCompile(`(?m)^\s*::[A-Za-z0-9_-]+(?:::[^\n]*)?$`)
)

// SanitizeRemoteText converts an already bounded GitHub payload into inert,
// secret-redacted evidence. Links, embedded images, HTML, terminal controls,
// bidi controls, and GitHub Actions command records are not preserved as
// executable/renderable syntax.
func SanitizeRemoteText(value string, limit int) TextEvidence {
	if limit <= 0 || limit > MaxLogExcerptBytes {
		limit = MaxTextBytes
	}
	originalBytes := len([]byte(value))
	clean, inputRedacted := outputsafe.SanitizeWithRedaction([]byte(value))
	clean = remoteImage.ReplaceAllString(clean, "[remote image omitted]")
	clean = remoteLink.ReplaceAllString(clean, "$1 [remote link omitted]")
	clean = remoteHTMLTag.ReplaceAllString(clean, "[remote HTML omitted]")
	clean = remoteURL.ReplaceAllString(clean, "[remote link omitted]")
	clean = actionsCommand.ReplaceAllString(clean, "[remote command syntax omitted]")
	clean = strings.ReplaceAll(clean, "\r", "")
	clean = strings.TrimSpace(clean)
	clean, truncated := truncateUTF8Bytes(clean, limit)
	redacted := redact.String(clean)
	lateRedacted := redacted != clean
	if lateRedacted {
		clean = redacted
	}
	// A redaction marker can grow the text. Re-apply the hard byte boundary.
	clean, secondTruncation := truncateUTF8Bytes(clean, limit)
	truncated = truncated || secondTruncation || originalBytes > len([]byte(clean))
	sum := Fingerprint("remote-text", clean)
	return TextEvidence{Text: clean, SHA256: sum, OriginalBytes: originalBytes,
		StoredBytes: len([]byte(clean)), Truncated: truncated,
		Redacted: inputRedacted || lateRedacted, Untrusted: true}
}

func EmptyTextEvidence() TextEvidence {
	return SanitizeRemoteText("", MaxTextBytes)
}

func truncateUTF8Bytes(value string, limit int) (string, bool) {
	if len([]byte(value)) <= limit {
		return value, false
	}
	data := []byte(value)
	data = data[:limit]
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(bytes.TrimSpace(data)), true
}

func sanitizeIdentity(value string, max int) string {
	value = outputsafe.Sanitize([]byte(value))
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > max {
		value = string(runes[:max])
	}
	return value
}
