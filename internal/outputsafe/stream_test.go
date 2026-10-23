package outputsafe

import (
	"strings"
	"testing"
)

func TestStreamCarriesEscapeAndUTF8StateAcrossChunks(t *testing.T) {
	stream := &Stream{}
	parts := [][]byte{
		[]byte("safe\x1b[3"),
		append([]byte("1mred\x1b]0;title"), '\x1b'),
		append([]byte("\\"), []byte("你")[:2]...),
		append([]byte{[]byte("你")[2]}, []byte("好\n")...),
	}
	var result strings.Builder
	for _, part := range parts {
		result.WriteString(stream.Feed(part))
	}
	result.WriteString(stream.Flush())
	if got := result.String(); got != "safered你好\n" {
		t.Fatalf("sanitized stream=%q", got)
	}
}

func TestSanitizeRemovesC1UnicodeControlsAndRedactsSecrets(t *testing.T) {
	got := Sanitize([]byte("before\u009b31mafter\u009dtitle\u009c token=secret-value-1234567890"))
	if strings.Contains(got, "31m") || strings.Contains(got, "title") ||
		strings.Contains(got, "secret-value") {
		t.Fatalf("unsafe output escaped sanitizer: %q", got)
	}
}

func TestSanitizeWithRedactionSeparatesRedactionFromControlStripping(t *testing.T) {
	secret := []byte("before\u009b31mafter token=secret-value-1234567890")
	got, redacted := SanitizeWithRedaction(secret)
	if !redacted || got != Sanitize(secret) {
		t.Fatalf("redaction was not reported apart from stripping: %q %v", got, redacted)
	}
	// Removing control characters shortens the text but is not a secret redaction.
	plain := []byte("before\u009b31mafter\u009dtitle")
	got, redacted = SanitizeWithRedaction(plain)
	if redacted || got != Sanitize(plain) {
		t.Fatalf("control stripping must not report redaction: %q %v", got, redacted)
	}
}

func TestRedactingStreamCarriesSecretAndPrivateKeyStateAcrossChunks(t *testing.T) {
	stream := &RedactingStream{}
	parts := []string{
		"visible\n-----BE", "GIN TEST PRIVATE KEY-----\n",
		"private-material\n-----END TEST PRI", "VATE KEY-----\n",
		"token=secret-value-", "1234567890\nafter\n",
	}
	var result strings.Builder
	for _, part := range parts {
		result.WriteString(stream.Feed([]byte(part)))
	}
	result.WriteString(stream.Flush())
	got := result.String()
	if strings.Contains(got, "private-material") || strings.Contains(got, "secret-value") ||
		!strings.Contains(got, "[REDACTED:private-key]") ||
		!strings.Contains(got, "[REDACTED:secret]") ||
		!strings.Contains(got, "visible") || !strings.Contains(got, "after") {
		t.Fatalf("streaming redaction failed: %q", got)
	}
}

func TestRedactingStreamReplacesOversizedUnterminatedLine(t *testing.T) {
	stream := &RedactingStream{}
	value := strings.Repeat("x", maxRedactingStreamLineBytes+128)
	got := stream.Feed([]byte(value)) + stream.Feed([]byte("\nvisible\n")) + stream.Flush()
	if strings.Contains(got, strings.Repeat("x", 128)) ||
		got != "[REDACTED:oversized-line]\nvisible\n" {
		t.Fatalf("oversized line was not replaced: %q", got)
	}
}

func TestRedactingStreamRemovesPresentationControlWhitespace(t *testing.T) {
	stream := &RedactingStream{}
	got := stream.Feed([]byte("first\rsecond\tcolumn\n")) + stream.Flush()
	if got != "firstsecond    column\n" {
		t.Fatalf("presentation controls were retained: %q", got)
	}
}

func TestRedactingStreamPreservesMultilineSecretRules(t *testing.T) {
	for _, value := range []string{
		"Bearer\n" + strings.Repeat("a", 25) + "\n",
		"API_TOKEN\n:\n" + strings.Repeat("b", 25) + "\n",
		"Bearer\r" + strings.Repeat("c", 25) + "\n",
	} {
		stream := &RedactingStream{}
		var got strings.Builder
		for _, b := range []byte(value) {
			got.WriteString(stream.Feed([]byte{b}))
		}
		got.WriteString(stream.Flush())
		if !strings.Contains(got.String(), "[REDACTED:") ||
			strings.Contains(got.String(), strings.Repeat("a", 25)) ||
			strings.Contains(got.String(), strings.Repeat("b", 25)) ||
			strings.Contains(got.String(), strings.Repeat("c", 25)) {
			t.Fatalf("multiline secret was not redacted: %q", got.String())
		}
	}
}

func TestRedactingStreamDiscardPendingKeepsPrivateKeyState(t *testing.T) {
	stream := &RedactingStream{}
	if got := stream.Feed([]byte("old output -----BE")); got != "" {
		t.Fatalf("published pending output: %q", got)
	}
	stream.DiscardPending()
	got := stream.Feed([]byte("GIN TEST PRIVATE KEY-----\nprivate-material\n"))
	got += stream.Feed([]byte("-----END TEST PRIVATE KEY-----\nvisible\n"))
	if strings.Contains(got, "old output") || strings.Contains(got, "private-material") ||
		!strings.Contains(got, "visible\n") {
		t.Fatalf("fence lost private key state or retained old output: %q", got)
	}
}

func TestRedactingStreamDiscardPendingKeepsOversizeBound(t *testing.T) {
	stream := &RedactingStream{}
	stream.Feed([]byte("old output"))
	stream.DiscardPending()
	got := stream.Feed([]byte(strings.Repeat("a", maxRedactingStreamLineBytes+1)))
	got += stream.Feed([]byte("rest\nvisible\n")) + stream.Flush()
	if got != "visible\n" {
		t.Fatalf("fenced oversized line was published: %q", got)
	}
}

func TestRedactingStreamRecognizesAssignedPrivateKeyBeforeOtherRules(t *testing.T) {
	for _, marker := range []string{"-----BEGIN RSA PRIVATE KEY-----", "-----BE\rGIN\tRSA PRIVATE KEY-----"} {
		stream := &RedactingStream{}
		got := stream.Feed([]byte("Bearer\r" + strings.Repeat("x", 24) + " PRIVATE_KEY=" + marker + "\n"))
		got += stream.Feed([]byte("private-material-12345\n-----END RSA PRIVATE KEY-----\nvisible\n"))
		got += stream.Flush()
		if strings.Contains(got, "private-material") || strings.Contains(got, strings.Repeat("x", 24)) ||
			!strings.Contains(got, "[REDACTED:private-key]") || !strings.HasSuffix(got, "visible\n") {
			t.Fatalf("assigned private key was not redacted: %q", got)
		}
	}
}

func TestRedactingStreamKeepsOversizedSecretContinuation(t *testing.T) {
	for _, prefix := range []string{"TOKEN=", "Bearer ", "API_TOKEN"} {
		t.Run(prefix, func(t *testing.T) {
			stream := &RedactingStream{}
			got := stream.Feed([]byte(prefix + strings.Repeat(" ", maxRedactingStreamLineBytes)))
			// Continue across more reads than the retained marker tail can hold.
			for range 3 {
				got += stream.Feed([]byte(strings.Repeat(" ", 128)))
			}
			if prefix == "API_TOKEN" {
				got += stream.Feed([]byte("\n:\n"))
			}
			got += stream.Feed([]byte("\nsecret-material-12345-abcd\nvisible\n"))
			got += stream.Flush()
			if got != "[REDACTED:oversized-line]\nvisible\n" {
				t.Fatalf("oversized secret continuation escaped redaction: %q", got)
			}
		})
	}
}
