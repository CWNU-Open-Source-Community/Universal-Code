package domain

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This marker describes text, not a rejected native tool batch. It grants no
// capabilities and never interprets or executes the apparent call arguments.
const SupervisorTextToolRepairReasonPrefix = "text_tool_call_not_executed: completed_round="

const supervisorTextToolRepairSuffix = "; trailing tool-call text is not execution; use native function calls or explicitly finish or wait"

func NewSupervisorTextToolRepairReason(round int) (string, error) {
	if round < 0 || round >= MaxSupervisorExecutionRounds {
		return "", errors.New("text tool-call correction requires an available tool round")
	}
	return SupervisorTextToolRepairReasonPrefix + strconv.Itoa(round) + supervisorTextToolRepairSuffix, nil
}

func SupervisorTextToolRepairRound(reason string) (int, bool) {
	if !strings.HasPrefix(reason, SupervisorTextToolRepairReasonPrefix) || !strings.HasSuffix(reason, supervisorTextToolRepairSuffix) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(reason, SupervisorTextToolRepairReasonPrefix), supervisorTextToolRepairSuffix)
	round, err := strconv.Atoi(raw)
	return round, err == nil && round >= 0 && round < MaxSupervisorExecutionRounds && strconv.Itoa(round) == raw
}

// RootActionHasTrailingToolCalls recognizes only a complete known provider
// envelope after a valid root object. Examples inside message/summary are not
// examined. No tool names or arguments are extracted from this text.
func RootActionHasTrailingToolCalls(raw string) bool {
	if len(raw) > 64*1024 || !utf8.ValidString(raw) {
		return false
	}
	raw = strings.TrimSpace(raw)
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	fields := make(map[string]json.RawMessage, 5)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		switch key {
		case "version", "action", "message", "summary", "reason":
		default:
			return false
		}
		if _, duplicate := fields[key]; duplicate {
			return false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return false
	}
	var action RootAction
	if json.Unmarshal([]byte(raw[:decoder.InputOffset()]), &action) != nil {
		return false
	}
	action.Version = strings.TrimSpace(action.Version)
	action.Message = strings.TrimSpace(action.Message)
	action.Summary = strings.TrimSpace(action.Summary)
	action.Reason = strings.TrimSpace(action.Reason)
	if action.Validate() != nil {
		return false
	}
	tail := strings.TrimSpace(raw[decoder.InputOffset():])
	for _, envelope := range []struct{ open, close, invoke, endInvoke string }{
		{"<｜｜DSML｜｜ calls>", "</｜｜DSML｜｜ calls>", "<｜｜DSML｜｜ invoke name=\"", "</｜｜DSML｜｜ invoke>"},
		{"<｜DSML｜function_calls>", "</｜DSML｜function_calls>", "<｜DSML｜invoke name=\"", "</｜DSML｜invoke>"},
	} {
		if strings.HasPrefix(tail, envelope.open) && strings.HasSuffix(tail, envelope.close) &&
			strings.Contains(tail, envelope.invoke) && strings.Contains(tail, envelope.endInvoke) {
			return true
		}
	}
	return false
}
