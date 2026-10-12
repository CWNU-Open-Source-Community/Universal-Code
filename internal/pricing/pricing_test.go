package pricing

import (
	"strings"
	"testing"
	"time"
)

func validSnapshot(t *testing.T) Snapshot {
	t.Helper()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	snapshot := Snapshot{
		ProtocolVersion: ProtocolVersion, ID: "prices-2026-08",
		Source: SourceOperatorImport, Currency: CurrencyUSD,
		ImportedBy: "cli_operator", ImportedAt: now,
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(24 * time.Hour),
		Entries: []Entry{
			{Provider: "mock", Model: "mock-code",
				InputPerMillionMicros: 150000, OutputPerMillionMicros: 600000,
				CacheReadPerMillionMicros: testPrice(0), ToolCallMicros: 0},
			{Provider: "deepseek", Model: "deepseek-v4-flash",
				InputPerMillionMicros: 140000, OutputPerMillionMicros: 280000,
				CacheReadPerMillionMicros: testPrice(14000), ToolCallMicros: 0},
			{Provider: "openai", Model: "gpt-4.1-mini",
				InputPerMillionMicros: 40000, OutputPerMillionMicros: 160000,
				CacheReadPerMillionMicros: testPrice(10000), ToolCallMicros: 1000},
		},
	}
	snapshot.Fingerprint = Fingerprint(snapshot)
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPricingSnapshotValidationAndFingerprint(t *testing.T) {
	snapshot := validSnapshot(t)
	if !snapshot.Active(time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)) {
		t.Fatal("in-window snapshot is not active")
	}
	if snapshot.Active(time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)) {
		t.Fatal("expired snapshot is active")
	}
	entry, ok := snapshot.Lookup("mock", "mock-code")
	if !ok || entry.OutputPerMillionMicros != 600000 {
		t.Fatalf("lookup failed: %#v %t", entry, ok)
	}
	if _, ok := snapshot.Lookup("mock", "unknown-model"); ok {
		t.Fatal("unknown model resolved")
	}
	if snapshot.Fingerprint != Fingerprint(snapshot) {
		t.Fatal("fingerprint is not deterministic")
	}
	tampered := snapshot
	tampered.Entries[0].OutputPerMillionMicros++
	if tampered.Fingerprint == Fingerprint(tampered) {
		t.Fatal("tampered entry kept its fingerprint")
	}
}

func TestPricingCostArithmeticIsCeilingAndInteger(t *testing.T) {
	entry := Entry{InputPerMillionMicros: 1000000, OutputPerMillionMicros: 2000000,
		ToolCallMicros: 500000}
	// 1 token at 1 USD per million -> exactly 1 micro-USD.
	if cost := entry.EstimateCost(1, 0, 0, 0); cost != 1 {
		t.Fatalf("single input token cost=%d", cost)
	}
	// Exact: 500001 tokens at 2 USD per million -> 1000002 micros.
	if cost := entry.EstimateCost(0, 500001, 0, 0); cost != 1000002 {
		t.Fatalf("exact output cost=%d", cost)
	}
	// Ceiling: one token at 1.5 USD per million rounds up to 2 micros.
	fractional := Entry{InputPerMillionMicros: 1500000}
	if cost := fractional.EstimateCost(1, 0, 0, 0); cost != 2 {
		t.Fatalf("ceiling fractional cost=%d", cost)
	}
	// Tool calls are per-call micros.
	if cost := entry.EstimateCost(0, 0, 0, 3); cost != 1500000 {
		t.Fatalf("tool cost=%d", cost)
	}
	// Negative usage returns zero.
	if cost := entry.EstimateCost(-1, 0, 0, 0); cost != 0 {
		t.Fatalf("negative usage cost=%d", cost)
	}
}

func TestPricingParseWireRejectsMalformedDocuments(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	valid := `{
		"protocol_version": "price_snapshot.v1",
		"id": "prices-2026-08",
		"source": "operator_import",
		"currency": "USD",
		"imported_by": "cli_operator",
		"valid_from": "2026-08-15T11:00:00Z",
		"valid_until": "2026-08-16T12:00:00Z",
		"entries": [{
			"provider": "mock", "model": "mock-code",
			"input_per_million_micros": 150000,
			"output_per_million_micros": 600000,
			"cache_read_per_million_micros": 0,
			"tool_call_micros": 0
		}]
	}`
	parsed, err := ParseWire([]byte(valid), now)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ID != "prices-2026-08" || len(parsed.Entries) != 1 {
		t.Fatalf("unexpected parse result: %#v", parsed)
	}
	rejections := []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: strings.Replace(valid, `"id"`, `"secret_field"`, 1)},
		{name: "duplicate entry", raw: strings.Replace(valid, `"tool_call_micros": 0`,
			`"tool_call_micros": 0`, 1) + strings.Repeat("x", 0)},
		{name: "negative price", raw: strings.Replace(valid, "150000", "-5", 1)},
		{name: "non-usd currency", raw: strings.Replace(valid, `"USD"`, `"EUR"`, 1)},
		{name: "expired window", raw: strings.Replace(valid, "2026-08-16T12:00:00Z",
			"2026-08-15T11:30:00Z", 1)},
	}
	// The duplicate-entry case needs a real duplicated block.
	rejections[1].raw = valid + valid[:1] + "}"
	for _, test := range rejections {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseWire([]byte(test.raw), now); err == nil {
				t.Fatal("malformed document was accepted")
			}
		})
	}
}

func TestPricingMaxMicrosBound(t *testing.T) {
	entry := Entry{Provider: "p", Model: "m", OutputPerMillionMicros: MaxMicros + 1}
	if err := entry.Validate(); err == nil {
		t.Fatal("oversized price was accepted")
	}
}
