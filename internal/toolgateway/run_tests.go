package toolgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/workspaceidentity"
)

const RunTestsTool ToolName = "run_tests"
const RunTestsVersion = "run-tests.v1"

// RunTestsInput selects a registered project test; executable, argv, network,
// environment and credentials are exclusively prepared by the host.
type RunTestsInput struct {
	Version             string `json:"version"`
	Target              string `json:"target"`
	TimeoutMilliseconds int64  `json:"timeout_milliseconds"`
}

func NormalizeRunTestsPayload(raw json.RawMessage) (RunTestsInput, json.RawMessage, error) {
	i, err := decodeStructuredPayload[RunTestsInput](raw)
	if err != nil {
		return i, nil, err
	}
	if i.Version != RunTestsVersion || i.Target == "" || strings.TrimSpace(i.Target) != i.Target ||
		i.TimeoutMilliseconds < 1 || i.TimeoutMilliseconds > MaxCommandRuntimeForegroundMillis {
		return i, nil, errors.New("run_tests requires a relative target and timeout from 1 to 25000 milliseconds")
	}
	if err := (workspaceidentity.PathPolicy{}).Check(i.Target); err != nil {
		return i, nil, err
	}
	canonical, err := json.Marshal(i)
	return i, canonical, err
}

var runTestsDefinition = ToolDefinition{Name: RunTestsTool, Class: ClassProcess, Approval: ApprovalAutomatic,
	Description: "Prefer this tool for project tests. Run the registered or detected test in a relative directory, with a bounded timeout and the current execution backend. Tests execute project code and can write files and caches. Plan uses the same selected backend and approval rules as Deliver. Current command approval, network restrictions, Run budget and Job receipts apply.",
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["version","target","timeout_milliseconds"],"properties":{"version":{"type":"string","const":"run-tests.v1"},"target":{"type":"string","minLength":1},"timeout_milliseconds":{"type":"integer","minimum":1,"maximum":25000}}}`),
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
		_, canonical, err := NormalizeRunTestsPayload(raw)
		if err != nil {
			return CommandRuntimeInput{}, nil, err
		}
		i, prepared, err := NormalizeCommandRuntimePayload(a.ProjectTestCommand)
		if err != nil || !bytes.Equal(prepared, a.ProjectTestCommand) || i.Action != CommandRuntimeActionRun || len(i.Commands) != 1 {
			return i, nil, errors.New("project test command has no exact native preparation")
		}
		return i, canonical, nil
	}
	if name != CommandRuntimeTool || len(a.ProjectTestCommand) != 0 {
		return CommandRuntimeInput{}, nil, errors.New("command tool authority mismatch")
	}
	return NormalizeCommandRuntimePayload(raw)
}
