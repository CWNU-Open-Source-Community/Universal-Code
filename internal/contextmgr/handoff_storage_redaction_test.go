package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"cyberagent-workbench/internal/redact"
)

// storageRedactionMessage mirrors session.ProjectContextMessage: durable
// provenance carries the digest of the redacted text and the same redacted
// content, so a masked credential already sits inside the record text before
// compaction excerpts it.
func storageRedactionMessage(id int64, role, sourceKind, content string, authorized bool) Message {
	projected := redact.String(content)
	return Message{Role: role, Content: projected, SourceMessageID: id, SourceKind: sourceKind,
		SourceRef: fmt.Sprintf("source/%d", id), ContentSHA256: handoffContentSHA256(projected),
		InstructionAuthorized: authorized}
}

// compactStorageRedactionInput places one masked credential so that the bounded
// excerpt boundary can fall immediately after it.
func compactStorageRedactionInput(filler int) []Message {
	objective := "CONSTRAINT: report the README credential field exactly once. " +
		strings.Repeat("d", filler) + "README.md?token=supersecretvalue123 " +
		strings.Repeat("Remaining bounded diagnostic detail. ", 25)
	messages := []Message{storageRedactionMessage(1, "user", "operator_message", objective, true)}
	for id := int64(2); id <= 9; id++ {
		messages = append(messages, storageRedactionMessage(id, "assistant", "model_response",
			"Progress: validation is still unfinished.", false))
	}
	return messages
}

// Compact excerpts record content before PrepareSummaryForStorage runs, and that
// pass is exactly what SQLiteStore.saveContextSummaryTx calls on every write. It
// applied the secret regexes to already-serialized JSON, where the assigned-value
// class consumes the JSON escape of the excerpt marker and rewrites the record
// text while content_sha256 still bound the old bytes, so validateHandoffEnvelope
// rejected the handoff the compactor had just produced. session.compactAfterTurn
// returns that error before it can mark messages compacted, so one placement made
// every later turn of that session fail.
func TestCompactBoundedExcerptSurvivesStorageRedaction(t *testing.T) {
	for filler := 360; filler <= 420; filler++ {
		result, err := NewManager(nil, DefaultConfig()).Compact(context.Background(),
			"task-storage-redaction", "ws-storage", compactStorageRedactionInput(filler))
		if err != nil {
			t.Fatalf("filler %d: compaction failed: %v", filler, err)
		}
		summary := result.Summary
		if !storedHandoffEnvelope(summary.Content) {
			t.Fatalf("filler %d: compact produced content that is not a handoff envelope", filler)
		}
		restored, err := PrepareSummaryForStorage(summary)
		if err != nil {
			t.Fatalf("filler %d: the storage write pass rejected the handoff it was given: %v",
				filler, err)
		}
		if restored.Content != summary.Content {
			t.Fatalf("filler %d: storage preparation rewrote serialized handoff JSON", filler)
		}
		if err := ValidateStoredSummary(summary); err != nil {
			t.Fatalf("filler %d: the storage read pass rejected the handoff: %v", filler, err)
		}
	}
}

// Skipping the second pass must leave preparation idempotent: saving an already
// stored envelope again cannot drift its content or its summary digest.
func TestPrepareSummaryForStorageIsIdempotentForStoredHandoff(t *testing.T) {
	result, err := NewManager(nil, DefaultConfig()).Compact(context.Background(),
		"task-storage-redaction", "ws-storage", compactStorageRedactionInput(385))
	if err != nil {
		t.Fatal(err)
	}
	first, err := PrepareSummaryForStorage(result.Summary)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareSummaryForStorage(first)
	if err != nil {
		t.Fatalf("re-preparing a stored handoff failed: %v", err)
	}
	if second.Content != first.Content || second.ContentSHA256 != first.ContentSHA256 {
		t.Fatalf("re-preparing a stored handoff drifted its content or digest")
	}
}

// The masking pass still runs on the text that record digests are bound to:
// unredacted content entering before an envelope exists is masked, not stored.
func TestPrepareSummaryForStorageMasksPlainContentBeforeDigestBinding(t *testing.T) {
	summary, err := PrepareSummaryForStorage(Summary{TaskID: "task-plain", WorkspaceID: "ws",
		Content:               "README.md?token=supersecretvalue123 and more bounded detail.",
		SourceMessageCount:    3,
		PreservedMessageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(summary.Content, "supersecretvalue123") {
		t.Fatalf("plain handoff content was stored unredacted: %q", summary.Content)
	}
	var envelope handoffMemoryEnvelope
	if err := json.Unmarshal([]byte(summary.Content), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, record := range envelope.Records {
		if strings.Contains(record.Content, "supersecretvalue123") ||
			record.ContentSHA256 != handoffContentSHA256(record.Content) {
			t.Fatalf("handoff record kept a secret or a stale digest: %#v", record)
		}
	}
}
