package contextmgr

import (
	"strings"
	"testing"
	"time"
)

func TestMemoryCandidateCopiesRedactedConclusionAndBindsReview(t *testing.T) {
	now := time.Now().UTC()
	key := "s" + "k-" + strings.Repeat("c", 28)
	source := MemoryCandidateSource{RunID: "run-candidate", NoteID: "note-candidate", NoteVersion: 1, Category: "decision", References: []string{"README.md"}, EvidenceIDs: []string{"receipt-one"}, UpdatedAt: now,
		CompletionKind: "thread_reply", CompletionRef: "run_event:reply-one", CompletionFingerprint: strings.Repeat("a", 64)}
	value, err := NewMemoryCandidate("workspace-candidate", "Decision "+key, "Use focused tests. "+key, source, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(value.Title+value.Content, key) || !value.Redacted || value.Status != MemoryCandidatePending || !strings.Contains(value.ConfidenceReason, "reply") {
		t.Fatalf("unsafe candidate: %#v", value)
	}
	changed := value
	changed.Content = "Different reviewed conclusion"
	if changed.Validate() == nil {
		t.Fatal("candidate content changed without invalidating its fingerprint")
	}
	changed = value
	changed.Source.NoteVersion++
	if changed.Validate() == nil {
		t.Fatal("candidate source version changed without invalidating its fingerprint")
	}
	confirmed := value.Resolve(MemoryCandidateConfirmed, "desktop_operator", "memory-candidate", now.Add(time.Second))
	if err := confirmed.Validate(); err != nil {
		t.Fatal(err)
	}
	if confirmed.Title != "" || confirmed.Content != "" || len(confirmed.Source.References) != 0 || len(confirmed.Source.EvidenceIDs) != 0 {
		t.Fatalf("resolution retained a second body: %#v", confirmed)
	}
	if _, err := confirmed.Memory("desktop_operator", now); err == nil {
		t.Fatal("resolved candidate could reconstruct a deleted memory")
	}
}

func TestMemoryCandidateRejectsSensitiveSourcesAndUnboundedContent(t *testing.T) {
	now := time.Now().UTC()
	source := MemoryCandidateSource{RunID: "run-candidate", NoteID: "note-candidate", NoteVersion: 1, Category: "summary", References: []string{".env"}, UpdatedAt: now, CompletionKind: "completed_run", CompletionRef: "run:run-candidate", CompletionFingerprint: strings.Repeat("b", 64)}
	if _, err := NewMemoryCandidate("workspace-candidate", "Summary", "Conclusion", source, now); err == nil {
		t.Fatal("sensitive source became an experience candidate")
	}
	source.References = []string{"README.md"}
	if _, err := NewMemoryCandidate("workspace-candidate", "Summary", strings.Repeat("x", MaxMemoryContentBytes+1), source, now); err == nil {
		t.Fatal("oversized candidate was silently truncated")
	}
}
