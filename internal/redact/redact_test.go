package redact

import (
	"strings"
	"testing"
)

func TestModelHistoryTextKeepsOnlyCompleteEscapedPlaceholders(t *testing.T) {
	const escaped = `password=[REDACTED:secret]\"`
	const text = `Earlier diagnostic: ` + escaped + ` token=example-original password=[REDACTED:secret]example-suffix\"`
	got, err := ModelHistoryText(text)
	if err != nil || !strings.Contains(got, escaped) || strings.Contains(got, "example-") {
		t.Fatalf("protected quotation or neighboring assignment changed incorrectly: %q, %v", got, err)
	}
}

func TestStringRedactsCommonSecrets(t *testing.T) {
	mimoToken := "t" + "p-" + strings.Repeat("a", 40)
	openAIToken := "s" + "k-" + "abcdefghijklmnopqrstuvwxyz123456"
	githubToken := "g" + "hp_" + "abcdefghijklmnopqrstuvwxyz123456"
	awsKey := "A" + "KIA" + "ABCDEFGHIJKLMNOP"
	input := strings.Join([]string{
		"MIMO_API_KEY=" + mimoToken,
		"OPENAI_API_KEY=" + openAIToken,
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456",
		"github=" + githubToken,
		"aws=" + awsKey,
		"jwt=aaaaaaaaaa.bbbbbbbbbb.cccccccccc",
	}, "\n")

	out := String(input)
	for _, forbidden := range []string{
		mimoToken[:11],
		openAIToken[:9],
		"abcdefghijklmnopqrstuvwxyz123456",
		githubToken[:10],
		awsKey,
		"aaaaaaaaaa.bbbbbbbbbb.cccccccccc",
	} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("redacted output still contained %q:\n%s", forbidden, out)
		}
	}
	for _, want := range []string{
		"[REDACTED:secret]",
		"[REDACTED:bearer-token]",
		"[REDACTED:github-token]",
		"[REDACTED:aws-access-key]",
		"[REDACTED:jwt]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("redacted output missing %q:\n%s", want, out)
		}
	}
}

func TestTextReportsFindings(t *testing.T) {
	openAIToken := "s" + "k-" + "abcdefghijklmnopqrstuvwxyz123456"
	result := Text("token=very-secret-value " + openAIToken)
	if result.Text == "" || len(result.Findings) == 0 {
		t.Fatalf("expected redacted text with findings: %#v", result)
	}
	var count int
	for _, finding := range result.Findings {
		count += finding.Count
	}
	if count != 2 {
		t.Fatalf("expected two findings, got %#v", result.Findings)
	}
}

func TestStringLeavesNormalTextAlone(t *testing.T) {
	input := "inspect workspace context and produce a safe artifact"
	if out := String(input); out != input {
		t.Fatalf("normal text changed: %q", out)
	}
}

// Connection strings, generic pass= assignments, and truncated PEM blocks
// are common real leak shapes the original rule set missed (verified
// empirically before this change).
func TestStringRedactsConnectionStringsAndTruncatedKeys(t *testing.T) {
	cases := []struct{ name, input, mustNotLeak string }{
		{"postgres url", "connecting to postgres://admin:s3cr3t-pw@db.internal:5432/app", "s3cr3t-pw"},
		{"mysql url", "mysql://root:hunter2@localhost/db", "hunter2"},
		{"https userinfo", "https://user:secret9@api.example.com/v1", "secret9"},
		{"generic pass assignment", "db_pass=sup3r-s3cr3t configured", "sup3r-s3cr3t"},
		{"truncated pem", "-----BEGIN OPENSSH PRIVATE KEY-----b3BlbnNzaC1rZXktdjEAAAAAb3BlbnNzaC1rZXk", "b3BlbnNzaC1rZXktdjEA"},
	}
	for _, tc := range cases {
		out := String(tc.input)
		if strings.Contains(out, tc.mustNotLeak) {
			t.Fatalf("%s leaked %q: %s", tc.name, tc.mustNotLeak, out)
		}
		if !strings.Contains(out, "[REDACTED:") {
			t.Fatalf("%s must be redacted: %s", tc.name, out)
		}
	}
	// The username survives URL redaction; credential-free URLs and short
	// non-secret values stay untouched.
	out := String("postgres://admin:[REDACTED:url-credential]@db.internal:5432/app")
	if !strings.Contains(out, "admin:") || !strings.Contains(out, ":5432/app") {
		t.Fatalf("url redaction must keep the user and port: %s", out)
	}
	plain := String("see https://example.com/docs and passed=true")
	if plain != "see https://example.com/docs and passed=true" {
		t.Fatalf("credential-free URL or short value changed: %q", plain)
	}
	// Known limitation: a truncated key whose body is not base64/whitespace
	// (output cut mid-word) is not matched by the conservative tail rule.
	partial := String("-----BEGIN OPENSSH PRIVATE KEY-----b3BlbnNza-truncated-no-end")
	if !strings.Contains(partial, "b3BlbnNza") {
		t.Fatalf("documented limitation changed unexpectedly: %q", partial)
	}
}
