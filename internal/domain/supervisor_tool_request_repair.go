package domain

import (
	"errors"
	"strconv"
	"strings"
)

// SupervisorToolRequestRepairReasonPrefix is set by Go only after rejecting a
// whole tool batch during pre-execution preparation. It does not assert that
// earlier batches in the turn had no effects, or grant authority to new tools.
const SupervisorToolRequestRepairReasonPrefix = "tool_request_rejected_before_execution: no tools in this batch were executed; "

func IsSupervisorToolRequestRepair(reason string) bool {
	_, ok := SupervisorToolRequestRepairRound(reason)
	return ok
}

func NewSupervisorToolRequestRepairReason(toolRound int, reason string) (string, error) {
	if toolRound < 0 || toolRound >= MaxSupervisorExecutionRounds || strings.TrimSpace(reason) == "" {
		return "", errors.New("tool request repair requires an available tool round and a rejection reason")
	}
	return SupervisorToolRequestRepairReasonPrefix + "round=" + strconv.Itoa(toolRound) + "; " + reason, nil
}

func SupervisorToolRequestRepairRound(reason string) (int, bool) {
	if !strings.HasPrefix(reason, SupervisorToolRequestRepairReasonPrefix+"round=") {
		return 0, false
	}
	raw, cause, ok := strings.Cut(strings.TrimPrefix(reason, SupervisorToolRequestRepairReasonPrefix+"round="), "; ")
	if !ok || strings.TrimSpace(cause) == "" {
		return 0, false
	}
	round, err := strconv.Atoi(raw)
	return round, err == nil && round >= 0 && round < MaxSupervisorExecutionRounds && strconv.Itoa(round) == raw
}
