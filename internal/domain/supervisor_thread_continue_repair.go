package domain

import (
	"errors"
	"strconv"
	"strings"
)

// This Go marker concerns a tool-free continue after completed tool work. It
// does not claim the earlier tools had no effects and grants no new capability.
const SupervisorThreadContinueRepairReasonPrefix = "thread_continue_requires_next_action: completed_round="

const supervisorThreadContinueRepairSuffix = "; use an offered next tool, finish this reply, or wait for required external input"

func NewSupervisorThreadContinueRepairReason(round int) (string, error) {
	if round < 1 || round >= MaxSupervisorExecutionRounds {
		return "", errors.New("Thread continuation correction requires a completed non-boundary tool round")
	}
	return SupervisorThreadContinueRepairReasonPrefix + strconv.Itoa(round) + supervisorThreadContinueRepairSuffix, nil
}

func SupervisorThreadContinueRepairRound(reason string) (int, bool) {
	if !strings.HasPrefix(reason, SupervisorThreadContinueRepairReasonPrefix) || !strings.HasSuffix(reason, supervisorThreadContinueRepairSuffix) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(reason, SupervisorThreadContinueRepairReasonPrefix), supervisorThreadContinueRepairSuffix)
	round, err := strconv.Atoi(raw)
	return round, err == nil && round >= 1 && round < MaxSupervisorExecutionRounds && strconv.Itoa(round) == raw
}
