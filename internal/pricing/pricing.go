// Package pricing defines the operator-owned, versioned price snapshot and
// the integer micro-USD cost arithmetic used by the monetary budget ledger.
// Prices are configuration data, never model output: a Provider response,
// README, Skill, or repository file can never raise a price or extend a
// budget.
package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ProtocolVersion = "price_snapshot.v1"

	// CurrencyUSD is the only supported billing currency in this slice.
	CurrencyUSD = "USD"

	// SourceOperatorImport marks operator-imported tables. Future sources
	// (vendor metadata) must add their own closed source value.
	SourceOperatorImport = "operator_import"

	MaxSnapshotBytes     = 64 * 1024
	MaxEntries           = 512
	MaxProviderNameBytes = 128
	MaxModelNameBytes    = 256
	// MicrosPerUSD keeps all monetary arithmetic in integer micro-USD so the
	// ledger never carries floating point.
	MicrosPerUSD = int64(1_000_000)
	MaxMicros    = int64(100_000_000_000) * MicrosPerUSD
)

// Entry prices one exact provider/model pair. All prices are integer
// micro-USD; per-million-token prices keep sub-cent precision without floats.
type Entry struct {
	Provider                        string
	Model                           string
	InputPerMillionMicros           int64
	OutputPerMillionMicros          int64
	CacheReadPerMillionMicros       *int64
	CacheCreationPerMillionMicros   *int64 `json:",omitempty"`
	CacheCreation5mPerMillionMicros *int64 `json:",omitempty"`
	CacheCreation1hPerMillionMicros *int64 `json:",omitempty"`
	ToolCallMicros                  int64
}

func (e Entry) Validate() error {
	if !validBounded(e.Provider, MaxProviderNameBytes) || !validBounded(e.Model, MaxModelNameBytes) {
		return errors.New("price entry provider/model must be bounded normalized UTF-8")
	}
	for label, value := range map[string]int64{
		"input price": e.InputPerMillionMicros, "output price": e.OutputPerMillionMicros,
		"tool call price": e.ToolCallMicros,
	} {
		if value < 0 || value > MaxMicros {
			return fmt.Errorf("price entry %s is out of range", label)
		}
	}
	for label, value := range map[string]*int64{
		"cache read price":        e.CacheReadPerMillionMicros,
		"cache creation price":    e.CacheCreationPerMillionMicros,
		"5m cache creation price": e.CacheCreation5mPerMillionMicros,
		"1h cache creation price": e.CacheCreation1hPerMillionMicros,
	} {
		if value != nil && (*value < 0 || *value > MaxMicros) {
			return fmt.Errorf("price entry %s is out of range", label)
		}
	}
	return nil
}

// EstimateCost converts token and tool-call usage into integer micro-USD
// using ceiling division so a partial unit never rounds down to free.
func (e Entry) EstimateCost(inputTokens, outputTokens, cacheReadTokens, toolCalls int64) int64 {
	if inputTokens < 0 || outputTokens < 0 || cacheReadTokens < 0 || toolCalls < 0 {
		return 0
	}
	return addCosts(ceilTokens(inputTokens, e.InputPerMillionMicros),
		ceilTokens(outputTokens, e.OutputPerMillionMicros),
		ceilTokens(cacheReadTokens, optionalPrice(e.CacheReadPerMillionMicros)),
		checkedMul(toolCalls, e.ToolCallMicros))
}

// TokenUsage uses the normalized input total, including its disjoint cache
// subsets. Missing category prices differ from explicitly configured zero.
type TokenUsage struct {
	InputTokens, OutputTokens                    int64
	CacheReadTokens, CacheCreationTokens         int64
	CacheCreation5mTokens, CacheCreation1hTokens int64
	CacheCreationBreakdownKnown                  bool
}

var ErrCachePriceMissing = errors.New("cache usage has no matching price in the reserved snapshot")

func (e Entry) UsageCost(u TokenUsage, toolCalls int64) (int64, error) {
	if u.InputTokens < 0 || u.OutputTokens < 0 || toolCalls < 0 ||
		u.CacheReadTokens < 0 || u.CacheCreationTokens < 0 ||
		u.CacheCreation5mTokens < 0 || u.CacheCreation1hTokens < 0 ||
		u.CacheReadTokens > u.InputTokens || u.CacheCreationTokens > u.InputTokens-u.CacheReadTokens ||
		u.CacheCreation5mTokens > u.CacheCreationTokens ||
		u.CacheCreation1hTokens > u.CacheCreationTokens-u.CacheCreation5mTokens ||
		(u.CacheCreationBreakdownKnown && u.CacheCreation5mTokens+u.CacheCreation1hTokens != u.CacheCreationTokens) {
		return 0, errors.New("cache token usage is inconsistent")
	}
	if u.CacheReadTokens > 0 && e.CacheReadPerMillionMicros == nil {
		return 0, ErrCachePriceMissing
	}
	creationCost := int64(0)
	if u.CacheCreationTokens > 0 {
		if u.CacheCreationBreakdownKnown {
			if (u.CacheCreation5mTokens > 0 && e.CacheCreation5mPerMillionMicros == nil) ||
				(u.CacheCreation1hTokens > 0 && e.CacheCreation1hPerMillionMicros == nil) {
				return 0, ErrCachePriceMissing
			}
			creationCost = addCosts(ceilTokens(u.CacheCreation5mTokens, optionalPrice(e.CacheCreation5mPerMillionMicros)),
				ceilTokens(u.CacheCreation1hTokens, optionalPrice(e.CacheCreation1hPerMillionMicros)))
		} else {
			if e.CacheCreationPerMillionMicros == nil {
				return 0, ErrCachePriceMissing
			}
			creationCost = ceilTokens(u.CacheCreationTokens, *e.CacheCreationPerMillionMicros)
		}
	}
	return addCosts(ceilTokens(u.InputTokens-u.CacheReadTokens-u.CacheCreationTokens, e.InputPerMillionMicros),
		ceilTokens(u.OutputTokens, e.OutputPerMillionMicros),
		ceilTokens(u.CacheReadTokens, optionalPrice(e.CacheReadPerMillionMicros)),
		creationCost, checkedMul(toolCalls, e.ToolCallMicros)), nil
}

// ReserveCost uses the highest configured input-category price because the
// eventual cache hit and creation split is unknown before dispatch.
func (e Entry) ReserveCost(inputTokens, outputTokens, toolCalls int64) int64 {
	inputRate := e.InputPerMillionMicros
	for _, rate := range []*int64{e.CacheReadPerMillionMicros, e.CacheCreationPerMillionMicros,
		e.CacheCreation5mPerMillionMicros, e.CacheCreation1hPerMillionMicros} {
		if rate != nil {
			inputRate = max(inputRate, *rate)
		}
	}
	return addCosts(ceilTokens(inputTokens, inputRate), ceilTokens(outputTokens, e.OutputPerMillionMicros),
		checkedMul(toolCalls, e.ToolCallMicros))
}

func optionalPrice(price *int64) int64 {
	if price == nil {
		return 0
	}
	return *price
}

func addCosts(costs ...int64) int64 {
	var total int64
	for _, cost := range costs {
		if cost > math.MaxInt64-total {
			return math.MaxInt64
		}
		total += cost
	}
	return total
}

func ceilTokens(tokens int64, perMillionMicros int64) int64 {
	if tokens <= 0 || perMillionMicros <= 0 {
		return 0
	}
	if tokens > math.MaxInt64/perMillionMicros {
		return math.MaxInt64
	}
	product := tokens * perMillionMicros
	return product/MicrosPerUSD + boolInt(product%MicrosPerUSD != 0)
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func checkedMul(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

// Snapshot is one immutable, operator-imported price table generation.
type Snapshot struct {
	ProtocolVersion string
	ID              string
	Source          string
	Currency        string
	Entries         []Entry
	ImportedBy      string
	ImportedAt      time.Time
	ValidFrom       time.Time
	ValidUntil      time.Time
	Fingerprint     string
}

func (s Snapshot) Validate() error {
	if s.ProtocolVersion != ProtocolVersion {
		return errors.New("price snapshot protocol version is invalid")
	}
	if s.Source != SourceOperatorImport {
		return errors.New("price snapshot source is invalid")
	}
	if s.Currency != CurrencyUSD {
		return errors.New("price snapshot currency is unsupported")
	}
	if !validBounded(s.ID, 128) || !validBounded(s.ImportedBy, MaxProviderNameBytes) {
		return errors.New("price snapshot identity is invalid")
	}
	if s.ImportedAt.IsZero() || s.ValidFrom.IsZero() || s.ValidUntil.IsZero() ||
		!s.ValidUntil.After(s.ValidFrom) || s.ValidUntil.After(s.ValidFrom.AddDate(1, 0, 0)) {
		return errors.New("price snapshot timestamps are invalid")
	}
	if len(s.Entries) == 0 || len(s.Entries) > MaxEntries {
		return errors.New("price snapshot entry count is out of range")
	}
	if len(s.Fingerprint) != sha256.Size*2 {
		return errors.New("price snapshot fingerprint is invalid")
	}
	seen := make(map[string]struct{}, len(s.Entries))
	for index, entry := range s.Entries {
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("price snapshot entry %d is invalid: %w", index, err)
		}
		key := entry.Provider + string([]byte{0}) + entry.Model
		if _, exists := seen[key]; exists {
			return errors.New("price snapshot entries must be unique per provider/model")
		}
		seen[key] = struct{}{}
	}
	if s.Fingerprint != Fingerprint(s) {
		return errors.New("price snapshot fingerprint mismatch")
	}
	return nil
}

// Active returns whether the snapshot is currently valid.
func (s Snapshot) Active(now time.Time) bool {
	return s.Validate() == nil && !now.Before(s.ValidFrom) && now.Before(s.ValidUntil)
}

// Lookup finds the exact entry for a provider/model pair.
func (s Snapshot) Lookup(provider, model string) (Entry, bool) {
	for _, entry := range s.Entries {
		if entry.Provider == provider && entry.Model == model {
			return entry, true
		}
	}
	return Entry{}, false
}

// Fingerprint is the deterministic content hash over the canonical entry set.
// It binds the ID and makes same-content imports idempotent.
func Fingerprint(s Snapshot) string {
	entries := append([]Entry(nil), s.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Provider != entries[j].Provider {
			return entries[i].Provider < entries[j].Provider
		}
		return entries[i].Model < entries[j].Model
	})
	hash := sha256.New()
	for _, part := range []string{s.ProtocolVersion, s.Source, s.Currency, s.ID, s.ImportedBy} {
		_, _ = hash.Write([]byte(strings.TrimSpace(part)))
		_, _ = hash.Write([]byte{0})
	}
	for _, entry := range entries {
		_, _ = hash.Write([]byte(entry.Provider))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(entry.Model))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(fmt.Sprintf("%d,%d,%d,%d", entry.InputPerMillionMicros,
			entry.OutputPerMillionMicros, optionalPrice(entry.CacheReadPerMillionMicros), entry.ToolCallMicros)))
		_, _ = hash.Write([]byte{0})
		// Retain the exact legacy hash when the legacy read price is present
		// and no new creation rates were supplied. Historical tables stay valid.
		if entry.CacheReadPerMillionMicros == nil {
			_, _ = hash.Write([]byte("cache_read_missing\x00"))
		}
		for _, field := range []struct {
			name  string
			price *int64
		}{
			{"cache_creation", entry.CacheCreationPerMillionMicros},
			{"cache_creation_5m", entry.CacheCreation5mPerMillionMicros},
			{"cache_creation_1h", entry.CacheCreation1hPerMillionMicros},
		} {
			if field.price != nil {
				_, _ = hash.Write([]byte(fmt.Sprintf("%s=%d\x00", field.name, *field.price)))
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// USDToMicros converts a finite non-negative USD amount to integer
// micro-USD, rounding to the nearest micro. Budget input uses this once at
// the boundary; the ledger itself never carries floats.
func USDToMicros(usd float64) (int64, error) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0, errors.New("USD amount must be finite and non-negative")
	}
	if usd > float64(MaxMicros)/float64(MicrosPerUSD) {
		return 0, errors.New("USD amount exceeds the supported cap")
	}
	return int64(math.Round(usd * float64(MicrosPerUSD))), nil
}

// MicrosToUSD renders a human-readable USD amount for display only.
func MicrosToUSD(micros int64) string {
	if micros < 0 {
		micros = 0
	}
	whole := micros / MicrosPerUSD
	fraction := micros % MicrosPerUSD
	return fmt.Sprintf("%d.%06d", whole, fraction)
}
func validBounded(value string, maxBytes int) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		utf8.ValidString(value) && len([]byte(value)) <= maxBytes &&
		strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}

// Wire is the operator-importable JSON shape. Only this closed schema is
// accepted; unknown fields and non-object shapes are rejected.
type Wire struct {
	ProtocolVersion string `json:"protocol_version"`
	ID              string `json:"id"`
	Source          string `json:"source"`
	Currency        string `json:"currency"`
	ImportedBy      string `json:"imported_by"`
	ValidFrom       string `json:"valid_from"`
	ValidUntil      string `json:"valid_until"`
	Entries         []struct {
		Provider                        string `json:"provider"`
		Model                           string `json:"model"`
		InputPerMillionMicros           int64  `json:"input_per_million_micros"`
		OutputPerMillionMicros          int64  `json:"output_per_million_micros"`
		CacheReadPerMillionMicros       *int64 `json:"cache_read_per_million_micros,omitempty"`
		CacheCreationPerMillionMicros   *int64 `json:"cache_creation_per_million_micros,omitempty"`
		CacheCreation5mPerMillionMicros *int64 `json:"cache_creation_5m_per_million_micros,omitempty"`
		CacheCreation1hPerMillionMicros *int64 `json:"cache_creation_1h_per_million_micros,omitempty"`
		ToolCallMicros                  int64  `json:"tool_call_micros"`
	} `json:"entries"`
}

// ParseWire decodes and validates one operator price table document.
func ParseWire(raw []byte, now time.Time) (Snapshot, error) {
	if len(raw) == 0 || len(raw) > MaxSnapshotBytes || !utf8.Valid(raw) ||
		!json.Valid(raw) {
		return Snapshot{}, errors.New("price snapshot document is invalid")
	}
	var wire Wire
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Snapshot{}, errors.New("price snapshot document has unknown or malformed fields")
	}
	var trailing any
	if decoder.Decode(&trailing) == nil {
		return Snapshot{}, errors.New("price snapshot document has trailing content")
	}
	validFrom, err := time.Parse(time.RFC3339, wire.ValidFrom)
	if err != nil {
		return Snapshot{}, errors.New("price snapshot valid_from is invalid")
	}
	validUntil, err := time.Parse(time.RFC3339, wire.ValidUntil)
	if err != nil {
		return Snapshot{}, errors.New("price snapshot valid_until is invalid")
	}
	snapshot := Snapshot{
		ProtocolVersion: wire.ProtocolVersion, ID: strings.TrimSpace(wire.ID),
		Source: wire.Source, Currency: wire.Currency, ImportedBy: strings.TrimSpace(wire.ImportedBy),
		ImportedAt: now.UTC(), ValidFrom: validFrom, ValidUntil: validUntil,
	}
	for _, entry := range wire.Entries {
		snapshot.Entries = append(snapshot.Entries, Entry{
			Provider: entry.Provider, Model: entry.Model,
			InputPerMillionMicros:           entry.InputPerMillionMicros,
			OutputPerMillionMicros:          entry.OutputPerMillionMicros,
			CacheReadPerMillionMicros:       entry.CacheReadPerMillionMicros,
			CacheCreationPerMillionMicros:   entry.CacheCreationPerMillionMicros,
			CacheCreation5mPerMillionMicros: entry.CacheCreation5mPerMillionMicros,
			CacheCreation1hPerMillionMicros: entry.CacheCreation1hPerMillionMicros,
			ToolCallMicros:                  entry.ToolCallMicros,
		})
	}
	snapshot.Fingerprint = Fingerprint(snapshot)
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	if !snapshot.Active(now) {
		return Snapshot{}, errors.New("price snapshot validity window must include the import time")
	}
	return snapshot, nil
}
