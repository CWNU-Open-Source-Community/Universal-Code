package pricing

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func testPrice(value int64) *int64 { return &value }

func TestCacheUsagePricesDisjointInputCategories(t *testing.T) {
	entry := Entry{InputPerMillionMicros: 2_000_000, OutputPerMillionMicros: 4_000_000,
		CacheReadPerMillionMicros: testPrice(500_000), CacheCreationPerMillionMicros: testPrice(3_000_000),
		CacheCreation5mPerMillionMicros: testPrice(3_000_000), CacheCreation1hPerMillionMicros: testPrice(4_000_000), ToolCallMicros: 7}
	u := TokenUsage{InputTokens: 300, OutputTokens: 50, CacheReadTokens: 100, CacheCreationTokens: 100,
		CacheCreation5mTokens: 40, CacheCreation1hTokens: 60, CacheCreationBreakdownKnown: true}
	if cost, err := entry.UsageCost(u, 1); err != nil || cost != 817 {
		t.Fatalf("mixed TTL usage cost=%d err=%v", cost, err)
	}
	u.CacheCreationBreakdownKnown = false
	if cost, err := entry.UsageCost(u, 1); err != nil || cost != 757 {
		t.Fatalf("aggregate creation cost=%d err=%v", cost, err)
	}
	if reserve := entry.ReserveCost(300, 50, 1); reserve != 1407 {
		t.Fatalf("write-cache reservation=%d", reserve)
	}
}

func TestCacheUsageMissingPriceIsNotFreeOrAnInferredTTL(t *testing.T) {
	u := TokenUsage{InputTokens: 100, CacheCreationTokens: 100}
	entry := Entry{InputPerMillionMicros: 1_000_000, CacheCreation5mPerMillionMicros: testPrice(2_000_000)}
	if _, err := entry.UsageCost(u, 0); !errors.Is(err, ErrCachePriceMissing) {
		t.Fatalf("unknown TTL inferred from 5m price: %v", err)
	}
	entry.CacheCreationPerMillionMicros = testPrice(0)
	if cost, err := entry.UsageCost(u, 0); err != nil || cost != 0 {
		t.Fatalf("explicit free creation cost=%d err=%v", cost, err)
	}
	u = TokenUsage{InputTokens: 100, CacheReadTokens: 100}
	if _, err := entry.UsageCost(u, 0); !errors.Is(err, ErrCachePriceMissing) {
		t.Fatalf("missing read price treated as free: %v", err)
	}
	entry.CacheReadPerMillionMicros = testPrice(0)
	if cost, err := entry.UsageCost(u, 0); err != nil || cost != 0 {
		t.Fatalf("explicit free read cost=%d err=%v", cost, err)
	}
	u.CacheCreationTokens = 1
	if _, err := entry.UsageCost(u, 0); err == nil {
		t.Fatal("overlapping cache categories accepted")
	}
}

func TestCachePriceExtensionsPreserveLegacySnapshotFingerprint(t *testing.T) {
	s := validSnapshot(t)
	var oldEntries []Entry
	if err := json.Unmarshal([]byte(`[{"Provider":"mock","Model":"mock-code","InputPerMillionMicros":150000,"OutputPerMillionMicros":600000,"CacheReadPerMillionMicros":0,"ToolCallMicros":0},{"Provider":"deepseek","Model":"deepseek-v4-flash","InputPerMillionMicros":140000,"OutputPerMillionMicros":280000,"CacheReadPerMillionMicros":14000,"ToolCallMicros":0},{"Provider":"openai","Model":"gpt-4.1-mini","InputPerMillionMicros":40000,"OutputPerMillionMicros":160000,"CacheReadPerMillionMicros":10000,"ToolCallMicros":1000}]`), &oldEntries); err != nil {
		t.Fatal(err)
	}
	s.Entries = oldEntries
	if err := s.Validate(); err != nil {
		t.Fatalf("legacy entries no longer match their fingerprint: %v", err)
	}
	before := s.Fingerprint
	s.Entries[0].CacheCreation5mPerMillionMicros = testPrice(0)
	if Fingerprint(s) == before {
		t.Fatal("an explicit free creation price was not fingerprinted")
	}
	s.Entries[0].CacheCreation5mPerMillionMicros = nil
	s.Entries[0].CacheReadPerMillionMicros = nil
	if Fingerprint(s) == before {
		t.Fatal("missing read price equated to explicitly free")
	}
}

func TestCacheCostArithmeticSaturatesInsteadOfWrapping(t *testing.T) {
	e := Entry{InputPerMillionMicros: math.MaxInt64, OutputPerMillionMicros: math.MaxInt64}
	if cost := e.EstimateCost(1, 1, 0, 0); cost <= 0 {
		t.Fatalf("ceiling addition overflowed: %d", cost)
	}
	if cost := e.EstimateCost(math.MaxInt64, math.MaxInt64, 0, 0); cost != math.MaxInt64 {
		t.Fatalf("sum did not saturate: %d", cost)
	}
}
