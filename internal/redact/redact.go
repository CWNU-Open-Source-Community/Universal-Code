package redact

import (
	"regexp"
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
		label:       "assigned-secret",
		pattern:     regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PASSWD|PRIVATE[_-]?KEY)[A-Z0-9_]*\s*[:=]\s*)(["']?)([^\s"']{8,})(["']?)`),
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
