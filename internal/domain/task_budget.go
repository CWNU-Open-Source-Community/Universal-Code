package domain

import (
	"cyberagent-workbench/internal/runmutation"
	"encoding/json"
	"errors"
	"math"
)

// TaskBudgetSettings is an operator input, never a repository permission grant.
// Missing fields use the established defaults. Optional zero bounds disable only
// that budget dimension; turns and tools always retain positive finite bounds.
type TaskBudgetSettings struct {
	MaxTurns       *int     `json:"max_turns,omitempty"`
	MaxTokens      *int64   `json:"max_tokens,omitempty"`
	MaxToolCalls   *int64   `json:"max_tool_calls,omitempty"`
	MaxCostUSD     *float64 `json:"max_cost_usd,omitempty"`
	TimeoutSeconds *int64   `json:"timeout_seconds,omitempty"`
}

func (s *TaskBudgetSettings) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("budget must be a JSON object")
	}
	for key, value := range fields {
		switch key {
		case "max_turns", "max_tokens", "max_tool_calls", "max_cost_usd", "timeout_seconds":
		default:
			return errors.New("budget contains an unsupported field")
		}
		if string(value) == "null" {
			return errors.New("budget fields must be numbers, not null")
		}
	}
	type plain TaskBudgetSettings
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errors.New("budget fields must contain supported numeric values")
	}
	*s = TaskBudgetSettings(decoded)
	return nil
}

const (
	MaxTaskTurns                = 10_000
	MaxTaskTokens         int64 = 1_000_000_000
	MaxTaskToolCalls      int64 = 1_000_000
	MaxTaskCostUSD              = 100_000.0
	MaxTaskTimeoutSeconds int64 = 604_800
)

func (s *TaskBudgetSettings) Normalize() (Budget, error) {
	b := DefaultBudget()
	if s != nil {
		if s.MaxTurns != nil {
			b.MaxTurns = *s.MaxTurns
		}
		if s.MaxTokens != nil {
			b.MaxTokens = *s.MaxTokens
		}
		if s.MaxToolCalls != nil {
			b.MaxToolCalls = *s.MaxToolCalls
		}
		if s.MaxCostUSD != nil {
			b.MaxCostUSD = *s.MaxCostUSD
		}
		if s.TimeoutSeconds != nil {
			b.TimeoutSeconds = *s.TimeoutSeconds
		}
	}
	if err := ValidateTaskBudget(b); err != nil {
		return Budget{}, err
	}
	// USD input is canonical integer micro-USD at the product boundary. This
	// is a limit, not a claim about billing or the provider's actual charge.
	if b.MaxCostUSD > 0 {
		if b.MaxCostUSD < 0.000001 {
			return Budget{}, errors.New("positive cost limit must be at least one micro-USD")
		}
		b.MaxCostUSD = math.Round(b.MaxCostUSD*1_000_000) / 1_000_000
	}
	return b, nil
}

func ValidateTaskBudget(b Budget) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if b.MaxTurns > MaxTaskTurns || b.MaxTokens > MaxTaskTokens ||
		b.MaxToolCalls <= 0 || b.MaxToolCalls > MaxTaskToolCalls ||
		b.MaxCostUSD > MaxTaskCostUSD || b.TimeoutSeconds > MaxTaskTimeoutSeconds {
		return errors.New("task budget exceeds supported bounds (turns 1..10000, tools 1..1000000, tokens 0..1000000000, USD 0..100000, timeout 0..604800 seconds)")
	}
	return nil
}

// CreationBudget preserves legacy default-budget fingerprints and pins the
// normalized operator ceiling independently of project narrowing.
func (c RunConfig) CreationBudget() Budget {
	if c.RequestedBudget != nil {
		return *c.RequestedBudget
	}
	return DefaultBudget()
}

// ControlledCreationFingerprint binds only canonical operator input, retaining
// historical default digests. Repository changes never reinterpret a retry.
func ControlledCreationFingerprint(goal, workspaceID, profile, surface, phase,
	networkMode string, allowedTargets []string, modelRoute, requestedBy string,
	budget Budget, webScope ...Scope,
) string {
	base := runmutation.RunCreationRequestFingerprintWithNetworkAndModelRoute(goal, workspaceID,
		profile, surface, phase, networkMode, allowedTargets, modelRoute, requestedBy)
	if len(webScope) > 0 && webScope[0].WebAccessMode != "" {
		raw, _ := json.Marshal(struct {
			Mode    WebAccessMode
			Targets []string
		}{webScope[0].WebAccessMode, webScope[0].WebAllowedTargets})
		base = runmutation.Fingerprint("run_creation_web_access.v1", base, string(raw))
	}
	if budget == DefaultBudget() {
		return base
	}
	raw, _ := json.Marshal(budget)
	return runmutation.Fingerprint("run_creation_request.v4", base, string(raw))
}
