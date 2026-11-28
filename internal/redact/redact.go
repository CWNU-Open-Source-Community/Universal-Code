package redact

import (
	"regexp"
	"strings"
)

type Finding struct {
	Label string
	Count int
}

type Result struct {
	Text     string
	Findings []Finding
}

type rule struct {
	label       string
	pattern     *regexp.Regexp
	replacement string
}

var defaultRules = []rule{
	{
		label:       "private-key",
		pattern:     regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
		replacement: "[REDACTED:private-key]",
	},
	{
		// Bounded output often cuts a PEM block before its END marker; the
		// complete-block rule above then misses it and the whole key body
		// leaks. Match a header whose remainder to the end of the text is
		// only base64/whitespace.
		label:       "private-key-truncated",
		pattern:     regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----[A-Za-z0-9+/=\s]+$`),
		replacement: "[REDACTED:private-key]",
	},
	{
		label:       "mimo-token",
		pattern:     regexp.MustCompile(`\b` + "t" + `p-[A-Za-z0-9]{30,}\b`),
		replacement: "[REDACTED:mimo-token]",
	},
	{
		label:       "openai-anthropic-key",
		pattern:     regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
		replacement: "[REDACTED:api-key]",
	},
	{
		label:       "github-token",
		pattern:     regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`),
		replacement: "[REDACTED:github-token]",
	},
	{
		label:       "aws-access-key",
		pattern:     regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
		replacement: "[REDACTED:aws-access-key]",
	},
	{
		label:       "jwt",
		pattern:     regexp.MustCompile(`\b[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
		replacement: "[REDACTED:jwt]",
	},
	{
		label:       "bearer-token",
		pattern:     regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/-]{20,}`),
		replacement: `$1[REDACTED:bearer-token]`,
	},
	{
		// Credentials embedded in connection strings and URLs
		// (postgres://admin:pw@host, https://user:secret9@host) are a common
		// real leak shape no other rule covers; the username stays visible
		// and only the password is removed.
		label:       "url-credential",
		pattern:     regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*://[^\s/?#:@]*:)([^\s/?#@]+)@`),
		replacement: `$1[REDACTED:url-credential]@`,
	},
	{
		label:       "assigned-secret",
		pattern:     regexp.MustCompile(`(?i)\b((?:[A-Z0-9_]*(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PASSWD|PRIVATE[_-]?KEY)[A-Z0-9_]*|(?:[A-Z0-9]+[_-])*PASS)\s*[:=]\s*)(["']?)([^\s"']{8,})(["']?)`),
		replacement: `$1$2[REDACTED:secret]$4`,
	},
}

func String(value string) string {
	return Text(value).Text
}

func Text(value string) Result {
	return textResult(value, false)
}

func textResult(value string, preserveQuotedPlaceholders bool) Result {
	out := value
	counts := map[string]int{}
	order := make([]string, 0, len(defaultRules))
	for _, rule := range defaultRules {
		// These expressions start at a word boundary. Check their required
		// separators first so ordinary multi-megabyte diffs skip the scans.
		if rule.label == "url-credential" && !strings.Contains(out, "://") ||
			rule.label == "assigned-secret" && !strings.ContainsAny(out, ":=") {
			continue
		}
		matches := rule.pattern.FindAllString(out, -1)
		if len(matches) == 0 {
			continue
		}
		if counts[rule.label] == 0 {
			order = append(order, rule.label)
		}
		counts[rule.label] += len(matches)
		if preserveQuotedPlaceholders && rule.label == "assigned-secret" {
			out = replaceQuotedAssignments(rule, out)
		} else {
			out = rule.pattern.ReplaceAllString(out, rule.replacement)
		}
	}
	findings := make([]Finding, 0, len(order))
	for _, label := range order {
		findings = append(findings, Finding{Label: label, Count: counts[label]})
	}
	return Result{Text: out, Findings: findings}
}
