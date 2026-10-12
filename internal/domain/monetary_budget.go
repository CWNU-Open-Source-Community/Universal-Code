package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// MonetaryScope values bind one reservation to the call path that created
	// it. All scopes share the single run-level aggregate so concurrent model
	// calls can never oversell the run total.
	MonetaryScopeRoot                = "root"
	MonetaryScopeSpecialist          = "specialist"
	MonetaryScopeReadOnlyFanout      = "readonly_fanout"
	MonetaryPricingPriced            = "priced"
	MonetaryPricingUsageUnknown      = "usage_unknown"
	MonetaryPricingPriceMissing      = "price_missing"
	MonetaryPricingCachePriceMissing = "cache_price_missing"
)

// MonetaryUsage is the durable run-level aggregate for the monetary budget.
// Every value is integer micro-USD; the currency is fixed to USD in this
// slice. Open exposure is Reserved - Settled - Released.
type MonetaryUsage struct {
	RunID           string
	Currency        string
	CapMicros       int64
	ReservedMicros  int64
	SettledMicros   int64
	ReleasedMicros  int64
	RemainingMicros int64
	EstimateSource  string
	UpdatedAt       time.Time
	ExhaustedAt     *time.Time
	Tracked         bool
	UnpricedCalls   int64
}

func (u MonetaryUsage) Validate() error {
	if strings.TrimSpace(u.RunID) == "" {
		return errors.New("monetary usage requires a run id")
	}
	if u.Currency != "USD" {
		return errors.New("monetary usage currency is unsupported")
	}
	values := []int64{u.CapMicros, u.ReservedMicros, u.SettledMicros, u.ReleasedMicros, u.RemainingMicros, u.UnpricedCalls}
	for _, value := range values {
		if value < 0 {
			return errors.New("monetary usage counters cannot be negative")
		}
	}
	if u.ReservedMicros < u.SettledMicros || u.ReservedMicros-u.SettledMicros < u.ReleasedMicros {
		return errors.New("monetary usage settled and released exceed the reservation")
	}
	if u.Tracked != (u.CapMicros > 0) {
		return errors.New("monetary usage tracked flag is inconsistent with the cap")
	}
	// Historical ledgers may already exceed a cap. Preserve those facts and
	// expose no remaining allowance instead of making usage unreadable.
	consumedMicros := u.ReservedMicros - u.ReleasedMicros
	if u.Tracked && u.RemainingMicros != max(0, u.CapMicros-consumedMicros) {
		return errors.New("monetary usage remaining amount is inconsistent")
	}
	if strings.TrimSpace(u.EstimateSource) != u.EstimateSource {
		return errors.New("monetary usage estimate source is invalid")
	}
	return nil
}

// MonetaryReserveRequest reserves the upper-bound cost of one model attempt
// against the run aggregate before any Provider call. The attempt identity
// (run + scope + attempt number) makes replays idempotent.
type MonetaryReserveRequest struct {
	RunID            string
	Scope            string
	Provider         string
	Model            string
	AttemptNumber    int64
	ReservedMicros   int64
	PriceFingerprint string
	EstimateSource   string
}

func (r MonetaryReserveRequest) Normalize() (MonetaryReserveRequest, error) {
	r.RunID = strings.TrimSpace(r.RunID)
	r.Scope = strings.TrimSpace(r.Scope)
	r.Provider = strings.TrimSpace(r.Provider)
	r.Model = strings.TrimSpace(r.Model)
	r.EstimateSource = strings.TrimSpace(r.EstimateSource)
	r.PriceFingerprint = strings.TrimSpace(r.PriceFingerprint)
	switch r.Scope {
	case MonetaryScopeRoot, MonetaryScopeSpecialist, MonetaryScopeReadOnlyFanout:
	default:
		return MonetaryReserveRequest{}, errors.New("monetary reserve scope is invalid")
	}
	for label, value := range map[string]string{
		"run id": r.RunID, "provider": r.Provider, "model": r.Model,
		"estimate source": r.EstimateSource, "price fingerprint": r.PriceFingerprint,
	} {
		if value == "" || len(value) > 256 || strings.ContainsRune(value, 0) {
			return MonetaryReserveRequest{}, fmt.Errorf("monetary reserve %s is invalid", label)
		}
	}
	if r.AttemptNumber <= 0 {
		return MonetaryReserveRequest{}, errors.New("monetary reserve attempt number must be positive")
	}
	if r.ReservedMicros <= 0 {
		return MonetaryReserveRequest{}, errors.New("monetary reserve amount must be positive")
	}
	return r, nil
}

// MonetarySettleRequest closes one reservation with the actual cost. The
// unused portion is released in the same transaction so the run exposure
// always equals reserved minus settled minus released.
type MonetarySettleRequest struct {
	RunID         string
	Scope         string
	AttemptNumber int64
	ActualMicros  int64
	PricingStatus string
}

func (r MonetarySettleRequest) Normalize() (MonetarySettleRequest, error) {
	r.RunID = strings.TrimSpace(r.RunID)
	r.Scope = strings.TrimSpace(r.Scope)
	if r.RunID == "" || r.AttemptNumber <= 0 || r.ActualMicros < 0 {
		return MonetarySettleRequest{}, errors.New("monetary settle request is invalid")
	}
	switch r.PricingStatus {
	case "", MonetaryPricingPriced, MonetaryPricingUsageUnknown, MonetaryPricingPriceMissing, MonetaryPricingCachePriceMissing:
	default:
		return MonetarySettleRequest{}, errors.New("monetary pricing status is invalid")
	}
	switch r.Scope {
	case MonetaryScopeRoot, MonetaryScopeSpecialist, MonetaryScopeReadOnlyFanout:
	default:
		return MonetarySettleRequest{}, errors.New("monetary settle scope is invalid")
	}
	return r, nil
}

// MonetaryReleaseRequest releases an open reservation without settling
// (for example when the run reaches a terminal state).
type MonetaryReleaseRequest struct {
	RunID         string
	Scope         string
	AttemptNumber int64
}

func (r MonetaryReleaseRequest) Normalize() (MonetaryReleaseRequest, error) {
	r.RunID = strings.TrimSpace(r.RunID)
	r.Scope = strings.TrimSpace(r.Scope)
	if r.RunID == "" || r.AttemptNumber <= 0 {
		return MonetaryReleaseRequest{}, errors.New("monetary release request is invalid")
	}
	switch r.Scope {
	case MonetaryScopeRoot, MonetaryScopeSpecialist, MonetaryScopeReadOnlyFanout:
	default:
		return MonetaryReleaseRequest{}, errors.New("monetary release scope is invalid")
	}
	return r, nil
}
