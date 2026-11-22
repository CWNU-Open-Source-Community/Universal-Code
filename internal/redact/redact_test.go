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
