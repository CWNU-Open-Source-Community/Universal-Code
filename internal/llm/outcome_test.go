package llm

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNormalizeProviderErrorClassifiesFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		want       Outcome
		wantReason ProviderFailureReason
	}{
		{name: "cancelled", err: context.Canceled, want: OutcomeCancelled,
			wantReason: ProviderFailureNone},
		{name: "deadline", err: context.DeadlineExceeded, want: OutcomeCancelled,
			wantReason: ProviderFailureNetwork},
		{name: "network", err: &url.Error{Op: "Post", URL: "https://provider.invalid", Err: errors.New("connection reset")}, want: OutcomeRetryable,
			wantReason: ProviderFailureNetwork},
		{name: "permanent", err: errors.New("invalid provider configuration"), want: OutcomePermanent,
			wantReason: ProviderFailureNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NormalizeProviderError("test", test.err)
			if got.Kind != test.want || got.Reason != test.wantReason ||
				got.Provider != "test" || !errors.Is(got, test.err) {
				t.Fatalf("unexpected normalized error: %#v", got)
			}
		})
	}
}

func TestProviderErrorPreservesTypedMetadataAndRedactsMessage(t *testing.T) {
	token := "t" + "p-" + strings.Repeat("a", 40)
	original := NewProviderError(OutcomeRateLimited, "", "MIMO_API_KEY="+token, nil)
	original.StatusCode = 429
	original.RetryAfter = 3 * time.Second
	normalized := NormalizeProviderError("mimo", original)
	if normalized.Kind != OutcomeRateLimited || normalized.Reason != ProviderFailureRateLimit ||
		normalized.Provider != "mimo" || normalized.StatusCode != 429 ||
		normalized.RetryAfter != 3*time.Second {
		t.Fatalf("typed metadata changed: %#v", normalized)
	}
	if strings.Contains(normalized.Error(), token[:12]) || !strings.Contains(normalized.Error(), "[REDACTED:") {
		t.Fatalf("provider error was not redacted: %q", normalized.Error())
	}
}

func TestProviderErrorDoesNotProjectCauseText(t *testing.T) {
	marker := "remote-body-marker-that-must-remain-private"
	providerErr := NewProviderError(OutcomeRetryable, "test", "", errors.New(marker))
	if strings.Contains(providerErr.Error(), marker) || !errors.Is(providerErr, providerErr.Cause) {
		t.Fatalf("Provider error projected private cause text: %q", providerErr.Error())
	}
	normalized := NormalizeProviderError("test", &url.Error{
		Op: "Post", URL: "https://provider.invalid", Err: errors.New(marker),
	})
	if strings.Contains(normalized.Error(), marker) || normalized.Reason != ProviderFailureNetwork {
		t.Fatalf("normalized Provider error projected private cause text: %#v", normalized)
	}
}

func TestProviderFailureReasonValidationAndNormalization(t *testing.T) {
	for _, reason := range []ProviderFailureReason{
		ProviderFailureNone, ProviderFailureNotConfigured,
		ProviderFailureAuthentication, ProviderFailureNetwork,
		ProviderFailureRateLimit, ProviderFailureCapacity,
		ProviderFailureModelNotFound, ProviderFailureProtocolIncompatible,
	} {
		if !reason.Valid() {
			t.Fatalf("valid Provider failure reason rejected: %q", reason)
		}
	}
	if ProviderFailureReason("unknown").Valid() {
		t.Fatal("unknown Provider failure reason was accepted")
	}
	tests := []struct {
		name   string
		error  *ProviderError
		reason ProviderFailureReason
	}{
		{name: "authentication", error: &ProviderError{Kind: OutcomePermanent, StatusCode: 401}, reason: ProviderFailureAuthentication},
		{name: "ambiguous not found", error: &ProviderError{Kind: OutcomePermanent, StatusCode: 404}, reason: ProviderFailureProtocolIncompatible},
		{name: "explicit model not found", error: &ProviderError{Kind: OutcomePermanent,
			StatusCode: 404, Reason: ProviderFailureModelNotFound}, reason: ProviderFailureModelNotFound},
		{name: "capacity", error: &ProviderError{Kind: OutcomeRetryable, StatusCode: 503}, reason: ProviderFailureCapacity},
		{name: "network", error: &ProviderError{Kind: OutcomeRetryable}, reason: ProviderFailureNetwork},
		{name: "protocol", error: &ProviderError{Kind: OutcomeInvalidResponse}, reason: ProviderFailureProtocolIncompatible},
		{name: "explicit", error: &ProviderError{Kind: OutcomeRetryable, StatusCode: 503, Reason: ProviderFailureNetwork}, reason: ProviderFailureNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := NormalizeProviderError("test", test.error)
			if normalized.Reason != test.reason || ProviderErrorReason(normalized) != test.reason {
				t.Fatalf("reason=%q want %q", normalized.Reason, test.reason)
			}
		})
	}
}

func TestNormalizeProviderErrorDowngradesInvalidTypedKind(t *testing.T) {
	for _, kind := range []Outcome{"unknown", OutcomeSuccess} {
		normalized := NormalizeProviderError("test", &ProviderError{Kind: kind, Message: "bad kind"})
		if normalized.Kind != OutcomePermanent {
			t.Fatalf("kind %q normalized to %q", kind, normalized.Kind)
		}
	}
}

func TestNewProviderErrorLeavesPermanentReasonUnclassified(t *testing.T) {
	providerErr := NewProviderError(OutcomePermanent, "test", "local validation failed", nil)
	if providerErr.Reason != ProviderFailureNone {
		t.Fatalf("generic permanent error was overclassified: %#v", providerErr)
	}
}

func TestModelAttemptValidation(t *testing.T) {
	base := ModelAttempt{Number: 1, MaxAttempts: 3, Provider: "test", Model: "model"}
	if err := base.ValidateStarted(); err != nil {
		t.Fatal(err)
	}
	completed := base
	completed.Outcome = OutcomeSuccess
	if err := completed.ValidateCompleted(); err != nil {
		t.Fatal(err)
	}
	failed := base
	failed.Outcome = OutcomeRetryable
	failed.ErrorText = "temporary"
	if err := failed.ValidateFailed(); err != nil {
		t.Fatal(err)
	}
	failed.ErrorText = ""
	if err := failed.ValidateFailed(); err == nil {
		t.Fatal("failed attempt accepted an empty error")
	}
	repair := ModelAttempt{
		Number: 4, TransportAttempt: 1, MaxAttempts: 3, ProtocolRepair: 1, Provider: "test", Model: "model",
	}
	if err := repair.ValidateStarted(); err != nil {
		t.Fatalf("global attempt number should be independent from transport limit: %v", err)
	}
	repair.ProtocolRepair = 2
	if err := repair.ValidateStarted(); err == nil {
		t.Fatal("model attempt accepted an unbounded protocol repair number")
	}
	stream := base
	stream.StreamEvents = -1
	if err := stream.ValidateStarted(); err == nil {
		t.Fatal("model attempt accepted negative stream counters")
	}
}

func TestModelContextAuditValidation(t *testing.T) {
	audit := ModelContextAudit{
		TokenBudget: 100, EstimatedTokens: 30,
		Included: []ModelContextSource{{Kind: "summary", SourceID: "summary-1", Tokens: 10}, {Kind: "note", SourceID: "note-1", Tokens: 20}},
		Omitted:  []ModelContextSource{{Kind: "note", SourceID: "note-2", Tokens: 90}},
	}
	if err := audit.Validate(); err != nil {
		t.Fatal(err)
	}
	mismatch := audit
	mismatch.EstimatedTokens = 29
	if err := mismatch.Validate(); err == nil {
		t.Fatal("context audit accepted a mismatched token estimate")
	}
	duplicate := audit
	duplicate.Omitted = []ModelContextSource{{Kind: "note", SourceID: "note-1", Tokens: 90}}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("context audit accepted a duplicate source")
	}
	attempt := ModelAttempt{Number: 1, MaxAttempts: 1, Provider: "test", Model: "model", Context: &audit}
	if err := attempt.ValidateStarted(); err != nil {
		t.Fatalf("valid context audit rejected by model attempt: %v", err)
	}
}

func TestModelContextAuditMemoryProvenanceAndOmissionReasons(t *testing.T) {
	validMemory := ModelContextSource{Kind: "long_term_memory", SourceID: strings.Repeat("m", 256), Tokens: 10,
		MemoryVersion: 2, ContentSHA256: strings.Repeat("a", 64)}
	valid := ModelContextAudit{TokenBudget: 100, EstimatedTokens: 10, Included: []ModelContextSource{validMemory},
		Omitted: []ModelContextSource{{Kind: "long_term_memory", SourceID: "oversized-memory", Tokens: 3000,
			OmissionReason: "long_term_memory_quota"}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("versioned memory audit rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*ModelContextSource)
	}{
		{"missing_version", func(source *ModelContextSource) { source.MemoryVersion = 0 }},
		{"missing_digest", func(source *ModelContextSource) { source.ContentSHA256 = "" }},
		{"invalid_digest", func(source *ModelContextSource) { source.ContentSHA256 = strings.Repeat("z", 64) }},
		{"uppercase_digest", func(source *ModelContextSource) { source.ContentSHA256 = strings.Repeat("A", 64) }},
		{"other_kind", func(source *ModelContextSource) { source.Kind = "note" }},
		{"included_omission", func(source *ModelContextSource) { source.OmissionReason = "budget" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := validMemory
			test.change(&source)
			audit := valid
			audit.Included = []ModelContextSource{source}
			if err := audit.Validate(); err == nil {
				t.Fatal("invalid memory audit accepted")
			}
		})
	}
	invalidReason := valid
	invalidReason.Omitted = []ModelContextSource{{Kind: "long_term_memory", SourceID: "omitted", Tokens: 20,
		OmissionReason: "unbounded free text"}}
	if err := invalidReason.Validate(); err == nil {
		t.Fatal("unknown omission reason accepted")
	}
}
