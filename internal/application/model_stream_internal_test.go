package application

import (
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/llm"
)

func TestModelStreamPreservesAdapterGenerationDuration(t *testing.T) {
	decode := int64(123)
	aggregator := &modelStreamAggregator{}
	aggregator.observeUsage(llm.ChatChunk{Done: true, Usage: &llm.Usage{OutputTokens: 10, TotalTokens: 10},
		Events: []llm.StreamEvent{{Type: llm.StreamResponseCompleted, DecodeMillis: &decode}}})
	result := aggregator.result(nil)
	if result.DecodeMillis == nil || *result.DecodeMillis != 123 || result.Usage == nil || result.Usage.OutputTokens != 10 {
		t.Fatalf("adapter generation timing was lost: %+v", result)
	}
}

func TestModelStreamPublicPreviewForcesFinalScanBelowCadence(t *testing.T) {
	registry := newActiveCallRegistry(1)
	key := activeCallKey{runID: "run-preview", attemptID: "attempt-preview", modelAttempt: 1}
	now := time.Now().UTC()
	entry := &activeCallEntry{
		key:     key,
		started: true,
		info: ActiveCallInfo{
			RunID: "run-preview", SessionID: "session-preview", AttemptID: "attempt-preview",
			ModelAttempt: 1, TransportAttempt: 1, MaxAttempts: 1,
			Provider: "provider", Model: "model", StartedAt: now,
		},
		publicRevision:  1,
		publicUpdatedAt: now,
		subscribers:     map[uint64]*activeCallSubscriber{},
	}
	registry.calls[key.runID] = entry
	aggregator := &modelStreamAggregator{
		ref:               llm.ModelRef{Provider: "provider", Model: "model"},
		live:              &activeCallLease{registry: registry, key: key, entry: entry},
		rootPreview:       newRootMessagePreviewer(nil),
		commentaryPreview: newPublicCommentaryPreviewer(nil),
	}

	message := strings.Repeat("safe-", 24)
	first := `{"version":"root_lifecycle.v1","action":"continue","message":"` + message
	if err := aggregator.appendText(first); err != nil {
		t.Fatal(err)
	}
	initial, found := registry.LookupPublic(key.runID)
	if !found || initial.Text == "" || initial.MessageComplete {
		t.Fatalf("expected a safe incomplete preview after the cadence boundary: %#v", initial)
	}

	tail := `-final"}`
	if len(tail) >= publicPreviewScanIntervalBytes {
		t.Fatalf("test tail unexpectedly reached the scan cadence: %d", len(tail))
	}
	if err := aggregator.appendText(tail); err != nil {
		t.Fatal(err)
	}
	deferred, found := registry.LookupPublic(key.runID)
	if !found || deferred.Revision != initial.Revision || deferred.Text != initial.Text || deferred.MessageComplete {
		t.Fatalf("sub-cadence tail should remain deferred until the final scan: %#v", deferred)
	}

	if err := aggregator.publishPublicPreview(true); err != nil {
		t.Fatal(err)
	}
	final, found := registry.LookupPublic(key.runID)
	if !found || final.Text != message+"-final" || !final.MessageComplete || final.Revision <= deferred.Revision {
		t.Fatalf("forced final scan did not publish the complete safe message: %#v", final)
	}
}

func TestModelStreamOrdinaryProseDoesNotBecomeToolCommentary(t *testing.T) {
	registry := newActiveCallRegistry(1)
	key := activeCallKey{runID: "run-plain", attemptID: "attempt-plain", modelAttempt: 1}
	now := time.Now().UTC()
	entry := &activeCallEntry{
		key: key, started: true,
		info: ActiveCallInfo{
			RunID: key.runID, SessionID: "session-plain", AttemptID: key.attemptID,
			ModelAttempt: 1, TransportAttempt: 1, MaxAttempts: 1,
			Provider: "provider", Model: "model", StartedAt: now,
		},
		publicRevision: 1, publicKind: PublicModelStreamRootMessage,
		publicUpdatedAt: now, subscribers: map[uint64]*activeCallSubscriber{},
	}
	registry.calls[key.runID] = entry
	aggregator := &modelStreamAggregator{
		ref:               llm.ModelRef{Provider: "provider", Model: "model"},
		live:              &activeCallLease{registry: registry, key: key, entry: entry},
		rootPreview:       newRootMessagePreviewer(nil),
		commentaryPreview: newPublicCommentaryPreviewer(nil),
	}
	aggregator.output.WriteString("这是一段普通最终回复，不是工具调用前的公开进度。")
	if err := aggregator.publishPublicPreview(true); err != nil {
		t.Fatal(err)
	}
	snapshot, found := registry.LookupPublic(key.runID)
	if !found || snapshot.Revision != 1 || snapshot.Text != "" ||
		snapshot.ContentKind != PublicModelStreamRootMessage {
		t.Fatalf("ordinary prose leaked into Live Activity: %#v found=%v", snapshot, found)
	}
	if err := aggregator.publishCommentaryPreview(true); err != nil {
		t.Fatal(err)
	}
	snapshot, found = registry.LookupPublic(key.runID)
	if !found || snapshot.ContentKind != PublicModelStreamToolCommentary ||
		snapshot.Text == "" || !snapshot.MessageComplete {
		t.Fatalf("explicit tool commentary was not published: %#v found=%v", snapshot, found)
	}
}
