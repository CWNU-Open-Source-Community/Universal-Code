package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/workspaceidentity"
)

const RunTestsTool ToolName = "run_tests"
const RunTestsVersion = "run-tests.v1"

// RunTestsInput selects a registered project test; executable, argv, network,
// environment and credentials are exclusively prepared by the host.
type RunTestsInput struct {
	Version             string  `json:"version"`
	Action              string  `json:"action,omitempty"`
	Target              string  `json:"target,omitempty"`
	TimeoutMilliseconds int64   `json:"timeout_milliseconds,omitempty"`
	JobID               string  `json:"job_id,omitempty"`
	Cursor              *uint64 `json:"cursor,omitempty"`
	MaxBytes            *int    `json:"max_bytes,omitempty"`
	WaitMilliseconds    *int    `json:"wait_milliseconds,omitempty"`
}

// Omitted action retains the exact durable encoding of run-tests.v1 calls.
func (i RunTestsInput) EffectiveAction() string {
	if i.Action == "" {
		return CommandRuntimeActionRun
	}
	return i.Action
}

func NormalizeRunTestsPayload(raw json.RawMessage) (RunTestsInput, json.RawMessage, error) {
	i, err := decodeStructuredPayload[RunTestsInput](raw)
	if err != nil {
		return i, nil, err
	}
	if i.Version != RunTestsVersion {
		return i, nil, errors.New("run_tests version is invalid")
	}
	switch i.EffectiveAction() {
	case CommandRuntimeActionRun, CommandRuntimeActionStart:
		maximum := int64(MaxCommandRuntimeForegroundMillis)
		if i.EffectiveAction() == CommandRuntimeActionStart {
			maximum = runner.MaxCommandRuntimeTimeout.Milliseconds()
		}
		if i.Target == "" || strings.TrimSpace(i.Target) != i.Target || i.TimeoutMilliseconds < 1 || i.TimeoutMilliseconds > maximum ||
			i.JobID != "" || i.Cursor != nil || i.MaxBytes != nil || i.WaitMilliseconds != nil {
			return i, nil, errors.New("run_tests run/start requires a relative target and bounded timeout; use action=start for tests longer than 25000 milliseconds")
		}
		if err := (workspaceidentity.PathPolicy{}).Check(i.Target); err != nil {
			return i, nil, err
		}
	case CommandRuntimeActionWait:
		if i.Target != "" || i.TimeoutMilliseconds != 0 {
			return i, nil, errors.New("run_tests wait observes the original job_id without a new target or timeout")
		}
		if err := i.WaitInput().Validate(); err != nil {
			return i, nil, err
		}
	default:
		return i, nil, errors.New("run_tests action must be run, start or wait")
	}
	canonical, err := json.Marshal(i)
	return i, canonical, err
}

func (i RunTestsInput) WaitInput() CommandRuntimeInput {
	return CommandRuntimeInput{Version: CommandRuntimeToolProtocolVersion, Action: CommandRuntimeActionWait,
		JobID: i.JobID, Cursor: i.Cursor, MaxBytes: i.MaxBytes, WaitMilliseconds: i.WaitMilliseconds}
}

var runTestsDefinition = ToolDefinition{Name: RunTestsTool, Class: ClassProcess, Approval: ApprovalAutomatic,
	Description: "Prefer this tool for project tests. Run the registered or detected test in a relative directory with a bounded timeout and the current execution backend. For tests longer than 25000 milliseconds use action=start, then action=wait with the returned original job_id and cursor until terminal. Never repeat start to observe a Job. Tests execute project code and can write files and caches. Plan uses the same selected backend and approval rules as Deliver. Current command approval, network restrictions, Run budget and Job receipts apply.",
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["version"],"properties":{"version":{"type":"string","const":"run-tests.v1"},"action":{"type":"string","enum":["run","start","wait"]},"target":{"type":"string","minLength":1},"timeout_milliseconds":{"type":"integer","minimum":1,"maximum":1800000},"job_id":{"type":"string","minLength":1},"cursor":{"type":"integer","minimum":0},"max_bytes":{"type":"integer","minimum":4,"maximum":32768},"wait_milliseconds":{"type":"integer","minimum":1,"maximum":5000}},"anyOf":[{"required":["target","timeout_milliseconds"]},{"required":["action","job_id","cursor","max_bytes","wait_milliseconds"]}]}`),
}

type ProjectTestPreparer interface {
	PrepareProjectTest(context.Context, ToolCall, RunTestsInput) (CommandRuntimeInput, error)
}

func (g *Gateway) invokeRunTests(ctx context.Context, call ToolCall) (Outcome, error) {
	i, canonical, err := NormalizeRunTestsPayload(call.Payload)
	if err != nil {
		return Outcome{}, err
	}
	call.Payload = canonical
	preparer, ok := g.commandRuntime.(ProjectTestPreparer)
	if !ok {
		return Outcome{}, errors.New("project test runtime is unavailable")
	}
	input, err := preparer.PrepareProjectTest(ctx, call, i)
	if err != nil {
		return Outcome{}, err
	}
	return g.invokePreparedCommandRuntime(ctx, call, input)
}

func IsCommandExecutionTool(name ToolName) bool {
	return name == CommandRuntimeTool || name == RunTestsTool
}

// The durable public request stays small. Its host-prepared command is pinned
// in authority so approval, display and dispatch use precisely the same intent.
func CommandToolInput(name ToolName, raw json.RawMessage, a commandruntimeadapter.Authority) (CommandRuntimeInput, json.RawMessage, error) {
	if name == RunTestsTool {
		request, canonical, err := NormalizeRunTestsPayload(raw)
		if err != nil {
			return CommandRuntimeInput{}, nil, err
		}
		i, prepared, err := NormalizeCommandRuntimePayload(a.ProjectTestCommand)
		if err != nil || !bytes.Equal(prepared, a.ProjectTestCommand) || i.Action != request.EffectiveAction() ||
			(i.Action != CommandRuntimeActionWait && len(i.Commands) != 1) ||
			(i.Action == CommandRuntimeActionWait && !bytes.Equal(mustRunTestsWaitJSON(request), prepared)) {
			return i, nil, errors.New("project test command has no exact native preparation")
		}
		return i, canonical, nil
	}
	if name != CommandRuntimeTool || len(a.ProjectTestCommand) != 0 {
		return CommandRuntimeInput{}, nil, errors.New("command tool authority mismatch")
	}
	return NormalizeCommandRuntimePayload(raw)
}

func mustRunTestsWaitJSON(i RunTestsInput) []byte { raw, _ := json.Marshal(i.WaitInput()); return raw }
