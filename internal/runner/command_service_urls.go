package runner

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"cyberagent-workbench/internal/redact"
)

const MaxCommandServiceURLs = 8
const CommandServiceURLPattern = `(?i)https?://[^\s<>"'` + "`" + `]+`

var commandServiceURLPattern = regexp.MustCompile(CommandServiceURLPattern)

type CommandServiceURLCandidate struct {
	URL      string `json:"url"`
	Source   string `json:"source"`
	Verified bool   `json:"verified"`
}

// CanonicalCommandServiceURL accepts only public, credential-free loopback
// candidates. A candidate is output text, never readiness or network authority.
func CanonicalCommandServiceURL(candidate string) (string, bool) {
	if len(candidate) > 2048 || strings.ContainsAny(candidate, "\\\r\n\t") {
		return "", false
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" || redact.String(candidate) != candidate {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return "", false
	}
	port := parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", false
		}
	}
	if host == "localhost" {
		parsed.Host = "127.0.0.1"
		if port != "" {
			parsed.Host += ":" + port
		}
	}
	return parsed.String(), true
}

// Callers pass only output already sanitized for their audience. No connection
// is made, and absence from a bounded output page says nothing about readiness.
func CommandServiceURLCandidates(frames []CommandRuntimeFrame) []CommandServiceURLCandidate {
	values := make([]CommandServiceURLCandidate, 0, MaxCommandServiceURLs)
	seen := make(map[string]bool)
	for _, frame := range frames {
		for _, match := range commandServiceURLPattern.FindAllString(frame.Text, MaxCommandServiceURLs*4) {
			canonical, ok := CanonicalCommandServiceURL(strings.TrimRight(match, ".,;)"))
			if !ok || seen[canonical] {
				continue
			}
			seen[canonical] = true
			values = append(values, CommandServiceURLCandidate{URL: canonical, Source: "command_output", Verified: false})
			if len(values) == MaxCommandServiceURLs {
				return values
			}
		}
	}
	return values
}
