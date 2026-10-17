package domain

import (
	"errors"
	"math"
	"strings"
)

// RunAgentUsage is the durable aggregate used to enforce Run-wide model
// budgets across the root Agent and every admitted Specialist.
type RunAgentUsage struct {
	ContextCompactionTokens   int64  `json:"context_compaction_tokens"`
	ContextCompactionMillis   int64  `json:"context_compaction_millis"`
	RunID                     string `json:"run_id"`
	RootTokens                int64  `json:"root_tokens"`
	SpecialistTokens          int64  `json:"specialist_tokens"`
	ReadOnlyFanoutTokens      int64  `json:"read_only_fanout_tokens"`
	TotalTokens               int64  `json:"total_tokens"`
	RootExecutionMillis       int64  `json:"root_execution_millis"`
	SpecialistExecutionMillis int64  `json:"specialist_execution_millis"`
	ReadOnlyFanoutMillis      int64  `json:"read_only_fanout_millis"`
	TotalExecutionMillis      int64  `json:"total_execution_millis"`
}

func (u RunAgentUsage) Validate() error {
	if strings.TrimSpace(u.RunID) == "" {
		return errors.New("run Agent usage requires a Run id")
	}
	values := []int64{
		u.RootTokens, u.SpecialistTokens, u.ReadOnlyFanoutTokens, u.TotalTokens,
		u.RootExecutionMillis, u.SpecialistExecutionMillis, u.ReadOnlyFanoutMillis,
		u.TotalExecutionMillis, u.ContextCompactionTokens, u.ContextCompactionMillis,
	}
	for _, value := range values {
		if value < 0 {
			return errors.New("run Agent usage counters cannot be negative")
		}
	}
	if u.RootTokens > math.MaxInt64-u.SpecialistTokens ||
		u.RootTokens+u.SpecialistTokens > math.MaxInt64-u.ReadOnlyFanoutTokens ||
		u.RootTokens+u.SpecialistTokens+u.ReadOnlyFanoutTokens > math.MaxInt64-u.ContextCompactionTokens ||
		u.TotalTokens != u.RootTokens+u.SpecialistTokens+u.ReadOnlyFanoutTokens+u.ContextCompactionTokens {
		return errors.New("run Agent token total is inconsistent")
	}
	if u.RootExecutionMillis > math.MaxInt64-u.SpecialistExecutionMillis ||
		u.RootExecutionMillis+u.SpecialistExecutionMillis >
			math.MaxInt64-u.ReadOnlyFanoutMillis ||
		u.RootExecutionMillis+u.SpecialistExecutionMillis+u.ReadOnlyFanoutMillis > math.MaxInt64-u.ContextCompactionMillis ||
		u.TotalExecutionMillis != u.RootExecutionMillis+u.SpecialistExecutionMillis+
			u.ReadOnlyFanoutMillis+u.ContextCompactionMillis {
		return errors.New("run Agent execution total is inconsistent")
	}
	return nil
}
