package session

import (
	"strings"
	"testing"
)

func TestPrepareMessageForStorageSealsRedactedContentAndAuthority(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 32)
	message, err := PrepareMessageForStorage(Message{
		SessionID: "session-provenance", Role: "user", Content: "TOKEN=" + secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if message.Provenance.Version != ContextProvenanceVersion ||
		message.Provenance.SourceKind != SourceOperatorMessage ||
		!message.Provenance.InstructionAuthorized ||
		message.Provenance.ContentSHA256 != ContentSHA256(message.Content) ||
		strings.Contains(message.Content, secret) {
		t.Fatalf("operator message was not normalized and sealed: %#v", message)
	}
}

func TestCompactedOperatorProtectsNestedTextAndRetainsItsSource(t *testing.T) {
	const original = `{"description":"pa\u0073sword=example-value"}`
	row, err := PrepareMessageForStorage(Message{SessionID: "nested", Role: "user", Content: original})
	if err != nil {
		t.Fatal(err)
	}
	row.ID, row.Compacted = 4, true
	got, verified, err := ProjectCompactedOperator(row)
	if err != nil || !verified || got.SourceMessageID != 4 || got.ContentSHA256 != ContentSHA256(original) ||
		!strings.Contains(got.Content, "[REDACTED") || strings.Contains(got.Content, "example-value") {
		t.Fatalf("compacted original or protected provider text lost its binding: verified=%t err=%v", verified, err)
	}
}

func TestPrepareMessageForStorageRejectsForgedEvidenceAuthorityAndDigest(t *testing.T) {
	message := NewEvidenceMessage("session-provenance", SourceWorkspaceFile, "README.md", "facts")
	message.Provenance.InstructionAuthorized = true
	if _, err := PrepareMessageForStorage(message); err == nil {
		t.Fatal("workspace evidence was allowed to grant instruction authority")
	}
	message = NewEvidenceMessage("session-provenance", SourceWorkspaceFile, "README.md", "facts")
	message.Provenance.ContentSHA256 = strings.Repeat("0", 64)
	if _, err := PrepareMessageForStorage(message); err == nil {
		t.Fatal("forged content digest was accepted")
	}
	if _, err := PrepareMessageForStorage(Message{
		SessionID: "session-provenance", Role: "document", Content: "facts",
	}); err == nil {
		t.Fatal("unknown role was silently promoted to an operator message")
	}
	message = NewEvidenceMessage("session-provenance", SourceWorkspaceFile, "README.md\nforged", "facts")
	if _, err := PrepareMessageForStorage(message); err == nil {
		t.Fatal("control characters in a source reference were accepted")
	}
}
